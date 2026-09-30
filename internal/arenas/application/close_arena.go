package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// CloseArenaCommand holds the parameters for closing a published Arena.
type CloseArenaCommand struct {
	AccountID string
	ArenaID   string
}

// CloseArenaResult is the outcome of a closing: the closed Arena and whether
// the call resolved an already closed Arena instead of closing again.
type CloseArenaResult struct {
	Arena    domain.Arena
	Replayed bool
}

// CloseArenaUseCase closes an Arena according to the creator rule: only the
// creator can close, only a published Arena can be closed and reopening does
// not exist in the MVP.
type CloseArenaUseCase struct {
	arenas ArenaRepository
}

// NewCloseArenaUseCase creates an instance of CloseArenaUseCase.
func NewCloseArenaUseCase(arenas ArenaRepository) *CloseArenaUseCase {
	return &CloseArenaUseCase{arenas: arenas}
}

// Execute closes the Arena owned by the creator.
func (uc *CloseArenaUseCase) Execute(ctx context.Context, cmd CloseArenaCommand) (*CloseArenaResult, error) {
	creatorID := domain.CreatorID(cmd.AccountID)
	if creatorID.IsZero() {
		return nil, domain.ErrEmptyCreatorID
	}
	arenaID := domain.ArenaID(cmd.ArenaID)
	if arenaID.IsZero() {
		return nil, domain.ErrEmptyArenaID
	}

	arena, err := uc.arenas.GetArenaForCreator(ctx, arenaID, creatorID)
	if err != nil {
		return nil, err
	}

	switch arena.Status() {
	case domain.ArenaStatusPublished:
		expectedVersion := arena.Version()
		if err := arena.Close(); err != nil {
			return nil, err
		}
		closed, err := uc.arenas.CloseArena(ctx, arenaID, creatorID, expectedVersion)
		if err != nil {
			return nil, err
		}
		return &CloseArenaResult{Arena: *closed}, nil
	case domain.ArenaStatusClosed:
		// Idempotent retry: the Arena is already closed.
		return &CloseArenaResult{Arena: *arena, Replayed: true}, nil
	default:
		return nil, domain.ErrInvalidStatusChange
	}
}
