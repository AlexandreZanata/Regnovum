package postgres_test

// P35-T03 — versioned purchase quotes on real PostgreSQL.
//
// One acceptance seals price, sightings, instants and hash in a
// single transaction: the persisted expiry judges later arrivals
// instead of recomputing them, and the frozen source set serves even
// after a source withdraws. The tests prove on a disposable database:
// exact round-trip with a verifiable seal, absence reporting, a late
// arrival judged by the persisted expiry, and the stored terms
// surviving a withdrawn source.

import (
	"context"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	pricepostgres "github.com/AlexandreZanata/Regnovum/internal/pricing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/application"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// pricingTestClock fixes the acceptance instant.
type pricingTestClock struct {
	now time.Time
}

func (c *pricingTestClock) Now() time.Time { return c.now }

func quoteCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func quoteRoundInstant() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func acceptQuoteCmd() application.AcceptQuoteCommand {
	return application.AcceptQuoteCommand{
		PriceMinor: 35000000,
		Sightings: []application.SightingInput{
			{Source: "fonte-1", PriceMinor: 35000000, ObservedAt: quoteRoundInstant(), Payload: []byte(`{"a":1}`)},
			{Source: "fonte-2", PriceMinor: 35010000, ObservedAt: quoteRoundInstant().Add(time.Second), Payload: []byte(`{"a":2}`)},
			{Source: "fonte-3", PriceMinor: 34990000, ObservedAt: quoteRoundInstant().Add(2 * time.Second), Payload: []byte(`{"a":3}`)},
		},
		TTL:     5 * time.Minute,
		MaxSkew: time.Minute,
		MaxAge:  5 * time.Minute,
	}
}

func mustAcceptQuote(t *testing.T, ctx context.Context, repo *pricepostgres.Repository, clock *pricingTestClock, cmd application.AcceptQuoteCommand) domain.Quote {
	t.Helper()
	uc, err := application.NewAcceptQuoteUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewAcceptQuoteUseCase: %v", err)
	}
	quote, err := uc.Execute(ctx, cmd)
	if err != nil {
		t.Fatalf("AcceptQuote: %v", err)
	}
	return quote
}

// TestQuoteAcceptanceRoundTripsSealedTerms proves the happy path: the
// stored snapshot recomputes its seal, carries price, ordered sources
// and instants, and judges liveness from the persisted expiry.
func TestQuoteAcceptanceRoundTripsSealedTerms(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := quoteCtx()
	defer cancel()

	repo := pricepostgres.NewRepository(pool)
	accepted := quoteRoundInstant().Add(3 * time.Second)
	quote := mustAcceptQuote(t, ctx, repo, &pricingTestClock{now: accepted}, acceptQuoteCmd())
	if quote.ID == "" {
		t.Fatalf("stored quote has no identity")
	}

	stored, err := repo.Find(ctx, quote.ID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if stored == nil {
		t.Fatalf("stored quote not found")
	}
	if stored.Price.Int64() != 35000000 || len(stored.Sightings) != 3 {
		t.Fatalf("stored terms changed: %+v", stored)
	}
	if stored.Sightings[0].Source.String() != "fonte-1" || stored.Sightings[0].Price.Int64() != 35000000 {
		t.Fatalf("stored sightings changed: %+v", stored.Sightings)
	}
	if !stored.ExpiresAt.Equal(accepted.Add(5 * time.Minute)) {
		t.Fatalf("stored expiry = %v, want acceptance plus TTL", stored.ExpiresAt)
	}
	if err := stored.VerifyHash(); err != nil {
		t.Fatalf("stored seal does not verify: %v", err)
	}
	if !stored.Live(accepted.Add(5*time.Minute - time.Second)) {
		t.Fatalf("snapshot dead a tick before its persisted expiry")
	}
	if stored.Live(accepted.Add(5 * time.Minute)) {
		t.Fatalf("snapshot alive on its persisted expiry tick")
	}
}

// TestQuoteFindReportsAbsence proves unknown identities resolve to
// absence instead of a guess.
func TestQuoteFindReportsAbsence(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := quoteCtx()
	defer cancel()

	repo := pricepostgres.NewRepository(pool)
	stored, err := repo.Find(ctx, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if stored != nil {
		t.Fatalf("absence returned %+v", stored)
	}
}

// TestQuoteLateArrivalJudgesPersistedExpiry proves TTL counts from
// acceptance: a webhook arriving five minutes late meets the
// persisted expiry instead of a fresh window.
func TestQuoteLateArrivalJudgesPersistedExpiry(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := quoteCtx()
	defer cancel()

	repo := pricepostgres.NewRepository(pool)
	accepted := quoteRoundInstant().Add(3 * time.Second)
	quote := mustAcceptQuote(t, ctx, repo, &pricingTestClock{now: accepted}, acceptQuoteCmd())

	late, err := repo.Find(ctx, quote.ID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if late.Live(accepted.Add(10 * time.Minute)) {
		t.Fatalf("late arrival alive past the persisted expiry")
	}
	if !late.Live(accepted.Add(4 * time.Minute)) {
		t.Fatalf("timely arrival dead inside the persisted window")
	}
}

// TestQuoteSurvivesWithdrawnSource proves the frozen source set: the
// stored snapshot still names all three sources with verifiable
// provenance after the round drops one of them.
func TestQuoteSurvivesWithdrawnSource(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := quoteCtx()
	defer cancel()

	repo := pricepostgres.NewRepository(pool)
	accepted := quoteRoundInstant().Add(3 * time.Second)
	quote := mustAcceptQuote(t, ctx, repo, &pricingTestClock{now: accepted}, acceptQuoteCmd())

	registry := application.NewRegistry()
	for _, id := range []string{"fonte-1", "fonte-2"} {
		sourceID, err := domain.ParseSourceID(id)
		if err != nil {
			t.Fatalf("ParseSourceID: %v", err)
		}
		if err := registry.Register(&withdrawnStubSource{id: sourceID}); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	if got := registry.Len(); got != 2 {
		t.Fatalf("round without fonte-3 has %d sources", got)
	}

	stored, err := repo.Find(ctx, quote.ID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(stored.Sources()) != 3 {
		t.Fatalf("withdrawal rewrote history: %+v", stored.Sources())
	}
	for _, sighting := range stored.Sightings {
		if sighting.PayloadHash == "" {
			t.Fatalf("sighting without provenance: %+v", sighting)
		}
	}
	if err := stored.VerifyHash(); err != nil {
		t.Fatalf("seal after withdrawal: %v", err)
	}
	if !stored.Live(accepted.Add(time.Minute)) {
		t.Fatalf("snapshot dead while its window holds")
	}
}

// withdrawnStubSource stands in for a remaining source after one
// withdraws: it is never fetched in this test, only counted.
type withdrawnStubSource struct {
	id domain.SourceID
}

func (s *withdrawnStubSource) ID() domain.SourceID { return s.id }

func (s *withdrawnStubSource) Fetch(context.Context) (domain.Observation, error) {
	return domain.Observation{}, domain.ErrSourceUnavailable
}
