package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

// commerceBookQuerier is the minimum to judge one commerce book
// from a pool or a transaction: the seal landing between the check
// and the write still refuses inside the same transaction.
type commerceBookQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// requireCommerceBookActive refuses new admissions in unknown books
// and in books past admission: closing, sealed or archived books
// are readable history, never a live commerce book. The
// compat-legacy namespace carries no lifecycle event and stays
// admissible for the legacy path.
func requireCommerceBookActive(ctx context.Context, q commerceBookQuerier, season commercedomain.SeasonKey) error {
	if season == commercedomain.SeasonKey(commercedomain.CompatSeasonKey) {
		return nil
	}
	var present bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.seasons WHERE season_key = $1)`,
		season.String()).Scan(&present); err != nil {
		return fmt.Errorf("read book registry: %w", err)
	}
	if !present {
		return commercedomain.ErrInvalidContract
	}
	var closed bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.season_lifecycle WHERE season_key = $1 AND to_state IN ('closing','sealed','archived'))`,
		season.String()).Scan(&closed); err != nil {
		return fmt.Errorf("read book lifecycle: %w", err)
	}
	if closed {
		return commercedomain.ErrContractState
	}
	return nil
}

// requireCommerceWindowActive refuses admission outside the
// half-open book window [starts_at, ends_at) judged on the database
// clock: the start admits, the exact end already belongs to the
// successor. The compat-legacy namespace bypasses the window and
// stays admissible for the legacy path.
func requireCommerceWindowActive(ctx context.Context, q commerceBookQuerier, season commercedomain.SeasonKey) error {
	if season == commercedomain.SeasonKey(commercedomain.CompatSeasonKey) {
		return nil
	}
	var startsAt, endsAt, now time.Time
	if err := q.QueryRow(ctx,
		`SELECT starts_at, ends_at, now() FROM app.seasons WHERE season_key = $1`,
		season.String()).Scan(&startsAt, &endsAt, &now); err != nil {
		return fmt.Errorf("read book window: %w", err)
	}
	if err := commercedomain.CheckSeasonWindow(season, now, startsAt, endsAt); err != nil {
		return err
	}
	return nil
}

func requireCommerceBookActiveTx(ctx context.Context, tx pgx.Tx, season commercedomain.SeasonKey) error {
	if err := requireCommerceBookActive(ctx, tx, season); err != nil {
		return err
	}
	return requireCommerceWindowActive(ctx, tx, season)
}
