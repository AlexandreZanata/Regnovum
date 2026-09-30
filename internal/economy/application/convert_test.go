package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubConversionRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: terms validation happens before it is
// ever called, and canned outcomes prove the mappings.
type stubConversionRepository struct {
	called int
	terms  *application.OptInTermsView
	result *application.ConversionResult
	err    error
}

func (s *stubConversionRepository) FindTerms(_ context.Context, _ string, _ domain.CharterVersion) (*application.OptInTermsView, error) {
	return s.terms, s.err
}

func (s *stubConversionRepository) Convert(_ context.Context, _ application.ConversionRequest) (*application.ConversionResult, error) {
	s.called++
	return s.result, s.err
}

// stubConsentReader answers standing acceptances for the conversion
// validation paths.
type stubConsentReader struct {
	accepted bool
}

func (s *stubConsentReader) RecordConsent(_ context.Context, _ string, _ domain.CharterVersion, _ domain.ConsentDecision) (*application.ConsentView, error) {
	return nil, errors.New("unused")
}

func (s *stubConsentReader) FindConsent(_ context.Context, _ string, _ domain.CharterVersion) (*application.ConsentView, error) {
	if s.accepted {
		return &application.ConsentView{Decision: domain.ConsentAccepted}, nil
	}
	return nil, nil
}

func (s *stubConsentReader) RecordOptIn(_ context.Context, _ string, _ domain.CharterVersion, _ domain.MilliInk, _ domain.ConversionRate, _ time.Time) (*application.OptInView, error) {
	return nil, errors.New("unused")
}

func convertTerms() *application.OptInTermsView {
	quantity, _ := domain.NewMilliInk(5000)
	rate, _ := domain.ParseConversionRate(1000, 1)
	return &application.OptInTermsView{
		OptInID: "optin-id", Quantity: quantity, Rate: rate,
		ValidTill: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func convertCommand() application.ConvertCommand {
	return application.ConvertCommand{AccountID: "holder", Charter: "v1", RateNum: 1000, RateDen: 1}
}

func newConvertUseCase(conversions *stubConversionRepository, accepted bool) *application.ConvertUseCase {
	return application.NewConvertUseCase(&stubConsentReader{accepted: accepted}, conversions, fixedConvertClock{})
}

// fixedConvertClock freezes conversion decisions at one instant.
type fixedConvertClock struct{}

func (fixedConvertClock) Now() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }

func TestConvertUseCaseRefusesWithoutStandingTerms(t *testing.T) {
	t.Parallel()

	t.Run("blank account", func(t *testing.T) {
		t.Parallel()

		stub := &stubConversionRepository{}
		useCase := newConvertUseCase(stub, true)
		cmd := convertCommand()
		cmd.AccountID = ""
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrOptInMissing) {
			t.Errorf("Execute = %v, want ErrOptInMissing", err)
		}
	})
	t.Run("bad charter", func(t *testing.T) {
		t.Parallel()

		stub := &stubConversionRepository{}
		useCase := newConvertUseCase(stub, true)
		cmd := convertCommand()
		cmd.Charter = "latest"
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidCharter) {
			t.Errorf("Execute = %v, want ErrInvalidCharter", err)
		}
	})
	t.Run("bad rate", func(t *testing.T) {
		t.Parallel()

		stub := &stubConversionRepository{}
		useCase := newConvertUseCase(stub, true)
		cmd := convertCommand()
		cmd.RateDen = 0
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidCharter) {
			t.Errorf("Execute = %v, want ErrInvalidCharter", err)
		}
	})
	t.Run("without acceptance", func(t *testing.T) {
		t.Parallel()

		stub := &stubConversionRepository{}
		useCase := newConvertUseCase(stub, false)
		if _, err := useCase.Execute(context.Background(), convertCommand()); !errors.Is(err, domain.ErrConsentRequired) {
			t.Errorf("Execute = %v, want ErrConsentRequired", err)
		}
		if stub.called != 0 {
			t.Errorf("unaccepted conversion reached the repository")
		}
	})
	t.Run("without intent", func(t *testing.T) {
		t.Parallel()

		stub := &stubConversionRepository{terms: nil}
		useCase := application.NewConvertUseCase(&stubConsentReader{accepted: true}, stub, fixedConvertClock{})
		if _, err := useCase.Execute(context.Background(), convertCommand()); !errors.Is(err, domain.ErrOptInMissing) {
			t.Errorf("Execute = %v, want ErrOptInMissing", err)
		}
	})
	t.Run("divergent rate", func(t *testing.T) {
		t.Parallel()

		stub := &stubConversionRepository{terms: convertTerms()}
		useCase := application.NewConvertUseCase(&stubConsentReader{accepted: true}, stub, fixedConvertClock{})
		cmd := convertCommand()
		cmd.RateNum = 999
		if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrRateMismatch) {
			t.Errorf("Execute = %v, want ErrRateMismatch", err)
		}
		if stub.called != 0 {
			t.Errorf("divergent rate reached the repository")
		}
	})
	t.Run("lapsed intent", func(t *testing.T) {
		t.Parallel()

		terms := convertTerms()
		terms.ValidTill = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		stub := &stubConversionRepository{terms: terms}
		useCase := application.NewConvertUseCase(&stubConsentReader{accepted: true}, stub, fixedConvertClock{})
		if _, err := useCase.Execute(context.Background(), convertCommand()); !errors.Is(err, domain.ErrOptInExpired) {
			t.Errorf("Execute = %v, want ErrOptInExpired", err)
		}
	})
}

func TestConvertUseCaseSettlesMatchingTerms(t *testing.T) {
	t.Parallel()

	want := &application.ConversionResult{TransferID: "transfer-id"}
	stub := &stubConversionRepository{terms: convertTerms(), result: want}
	useCase := newConvertUseCase(stub, true)
	got, err := useCase.Execute(context.Background(), convertCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Fatalf("Execute did not return the settled conversion")
	}
	if stub.called != 1 {
		t.Fatalf("calls = %d, want 1", stub.called)
	}
}
