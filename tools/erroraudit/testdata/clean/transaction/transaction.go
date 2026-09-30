// Package transaction is the clean half of the transaction rule: the rollback is
// deferred before anything else can fail, which is the shape the whole tree uses
// and the one this gate requires.
package transaction

import "context"

// Transfer rolls back on every path that does not commit.
func Transfer(ctx context.Context, pool *pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := write(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type pool struct{}

type handle struct{}

func (h *handle) Rollback(ctx context.Context) error {
	return ctx.Err()
}

func (h *handle) Commit(ctx context.Context) error {
	return ctx.Err()
}

func write(ctx context.Context, h *handle) error {
	return ctx.Err()
}
