package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
)

// seasonBookQuerier is the minimum to judge one publication book
// from a pool or a transaction: the seal landing between the check
// and the write still refuses inside the same transaction.
type seasonBookQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// requireSeasonBookActive refuses new admissions in unknown books
// and in books past admission: closing, sealed or archived books
// are readable history, never a live publication book. The
// compat-legacy namespace carries no lifecycle event and stays
// admissible for the legacy path.
func requireSeasonBookActive(ctx context.Context, q seasonBookQuerier, season meteringdomain.SeasonKey) error {
	if season == meteringdomain.SeasonKey(meteringdomain.CompatSeasonKey) {
		return nil
	}
	var present bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.seasons WHERE season_key = $1)`,
		season.String()).Scan(&present); err != nil {
		return fmt.Errorf("read book registry: %w", err)
	}
	if !present {
		return meteringdomain.ErrInvalidQuote
	}
	var closed bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.season_lifecycle WHERE season_key = $1 AND to_state IN ('closing','sealed','archived'))`,
		season.String()).Scan(&closed); err != nil {
		return fmt.Errorf("read book lifecycle: %w", err)
	}
	if closed {
		return meteringdomain.ErrQuoteExpired
	}
	return nil
}

// requireSeasonWindowActive refuses admission outside the half-open
// book window [starts_at, ends_at) judged on the database clock:
// the start admits, the exact end already belongs to the successor.
// The compat-legacy namespace bypasses the window and stays
// admissible for the legacy path.
func requireSeasonWindowActive(ctx context.Context, q seasonBookQuerier, season meteringdomain.SeasonKey) error {
	if season == meteringdomain.SeasonKey(meteringdomain.CompatSeasonKey) {
		return nil
	}
	var startsAt, endsAt, now time.Time
	if err := q.QueryRow(ctx,
		`SELECT starts_at, ends_at, now() FROM app.seasons WHERE season_key = $1`,
		season.String()).Scan(&startsAt, &endsAt, &now); err != nil {
		return fmt.Errorf("read book window: %w", err)
	}
	if err := meteringdomain.CheckSeasonWindow(season, now, startsAt, endsAt); err != nil {
		return err
	}
	return nil
}

func requireSeasonBookActiveTx(ctx context.Context, tx pgx.Tx, season meteringdomain.SeasonKey) error {
	if err := requireSeasonBookActive(ctx, tx, season); err != nil {
		return err
	}
	return requireSeasonWindowActive(ctx, tx, season)
}
