package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// GetArenaDraftUseCase answers the owner's private draft read. Only the
// creator reaches the draft at all.
type GetArenaDraftUseCase struct {
	arenas ArenaRepository
}

// NewGetArenaDraftUseCase creates an instance of GetArenaDraftUseCase.
func NewGetArenaDraftUseCase(arenas ArenaRepository) *GetArenaDraftUseCase {
	return &GetArenaDraftUseCase{arenas: arenas}
}

// Execute returns the draft owned by the creator.
func (uc *GetArenaDraftUseCase) Execute(ctx context.Context, accountID string, arenaID domain.ArenaID) (*domain.Arena, error) {
	creatorID := domain.CreatorID(accountID)
	if creatorID.IsZero() {
		return nil, domain.ErrEmptyCreatorID
	}
	if arenaID.IsZero() {
		return nil, domain.ErrEmptyArenaID
	}
	return uc.arenas.GetArenaForCreator(ctx, arenaID, creatorID)
}
