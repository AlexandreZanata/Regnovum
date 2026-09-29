package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubRefusalRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: validation happens before it is ever
// called, and canned verdicts prove the refusal gate.
type stubRefusalRepository struct {
	called  int
	refused bool
	result  *application.RefusalRefundResult
	err     error
}

func (s *stubRefusalRepository) FindConsent(_ context.Context, _ string, _ domain.CharterVersion) (*application.ConsentView, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.refused {
		return &application.ConsentView{
			Decision:  domain.ConsentRefused,
			DecidedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
		}, nil
	}
	return nil, nil
}

func (s *stubRefusalRepository) SettleRefusalRefund(_ context.Context, _ application.RefusalRefundRequest) (*application.RefusalRefundResult, error) {
	s.called++
	return s.result, s.err
}

func refusalCommand() application.RefusalRefundCommand {
	return application.RefusalRefundCommand{
		AccountID: "holder", Charter: "v1",
		PaidMinor: 990, RefundedMinor: 990,
		Source: "refund", ProviderRefund: "re_abc123",
	}
}

func TestRefusalRefundUseCaseRefusesWithoutRefusal(t *testing.T) {
	t.Parallel()

	t.Run("blank account", func(t *testing.T) {
		t.Parallel()

		stub := &stubRefusalRepository{refused: true}
		useCase := application.NewRefusalRefundUseCase(stub)
		cmd := refusalCommand()
		cmd.AccountID = ""
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidRefund) {
			t.Errorf("Execute = %v, want ErrInvalidRefund", err)
		}
		if stub.called != 0 {
			t.Errorf("invalid exit reached the repository")
		}
	})
	t.Run("bad charter", func(t *testing.T) {
		t.Parallel()

		stub := &stubRefusalRepository{refused: true}
		useCase := application.NewRefusalRefundUseCase(stub)
		cmd := refusalCommand()
		cmd.Charter = "latest"
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidCharter) {
			t.Errorf("Execute = %v, want ErrInvalidCharter", err)
		}
	})
	t.Run("bad source", func(t *testing.T) {
		t.Parallel()

		stub := &stubRefusalRepository{refused: true}
		useCase := application.NewRefusalRefundUseCase(stub)
		cmd := refusalCommand()
		cmd.Source = "chargeback"
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidRefund) {
			t.Errorf("Execute = %v, want ErrInvalidRefund", err)
		}
	})
	t.Run("bad provider", func(t *testing.T) {
		t.Parallel()

		stub := &stubRefusalRepository{refused: true}
		useCase := application.NewRefusalRefundUseCase(stub)
		cmd := refusalCommand()
		cmd.ProviderRefund = "ch_abc"
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidRefund) {
			t.Errorf("Execute = %v, want ErrInvalidRefund", err)
		}
	})
	t.Run("refunded above paid", func(t *testing.T) {
		t.Parallel()

		stub := &stubRefusalRepository{refused: true}
		useCase := application.NewRefusalRefundUseCase(stub)
		cmd := refusalCommand()
		cmd.RefundedMinor = cmd.PaidMinor + 1
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidRefund) {
			t.Errorf("Execute = %v, want ErrInvalidRefund", err)
		}
	})
	t.Run("without refusal", func(t *testing.T) {
		t.Parallel()

		stub := &stubRefusalRepository{}
		useCase := application.NewRefusalRefundUseCase(stub)
		if _, err := useCase.Execute(context.Background(), refusalCommand()); !errors.Is(err, domain.ErrConsentRequired) {
			t.Errorf("Execute = %v, want ErrConsentRequired", err)
		}
		if stub.called != 0 {
			t.Errorf("unrefused exit reached settlement")
		}
	})
}

func TestRefusalRefundUseCaseSettlesRefusedExit(t *testing.T) {
	t.Parallel()

	want := &application.RefusalRefundResult{RevokeUnits: 10}
	stub := &stubRefusalRepository{refused: true, result: want}
	useCase := application.NewRefusalRefundUseCase(stub)
	got, err := useCase.Execute(context.Background(), refusalCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Fatalf("Execute did not return the settled refund")
	}
	if stub.called != 1 {
		t.Fatalf("calls = %d, want 1", stub.called)
	}
}
