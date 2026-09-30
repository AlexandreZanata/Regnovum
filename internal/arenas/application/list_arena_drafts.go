package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// ListArenaDraftsUseCase answers the owner's draft list, newest first.
type ListArenaDraftsUseCase struct {
	arenas ArenaRepository
}

// NewListArenaDraftsUseCase creates an instance of ListArenaDraftsUseCase.
func NewListArenaDraftsUseCase(arenas ArenaRepository) *ListArenaDraftsUseCase {
	return &ListArenaDraftsUseCase{arenas: arenas}
}

// Execute returns the drafts owned by the creator.
func (uc *ListArenaDraftsUseCase) Execute(ctx context.Context, accountID string) ([]domain.Arena, error) {
	creatorID := domain.CreatorID(accountID)
	if creatorID.IsZero() {
		return nil, domain.ErrEmptyCreatorID
	}
	return uc.arenas.ListArenaDraftsForCreator(ctx, creatorID)
}
