package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.DisbursementRepository = (*Repository)(nil)

// Disbursement intention coordinates: the act is the idempotency key,
// the actor is fixed to the Treasury itself, and the operation names
// the disbursement. Callers never choose them: a spoofed actor would
// settle the same money under another name.
const disburseOperation = "disburse"

// disburseActor is the fixed intention actor for disbursement acts.
const disburseActor = "treasury"

// disburseHash binds the settled outcome: act, vault, beneficiary,
// purpose, amount and both governors. A retry recomputes it
// identically; anything else is a different act and never a replay,
// so a duplicated approval resolves instead of paying twice.
func disburseHash(request application.DisburseRequest) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s",
		string(request.Key), request.Vault.String(), string(request.Beneficiary),
		request.Purpose.String(), request.Amount.Millis(),
		string(request.ApproverOne), string(request.ApproverTwo), disburseOperation)
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

// Disburse settles one governed act in a single transaction: the
// origin vault debits while the beneficiary credits, and the audit row
// names governors beside the legs, or nothing moves at all. Short
// origins refuse instead of minting, unknown beneficiaries resolve to
// absence instead of a guess, and a unique collision retries once
// through re-lookup, so concurrent runs of one act resolve the single
// settlement instead of duplicating it.
func (r *Repository) Disburse(ctx context.Context, request application.DisburseRequest) (*application.DisburseResult, error) {
	if replayed, err := r.lookupDisbursement(ctx, request); err != nil || replayed != nil {
		return replayed, err
	}
	var last error
	for range 2 {
		result, err := r.createDisbursement(ctx, request)
		if err == nil {
			return result, nil
		}
		if !isDisburseConflict(err) {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupDisbursement(ctx, request); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("disbursement unsettled after conflict: %w", last)
}

// lookupDisbursement resolves a settled act without writing: the
// post-commit retry path that makes crash recovery exactly-once. Terms
// that settle nothing resolve to absence, and divergent terms under
// one act are a conflict, never a replay.
func (r *Repository) lookupDisbursement(ctx context.Context, request application.DisburseRequest) (*application.DisburseResult, error) {
	var transferID string
	var millis int64
	var hash string
	err := r.pool.QueryRow(ctx,
		`SELECT transfer_id::text, amount_milli, payload_hash FROM app.economy_intentions
		 WHERE intention_key = $1 AND actor = $2 AND operation = 'disburse'`,
		string(request.Key), disburseActor).Scan(&transferID, &millis, &hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup disbursement: %w", err)
	}
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		return nil, fmt.Errorf("stored disbursement holds %d milliINK: %w", millis, err)
	}
	if !amount.Equals(request.Amount) || hash != disburseHash(request) {
		return nil, domain.ErrIntentionConflict
	}
	return &application.DisburseResult{TransferID: transferID, Paid: amount, Replayed: true}, nil
}

// createDisbursement attempts the settlement once: rechecked origin
// stock, both legs, the audit row and the idempotent act sharing one
// transaction.
func (r *Repository) createDisbursement(ctx context.Context, request application.DisburseRequest) (*application.DisburseResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin disbursement transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := requireUnfrozen(ctx, tx); err != nil {
		return nil, err
	}
	origin, err := resolveCustody(ctx, tx, string(domain.CustodyTreasury), request.Vault.String())
	if err != nil {
		return nil, err
	}
	beneficiary, err := ensureCustody(ctx, tx, string(domain.CustodyUser), string(request.Beneficiary))
	if err != nil {
		return nil, err
	}
	if err := lockCustodies(ctx, tx, origin, beneficiary); err != nil {
		return nil, err
	}
	stock, err := custodyBalance(ctx, tx, origin)
	if err != nil {
		return nil, err
	}
	if _, err := stock.Sub(request.Amount); err != nil {
		return nil, domain.ErrInsufficientMilliInk
	}
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	if err := moveLegs(ctx, tx, transferID, origin, beneficiary, request.Amount.Millis()); err != nil {
		return nil, err
	}
	if err := recordDisbursement(ctx, tx, request, transferID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit disbursement: %w", err)
	}
	return &application.DisburseResult{TransferID: transferID, Paid: request.Amount}, nil
}

// recordDisbursement writes the audit row beside the settled legs: the
// governors stay named with the money they moved. Foreign keys refuse
// unknown beneficiaries and approvers, and the CHECKs refuse partial
// credentials and self-approval a second time, at the journal edge.
func recordDisbursement(ctx context.Context, tx pgx.Tx, request application.DisburseRequest, transferID string) error {
	hash := disburseHash(request)
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_intentions (intention_key, actor, operation, payload_hash, transfer_id, amount_milli)
		 VALUES ($1, $2, 'disburse', $3, $4::uuid, $5)`,
		string(request.Key), disburseActor, hash, transferID, request.Amount.Millis()); err != nil {
		return fmt.Errorf("record disbursement act: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.treasury_disbursements
		 (disbursement_key, origin_vault, beneficiary_account_id, purpose, amount_milli, approver_one, approver_two, transfer_id)
		 VALUES ($1, $2, $3::uuid, $4, $5, $6::uuid, $7::uuid, $8::uuid)`,
		string(request.Key), request.Vault.String(), string(request.Beneficiary),
		request.Purpose.String(), request.Amount.Millis(),
		string(request.ApproverOne), string(request.ApproverTwo), transferID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return domain.ErrUnknownCustody
		}
		return fmt.Errorf("record disbursement audit: %w", err)
	}
	return nil
}

// isDisburseConflict reports whether err is a unique collision that a
// re-lookup can resolve: a concurrent run of the same act, or a
// transfer id collision getting a fresh id on retry.
func isDisburseConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == intentionTripleConstraint ||
		pgErr.ConstraintName == intentionTransferConstraint ||
		pgErr.ConstraintName == "treasury_disbursements_key_unique" ||
		pgErr.ConstraintName == "treasury_disbursements_transfer_unique"
}
