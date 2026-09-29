package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/application"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// stubQuoteRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: sealing happens before it is ever
// called, and canned identities prove the handoff.
type stubQuoteRepository struct {
	calls  int
	stored domain.Quote
	id     string
	err    error
}

func (s *stubQuoteRepository) Store(_ context.Context, quote domain.Quote) (string, error) {
	s.calls++
	s.stored = quote
	if s.err != nil {
		return "", s.err
	}
	return s.id, nil
}

func (s *stubQuoteRepository) Find(context.Context, string) (*domain.Quote, error) {
	return nil, errors.New("not implemented")
}

// stubQuoteClock fixes the acceptance instant.
type stubQuoteClock struct {
	now time.Time
}

func (c *stubQuoteClock) Now() time.Time { return c.now }

func quoteAcceptInstant() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func acceptCommand() application.AcceptQuoteCommand {
	sightings := []application.SightingInput{
		{Source: "fonte-1", PriceMinor: 35000000, ObservedAt: quoteAcceptInstant(), Payload: []byte(`{"a":1}`)},
		{Source: "fonte-2", PriceMinor: 35010000, ObservedAt: quoteAcceptInstant().Add(time.Second), Payload: []byte(`{"a":2}`)},
		{Source: "fonte-3", PriceMinor: 34990000, ObservedAt: quoteAcceptInstant().Add(2 * time.Second), Payload: []byte(`{"a":3}`)},
	}
	return application.AcceptQuoteCommand{
		PriceMinor: 35000000,
		Sightings:  sightings,
		TTL:        5 * time.Minute,
		MaxSkew:    time.Minute,
		MaxAge:     5 * time.Minute,
	}
}

func mustAcceptUseCase(stub *stubQuoteRepository, clock *stubQuoteClock) *application.AcceptQuoteUseCase {
	uc, err := application.NewAcceptQuoteUseCase(stub, clock)
	if err != nil {
		panic(err)
	}
	return uc
}

func TestAcceptQuoteSealsAndStores(t *testing.T) {
	t.Parallel()

	stub := &stubQuoteRepository{id: "quote-1"}
	clock := &stubQuoteClock{now: quoteAcceptInstant().Add(3 * time.Second)}
	quote, err := mustAcceptUseCase(stub, clock).Execute(context.Background(), acceptCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if quote.ID != "quote-1" || quote.Price.Int64() != 35000000 {
		t.Fatalf("stored quote changed: %+v", quote)
	}
	if !quote.ExpiresAt.Equal(quoteAcceptInstant().Add(3 * time.Second).Add(5 * time.Minute)) {
		t.Fatalf("expiry not counted from acceptance: %+v", quote)
	}
	if len(quote.Sightings) != 3 {
		t.Fatalf("sightings not carried: %+v", quote)
	}
	if err := quote.VerifyHash(); err != nil {
		t.Fatalf("stored hash does not verify: %v", err)
	}
	if stub.calls != 1 || stub.stored.Hash != quote.Hash {
		t.Fatalf("repository did not receive the sealed snapshot")
	}
}

func TestAcceptQuoteRefusesBeforeWriting(t *testing.T) {
	t.Parallel()

	t.Run("zero price", func(t *testing.T) {
		t.Parallel()
		stub := &stubQuoteRepository{id: "quote-1"}
		cmd := acceptCommand()
		cmd.PriceMinor = 0
		if _, err := mustAcceptUseCase(stub, &stubQuoteClock{now: quoteAcceptInstant()}).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidPrice) {
			t.Fatalf("Execute = %v, want ErrInvalidPrice", err)
		}
		if stub.calls != 0 {
			t.Fatalf("priceless quote reached the repository")
		}
	})
	t.Run("bad source", func(t *testing.T) {
		t.Parallel()
		stub := &stubQuoteRepository{id: "quote-1"}
		cmd := acceptCommand()
		cmd.Sightings[0].Source = "OK"
		if _, err := mustAcceptUseCase(stub, &stubQuoteClock{now: quoteAcceptInstant()}).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidSource) {
			t.Fatalf("Execute = %v, want ErrInvalidSource", err)
		}
		if stub.calls != 0 {
			t.Fatalf("sourceless quote reached the repository")
		}
	})
	t.Run("void lifetime", func(t *testing.T) {
		t.Parallel()
		stub := &stubQuoteRepository{id: "quote-1"}
		cmd := acceptCommand()
		cmd.TTL = 0
		if _, err := mustAcceptUseCase(stub, &stubQuoteClock{now: quoteAcceptInstant().Add(3 * time.Second)}).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidQuote) {
			t.Fatalf("Execute = %v, want ErrInvalidQuote", err)
		}
		if stub.calls != 0 {
			t.Fatalf("lifetime-less quote reached the repository")
		}
	})
	t.Run("storage failure", func(t *testing.T) {
		t.Parallel()
		stub := &stubQuoteRepository{err: errors.New("disk on fire")}
		if _, err := mustAcceptUseCase(stub, &stubQuoteClock{now: quoteAcceptInstant().Add(3 * time.Second)}).Execute(context.Background(), acceptCommand()); err == nil {
			t.Fatalf("Execute = nil, want the storage failure")
		}
	})
}

func TestAcceptQuoteRefusesIncompleteComposition(t *testing.T) {
	t.Parallel()

	stub := &stubQuoteRepository{id: "quote-1"}
	if _, err := application.NewAcceptQuoteUseCase(nil, &stubQuoteClock{now: quoteAcceptInstant()}); !errors.Is(err, domain.ErrInvalidQuote) {
		t.Fatalf("nil repository = %v, want ErrInvalidQuote", err)
	}
	if _, err := application.NewAcceptQuoteUseCase(stub, nil); !errors.Is(err, domain.ErrInvalidQuote) {
		t.Fatalf("nil clock = %v, want ErrInvalidQuote", err)
	}
}
