package application_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

var deletionBase = testNow

// deletionStore is a faithful in-memory state machine: account scoping,
// cooldown, cancellation and terminal states behave like the PostgreSQL
// adapter.
type deletionStore struct {
	nextID   int
	requests map[string]*application.DeletionRequest
	active   map[string]string
	executed map[string]bool
}

func newDeletionStore() *deletionStore {
	return &deletionStore{
		requests: map[string]*application.DeletionRequest{},
		active:   map[string]string{},
		executed: map[string]bool{},
	}
}

func (s *deletionStore) CreateDeletionRequest(_ context.Context, accountID domain.AccountID, requestedAt time.Time) (*application.DeletionRequest, bool, error) {
	if active, ok := s.active[accountID.String()]; ok {
		return copyDeletionRequest(s.requests[active]), true, nil
	}
	s.nextID++
	request := &application.DeletionRequest{
		ID:          fmt.Sprintf("request-%d", s.nextID),
		AccountID:   accountID,
		Status:      domain.DeletionStatusRequested,
		RequestedAt: requestedAt,
	}
	s.requests[request.ID] = request
	s.active[accountID.String()] = request.ID
	return copyDeletionRequest(request), false, nil
}

func (s *deletionStore) GetDeletionRequest(_ context.Context, accountID domain.AccountID) (*application.DeletionRequest, error) {
	if active, ok := s.active[accountID.String()]; ok {
		return copyDeletionRequest(s.requests[active]), nil
	}
	var latest *application.DeletionRequest
	for _, request := range s.requests {
		if request.AccountID != accountID {
			continue
		}
		if latest == nil || request.RequestedAt.After(latest.RequestedAt) || (request.RequestedAt.Equal(latest.RequestedAt) && request.ID > latest.ID) {
			latest = request
		}
	}
	if latest == nil {
		return nil, application.ErrDeletionRequestNotFound
	}
	return copyDeletionRequest(latest), nil
}

func (s *deletionStore) CancelDeletionRequest(_ context.Context, accountID domain.AccountID, reason string, canceledAt time.Time) (*application.DeletionRequest, error) {
	active, ok := s.active[accountID.String()]
	if !ok {
		return nil, application.ErrDeletionNotCancellable
	}
	request := s.requests[active]
	request.Status = domain.DeletionStatusCanceled
	request.CanceledAt = &canceledAt
	delete(s.active, accountID.String())
	return copyDeletionRequest(request), nil
}

func (s *deletionStore) ListDueDeletionRequests(_ context.Context, now time.Time) ([]application.DeletionRequest, error) {
	due := make([]application.DeletionRequest, 0)
	for _, request := range s.requests {
		if request.Status != domain.DeletionStatusRequested {
			continue
		}
		if !domain.DeletionExecutable(request.RequestedAt, now) {
			continue
		}
		due = append(due, *copyDeletionRequest(request))
	}
	return due, nil
}

func (s *deletionStore) ExecuteDeletionRequest(_ context.Context, accountID domain.AccountID, executedAt time.Time) error {
	active, ok := s.active[accountID.String()]
	if !ok {
		return application.ErrDeletionNotExecutable
	}
	request := s.requests[active]
	if !domain.DeletionExecutable(request.RequestedAt, executedAt) {
		return application.ErrDeletionNotExecutable
	}
	request.Status = domain.DeletionStatusExecuted
	request.ExecutedAt = &executedAt
	delete(s.active, accountID.String())
	s.executed[accountID.String()] = true
	return nil
}

// snapshot copies the state so the fake unit of work can roll back on error.
func (s *deletionStore) snapshot() (map[string]*application.DeletionRequest, map[string]string, map[string]bool) {
	requests := make(map[string]*application.DeletionRequest, len(s.requests))
	for id, request := range s.requests {
		requests[id] = copyDeletionRequest(request)
	}
	active := make(map[string]string, len(s.active))
	for account, id := range s.active {
		active[account] = id
	}
	executed := make(map[string]bool, len(s.executed))
	for account, done := range s.executed {
		executed[account] = done
	}
	return requests, active, executed
}

func (s *deletionStore) restore(requests map[string]*application.DeletionRequest, active map[string]string, executed map[string]bool) {
	s.requests = requests
	s.active = active
	s.executed = executed
}

func copyDeletionRequest(request *application.DeletionRequest) *application.DeletionRequest {
	copied := *request
	if request.ExecutedAt != nil {
		executed := *request.ExecutedAt
		copied.ExecutedAt = &executed
	}
	if request.CanceledAt != nil {
		canceled := *request.CanceledAt
		copied.CanceledAt = &canceled
	}
	return &copied
}

// deletionAudit records trail facts and can be made to fail, so rollback is
// observable.
type deletionAudit struct {
	events   []application.DeletionAuditEvent
	failNext error
}

func (a *deletionAudit) RecordAccountDeletion(_ context.Context, event application.DeletionAuditEvent) error {
	if a.failNext != nil {
		err := a.failNext
		a.failNext = nil
		return err
	}
	a.events = append(a.events, event)
	return nil
}

// deletionUow mirrors the transaction contract over the in-memory store: a
// failure inside fn restores the snapshot, so rollback is observable.
type deletionUow struct {
	store *deletionStore
}

func (u deletionUow) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	requests, active, executed := u.store.snapshot()
	if err := fn(ctx); err != nil {
		u.store.restore(requests, active, executed)
		return err
	}
	return nil
}

func newDeletionHarness(t *testing.T, now time.Time) (*deletionStore, *deletionAudit, *application.RequestDeletionUseCase, *application.CancelDeletionUseCase, *application.ExecuteDueDeletionsUseCase) {
	t.Helper()
	store := newDeletionStore()
	audit := &deletionAudit{}
	clock := fixedClock{now: now}
	uow := deletionUow{store: store}
	request, err := application.NewRequestDeletionUseCase(store, audit, uow, clock)
	if err != nil {
		t.Fatalf("NewRequestDeletionUseCase: %v", err)
	}
	cancel, err := application.NewCancelDeletionUseCase(store, audit, uow, clock)
	if err != nil {
		t.Fatalf("NewCancelDeletionUseCase: %v", err)
	}
	execute, err := application.NewExecuteDueDeletionsUseCase(store, audit, uow, clock)
	if err != nil {
		t.Fatalf("NewExecuteDueDeletionsUseCase: %v", err)
	}
	return store, audit, request, cancel, execute
}

func TestDeletionJourneyRequestCoolDownExecute(t *testing.T) {
	t.Parallel()

	accountID := domain.AccountID("018f6b2a-0000-7000-8000-0000000000d1")
	store, audit, request, _, execute := newDeletionHarness(t, deletionBase)

	outcome, err := request.Execute(context.Background(), application.RequestDeletionCommand{AccountID: accountID})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if outcome.Replayed || outcome.Request.Status != domain.DeletionStatusRequested {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(audit.events) != 1 || audit.events[0].Action != "account.deletion_requested" {
		t.Fatalf("audit events = %+v", audit.events)
	}

	// Replay: the cooldown never restarts and no second audit event exists.
	replayed, err := request.Execute(context.Background(), application.RequestDeletionCommand{AccountID: accountID})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replayed.Replayed || !replayed.Request.RequestedAt.Equal(deletionBase) {
		t.Fatalf("replay = %+v", replayed)
	}
	if len(audit.events) != 1 {
		t.Fatalf("replay must not duplicate audit events: %+v", audit.events)
	}

	// Inside the cooldown nothing executes.
	inside, err := execute.Execute(context.Background())
	if err != nil {
		t.Fatalf("execute inside cooldown: %v", err)
	}
	if inside.Executed != 0 || store.executed[accountID.String()] {
		t.Fatalf("cooldown must block execution: %+v", inside)
	}

	// Past the cooldown the workflow anonymizes and audits once.
	lateClock := fixedClock{now: deletionBase.Add(domain.DeletionCooldown + time.Minute)}
	lateExecute, err := application.NewExecuteDueDeletionsUseCase(store, audit, deletionUow{store: store}, lateClock)
	if err != nil {
		t.Fatalf("NewExecuteDueDeletionsUseCase: %v", err)
	}
	done, err := lateExecute.Execute(context.Background())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if done.Executed != 1 || !store.executed[accountID.String()] {
		t.Fatalf("execution = %+v", done)
	}
	executedRequest, err := store.GetDeletionRequest(context.Background(), accountID)
	if err != nil {
		t.Fatalf("get executed request: %v", err)
	}
	if executedRequest.Status != domain.DeletionStatusExecuted {
		t.Fatalf("status = %s, want executed", executedRequest.Status)
	}
	last := audit.events[len(audit.events)-1]
	if last.Action != "account.deletion_executed" || last.ReasonCode != "cooling_off_elapsed" {
		t.Fatalf("execution audit = %+v", last)
	}

	// Idempotence: a replayed run finds nothing due.
	again, err := lateExecute.Execute(context.Background())
	if err != nil {
		t.Fatalf("replay execute: %v", err)
	}
	if again.Executed != 0 {
		t.Fatalf("replayed run executed %d, want 0", again.Executed)
	}
}

func TestDeletionCancelInsideWindowAndTerminalRefusal(t *testing.T) {
	t.Parallel()

	accountID := domain.AccountID("018f6b2a-0000-7000-8000-0000000000d2")
	store, audit, request, cancel, _ := newDeletionHarness(t, deletionBase)

	if _, err := request.Execute(context.Background(), application.RequestDeletionCommand{AccountID: accountID}); err != nil {
		t.Fatalf("request: %v", err)
	}

	canceled, err := cancel.Execute(context.Background(), application.CancelDeletionCommand{AccountID: accountID, Reason: "mudei de ideia"})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != domain.DeletionStatusCanceled || canceled.CanceledAt == nil {
		t.Fatalf("canceled = %+v", canceled)
	}
	if len(audit.events) != 2 || audit.events[1].Action != "account.deletion_canceled" {
		t.Fatalf("audit events = %+v", audit.events)
	}
	if _, err := cancel.Execute(context.Background(), application.CancelDeletionCommand{AccountID: accountID}); !errors.Is(err, application.ErrDeletionNotCancellable) {
		t.Fatalf("second cancel error = %v, want ErrDeletionNotCancellable", err)
	}

	// A canceled record can be requested again: the state machine restarts.
	restarted, err := request.Execute(context.Background(), application.RequestDeletionCommand{AccountID: accountID})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if restarted.Replayed {
		t.Fatal("a canceled record must allow a fresh request")
	}

	// A request past its window is no longer cancellable.
	lateClock := fixedClock{now: deletionBase.Add(domain.DeletionCooldown + time.Hour)}
	lateCancel, err := application.NewCancelDeletionUseCase(store, audit, deletionUow{store: store}, lateClock)
	if err != nil {
		t.Fatalf("NewCancelDeletionUseCase: %v", err)
	}
	if _, err := lateCancel.Execute(context.Background(), application.CancelDeletionCommand{AccountID: accountID}); !errors.Is(err, application.ErrDeletionNotCancellable) {
		t.Fatalf("late cancel error = %v, want ErrDeletionNotCancellable", err)
	}
}

func TestDeletionAuditFailureBlocksTransition(t *testing.T) {
	t.Parallel()

	accountID := domain.AccountID("018f6b2a-0000-7000-8000-0000000000d3")
	store, audit, request, _, _ := newDeletionHarness(t, deletionBase)

	// The request transition rolls back when its evidence cannot be
	// recorded: the store stays empty.
	audit.failNext = errors.New("audit unavailable")
	if _, err := request.Execute(context.Background(), application.RequestDeletionCommand{AccountID: accountID}); err == nil {
		t.Fatal("request must fail when the audit record fails")
	}
	if len(store.requests) != 0 {
		t.Fatal("failed audit must roll back the request transition")
	}

	// The same rule holds for execution: the account stays requested.
	if _, err := request.Execute(context.Background(), application.RequestDeletionCommand{AccountID: accountID}); err != nil {
		t.Fatalf("request: %v", err)
	}
	lateClock := fixedClock{now: deletionBase.Add(domain.DeletionCooldown + time.Minute)}
	lateExecute, err := application.NewExecuteDueDeletionsUseCase(store, audit, deletionUow{store: store}, lateClock)
	if err != nil {
		t.Fatalf("NewExecuteDueDeletionsUseCase: %v", err)
	}
	audit.failNext = errors.New("audit unavailable")
	if _, err := lateExecute.Execute(context.Background()); err == nil {
		t.Fatal("execution must fail when the audit record fails")
	}
	rolledBack, err := store.GetDeletionRequest(context.Background(), accountID)
	if err != nil {
		t.Fatalf("get rolled back request: %v", err)
	}
	if rolledBack.Status != domain.DeletionStatusRequested {
		t.Fatalf("status = %s, want requested after rollback", rolledBack.Status)
	}
}

func TestDeletionValidatesInputAndComposition(t *testing.T) {
	t.Parallel()

	store, audit, request, cancel, _ := newDeletionHarness(t, deletionBase)
	accountID := domain.AccountID("018f6b2a-0000-7000-8000-0000000000d4")

	if _, err := request.Execute(context.Background(), application.RequestDeletionCommand{}); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty request error = %v, want ErrEmptyAccountID", err)
	}
	if _, err := cancel.Execute(context.Background(), application.CancelDeletionCommand{}); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty cancel error = %v, want ErrEmptyAccountID", err)
	}
	if _, err := request.Execute(context.Background(), application.RequestDeletionCommand{AccountID: accountID}); err != nil {
		t.Fatalf("request: %v", err)
	}
	tooLong := make([]byte, 501)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	if _, err := cancel.Execute(context.Background(), application.CancelDeletionCommand{AccountID: accountID, Reason: string(tooLong)}); !errors.Is(err, application.ErrInvalidCancelReason) {
		t.Fatalf("long reason error = %v, want ErrInvalidCancelReason", err)
	}

	if _, err := application.NewRequestDeletionUseCase(nil, audit, deletionUow{store: store}, fixedClock{}); !errors.Is(err, application.ErrInvalidDeletionConfig) {
		t.Fatalf("nil repository error = %v, want ErrInvalidDeletionConfig", err)
	}
	if _, err := application.NewRequestDeletionUseCase(store, nil, deletionUow{store: store}, fixedClock{}); !errors.Is(err, application.ErrInvalidDeletionConfig) {
		t.Fatalf("nil audit error = %v, want ErrInvalidDeletionConfig", err)
	}
	if _, err := application.NewRequestDeletionUseCase(store, audit, nil, fixedClock{}); !errors.Is(err, application.ErrInvalidDeletionConfig) {
		t.Fatalf("nil uow error = %v, want ErrInvalidDeletionConfig", err)
	}
	if _, err := application.NewGetDeletionStatusUseCase(nil); !errors.Is(err, application.ErrInvalidDeletionConfig) {
		t.Fatalf("nil status repository error = %v, want ErrInvalidDeletionConfig", err)
	}
}

func TestDeletionStatusRequiresOwner(t *testing.T) {
	t.Parallel()

	accountID := domain.AccountID("018f6b2a-0000-7000-8000-0000000000d5")
	store, _, request, _, _ := newDeletionHarness(t, deletionBase)
	status, err := application.NewGetDeletionStatusUseCase(store)
	if err != nil {
		t.Fatalf("NewGetDeletionStatusUseCase: %v", err)
	}

	if _, err := status.Execute(context.Background(), accountID); !errors.Is(err, application.ErrDeletionRequestNotFound) {
		t.Fatalf("missing request error = %v, want ErrDeletionRequestNotFound", err)
	}
	if _, err := status.Execute(context.Background(), domain.AccountID("")); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}
	if _, err := request.Execute(context.Background(), application.RequestDeletionCommand{AccountID: accountID}); err != nil {
		t.Fatalf("request: %v", err)
	}
	resolved, err := status.Execute(context.Background(), accountID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if resolved.Status != domain.DeletionStatusRequested {
		t.Fatalf("status = %+v", resolved)
	}
}

func TestDeletionCancelReasonBoundaryLength(t *testing.T) {
	t.Parallel()

	accountID := domain.AccountID("018f6b2a-0000-7000-8000-0000000000d3")
	_, _, request, cancel, _ := newDeletionHarness(t, deletionBase)

	if _, err := request.Execute(context.Background(), application.RequestDeletionCommand{AccountID: accountID}); err != nil {
		t.Fatalf("request: %v", err)
	}
	// Exactly 500 characters is the boundary and cancels; 501 refuses.
	if _, err := cancel.Execute(context.Background(), application.CancelDeletionCommand{
		AccountID: accountID, Reason: strings.Repeat("m", 500),
	}); err != nil {
		t.Fatalf("500-char reason refused: %v", err)
	}
}
