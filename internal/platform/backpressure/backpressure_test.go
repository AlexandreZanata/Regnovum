package backpressure_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/backpressure"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

type zeroRandom struct{}

func (zeroRandom) Int63n(int64) int64 { return 0 }

func config() backpressure.Config {
	c := backpressure.DefaultConfig()
	c.Random = zeroRandom{}
	c.MaxInFlight = 1
	c.AcquireTimeout = 20 * time.Millisecond
	c.OperationTimeout = 100 * time.Millisecond
	c.MaxAttempts = 3
	c.BaseBackoff, c.MaxBackoff = time.Millisecond, 2*time.Millisecond
	c.FailureThreshold, c.OpenDuration = 2, 20*time.Millisecond
	return c
}

func TestRunnerBackpressuresConcurrentCalls(t *testing.T) {
	runner, err := backpressure.New(config())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- runner.Run(context.Background(), func(context.Context) error { close(started); <-release; return nil })
	}()
	<-started
	if err := runner.Run(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, backpressure.ErrSaturated) {
		t.Fatalf("second call = %v, want saturation", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestRunnerWithFallbackOnlyDegradesLocalRefusals(t *testing.T) {
	runner, err := backpressure.New(config())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = runner.RunWithFallback(context.Background(), func(context.Context) error { close(started); <-release; return nil }, nil)
	}()
	<-started
	called := false
	degraded, err := runner.RunWithFallback(context.Background(), func(context.Context) error { return nil }, func(context.Context) error { called = true; return nil })
	if err != nil || !degraded || !called {
		t.Fatalf("fallback result = degraded:%v err:%v called:%v", degraded, err, called)
	}
	close(release)
}

func TestRunnerRetriesTransientOperationAndSucceeds(t *testing.T) {
	c := config()
	c.FailureThreshold = 5
	runner, err := backpressure.New(c)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	want := errors.New("temporary")
	err = runner.Run(context.Background(), func(context.Context) error {
		if calls.Add(1) < 3 {
			return want
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
	if runner.CircuitState() != backpressure.Closed {
		t.Fatal("successful recovery must close the circuit")
	}
}

func TestRunnerOpensCircuitAfterFailuresAndRecovers(t *testing.T) {
	c := config()
	c.MaxAttempts = 1
	// The clock is the test's and not the system's: the open window is crossed by
	// advancing it, because a pause would be the test hoping that the instant it
	// asserts about has passed instead of making it pass.
	clock := testsource.NewClock(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	c.Now = clock.Now
	runner, err := backpressure.New(c)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("provider down")
	for i := 0; i < 2; i++ {
		if err := runner.Run(context.Background(), func(context.Context) error { return failure }); !errors.Is(err, failure) {
			t.Fatalf("failure = %v", err)
		}
	}
	if runner.CircuitState() != backpressure.Open {
		t.Fatal("circuit did not open")
	}
	if err := runner.Run(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, backpressure.ErrCircuitOpen) {
		t.Fatalf("open call = %v", err)
	}
	clock.Advance(c.OpenDuration + time.Millisecond)
	if err := runner.Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("half-open probe = %v", err)
	}
	if runner.CircuitState() != backpressure.Closed {
		t.Fatal("successful probe did not close circuit")
	}
}

func TestRunnerDoesNotRetryCancelledContext(t *testing.T) {
	runner, err := backpressure.New(config())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	err = runner.Run(ctx, func(context.Context) error { calls.Add(1); return errors.New("failure") })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d, an already-cancelled request must not reach the provider", calls.Load())
	}
}

func TestRunnerOperationTimeoutIsBounded(t *testing.T) {
	c := config()
	c.OperationTimeout = 10 * time.Millisecond
	c.MaxAttempts = 1
	runner, err := backpressure.New(c)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = runner.Run(context.Background(), func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("operation timeout was not bounded")
	}
}

func TestRunnerIsSafeForConcurrentUse(t *testing.T) {
	c := config()
	c.MaxInFlight = 8
	c.AcquireTimeout = time.Second
	c.MaxAttempts = 1
	runner, err := backpressure.New(c)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = runner.Run(context.Background(), func(context.Context) error { return nil })
		}()
	}
	wg.Wait()
}
