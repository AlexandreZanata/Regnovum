package domain_test

import (
	"errors"
	"math"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

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

func termsPrice(t *testing.T) domain.PriceMinor {
	t.Helper()
	price, err := domain.NewPriceMinor(35000000)
	if err != nil {
		t.Fatalf("NewPriceMinor: %v", err)
	}
	return price
}

// TestQuoteTermsDerivesGoldenExample proves the derivation leg by
// leg: 1% spread over R$350.000,00, R$0,30 fee, 10% tax and R$100,00
// settle 253833 milliINK with every fiat leg exact.
func TestQuoteTermsDerivesGoldenExample(t *testing.T) {
	t.Parallel()

	terms, err := domain.QuoteTerms(10000, termsPrice(t), termsSchedule())
	if err != nil {
		t.Fatalf("QuoteTerms: %v", err)
	}
	if terms.PriceAskMinor != 35350000 {
		t.Fatalf("ask = %d, want 35350000", terms.PriceAskMinor)
	}
	if terms.FeeMinor != 30 || terms.TaxMinor != 997 || terms.FiatNetMinor != 8973 {
		t.Fatalf("fiat legs changed: %+v", terms)
	}
	if terms.InkMilli != 253833 || terms.DustMilli != 0 {
		t.Fatalf("ink = %d dust = %d, want 253833 and 0", terms.InkMilli, terms.DustMilli)
	}
	if terms.Currency != "BRL" || terms.Schedule != 1 {
		t.Fatalf("currency/schedule not carried: %+v", terms)
	}
	if err := terms.VerifyTermsHash(); err != nil {
		t.Fatalf("seal of derived terms: %v", err)
	}
	mutated := terms
	mutated.InkMilli++
	if err := mutated.VerifyTermsHash(); !errors.Is(err, domain.ErrInvalidTerms) {
		t.Fatalf("edited terms = %v, want ErrInvalidTerms", err)
	}
}

// TestQuoteTermsRoundsCentsEdges proves centavo and milliINK edges: a
// single centavo settles 28 milliINK, divisibility floors with
// reported dust, and each rounding mode judges its remainder.
func TestQuoteTermsRoundsCentsEdges(t *testing.T) {
	t.Parallel()

	t.Run("single centavo", func(t *testing.T) {
		t.Parallel()
		schedule := termsSchedule()
		schedule.FeeMinor = 0
		schedule.TaxBps = 0
		schedule.MinFiatMinor = 1
		terms, err := domain.QuoteTerms(1, termsPrice(t), schedule)
		if err != nil {
			t.Fatalf("QuoteTerms: %v", err)
		}
		if terms.InkMilli != 28 {
			t.Fatalf("ink = %d, want 28", terms.InkMilli)
		}
	})
	t.Run("divisibility dust", func(t *testing.T) {
		t.Parallel()
		schedule := termsSchedule()
		schedule.StepMilliInk = 1000
		terms, err := domain.QuoteTerms(10000, termsPrice(t), schedule)
		if err != nil {
			t.Fatalf("QuoteTerms: %v", err)
		}
		if terms.InkMilli != 253000 || terms.DustMilli != 833 {
			t.Fatalf("ink = %d dust = %d, want 253000 and 833", terms.InkMilli, terms.DustMilli)
		}
	})
	t.Run("rounding modes", func(t *testing.T) {
		t.Parallel()
		price, err := domain.NewPriceMinor(10001)
		if err != nil {
			t.Fatalf("NewPriceMinor: %v", err)
		}
		schedule := termsSchedule()
		schedule.SpreadBps = 5000
		schedule.FeeMinor = 0
		schedule.TaxBps = 0
		schedule.MinFiatMinor = 1
		schedule.Rounding = domain.RoundDown
		down, err := domain.QuoteTerms(10000, price, schedule)
		if err != nil {
			t.Fatalf("down QuoteTerms: %v", err)
		}
		schedule.Rounding = domain.RoundUp
		up, err := domain.QuoteTerms(10000, price, schedule)
		if err != nil {
			t.Fatalf("up QuoteTerms: %v", err)
		}
		schedule.Rounding = domain.RoundNearest
		near, err := domain.QuoteTerms(10000, price, schedule)
		if err != nil {
			t.Fatalf("nearest QuoteTerms: %v", err)
		}
		if down.PriceAskMinor != 15001 || up.PriceAskMinor != 15002 || near.PriceAskMinor != 15002 {
			t.Fatalf("ask down/up/nearest = %d/%d/%d, want 15001/15002/15002",
				down.PriceAskMinor, up.PriceAskMinor, near.PriceAskMinor)
		}
	})
}

// TestQuoteTermsRefusesBadTickets proves zero, minimum, fees beyond
// the ticket, void schedules and overflowing tickets fail closed.
func TestQuoteTermsRefusesBadTickets(t *testing.T) {
	t.Parallel()

	t.Run("zero ticket", func(t *testing.T) {
		t.Parallel()
		if _, err := domain.QuoteTerms(0, termsPrice(t), termsSchedule()); !errors.Is(err, domain.ErrInvalidTerms) {
			t.Fatalf("QuoteTerms = %v, want ErrInvalidTerms", err)
		}
	})
	t.Run("fee eats ticket", func(t *testing.T) {
		t.Parallel()
		if _, err := domain.QuoteTerms(30, termsPrice(t), termsSchedule()); !errors.Is(err, domain.ErrInvalidTerms) {
			t.Fatalf("QuoteTerms = %v, want ErrInvalidTerms", err)
		}
	})
	t.Run("below minimum", func(t *testing.T) {
		t.Parallel()
		schedule := termsSchedule()
		schedule.FeeMinor = 0
		schedule.TaxBps = 0
		if _, err := domain.QuoteTerms(99, termsPrice(t), schedule); !errors.Is(err, domain.ErrInvalidTerms) {
			t.Fatalf("QuoteTerms = %v, want ErrInvalidTerms", err)
		}
	})
	t.Run("void schedule", func(t *testing.T) {
		t.Parallel()
		for _, mutate := range []func(*domain.FeeSchedule){
			func(s *domain.FeeSchedule) { s.Version = 0 },
			func(s *domain.FeeSchedule) { s.SatsPerInk = 0 },
			func(s *domain.FeeSchedule) { s.SpreadBps = -1 },
			func(s *domain.FeeSchedule) { s.FeeMinor = -1 },
			func(s *domain.FeeSchedule) { s.TaxBps = -1 },
			func(s *domain.FeeSchedule) { s.MinFiatMinor = 0 },
			func(s *domain.FeeSchedule) { s.StepMilliInk = 0 },
			func(s *domain.FeeSchedule) { s.Rounding = domain.Rounding(99) },
		} {
			schedule := termsSchedule()
			mutate(&schedule)
			if _, err := domain.QuoteTerms(10000, termsPrice(t), schedule); !errors.Is(err, domain.ErrInvalidTerms) {
				t.Fatalf("QuoteTerms(%+v) = nil, want ErrInvalidTerms", schedule)
			}
		}
	})
	t.Run("overflowing ticket", func(t *testing.T) {
		t.Parallel()
		schedule := termsSchedule()
		schedule.FeeMinor = 0
		schedule.TaxBps = 0
		schedule.MinFiatMinor = 1
		if _, err := domain.QuoteTerms(math.MaxInt64, termsPrice(t), schedule); !errors.Is(err, domain.ErrInvalidTerms) {
			t.Fatalf("QuoteTerms = %v, want ErrInvalidTerms", err)
		}
	})
}

func TestParseTermsLocale(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"pt", "en"} {
		locale, err := domain.ParseTermsLocale(raw)
		if err != nil {
			t.Errorf("ParseTermsLocale(%q): %v", raw, err)
		} else if locale.String() != raw {
			t.Errorf("ParseTermsLocale(%q).String() = %q", raw, locale.String())
		}
	}
	for _, raw := range []string{"", "PT", "pt-BR", " pt", "fr"} {
		if _, err := domain.ParseTermsLocale(raw); !errors.Is(err, domain.ErrInvalidTerms) {
			t.Errorf("ParseTermsLocale(%q) = %v, want ErrInvalidTerms", raw, err)
		}
	}
	pt := domain.TermsTitlesFor(domain.TermsLocalePortuguese)
	en := domain.TermsTitlesFor(domain.TermsLocaleEnglish)
	if pt.Title == "" || pt.Title == en.Title {
		t.Fatalf("titles did not translate: %q vs %q", pt.Title, en.Title)
	}
}
