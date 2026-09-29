package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubReserveRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: act and amount validation happens
// before it is ever called, and canned outcomes prove the mapping.
type stubReserveRepository struct {
	called int
	result *application.AllocateReserveResult
	err    error
}

func (s *stubReserveRepository) AllocateReserve(_ context.Context, _ application.AllocateReserveRequest) (*application.AllocateReserveResult, error) {
	s.called++
	return s.result, s.err
}

func allocateReserveCommand() application.AllocateReserveCommand {
	return application.AllocateReserveCommand{ActID: "act-2026-09", Millis: 1000}
}

func TestAllocateReserveUseCaseRefusesInexplicitActs(t *testing.T) {
	t.Parallel()

	t.Run("blank act", func(t *testing.T) {
		t.Parallel()

		stub := &stubReserveRepository{}
		useCase := application.NewAllocateReserveUseCase(stub)
		cmd := allocateReserveCommand()
		cmd.ActID = ""
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidIntention) {
			t.Errorf("Execute = %v, want ErrInvalidIntention", err)
		}
		if stub.called != 0 {
			t.Errorf("blank act reached the repository")
		}
	})
	t.Run("negative amount", func(t *testing.T) {
		t.Parallel()

		stub := &stubReserveRepository{}
		useCase := application.NewAllocateReserveUseCase(stub)
		cmd := allocateReserveCommand()
		cmd.Millis = -5
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrNegativeMilliInk) {
			t.Errorf("Execute = %v, want ErrNegativeMilliInk", err)
		}
		if stub.called != 0 {
			t.Errorf("negative amount reached the repository")
		}
	})
	t.Run("zero amount", func(t *testing.T) {
		t.Parallel()

		stub := &stubReserveRepository{}
		useCase := application.NewAllocateReserveUseCase(stub)
		cmd := allocateReserveCommand()
		cmd.Millis = 0
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidGrant) {
			t.Errorf("Execute = %v, want ErrInvalidGrant", err)
		}
		if stub.called != 0 {
			t.Errorf("zero amount reached the repository: allocations are always explicit")
		}
	})
}

func TestAllocateReserveUseCaseSettlesExplicitAmount(t *testing.T) {
	t.Parallel()

	want := &application.AllocateReserveResult{TransferID: "transfer-id"}
	stub := &stubReserveRepository{result: want}
	useCase := application.NewAllocateReserveUseCase(stub)
	got, err := useCase.Execute(context.Background(), allocateReserveCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Fatalf("Execute did not return the settled allocation")
	}
	if stub.called != 1 {
		t.Fatalf("calls = %d, want 1", stub.called)
	}
}
