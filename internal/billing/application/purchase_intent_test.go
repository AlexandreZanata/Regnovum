package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// stubPurchaseIntentRepository stands in for the PostgreSQL adapter
// where no database behavior is under test: envelope validation
// happens before it is ever called, and canned outcomes prove the
// mapping.
type stubPurchaseIntentRepository struct {
	called int
	result *application.PurchaseIntentResult
	err    error
}

func (s *stubPurchaseIntentRepository) AcceptPurchase(_ context.Context, _ application.AcceptPurchaseRequest) (*application.PurchaseIntentResult, error) {
	s.called++
	return s.result, s.err
}

func acceptPurchaseCommand() application.AcceptPurchaseCommand {
	return application.AcceptPurchaseCommand{
		IntentKey: "intent-1",
		AccountID: "00000000-0000-4000-8000-000000000001",
		QuoteID:   "00000000-0000-4000-8000-000000000002",
		FiatMinor: 10000,
	}
}

func TestAcceptPurchaseUseCaseRefusesMalformedEnvelopes(t *testing.T) {
	t.Parallel()

	t.Run("blank key", func(t *testing.T) {
		t.Parallel()
		stub := &stubPurchaseIntentRepository{}
		cmd := acceptPurchaseCommand()
		cmd.IntentKey = "  "
		uc, err := application.NewAcceptPurchaseUseCase(stub)
		if err != nil {
			t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
		}
		if _, err := uc.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrEmptyIdempotencyKey) {
			t.Errorf("Execute = %v, want ErrEmptyIdempotencyKey", err)
		}
		if stub.called != 0 {
			t.Errorf("keyless acceptance reached the repository")
		}
	})
	t.Run("blank account", func(t *testing.T) {
		t.Parallel()
		stub := &stubPurchaseIntentRepository{}
		cmd := acceptPurchaseCommand()
		cmd.AccountID = "  "
		uc, err := application.NewAcceptPurchaseUseCase(stub)
		if err != nil {
			t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
		}
		if _, err := uc.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrEmptyAccountID) {
			t.Errorf("Execute = %v, want ErrEmptyAccountID", err)
		}
		if stub.called != 0 {
			t.Errorf("accountless acceptance reached the repository")
		}
	})
	t.Run("blank quote", func(t *testing.T) {
		t.Parallel()
		stub := &stubPurchaseIntentRepository{}
		cmd := acceptPurchaseCommand()
		cmd.QuoteID = "  "
		uc, err := application.NewAcceptPurchaseUseCase(stub)
		if err != nil {
			t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
		}
		if _, err := uc.Execute(context.Background(), cmd); !errors.Is(err, application.ErrPurchaseQuoteNotFound) {
			t.Errorf("Execute = %v, want ErrPurchaseQuoteNotFound", err)
		}
		if stub.called != 0 {
			t.Errorf("quoteless acceptance reached the repository")
		}
	})
	t.Run("zero ticket", func(t *testing.T) {
		t.Parallel()
		stub := &stubPurchaseIntentRepository{}
		cmd := acceptPurchaseCommand()
		cmd.FiatMinor = 0
		uc, err := application.NewAcceptPurchaseUseCase(stub)
		if err != nil {
			t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
		}
		if _, err := uc.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidMoney) {
			t.Errorf("Execute = %v, want ErrInvalidMoney", err)
		}
		if stub.called != 0 {
			t.Errorf("ticketless acceptance reached the repository")
		}
	})
}

func TestAcceptPurchaseUseCaseSealsEnvelope(t *testing.T) {
	t.Parallel()

	want := &application.PurchaseIntentResult{IntentID: "intent-id"}
	stub := &stubPurchaseIntentRepository{result: want}
	uc, err := application.NewAcceptPurchaseUseCase(stub)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}
	got, err := uc.Execute(context.Background(), acceptPurchaseCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Fatalf("Execute did not return the sealed acceptance")
	}
	if stub.called != 1 {
		t.Fatalf("calls = %d, want 1", stub.called)
	}
	if _, err := application.NewAcceptPurchaseUseCase(nil); err == nil {
		t.Fatalf("nil repository must refuse composition")
	}
}
