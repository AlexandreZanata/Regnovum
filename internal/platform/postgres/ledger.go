package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Shared append-only journal primitives (P37-T02).
//
// Every INK movement writes paired legs under one transfer, locks
// both custodies in canonical id order before any balance is read,
// derives balances from the journal instead of storing them, and
// refuses mutations while the book is frozen. The three delivery
// adapters that move value (economy, metering, commerce) share these
// helpers instead of copying them: the SQL names tables, never Go
// types, so the platform stays strictly technical with standard and
// driver imports only. Callers map absence and frozen states to
// their own domain errors.
//
// LedgerQuerier covers pool and transaction reads.
type LedgerQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ResolveLedgerCustody maps a (kind, label) pair to its registry id.
// Unknown pairs report absence instead of failing: callers decide
// whether absence is unknown, inactive or unprovisioned.
func ResolveLedgerCustody(ctx context.Context, q LedgerQuerier, kind, label string) (string, bool, error) {
	var id string
	err := q.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2`,
		kind, label).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("resolve custody: %w", err)
	}
	return id, true, nil
}

// LockLedgerCustodies pins both custody rows in id order. Canonical
// ordering is what keeps two settlements in opposite directions from
// deadlocking: every transaction asks for the same rows in the same
// sequence.
func LockLedgerCustodies(ctx context.Context, tx pgx.Tx, firstID, secondID string) error {
	if _, err := tx.Exec(ctx,
		`SELECT id FROM app.economy_custodies WHERE id IN ($1::uuid, $2::uuid) ORDER BY id FOR UPDATE`,
		firstID, secondID); err != nil {
		return fmt.Errorf("lock custodies: %w", err)
	}
	return nil
}

// LedgerBalanceMillis rebuilds the balance from the journal in
// milliINK: credits minus debits. Balances are never stored, only
// derived, so they cannot drift from the legs.
func LedgerBalanceMillis(ctx context.Context, q LedgerQuerier, custodyID string) (int64, error) {
	var balance int64
	err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE custody_id = $1::uuid`,
		custodyID).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("read balance: %w", err)
	}
	return balance, nil
}

// LedgerFrozen reads the single conservation flag: a missing row
// means a book that never froze, which is open. Mutations check it;
// reads never do.
func LedgerFrozen(ctx context.Context, q LedgerQuerier) (bool, error) {
	var frozen bool
	if err := q.QueryRow(ctx, `SELECT frozen FROM app.economy_mode`).Scan(&frozen); err != nil {
		return false, nil
	}
	return frozen, nil
}
