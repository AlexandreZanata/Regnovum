package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// CreditInkCommand holds the parameters for crediting INK to a bucket. The
// five credit kinds of P06-T03 are expressed through OperationType:
// credit_free, credit_member, credit_purchase, credit_refund and
// credit_admin.
type CreditInkCommand struct {
	AccountID      string
	Bucket         string
	OperationType  string
	Amount         int64
	Reference      string
	IdempotencyKey string
}

// CreditInkUseCase validates and persists an idempotent INK credit. The
// same idempotency key returns the original operation without duplicating
// the transaction or the balance change.
type CreditInkUseCase struct {
	credits CreditRepository
	clock   Clock
}

// NewCreditInkUseCase creates an instance of CreditInkUseCase.
func NewCreditInkUseCase(credits CreditRepository, clock Clock) *CreditInkUseCase {
	return &CreditInkUseCase{credits: credits, clock: clock}
}

// Execute validates the credit, derives the signed ledger delta from the
// operation direction and applies it atomically.
func (uc *CreditInkUseCase) Execute(ctx context.Context, cmd CreditInkCommand) (*CreditResult, error) {
	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	bucket, err := domain.ParseBucket(cmd.Bucket)
	if err != nil {
		return nil, err
	}

	operationType, err := domain.ParseOperationType(cmd.OperationType)
	if err != nil {
		return nil, err
	}
	if !operationType.IsCredit() {
		return nil, domain.ErrNotACredit
	}
	if operationType.IsAdmin() {
		// Administrative credits only enter through the restricted, audited
		// adjustment path (P06-T07).
		return nil, domain.ErrAdminOpsRestricted
	}

	amount, err := domain.NewInk(cmd.Amount)
	if err != nil {
		return nil, err
	}

	reference, err := domain.ParseReference(cmd.Reference)
	if err != nil {
		return nil, err
	}

	idempotencyKey, err := domain.ParseIdempotencyKey(cmd.IdempotencyKey)
	if err != nil {
		return nil, err
	}

	direction, err := operationType.Direction()
	if err != nil {
		return nil, err
	}
	delta, err := direction.Apply(amount)
	if err != nil {
		return nil, err
	}

	return uc.credits.ApplyCredit(ctx, CreditRequest{
		AccountID:      accountID,
		Bucket:         bucket,
		OperationType:  operationType,
		IdempotencyKey: idempotencyKey,
		Reference:      reference,
		Delta:          delta,
		ChangedAt:      uc.clock.Now(),
	})
}
