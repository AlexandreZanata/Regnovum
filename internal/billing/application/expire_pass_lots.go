package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// DefaultExpiredPassLotsPageSize bounds one sweep page of the expiry job.
const DefaultExpiredPassLotsPageSize = 500

// ExpireArenaPassLotsResult is the derived report of the expiry sweep:
// every expired lot that still holds passes at the checked instant, and how
// many passes were lost to expiration.
type ExpireArenaPassLotsResult struct {
	CheckedAt     time.Time
	ExpiredLots   []domain.PassLot
	ExpiredPasses int64
}

// ExpireArenaPassLotsUseCase runs the idempotent expiration sweep. Lots are
// never deleted or mutated: expiration is derived from expires_at at the
// checked instant, so repeated runs over unchanged data produce the same
// report and no writes. The report is the input for operational metrics.
type ExpireArenaPassLotsUseCase struct {
	lots     PassLotQueryRepository
	clock    Clock
	pageSize int
}

// NewExpireArenaPassLotsUseCase creates an instance of
// ExpireArenaPassLotsUseCase with the standard page size.
func NewExpireArenaPassLotsUseCase(lots PassLotQueryRepository, clock Clock) *ExpireArenaPassLotsUseCase {
	return &ExpireArenaPassLotsUseCase{lots: lots, clock: clock, pageSize: DefaultExpiredPassLotsPageSize}
}

// Execute derives the expired-lot report at the current instant.
func (uc *ExpireArenaPassLotsUseCase) Execute(ctx context.Context) (*ExpireArenaPassLotsResult, error) {
	checkedAt := uc.clock.Now()
	lots, err := uc.lots.ListExpiredPassLots(ctx, checkedAt, uc.pageSize)
	if err != nil {
		return nil, err
	}

	result := &ExpireArenaPassLotsResult{CheckedAt: checkedAt, ExpiredLots: lots}
	for _, lot := range lots {
		result.ExpiredPasses += int64(lot.Remaining())
	}
	return result, nil
}
