package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// The bounds of a schedule.
const (
	// MaxCatchUpPeriods caps how far back a single pass may reach. Recovery
	// is desirable; an unbounded loop over a clock that jumped years would
	// queue thousands of rows in one pass.
	MaxCatchUpPeriods = 24
	// DefaultCatchUpPeriods is the recovery window of a schedule that does
	// not state one: a daily cadence recovers three days of downtime, a
	// monthly one three months.
	DefaultCatchUpPeriods = 3
	// DefaultTick is how often the automatic scheduler re-evaluates its
	// schedules. It is deliberately much shorter than the shortest cadence:
	// the pass is idempotent, so being early costs nothing, while being late
	// is the one thing lag cannot fix.
	DefaultTick = time.Minute
)

// MaintenanceLock is the exclusivity port of the scheduler.
//
// The port is a scope, not a pair of acquire/release calls, so the lock cannot
// outlive the work it guards: the adapter holds it for the duration of the
// callback and releases it however the callback ends, including a panic or a
// lost connection. Two instances therefore cannot run the same pass — the
// second one is told that it did not get the lock, which is a normal outcome
// and not an error.
type MaintenanceLock interface {
	// WithLock runs fn while holding the exclusive maintenance lock. It
	// reports whether the lock was acquired; when it was not, fn is not
	// called and the error is nil.
	WithLock(ctx context.Context, fn func(ctx context.Context) error) (bool, error)
}

// ScheduleSpec is one maintenance cadence.
type ScheduleSpec struct {
	// Type is the workload to queue; it must belong to the closed set.
	Type domain.JobType
	// Version is the payload schema version of the workload.
	Version int
	// Interval is the cadence slot the workload runs for.
	Interval domain.Interval
	// CatchUp is how many earlier periods a single pass may also queue.
	CatchUp int
	// MaxAttempts overrides the attempt budget; zero selects the default.
	MaxAttempts int
}

// Validate reports whether the schedule is coherent.
func (s ScheduleSpec) Validate() error {
	if !s.Type.IsValid() {
		return fmt.Errorf("%w: unknown workload %q", ErrInvalidSchedule, s.Type)
	}
	if s.Version < 1 {
		return fmt.Errorf("%w: workload %q needs a payload version", ErrInvalidSchedule, s.Type)
	}
	if !s.Interval.IsValid() {
		return fmt.Errorf("%w: workload %q has an unknown cadence", ErrInvalidSchedule, s.Type)
	}
	if s.CatchUp < 0 || s.CatchUp > MaxCatchUpPeriods {
		return fmt.Errorf("%w: workload %q asks to recover %d periods", ErrInvalidSchedule, s.Type, s.CatchUp)
	}
	if s.MaxAttempts < 0 {
		return fmt.Errorf("%w: workload %q has a negative attempt budget", ErrInvalidSchedule, s.Type)
	}
	return nil
}

// DefaultSchedules are the maintenance workloads of the platform.
//
// The cadences are product decisions, listed here so the whole rhythm of the
// platform is one readable table: grants settle monthly, everything that
// follows the calendar of a day runs daily.
func DefaultSchedules() []ScheduleSpec {
	return []ScheduleSpec{
		{Type: domain.TypeBillingReconciliation, Version: 1, Interval: domain.IntervalDaily, CatchUp: DefaultCatchUpPeriods},
		{Type: domain.TypeINKGrantMonthly, Version: 1, Interval: domain.IntervalMonthly, CatchUp: DefaultCatchUpPeriods},
		{Type: domain.TypePassExpiry, Version: 1, Interval: domain.IntervalDaily, CatchUp: DefaultCatchUpPeriods},
		{Type: domain.TypeRetentionRun, Version: 1, Interval: domain.IntervalDaily, CatchUp: DefaultCatchUpPeriods},
		{Type: domain.TypeSessionCleanup, Version: 1, Interval: domain.IntervalDaily, CatchUp: DefaultCatchUpPeriods},
	}
}

// PeriodRun is what one period of one schedule did.
type PeriodRun struct {
	Period domain.Period
	// Created reports that this pass queued the period; false means the
	// period was already queued, by this instance or by an earlier one.
	Created bool
}

// ScheduleReport is the outcome of one schedule in one pass, and the metric
// surface of the scheduler: the lag is how far the current period is behind
// the instant the pass ran.
type ScheduleReport struct {
	Type       domain.JobType
	Interval   domain.Interval
	Current    domain.Period
	LagSeconds int64
	Runs       []PeriodRun
	Created    int
	Replayed   int
}

// Report is the outcome of one pass.
type Report struct {
	RunAt time.Time
	// LockHeld reports whether this instance ran the pass. When another
	// instance holds the lock, the pass did nothing and the schedule list is
	// empty: that is the intended behaviour, not a degraded one.
	LockHeld  bool
	Schedules []ScheduleReport
}

// MaxLag is the worst lag of the pass, which is the number an alert is built
// on: a single schedule falling behind is what an operator needs to see.
func (r Report) MaxLag() time.Duration {
	var worst time.Duration
	for _, schedule := range r.Schedules {
		lag := time.Duration(schedule.LagSeconds) * time.Second
		if lag > worst {
			worst = lag
		}
	}
	return worst
}

// Created sums the work the pass queued.
func (r Report) Created() int {
	total := 0
	for _, schedule := range r.Schedules {
		total += schedule.Created
	}
	return total
}

// SchedulerDeps are the dependencies of the scheduler.
type SchedulerDeps struct {
	// Lock grants exclusive execution across instances.
	Lock MaintenanceLock
	// Queue validates and stores the queued work.
	Queue *EnqueueUseCase
	// Clock supplies the instant of a pass.
	Clock Clock
	// Schedules are the maintenance workloads; empty selects the defaults.
	Schedules []ScheduleSpec
	// Tick is how often the automatic loop runs a pass; zero selects the
	// default.
	Tick time.Duration
	// Logger receives one record per pass with the lag of each schedule.
	Logger *slog.Logger
}

// Scheduler queues the maintenance work of each cadence exactly once per
// period, from one instance at a time.
//
// Two properties carry the design. First, a period is a value, so the work of
// a period that was missed is still the work of that period: recovery is just
// queueing the periods that are behind. Second, the queue's own idempotency
// makes the pass a no-op for anything already queued, so the scheduler keeps no
// "last run" state to corrupt, to restore, or to disagree with a second
// instance about.
type Scheduler struct {
	lock      MaintenanceLock
	queue     *EnqueueUseCase
	clock     Clock
	schedules []ScheduleSpec
	tick      time.Duration
	logger    *slog.Logger
}

// NewScheduler validates the wiring and the schedules.
func NewScheduler(deps SchedulerDeps) (*Scheduler, error) {
	if deps.Lock == nil || deps.Queue == nil || deps.Clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	schedules := deps.Schedules
	if len(schedules) == 0 {
		schedules = DefaultSchedules()
	}
	seen := make(map[domain.JobType]bool, len(schedules))
	ordered := make([]ScheduleSpec, len(schedules))
	copy(ordered, schedules)
	for _, spec := range ordered {
		if err := spec.Validate(); err != nil {
			return nil, err
		}
		if seen[spec.Type] {
			return nil, fmt.Errorf("%w: workload %q is scheduled twice", ErrInvalidSchedule, spec.Type)
		}
		seen[spec.Type] = true
	}
	// A deterministic order keeps the pass, its logs and its tests readable:
	// two passes at the same instant touch the periods in the same sequence.
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Type < ordered[right].Type })

	tick := deps.Tick
	if tick == 0 {
		tick = DefaultTick
	}
	if tick < 0 {
		return nil, ErrInvalidSchedule
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return &Scheduler{
		lock:      deps.Lock,
		queue:     deps.Queue,
		clock:     deps.Clock,
		schedules: ordered,
		tick:      tick,
		logger:    logger,
	}, nil
}

// Schedules returns the configured cadences in pass order.
func (s *Scheduler) Schedules() []ScheduleSpec {
	if s == nil {
		return nil
	}
	return append([]ScheduleSpec(nil), s.schedules...)
}

// RunOnce runs one pass at the instant of the injected clock. It is the same
// use case the automatic loop calls: a manual run is an operator asking for a
// pass now, not a second implementation of the schedules.
func (s *Scheduler) RunOnce(ctx context.Context) (Report, error) {
	if s == nil || s.clock == nil {
		return Report{}, ErrInvalidQueueConfig
	}
	return s.RunAt(ctx, s.clock.Now())
}

// RunAt runs one pass as if the given instant were now, which is how a missed
// period is recovered deliberately.
func (s *Scheduler) RunAt(ctx context.Context, now time.Time) (Report, error) {
	if s == nil || s.lock == nil || s.queue == nil {
		return Report{}, ErrInvalidQueueConfig
	}
	if ctx == nil {
		return Report{}, errors.New("scheduler: nil context")
	}
	report := Report{RunAt: now.UTC()}
	acquired, err := s.lock.WithLock(ctx, func(lockedCtx context.Context) error {
		schedules, err := s.queueDue(lockedCtx, report.RunAt)
		if err != nil {
			return err
		}
		report.Schedules = schedules
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	report.LockHeld = acquired
	if !acquired {
		// Another instance is running the pass. Doing nothing is correct:
		// duplicated work is what the lock exists to prevent.
		report.Schedules = nil
		return report, nil
	}
	s.logPass(ctx, report)
	return report, nil
}

// Run runs passes until the context is cancelled. A failing pass is logged and
// retried on the next tick: the queue is idempotent, so the next pass repairs
// whatever this one could not queue.
func (s *Scheduler) Run(ctx context.Context) error {
	if s == nil || s.lock == nil || s.queue == nil || s.clock == nil {
		return ErrInvalidQueueConfig
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		report, err := s.RunOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.logger.WarnContext(ctx, "maintenance scheduler: pass failed", slog.String("error", err.Error()))
		} else if report.LockHeld {
			s.logger.InfoContext(ctx, "maintenance scheduler: pass complete",
				slog.Int("created", report.Created()),
				slog.Int64("lag_seconds", int64(report.MaxLag().Seconds())),
			)
		}
		if !sleepCtx(ctx, s.tick) {
			return ctx.Err()
		}
	}
}

// queueDue queues the current period of every schedule and, within its catch-up
// window, the periods that are behind.
func (s *Scheduler) queueDue(ctx context.Context, now time.Time) ([]ScheduleReport, error) {
	reports := make([]ScheduleReport, 0, len(s.schedules))
	for _, spec := range s.schedules {
		current, err := domain.PeriodAt(spec.Interval, now)
		if err != nil {
			return nil, err
		}
		start, err := current.Start()
		if err != nil {
			return nil, err
		}
		// The period was derived from this instant, so it contains it and the
		// measurement cannot run backwards; no clamp is needed to keep a lag
		// metric from going negative.
		lagSeconds := int64(now.Sub(start) / time.Second)
		report := ScheduleReport{
			Type:       spec.Type,
			Interval:   spec.Interval,
			Current:    current,
			LagSeconds: lagSeconds,
		}
		// Oldest first: a reader of the pass sees the recovery before the
		// current period, and a queue drained in order runs the backlog
		// before the newest work.
		for step := spec.CatchUp; step >= 0; step-- {
			period, err := current.Back(step)
			if err != nil {
				return nil, err
			}
			payload, err := domain.PeriodPayload(period)
			if err != nil {
				return nil, err
			}
			key, err := scheduleKey(spec.Type, period)
			if err != nil {
				return nil, err
			}
			result, err := s.queue.Enqueue(ctx, EnqueueCommand{
				Type:           spec.Type,
				Version:        spec.Version,
				Payload:        payload,
				IdempotencyKey: key,
				MaxAttempts:    spec.MaxAttempts,
			})
			if err != nil {
				return nil, fmt.Errorf("schedule %s for %s: %w", spec.Type, period, err)
			}
			created := result == nil || !result.Replayed
			if created {
				report.Created++
			} else {
				report.Replayed++
			}
			report.Runs = append(report.Runs, PeriodRun{Period: period, Created: created})
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// scheduleKey is the enqueue key of one period of one workload. It is what
// makes a pass idempotent and a recovery safe: the same period of the same
// workload can be queued any number of times and exists once.
func scheduleKey(jobType domain.JobType, period domain.Period) (string, error) {
	key := "schedule:" + string(jobType) + ":" + period.String()
	if len(key) > domain.MaxIdempotencyKeyLength {
		return "", domain.ErrInvalidIdempotencyKey
	}
	return key, nil
}

// logPass records the lag of each schedule, which is the metric the phase
// requires: without it an operator sees the queue, not the drift.
func (s *Scheduler) logPass(ctx context.Context, report Report) {
	for _, schedule := range report.Schedules {
		s.logger.InfoContext(ctx, "maintenance scheduler: schedule",
			slog.String("workload", string(schedule.Type)),
			slog.String("interval", schedule.Interval.String()),
			slog.String("period", schedule.Current.String()),
			slog.Int64("lag_seconds", schedule.LagSeconds),
			slog.Int("created", schedule.Created),
			slog.Int("replayed", schedule.Replayed),
		)
	}
}
