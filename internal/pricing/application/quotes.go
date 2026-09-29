package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// Clock exposes wall-clock time to pricing use cases, keeping
// acceptance decisions deterministic under test. Production wires the
// real clock; tests inject a fixed instant.
type Clock interface {
	Now() time.Time
}

// SightingInput carries one judged sighting into acceptance: its
// source slug, integer price, observed instant and the raw payload
// the provenance hash binds.
type SightingInput struct {
	Source     string
	PriceMinor int64
	ObservedAt time.Time
	Payload    []byte
}

// AcceptQuoteCommand seals one purchase quotation: the guarded median
// price, the judged sightings, and the approved lifetime with its
// freshness windows. The lifetime counts from acceptance: a later
// webhook judges the persisted expiry, never a recomputed one.
type AcceptQuoteCommand struct {
	PriceMinor int64
	Sightings  []SightingInput
	TTL        time.Duration
	MaxSkew    time.Duration
	MaxAge     time.Duration
}

// QuoteRepository persists accepted quotation snapshots without ever
// rewriting them. Every write carries the sealed terms with their
// sightings; reads serve the frozen source set even after a source
// withdraws.
type QuoteRepository interface {
	// Store persists one sealed snapshot with its sightings in a
	// single transaction, returning the stored identity.
	Store(ctx context.Context, quote domain.Quote) (string, error)
	// Find resolves one stored snapshot with its sightings, or
	// reports its absence.
	Find(ctx context.Context, id string) (*domain.Quote, error)
}

// AcceptQuoteUseCase seals and stores one purchase quotation. It is
// an internal operation: no public surface calls it.
type AcceptQuoteUseCase struct {
	quotes QuoteRepository
	clock  Clock
}

// NewAcceptQuoteUseCase creates an instance of AcceptQuoteUseCase,
// refusing incomplete composition.
func NewAcceptQuoteUseCase(quotes QuoteRepository, clock Clock) (*AcceptQuoteUseCase, error) {
	if quotes == nil || clock == nil {
		return nil, domain.ErrInvalidQuote
	}
	return &AcceptQuoteUseCase{quotes: quotes, clock: clock}, nil
}

// Execute validates the terms, seals the snapshot at the injected
// acceptance instant and stores it once. Malformed terms stop before
// any write; storage failures propagate without a sealed copy.
func (uc *AcceptQuoteUseCase) Execute(ctx context.Context, cmd AcceptQuoteCommand) (domain.Quote, error) {
	price, err := domain.NewPriceMinor(cmd.PriceMinor)
	if err != nil {
		return domain.Quote{}, err
	}
	sightings, err := observationsFromInputs(cmd.Sightings)
	if err != nil {
		return domain.Quote{}, err
	}
	accepted := uc.clock.Now().UTC()
	quote, err := domain.AcceptQuote(price, sightings, accepted, cmd.TTL,
		domain.ObservationLimits{MaxFutureSkew: cmd.MaxSkew, MaxAge: cmd.MaxAge})
	if err != nil {
		return domain.Quote{}, err
	}
	id, err := uc.quotes.Store(ctx, quote)
	if err != nil {
		return domain.Quote{}, err
	}
	if id == "" {
		return domain.Quote{}, domain.ErrInvalidQuote
	}
	quote.ID = id
	return quote, nil
}
