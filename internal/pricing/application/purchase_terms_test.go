package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/application"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

func termsInstant() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func termsSightings(prices ...int64) []application.SightingInput {
	ids := []string{"fonte-1", "fonte-2", "fonte-3"}
	inputs := make([]application.SightingInput, 0, len(prices))
	for i, price := range prices {
		inputs = append(inputs, application.SightingInput{
			Source:     ids[i%len(ids)],
			PriceMinor: price,
			ObservedAt: termsInstant(),
			Payload:    []byte(`{}`),
		})
	}
	return inputs
}

func termsSchedule() domain.FeeSchedule {
	return domain.FeeSchedule{
		Version:      1,
		SatsPerInk:   100,
		SpreadBps:    100,
		FeeMinor:     30,
		TaxBps:       1000,
		MinFiatMinor: 100,
		StepMilliInk: 1,
		Rounding:     domain.RoundDown,
	}
}

func purchaseCommand() application.PurchaseTermsCommand {
	return application.PurchaseTermsCommand{
		FiatMinor:  10000,
		Sightings:  termsSightings(35000000, 35010000, 34990000),
		MinSources: 3,
		MaxSpread:  100000,
		Schedule:   termsSchedule(),
		Locale:     "pt",
	}
}

// TestQuotePurchasePresentsSealedTerms proves the document: guarded
// median in, sealed canonical terms out, titles in pt.
func TestQuotePurchasePresentsSealedTerms(t *testing.T) {
	t.Parallel()

	document, err := application.NewQuotePurchaseUseCase().Execute(context.Background(), purchaseCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if document.Locale != "pt" || document.Titles.Title == "" {
		t.Fatalf("document not rendered in pt: %+v", document.Titles)
	}
	if document.Terms.InkMilli != 253833 || document.Terms.TaxMinor != 997 {
		t.Fatalf("terms changed: %+v", document.Terms)
	}
	if err := document.Terms.VerifyTermsHash(); err != nil {
		t.Fatalf("displayed seal does not verify: %v", err)
	}
}

// TestQuotePurchaseKeepsCanonicalAcrossSourcesAndLocales proves the
// canonical values never move with provenance or language: two
// distinct source sets with one median price identically, and en
// renders the same amounts under translated titles.
func TestQuotePurchaseKeepsCanonicalAcrossSourcesAndLocales(t *testing.T) {
	t.Parallel()

	now := termsInstant()
	other := []application.SightingInput{
		{Source: "fonte-4", PriceMinor: 35000000, ObservedAt: now, Payload: []byte(`{}`)},
		{Source: "fonte-5", PriceMinor: 35010000, ObservedAt: now, Payload: []byte(`{}`)},
		{Source: "fonte-6", PriceMinor: 34990000, ObservedAt: now, Payload: []byte(`{}`)},
	}
	first, err := application.NewQuotePurchaseUseCase().Execute(context.Background(), purchaseCommand())
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	cmd := purchaseCommand()
	cmd.Sightings = other
	second, err := application.NewQuotePurchaseUseCase().Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if first.Terms.Hash != second.Terms.Hash {
		t.Fatalf("source change moved canonical terms: %+v vs %+v", first.Terms, second.Terms)
	}
	cmd.Locale = "en"
	english, err := application.NewQuotePurchaseUseCase().Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("en Execute: %v", err)
	}
	if english.Terms.Hash != first.Terms.Hash {
		t.Fatalf("locale moved canonical terms: %+v vs %+v", english.Terms, first.Terms)
	}
	if english.Titles.Title == first.Titles.Title {
		t.Fatalf("en title did not translate: %q", english.Titles.Title)
	}
}

// TestQuotePurchaseRefusesBadRounds proves unknown locales, thin or
// divergent rounds and void tickets refuse before pricing.
func TestQuotePurchaseRefusesBadRounds(t *testing.T) {
	t.Parallel()

	t.Run("unknown locale", func(t *testing.T) {
		t.Parallel()
		cmd := purchaseCommand()
		cmd.Locale = "fr"
		if _, err := application.NewQuotePurchaseUseCase().Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidTerms) {
			t.Fatalf("Execute = %v, want ErrInvalidTerms", err)
		}
	})
	t.Run("thin round", func(t *testing.T) {
		t.Parallel()
		cmd := purchaseCommand()
		cmd.Sightings = termsSightings(35000000, 35010000)
		if _, err := application.NewQuotePurchaseUseCase().Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInsufficientSources) {
			t.Fatalf("Execute = %v, want ErrInsufficientSources", err)
		}
	})
	t.Run("divergent round", func(t *testing.T) {
		t.Parallel()
		cmd := purchaseCommand()
		cmd.Sightings = termsSightings(35000000, 35010000, 99900000)
		if _, err := application.NewQuotePurchaseUseCase().Execute(context.Background(), cmd); !errors.Is(err, domain.ErrDivergentSources) {
			t.Fatalf("Execute = %v, want ErrDivergentSources", err)
		}
	})
	t.Run("void ticket", func(t *testing.T) {
		t.Parallel()
		cmd := purchaseCommand()
		cmd.FiatMinor = 0
		if _, err := application.NewQuotePurchaseUseCase().Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidTerms) {
			t.Fatalf("Execute = %v, want ErrInvalidTerms", err)
		}
	})
}
