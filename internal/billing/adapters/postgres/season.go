package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
)

// purchaseBookQuerier is the minimum to judge one purchase book from
// a pool or a transaction: the seal landing between the check and
// the write still refuses inside the same transaction.
type purchaseBookQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// requirePurchaseBookActive refuses new admissions in unknown books
// and in books past admission: closing, sealed or archived books are
// readable history, never a live purchase book. The compat-legacy
// namespace carries no lifecycle event and stays admissible for the
// legacy path.
func requirePurchaseBookActive(ctx context.Context, q purchaseBookQuerier, season string) error {
	var present bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.seasons WHERE season_key = $1)`,
		season).Scan(&present); err != nil {
		return fmt.Errorf("read book registry: %w", err)
	}
	if !present {
		return application.ErrPurchaseQuoteNotFound
	}
	var closed bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.season_lifecycle WHERE season_key = $1 AND to_state IN ('closing','sealed','archived'))`,
		season).Scan(&closed); err != nil {
		return fmt.Errorf("read book lifecycle: %w", err)
	}
	if closed {
		return application.ErrPurchaseAfterCutoff
	}
	return nil
}

func requirePurchaseBookActiveTx(ctx context.Context, tx pgx.Tx, season string) error {
	return requirePurchaseBookActive(ctx, tx, season)
}
