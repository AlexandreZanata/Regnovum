package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// PriceMinor is one BTC/BRL reference price in integer minor units
// (centavos per BTC). Integers only: a fractional price is refused,
// never rounded, and float never appears on this path.
type PriceMinor int64

// NewPriceMinor validates a reference price: strictly positive.
// Zero and negative prices never price anything.
func NewPriceMinor(millis int64) (PriceMinor, error) {
	if millis <= 0 {
		return 0, ErrInvalidPrice
	}
	return PriceMinor(millis), nil
}

// Int64 returns the stored minor units.
func (p PriceMinor) Int64() int64 { return int64(p) }

// ObservationLimits bounds one freshness check: how far ahead of now
// an instant may claim (clock skew) and how far behind it may lag
// (staleness). Windows arrive per call: no ratified TTL lives here,
// and later phases set the vigente values where quotes are accepted.
type ObservationLimits struct {
	MaxFutureSkew time.Duration
	MaxAge        time.Duration
}

// Valid reports whether both windows are strictly positive.
func (l ObservationLimits) Valid() bool {
	return l.MaxFutureSkew > 0 && l.MaxAge > 0
}

// Observation is one validated rate sighting: its source, its integer
// price, the instant the source observed it and the hash of the raw
// payload it was parsed from. The hash binds sighting to bytes for the
// audit trail: a payload that does not reproduce it was adulterated in
// transit and never prices.
type Observation struct {
	Source      SourceID
	Price       PriceMinor
	ObservedAt  time.Time
	PayloadHash string
}

// NewObservation builds one sighting from a raw payload, hashing the
// bytes it was parsed from. Empty payloads, non-positive prices and
// zero instants are refused before any hash is minted.
func NewObservation(source SourceID, priceMinor int64, observedAt time.Time, payload []byte) (Observation, error) {
	if source.String() == "" {
		return Observation{}, ErrInvalidObservation
	}
	price, err := NewPriceMinor(priceMinor)
	if err != nil {
		return Observation{}, err
	}
	if observedAt.IsZero() {
		return Observation{}, ErrInvalidObservation
	}
	if len(payload) == 0 {
		return Observation{}, ErrInvalidObservation
	}
	sum := sha256.Sum256(payload)
	return Observation{
		Source:      source,
		Price:       price,
		ObservedAt:  observedAt.UTC(),
		PayloadHash: hex.EncodeToString(sum[:]),
	}, nil
}

// VerifyProvenance recomputes the payload hash and refuses a sighting
// whose bytes no longer reproduce it.
func (o Observation) VerifyProvenance(payload []byte) error {
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != o.PayloadHash {
		return ErrInvalidObservation
	}
	return nil
}

// Fresh reports whether the sighting falls inside the window around
// now: not further ahead than the skew allowance, not older than the
// age allowance. Bounds are half-open on the future side and closed
// on the past side: exactly now passes, exactly skew-ahead fails, and
// exactly age-old still prices.
func (o Observation) Fresh(now time.Time, limits ObservationLimits) error {
	if !limits.Valid() {
		return ErrInvalidObservation
	}
	instant := now.UTC()
	if !o.ObservedAt.Before(instant.Add(limits.MaxFutureSkew)) {
		return ErrStaleObservation
	}
	if o.ObservedAt.Before(instant.Add(-limits.MaxAge)) {
		return ErrStaleObservation
	}
	return nil
}
