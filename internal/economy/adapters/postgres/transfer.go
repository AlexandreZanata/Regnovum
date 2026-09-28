package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.TransferRepository = (*Repository)(nil)

// Transfer debits the source custody and credits the destination in one
// transaction. Both custody rows are locked in id order before any balance
// is read, so concurrent transfers serialize instead of double-spending;
// the balance is rechecked inside the lock, and the two legs commit
// together or not at all.
func (r *Repository) Transfer(ctx context.Context, request application.TransferRequest) (*application.TransferResult, error) {
	if !request.FromKind.CanSpend() {
		return nil, domain.ErrUnauthorizedCustody
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transfer transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Frozen books move nothing: reads continue, mutations wait.
	if err := requireUnfrozen(ctx, tx); err != nil {
		return nil, err
	}

	fromID, err := resolveCustody(ctx, tx, request.FromKind.String(), request.FromLabel)
	if err != nil {
		return nil, err
	}
	toID, err := resolveCustody(ctx, tx, request.ToKind.String(), request.ToLabel)
	if err != nil {
		return nil, err
	}
	if fromID == toID {
		return nil, domain.ErrSameCustody
	}
	if err := lockCustodies(ctx, tx, fromID, toID); err != nil {
		return nil, err
	}
	// The journal is the spendable balance: reservations move legs out
	// of the owner at reserve time, so what remains here is free to go.
	balance, err := custodyBalance(ctx, tx, fromID)
	if err != nil {
		return nil, err
	}
	if _, err := balance.Sub(request.Amount); err != nil {
		return nil, err
	}

	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $3)`,
		transferID, fromID, request.Amount.Millis()); err != nil {
		return nil, fmt.Errorf("record debit leg: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'credit', $3)`,
		transferID, toID, request.Amount.Millis()); err != nil {
		return nil, fmt.Errorf("record credit leg: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transfer: %w", err)
	}
	return &application.TransferResult{
		TransferID: transferID,
		Debited:    request.Amount,
		Credited:   request.Amount,
	}, nil
}

// resolveCustody maps a (kind, label) pair to its registry id. Unknown
// pairs fail before any lock is taken or leg written.
func resolveCustody(ctx context.Context, tx pgx.Tx, kind, label string) (string, error) {
	var id string
	err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2`,
		kind, label).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrUnknownCustody
		}
		return "", fmt.Errorf("resolve custody: %w", err)
	}
	return id, nil
}

// lockCustodies pins both custody rows in id order. Canonical ordering is
// what keeps two transfers in opposite directions from deadlocking: every
// transaction asks for the same rows in the same sequence.
func lockCustodies(ctx context.Context, tx pgx.Tx, firstID, secondID string) error {
	if _, err := tx.Exec(ctx,
		`SELECT id FROM app.economy_custodies WHERE id IN ($1::uuid, $2::uuid) ORDER BY id FOR UPDATE`,
		firstID, secondID); err != nil {
		return fmt.Errorf("lock custodies: %w", err)
	}
	return nil
}

// custodyBalance rebuilds the balance from the journal: credits minus
// debits. Balances are never stored, only derived, so they cannot drift
// from the legs.
func custodyBalance(ctx context.Context, tx pgx.Tx, custodyID string) (domain.MilliInk, error) {
	var balance int64
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE custody_id = $1::uuid`,
		custodyID).Scan(&balance)
	if err != nil {
		return domain.MilliInk{}, fmt.Errorf("read balance: %w", err)
	}
	return domain.NewMilliInk(balance)
}
