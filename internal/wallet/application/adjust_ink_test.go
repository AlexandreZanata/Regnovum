package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

const testOperatorID = "018f6b2a-0000-7000-8000-000000000099"

type fakeAuthorizer struct {
	allowed map[domain.AccountID]bool
	err     error
	calls   int
	actors  []domain.AccountID
}

func (a *fakeAuthorizer) EnsureAdministrator(_ context.Context, actor domain.AccountID) error {
	a.calls++
	a.actors = append(a.actors, actor)
	if a.err != nil {
		return a.err
	}
	if !a.allowed[actor] {
		return application.ErrNotAuthorized
	}
	return nil
}

type fakeAuditRecorder struct {
	events []application.AdminAdjustmentEvent
	err    error
}

func (r *fakeAuditRecorder) RecordAdminAdjustment(_ context.Context, event application.AdminAdjustmentEvent) error {
	r.events = append(r.events, event)
	return r.err
}

func adminFixture() (*fakeCreditRepo, *fakeDebitRepo, *fakeAuthorizer, *fakeAuditRecorder, *fakeClock) {
	credits := &fakeCreditRepo{}
	debits := &fakeDebitRepo{}
	authorizer := &fakeAuthorizer{allowed: map[domain.AccountID]bool{
		testOperatorID: true,
	}}
	audit := &fakeAuditRecorder{}
	clock := &fakeClock{now: testClockInstant}
	return credits, debits, authorizer, audit, clock
}

func validAdjustCommand() application.AdjustInkCommand {
	return application.AdjustInkCommand{
		ActorAccountID: testOperatorID,
		AccountID:      testAccountID,
		Bucket:         "FREE_INK",
		OperationType:  "credit_admin",
		Amount:         500,
		Reference:      "admin:ticket-42",
		Reason:         "correção manual aprovada no ticket 42",
		IdempotencyKey: "admin-adjust:42",
	}
}

func mustAdminOperation(t *testing.T, operationType domain.OperationType, reason, actor string) domain.Operation {
	t.Helper()
	parsedReason, err := domain.ParseReason(reason)
	if err != nil {
		t.Fatalf("parse reason: %v", err)
	}
	operation, err := domain.ReconstituteOperation(
		domain.OperationID("operation-admin-1"),
		domain.AccountID(testAccountID),
		operationType,
		mustIdempotencyKey(t, "admin-adjust:42"),
		mustReference(t, "admin:ticket-42"),
		parsedReason,
		domain.AccountID(actor),
		testClockInstant,
	)
	if err != nil {
		t.Fatalf("build operation: %v", err)
	}
	return *operation
}

func TestAdjustInkUseCaseNegativeAuthorization(t *testing.T) {
	credits, debits, authorizer, audit, clock := adminFixture()
	authorizer.allowed = map[domain.AccountID]bool{} // actor denied
	useCase := application.NewAdjustInkUseCase(credits, debits, authorizer, audit, clock)

	_, err := useCase.Execute(context.Background(), validAdjustCommand())
	if !errors.Is(err, application.ErrNotAuthorized) {
		t.Fatalf("Execute() error = %v, want ErrNotAuthorized", err)
	}
	if authorizer.calls != 1 || len(authorizer.actors) != 1 || authorizer.actors[0].String() != testOperatorID {
		t.Fatalf("authorizer calls = %+v, want one call with the acting administrator", authorizer.actors)
	}
	if len(credits.requests) != 0 || len(debits.requests) != 0 {
		t.Fatal("ledger was written for a denied actor")
	}
	if len(audit.events) != 0 {
		t.Fatal("audit was recorded for a denied actor")
	}
}

func TestAdjustInkUseCaseValidatesInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*application.AdjustInkCommand)
		want   error
	}{
		{name: "empty actor", mutate: func(cmd *application.AdjustInkCommand) { cmd.ActorAccountID = "" }, want: domain.ErrActorRequired},
		{name: "empty account", mutate: func(cmd *application.AdjustInkCommand) { cmd.AccountID = "" }, want: domain.ErrEmptyAccountID},
		{name: "invalid type", mutate: func(cmd *application.AdjustInkCommand) { cmd.OperationType = "mint_ink" }, want: domain.ErrInvalidOperationType},
		{name: "not an admin type", mutate: func(cmd *application.AdjustInkCommand) { cmd.OperationType = "credit_free" }, want: domain.ErrNotAdminAdjustment},
		{name: "empty reason", mutate: func(cmd *application.AdjustInkCommand) { cmd.Reason = "   " }, want: domain.ErrEmptyReason},
		{name: "invalid reason", mutate: func(cmd *application.AdjustInkCommand) { cmd.Reason = "linha\nquebrada" }, want: domain.ErrInvalidReason},
		{name: "empty reference", mutate: func(cmd *application.AdjustInkCommand) { cmd.Reference = "" }, want: domain.ErrEmptyReference},
		{name: "empty key", mutate: func(cmd *application.AdjustInkCommand) { cmd.IdempotencyKey = "" }, want: domain.ErrEmptyIdempotencyKey},
		{name: "negative amount", mutate: func(cmd *application.AdjustInkCommand) { cmd.Amount = -1 }, want: domain.ErrNegativeInk},
		{name: "zero amount", mutate: func(cmd *application.AdjustInkCommand) { cmd.Amount = 0 }, want: domain.ErrZeroAmount},
		{name: "invalid credit bucket", mutate: func(cmd *application.AdjustInkCommand) { cmd.Bucket = "GOLD_INK" }, want: domain.ErrInvalidBucket},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			credits, debits, authorizer, audit, clock := adminFixture()
			useCase := application.NewAdjustInkUseCase(credits, debits, authorizer, audit, clock)

			command := validAdjustCommand()
			tc.mutate(&command)

			result, err := useCase.Execute(context.Background(), command)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Execute() error = %v, want %v", err, tc.want)
			}
			if result != nil {
				t.Fatal("validation failure must return no result")
			}
			if authorizer.calls != 0 {
				t.Errorf("authorizer was called %d times before validation completed", authorizer.calls)
			}
			if len(credits.requests) != 0 || len(debits.requests) != 0 || len(audit.events) != 0 {
				t.Fatal("validation failure must not write or audit")
			}
		})
	}
}

func TestAdjustInkUseCaseCreditPositive(t *testing.T) {
	credits, debits, authorizer, audit, clock := adminFixture()
	operation := mustAdminOperation(t, domain.OperationCreditAdmin, "correção manual aprovada no ticket 42", testOperatorID)
	credits.result = &application.CreditResult{Operation: operation}
	useCase := application.NewAdjustInkUseCase(credits, debits, authorizer, audit, clock)

	result, err := useCase.Execute(context.Background(), validAdjustCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Operation.Type() != domain.OperationCreditAdmin {
		t.Fatalf("result = %+v, want a fresh credit_admin adjustment", result)
	}
	if len(credits.requests) != 1 || len(debits.requests) != 0 {
		t.Fatalf("credit calls = %d, debit calls = %d; want 1/0", len(credits.requests), len(debits.requests))
	}

	request := credits.requests[0]
	if request.OperationType != domain.OperationCreditAdmin {
		t.Errorf("OperationType = %q", request.OperationType)
	}
	if request.Reason.String() != "correção manual aprovada no ticket 42" {
		t.Errorf("Reason = %q", request.Reason)
	}
	if request.ActorAccountID.String() != testOperatorID {
		t.Errorf("ActorAccountID = %q", request.ActorAccountID)
	}
	if request.Bucket != domain.BucketFree || request.Delta != 500 {
		t.Errorf("Bucket/Delta = %q/%d, want FREE_INK/500", request.Bucket, request.Delta)
	}

	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}
	event := audit.events[0]
	if event.Replayed || event.ActorAccountID.String() != testOperatorID || event.AccountID.String() != testAccountID {
		t.Fatalf("audit event = %+v, want a fresh actor/target record", event)
	}
	if event.OperationID != "operation-admin-1" || event.OperationType != domain.OperationCreditAdmin {
		t.Errorf("audit event operation = %q/%q", event.OperationID, event.OperationType)
	}
	if event.Allocation.FromFree().Int64() != 500 || event.Allocation.FromPurchased().Int64() != 0 {
		t.Errorf("audit allocation = %d/%d, want 500/0", event.Allocation.FromFree().Int64(), event.Allocation.FromPurchased().Int64())
	}
	if event.Reason.String() != "correção manual aprovada no ticket 42" || event.IdempotencyKey.String() != "admin-adjust:42" {
		t.Errorf("audit reason/key = %q/%q", event.Reason, event.IdempotencyKey)
	}
	if !event.OccurredAt.Equal(testClockInstant) {
		t.Errorf("audit OccurredAt = %v, want %v", event.OccurredAt, testClockInstant)
	}
}

func TestAdjustInkUseCaseDebitNegative(t *testing.T) {
	credits, debits, authorizer, audit, clock := adminFixture()
	operation := mustAdminOperation(t, domain.OperationDebitAdmin, "reversão de concessão duplicada", testOperatorID)
	allocation := domain.NewAllocation(domain.Ink{}, mustInk(t, 300))
	debits.result = &application.DebitResult{Operation: operation, Allocation: allocation}
	useCase := application.NewAdjustInkUseCase(credits, debits, authorizer, audit, clock)

	command := validAdjustCommand()
	command.OperationType = "debit_admin"
	command.Amount = 300
	command.IdempotencyKey = "admin-adjust:43"

	result, err := useCase.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Operation.Type() != domain.OperationDebitAdmin {
		t.Fatalf("result = %+v, want a fresh debit_admin adjustment", result)
	}
	if !result.Allocation.Equals(allocation) {
		t.Errorf("Allocation = %+v, want 300 from PURCHASED_INK", result.Allocation)
	}
	if len(debits.requests) != 1 || len(credits.requests) != 0 {
		t.Fatalf("debit calls = %d, credit calls = %d; want 1/0", len(debits.requests), len(credits.requests))
	}
	if debits.requests[0].Reason.IsZero() || debits.requests[0].ActorAccountID.IsZero() {
		t.Fatal("admin debit request must carry reason and actor")
	}
	if len(audit.events) != 1 || audit.events[0].OperationType != domain.OperationDebitAdmin {
		t.Fatalf("audit events = %+v, want one debit_admin event", audit.events)
	}
}

func TestAdjustInkUseCaseReplayStillRecordsAudit(t *testing.T) {
	credits, debits, authorizer, audit, clock := adminFixture()
	operation := mustAdminOperation(t, domain.OperationCreditAdmin, "correção manual aprovada no ticket 42", testOperatorID)
	credits.result = &application.CreditResult{Operation: operation, Replayed: true}
	useCase := application.NewAdjustInkUseCase(credits, debits, authorizer, audit, clock)

	result, err := useCase.Execute(context.Background(), validAdjustCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Replayed {
		t.Fatal("result must report the replay")
	}
	if len(audit.events) != 1 || !audit.events[0].Replayed {
		t.Fatalf("audit events = %+v, want one replay record", audit.events)
	}
}

func TestAdjustInkUseCasePropagatesLedgerAndAuditFailures(t *testing.T) {
	credits, debits, authorizer, audit, clock := adminFixture()
	useCase := application.NewAdjustInkUseCase(credits, debits, authorizer, audit, clock)

	credits.err = domain.ErrInsufficientInk
	if _, err := useCase.Execute(context.Background(), validAdjustCommand()); !errors.Is(err, domain.ErrInsufficientInk) {
		t.Fatalf("ledger error not propagated: %v", err)
	}
	if len(audit.events) != 0 {
		t.Fatal("audit must not record a failed adjustment")
	}

	credits.err = nil
	operation := mustAdminOperation(t, domain.OperationCreditAdmin, "correção manual aprovada no ticket 42", testOperatorID)
	credits.result = &application.CreditResult{Operation: operation}
	audit.err = errors.New("audit sink unavailable")
	if _, err := useCase.Execute(context.Background(), validAdjustCommand()); !errors.Is(err, audit.err) {
		t.Fatalf("audit error not propagated: %v", err)
	}
}

func TestAdjustInkUseCaseAuthorizationErrorFromPort(t *testing.T) {
	credits, debits, authorizer, audit, clock := adminFixture()
	authorizer.err = errors.New("identity unavailable")
	useCase := application.NewAdjustInkUseCase(credits, debits, authorizer, audit, clock)

	if _, err := useCase.Execute(context.Background(), validAdjustCommand()); !errors.Is(err, authorizer.err) {
		t.Fatalf("authorizer error not propagated: %v", err)
	}
	if len(credits.requests) != 0 || len(audit.events) != 0 {
		t.Fatal("authorizer failure must not write or audit")
	}
}
