// Shared transaction context of the PostgreSQL platform package (P07-T05).
//
// Modules keep their independence by defining their own UnitOfWork ports;
// bootstrap composes those ports with TxManager, which begins one database
// transaction and exposes it through the context. Outbound adapters that
// support shared transactions (starting with the Arena Pass consumption)
// check TxFromContext and join the caller transaction instead of opening
// their own, so writes from different modules commit or roll back together.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// txContextKey is the unexported context key of the active transaction.
type txContextKey struct{}

// WithTx returns a context carrying the given transaction. A nil transaction
// is ignored so a missing transaction is always explicit.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	if tx == nil {
		return ctx
	}
	return context.WithValue(ctx, txContextKey{}, tx)
}

// TxFromContext returns the transaction carried by the context, if any.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	if ctx == nil {
		return nil, false
	}
	tx, ok := ctx.Value(txContextKey{}).(pgx.Tx)
	return tx, ok && tx != nil
}

// TxManager runs functions inside a single database transaction and exposes
// it to participants through the context. It satisfies the UnitOfWork ports
// modules declare for themselves; bootstrap owns the composition.
type TxManager struct {
	pool *pgxpool.Pool
}

// NewTxManager creates a transaction manager over the given pool.
func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

// WithinTransaction begins a transaction, runs fn with its context and
// commits only when fn returns nil; any error (including a panic-free
// failure inside fn) rolls the whole transaction back.
//
// A fault armed with WithTxFault fires after fn succeeds: FailBeforeCommit
// refuses the commit so every write rolls back, and FailAfterCommit commits
// for real and then reports ErrInjectedTxFault, which is exactly the
// after-commit uncertainty (the commit may have landed) that idempotent
// operations must survive.
func (m *TxManager) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin shared transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := fn(WithTx(ctx, tx)); err != nil {
		return err
	}

	if txFaultFromContext(ctx) == TxFaultBeforeCommit {
		return ErrInjectedTxFault
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit shared transaction: %w", err)
	}
	if txFaultFromContext(ctx) == TxFaultAfterCommit {
		return ErrInjectedTxFault
	}
	return nil
}
