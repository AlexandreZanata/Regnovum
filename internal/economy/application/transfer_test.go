package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubTransferRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: command validation happens before it
// is ever called, and one canned receipt proves the success mapping.
type stubTransferRepository struct {
	called int
	result *application.TransferResult
	err    error
}

func (s *stubTransferRepository) Transfer(_ context.Context, _ application.TransferRequest) (*application.TransferResult, error) {
	s.called++
	return s.result, s.err
}

func transferCommand(fromKind, fromLabel, toKind, toLabel string, millis int64) application.TransferCommand {
	return application.TransferCommand{
		FromSeason: domain.CompatSeasonKey,
		FromKind:   fromKind,
		FromLabel:  fromLabel,
		ToSeason:   domain.CompatSeasonKey,
		ToKind:     toKind,
		ToLabel:    toLabel,
		Millis:     millis,
	}
}

func TestTransferUseCaseRefusesSeasonViolations(t *testing.T) {
	t.Parallel()

	stub := &stubTransferRepository{}
	useCase := application.NewTransferUseCase(stub, stubSeasonBooks{})
	sound := transferCommand("user", "a", "user", "b", 100)

	seasonless := sound
	seasonless.FromSeason = ""
	if _, err := useCase.Execute(context.Background(), seasonless); !errors.Is(err, domain.ErrMissingSeason) {
		t.Errorf("seasonless Execute = %v, want ErrMissingSeason", err)
	}
	crossed := sound
	crossed.ToSeason = "temporada-2"
	if _, err := useCase.Execute(context.Background(), crossed); !errors.Is(err, domain.ErrCrossSeason) {
		t.Errorf("cross-season Execute = %v, want ErrCrossSeason", err)
	}
	if stub.called != 0 {
		t.Errorf("bookless commands reached the repository: missing books never touch storage")
	}
}

func TestTransferUseCaseRefusesInvalidCommands(t *testing.T) {
	t.Parallel()

	amount, err := domain.NewMilliInk(100)
	if err != nil {
		t.Fatalf("NewMilliInk(100): %v", err)
	}
	tests := []struct {
		name string
		cmd  application.TransferCommand
		err  error
	}{
		{"unknown source kind", transferCommand("vault", "a", "user", "b", 100), domain.ErrUnknownCustody},
		{"unknown destination kind", transferCommand("user", "a", "vault", "b", 100), domain.ErrUnknownCustody},
		{"blank source label", transferCommand("user", "", "user", "b", 100), domain.ErrUnknownCustody},
		{"blank destination label", transferCommand("user", "a", "user", "", 100), domain.ErrUnknownCustody},
		{"same custody", transferCommand("user", "a", "user", "a", 100), domain.ErrSameCustody},
		{"locked escrow source", transferCommand("escrow", "deal", "user", "b", 100), domain.ErrUnauthorizedCustody},
		{"locked title source", transferCommand("title", "bond", "user", "b", 100), domain.ErrUnauthorizedCustody},
		{"negative amount", transferCommand("user", "a", "user", "b", -100), domain.ErrNegativeMilliInk},
		{"zero amount", transferCommand("user", "a", "user", "b", 0), domain.ErrInvalidMilliInk},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubTransferRepository{result: &application.TransferResult{Debited: amount, Credited: amount}}
			useCase := application.NewTransferUseCase(stub, stubSeasonBooks{})
			if _, err := useCase.Execute(context.Background(), test.cmd); !errors.Is(err, test.err) {
				t.Errorf("Execute = %v, want %v", err, test.err)
			}
			if stub.called != 0 {
				t.Errorf("invalid command reached the repository: validation never touches storage")
			}
		})
	}
}

func TestTransferUseCaseMapsRepositoryOutcome(t *testing.T) {
	t.Parallel()

	debited, err := domain.NewMilliInk(250)
	if err != nil {
		t.Fatalf("NewMilliInk(250): %v", err)
	}
	want := &application.TransferResult{TransferID: "transfer-id", Debited: debited, Credited: debited}
	stub := &stubTransferRepository{result: want}
	useCase := application.NewTransferUseCase(stub, stubSeasonBooks{})
	got, err := useCase.Execute(context.Background(), transferCommand("treasury", "main", "user", "ana", 250))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Fatalf("Execute did not return the recorded receipt")
	}

	stub = &stubTransferRepository{err: domain.ErrInsufficientMilliInk}
	useCase = application.NewTransferUseCase(stub, stubSeasonBooks{})
	if _, err := useCase.Execute(context.Background(), transferCommand("user", "poor", "user", "rich", 250)); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("Execute without balance = %v, want ErrInsufficientMilliInk", err)
	}
}
