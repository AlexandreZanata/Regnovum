package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Quote is one accepted purchase quotation snapshot: the guarded
// integer price, the judged sightings in source order, the round
// observation instant, the acceptance instant, the expiry instant and
// the hash binding them. The ID travels empty until persistence names
// it; it never enters the hash, so a stored row recomputes exactly
// what acceptance sealed. Season names the purchase book and travels
// beside the seal: the seal covers price and instants, the book check
// matches seasons before any intent opens. Later webhooks judge the
// persisted expiry instead of recomputing it, and a withdrawn source
// never rewrites these terms.
type Quote struct {
	ID         string
	Price      PriceMinor
	Sightings  []Observation
	ObservedAt time.Time
	AcceptedAt time.Time
	ExpiresAt  time.Time
	Hash       string
	Season     SeasonKey
}

// Sources returns the snapshot source set in canonical order.
func (q Quote) Sources() []SourceID {
	sources := make([]SourceID, 0, len(q.Sightings))
	for _, sighting := range q.Sightings {
		sources = append(sources, sighting.Source)
	}
	return sources
}

// AcceptQuote seals one quotation at acceptance: every sighting is
// re-judged against the acceptance instant with the round windows,
// the lifetime counts from acceptance with the approved TTL, and the
// round observation is the latest sighting instant. Windows and
// lifetime arrive per call: no ratified TTL lives here.
func AcceptQuote(price PriceMinor, sightings []Observation, acceptedAt time.Time, ttl time.Duration, freshness ObservationLimits) (Quote, error) {
	if price.Int64() <= 0 {
		return Quote{}, ErrInvalidPrice
	}
	if len(sightings) == 0 {
		return Quote{}, ErrInsufficientSources
	}
	if acceptedAt.IsZero() {
		return Quote{}, ErrInvalidQuote
	}
	if ttl <= 0 || !freshness.Valid() {
		return Quote{}, ErrInvalidQuote
	}
	ordered := make([]Observation, 0, len(sightings))
	seen := map[SourceID]bool{}
	observed := time.Time{}
	for _, sighting := range sightings {
		if sighting.Source.String() == "" || sighting.Price.Int64() <= 0 || sighting.ObservedAt.IsZero() {
			return Quote{}, ErrInvalidObservation
		}
		if seen[sighting.Source] {
			return Quote{}, ErrDuplicateSource
		}
		seen[sighting.Source] = true
		if err := sighting.Fresh(acceptedAt, freshness); err != nil {
			return Quote{}, err
		}
		ordered = append(ordered, sighting)
		if observed.IsZero() || sighting.ObservedAt.After(observed) {
			observed = sighting.ObservedAt.UTC()
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Source < ordered[j].Source })
	accepted := acceptedAt.UTC()
	quote := Quote{
		Price:      price,
		Sightings:  ordered,
		ObservedAt: observed,
		AcceptedAt: accepted,
		ExpiresAt:  accepted.Add(ttl),
	}
	quote.Hash = sealQuote(quote)
	return quote, nil
}

// sealQuote binds price, ordered sources, sighting prices and
// instants: the canonical bytes any holder recomputes to detect
// tampering.
func sealQuote(quote Quote) string {
	legs := make([]string, 0, len(quote.Sightings))
	for _, sighting := range quote.Sightings {
		legs = append(legs, fmt.Sprintf("%s=%d", sighting.Source.String(), sighting.Price.Int64()))
	}
	canonical := fmt.Sprintf("%d\x00%s\x00%d\x00%d\x00%d",
		quote.Price.Int64(), strings.Join(legs, ","),
		quote.ObservedAt.UnixNano(), quote.AcceptedAt.UnixNano(), quote.ExpiresAt.UnixNano())
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// VerifyHash recomputes the seal and refuses a snapshot whose terms
// no longer agree: a checkpoint that fails here was edited or mixed
// up, and a withdrawn source never explains a new hash.
func (q Quote) VerifyHash() error {
	if q.Hash == "" || sealQuote(q) != q.Hash {
		return ErrInvalidQuote
	}
	return nil
}

// Live reports whether the snapshot prices at the given instant: the
// half-open window [accepted, expires). A tick before expiry prices,
// the expiry tick itself does not, and a late webhook judges the
// persisted expiry instead of extending it.
func (q Quote) Live(at time.Time) bool {
	instant := at.UTC()
	return !instant.Before(q.AcceptedAt) && instant.Before(q.ExpiresAt)
}

// SeasonalQuoteRequest carries one seasonal quotation attempt: the
// guarded price and sightings, the acceptance instant and lifetime
// with freshness windows, the purchase book with its exclusive end,
// and the reset acknowledgement shown before acceptance.
type SeasonalQuoteRequest struct {
	Price             PriceMinor
	Sightings         []Observation
	AcceptedAt        time.Time
	TTL               time.Duration
	Freshness         ObservationLimits
	Season            SeasonKey
	SeasonEndsAt      time.Time
	ResetAcknowledged bool
}

// AcceptSeasonalQuote seals one quotation in one purchase book: the
// reset disclosure is required outside compat-legacy, and the expiry
// is capped by the book end before sealing, so the settlement
// deadline never passes ends_at. The seal still covers price and the
// capped instants; the book travels beside it and is matched before
// any intent opens.
func AcceptSeasonalQuote(request SeasonalQuoteRequest) (Quote, error) {
	if err := RequireSeasonalReset(request.Season, request.ResetAcknowledged); err != nil {
		return Quote{}, err
	}
	quote, err := AcceptQuote(request.Price, request.Sightings, request.AcceptedAt, request.TTL, request.Freshness)
	if err != nil {
		return Quote{}, err
	}
	capped := CapExpiryBySeasonEnd(quote.AcceptedAt, quote.ExpiresAt, request.Season, request.SeasonEndsAt)
	if !capped.Equal(quote.ExpiresAt) {
		quote.ExpiresAt = capped
		quote.Hash = sealQuote(quote)
	}
	quote.Season = request.Season
	return quote, nil
}
