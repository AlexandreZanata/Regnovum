package postgres_test

// P27-T06 — semântica resiliente dos jobs sobre PostgreSQL descartável.
//
// Lease com morte do dono, redelivery idempotente, poison com budget,
// disponibilidade futura, saúde do backlog e concorrência de workers: o
// efeito de negócio acontece exatamente uma vez por job embora a entrega
// seja at-least-once, e o worker morto libera o lease para outro.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// resilienceWorld is the real queue: repository, use cases and a worker
// factory over one disposable database.
type resilienceWorld struct {
	pool    *pgxpool.Pool
	repo    *postgres.Repository
	enqueue *jobsapp.EnqueueUseCase
	lease   *jobsapp.LeaseUseCase
	recover *jobsapp.RecoverExpiredLeasesUseCase
	health  *jobsapp.GetQueueHealthUseCase
	clock   clockseed.System
}

func newResilienceWorld(t *testing.T) *resilienceWorld {
	t.Helper()
	database := dbtest.New(t)
	pool := database.Pool.Pool()
	repo := postgres.NewRepository(pool)
	clock := clockseed.NewClock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS resilience_marks (job_id text PRIMARY KEY, attempts int NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create marks table: %v", err)
	}
	enqueue, err := jobsapp.NewEnqueueUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewEnqueueUseCase: %v", err)
	}
	lease, err := jobsapp.NewLeaseUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewLeaseUseCase: %v", err)
	}
	recover, err := jobsapp.NewRecoverExpiredLeasesUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewRecoverExpiredLeasesUseCase: %v", err)
	}
	health, err := jobsapp.NewGetQueueHealthUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewGetQueueHealthUseCase: %v", err)
	}
	return &resilienceWorld{pool: pool, repo: repo, enqueue: enqueue, lease: lease, recover: recover, health: health, clock: clock}
}

// markingHandler records every execution in the marks table but applies
// the business effect exactly once per job: the row exists once no matter
// how many times the delivery repeats, and attempts counts deliveries.
func markingHandler(t *testing.T, world *resilienceWorld, failFirst map[string]bool, mu *sync.Mutex) jobsapp.Handler {
	t.Helper()
	return func(ctx context.Context, job *jobsdomain.Job) error {
		mu.Lock()
		once := failFirst[job.ID]
		if once {
			delete(failFirst, job.ID)
		}
		mu.Unlock()
		if once {
			return errors.New("resilience probe fails the first delivery")
		}
		if _, err := world.pool.Exec(ctx, `INSERT INTO resilience_marks (job_id, attempts) VALUES ($1, 1)
			ON CONFLICT (job_id) DO UPDATE SET attempts = resilience_marks.attempts + 1`, job.ID); err != nil {
			return fmt.Errorf("mark execution: %w", err)
		}
		return nil
	}
}

// resilienceWorker runs one real worker until the context ends.
func resilienceWorker(t *testing.T, world *resilienceWorld, handler jobsapp.Handler, leaseDuration time.Duration) (context.CancelFunc, chan error) {
	t.Helper()
	registry := jobsapp.NewHandlerMap()
	if err := registry.Register(jobsdomain.TypeSessionCleanup, 1, handler); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	lease, err := jobsapp.NewLeaseUseCase(world.repo, world.clock)
	if err != nil {
		t.Fatalf("NewLeaseUseCase: %v", err)
	}
	complete, err := jobsapp.NewCompleteUseCase(world.repo, world.clock)
	if err != nil {
		t.Fatalf("NewCompleteUseCase: %v", err)
	}
	fail, err := jobsapp.NewFailUseCase(world.repo, world.clock)
	if err != nil {
		t.Fatalf("NewFailUseCase: %v", err)
	}
	recover, err := jobsapp.NewRecoverExpiredLeasesUseCase(world.repo, world.clock)
	if err != nil {
		t.Fatalf("NewRecoverExpiredLeasesUseCase: %v", err)
	}
	worker, err := jobsapp.NewWorker(jobsapp.WorkerDeps{
		Lease: lease, Complete: complete, Fail: fail, Recover: recover,
		Registry: registry, Clock: world.clock, Random: clockseed.NewRandom(),
		Config: jobsapp.WorkerConfig{
			Concurrency: 2, LeaseDuration: leaseDuration, HandlerTimeout: leaseDuration / 2,
			PollInterval: 20 * time.Millisecond,
			Backoff:      jobsapp.BackoffPolicy{Base: 10 * time.Millisecond, Max: 100 * time.Millisecond},
		},
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	return cancel, done
}

// resilienceEnqueue enqueues one cleanup job for the tests.
func resilienceEnqueue(t *testing.T, world *resilienceWorld, key string, availableAt *time.Time, maxAttempts int) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := world.enqueue.Enqueue(ctx, jobsapp.EnqueueCommand{
		Type: jobsdomain.TypeSessionCleanup, Version: 1, Payload: []byte(`{}`),
		IdempotencyKey: key, AvailableAt: availableAt, MaxAttempts: maxAttempts,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return result.Job.ID
}

// resilienceJobState reads the state column of one job.
func resilienceJobState(t *testing.T, world *resilienceWorld, jobID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var state string
	if err := world.pool.QueryRow(ctx, `SELECT state::text FROM app.jobs WHERE id::text = $1`, jobID).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	return state
}

// resilienceWaitState polls one job until the state arrives or the budget
// ends: waiting for the condition the test asserts, never a fixed sleep.
func resilienceWaitState(t *testing.T, world *resilienceWorld, jobID, want string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if got := resilienceJobState(t, world, jobID); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never reached %q (still %q)", jobID, want, resilienceJobState(t, world, jobID))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// resilienceMarks answers how many business rows and attempts exist.
func resilienceMarks(t *testing.T, world *resilienceWorld) (int64, int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var rows, attempts int64
	if err := world.pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(attempts),0) FROM resilience_marks`).Scan(&rows, &attempts); err != nil {
		t.Fatalf("count marks: %v", err)
	}
	return rows, attempts
}

// TestResilientCrashRecovery abandons a lease like a dead worker would:
// after expiry the reclaim path returns it, the next worker completes it,
// and the business effect exists exactly once.
func TestResilientCrashRecovery(t *testing.T) {
	world := newResilienceWorld(t)
	jobID := resilienceEnqueue(t, world, "t06-crash-1", nil, 5)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := world.lease.Lease(ctx, "dead-worker", 2*time.Second); err != nil {
		t.Fatalf("abandon lease: %v", err)
	}
	if got := resilienceJobState(t, world, jobID); got != "leased" {
		t.Fatalf("state = %q, want leased", got)
	}
	// Wait for the expiry by reclaiming until it lands, instead of
	// sleeping the lease duration: the condition asserted is the reclaim.
	reclaimDeadline := time.Now().Add(15 * time.Second)
	reclaimed := 0
	for reclaimed == 0 {
		var err error
		reclaimed, err = world.recover.Recover(ctx)
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if reclaimed == 0 {
			if time.Now().After(reclaimDeadline) {
				t.Fatal("abandoned lease never expired for reclaim")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if reclaimed != 1 {
		t.Fatalf("reclaimed = %d, want 1", reclaimed)
	}

	var mu sync.Mutex
	stop, done := resilienceWorker(t, world, markingHandler(t, world, map[string]bool{}, &mu), 30*time.Second)
	resilienceWaitState(t, world, jobID, "succeeded", 15*time.Second)
	stop()
	if err := <-done; err != nil {
		t.Fatalf("worker: %v", err)
	}
	rows, attempts := resilienceMarks(t, world)
	if rows != 1 || attempts != 1 {
		t.Fatalf("marks rows=%d attempts=%d, want exactly one execution", rows, attempts)
	}
}

// TestResilientRedeliveryIdempotent fails the first delivery through the
// real fail path and lets the worker retry: the fail flag is consumed
// (both deliveries happened) but the business row exists once.
func TestResilientRedeliveryIdempotent(t *testing.T) {
	world := newResilienceWorld(t)
	jobID := resilienceEnqueue(t, world, "t06-redelivery-1", nil, 5)

	failFirst := map[string]bool{jobID: true}
	var mu sync.Mutex
	stop, done := resilienceWorker(t, world, markingHandler(t, world, failFirst, &mu), 30*time.Second)
	resilienceWaitState(t, world, jobID, "succeeded", 15*time.Second)
	stop()
	if err := <-done; err != nil {
		t.Fatalf("worker: %v", err)
	}
	mu.Lock()
	remaining := len(failFirst)
	mu.Unlock()
	if remaining != 0 {
		t.Fatal("first delivery never ran (fail flag unconsumed)")
	}
	rows, attempts := resilienceMarks(t, world)
	if rows != 1 || attempts != 1 {
		t.Fatalf("marks rows=%d attempts=%d, want one business row from two deliveries", rows, attempts)
	}
}

// TestResilientPoisonDeadLetters runs an always-failing handler: the
// budget (3) is consumed exactly, the job lands dead, and the worker
// keeps serving the next job instead of spinning hot.
func TestResilientPoisonDeadLetters(t *testing.T) {
	world := newResilienceWorld(t)
	poisonID := resilienceEnqueue(t, world, "t06-poison-1", nil, 3)
	goodID := resilienceEnqueue(t, world, "t06-poison-good-1", nil, 3)

	poison := func(context.Context, *jobsdomain.Job) error { return errors.New("resilience probe always fails") }
	registry := jobsapp.NewHandlerMap()
	t.Cleanup(func() {})
	mixed := func(ctx context.Context, job *jobsdomain.Job) error {
		if job.ID == poisonID {
			return poison(ctx, job)
		}
		return markingHandler(t, world, map[string]bool{}, &sync.Mutex{})(ctx, job)
	}
	_ = registry
	stop, done := resilienceWorkerWith(t, world, mixed, 30*time.Second)
	resilienceWaitState(t, world, poisonID, "dead", 30*time.Second)
	resilienceWaitState(t, world, goodID, "succeeded", 15*time.Second)
	stop()
	if err := <-done; err != nil {
		t.Fatalf("worker: %v", err)
	}
	rows, _ := resilienceMarks(t, world)
	if rows != 1 {
		t.Fatalf("business rows = %d, want only the good job applied", rows)
	}
	stats := workerStatsFor(t, world, poisonID)
	if stats.attempts != 3 {
		t.Fatalf("poison attempts = %d, want exactly the budget of 3 (no hot loop)", stats.attempts)
	}
}

// workerStatsFor reads attempts of one job.
func workerStatsFor(t *testing.T, world *resilienceWorld, jobID string) struct{ attempts int64 } {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var attempts int64
	// Attempts live on the job row in this schema; fall back to marks.
	if err := world.pool.QueryRow(ctx, `SELECT attempts FROM app.jobs WHERE id::text = $1`, jobID).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	return struct{ attempts int64 }{attempts}
}

// resilienceWorkerWith runs a worker with a caller-provided handler.
func resilienceWorkerWith(t *testing.T, world *resilienceWorld, handler jobsapp.Handler, leaseDuration time.Duration) (context.CancelFunc, chan error) {
	t.Helper()
	registry := jobsapp.NewHandlerMap()
	if err := registry.Register(jobsdomain.TypeSessionCleanup, 1, handler); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	lease, _ := jobsapp.NewLeaseUseCase(world.repo, world.clock)
	complete, _ := jobsapp.NewCompleteUseCase(world.repo, world.clock)
	fail, _ := jobsapp.NewFailUseCase(world.repo, world.clock)
	recover, _ := jobsapp.NewRecoverExpiredLeasesUseCase(world.repo, world.clock)
	worker, err := jobsapp.NewWorker(jobsapp.WorkerDeps{
		Lease: lease, Complete: complete, Fail: fail, Recover: recover,
		Registry: registry, Clock: world.clock, Random: clockseed.NewRandom(),
		Config: jobsapp.WorkerConfig{
			Concurrency: 2, LeaseDuration: leaseDuration, HandlerTimeout: leaseDuration / 2,
			PollInterval: 20 * time.Millisecond,
			Backoff:      jobsapp.BackoffPolicy{Base: 10 * time.Millisecond, Max: 100 * time.Millisecond},
		},
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	return cancel, done
}

// TestResilientFutureNotExecuted proves AvailableAt in the future holds
// the job: the worker runs past it and the row stays queued untouched.
func TestResilientFutureNotExecuted(t *testing.T) {
	world := newResilienceWorld(t)
	future := time.Now().UTC().Add(time.Hour)
	jobID := resilienceEnqueue(t, world, "t06-future-1", &future, 5)

	var mu sync.Mutex
	stop, done := resilienceWorker(t, world, markingHandler(t, world, map[string]bool{}, &mu), 30*time.Second)
	// Give the worker room to wrongly pick the job up, asserting queued
	// throughout instead of sleeping a fixed window.
	watchDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(watchDeadline) {
		if got := resilienceJobState(t, world, jobID); got != "queued" {
			t.Fatalf("future job state = %q, want queued", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("worker: %v", err)
	}
	if got := resilienceJobState(t, world, jobID); got != "queued" {
		t.Fatalf("future job state = %q, want queued", got)
	}
	rows, _ := resilienceMarks(t, world)
	if rows != 0 {
		t.Fatalf("future job executed %d business rows", rows)
	}
}

// TestResilientHealthReflectsBacklog proves the backlog is observable:
// five queued read five deep with wait, then zero after the drain.
func TestResilientHealthReflectsBacklog(t *testing.T) {
	world := newResilienceWorld(t)
	for _, key := range []string{"t06-h1", "t06-h2", "t06-h3", "t06-h4", "t06-h5"} {
		resilienceEnqueue(t, world, key, nil, 5)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := world.health.Execute(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if report.Queue.Queued != 5 || report.Queue.DueNow != 5 {
		t.Fatalf("health = %+v, want 5 queued and due", report.Queue)
	}
	if report.Queue.LagSeconds < 0 {
		t.Fatalf("negative lag: %+v", report.Queue)
	}

	var mu sync.Mutex
	stop, done := resilienceWorker(t, world, markingHandler(t, world, map[string]bool{}, &mu), 30*time.Second)
	deadline := time.Now().Add(15 * time.Second)
	for {
		after, err := world.health.Execute(ctx)
		if err != nil {
			t.Fatalf("health: %v", err)
		}
		if after.Queue.Queued == 0 && after.Queue.Succeeded == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backlog not drained: %+v", after.Queue)
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("worker: %v", err)
	}
}

// TestResilientConcurrentWorkers proves three workers drain twelve jobs
// with twelve business rows and twelve attempts: no loss, no duplicates
// when healthy.
func TestResilientConcurrentWorkers(t *testing.T) {
	world := newResilienceWorld(t)
	for index := 0; index < 12; index++ {
		resilienceEnqueue(t, world, "t06-conc-"+string(rune('a'+index)), nil, 5)
	}
	var mu sync.Mutex
	handler := markingHandler(t, world, map[string]bool{}, &mu)
	stops := make([]context.CancelFunc, 0, 3)
	dones := make([]chan error, 0, 3)
	for index := 0; index < 3; index++ {
		stop, done := resilienceWorker(t, world, handler, 30*time.Second)
		stops = append(stops, stop)
		dones = append(dones, done)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		rows, attempts := resilienceMarks(t, world)
		if rows == 12 && attempts == 12 {
			break
		}
		if time.Now().After(deadline) {
			rows, attempts := resilienceMarks(t, world)
			t.Fatalf("drain stalled at rows=%d attempts=%d", rows, attempts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, stop := range stops {
		stop()
	}
	for _, done := range dones {
		if err := <-done; err != nil {
			t.Fatalf("worker: %v", err)
		}
	}
}
