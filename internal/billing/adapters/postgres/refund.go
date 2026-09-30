package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

var (
	_ application.RefundLedger    = (*Repository)(nil)
	_ application.RefundPassStore = (*Repository)(nil)
	_ application.RefundJournal   = (*Repository)(nil)
)

// PurchasedBalance returns the current PURCHASED_INK projection. The ledger
// stays the source of truth; the projection is what the runtime guards with
// CHECK (balance >= 0), so reading it is sufficient for the clawback cap.
func (r *Repository) PurchasedBalance(ctx context.Context, accountID domain.AccountID) (int64, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return 0, fmt.Errorf("refund purchased balance: %w", err)
	}
	row, err := r.queries.GetWalletAccount(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("refund purchased balance: %w", err)
	}
	return row.BalancePurchased, nil
}

// DebitPurchasedInk withdraws the exact amount from PURCHASED_INK with the
// refund-scoped idempotency key. The operation, the negative transaction and
// the balance projection change in a single transaction; a replay of the
// same key writes nothing and reports Replayed.
func (r *Repository) DebitPurchasedInk(ctx context.Context, request application.RefundDebitRequest) (*application.RefundDebitResult, error) {
	if request.Amount < 1 {
		return nil, fmt.Errorf("refund debit amount must be positive: %w", domain.ErrInvalidRefundAmount)
	}
	pgUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, fmt.Errorf("refund debit: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refund debit transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := r.queries.WithTx(tx)

	operationRow, err := qtx.CreateWalletOperationIfAbsent(ctx, platformpg.CreateWalletOperationIfAbsentParams{
		AccountID:      pgUUID,
		OperationType:  "debit_refund",
		IdempotencyKey: request.Idempotency.String(),
		Reference:      request.Reference.String(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Rollback(ctx); err != nil {
			return nil, fmt.Errorf("rollback refund replay: %w", err)
		}
		return &application.RefundDebitResult{Replayed: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create refund debit operation: %w", err)
	}

	if _, err := qtx.CreateWalletTransaction(ctx, platformpg.CreateWalletTransactionParams{
		OperationID: operationRow.ID,
		Bucket:      "PURCHASED_INK",
		Amount:      -request.Amount,
	}); err != nil {
		return nil, fmt.Errorf("create refund debit transaction: %w", err)
	}

	if _, err := qtx.CreditPurchasedBalance(ctx, platformpg.CreditPurchasedBalanceParams{
		AccountID:        pgUUID,
		BalancePurchased: -request.Amount,
	}); err != nil {
		return nil, fmt.Errorf("apply refund debit balance: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit refund debit transaction: %w", err)
	}
	return &application.RefundDebitResult{}, nil
}

// LotForRefund resolves the PURCHASE lot granted for the intent reference.
func (r *Repository) LotForRefund(ctx context.Context, accountID domain.AccountID, reference domain.Reference) (*application.RefundPassLot, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("refund pass lot: %w", err)
	}
	row, err := r.queries.GetArenaPassLotByGrant(ctx, platformpg.GetArenaPassLotByGrantParams{
		AccountID: pgUUID,
		Origin:    domain.OriginPurchase.String(),
		Reference: reference.String(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &application.RefundPassLot{Found: false}, nil
		}
		return nil, fmt.Errorf("refund pass lot: %w", err)
	}
	return &application.RefundPassLot{
		LotID:     uuidToString(row.ID),
		Quantity:  row.Quantity,
		Remaining: row.RemainingQuantity,
		Found:     true,
	}, nil
}

// RevokeRemaining zeroes the remaining projection of one lot. It is
// idempotent: revoking twice keeps zero.
func (r *Repository) RevokeRemaining(ctx context.Context, lotID string) (int32, error) {
	var id pgtype.UUID
	if err := id.Scan(lotID); err != nil {
		return 0, fmt.Errorf("refund revoke lot: %w", err)
	}
	before, err := r.queries.GetArenaPassLot(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("refund revoke lot: %w", err)
	}
	remaining := before.RemainingQuantity
	if remaining <= 0 {
		return 0, nil
	}
	if _, err := r.queries.ZeroPassLotRemaining(ctx, id); err != nil {
		return 0, fmt.Errorf("refund revoke lot: %w", err)
	}
	return remaining, nil
}

// GetRefundByProviderID resolves a recorded refund, nil when none exists.
func (r *Repository) GetRefundByProviderID(ctx context.Context, providerRefundID string) (*application.RefundRecord, error) {
	row, err := r.queries.GetBillingRefundByProviderID(ctx, providerRefundID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load refund: %w", err)
	}
	return mapRefundRow(row)
}

// RecordRefund inserts the outcome exactly once per provider object.
func (r *Repository) RecordRefund(ctx context.Context, request application.RecordRefundRequest) (*application.RefundRecord, bool, error) {
	intentUUID, err := uuidFromString(request.IntentID)
	if err != nil {
		return nil, false, fmt.Errorf("record refund: %w", err)
	}
	accountUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, false, fmt.Errorf("record refund: %w", err)
	}

	row, err := r.queries.InsertBillingRefundIfAbsent(ctx, platformpg.InsertBillingRefundIfAbsentParams{
		AccountID:           accountUUID,
		CheckoutIntentID:    intentUUID,
		ProviderRefundID:    request.ProviderRefund,
		Source:              request.Source.String(),
		Status:              request.Status.String(),
		ChargedAmountMinor:  request.ChargedMinor,
		RefundedAmountMinor: request.RefundedMinor,
		InkRevoked:          request.InkRevoked,
		PassesRevoked:       request.PassesRevoked,
		NeedsReview:         request.NeedsReview,
		ReviewReason:        textOrNull(request.ReviewReason),
	})
	if err == nil {
		record, mapErr := mapRefundRow(row)
		if mapErr != nil {
			return nil, false, mapErr
		}
		return record, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("record refund: %w", err)
	}

	stored, err := r.queries.GetBillingRefundByProviderID(ctx, request.ProviderRefund)
	if err != nil {
		return nil, false, fmt.Errorf("load replayed refund: %w", err)
	}
	record, err := mapRefundRow(stored)
	if err != nil {
		return nil, false, err
	}
	return record, true, nil
}

// ListRefundsByIntent returns the audit trail of one intent, oldest first.
func (r *Repository) ListRefundsByIntent(ctx context.Context, intentID string) ([]application.RefundRecord, error) {
	intentUUID, err := uuidFromString(intentID)
	if err != nil {
		return nil, fmt.Errorf("list refunds: %w", err)
	}
	rows, err := r.queries.ListBillingRefundsByIntent(ctx, intentUUID)
	if err != nil {
		return nil, fmt.Errorf("list refunds: %w", err)
	}
	records := make([]application.RefundRecord, 0, len(rows))
	for _, row := range rows {
		record, err := mapRefundRow(row)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, nil
}

func mapRefundRow(row platformpg.AppBillingRefund) (*application.RefundRecord, error) {
	source, err := domain.ParseRefundSource(row.Source)
	if err != nil {
		return nil, fmt.Errorf("stored refund source is invalid: %w", err)
	}
	status, err := domain.ParseRefundStatus(row.Status)
	if err != nil {
		return nil, fmt.Errorf("stored refund status is invalid: %w", err)
	}
	reviewReason := ""
	if row.ReviewReason.Valid {
		reviewReason = row.ReviewReason.String
	}
	return &application.RefundRecord{
		ID:             uuidToString(row.ID),
		IntentID:       uuidToString(row.CheckoutIntentID),
		AccountID:      domain.AccountID(uuidToString(row.AccountID)),
		ProviderRefund: row.ProviderRefundID,
		Source:         source,
		Status:         status,
		ChargedMinor:   row.ChargedAmountMinor,
		RefundedMinor:  row.RefundedAmountMinor,
		InkRevoked:     row.InkRevoked,
		PassesRevoked:  row.PassesRevoked,
		NeedsReview:    row.NeedsReview,
		ReviewReason:   reviewReason,
	}, nil
}

func uuidFromString(raw string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid uuid format: %w", err)
	}
	return id, nil
}

func textOrNull(raw string) pgtype.Text {
	if raw == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: raw, Valid: true}
}
