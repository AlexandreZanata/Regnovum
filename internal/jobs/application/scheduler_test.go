package application_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// schedulerClock is the stub clock the phase requires: a pass runs at the
// instant the test chooses, so cadence arithmetic is exercised without waiting.
type schedulerClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *schedulerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *schedulerClock) set(instant time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = instant
}

// recordingQueue is the queue port of the scheduler: it records every stored
// period and resolves a repeated key the way the durable queue does.
type recordingQueue struct {
	mu       sync.Mutex
	stored   map[string]string
	order    []string
	holdFunc func()
}

func newRecordingQueue() *recordingQueue {
	return &recordingQueue{stored: make(map[string]string)}
}

func (q *recordingQueue) Enqueue(_ context.Context, record application.EnqueueRecord) (*domain.Job, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.holdFunc != nil {
		q.holdFunc()
	}
	if existing, ok := q.stored[record.IdempotencyKey]; ok {
		return &domain.Job{ID: existing, IdempotencyKey: record.IdempotencyKey, Type: record.Type, Version: record.Version}, true, nil
	}
	id := fmt.Sprintf("job-%d", len(q.stored)+1)
	q.stored[record.IdempotencyKey] = id
	q.order = append(q.order, record.IdempotencyKey)
	return &domain.Job{ID: id, IdempotencyKey: record.IdempotencyKey, Type: record.Type, Version: record.Version, Payload: record.Payload}, false, nil
}

func (q *recordingQueue) Lease(context.Context, string, time.Time, time.Time) (*domain.Job, error) {
	return nil, errors.New("recording queue: Lease is not part of this test")
}

func (q *recordingQueue) Complete(context.Context, string, string, time.Time) (*domain.Job, error) {
	return nil, errors.New("recording queue: Complete is not part of this test")
}

func (q *recordingQueue) Fail(context.Context, domain.Failure, string, string, time.Time, time.Time) (*domain.Job, error) {
	return nil, errors.New("recording queue: Fail is not part of this test")
}

func (q *recordingQueue) ReleaseExpiredLeases(context.Context, time.Time) (int, error) {
	return 0, errors.New("recording queue: ReleaseExpiredLeases is not part of this test")
}

func (q *recordingQueue) JobByID(context.Context, string) (*domain.Job, error) {
	return nil, errors.New("recording queue: JobByID is not part of this test")
}

func (q *recordingQueue) keys() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.order...)
}

// lock is the exclusivity port under the scheduler's control.
type lock struct {
	mu       sync.Mutex
	holders  int
	refusals int
	onPass   func()
	hold     func()
}

func (l *lock) WithLock(ctx context.Context, fn func(ctx context.Context) error) (bool, error) {
	l.mu.Lock()
	// A lock held elsewhere refuses the pass without calling it.
	if l.holders > 0 {
		l.refusals++
		l.mu.Unlock()
		return false, nil
	}
	l.holders++
	onPass, hold := l.onPass, l.hold
	l.mu.Unlock()

	if hold != nil {
		hold()
	}
	err := fn(ctx)

	l.mu.Lock()
	l.holders--
	l.mu.Unlock()
	if onPass != nil {
		onPass()
	}
	return true, err
}

func (l *lock) counts() (refusals int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refusals
}

func instant(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

type schedulerHarness struct {
	scheduler *application.Scheduler
	queue     *recordingQueue
	lock      *lock
	clock     *schedulerClock
}

func newSchedulerHarness(t *testing.T, schedules []application.ScheduleSpec) *schedulerHarness {
	t.Helper()
	queue := newRecordingQueue()
	exclusive := &lock{}
	clock := &schedulerClock{now: instant("2026-09-18T12:00:00Z")}
	enqueue, err := application.NewEnqueueUseCase(queue, clock)
	if err != nil {
		t.Fatalf("NewEnqueueUseCase() error = %v", err)
	}
	scheduler, err := application.NewScheduler(application.SchedulerDeps{
		Lock:      exclusive,
		Queue:     enqueue,
		Clock:     clock,
		Schedules: schedules,
		Tick:      time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewScheduler() error = %v", err)
	}
	return &schedulerHarness{scheduler: scheduler, queue: queue, lock: exclusive, clock: clock}
}

func monthlySchedule() application.ScheduleSpec {
	return application.ScheduleSpec{Type: domain.TypeINKGrantMonthly, Version: 1, Interval: domain.IntervalMonthly}
}

func monthlyScheduleWithRecovery(window int) application.ScheduleSpec {
	spec := monthlySchedule()
	spec.CatchUp = window
	return spec
}

// TestAPassQueuesOneJobPerPeriodOfEverySchedule proves the plain case, and that
// the payload of each run carries the period it belongs to.
func TestAPassQueuesOneJobPerPeriodOfEverySchedule(t *testing.T) {
	built := newSchedulerHarness(t, []application.ScheduleSpec{
		monthlySchedule(),
		{Type: domain.TypeRetentionRun, Version: 1, Interval: domain.IntervalDaily},
	})
	report, err := built.scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !report.LockHeld {
		t.Fatal("the pass did not take the lock")
	}
	if report.Created() != 2 {
		t.Errorf("created = %d, want one job per schedule", report.Created())
	}
	keys := built.queue.keys()
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want two jobs", keys)
	}
	if keys[0] != "schedule:ink_grant_monthly:2026-09" {
		t.Errorf("key = %q, want the monthly period of the pass", keys[0])
	}
	if keys[1] != "schedule:retention_run:2026-09-18" {
		t.Errorf("key = %q, want the daily period of the pass", keys[1])
	}
}

// TestASecondPassInTheSamePeriodDoesNothing is what makes the scheduler safe to
// run on a short tick: being early costs nothing.
func TestASecondPassInTheSamePeriodDoesNothing(t *testing.T) {
	built := newSchedulerHarness(t, []application.ScheduleSpec{monthlySchedule()})
	if _, err := built.scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	before := len(built.queue.keys())
	report, err := built.scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}
	if report.Created() != 0 {
		t.Errorf("created = %d, want no new work in the same period", report.Created())
	}
	if len(built.queue.keys()) != before {
		t.Errorf("keys = %v, want the queue unchanged", built.queue.keys())
	}
	latest := report.Schedules[0].Runs[len(report.Schedules[0].Runs)-1]
	if latest.Created {
		t.Error("the current period was reported as created twice")
	}
}

// TestADelayRecoversTheMissedPeriods is the recovery half of the phase: a
// process that was down for months queues every period it owes, each of them
// with its own period in the payload.
func TestADelayRecoversTheMissedPeriods(t *testing.T) {
	built := newSchedulerHarness(t, []application.ScheduleSpec{monthlyScheduleWithRecovery(3)})
	// The last pass an instance managed to run.
	built.clock.set(instant("2026-06-15T03:00:00Z"))
	if _, err := built.scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	// Three months later the process starts again. Every period between the
	// last pass and now must be queued, each of them once.
	built.clock.set(instant("2026-09-18T03:00:00Z"))
	report, err := built.scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("recovery RunOnce() error = %v", err)
	}
	keys := built.queue.keys()
	want := []string{
		"schedule:ink_grant_monthly:2026-06",
		"schedule:ink_grant_monthly:2026-07",
		"schedule:ink_grant_monthly:2026-08",
		"schedule:ink_grant_monthly:2026-09",
	}
	tail := keys[len(keys)-len(want):]
	for index, key := range want {
		if tail[index] != key {
			t.Errorf("key %d = %q, want %q", index, tail[index], key)
		}
	}
	if report.Created() != 3 {
		t.Errorf("created = %d, want the three missed months", report.Created())
	}
	for _, run := range report.Schedules[0].Runs {
		if run.Period.String() == "2026-06" && run.Created {
			t.Error("a period already queued was created again")
		}
	}
	// The recovery window is bounded on purpose: a process that was away for
	// two years queues its window, not every month since the beginning.
	if len(keys) != 4+3 {
		t.Errorf("keys = %v, want the first pass window plus the three recovered months", keys)
	}
}

func TestCatchUpWindowIsBounded(t *testing.T) {
	built := newSchedulerHarness(t, []application.ScheduleSpec{
		{Type: domain.TypeRetentionRun, Version: 1, Interval: domain.IntervalDaily, CatchUp: 2},
	})
	if _, err := built.scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	keys := built.queue.keys()
	if len(keys) != 3 {
		t.Fatalf("keys = %v, want the current day plus two recovered ones", keys)
	}
	if keys[0] != "schedule:retention_run:2026-09-16" || keys[2] != "schedule:retention_run:2026-09-18" {
		t.Errorf("keys = %v, want oldest first within the window", keys)
	}
}

// TestOnlyOneInstanceRunsThePass is the exclusivity requirement: a second
// instance is told it did not get the lock and queues nothing.
func TestOnlyOneInstanceRunsThePass(t *testing.T) {
	built := newSchedulerHarness(t, []application.ScheduleSpec{monthlySchedule()})
	// While a pass is in flight, the lock refuses the other instance. The
	// nested call models the concurrent instance exactly: same scheduler,
	// same clock, one lock.
	var nested application.Report
	var nestedErr error
	built.lock.onPass = func() {}
	built.lock.hold = func() {
		nested, nestedErr = built.scheduler.RunOnce(context.Background())
	}
	report, err := built.scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if nestedErr != nil {
		t.Fatalf("nested RunOnce() error = %v", nestedErr)
	}
	if !report.LockHeld {
		t.Error("the first instance did not take the lock")
	}
	if nested.LockHeld {
		t.Error("the second instance believed it held the lock")
	}
	if len(nested.Schedules) != 0 || nested.Created() != 0 {
		t.Errorf("nested report = %+v, want no work", nested)
	}
	if refusals := built.lock.counts(); refusals != 1 {
		t.Errorf("refusals = %d, want exactly one refused pass", refusals)
	}
	if keys := built.queue.keys(); len(keys) != 1 {
		t.Errorf("keys = %v, want one job: two instances duplicated work", keys)
	}
}

// TestLagIsMeasuredAgainstThePeriodStart is the metric the phase asks for: the
// pass reports how far behind the cadence it is.
func TestLagIsMeasuredAgainstThePeriodStart(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		schedule application.ScheduleSpec
		now      time.Time
		wantLag  int64
	}{
		{"monthly on the first day", application.ScheduleSpec{Type: domain.TypeINKGrantMonthly, Version: 1, Interval: domain.IntervalMonthly}, instant("2026-09-01T00:00:00Z"), 0},
		{"monthly mid-month", application.ScheduleSpec{Type: domain.TypeINKGrantMonthly, Version: 1, Interval: domain.IntervalMonthly}, instant("2026-09-18T12:00:00Z"), 17*24*3600 + 12*3600},
		{"daily on the boundary", application.ScheduleSpec{Type: domain.TypeRetentionRun, Version: 1, Interval: domain.IntervalDaily}, instant("2026-09-18T00:00:00Z"), 0},
		{"daily mid-day", application.ScheduleSpec{Type: domain.TypeRetentionRun, Version: 1, Interval: domain.IntervalDaily}, instant("2026-09-18T06:30:00Z"), 6*3600 + 30*60},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			built := newSchedulerHarness(t, []application.ScheduleSpec{testCase.schedule})
			report, err := built.scheduler.RunAt(context.Background(), testCase.now)
			if err != nil {
				t.Fatalf("RunAt() error = %v", err)
			}
			if got := report.Schedules[0].LagSeconds; got != testCase.wantLag {
				t.Errorf("lag = %d seconds, want %d", got, testCase.wantLag)
			}
			if got := int64(report.MaxLag().Seconds()); got != testCase.wantLag {
				t.Errorf("MaxLag() = %d seconds, want %d", got, testCase.wantLag)
			}
		})
	}
}

// TestLagIsNeverNegative holds because the period contains the instant the lag
// is measured from, so the measurement cannot run backwards — a clock that is
// early only means the period has just started.
func TestLagIsNeverNegative(t *testing.T) {
	built := newSchedulerHarness(t, []application.ScheduleSpec{monthlySchedule()})
	for _, at := range []time.Time{
		instant("2026-09-01T00:00:00Z").Add(-time.Hour),
		instant("2026-09-01T00:00:00Z"),
		instant("2026-09-30T23:59:59Z"),
	} {
		report, err := built.scheduler.RunAt(context.Background(), at)
		if err != nil {
			t.Fatalf("RunAt(%s) error = %v", at, err)
		}
		if report.Schedules[0].LagSeconds < 0 {
			t.Errorf("RunAt(%s) lag = %d, want a non-negative measure", at, report.Schedules[0].LagSeconds)
		}
	}
}

// TestManualAndAutomaticRunsAreTheSameUseCase pins the requirement that an
// operator running maintenance by hand runs the schedules, not a second
// implementation of them.
func TestManualAndAutomaticRunsAreTheSameUseCase(t *testing.T) {
	built := newSchedulerHarness(t, []application.ScheduleSpec{monthlySchedule()})
	automatic, err := built.scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	manual, err := built.scheduler.RunAt(context.Background(), built.clock.Now())
	if err != nil {
		t.Fatalf("RunAt() error = %v", err)
	}
	if manual.RunAt != automatic.RunAt {
		t.Errorf("RunAt() = %s, want the same instant as the automatic pass %s", manual.RunAt, automatic.RunAt)
	}
	if len(manual.Schedules) != len(automatic.Schedules) {
		t.Fatalf("schedules = %d, want %d", len(manual.Schedules), len(automatic.Schedules))
	}
	if manual.Schedules[0].Type != automatic.Schedules[0].Type || manual.Schedules[0].Current != automatic.Schedules[0].Current {
		t.Error("the manual pass covered a different schedule than the automatic one")
	}
	if manual.Created() != 0 {
		t.Errorf("the manual pass created %d jobs, want the same periods as the automatic one", manual.Created())
	}
}

// TestRunLoopsUntilTheContextEnds keeps the automatic path honest: it stops,
// and it stops promptly.
func TestRunLoopsUntilTheContextEnds(t *testing.T) {
	built := newSchedulerHarness(t, []application.ScheduleSpec{monthlySchedule()})
	ctx, cancel := context.WithCancel(context.Background())
	passes := 0
	built.lock.onPass = func() {
		passes++
		if passes == 2 {
			cancel()
		}
	}
	err := built.scheduler.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
	if passes < 2 {
		t.Errorf("passes = %d, want the loop to keep evaluating", passes)
	}
}

func TestSchedulerRefusesAnIncoherentWiring(t *testing.T) {
	queue := newRecordingQueue()
	clock := &schedulerClock{now: instant("2026-09-18T12:00:00Z")}
	enqueue, err := application.NewEnqueueUseCase(queue, clock)
	if err != nil {
		t.Fatalf("NewEnqueueUseCase() error = %v", err)
	}
	for _, testCase := range []struct {
		name string
		deps application.SchedulerDeps
	}{
		{"no lock", application.SchedulerDeps{Queue: enqueue, Clock: clock}},
		{"no queue", application.SchedulerDeps{Lock: &lock{}, Clock: clock}},
		{"no clock", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue}},
		{"unknown workload", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue, Clock: clock, Schedules: []application.ScheduleSpec{{Type: "make_coffee", Version: 1, Interval: domain.IntervalDaily}}}},
		{"missing version", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue, Clock: clock, Schedules: []application.ScheduleSpec{{Type: domain.TypeRetentionRun, Interval: domain.IntervalDaily}}}},
		{"unknown cadence", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue, Clock: clock, Schedules: []application.ScheduleSpec{{Type: domain.TypeRetentionRun, Version: 1, Interval: "weekly"}}}},
		{"recovery window beyond the cap", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue, Clock: clock, Schedules: []application.ScheduleSpec{{Type: domain.TypeRetentionRun, Version: 1, Interval: domain.IntervalDaily, CatchUp: application.MaxCatchUpPeriods + 1}}}},
		{"negative recovery window", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue, Clock: clock, Schedules: []application.ScheduleSpec{{Type: domain.TypeRetentionRun, Version: 1, Interval: domain.IntervalDaily, CatchUp: -1}}}},
		{"negative attempt budget", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue, Clock: clock, Schedules: []application.ScheduleSpec{{Type: domain.TypeRetentionRun, Version: 1, Interval: domain.IntervalDaily, MaxAttempts: -1}}}},
		{"negative tick", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue, Clock: clock, Tick: -time.Second}},
		{"duplicated workload", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue, Clock: clock, Schedules: []application.ScheduleSpec{
			{Type: domain.TypeRetentionRun, Version: 1, Interval: domain.IntervalDaily},
			{Type: domain.TypeRetentionRun, Version: 1, Interval: domain.IntervalMonthly},
		}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := application.NewScheduler(testCase.deps); err == nil {
				t.Error("NewScheduler() error = nil, want a refusal")
			}
		})
	}
	for _, testCase := range []struct {
		name string
		deps application.SchedulerDeps
	}{
		{"defaults", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue, Clock: clock}},
		{"explicit schedules", application.SchedulerDeps{Lock: &lock{}, Queue: enqueue, Clock: clock, Schedules: []application.ScheduleSpec{monthlySchedule()}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			scheduler, err := application.NewScheduler(testCase.deps)
			if err != nil {
				t.Fatalf("NewScheduler() error = %v", err)
			}
			if len(scheduler.Schedules()) == 0 {
				t.Error("Schedules() is empty")
			}
			// The pass order is deterministic, so logs and tests read the
			// same way on every run.
			schedules := scheduler.Schedules()
			for index := 1; index < len(schedules); index++ {
				if schedules[index-1].Type > schedules[index].Type {
					t.Errorf("schedules are not ordered: %q before %q", schedules[index-1].Type, schedules[index].Type)
				}
			}
		})
	}
}

func TestDefaultSchedulesCoverTheMaintenanceWorkloads(t *testing.T) {
	schedules := application.DefaultSchedules()
	seen := make(map[domain.JobType]bool, len(schedules))
	for _, schedule := range schedules {
		if err := schedule.Validate(); err != nil {
			t.Errorf("default schedule %q is invalid: %v", schedule.Type, err)
		}
		seen[schedule.Type] = true
	}
	for _, workload := range []domain.JobType{
		domain.TypeINKGrantMonthly,
		domain.TypePassExpiry,
		domain.TypeRetentionRun,
		domain.TypeSessionCleanup,
		domain.TypeBillingReconciliation,
	} {
		if !seen[workload] {
			t.Errorf("workload %q has no default schedule", workload)
		}
	}
	// Email delivery is not a cadence: it is queued by the event that asks for
	// it, so a schedule for it would send mail on a timer.
	if seen[domain.TypeEmailDelivery] {
		t.Error("email delivery was scheduled")
	}
}

func TestSchedulerSurfacesAQueueFailure(t *testing.T) {
	built := newSchedulerHarness(t, []application.ScheduleSpec{monthlySchedule()})
	built.queue.holdFunc = func() {}
	built.lock.hold = func() {}
	failure := errors.New("queue: write unavailable")
	broken := &failingQueue{err: failure}
	enqueue, err := application.NewEnqueueUseCase(broken, built.clock)
	if err != nil {
		t.Fatalf("NewEnqueueUseCase() error = %v", err)
	}
	scheduler, err := application.NewScheduler(application.SchedulerDeps{
		Lock:      &lock{},
		Queue:     enqueue,
		Clock:     built.clock,
		Schedules: []application.ScheduleSpec{monthlySchedule()},
	})
	if err != nil {
		t.Fatalf("NewScheduler() error = %v", err)
	}
	if _, err := scheduler.RunOnce(context.Background()); !errors.Is(err, failure) {
		t.Errorf("RunOnce() error = %v, want the queue failure", err)
	}
}

// failingQueue refuses every enqueue.
type failingQueue struct{ err error }

func (q *failingQueue) Enqueue(context.Context, application.EnqueueRecord) (*domain.Job, bool, error) {
	return nil, false, q.err
}

func (q *failingQueue) Lease(context.Context, string, time.Time, time.Time) (*domain.Job, error) {
	return nil, q.err
}

func (q *failingQueue) Complete(context.Context, string, string, time.Time) (*domain.Job, error) {
	return nil, q.err
}

func (q *failingQueue) Fail(context.Context, domain.Failure, string, string, time.Time, time.Time) (*domain.Job, error) {
	return nil, q.err
}

func (q *failingQueue) ReleaseExpiredLeases(context.Context, time.Time) (int, error) {
	return 0, q.err
}

func (q *failingQueue) JobByID(context.Context, string) (*domain.Job, error) {
	return nil, q.err
}
