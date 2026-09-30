package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// GetMyPositionQuery addresses the private position projection of one
// account in one Arena.
type GetMyPositionQuery struct {
	AccountID string
	ArenaID   string
}

// GetMyPositionUseCase answers the authenticated owner's position
// projection. Only the account that holds the position reaches it: probing
// another pair is indistinguishable from absence.
type GetMyPositionUseCase struct {
	positions PositionRepository
}

// NewGetMyPositionUseCase creates an instance of GetMyPositionUseCase.
func NewGetMyPositionUseCase(positions PositionRepository) *GetMyPositionUseCase {
	return &GetMyPositionUseCase{positions: positions}
}

// Execute returns the stored projection.
func (uc *GetMyPositionUseCase) Execute(ctx context.Context, query GetMyPositionQuery) (*domain.DebatePosition, error) {
	accountID, err := domain.ParseAccountID(query.AccountID)
	if err != nil {
		return nil, err
	}
	arenaID, err := domain.ParseArenaID(query.ArenaID)
	if err != nil {
		return nil, err
	}
	return uc.positions.GetByAccountAndArena(ctx, arenaID, accountID)
}
