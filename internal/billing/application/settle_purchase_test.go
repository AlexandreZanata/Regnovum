package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
)

func settlePurchaseCommand() application.SettlePurchaseCommand {
	return application.SettlePurchaseCommand{
		Payload:         []byte(`{"event_id":"evt-1"}`),
		SignatureHeader: "t=1,v1=deadbeef",
		TimestampHeader: "1",
	}
}

func TestSettlePurchaseUseCaseRefusesEmptyDeliveries(t *testing.T) {
	t.Parallel()

	newUseCase := func(calls *int) *application.SettlePurchaseUseCase {
		uc, err := application.NewSettlePurchaseUseCase(&stubSettlementRepository{calls: calls})
		if err != nil {
			t.Fatalf("NewSettlePurchaseUseCase: %v", err)
		}
		return uc
	}
	t.Run("empty payload", func(t *testing.T) {
		t.Parallel()
		var calls int
		cmd := settlePurchaseCommand()
		cmd.Payload = nil
		if _, err := newUseCase(&calls).Execute(context.Background(), cmd); !errors.Is(err, application.ErrWebhookPayloadMalformed) {
			t.Errorf("Execute = %v, want ErrWebhookPayloadMalformed", err)
		}
		if calls != 0 {
			t.Errorf("empty delivery reached the repository")
		}
	})
	t.Run("missing signature", func(t *testing.T) {
		t.Parallel()
		var calls int
		cmd := settlePurchaseCommand()
		cmd.SignatureHeader = ""
		if _, err := newUseCase(&calls).Execute(context.Background(), cmd); !errors.Is(err, application.ErrWebhookSignatureInvalid) {
			t.Errorf("Execute = %v, want ErrWebhookSignatureInvalid", err)
		}
		if calls != 0 {
			t.Errorf("unsigned delivery reached the repository")
		}
	})
	t.Run("missing timestamp", func(t *testing.T) {
		t.Parallel()
		var calls int
		cmd := settlePurchaseCommand()
		cmd.TimestampHeader = ""
		if _, err := newUseCase(&calls).Execute(context.Background(), cmd); !errors.Is(err, application.ErrWebhookSignatureInvalid) {
			t.Errorf("Execute = %v, want ErrWebhookSignatureInvalid", err)
		}
		if calls != 0 {
			t.Errorf("undated delivery reached the repository")
		}
	})
}

func TestSettlePurchaseUseCaseSettlesDelivery(t *testing.T) {
	t.Parallel()

	want := &application.SettlePurchaseResult{SettlementID: "settlement-id"}
	var calls int
	uc, err := application.NewSettlePurchaseUseCase(&stubSettlementRepository{calls: &calls, result: want})
	if err != nil {
		t.Fatalf("NewSettlePurchaseUseCase: %v", err)
	}
	got, err := uc.Execute(context.Background(), settlePurchaseCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Fatalf("Execute did not return the settled delivery")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if _, err := application.NewSettlePurchaseUseCase(nil); err == nil {
		t.Fatalf("nil repository must refuse composition")
	}
}

// stubSettlementRepository stands in for the PostgreSQL adapter where
// no database behavior is under test.
type stubSettlementRepository struct {
	calls  *int
	result *application.SettlePurchaseResult
	err    error
}

func (s *stubSettlementRepository) SettlePurchase(_ context.Context, _ application.SettlePurchaseRequest) (*application.SettlePurchaseResult, error) {
	*s.calls++
	return s.result, s.err
}
