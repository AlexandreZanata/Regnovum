package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubDisbursementRepository stands in for the PostgreSQL adapter where
// no database behavior is under test: governor validation happens
// before it is ever called, and canned outcomes prove the mapping.
type stubDisbursementRepository struct {
	called int
	result *application.DisburseResult
	err    error
}

func (s *stubDisbursementRepository) Disburse(_ context.Context, _ application.DisburseRequest) (*application.DisburseResult, error) {
	s.called++
	return s.result, s.err
}

func disburseCommand() application.DisburseCommand {
	return application.DisburseCommand{
		Key: "disburse-1", Vault: "operating_cash", Beneficiary: "bea-uuid",
		Purpose: "compensation", Millis: 1000,
		ApproverOne: "ana-uuid", ApproverTwo: "bob-uuid",
	}
}

func TestDisburseUseCaseRefusesUngovernedActs(t *testing.T) {
	t.Parallel()

	t.Run("blank key", func(t *testing.T) {
		t.Parallel()

		stub := &stubDisbursementRepository{}
		cmd := disburseCommand()
		cmd.Key = ""
		if _, err := application.NewDisburseUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidIntention) {
			t.Errorf("Execute = %v, want ErrInvalidIntention", err)
		}
		if stub.called != 0 {
			t.Errorf("keyless act reached the repository")
		}
	})
	t.Run("non-vault origin", func(t *testing.T) {
		t.Parallel()

		stub := &stubDisbursementRepository{}
		cmd := disburseCommand()
		cmd.Vault = "slush"
		if _, err := application.NewDisburseUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrUnknownCustody) {
			t.Errorf("Execute = %v, want ErrUnknownCustody", err)
		}
		if stub.called != 0 {
			t.Errorf("non-vault origin reached the repository")
		}
	})
	t.Run("invalid purpose", func(t *testing.T) {
		t.Parallel()

		stub := &stubDisbursementRepository{}
		cmd := disburseCommand()
		cmd.Purpose = "decree"
		if _, err := application.NewDisburseUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidDisbursement) {
			t.Errorf("Execute = %v, want ErrInvalidDisbursement", err)
		}
		if stub.called != 0 {
			t.Errorf("unallowlisted purpose reached the repository")
		}
	})
	t.Run("zero amount", func(t *testing.T) {
		t.Parallel()

		stub := &stubDisbursementRepository{}
		cmd := disburseCommand()
		cmd.Millis = 0
		if _, err := application.NewDisburseUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidGrant) {
			t.Errorf("Execute = %v, want ErrInvalidGrant", err)
		}
	})
	t.Run("partial credential", func(t *testing.T) {
		t.Parallel()

		stub := &stubDisbursementRepository{}
		cmd := disburseCommand()
		cmd.ApproverTwo = ""
		if _, err := application.NewDisburseUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidIntention) {
			t.Errorf("Execute = %v, want ErrInvalidIntention", err)
		}
		if stub.called != 0 {
			t.Errorf("partially credentialed act reached the repository")
		}
	})
	t.Run("duplicated approval", func(t *testing.T) {
		t.Parallel()

		stub := &stubDisbursementRepository{}
		cmd := disburseCommand()
		cmd.ApproverTwo = cmd.ApproverOne
		if _, err := application.NewDisburseUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidDisbursement) {
			t.Errorf("Execute = %v, want ErrInvalidDisbursement", err)
		}
		if stub.called != 0 {
			t.Errorf("twice-governed act reached the repository")
		}
	})
	t.Run("self approval", func(t *testing.T) {
		t.Parallel()

		stub := &stubDisbursementRepository{}
		cmd := disburseCommand()
		cmd.Beneficiary = cmd.ApproverOne
		if _, err := application.NewDisburseUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidDisbursement) {
			t.Errorf("Execute = %v, want ErrInvalidDisbursement", err)
		}
		if stub.called != 0 {
			t.Errorf("self-governed act reached the repository")
		}
	})
}

func TestDisburseUseCaseSettlesGovernedAct(t *testing.T) {
	t.Parallel()

	want := &application.DisburseResult{TransferID: "transfer-id"}
	stub := &stubDisbursementRepository{result: want}
	useCase := application.NewDisburseUseCase(stub)
	got, err := useCase.Execute(context.Background(), disburseCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Fatalf("Execute did not return the settled disbursement")
	}
	if stub.called != 1 {
		t.Fatalf("calls = %d, want 1", stub.called)
	}
}
