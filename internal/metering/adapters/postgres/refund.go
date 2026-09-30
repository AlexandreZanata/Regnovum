package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
)

// Refund surface of the metering adapter (P36-T07): an erroneous
// publication is compensated by a new current transfer reversing the
// original legs, linked to its cause without editing it. The amount
// always equals the original charge, the posting time always comes
// from the database clock, and closed periods never reopen.
var _ application.RefundRepository = (*Repository)(nil)

// originalSettlement is the compensated cause: the settled row with
// the custody pair its transfer moved between. The season pins the
// original book: the compensation reverses in the same book, never
// in the current one, and never after the seal.
type originalSettlement struct {
	publicationID string
	amountMilli   int64
	debitKind     string
	debitLabel    string
	creditKind    string
	creditLabel   string
	season        meteringdomain.SeasonKey
}

// Refund compensates one settled publication in full keyed
// idempotently by account and token. Replays resolve the original
// compensation untouched, a second compensation of the same
// publication refuses, and unknown causes refuse without writing.
func (r *Repository) Refund(ctx context.Context, request application.RefundRequest) (*application.RefundResult, error) {
	if replayed, err := r.lookupRefund(ctx, r.pool, request); err != nil || replayed != nil {
		return replayed, err
	}
	var last error
	for range 2 {
		result, retry, err := r.createRefund(ctx, request)
		if err == nil {
			return result, nil
		}
		if !retry {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupRefund(ctx, r.pool, request); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("refund unsettled after conflict: %w", last)
}

// lookupRefund resolves a settled compensation without writing: the
// post-commit retry path that makes crash recovery exactly-once. An
// unknown cause refuses; terms that settle nothing resolve to
// absence; divergent terms under one key conflict, never replay.
func (r *Repository) lookupRefund(ctx context.Context, q rowQuerier, request application.RefundRequest) (*application.RefundResult, error) {
	original, err := readOriginalSettlement(ctx, q, request.Account, request.OriginalKey.String())
	if err != nil {
		return nil, err
	}
	var result application.RefundResult
	var originalID, reason string
	var amount int64
	err = q.QueryRow(ctx,
		`SELECT id::text, original_id::text, amount_milli, transfer_id::text, reason, posted_at
		 FROM app.metering_refunds WHERE account_label = $1 AND refund_key = $2`,
		request.Account, request.Key.String()).Scan(
		&result.RefundID, &originalID, &amount, &result.TransferID, &reason, &result.PostedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup refund: %w", err)
	}
	if originalID != original.publicationID || reason != request.Reason || amount != original.amountMilli {
		return nil, meteringdomain.ErrPublishConflict
	}
	result.OriginalID = originalID
	result.AmountMilli = amount
	result.Replayed = true
	return &result, nil
}

// readOriginalSettlement resolves the compensated cause with the
// custody pair its transfer moved between. Another account's
// publications resolve to absence: refunds settle only the owner's
// settled intentions. The season pins the original book for the
// same-book reversal below.
func readOriginalSettlement(ctx context.Context, q rowQuerier, account, originalKey string) (originalSettlement, error) {
	var settled originalSettlement
	var transfer, seasonKey string
	err := q.QueryRow(ctx,
		`SELECT id::text, amount_milli, transfer_id::text, season_key
		 FROM app.metering_publications WHERE account_label = $1 AND intention_key = $2`,
		account, originalKey).Scan(&settled.publicationID, &settled.amountMilli, &transfer, &seasonKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return originalSettlement{}, meteringdomain.ErrUnknownPublication
		}
		return originalSettlement{}, fmt.Errorf("lookup original publication: %w", err)
	}
	season, err := meteringdomain.ParseSeasonKey(seasonKey)
	if err != nil {
		return originalSettlement{}, fmt.Errorf("stored publication book %q: %w", seasonKey, err)
	}
	settled.season = season
	rows, err := q.Query(ctx,
		`SELECT c.kind, c.label, e.direction, e.amount_milli FROM app.economy_entries e
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE e.transfer_id = $1::uuid`, transfer)
	if err != nil {
		return originalSettlement{}, fmt.Errorf("read original legs: %w", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var kind, label, direction string
		var millis int64
		if err := rows.Scan(&kind, &label, &direction, &millis); err != nil {
			return originalSettlement{}, fmt.Errorf("scan original leg: %w", err)
		}
		if millis != settled.amountMilli {
			return originalSettlement{}, fmt.Errorf("original leg holds %d milliINK, want %d", millis, settled.amountMilli)
		}
		switch direction {
		case "debit":
			settled.debitKind, settled.debitLabel = kind, label
		case "credit":
			settled.creditKind, settled.creditLabel = kind, label
		default:
			return originalSettlement{}, fmt.Errorf("original leg with direction %q", direction)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return originalSettlement{}, fmt.Errorf("iterate original legs: %w", err)
	}
	if seen != 2 || settled.debitLabel == "" || settled.creditLabel == "" {
		return originalSettlement{}, fmt.Errorf("original transfer %s is not one pair", transfer)
	}
	return settled, nil
}

// createRefund attempts the compensation once in a single
// transaction: the legs reverse the original pair under a new
// transfer and the refund row links to the untouched cause, so the
// correction lands current without backdating or reopening history.
// The reversal carries the original book, never the current one,
// and refuses after the seal: a sealed book is readable history,
// never a live ledger.
func (r *Repository) createRefund(ctx context.Context, request application.RefundRequest) (*application.RefundResult, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin refund transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := requireLedgerOpen(ctx, tx); err != nil {
		return nil, false, err
	}
	if replayed, err := r.lookupRefund(ctx, tx, request); err != nil || replayed != nil {
		return replayed, false, err
	}
	original, err := readOriginalSettlementTx(ctx, tx, request)
	if err != nil {
		return nil, false, err
	}
	if err := requireSeasonBookActiveTx(ctx, tx, original.season); err != nil {
		return nil, false, err
	}
	fromID, err := resolveSeasonCustody(ctx, tx, original.creditKind, original.creditLabel, original.season)
	if err != nil {
		return nil, false, err
	}
	toID, err := resolveSeasonCustody(ctx, tx, original.debitKind, original.debitLabel, original.season)
	if err != nil {
		return nil, false, err
	}
	if err := coverRefund(ctx, tx, fromID, toID, original.amountMilli); err != nil {
		if !errors.Is(err, economydomain.ErrInsufficientMilliInk) {
			return nil, false, err
		}
		// A concurrent compensation of the same cause may have
		// settled while this attempt waited on the custody locks:
		// resolve it before refusing, so losers replay instead of
		// mistaking a won race for an empty source. Roll back
		// first: nothing was written yet.
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return nil, false, fmt.Errorf("abort uncovered refund: %w", rbErr)
		}
		if replayed, err := r.lookupRefund(ctx, r.pool, request); err != nil || replayed != nil {
			return replayed, false, err
		}
		return nil, false, err
	}
	transferID, err := recordSettlementLegs(ctx, tx, fromID, toID, original.amountMilli, original.season)
	if err != nil {
		return nil, false, err
	}
	result, retry, err := recordRefundRow(ctx, tx, request, original, transferID)
	if err != nil {
		return nil, retry, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit refund: %w", err)
	}
	return &result, false, nil
}

// readOriginalSettlementTx resolves the cause inside the writing
// transaction. It duplicates the lookup instead of reusing the pool
// read so the cause cannot settle between the check and the legs.
func readOriginalSettlementTx(ctx context.Context, tx pgx.Tx, request application.RefundRequest) (originalSettlement, error) {
	return readOriginalSettlement(ctx, tx, request.Account, request.OriginalKey.String())
}

// coverRefund locks the reversed pair in id order and rechecks the
// source balance inside the locks.
func coverRefund(ctx context.Context, tx pgx.Tx, fromID, toID string, millis int64) error {
	if err := lockLedgerCustodies(ctx, tx, fromID, toID); err != nil {
		return err
	}
	balance, err := ledgerBalance(ctx, tx, fromID)
	if err != nil {
		return err
	}
	cover, err := economydomain.NewMilliInk(millis)
	if err != nil {
		return err
	}
	_, err = balance.Sub(cover)
	return err
}

// recordRefundRow stores the compensation linked to its untouched
// cause and returns the database posted instant. A key collision
// retries through re-lookup; a second compensation of one cause
// refuses outright.
func recordRefundRow(ctx context.Context, tx pgx.Tx, request application.RefundRequest, original originalSettlement, transferID string) (application.RefundResult, bool, error) {
	var result application.RefundResult
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason)
		 VALUES ($1, $2, $3::uuid, $4, $5::uuid, $6)
		 RETURNING id::text, posted_at`,
		request.Key.String(), request.Account, original.publicationID,
		original.amountMilli, transferID, request.Reason).Scan(&result.RefundID, &result.PostedAt); err != nil {
		if isRefundRetryable(err) {
			return application.RefundResult{}, true, fmt.Errorf("concurrent refund race: %w", err)
		}
		if isRefundDuplicate(err) {
			return application.RefundResult{}, false, meteringdomain.ErrRefundDuplicate
		}
		return application.RefundResult{}, false, fmt.Errorf("record refund: %w", err)
	}
	result.TransferID = transferID
	result.OriginalID = original.publicationID
	result.AmountMilli = original.amountMilli
	return result, false, nil
}

// isRefundConflict reports whether err violates the named unique
// guard.
func isRefundConflict(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == constraint
}

// isRefundRetryable reports whether err is a unique collision that a
// re-lookup can resolve: a concurrent run of the same key, or a
// transfer id collision retried with a fresh id.
func isRefundRetryable(err error) bool {
	return isRefundConflict(err, "metering_refunds_account_key_unique") ||
		isRefundConflict(err, "metering_refunds_transfer_unique")
}

// isRefundDuplicate reports whether err doubles the compensation of
// one cause: a second refund key for the same publication.
func isRefundDuplicate(err error) bool {
	return isRefundConflict(err, "metering_refunds_original_unique")
}
