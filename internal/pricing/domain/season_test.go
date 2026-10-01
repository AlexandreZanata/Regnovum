package domain

import (
	"errors"
	"testing"
	"time"
)

func TestSeasonalQuoteCapsExpiryByBookEnd(t *testing.T) {
	price, err := NewPriceMinor(35000000)
	if err != nil {
		t.Fatalf("NewPriceMinor: %v", err)
	}
	accepted := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	sighting, err := NewObservation("fonte-1", price.Int64(), accepted.Add(-time.Second), []byte(`{"a":1}`))
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	endsAt := time.Date(2026, 10, 4, 12, 2, 0, 0, time.UTC)
	quote, err := AcceptSeasonalQuote(SeasonalQuoteRequest{
		Price: price, Sightings: []Observation{sighting}, AcceptedAt: accepted, TTL: 5 * time.Minute,
		Freshness:         ObservationLimits{MaxFutureSkew: time.Minute, MaxAge: 5 * time.Minute},
		Season:            SeasonKey("temporada-1"),
		SeasonEndsAt:      endsAt,
		ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("AcceptSeasonalQuote: %v", err)
	}
	if !quote.ExpiresAt.Equal(endsAt) {
		t.Fatalf("capped expiry = %v, want book end %v", quote.ExpiresAt, endsAt)
	}
	if string(quote.Season) != "temporada-1" {
		t.Fatalf("quote season = %q, want temporada-1", quote.Season)
	}
	if err := quote.VerifyHash(); err != nil {
		t.Fatalf("capped seal does not verify: %v", err)
	}
	if !quote.Live(endsAt.Add(-time.Nanosecond)) {
		t.Fatal("quote dead a tick before the capped expiry")
	}
	if quote.Live(endsAt) {
		t.Fatal("quote live at the capped expiry tick: [accepted, expires) is half-open")
	}
}

func TestSeasonalQuoteRefusesWithoutReset(t *testing.T) {
	price, _ := NewPriceMinor(35000000)
	accepted := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	sighting, _ := NewObservation("fonte-1", price.Int64(), accepted.Add(-time.Second), []byte(`{"a":1}`))
	endsAt := accepted.Add(90 * 24 * time.Hour)
	if _, err := AcceptSeasonalQuote(SeasonalQuoteRequest{
		Price: price, Sightings: []Observation{sighting}, AcceptedAt: accepted, TTL: 5 * time.Minute,
		Freshness:         ObservationLimits{MaxFutureSkew: time.Minute, MaxAge: 5 * time.Minute},
		Season:            SeasonKey("temporada-1"),
		SeasonEndsAt:      endsAt,
		ResetAcknowledged: false,
	}); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("seasonal without reset = %v, want ErrInvalidQuote", err)
	}
}

func TestSeasonalResetAndKeyValidation(t *testing.T) {
	if err := RequireSeasonalReset(SeasonKey(CompatSeasonKey), false); err != nil {
		t.Fatalf("compat without reset = %v, want nil", err)
	}
	if _, err := ParseSeasonKey("   "); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("blank season = %v, want ErrInvalidQuote", err)
	}
}
