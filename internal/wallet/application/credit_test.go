package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

const testAccountID = "018f6b2a-0000-7000-8000-000000000010"

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

type fakeCreditRepo struct {
	requests []application.CreditRequest
	result   *application.CreditResult
	err      error
}

func (r *fakeCreditRepo) ApplyCredit(_ context.Context, request application.CreditRequest) (*application.CreditResult, error) {
	r.requests = append(r.requests, request)
	if r.err != nil {
		return nil, r.err
	}
	return r.result, nil
}

func newCreditFixture() (*fakeCreditRepo, *fakeClock) {
	return &fakeCreditRepo{}, &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
}

func validCreditCommand() application.CreditInkCommand {
	return application.CreditInkCommand{
		AccountID:      testAccountID,
		Bucket:         "FREE_INK",
		OperationType:  "credit_free",
		Amount:         5000,
		Reference:      "free:2026-09",
		IdempotencyKey: "free:2026-09:018f6b2a",
	}
}

func TestCreditInkUseCaseValidatesInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*application.CreditInkCommand)
		want   error
	}{
		{name: "empty account", mutate: func(cmd *application.CreditInkCommand) { cmd.AccountID = "" }, want: domain.ErrEmptyAccountID},
		{name: "invalid bucket", mutate: func(cmd *application.CreditInkCommand) { cmd.Bucket = "GOLD_INK" }, want: domain.ErrInvalidBucket},
		{name: "invalid operation type", mutate: func(cmd *application.CreditInkCommand) { cmd.OperationType = "mint_ink" }, want: domain.ErrInvalidOperationType},
		{name: "debit is not a credit", mutate: func(cmd *application.CreditInkCommand) { cmd.OperationType = "debit_argument" }, want: domain.ErrNotACredit},
		{name: "admin credit is restricted", mutate: func(cmd *application.CreditInkCommand) { cmd.OperationType = "credit_admin" }, want: domain.ErrAdminOpsRestricted},
		{name: "expiry is not a credit", mutate: func(cmd *application.CreditInkCommand) { cmd.OperationType = "expire_free" }, want: domain.ErrNotACredit},
		{name: "negative amount", mutate: func(cmd *application.CreditInkCommand) { cmd.Amount = -1 }, want: domain.ErrNegativeInk},
		{name: "zero amount", mutate: func(cmd *application.CreditInkCommand) { cmd.Amount = 0 }, want: domain.ErrZeroAmount},
		{name: "empty reference", mutate: func(cmd *application.CreditInkCommand) { cmd.Reference = "" }, want: domain.ErrEmptyReference},
		{name: "invalid reference", mutate: func(cmd *application.CreditInkCommand) { cmd.Reference = "bad ref" }, want: domain.ErrInvalidReference},
		{name: "empty key", mutate: func(cmd *application.CreditInkCommand) { cmd.IdempotencyKey = "" }, want: domain.ErrEmptyIdempotencyKey},
		{name: "invalid key", mutate: func(cmd *application.CreditInkCommand) { cmd.IdempotencyKey = "bad key" }, want: domain.ErrInvalidIdempotencyKey},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, clock := newCreditFixture()
			useCase := application.NewCreditInkUseCase(repo, clock)

			command := validCreditCommand()
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

func TestCreditInkUseCaseBuildsValidatedRequest(t *testing.T) {
	repo, clock := newCreditFixture()
	operation, err := domain.ReconstituteOperation(
		domain.OperationID("operation-1"),
		domain.AccountID(testAccountID),
		domain.OperationCreditFree,
		mustIdempotencyKey(t, "free:2026-09:018f6b2a"),
		mustReference(t, "free:2026-09"),
		domain.Reason{},
		domain.AccountID(""),
		clock.now,
	)
	if err != nil {
		t.Fatalf("build operation: %v", err)
	}
	repo.result = &application.CreditResult{Operation: *operation}

	useCase := application.NewCreditInkUseCase(repo, clock)
	result, err := useCase.Execute(context.Background(), validCreditCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result == nil || result.Replayed {
		t.Fatalf("result = %+v, want a fresh operation", result)
	}

	if len(repo.requests) != 1 {
		t.Fatalf("repository calls = %d, want 1", len(repo.requests))
	}
	request := repo.requests[0]
	if request.AccountID.String() != testAccountID {
		t.Errorf("AccountID = %q", request.AccountID)
	}
	if request.Bucket != domain.BucketFree {
		t.Errorf("Bucket = %q", request.Bucket)
	}
	if request.OperationType != domain.OperationCreditFree {
		t.Errorf("OperationType = %q", request.OperationType)
	}
	if request.IdempotencyKey.String() != "free:2026-09:018f6b2a" {
		t.Errorf("IdempotencyKey = %q", request.IdempotencyKey)
	}
	if request.Reference.String() != "free:2026-09" {
		t.Errorf("Reference = %q", request.Reference)
	}
	if request.Delta != 5000 {
		t.Errorf("Delta = %d, want 5000", request.Delta)
	}
	if !request.ChangedAt.Equal(clock.now) {
		t.Errorf("ChangedAt = %v, want %v", request.ChangedAt, clock.now)
	}
}

func TestCreditInkUseCasePropagatesReplayAndErrors(t *testing.T) {
	repo, clock := newCreditFixture()
	useCase := application.NewCreditInkUseCase(repo, clock)

	operation, err := domain.ReconstituteOperation(
		domain.OperationID("operation-1"),
		domain.AccountID(testAccountID),
		domain.OperationCreditFree,
		mustIdempotencyKey(t, "free:2026-09:018f6b2a"),
		mustReference(t, "free:2026-09"),
		domain.Reason{},
		domain.AccountID(""),
		clock.now,
	)
	if err != nil {
		t.Fatalf("build operation: %v", err)
	}
	repo.result = &application.CreditResult{Operation: *operation, Replayed: true}

	result, err := useCase.Execute(context.Background(), validCreditCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Replayed || result.Operation.ID().String() != "operation-1" {
		t.Fatalf("result = %+v, want the replayed original operation", result)
	}

	repo.err = errors.New("storage unavailable")
	if _, err := useCase.Execute(context.Background(), validCreditCommand()); !errors.Is(err, repo.err) {
		t.Fatalf("repository error not propagated: %v", err)
	}

	repo.err = application.ErrIdempotencyMismatch
	if _, err := useCase.Execute(context.Background(), validCreditCommand()); !errors.Is(err, application.ErrIdempotencyMismatch) {
		t.Fatalf("mismatch error not propagated: %v", err)
	}
}

func mustIdempotencyKey(t *testing.T, raw string) domain.IdempotencyKey {
	t.Helper()
	key, err := domain.ParseIdempotencyKey(raw)
	if err != nil {
		t.Fatalf("parse idempotency key %q: %v", raw, err)
	}
	return key
}

func mustReference(t *testing.T, raw string) domain.Reference {
	t.Helper()
	reference, err := domain.ParseReference(raw)
	if err != nil {
		t.Fatalf("parse reference %q: %v", raw, err)
	}
	return reference
}
