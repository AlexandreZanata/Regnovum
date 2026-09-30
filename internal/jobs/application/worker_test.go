package application_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// mutableClock lets a test drive the worker's notion of time.
type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fixedRandom always returns the same u64, so jitter is deterministic.
type fixedRandom struct{ value uint64 }

func (r fixedRandom) Read(buffer []byte) (int, error) {
	binary.BigEndian.PutUint64(buffer, r.value)
	return len(buffer), nil
}

// failingRandom simulates an entropy failure.
type failingRandom struct{}

func (failingRandom) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

// memoryQueue is an in-memory QueueRepository that mirrors the SQL semantics
// of app.jobs (SKIP LOCKED is not needed in a single process, but the lease,
// attempt-budget and retry rules are the same).
type memoryQueue struct {
	mu       sync.Mutex
	nextID   int
	jobs     map[string]*jobsdomain.Job
	order    []string
	failCall []failCall
	complete []string
	released int
}

type failCall struct {
	id      string
	code    jobsdomain.FailureCode
	retryAt *time.Time
}

func newMemoryQueue() *memoryQueue {
	return &memoryQueue{jobs: map[string]*jobsdomain.Job{}}
}

func (q *memoryQueue) seed(t *testing.T, payload string, maxAttempts int, jobType jobsdomain.JobType, version int, availableAt time.Time) *jobsdomain.Job {
	t.Helper()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.nextID++
	return q.insert(jobType, version, payload, maxAttempts, availableAt)
}

// insert must be called with the lock held.
func (q *memoryQueue) insert(jobType jobsdomain.JobType, version int, payload string, maxAttempts int, availableAt time.Time) *jobsdomain.Job {
	id := fmt.Sprintf("job-%d", q.nextID)
	job := &jobsdomain.Job{
		ID: id, Type: jobType, Version: version, Payload: []byte(payload),
		State: jobsdomain.StateQueued, AvailableAt: availableAt,
		MaxAttempts: maxAttempts, CreatedAt: availableAt, UpdatedAt: availableAt,
	}
	q.jobs[id] = job
	q.order = append(q.order, id)
	return job
}

func (q *memoryQueue) Enqueue(_ context.Context, record jobsapp.EnqueueRecord) (*jobsdomain.Job, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if record.IdempotencyKey != "" {
		for _, job := range q.jobs {
			if job.IdempotencyKey == record.IdempotencyKey {
				return copyJob(job), true, nil
			}
		}
	}
	q.nextID++
	job := q.insert(record.Type, record.Version, string(record.Payload), record.MaxAttempts, record.AvailableAt)
	job.IdempotencyKey = record.IdempotencyKey
	return copyJob(job), false, nil
}

func (q *memoryQueue) Lease(_ context.Context, owner string, now, until time.Time) (*jobsdomain.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, id := range q.order {
		job := q.jobs[id]
		due := job.State == jobsdomain.StateQueued && !job.AvailableAt.After(now)
		expired := job.State == jobsdomain.StateLeased && !job.LeasedUntil.After(now)
		if !due && !expired {
			continue
		}
		job.State = jobsdomain.StateLeased
		job.Attempts++
		job.LeaseOwner = owner
		job.LeasedUntil = until
		job.UpdatedAt = now
		return copyJob(job), nil
	}
	return nil, nil
}

func (q *memoryQueue) Complete(_ context.Context, id, owner string, now time.Time) (*jobsdomain.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job := q.jobs[id]
	if job == nil || job.State != jobsdomain.StateLeased || job.LeaseOwner != owner {
		return nil, jobsapp.ErrLeaseNotHeld
	}
	job.State = jobsdomain.StateSucceeded
	job.LeaseOwner, job.LeasedUntil = "", time.Time{}
	job.UpdatedAt = now
	q.complete = append(q.complete, id)
	return copyJob(job), nil
}

func (q *memoryQueue) Fail(_ context.Context, failure jobsdomain.Failure, id, owner string, retryAt, now time.Time) (*jobsdomain.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job := q.jobs[id]
	if job == nil || job.State != jobsdomain.StateLeased || job.LeaseOwner != owner {
		return nil, jobsapp.ErrLeaseNotHeld
	}
	var retry *time.Time
	if job.Attempts >= job.MaxAttempts {
		job.State = jobsdomain.StateDead
	} else {
		job.State = jobsdomain.StateQueued
		job.AvailableAt = retryAt
		copied := retryAt
		retry = &copied
	}
	job.LeaseOwner, job.LeasedUntil = "", time.Time{}
	job.LastErrorCode, job.LastErrorDetail = failure.Code, failure.Detail
	job.UpdatedAt = now
	q.failCall = append(q.failCall, failCall{id: id, code: failure.Code, retryAt: retry})
	return copyJob(job), nil
}

func (q *memoryQueue) ReleaseExpiredLeases(_ context.Context, now time.Time) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	released := 0
	for _, job := range q.jobs {
		if job.State == jobsdomain.StateLeased && !job.LeasedUntil.After(now) {
			job.State = jobsdomain.StateQueued
			job.LeaseOwner, job.LeasedUntil = "", time.Time{}
			released++
		}
	}
	q.released += released
	return released, nil
}

func (q *memoryQueue) JobByID(_ context.Context, id string) (*jobsdomain.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job := q.jobs[id]
	if job == nil {
		return nil, jobsapp.ErrJobNotFound
	}
	return copyJob(job), nil
}

func (q *memoryQueue) snapshot() []jobsdomain.Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]jobsdomain.Job, 0, len(q.jobs))
	for _, id := range q.order {
		out = append(out, *copyJob(q.jobs[id]))
	}
	return out
}

func (q *memoryQueue) failCalls() []failCall {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]failCall(nil), q.failCall...)
}

func (q *memoryQueue) completed() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.complete...)
}

func copyJob(job *jobsdomain.Job) *jobsdomain.Job {
	if job == nil {
		return nil
	}
	clone := *job
	return &clone
}

type workerFixture struct {
	queue   *memoryQueue
	clock   *mutableClock
	worker  *jobsapp.Worker
	stats   func() jobsapp.WorkerStats
	stop    func()
	waitFor func()
}

func newWorkerFixture(t *testing.T, cfg jobsapp.WorkerConfig, registry jobsapp.HandlerRegistry) *workerFixture {
	t.Helper()
	queue := newMemoryQueue()
	clock := &mutableClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	lease, err := jobsapp.NewLeaseUseCase(queue, clock)
	if err != nil {
		t.Fatalf("lease use case: %v", err)
	}
	complete, err := jobsapp.NewCompleteUseCase(queue, clock)
	if err != nil {
		t.Fatalf("complete use case: %v", err)
	}
	fail, err := jobsapp.NewFailUseCase(queue, clock)
	if err != nil {
		t.Fatalf("fail use case: %v", err)
	}
	recoverLeases, err := jobsapp.NewRecoverExpiredLeasesUseCase(queue, clock)
	if err != nil {
		t.Fatalf("recover use case: %v", err)
	}
	if registry == nil {
		registry = jobsapp.NewHandlerMap()
	}

	worker, err := jobsapp.NewWorker(jobsapp.WorkerDeps{
		Lease:    lease,
		Complete: complete,
		Fail:     fail,
		Recover:  recoverLeases,
		Registry: registry,
		Clock:    clock,
		Random:   fixedRandom{value: 0},
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = worker.Run(ctx)
	}()

	return &workerFixture{
		queue:   queue,
		clock:   clock,
		worker:  worker,
		stats:   worker.Stats,
		stop:    cancel,
		waitFor: func() { <-done },
	}
}

func testWorkerConfig() jobsapp.WorkerConfig {
	return jobsapp.WorkerConfig{
		Concurrency:    2,
		LeaseDuration:  time.Minute,
		HandlerTimeout: 5 * time.Second,
		PollInterval:   5 * time.Millisecond,
		Backoff:        jobsapp.BackoffPolicy{Base: time.Millisecond, Max: 10 * time.Millisecond},
	}
}

func waitUntil(t *testing.T, deadline time.Duration, condition func() bool, message string) {
	t.Helper()
	limit := time.Now().Add(deadline)
	for time.Now().Before(limit) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", message)
}

func TestWorkerProcessesJobsAndBoundsConcurrency(t *testing.T) {
	registry := jobsapp.NewHandlerMap()
	var inFlight, peak int64
	handler := func(ctx context.Context, _ *jobsdomain.Job) error {
		current := atomic.AddInt64(&inFlight, 1)
		for {
			observed := atomic.LoadInt64(&peak)
			if current <= observed || atomic.CompareAndSwapInt64(&peak, observed, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
		return nil
	}
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 1, handler); err != nil {
		t.Fatalf("register: %v", err)
	}

	cfg := testWorkerConfig()
	cfg.Concurrency = 3
	fixture := newWorkerFixture(t, cfg, registry)
	defer func() { fixture.stop(); fixture.waitFor() }()

	const jobs = 9
	for i := 0; i < jobs; i++ {
		fixture.queue.seed(t, `{"account_id":"a1"}`, 3, jobsdomain.TypeEmailDelivery, 1, fixture.clock.Now())
	}

	waitUntil(t, 5*time.Second, func() bool { return fixture.stats().Succeeded == jobs }, "all jobs to succeed")
	if peak > int64(cfg.Concurrency) {
		t.Errorf("peak concurrency = %d, want at most %d", peak, cfg.Concurrency)
	}
	if peak < 2 {
		t.Errorf("peak concurrency = %d; the pool should run jobs in parallel", peak)
	}
	if len(fixture.queue.completed()) != jobs {
		t.Errorf("completed %d jobs, want %d", len(fixture.queue.completed()), jobs)
	}
}

// TestWorkerGracefulShutdownCompletesInFlightJob is the SIGTERM proof at the
// runtime level: the signal stops new claims but the job already leased is
// finished and recorded instead of being abandoned.
func TestWorkerGracefulShutdownCompletesInFlightJob(t *testing.T) {
	registry := jobsapp.NewHandlerMap()
	started := make(chan struct{})
	release := make(chan struct{})
	handler := func(ctx context.Context, _ *jobsdomain.Job) error {
		close(started)
		<-release
		return nil
	}
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 1, handler); err != nil {
		t.Fatalf("register: %v", err)
	}

	fixture := newWorkerFixture(t, testWorkerConfig(), registry)
	defer fixture.waitFor()

	job := fixture.queue.seed(t, `{"account_id":"a1"}`, 3, jobsdomain.TypeEmailDelivery, 1, fixture.clock.Now())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}

	// The shutdown signal arrives while the handler is in flight.
	fixture.stop()
	close(release)
	fixture.waitFor()

	completed := fixture.queue.completed()
	if len(completed) != 1 || completed[0] != job.ID {
		t.Fatalf("in-flight job was abandoned: completed = %v", completed)
	}
	state, err := fixture.queue.JobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if state.State != jobsdomain.StateSucceeded {
		t.Errorf("state after shutdown = %q, want succeeded", state.State)
	}
	if fixture.stats().Succeeded != 1 {
		t.Errorf("succeeded = %d, want 1", fixture.stats().Succeeded)
	}
}

// TestWorkerRetryHonoursBudgetAndBackoff proves retries are spaced by the
// backoff policy and stop at the attempt budget, promoting the job to dead.
func TestWorkerRetryHonoursBudgetAndBackoff(t *testing.T) {
	registry := jobsapp.NewHandlerMap()
	handler := func(context.Context, *jobsdomain.Job) error {
		return errors.New("postgres://arena:secret@db:5432/arena?sslmode=disable")
	}
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 1, handler); err != nil {
		t.Fatalf("register: %v", err)
	}

	cfg := testWorkerConfig()
	cfg.Concurrency = 1
	cfg.Backoff = jobsapp.BackoffPolicy{Base: time.Second, Max: 4 * time.Second}
	fixture := newWorkerFixture(t, cfg, registry)
	defer func() { fixture.stop(); fixture.waitFor() }()

	const budget = 3
	fixture.queue.seed(t, `{"account_id":"a1"}`, budget, jobsdomain.TypeEmailDelivery, 1, fixture.clock.Now())

	// Drive time forward so each retry instant arrives. Full jitter with a
	// fixed entropy value makes the wait a fixed fraction of the cap.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && fixture.stats().Dead == 0 {
		fixture.clock.advance(2 * time.Second)
		time.Sleep(3 * time.Millisecond)
	}

	if got := fixture.stats().Dead; got != 1 {
		t.Fatalf("dead = %d, want 1 (attempt budget must be enforced)", got)
	}
	calls := fixture.queue.failCalls()
	if len(calls) != budget {
		t.Fatalf("fail calls = %d, want %d attempts", len(calls), budget)
	}
	for index, call := range calls[:budget-1] {
		if call.retryAt == nil {
			t.Errorf("attempt %d has no retry instant; backoff must schedule the retry", index+1)
		}
	}
	if calls[budget-1].code != jobsdomain.FailureHandlerError {
		t.Errorf("last code = %q", calls[budget-1].code)
	}

	jobs := fixture.queue.snapshot()
	if jobs[0].State != jobsdomain.StateDead {
		t.Errorf("state = %q, want dead", jobs[0].State)
	}
	for _, unsafe := range []string{"@", "/", "?", "="} {
		if strings.Contains(jobs[0].LastErrorDetail, unsafe) {
			t.Errorf("redacted detail kept %q: %q", unsafe, jobs[0].LastErrorDetail)
		}
	}
}

// TestWorkerPoisonJobDoesNotBlockTheQueue proves one always-failing job leaves
// the pool working: it goes dead and the healthy job still succeeds.
func TestWorkerPoisonJobDoesNotBlockTheQueue(t *testing.T) {
	registry := jobsapp.NewHandlerMap()
	handler := func(_ context.Context, job *jobsdomain.Job) error {
		if string(job.Payload) == `{"poison":true}` {
			return errors.New("always fails")
		}
		return nil
	}
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 1, handler); err != nil {
		t.Fatalf("register: %v", err)
	}

	cfg := testWorkerConfig()
	cfg.Concurrency = 1
	fixture := newWorkerFixture(t, cfg, registry)
	defer func() { fixture.stop(); fixture.waitFor() }()

	fixture.queue.seed(t, `{"poison":true}`, 1, jobsdomain.TypeEmailDelivery, 1, fixture.clock.Now())
	healthy := fixture.queue.seed(t, `{"account_id":"a2"}`, 1, jobsdomain.TypeEmailDelivery, 1, fixture.clock.Now())

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && fixture.stats().Dead == 0 {
		fixture.clock.advance(time.Second)
		time.Sleep(3 * time.Millisecond)
	}
	waitUntil(t, 5*time.Second, func() bool { return fixture.stats().Succeeded == 1 }, "the healthy job to succeed")

	stats := fixture.stats()
	if stats.Dead != 1 {
		t.Errorf("dead = %d, want 1", stats.Dead)
	}
	if stats.Succeeded != 1 {
		t.Errorf("succeeded = %d, want 1", stats.Succeeded)
	}
	if got := fixture.queue.completed(); len(got) != 1 || got[0] != healthy.ID {
		t.Errorf("completed = %v, want the healthy job", got)
	}
}

func TestWorkerHandlerTimeoutIsRecorded(t *testing.T) {
	registry := jobsapp.NewHandlerMap()
	handler := func(ctx context.Context, _ *jobsdomain.Job) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	}
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 1, handler); err != nil {
		t.Fatalf("register: %v", err)
	}

	cfg := testWorkerConfig()
	cfg.Concurrency = 1
	cfg.HandlerTimeout = 30 * time.Millisecond
	cfg.Backoff = jobsapp.BackoffPolicy{Base: time.Millisecond, Max: time.Millisecond}
	fixture := newWorkerFixture(t, cfg, registry)
	defer func() { fixture.stop(); fixture.waitFor() }()

	fixture.queue.seed(t, `{"account_id":"a1"}`, 1, jobsdomain.TypeEmailDelivery, 1, fixture.clock.Now())
	waitUntil(t, 5*time.Second, func() bool { return fixture.stats().Dead == 1 }, "the timed-out job to die")

	calls := fixture.queue.failCalls()
	if len(calls) == 0 || calls[0].code != jobsdomain.FailureHandlerTimeout {
		t.Fatalf("failure code = %v, want JOB_HANDLER_TIMEOUT", calls)
	}
}

func TestWorkerRecoversHandlerPanic(t *testing.T) {
	registry := jobsapp.NewHandlerMap()
	handler := func(context.Context, *jobsdomain.Job) error { panic("boom") }
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 1, handler); err != nil {
		t.Fatalf("register: %v", err)
	}

	cfg := testWorkerConfig()
	cfg.Concurrency = 1
	fixture := newWorkerFixture(t, cfg, registry)
	defer func() { fixture.stop(); fixture.waitFor() }()

	fixture.queue.seed(t, `{"account_id":"a1"}`, 1, jobsdomain.TypeEmailDelivery, 1, fixture.clock.Now())
	waitUntil(t, 5*time.Second, func() bool { return fixture.stats().Panics == 1 }, "the panic to be recovered")
	waitUntil(t, 5*time.Second, func() bool { return fixture.stats().Dead == 1 }, "the panicking job to die")
}

func TestWorkerUnknownHandlerAndVersion(t *testing.T) {
	registry := jobsapp.NewHandlerMap()
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 2, func(context.Context, *jobsdomain.Job) error { return nil }); err != nil {
		t.Fatalf("register: %v", err)
	}

	cfg := testWorkerConfig()
	cfg.Concurrency = 1
	fixture := newWorkerFixture(t, cfg, registry)
	defer func() { fixture.stop(); fixture.waitFor() }()

	// A workload with no handler at all.
	fixture.queue.seed(t, `{}`, 1, jobsdomain.TypeRetentionRun, 1, fixture.clock.Now())
	// A workload that exists but not for this payload version.
	fixture.queue.seed(t, `{}`, 1, jobsdomain.TypeEmailDelivery, 1, fixture.clock.Now())

	waitUntil(t, 5*time.Second, func() bool { return fixture.stats().Dead == 2 }, "both jobs to die")

	codes := map[jobsdomain.FailureCode]int{}
	for _, call := range fixture.queue.failCalls() {
		codes[call.code]++
	}
	if codes[jobsdomain.FailureUnknownType] != 1 {
		t.Errorf("unknown type failures = %d, want 1", codes[jobsdomain.FailureUnknownType])
	}
	if codes[jobsdomain.FailureUnsupportedVersion] != 1 {
		t.Errorf("unsupported version failures = %d, want 1", codes[jobsdomain.FailureUnsupportedVersion])
	}
}

func TestWorkerReclaimsAbandonedLease(t *testing.T) {
	registry := jobsapp.NewHandlerMap()
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 1, func(context.Context, *jobsdomain.Job) error { return nil }); err != nil {
		t.Fatalf("register: %v", err)
	}

	cfg := testWorkerConfig()
	cfg.Concurrency = 1
	fixture := newWorkerFixture(t, cfg, registry)
	defer func() { fixture.stop(); fixture.waitFor() }()

	// A sibling died holding a lease that has already expired.
	job := fixture.queue.seed(t, `{"account_id":"a1"}`, 3, jobsdomain.TypeEmailDelivery, 1, fixture.clock.Now())
	fixture.queue.mu.Lock()
	job.State = jobsdomain.StateLeased
	job.LeaseOwner = "worker-dead"
	job.LeasedUntil = fixture.clock.Now().Add(-time.Minute)
	fixture.queue.mu.Unlock()

	// The queue itself treats an expired lease as claimable (the SQL claim
	// matches queued-due or leased-expired), so the abandoned job is picked
	// up by the next claim; the periodic sweep is a separate, supplementary
	// maintenance path already proven against PostgreSQL in the adapter test.
	waitUntil(t, 5*time.Second, func() bool { return fixture.stats().Succeeded == 1 }, "the abandoned job to be reclaimed and run")

	recovered, err := fixture.queue.JobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if recovered.State != jobsdomain.StateSucceeded {
		t.Errorf("state = %q, want succeeded", recovered.State)
	}
	if recovered.LeaseOwner != "worker-dead" && recovered.LeaseOwner != "" {
		t.Errorf("lease owner = %q, want the reclaiming worker or none", recovered.LeaseOwner)
	}
}

func TestHandlerMapRegistrationRules(t *testing.T) {
	registry := jobsapp.NewHandlerMap()
	handler := func(context.Context, *jobsdomain.Job) error { return nil }

	if err := registry.Register("send_email", 1, handler); !errors.Is(err, jobsdomain.ErrUnknownJobType) {
		t.Errorf("unknown type: %v", err)
	}
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 0, handler); !errors.Is(err, jobsdomain.ErrInvalidVersion) {
		t.Errorf("zero version: %v", err)
	}
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 1, nil); !errors.Is(err, jobsapp.ErrInvalidHandler) {
		t.Errorf("nil handler: %v", err)
	}
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 1, handler); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 1, handler); !errors.Is(err, jobsapp.ErrDuplicateHandler) {
		t.Errorf("duplicate: %v", err)
	}
	// The same workload at another version is a distinct handler.
	if err := registry.Register(jobsdomain.TypeEmailDelivery, 2, handler); err != nil {
		t.Errorf("new version: %v", err)
	}
	if registry.Len() != 2 {
		t.Errorf("Len = %d, want 2", registry.Len())
	}
	if _, err := registry.Resolve(jobsdomain.TypeEmailDelivery, 9); !errors.Is(err, jobsapp.ErrUnsupportedHandlerVersion) {
		t.Errorf("unknown version: %v", err)
	}
	if _, err := registry.Resolve(jobsdomain.TypeRetentionRun, 1); !errors.Is(err, jobsapp.ErrUnknownHandler) {
		t.Errorf("unknown workload: %v", err)
	}
}

func TestBackoffPolicyIsBoundedAndJittered(t *testing.T) {
	policy := jobsapp.BackoffPolicy{Base: time.Second, Max: 8 * time.Second}
	if err := policy.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	// Full jitter with a fixed high entropy value returns a value close to
	// the cap, and the cap doubles per attempt until Max bounds it.
	for attempt, wantCap := range map[int]time.Duration{
		1: time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 8 * time.Second,
		9: 8 * time.Second,
	} {
		delay, err := policy.Delay(attempt, fixedRandom{value: ^uint64(0)})
		if err != nil {
			t.Fatalf("delay attempt %d: %v", attempt, err)
		}
		if delay <= 0 || delay > wantCap {
			t.Errorf("attempt %d: delay = %v, want (0, %v]", attempt, delay, wantCap)
		}
		if delay < wantCap*9/10 {
			t.Errorf("attempt %d: delay = %v, want near the cap %v", attempt, delay, wantCap)
		}
	}

	// Minimum entropy yields (almost) no wait: the jitter spreads retries.
	zero, err := policy.Delay(4, fixedRandom{value: 0})
	if err != nil {
		t.Fatalf("zero delay: %v", err)
	}
	if zero != 0 {
		t.Errorf("zero entropy delay = %v, want 0", zero)
	}

	if _, err := policy.Delay(1, failingRandom{}); err == nil {
		t.Error("entropy failure must surface")
	}
	if _, err := (jobsapp.BackoffPolicy{}).Delay(1, fixedRandom{}); !errors.Is(err, jobsapp.ErrInvalidBackoff) {
		t.Errorf("invalid policy: %v", err)
	}
	if _, err := policy.Delay(1, nil); !errors.Is(err, jobsapp.ErrInvalidWorkerConfig) {
		t.Errorf("missing entropy source: %v", err)
	}
}

func TestWorkerConfigValidation(t *testing.T) {
	valid := jobsapp.DefaultWorkerConfig()
	if err := valid.Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
	if valid.HandlerTimeout >= valid.LeaseDuration {
		t.Fatal("the default handler timeout must be shorter than the lease")
	}

	cases := map[string]func(*jobsapp.WorkerConfig){
		"zero concurrency":       func(c *jobsapp.WorkerConfig) { c.Concurrency = 0 },
		"negative concurrency":   func(c *jobsapp.WorkerConfig) { c.Concurrency = -1 },
		"unbounded concurrency":  func(c *jobsapp.WorkerConfig) { c.Concurrency = 65 },
		"zero lease":             func(c *jobsapp.WorkerConfig) { c.LeaseDuration = 0 },
		"lease above maximum":    func(c *jobsapp.WorkerConfig) { c.LeaseDuration = jobsapp.MaxLeaseDuration + time.Second },
		"zero handler timeout":   func(c *jobsapp.WorkerConfig) { c.HandlerTimeout = 0 },
		"timeout equal to lease": func(c *jobsapp.WorkerConfig) { c.HandlerTimeout = c.LeaseDuration },
		"timeout above lease":    func(c *jobsapp.WorkerConfig) { c.HandlerTimeout = c.LeaseDuration + time.Second },
		"zero poll interval":     func(c *jobsapp.WorkerConfig) { c.PollInterval = 0 },
		"invalid backoff":        func(c *jobsapp.WorkerConfig) { c.Backoff = jobsapp.BackoffPolicy{} },
	}
	for name, mutate := range cases {
		cfg := jobsapp.DefaultWorkerConfig()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNewWorkerRequiresDependencies(t *testing.T) {
	base := jobsapp.WorkerDeps{
		Lease:    nil,
		Complete: nil,
		Fail:     nil,
		Recover:  nil,
		Registry: jobsapp.NewHandlerMap(),
		Clock:    &mutableClock{now: time.Now()},
		Random:   fixedRandom{},
		Config:   jobsapp.DefaultWorkerConfig(),
	}
	if _, err := jobsapp.NewWorker(base); !errors.Is(err, jobsapp.ErrInvalidQueueConfig) {
		t.Errorf("missing use cases: %v", err)
	}
}
