package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	jobsrepo "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// testClock is a mutable stub clock: lease windows and retry instants are
// driven explicitly instead of by wall time.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type queueHarness struct {
	repo  *jobsrepo.Repository
	clock *testClock

	enqueue  *jobsapp.EnqueueUseCase
	lease    *jobsapp.LeaseUseCase
	complete *jobsapp.CompleteUseCase
	fail     *jobsapp.FailUseCase
	recover  *jobsapp.RecoverExpiredLeasesUseCase
}

func newQueueHarness(t *testing.T) (*queueHarness, *dbtest.TestDB) {
	t.Helper()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	repo := jobsrepo.NewRepository(pool)
	clock := &testClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	enqueue, err := jobsapp.NewEnqueueUseCase(repo, clock)
	if err != nil {
		t.Fatalf("enqueue use case: %v", err)
	}
	lease, err := jobsapp.NewLeaseUseCase(repo, clock)
	if err != nil {
		t.Fatalf("lease use case: %v", err)
	}
	complete, err := jobsapp.NewCompleteUseCase(repo, clock)
	if err != nil {
		t.Fatalf("complete use case: %v", err)
	}
	fail, err := jobsapp.NewFailUseCase(repo, clock)
	if err != nil {
		t.Fatalf("fail use case: %v", err)
	}
	recoverLeases, err := jobsapp.NewRecoverExpiredLeasesUseCase(repo, clock)
	if err != nil {
		t.Fatalf("recover use case: %v", err)
	}

	return &queueHarness{
		repo: repo, clock: clock,
		enqueue: enqueue, lease: lease, complete: complete, fail: fail, recover: recoverLeases,
	}, db
}

func (h *queueHarness) mustEnqueue(t *testing.T, payload string, maxAttempts int) *jobsapp.EnqueueResult {
	t.Helper()
	result, err := h.enqueue.Enqueue(context.Background(), jobsapp.EnqueueCommand{
		Type:        jobsdomain.TypeEmailDelivery,
		Version:     1,
		Payload:     []byte(payload),
		MaxAttempts: maxAttempts,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return result
}

func TestQueueEnqueueIsIdempotent(t *testing.T) {
	h, _ := newQueueHarness(t)
	ctx := context.Background()

	first := h.mustEnqueue(t, `{"account_id":"a1"}`, 0)
	if first.Replayed {
		t.Error("first enqueue must not be a replay")
	}
	if first.Job.State != jobsdomain.StateQueued {
		t.Errorf("state = %q, want queued", first.Job.State)
	}
	if first.Job.MaxAttempts != jobsdomain.DefaultMaxAttempts {
		t.Errorf("max attempts = %d, want default", first.Job.MaxAttempts)
	}

	// Repeating the same key resolves the original job instead of creating a
	// second one.
	replay, err := h.enqueue.Enqueue(ctx, jobsapp.EnqueueCommand{
		Type:           jobsdomain.TypeEmailDelivery,
		Version:        1,
		Payload:        []byte(`{"account_id":"a1"}`),
		IdempotencyKey: "email:a1:verify",
	})
	if err != nil {
		t.Fatalf("keyed enqueue: %v", err)
	}
	again, err := h.enqueue.Enqueue(ctx, jobsapp.EnqueueCommand{
		Type:           jobsdomain.TypeEmailDelivery,
		Version:        1,
		Payload:        []byte(`{"account_id":"a1"}`),
		IdempotencyKey: "email:a1:verify",
	})
	if err != nil {
		t.Fatalf("keyed replay: %v", err)
	}
	if !again.Replayed {
		t.Error("second keyed enqueue must be a replay")
	}
	if again.Job.ID != replay.Job.ID {
		t.Errorf("replay returned a different job: %s vs %s", again.Job.ID, replay.Job.ID)
	}

	// A different key is a different job.
	other, err := h.enqueue.Enqueue(ctx, jobsapp.EnqueueCommand{
		Type:           jobsdomain.TypeEmailDelivery,
		Version:        1,
		Payload:        []byte(`{"account_id":"a1"}`),
		IdempotencyKey: "email:a1:reset",
	})
	if err != nil {
		t.Fatalf("distinct key: %v", err)
	}
	if other.Job.ID == replay.Job.ID {
		t.Error("a distinct key must create a distinct job")
	}

	// An invalid payload never reaches the queue.
	if _, err := h.enqueue.Enqueue(ctx, jobsapp.EnqueueCommand{
		Type: jobsdomain.TypeEmailDelivery, Version: 1, Payload: []byte(`[]`),
	}); !errors.Is(err, jobsdomain.ErrPayloadNotObject) {
		t.Errorf("invalid payload: %v", err)
	}
}

func TestQueueLeaseRespectsAvailability(t *testing.T) {
	h, _ := newQueueHarness(t)
	ctx := context.Background()

	// A job scheduled in the future is not claimable yet.
	future := h.clock.now.Add(time.Hour)
	if _, err := h.enqueue.Enqueue(ctx, jobsapp.EnqueueCommand{
		Type: jobsdomain.TypeEmailDelivery, Version: 1,
		Payload: []byte(`{}`), AvailableAt: &future,
	}); err != nil {
		t.Fatalf("deferred enqueue: %v", err)
	}
	job, err := h.lease.Lease(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if job != nil {
		t.Fatalf("a future job must not be leased, got %s", job.ID)
	}

	// Once its instant arrives, it is claimable.
	h.clock.advance(2 * time.Hour)
	job, err = h.lease.Lease(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("lease after availability: %v", err)
	}
	if job == nil {
		t.Fatal("the job should be claimable once available")
	}
	if job.State != jobsdomain.StateLeased || job.Attempts != 1 {
		t.Errorf("leased job = state %q attempts %d", job.State, job.Attempts)
	}
	if job.LeaseOwner != "worker-1" {
		t.Errorf("lease owner = %q", job.LeaseOwner)
	}
}

// TestQueueMultiworkerNeverLeasesTheSameJob is the concurrency proof: many
// workers claiming at once each get a distinct job (FOR UPDATE SKIP LOCKED),
// and together they drain the queue exactly once.
func TestQueueMultiworkerNeverLeasesTheSameJob(t *testing.T) {
	h, _ := newQueueHarness(t)
	ctx := context.Background()

	const jobs = 24
	const workers = 8
	for i := 0; i < jobs; i++ {
		h.mustEnqueue(t, `{"account_id":"a1"}`, 0)
	}

	var (
		mu       sync.Mutex
		seen     = map[string]int{}
		total    int
		failures []error
	)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			owner := fmt.Sprintf("worker-%d", worker)
			for {
				job, err := h.lease.Lease(ctx, owner, time.Minute)
				if err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
					return
				}
				if job == nil {
					return
				}
				mu.Lock()
				seen[job.ID]++
				total++
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	for _, err := range failures {
		t.Errorf("worker error: %v", err)
	}
	if total != jobs {
		t.Errorf("leased %d jobs, want %d", total, jobs)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("job %s was leased %d times, want 1", id, count)
		}
	}

	// The queue is drained: another claim finds nothing.
	job, err := h.lease.Lease(ctx, "worker-Z", time.Minute)
	if err != nil {
		t.Fatalf("drained lease: %v", err)
	}
	if job != nil {
		t.Errorf("queue should be empty, got %s", job.ID)
	}
}

// TestQueueLeaseRecoveryReclaimsAbandonedWork proves a worker that dies
// without reporting back does not strand work: the expired lease returns the
// job to the queue and another worker claims it.
func TestQueueLeaseRecoveryReclaimsAbandonedWork(t *testing.T) {
	h, _ := newQueueHarness(t)
	ctx := context.Background()

	enqueued := h.mustEnqueue(t, `{"account_id":"a1"}`, 3)
	leased, err := h.lease.Lease(ctx, "worker-dead", 30*time.Second)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if leased == nil {
		t.Fatal("expected a leased job")
	}

	// Before the lease ends nothing is reclaimed.
	reclaimed, err := h.recover.Recover(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if reclaimed != 0 {
		t.Errorf("reclaimed %d before expiry, want 0", reclaimed)
	}

	// After it ends, the sweep returns the job to the queue.
	h.clock.advance(time.Minute)
	reclaimed, err = h.recover.Recover(ctx)
	if err != nil {
		t.Fatalf("recover after expiry: %v", err)
	}
	if reclaimed != 1 {
		t.Errorf("reclaimed %d, want 1", reclaimed)
	}

	recovered, err := h.repo.JobByID(ctx, enqueued.Job.ID)
	if err != nil {
		t.Fatalf("load recovered job: %v", err)
	}
	if recovered.State != jobsdomain.StateQueued {
		t.Errorf("state after recovery = %q, want queued", recovered.State)
	}
	if recovered.LeaseOwner != "" || !recovered.LeasedUntil.IsZero() {
		t.Error("recovered job must carry no lease")
	}
	if recovered.Attempts != 1 {
		t.Errorf("attempts after recovery = %d, want 1 (the attempt was consumed)", recovered.Attempts)
	}

	// Another worker claims it; the previous holder is now stale.
	reclaimedJob, err := h.lease.Lease(ctx, "worker-alive", time.Minute)
	if err != nil {
		t.Fatalf("re-lease: %v", err)
	}
	if reclaimedJob == nil || reclaimedJob.ID != enqueued.Job.ID {
		t.Fatalf("expected the reclaimed job, got %+v", reclaimedJob)
	}
	if _, err := h.complete.Complete(ctx, enqueued.Job.ID, "worker-dead"); !errors.Is(err, jobsapp.ErrLeaseNotHeld) {
		t.Errorf("stale holder must not complete: %v", err)
	}
	if _, err := h.complete.Complete(ctx, enqueued.Job.ID, "worker-alive"); err != nil {
		t.Fatalf("current holder should complete: %v", err)
	}
}

// TestQueueFailureBudgetMovesJobToDead proves the attempt budget is enforced
// and that a poison job leaves the queue instead of blocking it.
func TestQueueFailureBudgetMovesJobToDead(t *testing.T) {
	h, _ := newQueueHarness(t)
	ctx := context.Background()

	poison := h.mustEnqueue(t, `{"account_id":"a1"}`, 2)
	healthy := h.mustEnqueue(t, `{"account_id":"a2"}`, 2)

	// First attempt fails: the job is requeued for a later retry.
	job, err := h.lease.Lease(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}
	if job == nil {
		t.Fatal("expected a job")
	}
	first := job
	retryAt := h.clock.now.Add(5 * time.Minute)
	rawDetail := "postgres://arena:secret@db:5432/arena?sslmode=disable"
	failed, err := h.fail.Fail(ctx, first.ID, "worker-1", jobsdomain.FailureHandlerError, rawDetail, &retryAt)
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if failed.State != jobsdomain.StateQueued {
		t.Errorf("state after first failure = %q, want queued", failed.State)
	}
	if !failed.AvailableAt.Equal(retryAt) {
		t.Errorf("retry at = %v, want %v", failed.AvailableAt, retryAt)
	}
	if failed.LastErrorCode != jobsdomain.FailureHandlerError {
		t.Errorf("error code = %q", failed.LastErrorCode)
	}
	// The detail is redacted by the domain before it reaches the column:
	// the credential and the query string never survive.
	if failed.LastErrorDetail == rawDetail {
		t.Error("failure detail must be sanitized")
	}
	for _, unsafe := range []string{"@", "/", "?", "=", "\"", "\n"} {
		if strings.Contains(failed.LastErrorDetail, unsafe) {
			t.Errorf("redacted detail kept %q: %q", unsafe, failed.LastErrorDetail)
		}
	}

	// The retry instant has not arrived, so the requeued job is not claimable
	// but the healthy one is: a failing job never blocks its peers.
	next, err := h.lease.Lease(ctx, "worker-2", time.Minute)
	if err != nil {
		t.Fatalf("lease healthy: %v", err)
	}
	if next == nil || next.ID != healthy.Job.ID {
		t.Fatalf("expected the healthy job, got %+v", next)
	}
	if _, err := h.complete.Complete(ctx, next.ID, "worker-2"); err != nil {
		t.Fatalf("complete healthy: %v", err)
	}

	// The poison job's retry arrives; the second failure exhausts the budget.
	h.clock.advance(6 * time.Minute)
	retried, err := h.lease.Lease(ctx, "worker-3", time.Minute)
	if err != nil {
		t.Fatalf("lease retry: %v", err)
	}
	if retried == nil || retried.ID != poison.Job.ID {
		t.Fatalf("expected the retried job, got %+v", retried)
	}
	dead, err := h.fail.Fail(ctx, retried.ID, "worker-3", jobsdomain.FailureInvalidPayload, "handler refused payload", nil)
	if err != nil {
		t.Fatalf("fail exhausted: %v", err)
	}
	if dead.State != jobsdomain.StateDead {
		t.Fatalf("state = %q, want dead", dead.State)
	}
	if dead.LastErrorCode != jobsdomain.FailureInvalidPayload {
		t.Errorf("error code = %q, want the controlled payload failure", dead.LastErrorCode)
	}
	if dead.Attempts != dead.MaxAttempts {
		t.Errorf("attempts = %d, max = %d", dead.Attempts, dead.MaxAttempts)
	}

	// The poisoned job left the queue: nothing else is claimable.
	empty, err := h.lease.Lease(ctx, "worker-4", time.Minute)
	if err != nil {
		t.Fatalf("lease after dead: %v", err)
	}
	if empty != nil {
		t.Errorf("dead job must leave the queue, got %s", empty.ID)
	}
}

func TestQueueCompleteAndLookup(t *testing.T) {
	h, _ := newQueueHarness(t)
	ctx := context.Background()

	if _, err := h.repo.JobByID(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, jobsapp.ErrJobNotFound) {
		t.Errorf("unknown job: %v", err)
	}
	if _, err := h.repo.JobByID(ctx, "not-a-uuid"); !errors.Is(err, jobsapp.ErrJobNotFound) {
		t.Errorf("malformed id: %v", err)
	}

	enqueued := h.mustEnqueue(t, `{"account_id":"a1"}`, 0)
	job, err := h.lease.Lease(ctx, "worker-1", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("lease: %v %v", job, err)
	}

	// A different worker cannot complete it.
	if _, err := h.complete.Complete(ctx, job.ID, "worker-2"); !errors.Is(err, jobsapp.ErrLeaseNotHeld) {
		t.Errorf("foreign holder: %v", err)
	}

	done, err := h.complete.Complete(ctx, job.ID, "worker-1")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.State != jobsdomain.StateSucceeded {
		t.Errorf("state = %q, want succeeded", done.State)
	}
	if done.LeaseOwner != "" || !done.LeasedUntil.IsZero() {
		t.Error("a terminal job must carry no lease")
	}

	reloaded, err := h.repo.JobByID(ctx, enqueued.Job.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.State != jobsdomain.StateSucceeded {
		t.Errorf("reloaded state = %q", reloaded.State)
	}

	// A completed job is never leased again.
	nothing, err := h.lease.Lease(ctx, "worker-9", time.Minute)
	if err != nil {
		t.Fatalf("lease after completion: %v", err)
	}
	if nothing != nil {
		t.Errorf("terminal job must not be claimable, got %s", nothing.ID)
	}
}
