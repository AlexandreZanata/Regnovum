package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

func quoteInstant() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func quoteFreshness() domain.ObservationLimits {
	return domain.ObservationLimits{MaxFutureSkew: time.Minute, MaxAge: 5 * time.Minute}
}

func quoteSighting(t *testing.T, id string, priceMinor int64, at time.Time) domain.Observation {
	t.Helper()
	source, err := domain.ParseSourceID(id)
	if err != nil {
		t.Fatalf("ParseSourceID: %v", err)
	}
	seen, err := domain.NewObservation(source, priceMinor, at, []byte(`{}`))
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	return seen
}

func quoteSightings(t *testing.T) []domain.Observation {
	t.Helper()
	return []domain.Observation{
		quoteSighting(t, "fonte-1", 35000000, quoteInstant()),
		quoteSighting(t, "fonte-2", 35010000, quoteInstant().Add(2*time.Second)),
		quoteSighting(t, "fonte-3", 34990000, quoteInstant().Add(time.Second)),
	}
}

func quotePrice(t *testing.T) domain.PriceMinor {
	t.Helper()
	price, err := domain.NewPriceMinor(35000000)
	if err != nil {
		t.Fatalf("NewPriceMinor: %v", err)
	}
	return price
}

func mustAcceptQuote(t *testing.T) domain.Quote {
	t.Helper()
	quote, err := domain.AcceptQuote(quotePrice(t), quoteSightings(t), quoteInstant().Add(3*time.Second), 5*time.Minute, quoteFreshness())
	if err != nil {
		t.Fatalf("AcceptQuote: %v", err)
	}
	return quote
}

// TestAcceptQuoteSealsTerms proves the snapshot: price, ordered
// sightings, round observation at the latest sighting, expiry five
// minutes past acceptance, and a hash the terms recompute.
func TestAcceptQuoteSealsTerms(t *testing.T) {
	t.Parallel()

	quote := mustAcceptQuote(t)
	if quote.Price.Int64() != 35000000 {
		t.Fatalf("price = %d, want 35000000", quote.Price.Int64())
	}
	sources := quote.Sources()
	if len(sources) != 3 || sources[0].String() != "fonte-1" || sources[2].String() != "fonte-3" {
		t.Fatalf("sources not ordered: %+v", sources)
	}
	if !quote.ObservedAt.Equal(quoteInstant().Add(2 * time.Second)) {
		t.Fatalf("round observation = %v, want the latest sighting", quote.ObservedAt)
	}
	if !quote.AcceptedAt.Equal(quoteInstant().Add(3*time.Second)) || !quote.ExpiresAt.Equal(quoteInstant().Add(3*time.Second).Add(5*time.Minute)) {
		t.Fatalf("lifetime not sealed: %+v", quote)
	}
	if quote.Hash == "" {
		t.Fatalf("hash missing")
	}
	if err := quote.VerifyHash(); err != nil {
		t.Fatalf("VerifyHash of sealed terms: %v", err)
	}
	mutated := quote
	mutated.Price = 35000001
	if err := mutated.VerifyHash(); !errors.Is(err, domain.ErrInvalidQuote) {
		t.Fatalf("edited snapshot = %v, want ErrInvalidQuote", err)
	}
}

// TestQuoteLiveJudgesExpiryTick proves the half-open window: a tick
// before expiry prices, the expiry tick itself does not, and a late
// arrival judges the persisted expiry instead of extending it.
func TestQuoteLiveJudgesExpiryTick(t *testing.T) {
	t.Parallel()

	quote := mustAcceptQuote(t)
	accepted := quoteInstant().Add(3 * time.Second)
	for _, at := range []time.Time{
		accepted,
		accepted.Add(5*time.Minute - time.Second),
		accepted.Add(5*time.Minute - time.Nanosecond),
	} {
		if !quote.Live(at) {
			t.Errorf("Live(%v) = false, want true", at.Sub(accepted))
		}
	}
	for _, at := range []time.Time{
		accepted.Add(-time.Second),
		accepted.Add(5 * time.Minute),
		accepted.Add(10 * time.Minute),
	} {
		if quote.Live(at) {
			t.Errorf("Live(%v) = true, want false", at.Sub(accepted))
		}
	}
}

// TestAcceptQuoteNormalizesZones proves ISO/UTC handling: instants
// carried in another zone seal the same UTC snapshot.
func TestAcceptQuoteNormalizesZones(t *testing.T) {
	t.Parallel()

	zoned := quoteInstant().In(time.FixedZone("BRT-3", -3*60*60))
	sighting := quoteSighting(t, "fonte-1", 35000000, zoned)
	quote, err := domain.AcceptQuote(quotePrice(t), []domain.Observation{sighting}, zoned, 5*time.Minute, quoteFreshness())
	if err != nil {
		t.Fatalf("AcceptQuote: %v", err)
	}
	if quote.ObservedAt.Location() != time.UTC || !quote.ObservedAt.Equal(quoteInstant()) {
		t.Fatalf("instant not normalized: %+v", quote.ObservedAt)
	}
}

// TestAcceptQuoteRefusesBadTerms proves malformed acceptances never
// seal: non-positive prices, empty or repeated sightings, zero
// instants, void windows and skew-aged rounds.
func TestAcceptQuoteRefusesBadTerms(t *testing.T) {
	t.Parallel()

	accepted := quoteInstant().Add(3 * time.Second)
	_, err := domain.AcceptQuote(0, quoteSightings(t), accepted, 5*time.Minute, quoteFreshness())
	if !errors.Is(err, domain.ErrInvalidPrice) {
		t.Errorf("zero price = %v, want ErrInvalidPrice", err)
	}
	_, err = domain.AcceptQuote(quotePrice(t), nil, accepted, 5*time.Minute, quoteFreshness())
	if !errors.Is(err, domain.ErrInsufficientSources) {
		t.Errorf("no sightings = %v, want ErrInsufficientSources", err)
	}
	repeated := []domain.Observation{quoteSightings(t)[0], quoteSightings(t)[0]}
	_, err = domain.AcceptQuote(quotePrice(t), repeated, accepted, 5*time.Minute, quoteFreshness())
	if !errors.Is(err, domain.ErrDuplicateSource) {
		t.Errorf("repeated source = %v, want ErrDuplicateSource", err)
	}
	_, err = domain.AcceptQuote(quotePrice(t), quoteSightings(t), time.Time{}, 5*time.Minute, quoteFreshness())
	if !errors.Is(err, domain.ErrInvalidQuote) {
		t.Errorf("zero acceptance = %v, want ErrInvalidQuote", err)
	}
	_, err = domain.AcceptQuote(quotePrice(t), quoteSightings(t), accepted, 0, quoteFreshness())
	if !errors.Is(err, domain.ErrInvalidQuote) {
		t.Errorf("zero TTL = %v, want ErrInvalidQuote", err)
	}
	aged := []domain.Observation{quoteSighting(t, "fonte-1", 35000000, quoteInstant().Add(-time.Hour))}
	_, err = domain.AcceptQuote(quotePrice(t), aged, accepted, 5*time.Minute, quoteFreshness())
	if !errors.Is(err, domain.ErrStaleObservation) {
		t.Errorf("aged round = %v, want ErrStaleObservation", err)
	}
	ahead := []domain.Observation{quoteSighting(t, "fonte-1", 35000000, accepted.Add(time.Hour))}
	_, err = domain.AcceptQuote(quotePrice(t), ahead, accepted, 5*time.Minute, quoteFreshness())
	if !errors.Is(err, domain.ErrStaleObservation) {
		t.Errorf("future round = %v, want ErrStaleObservation", err)
	}
}
