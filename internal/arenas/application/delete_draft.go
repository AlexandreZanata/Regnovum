package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// DeleteArenaDraftCommand holds the parameters for discarding a draft.
type DeleteArenaDraftCommand struct {
	AccountID string
	ArenaID   string
}

// DeleteArenaDraftUseCase discards a private draft. Drafts are the only
// deletable Arena state; published content is retained (P08-T01 trigger).
type DeleteArenaDraftUseCase struct {
	arenas ArenaRepository
}

// NewDeleteArenaDraftUseCase creates an instance of
// DeleteArenaDraftUseCase.
func NewDeleteArenaDraftUseCase(arenas ArenaRepository) *DeleteArenaDraftUseCase {
	return &DeleteArenaDraftUseCase{arenas: arenas}
}

// Execute deletes the draft owned by the creator.
func (uc *DeleteArenaDraftUseCase) Execute(ctx context.Context, cmd DeleteArenaDraftCommand) error {
	creatorID := domain.CreatorID(cmd.AccountID)
	if creatorID.IsZero() {
		return domain.ErrEmptyCreatorID
	}
	arenaID := domain.ArenaID(cmd.ArenaID)
	if arenaID.IsZero() {
		return domain.ErrEmptyArenaID
	}

	return uc.arenas.DeleteArenaDraft(ctx, arenaID, creatorID)
}
