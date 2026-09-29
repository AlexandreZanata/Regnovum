package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// AllocateReserveCommand funds the Sovereign Reserve from existing
// Treasury stock: the allocation act and its exact amount. Amounts are
// always explicit in milliINK — Q23 stays PENDENTE, so no percentage,
// share or default amount lives here or anywhere else: an act the
// titular never ratified cannot be inferred from a constant.
type AllocateReserveCommand struct {
	ActID  string
	Millis int64
}

// AllocateReserveRequest is a validated allocation for the repository
// port.
type AllocateReserveRequest struct {
	Act    domain.IntentionKey
	Amount domain.MilliInk
}

// AllocateReserveResult is the settled allocation: the Treasury
// transfer that funded the reserve and the allocated amount, with
// replay marking the retries that resolved the original settlement.
type AllocateReserveResult struct {
	TransferID string
	Allocated  domain.MilliInk
	Replayed   bool
}

// ReserveRepository settles sovereign allocations across the Treasury
// in one transaction: the Genesis home debits while the reserve vault
// credits, or nothing moves at all. Obligations and escrows are never
// debited: only the Genesis home funds the reserve.
type ReserveRepository interface {
	// AllocateReserve settles one allocation act, keyed idempotently by
	// the act itself. Replays resolve the original settlement
	// untouched; divergent amounts under one act conflict instead of
	// rewriting it, so later acts stay prospective.
	AllocateReserve(ctx context.Context, request AllocateReserveRequest) (*AllocateReserveResult, error)
}

// AllocateReserveUseCase settles one explicit sovereign allocation
// without creating value. It is an internal operation: no public
// surface calls it.
type AllocateReserveUseCase struct {
	reserves ReserveRepository
}

// NewAllocateReserveUseCase creates an instance of AllocateReserveUseCase.
func NewAllocateReserveUseCase(reserves ReserveRepository) *AllocateReserveUseCase {
	return &AllocateReserveUseCase{reserves: reserves}
}

// Execute validates the act and settles it from existing stock.
func (uc *AllocateReserveUseCase) Execute(ctx context.Context, cmd AllocateReserveCommand) (*AllocateReserveResult, error) {
	act, err := domain.ParseIntentionKey(cmd.ActID)
	if err != nil {
		return nil, err
	}
	amount, err := domain.NewMilliInk(cmd.Millis)
	if err != nil {
		return nil, err
	}
	if amount.IsZero() {
		return nil, domain.ErrInvalidGrant
	}
	return uc.reserves.AllocateReserve(ctx, AllocateReserveRequest{Act: act, Amount: amount})
}
