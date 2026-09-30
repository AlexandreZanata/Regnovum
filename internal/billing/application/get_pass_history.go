package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// GetArenaPassHistoryUseCase answers the owner's paginated consumption
// history. Pages are keyset-based: the cursor only moves backward from the
// last delivered entry, so entries can never be duplicated or skipped, and
// the account filter is applied on every page independently of the cursor.
type GetArenaPassHistoryUseCase struct {
	lots    PassLotQueryRepository
	cursors *HistoryCursorCodec
}

// NewGetArenaPassHistoryUseCase creates an instance of
// GetArenaPassHistoryUseCase with signed cursors.
func NewGetArenaPassHistoryUseCase(lots PassLotQueryRepository, cursors *HistoryCursorCodec) *GetArenaPassHistoryUseCase {
	return &GetArenaPassHistoryUseCase{lots: lots, cursors: cursors}
}

// Execute returns one page of the history plus the cursor of the next page
// (empty when the page is the last one). Limits are clamped to the contract
// bounds: default 20, maximum 100.
func (uc *GetArenaPassHistoryUseCase) Execute(ctx context.Context, accountID domain.AccountID, cursor string, limit int) (*ArenaPassHistory, error) {
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	after, err := uc.cursors.Decode(cursor)
	if err != nil {
		return nil, err
	}

	switch {
	case limit <= 0:
		limit = DefaultPassHistoryLimit
	case limit > MaxPassHistoryLimit:
		limit = MaxPassHistoryLimit
	}

	entries, err := uc.lots.ListConsumptionsPage(ctx, accountID, after, limit+1)
	if err != nil {
		return nil, err
	}

	history := &ArenaPassHistory{Entries: entries}
	if len(entries) > limit {
		history.Entries = entries[:limit]
		history.NextCursor = uc.cursors.Encode(history.Entries[limit-1])
	}
	return history, nil
}
