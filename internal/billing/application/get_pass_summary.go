package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// GetArenaPassSummaryUseCase answers the owner's private pass projection.
// Expiration is derived at the checked instant: expired lots appear in the
// breakdown flagged and are never part of the available total.
type GetArenaPassSummaryUseCase struct {
	lots  PassLotQueryRepository
	clock Clock
}

// NewGetArenaPassSummaryUseCase creates an instance of
// GetArenaPassSummaryUseCase.
func NewGetArenaPassSummaryUseCase(lots PassLotQueryRepository, clock Clock) *GetArenaPassSummaryUseCase {
	return &GetArenaPassSummaryUseCase{lots: lots, clock: clock}
}

// Execute returns the derived summary of the account.
func (uc *GetArenaPassSummaryUseCase) Execute(ctx context.Context, accountID domain.AccountID) (*ArenaPassSummary, error) {
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	checkedAt := uc.clock.Now()
	lots, err := uc.lots.ListAccountPassLots(ctx, accountID)
	if err != nil {
		return nil, err
	}

	summary := &ArenaPassSummary{
		AccountID: accountID,
		CheckedAt: checkedAt,
		Lots:      make([]ArenaPassLotSummary, 0, len(lots)),
	}
	for _, lot := range lots {
		expired := lot.IsExpired(checkedAt)
		summary.Lots = append(summary.Lots, ArenaPassLotSummary{Lot: lot, Expired: expired})
		if !expired {
			summary.AvailableTotal += int64(lot.Remaining())
		}
	}
	return summary, nil
}
