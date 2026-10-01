package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubIntentionRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: command validation happens before it
// is ever called, and canned outcomes prove the mappings.
type stubIntentionRepository struct {
	called   int
	requests []application.IdempotentTransferRequest
	result   *application.IdempotentTransferResult
	err      error
}

func (s *stubIntentionRepository) TransferIdempotent(_ context.Context, request application.IdempotentTransferRequest) (*application.IdempotentTransferResult, error) {
	s.called++
	s.requests = append(s.requests, request)
	return s.result, s.err
}

func intentionCommand(key, actor, operation string) application.IdempotentTransferCommand {
	return application.IdempotentTransferCommand{
		Key: key, Actor: actor, Operation: operation,
		FromSeason: domain.CompatSeasonKey,
		FromKind:   "treasury", FromLabel: "main",
		ToSeason: domain.CompatSeasonKey,
		ToKind:   "user", ToLabel: "ana",
		Millis: 250,
	}
}

func TestIdempotentUseCaseRefusesInvalidCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*application.IdempotentTransferCommand)
		err    error
	}{
		{"blank key", func(c *application.IdempotentTransferCommand) { c.Key = "  " }, domain.ErrInvalidIntention},
		{"blank actor", func(c *application.IdempotentTransferCommand) { c.Actor = "" }, domain.ErrInvalidIntention},
		{"blank operation", func(c *application.IdempotentTransferCommand) { c.Operation = "" }, domain.ErrInvalidIntention},
		{"long key", func(c *application.IdempotentTransferCommand) { c.Key = strings.Repeat("k", 129) }, domain.ErrInvalidIntention},
		{"unknown kind", func(c *application.IdempotentTransferCommand) { c.FromKind = "vault" }, domain.ErrUnknownCustody},
		{"blank label", func(c *application.IdempotentTransferCommand) { c.ToLabel = "" }, domain.ErrUnknownCustody},
		{"same custody", func(c *application.IdempotentTransferCommand) { c.ToKind = "treasury"; c.ToLabel = "main" }, domain.ErrSameCustody},
		{"locked source", func(c *application.IdempotentTransferCommand) { c.FromKind = "escrow"; c.FromLabel = "deal" }, domain.ErrUnauthorizedCustody},
		{"negative amount", func(c *application.IdempotentTransferCommand) { c.Millis = -5 }, domain.ErrNegativeMilliInk},
		{"zero amount", func(c *application.IdempotentTransferCommand) { c.Millis = 0 }, domain.ErrInvalidMilliInk},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubIntentionRepository{}
			useCase := application.NewIdempotentTransferUseCase(stub, stubSeasonBooks{})
			cmd := intentionCommand("key-1", "ophelia", "sale")
			test.mutate(&cmd)
			if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, test.err) {
				t.Errorf("Execute = %v, want %v", err, test.err)
			}
			if stub.called != 0 {
				t.Errorf("invalid command reached the repository: validation never touches storage")
			}
		})
	}
}

func TestIdempotentUseCaseBindsHashToPayload(t *testing.T) {
	t.Parallel()

	stub := &stubIntentionRepository{result: &application.IdempotentTransferResult{}}
	useCase := application.NewIdempotentTransferUseCase(stub, stubSeasonBooks{})
	first := intentionCommand("key-1", "ophelia", "sale")
	if _, err := useCase.Execute(context.Background(), first); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	second := first
	second.ToLabel = "bia"
	if _, err := useCase.Execute(context.Background(), second); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(stub.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(stub.requests))
	}
	if stub.requests[0].PayloadHash == "" || stub.requests[0].PayloadHash == stub.requests[1].PayloadHash {
		t.Errorf("payload hashes do not tell changed terms apart: %q vs %q",
			stub.requests[0].PayloadHash, stub.requests[1].PayloadHash)
	}
	third := first
	if _, err := useCase.Execute(context.Background(), third); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if stub.requests[2].PayloadHash != stub.requests[0].PayloadHash {
		t.Errorf("identical payload hashed differently: %q vs %q",
			stub.requests[2].PayloadHash, stub.requests[0].PayloadHash)
	}
}

func TestIdempotentUseCaseMapsRepositoryOutcome(t *testing.T) {
	t.Parallel()

	debited, err := domain.NewMilliInk(250)
	if err != nil {
		t.Fatalf("NewMilliInk(250): %v", err)
	}
	want := &application.IdempotentTransferResult{TransferID: "transfer-id", Debited: debited, Credited: debited}
	stub := &stubIntentionRepository{result: want}
	useCase := application.NewIdempotentTransferUseCase(stub, stubSeasonBooks{})
	got, err := useCase.Execute(context.Background(), intentionCommand("key-1", "ophelia", "sale"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Fatalf("Execute did not return the settled response")
	}

	stub = &stubIntentionRepository{err: domain.ErrIntentionConflict}
	useCase = application.NewIdempotentTransferUseCase(stub, stubSeasonBooks{})
	if _, err := useCase.Execute(context.Background(), intentionCommand("key-1", "ophelia", "sale")); !errors.Is(err, domain.ErrIntentionConflict) {
		t.Fatalf("Execute with a conflicting payload = %v, want ErrIntentionConflict", err)
	}
}
