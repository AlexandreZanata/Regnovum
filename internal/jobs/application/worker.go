package application

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// Random supplies entropy for retry jitter. It is a local port (the
// application layer does not import platform packages), so the worker is
// deterministic under a stub and never reads a global source.
type Random interface {
	// Read fills buffer and returns the number of bytes written, or an
	// error when the entropy source fails.
	Read(buffer []byte) (int, error)
}

// Handler runs one leased job. Returning nil records success; any error is
// recorded as a redacted failure and the retry policy applies. The handler
// must respect the context deadline: the worker never waits past it.
type Handler func(ctx context.Context, job *domain.Job) error

// HandlerRegistry resolves a versioned workload name to its handler.
//
// The registry is typed (the key is the closed JobType vocabulary, never a
// string chosen at runtime) and versioned ((type, payload version) is the
// key), so a payload schema bump registers a new handler instead of changing
// the meaning of an enqueued job.
type HandlerRegistry interface {
	// Resolve returns the handler for a workload and payload version.
	Resolve(jobType domain.JobType, version int) (Handler, error)
}

// handlerKey is the typed, versioned registry key.
type handlerKey struct {
	jobType domain.JobType
	version int
}

// HandlerMap is the in-process HandlerRegistry. It is immutable once the
// worker starts: registration happens during composition, so a running worker
// can never silently change what a workload means.
type HandlerMap struct {
	handlers map[handlerKey]Handler
}

// NewHandlerMap creates an empty registry.
func NewHandlerMap() *HandlerMap {
	return &HandlerMap{handlers: make(map[handlerKey]Handler)}
}

// Register binds a handler to a workload and payload version. Registering the
// same pair twice is refused instead of overwriting: a silent replacement
// would change the semantics of jobs already in the queue.
func (m *HandlerMap) Register(jobType domain.JobType, version int, handler Handler) error {
	if !jobType.IsValid() {
		return domain.ErrUnknownJobType
	}
	if version < 1 {
		return domain.ErrInvalidVersion
	}
	if handler == nil {
		return ErrInvalidHandler
	}
	key := handlerKey{jobType: jobType, version: version}
	if _, exists := m.handlers[key]; exists {
		return fmt.Errorf("%w: %s v%d", ErrDuplicateHandler, jobType, version)
	}
	m.handlers[key] = handler
	return nil
}

// Resolve returns the handler for a workload and version.
func (m *HandlerMap) Resolve(jobType domain.JobType, version int) (Handler, error) {
	if m == nil {
		return nil, ErrUnknownHandler
	}
	if handler, ok := m.handlers[handlerKey{jobType: jobType, version: version}]; ok {
		return handler, nil
	}
	// Distinguish "this workload has no handler at all" from "this payload
	// version is not supported": the first is a wiring gap, the second is a
	// deployment that must be rolled forward.
	for key := range m.handlers {
		if key.jobType == jobType {
			return nil, fmt.Errorf("%w: %s v%d", ErrUnsupportedHandlerVersion, jobType, version)
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownHandler, jobType)
}

// Len reports how many versioned handlers are registered.
func (m *HandlerMap) Len() int {
	if m == nil {
		return 0
	}
	return len(m.handlers)
}

// BackoffPolicy computes the wait before the next attempt.
//
// The policy is exponential with full jitter: the cap doubles per attempt and
// the actual wait is uniform in [0, cap]. Full jitter spreads retries from a
// fleet of workers instead of synchronizing them into a thundering herd, and
// it keeps a failing dependency from being hammered.
type BackoffPolicy struct {
	// Base is the initial cap.
	Base time.Duration
	// Max bounds the cap no matter how many attempts were consumed.
	Max time.Duration
}

// DefaultBackoffPolicy returns the production policy.
func DefaultBackoffPolicy() BackoffPolicy {
	return BackoffPolicy{Base: 2 * time.Second, Max: 10 * time.Minute}
}

// Validate reports whether the policy is usable.
func (p BackoffPolicy) Validate() error {
	if p.Base <= 0 || p.Max <= 0 || p.Max < p.Base {
		return ErrInvalidBackoff
	}
	return nil
}

// Delay returns the wait after the given number of attempts. attempt is at
// least one (the attempt already consumed). The result is deterministic for a
// deterministic entropy source.
func (p BackoffPolicy) Delay(attempt int, random Random) (time.Duration, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	if random == nil {
		return 0, ErrInvalidWorkerConfig
	}
	if attempt < 1 {
		attempt = 1
	}

	cap := p.Base
	if shift := attempt - 1; shift > 0 {
		if shift > 62 || p.Base > time.Duration(math.MaxInt64>>shift) {
			cap = p.Max
		} else {
			cap = p.Base << shift
		}
	}
	if cap > p.Max || cap <= 0 {
		cap = p.Max
	}

	var buf [8]byte
	if _, err := random.Read(buf[:]); err != nil {
		return 0, fmt.Errorf("backoff jitter: %w", err)
	}
	fraction := float64(binary.BigEndian.Uint64(buf[:])>>11) / float64(uint64(1)<<53)
	return time.Duration(fraction * float64(cap)), nil
}

// WorkerConfig bounds one worker pool.
type WorkerConfig struct {
	// Concurrency is how many jobs one process handles at once.
	Concurrency int
	// LeaseDuration is how long a claimed job stays claimed without renewal.
	LeaseDuration time.Duration
	// HandlerTimeout bounds a single handler run. It must be strictly
	// shorter than LeaseDuration, so an outcome is always recorded before
	// the lease expires and two workers never run the same job.
	HandlerTimeout time.Duration
	// PollInterval is how long an idle worker waits before looking again.
	PollInterval time.Duration
	// Backoff is the retry policy applied to a failure.
	Backoff BackoffPolicy
}

// DefaultWorkerConfig returns the production defaults.
func DefaultWorkerConfig() WorkerConfig {
	return WorkerConfig{
		Concurrency:    4,
		LeaseDuration:  DefaultLeaseDuration,
		HandlerTimeout: 30 * time.Second,
		PollInterval:   2 * time.Second,
		Backoff:        DefaultBackoffPolicy(),
	}
}

// Validate reports whether the configuration is coherent.
func (c WorkerConfig) Validate() error {
	if c.Concurrency < 1 || c.Concurrency > 64 {
		return fmt.Errorf("%w: concurrency must be between 1 and 64", ErrInvalidWorkerConfig)
	}
	if c.LeaseDuration <= 0 || c.LeaseDuration > MaxLeaseDuration {
		return fmt.Errorf("%w: lease duration is invalid", ErrInvalidWorkerConfig)
	}
	if c.HandlerTimeout <= 0 {
		return fmt.Errorf("%w: handler timeout must be positive", ErrInvalidWorkerConfig)
	}
	if c.HandlerTimeout >= c.LeaseDuration {
		return fmt.Errorf("%w: handler timeout must be shorter than the lease duration", ErrInvalidWorkerConfig)
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("%w: poll interval must be positive", ErrInvalidWorkerConfig)
	}
	if err := c.Backoff.Validate(); err != nil {
		return err
	}
	return nil
}

// WorkerStats is a snapshot of what one worker pool did.
type WorkerStats struct {
	Leased    int64
	Succeeded int64
	Failed    int64
	Dead      int64
	Reclaimed int64
	Panics    int64
}

type workerCounters struct {
	leased    atomic.Int64
	succeeded atomic.Int64
	failed    atomic.Int64
	dead      atomic.Int64
	reclaimed atomic.Int64
	panics    atomic.Int64
}

// WorkerDeps are the dependencies of the worker runtime.
type WorkerDeps struct {
	Lease    *LeaseUseCase
	Complete *CompleteUseCase
	Fail     *FailUseCase
	Recover  *RecoverExpiredLeasesUseCase
	Registry HandlerRegistry
	Clock    Clock
	Random   Random
	Logger   *slog.Logger
	Config   WorkerConfig
}

// Worker runs leased jobs until its context is cancelled.
//
// Design invariants:
//   - bounded parallelism: at most Concurrency jobs run at once;
//   - a leased job is always finished and recorded; the shutdown signal stops
//     new claims but never cancels a handler already in flight, so a SIGTERM
//     does not abandon work (and if the process dies anyway, the lease expires
//     and another worker reclaims it);
//   - one job can never stop the pool: a handler error is recorded and the
//     loop continues, a panicking handler is recovered and recorded, and a
//     job that exhausts its budget leaves the queue as dead;
//   - retries respect the attempt budget and are spaced by exponential
//     backoff with full jitter.
type Worker struct {
	lease    *LeaseUseCase
	complete *CompleteUseCase
	fail     *FailUseCase
	recover  *RecoverExpiredLeasesUseCase
	registry HandlerRegistry
	clock    Clock
	random   Random
	logger   *slog.Logger
	cfg      WorkerConfig
	counters workerCounters
}

// NewWorker builds the worker runtime.
func NewWorker(deps WorkerDeps) (*Worker, error) {
	if deps.Lease == nil || deps.Complete == nil || deps.Fail == nil || deps.Recover == nil {
		return nil, ErrInvalidQueueConfig
	}
	if deps.Registry == nil || deps.Clock == nil || deps.Random == nil {
		return nil, ErrInvalidWorkerConfig
	}
	if err := deps.Config.Validate(); err != nil {
		return nil, err
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Worker{
		lease:    deps.Lease,
		complete: deps.Complete,
		fail:     deps.Fail,
		recover:  deps.Recover,
		registry: deps.Registry,
		clock:    deps.Clock,
		random:   deps.Random,
		logger:   logger,
		cfg:      deps.Config,
	}, nil
}

// Config returns the effective configuration.
func (w *Worker) Config() WorkerConfig { return w.cfg }

// Stats returns a snapshot of the counters.
func (w *Worker) Stats() WorkerStats {
	return WorkerStats{
		Leased:    w.counters.leased.Load(),
		Succeeded: w.counters.succeeded.Load(),
		Failed:    w.counters.failed.Load(),
		Dead:      w.counters.dead.Load(),
		Reclaimed: w.counters.reclaimed.Load(),
		Panics:    w.counters.panics.Load(),
	}
}

// Run starts the pool and blocks until ctx is cancelled. It returns after
// every in-flight job has been recorded (bounded by HandlerTimeout per job).
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for id := 0; id < w.cfg.Concurrency; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			w.loop(ctx, fmt.Sprintf("worker-%d", id))
		}(id)
	}
	wg.Wait()
	return nil
}

func (w *Worker) loop(ctx context.Context, owner string) {
	for {
		if ctx.Err() != nil {
			return
		}

		job, err := w.lease.Lease(ctx, owner, w.cfg.LeaseDuration)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.logger.Error("job worker: lease failed", slog.String("owner", owner), slog.String("error", err.Error()))
			if !sleepCtx(ctx, w.cfg.PollInterval) {
				return
			}
			continue
		}
		if job == nil {
			// Idle: reclaim work abandoned by a sibling that died, then wait.
			w.reclaim(ctx, owner)
			if !sleepCtx(ctx, w.cfg.PollInterval) {
				return
			}
			continue
		}

		w.counters.leased.Add(1)
		w.process(ctx, owner, job)
	}
}

// process runs one leased job and always records an outcome.
func (w *Worker) process(ctx context.Context, owner string, job *domain.Job) {
	handler, err := w.registry.Resolve(job.Type, job.Version)
	if err != nil {
		code := domain.FailureUnknownType
		if errors.Is(err, ErrUnsupportedHandlerVersion) {
			code = domain.FailureUnsupportedVersion
		}
		w.recordFailure(ctx, owner, job, code, err.Error())
		return
	}

	handlerErr := w.invoke(ctx, handler, job)
	if handlerErr == nil {
		if _, err := w.complete.Complete(ctx, job.ID, owner); err != nil {
			w.logger.Error("job worker: complete failed", slog.String("job_id", job.ID), slog.String("error", err.Error()))
			return
		}
		w.counters.succeeded.Add(1)
		return
	}

	code := domain.FailureHandlerError
	if errors.Is(handlerErr, context.DeadlineExceeded) {
		code = domain.FailureHandlerTimeout
	} else if errors.Is(handlerErr, errHandlerPanicked) {
		code = domain.FailureHandlerError
	}
	w.recordFailure(ctx, owner, job, code, handlerErr.Error())
}

var errHandlerPanicked = errors.New("handler panicked")

// invoke runs the handler under its own deadline.
//
// The handler context is detached from the shutdown signal
// (context.WithoutCancel): a job already leased is finished and its outcome
// recorded, so SIGTERM never leaves a lease without an outcome. The lease
// duration strictly exceeds the handler timeout, so the outcome always lands
// before the lease expires.
func (w *Worker) invoke(ctx context.Context, handler Handler, job *domain.Job) (err error) {
	handlerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.cfg.HandlerTimeout)
	defer cancel()

	defer func() {
		if recovered := recover(); recovered != nil {
			w.counters.panics.Add(1)
			w.logger.Error("job worker: handler panicked",
				slog.String("job_id", job.ID), slog.String("type", string(job.Type)))
			err = errHandlerPanicked
		}
	}()

	return handler(handlerCtx, job)
}

// recordFailure stores a redacted failure and applies the retry policy. The
// repository promotes the job to dead once the budget is spent, so a poison
// job leaves the queue instead of blocking it.
func (w *Worker) recordFailure(ctx context.Context, owner string, job *domain.Job, code domain.FailureCode, detail string) {
	var retryAt *time.Time
	if job.WithinAttemptBudget() {
		delay, err := w.cfg.Backoff.Delay(job.Attempts, w.random)
		if err != nil {
			w.logger.Error("job worker: backoff failed", slog.String("job_id", job.ID), slog.String("error", err.Error()))
		} else {
			instant := w.clock.Now().UTC().Add(delay)
			retryAt = &instant
		}
	}

	updated, err := w.fail.Fail(ctx, job.ID, owner, code, detail, retryAt)
	if err != nil {
		w.logger.Error("job worker: fail failed", slog.String("job_id", job.ID), slog.String("error", err.Error()))
		return
	}
	if updated.State == domain.StateDead {
		w.counters.dead.Add(1)
		w.logger.Warn("job worker: job exhausted its attempt budget",
			slog.String("job_id", job.ID), slog.String("type", string(job.Type)), slog.String("code", string(code)))
		return
	}
	w.counters.failed.Add(1)
}

// reclaim returns abandoned work to the queue.
func (w *Worker) reclaim(ctx context.Context, owner string) {
	reclaimed, err := w.recover.Recover(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Error("job worker: lease recovery failed", slog.String("owner", owner), slog.String("error", err.Error()))
		}
		return
	}
	if reclaimed > 0 {
		w.counters.reclaimed.Add(int64(reclaimed))
		w.logger.Warn("job worker: reclaimed abandoned leases", slog.Int("count", reclaimed))
	}
}

// sleepCtx waits for the interval and reports whether the worker should keep
// running (false once the context is cancelled).
func sleepCtx(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
