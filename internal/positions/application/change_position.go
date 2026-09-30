package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// ChangePositionCommand holds the authenticated change of position. The
// account is always an authenticated identity: the local visitor choice
// never reaches this use case.
type ChangePositionCommand struct {
	AccountID string
	ArenaID   string
	Position  string
}

// ChangePositionResult is the outcome: the stored change identifier, kept
// for later attribution, and the updated projection.
type ChangePositionResult struct {
	ChangeID string
	Position *domain.DebatePosition
}

// ChangePositionUseCase records one accepted position change. The change is
// appended to the immutable history and the current-position projection
// moves in the same transaction, under the optimistic version check: a
// concurrent change either advances the chain or fails with
// ErrVersionConflict, never leaving the projection diverging from the
// chain. Only the holder of a confirmed position changes it, and only a
// published, open Arena accepts changes (BUSINESS_RULES §3.3).
type ChangePositionUseCase struct {
	positions PositionRepository
	arenas    ArenaEligibility
	uow       UnitOfWork
	clock     Clock
}

// NewChangePositionUseCase creates an instance of ChangePositionUseCase.
func NewChangePositionUseCase(positions PositionRepository, arenas ArenaEligibility, uow UnitOfWork, clock Clock) *ChangePositionUseCase {
	return &ChangePositionUseCase{
		positions: positions,
		arenas:    arenas,
		uow:       uow,
		clock:     clock,
	}
}

// Execute records the change.
func (uc *ChangePositionUseCase) Execute(ctx context.Context, cmd ChangePositionCommand) (*ChangePositionResult, error) {
	arenaID, err := domain.ParseArenaID(cmd.ArenaID)
	if err != nil {
		return nil, err
	}
	accountID, err := domain.ParseAccountID(cmd.AccountID)
	if err != nil {
		return nil, err
	}
	next, err := domain.ParsePosition(cmd.Position)
	if err != nil {
		return nil, err
	}

	// Ownership: only the holder of a confirmed position changes it.
	projection, err := uc.positions.GetByAccountAndArena(ctx, arenaID, accountID)
	if err != nil {
		return nil, err
	}

	// Closed Arenas accept no new changes (BUSINESS_RULES §3.3).
	if err := uc.arenas.EnsureAcceptsPositions(ctx, arenaID); err != nil {
		return nil, err
	}

	// Domain transition first: same-position targets and invalid instants
	// never open a transaction.
	expectedVersion := projection.Version()
	change, err := projection.ChangeTo(next, uc.clock.Now())
	if err != nil {
		return nil, err
	}

	changeID := ""
	err = uc.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		id, err := uc.positions.CreatePositionChange(txCtx, change)
		if err != nil {
			return err
		}
		changeID = id
		return uc.positions.UpdateCurrentPosition(txCtx, change, expectedVersion)
	})
	if err != nil {
		return nil, err
	}

	return &ChangePositionResult{ChangeID: changeID, Position: projection}, nil
}
