package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.IdempotentTransferRepository = (*Repository)(nil)

// intentionConstraints names the unique guards the adapter tells apart:
// the triple (a concurrent run of the same intention) and the transfer
// (a random id collision, retried with a fresh id).
const (
	intentionTripleConstraint   = "economy_intentions_triple_unique"
	intentionTransferConstraint = "economy_intentions_transfer_unique"
)

// findIntention resolves a settled triple to its stored response. A
// matching payload replays untouched; a different payload under the same
// triple is a conflict, never a merge.
func findIntention(ctx context.Context, q rowQuerier, request application.IdempotentTransferRequest) (*application.IdempotentTransferResult, error) {
	var storedHash, transferID string
	var millis int64
	err := q.QueryRow(ctx,
		`SELECT payload_hash, transfer_id::text, amount_milli FROM app.economy_intentions
		 WHERE intention_key = $1 AND actor = $2 AND operation = $3`,
		string(request.Key), string(request.Actor), string(request.Operation),
	).Scan(&storedHash, &transferID, &millis)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup intention: %w", err)
	}
	if storedHash != request.PayloadHash {
		return nil, domain.ErrIntentionConflict
	}
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		return nil, fmt.Errorf("stored intention holds %d milliINK: %w", millis, err)
	}
	return &application.IdempotentTransferResult{
		TransferID: transferID,
		Debited:    amount,
		Credited:   amount,
		Replayed:   true,
	}, nil
}

// TransferIdempotent settles one business intention at most once. The
// lookup, the legs and the persisted response share a single
// transaction; a retry after a post-commit timeout reads the stored
// outcome instead of writing new legs.
func (r *Repository) TransferIdempotent(ctx context.Context, request application.IdempotentTransferRequest) (*application.IdempotentTransferResult, error) {
	if stored, err := findIntention(ctx, r.pool, request); err != nil || stored != nil {
		return stored, err
	}
	var lastErr error
	for range 2 {
		result, retry, err := r.createIntention(ctx, request)
		if err == nil {
			return result, nil
		}
		if !retry {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// createIntention attempts the settlement in one transaction. It reports
// whether a retry is worthwhile: true only when a concurrent run of the
// same triple (or a transfer id collision) may have committed first.
func (r *Repository) createIntention(ctx context.Context, request application.IdempotentTransferRequest) (*application.IdempotentTransferResult, bool, error) {
	// Fail-closed order mirrors the use case: known kinds first, then
	// the in-transaction checks in the same sequence.
	if !request.FromKind.IsValid() || !request.ToKind.IsValid() {
		return nil, false, domain.ErrUnknownCustody
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin intention transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Frozen books settle nothing new: settled intentions still replay
	// through the fast path above, which writes nothing.
	if err := requireUnfrozen(ctx, tx); err != nil {
		return nil, false, err
	}

	if stored, err := findIntention(ctx, tx, request); err != nil || stored != nil {
		return stored, false, err
	}
	fromID, err := resolveCustody(ctx, tx, request.FromKind.String(), request.FromLabel)
	if err != nil {
		return nil, false, err
	}
	toID, err := resolveCustody(ctx, tx, request.ToKind.String(), request.ToLabel)
	if err != nil {
		return nil, false, err
	}
	if fromID == toID {
		return nil, false, domain.ErrSameCustody
	}
	if !request.FromKind.CanSpend() {
		return nil, false, domain.ErrUnauthorizedCustody
	}
	if request.Amount.IsZero() {
		return nil, false, domain.ErrInvalidMilliInk
	}
	if err := lockCustodies(ctx, tx, fromID, toID); err != nil {
		return nil, false, err
	}
	balance, err := custodyBalance(ctx, tx, fromID)
	if err != nil {
		return nil, false, err
	}
	if _, err := balance.Sub(request.Amount); err != nil {
		return nil, false, err
	}

	transferID, retry, err := recordIntentionLegs(ctx, tx, request, fromID, toID)
	if err != nil {
		return nil, retry, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit intention: %w", err)
	}
	return &application.IdempotentTransferResult{
		TransferID: transferID,
		Debited:    request.Amount,
		Credited:   request.Amount,
		Replayed:   false,
	}, false, nil
}

// recordIntentionLegs writes the debit/credit pair and the intention row
// inside the caller transaction. A unique collision on the triple or the
// transfer id reports a worthwhile retry: a concurrent run may have
// committed the same intention first.
func recordIntentionLegs(ctx context.Context, tx pgx.Tx, request application.IdempotentTransferRequest, fromID, toID string) (string, bool, error) {
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return "", false, fmt.Errorf("generate transfer id: %w", err)
	}
	for _, leg := range []struct {
		custody   string
		direction string
	}{
		{fromID, "debit"},
		{toID, "credit"},
	} {
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
			 VALUES ($1::uuid, $2::uuid, $3, $4)`,
			transferID, leg.custody, leg.direction, request.Amount.Millis()); err != nil {
			return "", false, fmt.Errorf("record %s leg: %w", leg.direction, err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_intentions (intention_key, actor, operation, payload_hash, transfer_id, amount_milli)
		 VALUES ($1, $2, $3, $4, $5::uuid, $6)`,
		string(request.Key), string(request.Actor), string(request.Operation),
		request.PayloadHash, transferID, request.Amount.Millis()); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
			(pgErr.ConstraintName == intentionTripleConstraint || pgErr.ConstraintName == intentionTransferConstraint) {
			return "", true, fmt.Errorf("concurrent intention race: %w", err)
		}
		return "", false, fmt.Errorf("record intention: %w", err)
	}
	return transferID, false, nil
}
