package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// commitCaptureStub records the owner the commitment delegated to:
// the Treasury scoping under test never reaches a database through it.
type commitCaptureStub struct {
	stubHoldsRepository
	kind  domain.CustodyKind
	label string
}

func (s *commitCaptureStub) Reserve(_ context.Context, reservation application.HoldReservation) (*application.HoldView, error) {
	s.kind, s.label = reservation.OwnerKind, reservation.OwnerLabel
	return &application.HoldView{}, nil
}

func commitFundsCommand() application.CommitFundsCommand {
	return application.CommitFundsCommand{
		Season: domain.CompatSeasonKey,
		Vault:  "commercial_stock", Purpose: "accepted sale #1",
		Millis: 1000, ExpiresAt: holdNow.Add(time.Hour),
	}
}

func commitUseCase(holds application.HoldsRepository) *application.CommitFundsUseCase {
	return application.NewCommitFundsUseCase(holds, fixedClock{now: holdNow})
}

func TestCommitFundsUseCaseRefusesNonTreasuryOwners(t *testing.T) {
	t.Parallel()

	for _, vault := range []string{"", "slush", "MAIN", "available", "7f3a1c2e-9b4d-4e8f-a5c1-2d3e4f5a6b7c"} {
		t.Run("vault "+vault, func(t *testing.T) {
			t.Parallel()

			stub := &stubHoldsRepository{}
			if _, err := commitUseCase(stub).Execute(context.Background(), application.CommitFundsCommand{
				Season: domain.CompatSeasonKey,
				Vault:  vault, Purpose: "due payment",
				Millis: 100, ExpiresAt: holdNow.Add(time.Hour),
			}); !errors.Is(err, domain.ErrUnknownCustody) {
				t.Errorf("Execute(%q) = %v, want ErrUnknownCustody", vault, err)
			}
			if stub.called != 0 {
				t.Errorf("non-vault owner reached the repository")
			}
		})
	}
}

func TestCommitFundsUseCaseRefusesInexplicitCommitments(t *testing.T) {
	t.Parallel()

	t.Run("blank purpose", func(t *testing.T) {
		t.Parallel()

		stub := &stubHoldsRepository{}
		cmd := commitFundsCommand()
		cmd.Purpose = "  "
		if _, err := commitUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidHold) {
			t.Errorf("Execute = %v, want ErrInvalidHold", err)
		}
	})
	t.Run("zero amount", func(t *testing.T) {
		t.Parallel()

		stub := &stubHoldsRepository{}
		cmd := commitFundsCommand()
		cmd.Millis = 0
		if _, err := commitUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidHold) {
			t.Errorf("Execute = %v, want ErrInvalidHold", err)
		}
	})
	t.Run("past expiry", func(t *testing.T) {
		t.Parallel()

		stub := &stubHoldsRepository{}
		cmd := commitFundsCommand()
		cmd.ExpiresAt = holdNow.Add(-time.Hour)
		if _, err := commitUseCase(stub).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidHold) {
			t.Errorf("Execute = %v, want ErrInvalidHold", err)
		}
	})
}

func TestCommitFundsUseCaseLocksTreasuryVault(t *testing.T) {
	t.Parallel()

	stub := &commitCaptureStub{}
	if _, err := commitUseCase(stub).Execute(context.Background(), commitFundsCommand()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if stub.kind != domain.CustodyTreasury || stub.label != "commercial_stock" {
		t.Fatalf("owner = %q/%q, want treasury/commercial_stock", stub.kind, stub.label)
	}
}
