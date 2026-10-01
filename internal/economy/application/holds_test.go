package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// fixedClock freezes expiry decisions at one instant.
type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

// stubHoldsRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: command validation happens before it
// is ever called, and canned views prove the mappings.
type stubHoldsRepository struct {
	called int
	view   *application.HoldView
	err    error
}

func (s *stubHoldsRepository) Reserve(_ context.Context, _ application.HoldReservation) (*application.HoldView, error) {
	s.called++
	return s.view, s.err
}

func (s *stubHoldsRepository) Release(_ context.Context, _ string) (*application.HoldView, error) {
	s.called++
	return s.view, s.err
}

func (s *stubHoldsRepository) Capture(_ context.Context, _, _, _ string) (*application.HoldView, error) {
	s.called++
	return s.view, s.err
}

func (s *stubHoldsRepository) Expire(_ context.Context, _ string) (*application.HoldView, error) {
	s.called++
	return s.view, s.err
}

var holdNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func reserveCommand() application.ReserveCommand {
	return application.ReserveCommand{
		Season:    domain.CompatSeasonKey,
		OwnerKind: "user", OwnerLabel: "ana", Purpose: "escrow for deal",
		Millis: 1000, ExpiresAt: holdNow.Add(time.Hour),
	}
}

func TestReserveUseCaseRefusesInvalidReservations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*application.ReserveCommand)
		err    error
	}{
		{"unknown kind", func(c *application.ReserveCommand) { c.OwnerKind = "vault" }, domain.ErrUnknownCustody},
		{"blank label", func(c *application.ReserveCommand) { c.OwnerLabel = "" }, domain.ErrUnknownCustody},
		{"locked kind", func(c *application.ReserveCommand) { c.OwnerKind = "escrow" }, domain.ErrUnauthorizedCustody},
		{"blank purpose", func(c *application.ReserveCommand) { c.Purpose = "  " }, domain.ErrInvalidHold},
		{"long purpose", func(c *application.ReserveCommand) { c.Purpose = strings.Repeat("p", 281) }, domain.ErrInvalidHold},
		{"negative amount", func(c *application.ReserveCommand) { c.Millis = -5 }, domain.ErrNegativeMilliInk},
		{"zero amount", func(c *application.ReserveCommand) { c.Millis = 0 }, domain.ErrInvalidHold},
		{"past expiry", func(c *application.ReserveCommand) { c.ExpiresAt = holdNow.Add(-time.Hour) }, domain.ErrInvalidHold},
		{"zero expiry", func(c *application.ReserveCommand) { c.ExpiresAt = time.Time{} }, domain.ErrInvalidHold},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubHoldsRepository{}
			useCase := application.NewReserveUseCase(stub, fixedClock{now: holdNow}, stubSeasonBooks{})
			cmd := reserveCommand()
			test.mutate(&cmd)
			if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, test.err) {
				t.Errorf("Execute = %v, want %v", err, test.err)
			}
			if stub.called != 0 {
				t.Errorf("invalid reservation reached the repository: validation never touches storage")
			}
		})
	}
}

func TestSettleUseCasesValidateHoldIdentity(t *testing.T) {
	t.Parallel()

	view := &application.HoldView{Status: "released"}
	stub := &stubHoldsRepository{view: view}

	if _, err := application.NewReleaseUseCase(stub).Execute(context.Background(), application.SettleCommand{}); !errors.Is(err, domain.ErrHoldNotFound) {
		t.Errorf("blank release = %v, want ErrHoldNotFound", err)
	}
	if _, err := application.NewExpireUseCase(stub).Execute(context.Background(), application.SettleCommand{}); !errors.Is(err, domain.ErrHoldNotFound) {
		t.Errorf("blank expire = %v, want ErrHoldNotFound", err)
	}
	if _, err := application.NewCaptureUseCase(stub).Execute(context.Background(),
		application.CaptureCommand{HoldID: "", ToKind: "user", ToLabel: "bia"}); !errors.Is(err, domain.ErrHoldNotFound) {
		t.Errorf("blank capture = %v, want ErrHoldNotFound", err)
	}
	if _, err := application.NewCaptureUseCase(stub).Execute(context.Background(),
		application.CaptureCommand{HoldID: "hold-id", ToKind: "vault", ToLabel: "bia"}); !errors.Is(err, domain.ErrUnknownCustody) {
		t.Errorf("unknown capture kind = %v, want ErrUnknownCustody", err)
	}
	got, err := application.NewReleaseUseCase(stub).Execute(context.Background(), application.SettleCommand{HoldID: "hold-id"})
	if err != nil || got != view {
		t.Fatalf("release did not return the settled view: %+v, %v", got, err)
	}
}
