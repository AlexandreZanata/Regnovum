// Package backpressure protects the process from slow or failing external
// dependencies. It is deliberately provider-neutral: adapters supply the
// operation, while this package owns concurrency, deadlines and retry policy.
package backpressure

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
)

var (
	ErrSaturated     = errors.New("backpressure: dependency capacity exhausted")
	ErrCircuitOpen   = errors.New("backpressure: dependency circuit is open")
	ErrInvalidConfig = errors.New("backpressure: invalid configuration")
)

type State uint8

const (
	Closed State = iota
	Open
	HalfOpen
)

type Clock func() time.Time

type Random interface {
	Int63n(int64) int64
}

type Operation func(context.Context) error

type Config struct {
	MaxInFlight      int
	AcquireTimeout   time.Duration
	OperationTimeout time.Duration
	MaxAttempts      int
	BaseBackoff      time.Duration
	MaxBackoff       time.Duration
	FailureThreshold int
	OpenDuration     time.Duration
	Now              Clock
	Random           Random
}

// DefaultConfig is the package's starting point. The clock is the system clock
// through the package that owns the effect, and the randomness source is left
// for the caller because it is required: a caller who wants a reproducible run
// replaces the clock too, and the sources for that are in
// internal/platform/testsource.
func DefaultConfig() Config {
	return Config{MaxInFlight: 16, AcquireTimeout: 250 * time.Millisecond, OperationTimeout: 3 * time.Second, MaxAttempts: 3, BaseBackoff: 50 * time.Millisecond, MaxBackoff: 500 * time.Millisecond, FailureThreshold: 5, OpenDuration: 5 * time.Second, Now: clockseed.SystemClockNow}
}

func (c Config) Validate() error {
	if c.MaxInFlight < 1 || c.AcquireTimeout <= 0 || c.OperationTimeout <= 0 || c.MaxAttempts < 1 || c.BaseBackoff <= 0 || c.MaxBackoff < c.BaseBackoff || c.FailureThreshold < 1 || c.OpenDuration <= 0 {
		return ErrInvalidConfig
	}
	return nil
}

type Circuit struct {
	mu            sync.Mutex
	state         State
	failures      int
	openedAt      time.Time
	probeInFlight bool
	threshold     int
	openDuration  time.Duration
	now           Clock
}

func NewCircuit(threshold int, openDuration time.Duration, now Clock) (*Circuit, error) {
	if threshold < 1 || openDuration <= 0 {
		return nil, ErrInvalidConfig
	}
	if now == nil {
		now = clockseed.SystemClockNow
	}
	return &Circuit{threshold: threshold, openDuration: openDuration, now: now}, nil
}

func (c *Circuit) State() State { c.mu.Lock(); defer c.mu.Unlock(); return c.state }

func (c *Circuit) allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == Closed {
		return true
	}
	if c.state == Open && c.now().Sub(c.openedAt) >= c.openDuration {
		c.state = HalfOpen
	}
	if c.state != HalfOpen || c.probeInFlight {
		return false
	}
	c.probeInFlight = true
	return true
}

func (c *Circuit) success() {
	c.mu.Lock()
	c.state, c.failures, c.probeInFlight = Closed, 0, false
	c.mu.Unlock()
}

func (c *Circuit) failure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probeInFlight = false
	c.failures++
	if c.failures >= c.threshold {
		c.state, c.openedAt = Open, c.now()
	}
}

// Runner executes one dependency operation under all safety controls. A nil
// operation is rejected; callers decide whether a failure is safe to degrade.
type Runner struct {
	cfg     Config
	circuit *Circuit
	slots   chan struct{}
	rngMu   sync.Mutex
}

func New(cfg Config) (*Runner, error) {
	if cfg.Now == nil {
		cfg.Now = clockseed.SystemClockNow
	}
	if cfg.Random == nil {
		return nil, ErrInvalidConfig
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	circuit, err := NewCircuit(cfg.FailureThreshold, cfg.OpenDuration, cfg.Now)
	if err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg, circuit: circuit, slots: make(chan struct{}, cfg.MaxInFlight)}, nil
}

func (r *Runner) CircuitState() State { return r.circuit.State() }

// RunWithFallback makes graceful degradation explicit at the call site. The
// fallback is used only when this process cannot safely start the dependency
// call (capacity or circuit refusal); provider errors are returned unchanged so
// a caller cannot accidentally hide a failed side effect.
func (r *Runner) RunWithFallback(ctx context.Context, operation Operation, fallback Operation) (degraded bool, err error) {
	err = r.Run(ctx, operation)
	if !errors.Is(err, ErrSaturated) && !errors.Is(err, ErrCircuitOpen) {
		return false, err
	}
	if fallback == nil {
		return true, err
	}
	return true, fallback(ctx)
}

// Run returns the dependency result or a classified local refusal. Only
// transient operation failures are retried; cancellation and context deadline
// are never retried, preventing shutdown and request cancellation from being
// amplified into provider traffic.
func (r *Runner) Run(ctx context.Context, operation Operation) error {
	if r == nil || operation == nil {
		return ErrInvalidConfig
	}
	if err := r.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-r.slots }()
	if !r.circuit.allow() {
		return ErrCircuitOpen
	}
	for attempt := 1; attempt <= r.cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			r.circuit.failure()
			return err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, r.cfg.OperationTimeout)
		err := operation(attemptCtx)
		cancel()
		if err == nil {
			r.circuit.success()
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			r.circuit.failure()
			return err
		}
		if attempt == r.cfg.MaxAttempts {
			r.circuit.failure()
			return err
		}
		if err := sleep(ctx, r.backoff(attempt)); err != nil {
			r.circuit.failure()
			return err
		}
	}
	return ErrInvalidConfig
}

func (r *Runner) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	acquireCtx, cancel := context.WithTimeout(ctx, r.cfg.AcquireTimeout)
	defer cancel()
	select {
	case r.slots <- struct{}{}:
		return nil
	case <-acquireCtx.Done():
		return ErrSaturated
	}
}

func (r *Runner) backoff(attempt int) time.Duration {
	cap := r.cfg.BaseBackoff
	for i := 1; i < attempt && cap < r.cfg.MaxBackoff; i++ {
		if cap > r.cfg.MaxBackoff/2 {
			cap = r.cfg.MaxBackoff
			break
		}
		cap *= 2
	}
	r.rngMu.Lock()
	n := r.cfg.Random.Int63n(int64(cap) + 1)
	r.rngMu.Unlock()
	return time.Duration(n)
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
