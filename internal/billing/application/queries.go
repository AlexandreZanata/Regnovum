package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// PassLotQueryRepository exposes the read projections of Arena Pass lots.
type PassLotQueryRepository interface {
	// ListAccountPassLots returns every lot of the account in canonical
	// order: nearest expiration first, lots that never expire last.
	ListAccountPassLots(ctx context.Context, accountID domain.AccountID) ([]domain.PassLot, error)

	// ListExpiredPassLots returns a bounded page of lots already expired at
	// the instant that still hold passes, ordered by expiration.
	ListExpiredPassLots(ctx context.Context, at time.Time, limit int) ([]domain.PassLot, error)

	// ListConsumptionsPage returns up to limit consumption entries strictly
	// older than the cursor position, newest first.
	ListConsumptionsPage(ctx context.Context, accountID domain.AccountID, after *ConsumptionPosition, limit int) ([]PassConsumptionRecord, error)
}

// History pagination bounds fixed by the API conventions (limit defaults to
// 20, maximum 100).
const (
	DefaultPassHistoryLimit = 20
	MaxPassHistoryLimit     = 100
)

// PassConsumptionRecord is one consumption of the owner history: the Arena
// that consumed the pass, the stable origin and reference of the lot and the
// instant. Internal lot identifiers and financial provider data never leave
// the adapter.
type PassConsumptionRecord struct {
	ConsumptionID string
	ArenaID       string
	Origin        domain.PassOrigin
	Reference     domain.Reference
	ConsumedAt    time.Time
}

// ConsumptionPosition is the decoded keyset cursor position: the last
// consumption entry already delivered to the caller.
type ConsumptionPosition struct {
	ConsumedAt    time.Time
	ConsumptionID string
}

// ArenaPassHistory is one page of the owner consumption history.
type ArenaPassHistory struct {
	Entries    []PassConsumptionRecord
	NextCursor string
}

// ArenaPassLotSummary is the private breakdown entry of one lot: expiration
// is derived at read time and expired lots are never counted as available.
type ArenaPassLotSummary struct {
	Lot     domain.PassLot
	Expired bool
}

// ArenaPassSummary is the owner's private pass projection: the total of
// passes available at the checked instant plus the per-lot breakdown.
type ArenaPassSummary struct {
	AccountID      domain.AccountID
	CheckedAt      time.Time
	AvailableTotal int64
	Lots           []ArenaPassLotSummary
}
