package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubConsentRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: command validation happens before it
// is ever called, and canned verdicts prove the gating.
type stubConsentRepository struct {
	called   int
	view     *application.ConsentView
	optin    *application.OptInView
	err      error
	accepted bool
}

func (s *stubConsentRepository) RecordConsent(_ context.Context, _ string, _ domain.CharterVersion, _ domain.ConsentDecision) (*application.ConsentView, error) {
	s.called++
	return s.view, s.err
}

func (s *stubConsentRepository) FindConsent(_ context.Context, _ string, _ domain.CharterVersion) (*application.ConsentView, error) {
	s.called++
	if s.accepted {
		return &application.ConsentView{Decision: domain.ConsentAccepted}, s.err
	}
	return nil, s.err
}

func (s *stubConsentRepository) RecordOptIn(_ context.Context, _ string, _ domain.CharterVersion, _ domain.MilliInk, _ domain.ConversionRate, _ time.Time) (*application.OptInView, error) {
	s.called++
	return s.optin, s.err
}

func TestConsentUseCaseRefusesInvalidVerdicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  application.ConsentCommand
		err  error
	}{
		{"blank account", application.ConsentCommand{AccountID: "", Charter: "v1", Decision: "accepted"}, domain.ErrInvalidCharter},
		{"bad version", application.ConsentCommand{AccountID: "a", Charter: "1.0", Decision: "accepted"}, domain.ErrInvalidCharter},
		{"zero version", application.ConsentCommand{AccountID: "a", Charter: "v0", Decision: "accepted"}, domain.ErrInvalidCharter},
		{"bad decision", application.ConsentCommand{AccountID: "a", Charter: "v1", Decision: "maybe"}, domain.ErrInvalidCharter},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubConsentRepository{}
			useCase := application.NewConsentUseCase(stub)
			if _, err := useCase.Execute(context.Background(), test.cmd); !errors.Is(err, test.err) {
				t.Errorf("Execute = %v, want %v", err, test.err)
			}
			if stub.called != 0 {
				t.Errorf("invalid verdict reached the repository: validation never touches storage")
			}
		})
	}
}

func TestOptInUseCaseRequiresAcceptance(t *testing.T) {
	t.Parallel()

	valid := application.OptInCommand{
		AccountID: "holder", Charter: "v1", Millis: 1000, RateNum: 10, RateDen: 1, ValidDays: 30,
	}
	invalid := []struct {
		name   string
		mutate func(*application.OptInCommand)
		err    error
	}{
		{"blank account", func(c *application.OptInCommand) { c.AccountID = "" }, domain.ErrInvalidCharter},
		{"bad version", func(c *application.OptInCommand) { c.Charter = "latest" }, domain.ErrInvalidCharter},
		{"zero quantity", func(c *application.OptInCommand) { c.Millis = 0 }, domain.ErrInvalidCharter},
		{"negative quantity", func(c *application.OptInCommand) { c.Millis = -5 }, domain.ErrNegativeMilliInk},
		{"zero rate", func(c *application.OptInCommand) { c.RateNum = 0 }, domain.ErrInvalidCharter},
		{"zero validity", func(c *application.OptInCommand) { c.ValidDays = 0 }, domain.ErrInvalidCharter},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubConsentRepository{accepted: true}
			useCase := application.NewOptInUseCase(stub, fixedConsentClock{})
			cmd := valid
			test.mutate(&cmd)
			if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, test.err) {
				t.Errorf("Execute = %v, want %v", err, test.err)
			}
			if stub.called != 0 {
				t.Errorf("invalid intent reached the repository: validation never touches storage")
			}
		})
	}

	t.Run("without acceptance", func(t *testing.T) {
		t.Parallel()

		stub := &stubConsentRepository{}
		useCase := application.NewOptInUseCase(stub, fixedConsentClock{})
		if _, err := useCase.Execute(context.Background(), valid); !errors.Is(err, domain.ErrConsentRequired) {
			t.Errorf("Execute without acceptance = %v, want ErrConsentRequired", err)
		}
	})
}

// fixedConsentClock freezes validity decisions at one instant.
type fixedConsentClock struct{}

func (fixedConsentClock) Now() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }
