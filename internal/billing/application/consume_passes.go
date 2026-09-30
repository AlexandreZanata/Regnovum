package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// ConsumePassRequest is a validated pass consumption: one published Arena
// consumes exactly one pass of its creator.
type ConsumePassRequest struct {
	AccountID  domain.AccountID
	ArenaID    domain.ArenaID
	ConsumedAt time.Time
}

// ConsumePassResult is the outcome of a consumption: the lot that provided
// the pass (the original one on replays), its remaining count after the
// consumption, and whether it was a replay of an earlier attempt.
type ConsumePassResult struct {
	Lot       domain.PassLot
	Remaining int32
	Replayed  bool
}

// ArenaPassConsumer consumes Arena Passes atomically. The port is the
// boundary the arenas module will use for publication (P07-T05).
type ArenaPassConsumer interface {
	// ConsumeArenaPass locks the creator's consumable lots, consumes the one
	// with the nearest expiration (then a non-expiring one), records the
	// consumption under the Arena and updates the lot projection in a single
	// transaction. The same Arena resolves to the original consumption; an
	// expired lot is never a candidate.
	ConsumeArenaPass(ctx context.Context, request ConsumePassRequest) (*ConsumePassResult, error)
}

// ConsumeArenaPassCommand holds the parameters for consuming one Arena Pass.
type ConsumeArenaPassCommand struct {
	AccountID string
	ArenaID   string
}

// ConsumeArenaPassUseCase validates the consumption and delegates the atomic
// selection and write to the repository.
type ConsumeArenaPassUseCase struct {
	passes ArenaPassConsumer
	clock  Clock
}

// NewConsumeArenaPassUseCase creates an instance of
// ConsumeArenaPassUseCase.
func NewConsumeArenaPassUseCase(passes ArenaPassConsumer, clock Clock) *ConsumeArenaPassUseCase {
	return &ConsumeArenaPassUseCase{passes: passes, clock: clock}
}

// Execute validates the account and Arena and consumes one pass.
func (uc *ConsumeArenaPassUseCase) Execute(ctx context.Context, cmd ConsumeArenaPassCommand) (*ConsumePassResult, error) {
	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	arenaID, err := domain.ParseArenaID(cmd.ArenaID)
	if err != nil {
		return nil, err
	}

	return uc.passes.ConsumeArenaPass(ctx, ConsumePassRequest{
		AccountID:  accountID,
		ArenaID:    arenaID,
		ConsumedAt: uc.clock.Now(),
	})
}
