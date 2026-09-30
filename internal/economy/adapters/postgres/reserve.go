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

var _ application.ReserveRepository = (*Repository)(nil)

// Reserve intention coordinates: the act is the idempotency key, the
// actor is fixed to the Treasury itself, and the operation names the
// reserve funding. Callers never choose them: a spoofed actor would
// settle the same money under another name.
const (
	reserveActor     = "treasury"
	reserveOperation = "reserve"
)

// reserveHash binds the settled outcome: act and allocated amount. A
// retry recomputes it identically; anything else is a different act
// and never a replay, so later terms stay prospective.
func reserveHash(actID string, millis int64) string {
	canonical := fmt.Sprintf("%s\x00%d\x00%s", actID, millis, reserveOperation)
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

// AllocateReserve funds the Sovereign Reserve from existing Treasury
// stock in a single transaction: the Genesis home debits while the
// reserve vault credits, or nothing moves at all. Fresh value is never
// created and obligations are never debited: an empty home refuses
// instead of minting or invading escrows. A unique collision retries
// once through re-lookup, so concurrent runs of one act resolve the
// single settlement instead of duplicating it.
func (r *Repository) AllocateReserve(ctx context.Context, request application.AllocateReserveRequest) (*application.AllocateReserveResult, error) {
	if replayed, err := r.lookupReserveAllocation(ctx, request); err != nil || replayed != nil {
		return replayed, err
	}
	var last error
	for range 2 {
		result, err := r.createReserveAllocation(ctx, request)
		if err == nil {
			return result, nil
		}
		if !isReserveConflict(err) {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupReserveAllocation(ctx, request); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("reserve allocation unsettled after conflict: %w", last)
}

// lookupReserveAllocation resolves a settled allocation without
// writing: the post-commit retry path that makes crash recovery
// exactly-once. Terms that settle nothing resolve to absence, and a
// divergent amount under one act is a conflict, never a replay.
func (r *Repository) lookupReserveAllocation(ctx context.Context, request application.AllocateReserveRequest) (*application.AllocateReserveResult, error) {
	var transferID string
	var millis int64
	var hash string
	err := r.pool.QueryRow(ctx,
		`SELECT transfer_id::text, amount_milli, payload_hash FROM app.economy_intentions
		 WHERE intention_key = $1 AND actor = $2 AND operation = 'reserve'`,
		string(request.Act), reserveActor).Scan(&transferID, &millis, &hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup reserve allocation: %w", err)
	}
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		return nil, fmt.Errorf("stored allocation holds %d milliINK: %w", millis, err)
	}
	if !amount.Equals(request.Amount) || hash != reserveHash(string(request.Act), request.Amount.Millis()) {
		return nil, domain.ErrIntentionConflict
	}
	return &application.AllocateReserveResult{TransferID: transferID, Allocated: amount, Replayed: true}, nil
}

// createReserveAllocation attempts the settlement once: the home stock
// rechecked inside the row locks, both Treasury legs and the idempotent
// act sharing one transaction.
func (r *Repository) createReserveAllocation(ctx context.Context, request application.AllocateReserveRequest) (*application.AllocateReserveResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reserve transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := requireUnfrozen(ctx, tx); err != nil {
		return nil, err
	}
	home, err := resolveTreasuryHome(ctx, tx)
	if err != nil {
		return nil, err
	}
	vault, err := ensureCustody(ctx, tx, string(domain.CustodyTreasury), domain.TreasuryVaultSovereignReserve.String())
	if err != nil {
		return nil, err
	}
	if err := lockCustodies(ctx, tx, home, vault); err != nil {
		return nil, err
	}
	stock, err := custodyBalance(ctx, tx, home)
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
	if err := moveLegs(ctx, tx, transferID, home, vault, request.Amount.Millis()); err != nil {
		return nil, err
	}
	hash := reserveHash(string(request.Act), request.Amount.Millis())
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_intentions (intention_key, actor, operation, payload_hash, transfer_id, amount_milli)
		 VALUES ($1, $2, 'reserve', $3, $4::uuid, $5)`,
		string(request.Act), reserveActor, hash, transferID, request.Amount.Millis()); err != nil {
		return nil, fmt.Errorf("record reserve allocation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reserve allocation: %w", err)
	}
	return &application.AllocateReserveResult{TransferID: transferID, Allocated: request.Amount}, nil
}

// resolveTreasuryHome pins the Genesis home custody the reserve draws
// from: allocations leave existing stock, never obligations.
func resolveTreasuryHome(ctx context.Context, tx pgx.Tx) (string, error) {
	var home string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&home); err != nil {
		return "", fmt.Errorf("resolve treasury home: %w", err)
	}
	return home, nil
}

// isReserveConflict reports whether err is a unique collision that a
// re-lookup can resolve: a concurrent run of the same act, or a
// transfer id collision getting a fresh id on retry.
func isReserveConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == intentionTripleConstraint ||
		pgErr.ConstraintName == intentionTransferConstraint
}
