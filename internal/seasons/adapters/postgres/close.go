package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/seasons/application"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

// Closer is the fenced closing barrier store: one run per book with
// generation, lease, checkpoint cursors and the cutoff conservation
// snapshot. It writes only season_lifecycle and season_close_runs:
// legs move solely through the family drain paths under a valid
// lease, and liquidation never enters any competitive ranking.
//
// Lock order is always the run row first, then item rows, with no
// network under lock. Cursors order batches for determinism only:
// commit order is never inferred from keys or timestamps, the
// barrier orders admitted work by commit visibility. Sealed books
// stay readable history.
type Closer struct {
	pool *pgxpool.Pool
}

var _ application.CloseStore = (*Closer)(nil)

// NewCloser builds the barrier store with explicit wiring. Only the
// composition root instantiates it; tests stand in for the root.
func NewCloser(pool *pgxpool.Pool) (*Closer, error) {
	if pool == nil {
		return nil, fmt.Errorf("seasons: closing store needs a pool")
	}
	return &Closer{pool: pool}, nil
}

// closeQuerier covers pool and transaction reads and writes for the
// barrier: single statements only, callers own the transaction.
type closeQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// runRow is one stored barrier run without driver types.
type runRow struct {
	view        application.CloseRunView
	leasedUntil time.Time
	state       string
}

// scanRunRow reads one barrier run row.
func scanRunRow(ctx context.Context, q closeQuerier, season string) (runRow, error) {
	stored, err := scanRunRowTx(ctx, q, season, false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return runRow{}, seasondomain.ErrInvalidSeason
		}
		return runRow{}, err
	}
	return stored, nil
}

// fenceRun judges one drain write against the stored run inside the
// caller transaction: same generation, same owner, never sealed.
func fenceRun(stored runRow, drain seasondomain.CloseDrain) error {
	if drain.Season != stored.view.Season {
		return seasondomain.ErrInvalidSeason
	}
	if drain.Generation != stored.view.Generation {
		return seasondomain.ErrStaleGeneration
	}
	if drain.Owner != stored.view.LeaseOwner {
		return seasondomain.ErrLeaseHeld
	}
	if stored.state == string(seasondomain.CloseStateSealed) {
		return seasondomain.ErrInvalidTransition
	}
	return nil
}

// latestStage reads the newest lifecycle stage of one book.
func latestStage(ctx context.Context, q closeQuerier, season string) (string, bool, error) {
	var stage string
	err := q.QueryRow(ctx,
		`SELECT to_state FROM app.season_lifecycle WHERE season_key = $1
		 ORDER BY recorded_at DESC, id DESC LIMIT 1`, season).Scan(&stage)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read book stage: %w", err)
	}
	return stage, true, nil
}

// closeSnapshot captures the conservation sentinels of one book: the
// Genesis S, the journal leg count and the settled intention count.
type closeSnapshot struct {
	milli      int64
	legs       int64
	intentions int64
}

// readSnapshot counts one book without locking: the barrier, not the
// snapshot, orders admitted work.
func readSnapshot(ctx context.Context, q closeQuerier, season string) (closeSnapshot, error) {
	var snap closeSnapshot
	if err := q.QueryRow(ctx,
		`SELECT COALESCE((SELECT amount_milli FROM app.economy_genesis WHERE season_key = $1), 0)`,
		season).Scan(&snap.milli); err != nil {
		return closeSnapshot{}, fmt.Errorf("read genesis snapshot: %w", err)
	}
	if err := q.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_entries WHERE season_key = $1`, season).Scan(&snap.legs); err != nil {
		return closeSnapshot{}, fmt.Errorf("read legs snapshot: %w", err)
	}
	if err := q.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_intentions WHERE season_key = $1`, season).Scan(&snap.intentions); err != nil {
		return closeSnapshot{}, fmt.Errorf("read intentions snapshot: %w", err)
	}
	return snap, nil
}

// isStageConflict reports whether err is a lifecycle uniqueness
// collision: a concurrent closer recording the same stage.
func isStageConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "season_lifecycle_stage_unique"
}

// isRunConflict reports whether err is a close-run primary key
// collision: a concurrent closer opening the same barrier.
func isRunConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "season_close_runs_pkey"
}

// BeginClose records the barrier once the book end is reached on the
// database clock. Two closers open exactly one run: the loser
// replays the stored run in the same generation. Never-active books
// and past stages refuse: closing starts from active only.
func (c *Closer) BeginClose(ctx context.Context, season, owner string, ttl time.Duration) (application.CloseRunView, error) {
	if _, err := seasondomain.ParseCloseOwner(owner); err != nil {
		return application.CloseRunView{}, err
	}
	if ttl <= 0 {
		return application.CloseRunView{}, seasondomain.ErrInvalidSeason
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return application.CloseRunView{}, fmt.Errorf("begin close transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var startsAt, endsAt, now time.Time
	if err := tx.QueryRow(ctx,
		`SELECT starts_at, ends_at, now() FROM app.seasons WHERE season_key = $1`, season).Scan(&startsAt, &endsAt, &now); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.CloseRunView{}, seasondomain.ErrInvalidSeason
		}
		return application.CloseRunView{}, fmt.Errorf("read book window: %w", err)
	}
	run, err := seasondomain.BeginCloseRun(applicationBeginRequest(season, owner, endsAt, now, ttl))
	if err != nil {
		return application.CloseRunView{}, err
	}
	stage, found, err := latestStage(ctx, tx, season)
	if err != nil {
		return application.CloseRunView{}, err
	}
	if !found || stage != string(seasondomain.StateActive) {
		if stage == string(seasondomain.StateClosing) {
			return c.replayRun(ctx, season)
		}
		return application.CloseRunView{}, seasondomain.ErrInvalidTransition
	}
	snap, err := readSnapshot(ctx, tx, season)
	if err != nil {
		return application.CloseRunView{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ($1, 'active', 'closing', now())`, season); err != nil {
		if isStageConflict(err) {
			return c.replayRun(ctx, season)
		}
		return application.CloseRunView{}, fmt.Errorf("record closing stage: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.season_close_runs
		 (season_key, generation, lease_owner, leased_until, state, cutoff_at,
		  snapshot_milli, snapshot_legs, snapshot_intentions)
		 VALUES ($1, 1, $2, $3, 'closing', $4, $5, $6, $7)`,
		season, owner, run.LeasedUntil, run.CutoffAt, snap.milli, snap.legs, snap.intentions); err != nil {
		if isRunConflict(err) {
			return c.replayRun(ctx, season)
		}
		return application.CloseRunView{}, fmt.Errorf("record close run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return application.CloseRunView{}, fmt.Errorf("commit close barrier: %w", err)
	}
	return c.Load(ctx, season)
}

// applicationBeginRequest adapts the adapter clock readings to the
// pure barrier opening. Snapshots are filled by the caller after the
// pure check passes; the stored row carries the transaction counts.
func applicationBeginRequest(season, owner string, endsAt, now time.Time, ttl time.Duration) seasondomain.BeginRequest {
	return seasondomain.BeginRequest{
		Season: season, Owner: owner, EndsAt: endsAt, Now: now, LeaseTTL: ttl,
	}
}

// replayRun loads the stored run for a concurrent closer after
// rolling back the collided transaction: the loser proceeds in the
// winner generation instead of opening a second barrier. The winner
// may still be committing, so the replay polls briefly.
func (c *Closer) replayRun(ctx context.Context, season string) (application.CloseRunView, error) {
	var last error
	for i := 0; i < 100; i++ {
		stored, err := c.Load(ctx, season)
		if err == nil {
			return stored, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return application.CloseRunView{}, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return application.CloseRunView{}, last
}

// Load reads one stored barrier run.
func (c *Closer) Load(ctx context.Context, season string) (application.CloseRunView, error) {
	stored, err := scanRunRow(ctx, c.pool, season)
	if err != nil {
		return application.CloseRunView{}, err
	}
	return stored.view, nil
}
