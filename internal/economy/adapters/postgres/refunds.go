package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.RefusalRefundRepository = (*Repository)(nil)

// refusalKeyPrefix anchors the legacy idempotency key for one fiat
// correlation object: the same provider refund never settles twice.
const refusalKeyPrefix = "refusal-refund-"

// refundKey names the idempotency key for one provider object.
func refundKey(providerID string) string {
	return refusalKeyPrefix + providerID
}

// refundReference binds the settled outcome to its inputs: charter,
// provider object, fiat amounts and source. A retry recomputes it
// identically; anything else is a different refund and never a replay.
func refundReference(charter domain.CharterVersion, providerID string, paid, refunded int64, source domain.RefundSource) string {
	return fmt.Sprintf("refusal:%s:%s:paid=%d:refunded=%d:source=%s", charter.String(), providerID, paid, refunded, source.String())
}

// SettleRefusalRefund revokes the unused purchased remainder of a refused
// holder in the legacy books only. The economy journal never moves, so no
// silent conversion and no new Genesis INK can leave this path: every
// revoked unit already existed as purchased credit, and stockout of the
// remainder becomes review instead of mint. A unique collision retries
// once through re-lookup, so concurrent runs of one provider object
// resolve the single settlement instead of duplicating it.
func (r *Repository) SettleRefusalRefund(ctx context.Context, request application.RefusalRefundRequest) (*application.RefusalRefundResult, error) {
	if replayed, err := r.lookupRefusalRefund(ctx, request); err != nil || replayed != nil {
		return replayed, err
	}
	var last error
	for range 2 {
		result, err := r.createRefusalRefund(ctx, request)
		if err == nil {
			return result, nil
		}
		if !isRefusalConflict(err) {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupRefusalRefund(ctx, request); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("refusal refund unsettled after conflict: %w", last)
}

// lookupRefusalRefund resolves a settled refusal exit without writing: the
// post-commit retry path that makes crash recovery exactly-once. The
// stored reference must match the request verbatim, and the stored row
// must belong to the caller, so a divergent reuse or a third party never
// replays as the holder.
func (r *Repository) lookupRefusalRefund(ctx context.Context, request application.RefusalRefundRequest) (*application.RefusalRefundResult, error) {
	var storedAccount, storedReference string
	err := r.pool.QueryRow(ctx,
		`SELECT account_id::text, reference FROM app.wallet_operations WHERE idempotency_key = $1`,
		refundKey(request.ProviderID)).Scan(&storedAccount, &storedReference)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup refusal refund: %w", err)
	}
	if storedAccount != request.AccountID {
		return nil, domain.ErrIntentionConflict
	}
	if storedReference != refundReference(request.Charter, request.ProviderID, request.PaidMinor, request.Refunded, request.Source) {
		return nil, domain.ErrIntentionConflict
	}
	var revoked int64
	err = r.pool.QueryRow(ctx,
		`SELECT COALESCE(-SUM(t.amount), 0) FROM app.wallet_transactions t
		 JOIN app.wallet_operations o ON o.id = t.operation_id
		 WHERE o.idempotency_key = $1 AND t.bucket = 'PURCHASED_INK'`,
		refundKey(request.ProviderID)).Scan(&revoked)
	if err != nil {
		return nil, fmt.Errorf("read settled revocation: %w", err)
	}
	granted, current, err := r.readPurchasedPosition(ctx, request.AccountID)
	if err != nil {
		return nil, err
	}
	pre := current + revoked
	_, needsReview, err := domain.AssessRefusalRefund(granted, pre, request.PaidMinor, request.Refunded, request.Source)
	if err != nil {
		return nil, fmt.Errorf("stored refund holds incoherent balances: %w", err)
	}
	decidedAt, err := r.readRefusalDecidedAt(ctx, request.AccountID, request.Charter)
	if err != nil {
		return nil, err
	}
	return &application.RefusalRefundResult{
		RevokeUnits:  revoked,
		FiatMinor:    request.Refunded,
		NeedsReview:  needsReview,
		ReviewReason: refusalReviewReason(request.Source, revoked, granted, pre, request.PaidMinor, request.Refunded),
		Replayed:     true,
		RespondBy:    domain.RefusalResponseDue(decidedAt),
	}, nil
}

// readPurchasedPosition resolves the purchased credit ever granted and the
// balance still held, without locking: absence of a wallet row means no
// purchased right at all.
func (r *Repository) readPurchasedPosition(ctx context.Context, accountID string) (granted, remaining int64, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(t.amount), 0) FROM app.wallet_transactions t
		 JOIN app.wallet_operations o ON o.id = t.operation_id
		 WHERE o.account_id = $1::uuid AND t.bucket = 'PURCHASED_INK' AND t.amount > 0`,
		accountID).Scan(&granted)
	if err != nil {
		return 0, 0, fmt.Errorf("sum purchased grants: %w", err)
	}
	err = r.pool.QueryRow(ctx,
		`SELECT COALESCE(balance_purchased, 0) FROM app.wallet_accounts WHERE account_id = $1::uuid`,
		accountID).Scan(&remaining)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return granted, 0, nil
		}
		return 0, 0, fmt.Errorf("read purchased balance: %w", err)
	}
	return granted, remaining, nil
}

// readRefusalDecidedAt resolves when the standing refusal was recorded, so
// the response deadline answers from the holder's own verdict.
func (r *Repository) readRefusalDecidedAt(ctx context.Context, accountID string, charter domain.CharterVersion) (time.Time, error) {
	var decidedAt time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT decided_at FROM app.economy_charter_consents WHERE account_id = $1::uuid AND charter_version = $2`,
		accountID, charter.String()).Scan(&decidedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("read refusal verdict: %w", err)
	}
	return decidedAt, nil
}

// refusalReviewReason names why a settled exit waits for a human: a
// contested charge always does, an empty revocation has nothing to act
// on, and a shortfall means consumption already took part of the grant.
func refusalReviewReason(source domain.RefundSource, revoke, granted, pre, paid, refunded int64) string {
	if source.IsDispute() {
		return application.RefusalReviewChargeback
	}
	if revoke == 0 {
		return application.RefusalReviewNothingReversible
	}
	reversible, err := refusalReversible(granted, paid, refunded)
	if err != nil || revoke < reversible {
		return application.RefusalReviewAlreadyConsumed
	}
	_ = pre
	return ""
}

// refusalReversible recomputes the uncapped reversible quantity for review
// attribution. It mirrors the domain proration without the balance cap.
func refusalReversible(granted, paid, refunded int64) (int64, error) {
	if granted == 0 {
		return 0, nil
	}
	if refunded >= paid {
		return granted, nil
	}
	return granted * refunded / paid, nil
}

// createRefusalRefund attempts the settlement once: revalidated refusal,
// legacy-only debit and idempotent record share one transaction.
func (r *Repository) createRefusalRefund(ctx context.Context, request application.RefusalRefundRequest) (*application.RefusalRefundResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refusal refund transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := requireUnfrozen(ctx, tx); err != nil {
		return nil, err
	}
	decidedAt, err := lockRefusalVerdict(ctx, tx, request.AccountID, request.Charter)
	if err != nil {
		return nil, err
	}
	granted, remaining, free, err := lockPurchasedPosition(ctx, tx, request.AccountID)
	if err != nil {
		return nil, err
	}
	_ = free
	revoke, needsReview, err := domain.AssessRefusalRefund(granted, remaining, request.PaidMinor, request.Refunded, request.Source)
	if err != nil {
		return nil, err
	}
	reason := refusalReviewReason(request.Source, revoke, granted, remaining, request.PaidMinor, request.Refunded)
	if revoke == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit empty refusal refund: %w", err)
		}
		return &application.RefusalRefundResult{
			RevokeUnits:  0,
			FiatMinor:    request.Refunded,
			NeedsReview:  needsReview,
			ReviewReason: reason,
			RespondBy:    domain.RefusalResponseDue(decidedAt),
		}, nil
	}
	if err := insertRefusalDebit(ctx, tx, request, revoke); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit refusal refund: %w", err)
	}
	return &application.RefusalRefundResult{
		RevokeUnits:  revoke,
		FiatMinor:    request.Refunded,
		NeedsReview:  needsReview,
		ReviewReason: reason,
		RespondBy:    domain.RefusalResponseDue(decidedAt),
	}, nil
}

// lockRefusalVerdict re-reads the verdict under lock: only a standing
// refusal may exit through a refund, so acceptance, absence or another
// holder's row never reaches the legacy journal.
func lockRefusalVerdict(ctx context.Context, tx pgx.Tx, accountID string, charter domain.CharterVersion) (time.Time, error) {
	var decision string
	var decidedAt time.Time
	err := tx.QueryRow(ctx,
		`SELECT decision, decided_at FROM app.economy_charter_consents
		 WHERE account_id = $1::uuid AND charter_version = $2 FOR UPDATE`,
		accountID, charter.String()).Scan(&decision, &decidedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, domain.ErrConsentRequired
		}
		return time.Time{}, fmt.Errorf("lock refusal verdict: %w", err)
	}
	if decision != domain.ConsentRefused.String() {
		return time.Time{}, domain.ErrConsentRequired
	}
	return decidedAt, nil
}

// lockPurchasedPosition locks the holder's own legacy row and reads the
// purchased grant ever credited beside both balances. Only this row is
// ever locked or written: an innocent third party never moves.
func lockPurchasedPosition(ctx context.Context, tx pgx.Tx, accountID string) (granted, remaining, free int64, err error) {
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(balance_free, 0), COALESCE(balance_purchased, 0)
		 FROM app.wallet_accounts WHERE account_id = $1::uuid FOR UPDATE`,
		accountID).Scan(&free, &remaining)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			remaining = 0
		} else {
			return 0, 0, 0, fmt.Errorf("lock legacy balances: %w", err)
		}
	}
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(t.amount), 0) FROM app.wallet_transactions t
		 JOIN app.wallet_operations o ON o.id = t.operation_id
		 WHERE o.account_id = $1::uuid AND t.bucket = 'PURCHASED_INK' AND t.amount > 0`,
		accountID).Scan(&granted)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("sum purchased grants: %w", err)
	}
	return granted, remaining, free, nil
}

// insertRefusalDebit writes the legacy-only compensation: one
// debit_refund operation, its single PURCHASED_INK leg and the rewritten
// projection, all under the row lock the caller holds.
func insertRefusalDebit(ctx context.Context, tx pgx.Tx, request application.RefusalRefundRequest, revoke int64) error {
	var operation string
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		 VALUES ($1::uuid, 'debit_refund', $2, $3) RETURNING id::text`,
		request.AccountID, refundKey(request.ProviderID),
		refundReference(request.Charter, request.ProviderID, request.PaidMinor, request.Refunded, request.Source)).Scan(&operation); err != nil {
		return fmt.Errorf("record refusal operation: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) VALUES ($1::uuid, 'PURCHASED_INK', $2)`,
		operation, -revoke); err != nil {
		return fmt.Errorf("record refusal leg: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE app.wallet_accounts SET balance_purchased = balance_purchased - $2 WHERE account_id = $1::uuid`,
		request.AccountID, revoke); err != nil {
		return fmt.Errorf("rewrite purchased projection: %w", err)
	}
	return nil
}

// isRefusalConflict reports whether err is a unique collision that a
// re-lookup can resolve: a concurrent run of the same provider object.
func isRefusalConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "wallet_operations_idempotency_key_unique"
}
