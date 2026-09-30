package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// TestConvertUseCaseRequiresSeasonalReset proves an omitted reset
// never authorizes a seasonal conversion, while the compat-legacy
// path keeps its legacy contract without one.
func TestConvertUseCaseRequiresSeasonalReset(t *testing.T) {
	t.Parallel()

	stub := &stubConversionRepository{terms: convertTerms()}
	useCase := newConvertUseCase(stub, true)
	cmd := convertCommand()
	cmd.Season = "temporada-futura"
	cmd.ResetAcknowledged = false
	if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrConsentRequired) {
		t.Fatalf("seasonal without reset = %v, want ErrConsentRequired", err)
	}
	if stub.called != 0 {
		t.Fatal("refused seasonal conversion reached the repository")
	}

	stub = &stubConversionRepository{terms: convertTerms(), result: &application.ConversionResult{}}
	useCase = newConvertUseCase(stub, true)
	cmd = convertCommand()
	cmd.Season = "temporada-futura"
	cmd.ResetAcknowledged = true
	if _, err := useCase.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("seasonal with reset: %v", err)
	}

	stub = &stubConversionRepository{terms: convertTerms(), result: &application.ConversionResult{}}
	useCase = newConvertUseCase(stub, true)
	if _, err := useCase.Execute(context.Background(), convertCommand()); err != nil {
		t.Fatalf("legacy compat without reset: %v", err)
	}
}

// TestConvertUseCaseRefusesBadSeason proves blank and malformed books
// are refused before any consent or term is read.
func TestConvertUseCaseRefusesBadSeason(t *testing.T) {
	t.Parallel()

	stub := &stubConversionRepository{terms: convertTerms()}
	useCase := newConvertUseCase(stub, true)
	cmd := convertCommand()
	cmd.Season = "   "
	if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrMissingSeason) {
		t.Fatalf("blank season = %v, want ErrMissingSeason", err)
	}
}

// TestGrantGuardUsesSeasonBook proves the grant decision is per book:
// an unfounded book answers legacy even when another book holds S.
func TestGrantGuardUsesSeasonBook(t *testing.T) {
	t.Parallel()

	stock, err := domain.NewMilliInk(1000)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	policy := &stubGrantPolicy{genesis: true, stock: stock}
	useCase := application.NewGrantGuardUseCase(policy)
	if got, err := useCase.Execute(context.Background(), application.GrantCommand{Millis: 100, Season: "temporada-1"}); err != nil || got != domain.GrantSourceTreasury {
		t.Fatalf("seasonal funded = %q, %v; want treasury", got, err)
	}
	if _, err := useCase.Execute(context.Background(), application.GrantCommand{Millis: 100, Season: "   "}); !errors.Is(err, domain.ErrMissingSeason) {
		t.Fatalf("blank season = %v, want ErrMissingSeason", err)
	}
}
