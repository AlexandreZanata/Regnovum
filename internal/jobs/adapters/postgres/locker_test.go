package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	jobsrepo "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// schedulerClock is the stub clock of the scheduler integration tests.
type schedulerClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *schedulerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// lockerHarness wires two independent instances over the same database: the
// point of the lock is what happens between processes, and two lockers over
// one pool are two clients as far as PostgreSQL is concerned.
type lockerHarness struct {
	first  *jobsapp.Scheduler
	second *jobsapp.Scheduler
	clock  *schedulerClock
	pool   *pgxpool.Pool
}

func newLockerHarness(t *testing.T, schedules []jobsapp.ScheduleSpec) *lockerHarness {
	t.Helper()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	clock := &schedulerClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	manager := platformpg.NewTxManager(pool)

	build := func() *jobsapp.Scheduler {
		repo := jobsrepo.NewRepository(pool)
		enqueue, err := jobsapp.NewEnqueueUseCase(repo, clock)
		if err != nil {
			t.Fatalf("enqueue use case: %v", err)
		}
		locker, err := jobsrepo.NewLocker(manager)
		if err != nil {
			t.Fatalf("NewLocker() error = %v", err)
		}
		scheduler, err := jobsapp.NewScheduler(jobsapp.SchedulerDeps{
			Lock:      locker,
			Queue:     enqueue,
			Clock:     clock,
			Schedules: schedules,
		})
		if err != nil {
			t.Fatalf("NewScheduler() error = %v", err)
		}
		return scheduler
	}
	return &lockerHarness{first: build(), second: build(), clock: clock, pool: pool}
}

func (h *lockerHarness) jobCount(t *testing.T) int {
	t.Helper()
	var total int
	if err := h.pool.QueryRow(context.Background(), "SELECT count(*) FROM app.jobs").Scan(&total); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return total
}

func (h *lockerHarness) consecutivePasses(t *testing.T) int64 {
	t.Helper()
	var total int64
	if err := h.pool.QueryRow(context.Background(),
		"SELECT count(DISTINCT idempotency_key) FROM app.jobs WHERE idempotency_key LIKE 'schedule:%'").Scan(&total); err != nil {
		t.Fatalf("count schedule keys: %v", err)
	}
	return total
}

func monthly(t *testing.T) []jobsapp.ScheduleSpec {
	t.Helper()
	return []jobsapp.ScheduleSpec{{Type: domain.TypeINKGrantMonthly, Version: 1, Interval: domain.IntervalMonthly, CatchUp: 1}}
}

// TestOnlyOneOfTwoInstancesRunsThePass is the exclusivity requirement measured
// against the database's own lock: the second instance is refused, does no
// work, and the queue holds exactly one job per period.
func TestOnlyOneOfTwoInstancesRunsThePass(t *testing.T) {
	built := newLockerHarness(t, monthly(t))
	ctx := context.Background()

	outerReport, err := built.first.RunAt(ctx, built.clock.Now())
	if err != nil {
		t.Fatalf("first RunAt() error = %v", err)
	}
	if !outerReport.LockHeld {
		t.Fatal("the first instance did not take the lock")
	}
	if created := built.jobCount(t); created != 2 {
		t.Fatalf("jobs = %d, want the current period and one recovered", created)
	}

	// A second pass, from the other instance and in the same period: it takes
	// the lock and finds the work already queued.
	innerReport, innerErr := built.second.RunAt(ctx, built.clock.Now())
	if innerErr != nil {
		t.Fatalf("second RunAt() error = %v", innerErr)
	}
	if created := innerReport.Created(); created != 0 {
		t.Errorf("second instance created %d jobs, want none", created)
	}
	if jobs := built.jobCount(t); jobs != 2 {
		t.Errorf("jobs = %d, want the queue unchanged by the second instance", jobs)
	}
	if distinct := built.consecutivePasses(t); distinct != 2 {
		t.Errorf("distinct periods = %d, want one row per period", distinct)
	}
}

// TestTwoInstancesRacingProduceOnePassEach proves the lock is what serializes
// them: both calls start together, exactly one acquires at a time, and the
// periods are queued once.
func TestTwoInstancesRacingProduceOnePassEach(t *testing.T) {
	built := newLockerHarness(t, monthly(t))
	ctx := context.Background()
	start := make(chan struct{})
	reports := make([]jobsapp.Report, 2)
	errs := make([]error, 2)
	var group sync.WaitGroup
	for index, scheduler := range []*jobsapp.Scheduler{built.first, built.second} {
		group.Add(1)
		go func(index int, scheduler *jobsapp.Scheduler) {
			defer group.Done()
			<-start
			reports[index], errs[index] = scheduler.RunAt(ctx, built.clock.Now())
		}(index, scheduler)
	}
	close(start)
	group.Wait()

	held := 0
	for index, err := range errs {
		if err != nil {
			t.Fatalf("instance %d error = %v", index, err)
		}
		if reports[index].LockHeld {
			held++
		}
	}
	// Either both ran one after the other (both held the lock, the second
	// finding the work queued) or one was refused while the other ran. What
	// must never happen is duplicated periods.
	if held == 0 {
		t.Error("no instance held the lock")
	}
	if jobs := built.jobCount(t); jobs != 2 {
		t.Errorf("jobs = %d, want one row per period regardless of who ran", jobs)
	}
	for _, report := range reports {
		if !report.LockHeld && len(report.Schedules) != 0 {
			t.Error("a refused instance reported work")
		}
	}
}

// TestTheLockIsReleasedByTheTransaction proves the failure mode that matters:
// a crashed pass cannot leave maintenance locked out. The lock is
// transaction-scoped, so an error rolls the whole pass back and frees it.
func TestTheLockIsReleasedByTheTransaction(t *testing.T) {
	built := newLockerHarness(t, monthly(t))
	ctx := context.Background()
	manager := platformpg.NewTxManager(built.pool)

	refused := errors.New("injected failure inside the pass")
	locker, err := jobsrepo.NewLocker(manager)
	if err != nil {
		t.Fatalf("NewLocker() error = %v", err)
	}
	acquired, err := locker.WithLock(ctx, func(context.Context) error { return refused })
	if !acquired || !errors.Is(err, refused) {
		t.Fatalf("WithLock() = %v, %v; want the failure surfaced while holding the lock", acquired, err)
	}
	// The next pass takes it again: nothing was left behind.
	report, err := built.first.RunAt(ctx, built.clock.Now())
	if err != nil {
		t.Fatalf("RunAt() after a failed pass error = %v", err)
	}
	if !report.LockHeld {
		t.Error("the lock stayed held after the transaction ended")
	}
	if jobs := built.jobCount(t); jobs != 2 {
		t.Errorf("jobs = %d, want the recovered and current periods", jobs)
	}
}

// TestAMissedPeriodIsRecoveredAgainstRealPostgreSQL closes the loop between the
// cadence arithmetic and the durable queue: the periods the pass queues are the
// ones the queue resolves as already existing.
func TestAMissedPeriodIsRecoveredAgainstRealPostgreSQL(t *testing.T) {
	// The recovery window has to reach across the downtime for the periods in
	// between to come back: this schedule recovers three, which covers the
	// quarter the instance was away.
	built := newLockerHarness(t, []jobsapp.ScheduleSpec{{
		Type: domain.TypeINKGrantMonthly, Version: 1, Interval: domain.IntervalMonthly, CatchUp: 3,
	}})
	ctx := context.Background()
	built.clock.now = time.Date(2026, 6, 15, 3, 0, 0, 0, time.UTC)
	if _, err := built.first.RunAt(ctx, built.clock.Now()); err != nil {
		t.Fatalf("June pass error = %v", err)
	}
	built.clock.now = time.Date(2026, 9, 18, 3, 0, 0, 0, time.UTC)
	report, err := built.second.RunAt(ctx, built.clock.Now())
	if err != nil {
		t.Fatalf("September pass error = %v", err)
	}
	if report.Schedules[0].Current.String() != "2026-09" {
		t.Errorf("current period = %q, want 2026-09", report.Schedules[0].Current)
	}
	if created := report.Created(); created != 3 {
		t.Errorf("created = %d, want the three months the instance was away", created)
	}
	rows, err := built.pool.Query(ctx, "SELECT idempotency_key FROM app.jobs WHERE idempotency_key LIKE 'schedule:%' ORDER BY idempotency_key")
	if err != nil {
		t.Fatalf("query keys: %v", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan key: %v", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate keys: %v", err)
	}
	joined := strings.Join(keys, ",")
	for _, period := range []string{"2026-07", "2026-08", "2026-09"} {
		if !strings.Contains(joined, "schedule:ink_grant_monthly:"+period) {
			t.Errorf("period %s is missing from %v", period, keys)
		}
	}
	if len(keys) != len(unique(keys)) {
		t.Errorf("keys = %v, want no duplicated period", keys)
	}
	// Every scheduled row carries the period it belongs to, which is what
	// lets the handler run the work of the period instead of the work of
	// today.
	payloads, err := scheduledPayloads(ctx, built.pool)
	if err != nil {
		t.Fatalf("read payloads: %v", err)
	}
	for key, payload := range payloads {
		period := key[strings.LastIndex(key, ":")+1:]
		if !strings.Contains(payload, period) {
			t.Errorf("payload of %s = %s, want the period", key, payload)
		}
	}
}

func scheduledPayloads(ctx context.Context, pool *pgxpool.Pool) (map[string]string, error) {
	rows, err := pool.Query(ctx, "SELECT idempotency_key, parameters::text FROM app.jobs WHERE idempotency_key LIKE 'schedule:%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var key, payload string
		if err := rows.Scan(&key, &payload); err != nil {
			return nil, err
		}
		out[key] = payload
	}
	return out, rows.Err()
}

func unique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// TestNewLockerRequiresTheManager keeps the adapter from being wired without
// the transaction it derives its safety from.
func TestNewLockerRequiresTheManager(t *testing.T) {
	if _, err := jobsrepo.NewLocker(nil); err == nil {
		t.Error("NewLocker(nil) error = nil, want a refusal")
	}
	var locker *jobsrepo.Locker
	if _, err := locker.WithLock(context.Background(), func(context.Context) error { return nil }); err == nil {
		t.Error("a nil locker accepted a pass")
	}
}
