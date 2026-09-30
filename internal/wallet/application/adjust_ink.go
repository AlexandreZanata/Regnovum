package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// AdjustInkCommand holds the parameters of a restricted administrative
// adjustment. OperationType accepts only credit_admin (positive adjustment,
// Bucket is the destination) and debit_admin (negative adjustment, the
// consumption follows the FREE-first priority and Bucket is ignored).
type AdjustInkCommand struct {
	ActorAccountID string
	AccountID      string
	Bucket         string
	OperationType  string
	Amount         int64
	Reference      string
	Reason         string
	IdempotencyKey string
}

// AdjustInkUseCase applies an audited administrative adjustment. It is the
// only path that may write credit_admin/debit_admin operations: the general
// credit and debit use cases reject administrative types.
type AdjustInkUseCase struct {
	credits    CreditRepository
	debits     DebitRepository
	authorizer AdministratorAuthorizer
	audit      AdminAuditRecorder
	clock      Clock
}

// NewAdjustInkUseCase creates an instance of AdjustInkUseCase.
func NewAdjustInkUseCase(
	credits CreditRepository,
	debits DebitRepository,
	authorizer AdministratorAuthorizer,
	audit AdminAuditRecorder,
	clock Clock,
) *AdjustInkUseCase {
	return &AdjustInkUseCase{
		credits:    credits,
		debits:     debits,
		authorizer: authorizer,
		audit:      audit,
		clock:      clock,
	}
}

// Execute validates the adjustment, asserts administrator authorization,
// applies it to the ledger and records the audit event.
func (uc *AdjustInkUseCase) Execute(ctx context.Context, cmd AdjustInkCommand) (*AdminAdjustmentResult, error) {
	actor := domain.AccountID(cmd.ActorAccountID)
	if actor.IsZero() {
		return nil, domain.ErrActorRequired
	}

	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	operationType, err := domain.ParseOperationType(cmd.OperationType)
	if err != nil {
		return nil, err
	}
	if !operationType.IsAdmin() {
		return nil, domain.ErrNotAdminAdjustment
	}

	reason, err := domain.ParseReason(cmd.Reason)
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

	amount, err := domain.NewInk(cmd.Amount)
	if err != nil {
		return nil, err
	}
	if amount.IsZero() {
		return nil, domain.ErrZeroAmount
	}

	bucket := domain.Bucket("")
	if operationType.IsCredit() {
		bucket, err = domain.ParseBucket(cmd.Bucket)
		if err != nil {
			return nil, err
		}
	}

	if err := uc.authorizer.EnsureAdministrator(ctx, actor); err != nil {
		return nil, err
	}

	now := uc.clock.Now()
	result, err := uc.apply(ctx, accountID, actor, operationType, amount, reason, reference, idempotencyKey, bucket, now)
	if err != nil {
		return nil, err
	}

	// The audit event is recorded for both fresh and replayed outcomes: the
	// recorder deduplicates by idempotency key, so a retry repairs a lost
	// record without duplicating the adjustment.
	event := AdminAdjustmentEvent{
		ActorAccountID: actor,
		AccountID:      accountID,
		OperationID:    result.Operation.ID().String(),
		OperationType:  operationType,
		Allocation:     result.Allocation,
		Reason:         reason,
		Reference:      reference,
		IdempotencyKey: idempotencyKey,
		Replayed:       result.Replayed,
		OccurredAt:     now,
	}
	if err := uc.audit.RecordAdminAdjustment(ctx, event); err != nil {
		return nil, err
	}

	return result, nil
}

// apply routes the adjustment to the credit or debit ledger path with the
// administrative audit fields attached.
func (uc *AdjustInkUseCase) apply(
	ctx context.Context,
	accountID, actor domain.AccountID,
	operationType domain.OperationType,
	amount domain.Ink,
	reason domain.Reason,
	reference domain.Reference,
	idempotencyKey domain.IdempotencyKey,
	bucket domain.Bucket,
	now time.Time,
) (*AdminAdjustmentResult, error) {
	if operationType.IsCredit() {
		result, err := uc.credits.ApplyCredit(ctx, CreditRequest{
			AccountID:      accountID,
			Bucket:         bucket,
			OperationType:  operationType,
			IdempotencyKey: idempotencyKey,
			Reference:      reference,
			Reason:         reason,
			ActorAccountID: actor,
			Delta:          amount.Int64(),
			ChangedAt:      now,
		})
		if err != nil {
			return nil, err
		}

		allocation := domain.NewAllocation(amount, domain.Ink{})
		if bucket == domain.BucketPurchased {
			allocation = domain.NewAllocation(domain.Ink{}, amount)
		}
		return &AdminAdjustmentResult{
			Operation:  result.Operation,
			Allocation: allocation,
			Replayed:   result.Replayed,
		}, nil
	}

	result, err := uc.debits.ApplyDebit(ctx, DebitRequest{
		AccountID:      accountID,
		OperationType:  operationType,
		IdempotencyKey: idempotencyKey,
		Reference:      reference,
		Reason:         reason,
		ActorAccountID: actor,
		Amount:         amount,
		ChangedAt:      now,
	})
	if err != nil {
		return nil, err
	}
	return &AdminAdjustmentResult{
		Operation:  result.Operation,
		Allocation: result.Allocation,
		Replayed:   result.Replayed,
	}, nil
}
