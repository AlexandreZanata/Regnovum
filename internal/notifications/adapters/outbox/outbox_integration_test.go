package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	jobsrepo "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/outbox"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// markerTable stands in for whatever the calling flow writes. The subject of
// these tests is the ordering between that write and the job row, not the
// shape of the domain artifact, so a one-column table says it without dragging
// a module's schema in.
const markerTable = "notifications_test_marker"

// queueIntegration wires the real queue and the real adapter over a
// disposable database.
type queueIntegration struct {
	*harness
	pool   *pgxpool.Pool
	repo   *jobsrepo.Repository
	uow    *platformpg.TxManager
	lease  *jobsapp.LeaseUseCase
	fail   *jobsapp.FailUseCase
	finish *jobsapp.CompleteUseCase
}

func newQueueIntegration(t *testing.T) *queueIntegration {
	t.Helper()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	if _, err := pool.Exec(context.Background(), fmt.Sprintf("CREATE TABLE %s (id text PRIMARY KEY)", markerTable)); err != nil {
		t.Fatalf("create marker table: %v", err)
	}
	clock := fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	repo := jobsrepo.NewRepository(pool)
	enqueue, err := jobsapp.NewEnqueueUseCase(repo, clock)
	if err != nil {
		t.Fatalf("enqueue use case: %v", err)
	}
	lease, err := jobsapp.NewLeaseUseCase(repo, clock)
	if err != nil {
		t.Fatalf("lease use case: %v", err)
	}
	fail, err := jobsapp.NewFailUseCase(repo, clock)
	if err != nil {
		t.Fatalf("fail use case: %v", err)
	}
	finish, err := jobsapp.NewCompleteUseCase(repo, clock)
	if err != nil {
		t.Fatalf("complete use case: %v", err)
	}
	enqueuer, err := outbox.NewEnqueuer(enqueue)
	if err != nil {
		t.Fatalf("NewEnqueuer() error = %v", err)
	}
	directory := &fakeDirectory{}
	notifier, err := application.NewNotifier(directory, enqueuer)
	if err != nil {
		t.Fatalf("NewNotifier() error = %v", err)
	}
	sender := &fakeSender{}
	deliverer, err := application.NewDeliverer(fakeRenderer{}, sender)
	if err != nil {
		t.Fatalf("NewDeliverer() error = %v", err)
	}
	handler, err := outbox.NewHandler(deliverer)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return &queueIntegration{
		harness: &harness{
			enqueuer:  enqueuer,
			directory: directory,
			notifier:  notifier,
			handler:   handler,
			sender:    sender,
		},
		pool:   pool,
		repo:   repo,
		uow:    platformpg.NewTxManager(pool),
		lease:  lease,
		fail:   fail,
		finish: finish,
	}
}

func (q *queueIntegration) count(t *testing.T, table string) int {
	t.Helper()
	var total int
	if err := q.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&total); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return total
}

// insertMarker writes the stand-in domain artifact through the transaction the
// context carries, which is exactly what a module repository does: reaching the
// pool directly would write outside the transaction and prove nothing.
func (q *queueIntegration) insertMarker(ctx context.Context, id string) error {
	tx, ok := platformpg.TxFromContext(ctx)
	if !ok {
		return errors.New("integration: the context carries no transaction")
	}
	_, err := tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id) VALUES ($1)", markerTable), id)
	return err
}

func (q *queueIntegration) jobCount(t *testing.T) int { return q.count(t, "app.jobs") }

func (q *queueIntegration) markerCount(t *testing.T) int { return q.count(t, markerTable) }

// TestEnqueueJoinsTheCallerTransaction is the gate of P15-T04: the job row is
// written by the same transaction that writes the domain effect, so a rolled
// back event leaves no message queued and a committed event always leaves one.
func TestEnqueueJoinsTheCallerTransaction(t *testing.T) {
	built := newQueueIntegration(t)
	ctx := context.Background()
	request := verificationRequest(t)

	rollbackFailure := errors.New("domain write refused")
	err := built.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := built.insertMarker(txCtx, "rolled-back-event"); err != nil {
			return err
		}
		if _, err := built.notifier.Notify(txCtx, request); err != nil {
			return err
		}
		return rollbackFailure
	})
	if !errors.Is(err, rollbackFailure) {
		t.Fatalf("transaction error = %v, want the injected failure", err)
	}
	if jobs := built.jobCount(t); jobs != 0 {
		t.Errorf("jobs after rollback = %d, want 0: a message was queued for an event that did not happen", jobs)
	}
	if markers := built.markerCount(t); markers != 0 {
		t.Errorf("markers after rollback = %d, want 0", markers)
	}

	var work application.DurableWork
	err = built.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := built.insertMarker(txCtx, "committed-event"); err != nil {
			return err
		}
		var inner error
		work, inner = built.notifier.Notify(txCtx, request)
		return inner
	})
	if err != nil {
		t.Fatalf("committed transaction error = %v", err)
	}
	if work.JobID == "" {
		t.Error("the committed event queued no job")
	}
	if jobs := built.jobCount(t); jobs != 1 {
		t.Errorf("jobs after commit = %d, want 1", jobs)
	}
	if markers := built.markerCount(t); markers != 1 {
		t.Errorf("markers after commit = %d, want 1", markers)
	}

	// Repeating the same event resolves the existing row instead of queueing
	// a second message.
	err = built.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		_, inner := built.notifier.Notify(txCtx, request)
		return inner
	})
	if err != nil {
		t.Fatalf("repeated transaction error = %v", err)
	}
	if jobs := built.jobCount(t); jobs != 1 {
		t.Errorf("jobs after the repeat = %d, want 1: a retry duplicated the effect", jobs)
	}
}

// TestAWorkerFailureDoesNotUndoTheCommittedEvent is the other half: by the time
// the worker runs, the event is already durable, and a provider failure only
// returns the job to the queue.
func TestAWorkerFailureDoesNotUndoTheCommittedEvent(t *testing.T) {
	built := newQueueIntegration(t)
	ctx := context.Background()
	var work application.DurableWork
	err := built.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := built.insertMarker(txCtx, "committed-event"); err != nil {
			return err
		}
		var inner error
		work, inner = built.notifier.Notify(txCtx, verificationRequest(t))
		return inner
	})
	if err != nil {
		t.Fatalf("committed transaction error = %v", err)
	}
	if work.JobID == "" {
		t.Fatal("the event queued no job")
	}

	// The provider is down: the handler fails and the worker records the
	// failure, which returns the job to the queue.
	built.sender.err = application.ErrProviderUnavailable
	leased, err := built.lease.Lease(ctx, "worker-1", 30*time.Second)
	if err != nil {
		t.Fatalf("Lease() error = %v", err)
	}
	if leased == nil {
		t.Fatal("Lease() returned no job")
	}
	handlerErr := built.handler.Handle(ctx, leased)
	if !application.IsRetryable(handlerErr) {
		t.Fatalf("Handle() error = %v, want a retryable provider failure", handlerErr)
	}
	retryAt := time.Date(2026, 9, 18, 12, 5, 0, 0, time.UTC)
	failed, err := built.fail.Fail(ctx, leased.ID, "worker-1", jobsdomain.FailureHandlerError, "provider unavailable", &retryAt)
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if failed.State != jobsdomain.StateQueued {
		t.Errorf("state = %q, want it requeued", failed.State)
	}
	if markers := built.markerCount(t); markers != 1 {
		t.Errorf("markers = %d, want the committed event untouched by a delivery failure", markers)
	}
	if jobs := built.jobCount(t); jobs != 1 {
		t.Errorf("jobs = %d, want the single durable row", jobs)
	}

	// The retry succeeds, with the same provider key as the failed attempt,
	// and the event stays exactly as it was.
	built.sender.err = nil
	retryClock := fakeClock{now: time.Date(2026, 9, 18, 12, 6, 0, 0, time.UTC)}
	retryLease, err := jobsapp.NewLeaseUseCase(built.repo, retryClock)
	if err != nil {
		t.Fatalf("lease use case: %v", err)
	}
	again, err := retryLease.Lease(ctx, "worker-1", 30*time.Second)
	if err != nil {
		t.Fatalf("retry Lease() error = %v", err)
	}
	if again == nil {
		t.Fatal("the retry found no due job")
	}
	if again.ID != work.JobID {
		t.Errorf("retry leased %q, want the original job %q", again.ID, work.JobID)
	}
	if err := built.handler.Handle(ctx, again); err != nil {
		t.Fatalf("retry Handle() error = %v", err)
	}
	if _, err := built.finish.Complete(ctx, again.ID, "worker-1"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if markers := built.markerCount(t); markers != 1 {
		t.Errorf("markers after a successful retry = %d, want the event untouched", markers)
	}
	keys := built.sender.deliveredKeys()
	if len(keys) != 2 {
		t.Fatalf("attempts = %d, want the failed one and the retry", len(keys))
	}
	if keys[0] != keys[1] {
		t.Errorf("provider keys = %q and %q, want the retry to be the same logical send", keys[0], keys[1])
	}
	if keys[0] != "job:"+work.JobID {
		t.Errorf("provider key = %q, want it anchored on the queued job", keys[0])
	}
}
