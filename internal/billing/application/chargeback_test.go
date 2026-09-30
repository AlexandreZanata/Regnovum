package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
)

func settleChargebackCommand() application.SettleChargebackCommand {
	return application.SettleChargebackCommand{
		Payload:         []byte(`{"event_id":"dp-1"}`),
		SignatureHeader: "t=1,v1=deadbeef",
		TimestampHeader: "1",
	}
}

func TestSettleChargebackUseCaseRefusesEmptyDeliveries(t *testing.T) {
	t.Parallel()

	newUseCase := func(calls *int) *application.SettleChargebackUseCase {
		uc, err := application.NewSettleChargebackUseCase(&stubChargebackRepository{calls: calls})
		if err != nil {
			t.Fatalf("NewSettleChargebackUseCase: %v", err)
		}
		return uc
	}
	t.Run("empty payload", func(t *testing.T) {
		t.Parallel()
		var calls int
		cmd := settleChargebackCommand()
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
		cmd := settleChargebackCommand()
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
		cmd := settleChargebackCommand()
		cmd.TimestampHeader = ""
		if _, err := newUseCase(&calls).Execute(context.Background(), cmd); !errors.Is(err, application.ErrWebhookSignatureInvalid) {
			t.Errorf("Execute = %v, want ErrWebhookSignatureInvalid", err)
		}
		if calls != 0 {
			t.Errorf("undated delivery reached the repository")
		}
	})
}

func TestSettleChargebackUseCaseSettlesDelivery(t *testing.T) {
	t.Parallel()

	want := &application.SettleChargebackResult{ChargebackID: "chargeback-id"}
	var calls int
	uc, err := application.NewSettleChargebackUseCase(&stubChargebackRepository{calls: &calls, result: want})
	if err != nil {
		t.Fatalf("NewSettleChargebackUseCase: %v", err)
	}
	got, err := uc.Execute(context.Background(), settleChargebackCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Fatalf("Execute did not return the settled dispute")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if _, err := application.NewSettleChargebackUseCase(nil); err == nil {
		t.Fatalf("nil repository must refuse composition")
	}
}

// stubChargebackRepository stands in for the PostgreSQL adapter where
// no database behavior is under test.
type stubChargebackRepository struct {
	calls  *int
	result *application.SettleChargebackResult
	err    error
}

func (s *stubChargebackRepository) SettleChargeback(_ context.Context, _ application.SettleChargebackRequest) (*application.SettleChargebackResult, error) {
	*s.calls++
	return s.result, s.err
}
