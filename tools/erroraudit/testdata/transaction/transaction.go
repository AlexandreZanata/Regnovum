// Package transaction is the fixture of the transaction without a rollback: the
// commit is written and the path back is not, so an error between the two leaves
// the lock open until the connection ends.
package transaction

import "context"

// Transfer begins a transaction and never rolls it back.
func Transfer(ctx context.Context, pool *pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Open hands the transaction to nobody: the caller receives the error and the
// transaction is dropped with it.
func Open(ctx context.Context, pool *pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	_ = tx
	return nil
}

type pool struct{}

type transactionHandle struct{}

func (handle *transactionHandle) Commit(ctx context.Context) error { return nil }
