package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

// WithdrawArgumentCommand holds one authenticated withdrawal: the author
// retracts the argument from the display.
type WithdrawArgumentCommand struct {
	AccountID  string
	ArgumentID string
}

// WithdrawArgumentResult is the outcome: the stored argument (still holding
// its historical content) and whether the call resolved an earlier
// withdrawal instead of transitioning again.
type WithdrawArgumentResult struct {
	Argument PublishedArgument
	Replayed bool
}

// WithdrawArgumentUseCase retracts an argument from the public display
// without erasing the historical content and without any automatic refund.
// The action is owner-scoped, idempotent (repeats resolve the recorded
// withdrawal) and auditable: the withdrawal instant is recorded once and a
// moderation removal is never overridden. Replies and derived metrics stay
// coherent because nothing is deleted.
type WithdrawArgumentUseCase struct {
	arguments ArgumentRepository
	clock     Clock
}

// NewWithdrawArgumentUseCase creates an instance of WithdrawArgumentUseCase.
func NewWithdrawArgumentUseCase(arguments ArgumentRepository, clock Clock) *WithdrawArgumentUseCase {
	return &WithdrawArgumentUseCase{arguments: arguments, clock: clock}
}

// Execute withdraws the argument.
func (uc *WithdrawArgumentUseCase) Execute(ctx context.Context, cmd WithdrawArgumentCommand) (*WithdrawArgumentResult, error) {
	authorID, err := domain.ParseAccountID(cmd.AccountID)
	if err != nil {
		return nil, err
	}
	argumentID, err := domain.ParseArgumentID(cmd.ArgumentID)
	if err != nil {
		return nil, err
	}

	stored, err := uc.arguments.GetForAuthor(ctx, argumentID, authorID)
	if err != nil {
		return nil, err
	}
	switch stored.Status {
	case statusWithdrawn:
		return &WithdrawArgumentResult{Argument: *stored, Replayed: true}, nil
	case statusRemoved:
		// The moderation decision is not the author's to undo.
		return nil, ErrArgumentNotWithdrawable
	}

	updated, transitioned, err := uc.arguments.WithdrawArgument(ctx, argumentID, authorID, uc.clock.Now())
	if err != nil {
		return nil, err
	}
	if transitioned {
		return &WithdrawArgumentResult{Argument: *updated}, nil
	}

	// The status moved concurrently: re-read and resolve.
	stored, err = uc.arguments.GetForAuthor(ctx, argumentID, authorID)
	if err != nil {
		return nil, err
	}
	if stored.Status == statusWithdrawn {
		return &WithdrawArgumentResult{Argument: *stored, Replayed: true}, nil
	}
	return nil, ErrArgumentNotWithdrawable
}
