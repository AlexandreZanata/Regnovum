package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// DebitInkCommand holds the parameters for debiting INK by bucket priority.
// The debit kinds are expressed through OperationType: debit_argument and
// debit_admin. The bucket split is not a parameter — the plan always drains
// FREE_INK before PURCHASED_INK.
type DebitInkCommand struct {
	AccountID      string
	OperationType  string
	Amount         int64
	Reference      string
	IdempotencyKey string
}

// DebitInkUseCase validates and persists an atomic INK debit. The same
// idempotency key returns the original operation without debiting twice.
type DebitInkUseCase struct {
	debits DebitRepository
	clock  Clock
}

// NewDebitInkUseCase creates an instance of DebitInkUseCase.
func NewDebitInkUseCase(debits DebitRepository, clock Clock) *DebitInkUseCase {
	return &DebitInkUseCase{debits: debits, clock: clock}
}

// Execute validates the debit and delegates the locked allocation to the
// repository.
func (uc *DebitInkUseCase) Execute(ctx context.Context, cmd DebitInkCommand) (*DebitResult, error) {
	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	operationType, err := domain.ParseOperationType(cmd.OperationType)
	if err != nil {
		return nil, err
	}
	if !operationType.IsDebit() {
		return nil, domain.ErrNotADebit
	}
	if operationType.IsAdmin() {
		// Administrative debits only enter through the restricted, audited
		// adjustment path (P06-T07).
		return nil, domain.ErrAdminOpsRestricted
	}

	amount, err := domain.NewInk(cmd.Amount)
	if err != nil {
		return nil, err
	}
	if amount.IsZero() {
		return nil, domain.ErrZeroAmount
	}

	reference, err := domain.ParseReference(cmd.Reference)
	if err != nil {
		return nil, err
	}

	idempotencyKey, err := domain.ParseIdempotencyKey(cmd.IdempotencyKey)
	if err != nil {
		return nil, err
	}

	return uc.debits.ApplyDebit(ctx, DebitRequest{
		AccountID:      accountID,
		OperationType:  operationType,
		IdempotencyKey: idempotencyKey,
		Reference:      reference,
		Amount:         amount,
		ChangedAt:      uc.clock.Now(),
	})
}
