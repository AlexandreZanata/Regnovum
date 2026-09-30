package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubRestitutionPort stands in for the PostgreSQL adapter where no
// database behavior is under test: cause and governor validation
// happen before it is ever called, and the canned settlement proves
// the single-port mapping.
type stubRestitutionPort struct {
	called  int
	request application.DisburseRequest
	result  *application.DisburseResult
	err     error
}

func (s *stubRestitutionPort) Disburse(_ context.Context, request application.DisburseRequest) (*application.DisburseResult, error) {
	s.called++
	s.request = request
	return s.result, s.err
}

func restituteCommand() application.RestituteCommand {
	return application.RestituteCommand{
		Key: "restitution-1", Vault: "operating_cash", Beneficiary: "bea-uuid",
		Cause: "broken_contract", Millis: 1000,
		ApproverOne: "ana-uuid", ApproverTwo: "bob-uuid",
	}
}

func TestRestituteUseCaseRefusesInvalidReparations(t *testing.T) {
	t.Parallel()

	t.Run("unknown cause", func(t *testing.T) {
		t.Parallel()

		stub := &stubRestitutionPort{}
		cmd := restituteCommand()
		cmd.Cause = "decree"
		if _, err := application.NewRestituteUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidDisbursement) {
			t.Errorf("Execute = %v, want ErrInvalidDisbursement", err)
		}
		if stub.called != 0 {
			t.Errorf("causeless reparation reached the repository")
		}
	})
	t.Run("blank key", func(t *testing.T) {
		t.Parallel()

		stub := &stubRestitutionPort{}
		cmd := restituteCommand()
		cmd.Key = ""
		if _, err := application.NewRestituteUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidIntention) {
			t.Errorf("Execute = %v, want ErrInvalidIntention", err)
		}
		if stub.called != 0 {
			t.Errorf("keyless reparation reached the repository")
		}
	})
	t.Run("non-vault origin", func(t *testing.T) {
		t.Parallel()

		stub := &stubRestitutionPort{}
		cmd := restituteCommand()
		cmd.Vault = "escrow"
		if _, err := application.NewRestituteUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrUnknownCustody) {
			t.Errorf("Execute = %v, want ErrUnknownCustody", err)
		}
		if stub.called != 0 {
			t.Errorf("escrow origin reached the repository")
		}
	})
	t.Run("zero amount", func(t *testing.T) {
		t.Parallel()

		stub := &stubRestitutionPort{}
		cmd := restituteCommand()
		cmd.Millis = 0
		if _, err := application.NewRestituteUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidGrant) {
			t.Errorf("Execute = %v, want ErrInvalidGrant", err)
		}
		if stub.called != 0 {
			t.Errorf("zero reparation reached the repository")
		}
	})
	t.Run("duplicated approval", func(t *testing.T) {
		t.Parallel()

		stub := &stubRestitutionPort{}
		cmd := restituteCommand()
		cmd.ApproverTwo = cmd.ApproverOne
		if _, err := application.NewRestituteUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidDisbursement) {
			t.Errorf("Execute = %v, want ErrInvalidDisbursement", err)
		}
		if stub.called != 0 {
			t.Errorf("twice-governed reparation reached the repository")
		}
	})
	t.Run("self approval", func(t *testing.T) {
		t.Parallel()

		stub := &stubRestitutionPort{}
		cmd := restituteCommand()
		cmd.Beneficiary = cmd.ApproverOne
		if _, err := application.NewRestituteUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidDisbursement) {
			t.Errorf("Execute = %v, want ErrInvalidDisbursement", err)
		}
		if stub.called != 0 {
			t.Errorf("self-governed reparation reached the repository")
		}
	})
}

func TestRestituteUseCaseSettlesThroughSinglePort(t *testing.T) {
	t.Parallel()

	for _, cause := range []string{"broken_contract", "paying_suspension", "chargeback"} {
		stub := &stubRestitutionPort{result: &application.DisburseResult{TransferID: "transfer-id"}}
		cmd := restituteCommand()
		cmd.Cause = cause
		got, err := application.NewRestituteUseCase(stub).Execute(context.Background(), cmd)
		if err != nil {
			t.Fatalf("Execute(%s): %v", cause, err)
		}
		if got.Cause.String() != cause {
			t.Fatalf("Cause = %q, want %q", got.Cause.String(), cause)
		}
		if stub.called != 1 {
			t.Fatalf("calls = %d, want 1", stub.called)
		}
		if stub.request.Purpose != domain.DisbursementCompensation {
			t.Fatalf("purpose = %q, want compensation through the single port", stub.request.Purpose)
		}
		if string(stub.request.Key) != cmd.Key || stub.request.Vault.String() != cmd.Vault || string(stub.request.Beneficiary) != cmd.Beneficiary {
			t.Fatalf("request lost origin/beneficiary/reference: %+v", stub.request)
		}
	}
}
