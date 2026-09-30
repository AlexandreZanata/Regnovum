package application

import (
	"context"
	"errors"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// ConfirmInitialPositionCommand holds the authenticated confirmation of the
// first position. The account is always an authenticated identity: the
// local visitor choice never reaches this use case.
type ConfirmInitialPositionCommand struct {
	AccountID string
	ArenaID   string
	Position  string
}

// ConfirmInitialPositionResult is the outcome: the stored projection and
// whether the call resolved an existing confirmation instead of writing.
type ConfirmInitialPositionResult struct {
	Position *domain.DebatePosition
	Replayed bool
}

// ConfirmInitialPositionUseCase records the first confirmed position of one
// account in one Arena. It is idempotent: retrying with the same position
// resolves the recorded projection, while a different position is refused
// because the initial choice is immutable history. A first confirmation
// requires an eligible account and a published, open Arena; a replay does
// not re-check eligibility, so a retry after the Arena closes still answers
// the recorded result.
type ConfirmInitialPositionUseCase struct {
	positions PositionRepository
	accounts  AccountEligibility
	arenas    ArenaEligibility
	clock     Clock
}

// NewConfirmInitialPositionUseCase creates an instance of
// ConfirmInitialPositionUseCase.
func NewConfirmInitialPositionUseCase(positions PositionRepository, accounts AccountEligibility, arenas ArenaEligibility, clock Clock) *ConfirmInitialPositionUseCase {
	return &ConfirmInitialPositionUseCase{
		positions: positions,
		accounts:  accounts,
		arenas:    arenas,
		clock:     clock,
	}
}

// Execute confirms the initial position.
func (uc *ConfirmInitialPositionUseCase) Execute(ctx context.Context, cmd ConfirmInitialPositionCommand) (*ConfirmInitialPositionResult, error) {
	arenaID, err := domain.ParseArenaID(cmd.ArenaID)
	if err != nil {
		return nil, err
	}
	accountID, err := domain.ParseAccountID(cmd.AccountID)
	if err != nil {
		return nil, err
	}
	position, err := domain.ParsePosition(cmd.Position)
	if err != nil {
		return nil, err
	}

	// Idempotency first: an existing projection is the recorded result, so
	// retries never need eligibility again.
	stored, err := uc.positions.GetByAccountAndArena(ctx, arenaID, accountID)
	if err == nil {
		return resolveExistingConfirmation(stored, position)
	}
	if !errors.Is(err, ErrPositionNotFound) {
		return nil, err
	}

	// Only a first confirmation needs an eligible account and an open
	// Arena. The account is checked first: it is the authenticated caller.
	if err := uc.accounts.EnsureEligible(ctx, accountID); err != nil {
		return nil, err
	}
	if err := uc.arenas.EnsureAcceptsPositions(ctx, arenaID); err != nil {
		return nil, err
	}

	created, inserted, err := uc.positions.ConfirmInitialPosition(ctx, arenaID, accountID, position, uc.clock.Now())
	if err != nil {
		return nil, err
	}
	if inserted {
		return &ConfirmInitialPositionResult{Position: created}, nil
	}

	// Lost the insert race: resolve against the projection that won.
	winner, err := uc.positions.GetByAccountAndArena(ctx, arenaID, accountID)
	if err != nil {
		return nil, err
	}
	return resolveExistingConfirmation(winner, position)
}

// resolveExistingConfirmation compares the requested position with the
// stored initial one: the same value is a replay, a different value is a
// conflict (changes are a separate, recorded transition).
func resolveExistingConfirmation(stored *domain.DebatePosition, requested domain.Position) (*ConfirmInitialPositionResult, error) {
	if stored.InitialPosition().Equals(requested) {
		return &ConfirmInitialPositionResult{Position: stored, Replayed: true}, nil
	}
	return nil, ErrInitialPositionAlreadySet
}
