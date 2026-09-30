package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// ListPositionChangesQuery addresses the private change history of one
// account in one Arena.
type ListPositionChangesQuery struct {
	AccountID string
	ArenaID   string
}

// ListPositionChangesUseCase answers the authenticated owner's change
// history, newest first. The history is private by default: only the account
// itself reads it.
type ListPositionChangesUseCase struct {
	positions PositionRepository
}

// NewListPositionChangesUseCase creates an instance of
// ListPositionChangesUseCase.
func NewListPositionChangesUseCase(positions PositionRepository) *ListPositionChangesUseCase {
	return &ListPositionChangesUseCase{positions: positions}
}

// Execute returns the stored history.
func (uc *ListPositionChangesUseCase) Execute(ctx context.Context, query ListPositionChangesQuery) ([]PositionChangeRecord, error) {
	accountID, err := domain.ParseAccountID(query.AccountID)
	if err != nil {
		return nil, err
	}
	arenaID, err := domain.ParseArenaID(query.ArenaID)
	if err != nil {
		return nil, err
	}
	return uc.positions.ListPositionChanges(ctx, arenaID, accountID)
}
