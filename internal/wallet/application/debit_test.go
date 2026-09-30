package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

var testClockInstant = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func mustInk(t *testing.T, amount int64) domain.Ink {
	t.Helper()
	ink, err := domain.NewInk(amount)
	if err != nil {
		t.Fatalf("NewInk(%d): %v", amount, err)
	}
	return ink
}

type fakeDebitRepo struct {
	requests []application.DebitRequest
	result   *application.DebitResult
	err      error
}

func (r *fakeDebitRepo) ApplyDebit(_ context.Context, request application.DebitRequest) (*application.DebitResult, error) {
	r.requests = append(r.requests, request)
	if r.err != nil {
		return nil, r.err
	}
	return r.result, nil
}

func validDebitCommand() application.DebitInkCommand {
	return application.DebitInkCommand{
		AccountID:      testAccountID,
		OperationType:  "debit_argument",
		Amount:         3000,
		Reference:      "argument:018f6b2a",
		IdempotencyKey: "argument-publish:018f6b2a",
	}
}

func TestDebitInkUseCaseValidatesInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*application.DebitInkCommand)
		want   error
	}{
		{name: "empty account", mutate: func(cmd *application.DebitInkCommand) { cmd.AccountID = "" }, want: domain.ErrEmptyAccountID},
		{name: "invalid operation type", mutate: func(cmd *application.DebitInkCommand) { cmd.OperationType = "burn_ink" }, want: domain.ErrInvalidOperationType},
		{name: "credit is not a debit", mutate: func(cmd *application.DebitInkCommand) { cmd.OperationType = "credit_free" }, want: domain.ErrNotADebit},
		{name: "negative amount", mutate: func(cmd *application.DebitInkCommand) { cmd.Amount = -1 }, want: domain.ErrNegativeInk},
		{name: "zero amount", mutate: func(cmd *application.DebitInkCommand) { cmd.Amount = 0 }, want: domain.ErrZeroAmount},
		{name: "empty reference", mutate: func(cmd *application.DebitInkCommand) { cmd.Reference = "" }, want: domain.ErrEmptyReference},
		{name: "invalid reference", mutate: func(cmd *application.DebitInkCommand) { cmd.Reference = "bad ref" }, want: domain.ErrInvalidReference},
		{name: "empty key", mutate: func(cmd *application.DebitInkCommand) { cmd.IdempotencyKey = "" }, want: domain.ErrEmptyIdempotencyKey},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeDebitRepo{}
			clock := &fakeClock{now: testClockInstant}
			useCase := application.NewDebitInkUseCase(repo, clock)

			command := validDebitCommand()
			tc.mutate(&command)

			result, err := useCase.Execute(context.Background(), command)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Execute() error = %v, want %v", err, tc.want)
			}
			if result != nil {
				t.Fatal("Execute() must return nil result on validation error")
			}
			if len(repo.requests) != 0 {
				t.Fatalf("repository was called %d times on validation error", len(repo.requests))
			}
		})
	}
}

func TestDebitInkUseCaseRejectsAdminAdjustments(t *testing.T) {
	repo := &fakeDebitRepo{}
	clock := &fakeClock{now: testClockInstant}
	useCase := application.NewDebitInkUseCase(repo, clock)

	command := validDebitCommand()
	command.OperationType = "debit_admin"
	command.IdempotencyKey = "admin-adjust:1"

	if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, domain.ErrAdminOpsRestricted) {
		t.Fatalf("debit_admin Execute() error = %v, want ErrAdminOpsRestricted", err)
	}
	if len(repo.requests) != 0 {
		t.Fatalf("repository was called %d times for a restricted type", len(repo.requests))
	}
}

func TestDebitInkUseCaseBuildsValidatedRequest(t *testing.T) {
	operation, err := domain.ReconstituteOperation(
		domain.OperationID("operation-2"),
		domain.AccountID(testAccountID),
		domain.OperationDebitArgument,
		mustIdempotencyKey(t, "argument-publish:018f6b2a"),
		mustReference(t, "argument:018f6b2a"),
		domain.Reason{},
		domain.AccountID(""),
		testClockInstant,
	)
	if err != nil {
		t.Fatalf("build operation: %v", err)
	}
	allocation, err := domain.AllocateDebit(mustInk(t, 3000), mustInk(t, 5000), mustInk(t, 0))
	if err != nil {
		t.Fatalf("build allocation: %v", err)
	}
	repo := &fakeDebitRepo{result: &application.DebitResult{Operation: *operation, Allocation: allocation}}
	clock := &fakeClock{now: testClockInstant}
	useCase := application.NewDebitInkUseCase(repo, clock)

	result, err := useCase.Execute(context.Background(), validDebitCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed {
		t.Fatal("fresh debit must not be a replay")
	}
	if !result.Allocation.Equals(allocation) {
		t.Errorf("Allocation = %+v, want %+v", result.Allocation, allocation)
	}

	if len(repo.requests) != 1 {
		t.Fatalf("repository calls = %d, want 1", len(repo.requests))
	}
	request := repo.requests[0]
	if request.AccountID.String() != testAccountID {
		t.Errorf("AccountID = %q", request.AccountID)
	}
	if request.OperationType != domain.OperationDebitArgument {
		t.Errorf("OperationType = %q", request.OperationType)
	}
	if request.Amount.Int64() != 3000 {
		t.Errorf("Amount = %d, want 3000", request.Amount.Int64())
	}
	if request.Reference.String() != "argument:018f6b2a" {
		t.Errorf("Reference = %q", request.Reference)
	}
	if request.IdempotencyKey.String() != "argument-publish:018f6b2a" {
		t.Errorf("IdempotencyKey = %q", request.IdempotencyKey)
	}
	if !request.ChangedAt.Equal(clock.now) {
		t.Errorf("ChangedAt = %v, want %v", request.ChangedAt, clock.now)
	}
}

func TestDebitInkUseCasePropagatesReplayInsufficientAndErrors(t *testing.T) {
	operation, err := domain.ReconstituteOperation(
		domain.OperationID("operation-2"),
		domain.AccountID(testAccountID),
		domain.OperationDebitArgument,
		mustIdempotencyKey(t, "argument-publish:018f6b2a"),
		mustReference(t, "argument:018f6b2a"),
		domain.Reason{},
		domain.AccountID(""),
		testClockInstant,
	)
	if err != nil {
		t.Fatalf("build operation: %v", err)
	}
	repo := &fakeDebitRepo{result: &application.DebitResult{Operation: *operation, Replayed: true}}
	clock := &fakeClock{now: testClockInstant}
	useCase := application.NewDebitInkUseCase(repo, clock)

	result, err := useCase.Execute(context.Background(), validDebitCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Replayed || result.Operation.ID().String() != "operation-2" {
		t.Fatalf("result = %+v, want the replayed original operation", result)
	}

	repo.err = domain.ErrInsufficientInk
	if _, err := useCase.Execute(context.Background(), validDebitCommand()); !errors.Is(err, domain.ErrInsufficientInk) {
		t.Fatalf("insufficient error not propagated: %v", err)
	}

	repo.err = application.ErrIdempotencyMismatch
	if _, err := useCase.Execute(context.Background(), validDebitCommand()); !errors.Is(err, application.ErrIdempotencyMismatch) {
		t.Fatalf("mismatch error not propagated: %v", err)
	}
}
