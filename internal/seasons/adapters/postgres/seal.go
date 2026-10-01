package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/seasons/application"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

// openObligations counts what still blocks a seal: non-terminal
// escrows and unsettled holds of one book. Terminal receipts never
// reopen and never count.
func openObligations(ctx context.Context, tx pgx.Tx, season string) (escrows, holds int64, err error) {
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM app.commerce_contracts c
		 WHERE c.season_key = $1 AND NOT EXISTS (
		   SELECT 1 FROM app.commerce_settlements s
		   WHERE s.contract_id = c.id AND s.action = ANY($2))`,
		season, terminalSettlementActions).Scan(&escrows); err != nil {
		return 0, 0, fmt.Errorf("count open escrows: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_holds
		 WHERE season_key = $1 AND status IN ('active', 'expired')`,
		season).Scan(&holds); err != nil {
		return 0, 0, fmt.Errorf("count open holds: %w", err)
	}
	return escrows, holds, nil
}

// Seal refuses while obligations stay open and records the sealed
// lifecycle row once conserved. Blocked seals move nothing: custody
// is preserved, no successor opens. Two sealers record exactly one
// seal: the loser replays it.
func (c *Closer) Seal(ctx context.Context, season string, generation int64, owner string) (application.SealView, error) {
	drain := seasondomain.CloseDrain{Season: season, Generation: generation, Owner: owner}
	if err := drain.Valid(); err != nil {
		return application.SealView{}, err
	}
	// A stored seal replays for the same fence instead of refusing:
	// one seal, every sealer agrees.
	if decided, found, err := existingSeal(ctx, c.pool, season); err != nil {
		return application.SealView{}, err
	} else if found {
		stored, err := c.Load(ctx, season)
		if err != nil {
			return application.SealView{}, err
		}
		if generation != stored.Generation {
			return application.SealView{}, seasondomain.ErrStaleGeneration
		}
		if owner != stored.LeaseOwner {
			return application.SealView{}, seasondomain.ErrLeaseHeld
		}
		return application.SealView{Season: season, Generation: stored.Generation, CutoffAt: stored.CutoffAt, SealedAt: decided}, nil
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return application.SealView{}, fmt.Errorf("begin seal transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	stored, err := drainFence(ctx, tx, drain)
	if err != nil {
		return application.SealView{}, err
	}
	openEscrows, activeHolds, err := openObligations(ctx, tx, season)
	if err != nil {
		return application.SealView{}, err
	}
	current, err := readSnapshot(ctx, tx, season)
	if err != nil {
		return application.SealView{}, err
	}
	if err := seasondomain.CanSeal(openEscrows, activeHolds,
		seasondomain.SealSnapshot{
			Milli:      stored.view.SnapshotMilli,
			Legs:       stored.view.SnapshotLegs,
			Intentions: stored.view.SnapshotIntentions,
		},
		seasondomain.SealSnapshot{
			Milli:      current.milli,
			Legs:       current.legs,
			Intentions: current.intentions,
		}); err != nil {
		return application.SealView{}, err
	}
	var sealedAt time.Time
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ($1, 'closing', 'sealed', now()) RETURNING decided_at`, season).Scan(&sealedAt); err != nil {
		if isSealConflict(err) {
			return c.replaySeal(ctx, season)
		}
		return application.SealView{}, fmt.Errorf("record sealed stage: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE app.season_close_runs SET state = 'sealed', updated_at = now()
		 WHERE season_key = $1 AND generation = $2 AND lease_owner = $3 AND state <> 'sealed'`,
		season, generation, owner)
	if err != nil {
		return application.SealView{}, fmt.Errorf("mark run sealed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return application.SealView{}, seasondomain.ErrStaleGeneration
	}
	if err := tx.Commit(ctx); err != nil {
		return application.SealView{}, fmt.Errorf("commit seal: %w", err)
	}
	return application.SealView{Season: season, Generation: generation, CutoffAt: stored.view.CutoffAt, SealedAt: sealedAt}, nil
}

// isSealConflict reports whether err is a sealed-stage uniqueness
// collision: a concurrent sealer recording the same seal.
func isSealConflict(err error) bool {
	return isStageConflict(err)
}

// existingSeal reads a stored seal without locking: a present seal
// replays for the same fence instead of refusing.
func existingSeal(ctx context.Context, q closeQuerier, season string) (time.Time, bool, error) {
	var decided time.Time
	if err := q.QueryRow(ctx,
		`SELECT decided_at FROM app.season_lifecycle
		 WHERE season_key = $1 AND to_state = 'sealed'`, season).Scan(&decided); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("read stored seal: %w", err)
	}
	return decided, true, nil
}

// replaySeal loads the stored seal for a concurrent sealer outside
// the rolled-back transaction: one seal, every sealer agrees.
func (c *Closer) replaySeal(ctx context.Context, season string) (application.SealView, error) {
	decided, found, err := existingSeal(ctx, c.pool, season)
	if err != nil {
		return application.SealView{}, err
	}
	if !found {
		return application.SealView{}, seasondomain.ErrInvalidSeason
	}
	stored, err := c.Load(ctx, season)
	if err != nil {
		return application.SealView{}, err
	}
	return application.SealView{Season: season, Generation: stored.Generation, CutoffAt: stored.CutoffAt, SealedAt: decided}, nil
}

// Takeover moves a lapsed run to the next generation under a new
// owner. A held lease refuses on the database clock; sealed runs
// never reopen.
func (c *Closer) Takeover(ctx context.Context, season, owner string, ttl time.Duration) (application.CloseRunView, error) {
	if _, err := seasondomain.ParseCloseOwner(owner); err != nil {
		return application.CloseRunView{}, err
	}
	if ttl <= 0 {
		return application.CloseRunView{}, seasondomain.ErrInvalidSeason
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return application.CloseRunView{}, fmt.Errorf("begin takeover transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	stored, err := scanRunRowTx(ctx, tx, season, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.CloseRunView{}, seasondomain.ErrInvalidSeason
		}
		return application.CloseRunView{}, err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return application.CloseRunView{}, fmt.Errorf("read database clock: %w", err)
	}
	next, err := seasondomain.TakeoverRun(seasondomain.CloseRun{
		Generation: stored.view.Generation, LeaseOwner: stored.view.LeaseOwner,
		LeasedUntil: stored.view.LeasedUntil,
		State:       closeStateOf(stored),
	}, owner, now, ttl)
	if err != nil {
		return application.CloseRunView{}, err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE app.season_close_runs
		 SET generation = $2, lease_owner = $3, leased_until = $4,
		     state = 'closing', error_code = '', error_detail = '', updated_at = now()
		 WHERE season_key = $1 AND generation = $5 AND state <> 'sealed'`,
		season, next.Generation, next.LeaseOwner, next.LeasedUntil, stored.view.Generation)
	if err != nil {
		return application.CloseRunView{}, fmt.Errorf("record takeover: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return application.CloseRunView{}, seasondomain.ErrStaleGeneration
	}
	if err := tx.Commit(ctx); err != nil {
		return application.CloseRunView{}, fmt.Errorf("commit takeover: %w", err)
	}
	return c.Load(ctx, season)
}

// RecordFailure parks a run that hit a worker error keeping the
// cursor for resume. Sealed runs never fail.
func (c *Closer) RecordFailure(ctx context.Context, season string, generation int64, owner, code, detail string) (application.CloseRunView, error) {
	drain := seasondomain.CloseDrain{Season: season, Generation: generation, Owner: owner}
	if err := drain.Valid(); err != nil {
		return application.CloseRunView{}, err
	}
	failure := seasondomain.CloseFailure{Code: code, Detail: detail}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return application.CloseRunView{}, fmt.Errorf("begin failure transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	stored, err := drainFence(ctx, tx, drain)
	if err != nil {
		return application.CloseRunView{}, err
	}
	next, err := seasondomain.RecordFailure(seasondomain.CloseRun{State: closeStateOf(stored)}, failure)
	if err != nil {
		return application.CloseRunView{}, err
	}
	_ = next
	tag, err := tx.Exec(ctx,
		`UPDATE app.season_close_runs SET state = 'failed', error_code = $2, error_detail = $3, updated_at = now()
		 WHERE season_key = $1 AND generation = $4 AND lease_owner = $5 AND state <> 'sealed'`,
		season, code, detail, generation, owner)
	if err != nil {
		return application.CloseRunView{}, fmt.Errorf("park close run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return application.CloseRunView{}, seasondomain.ErrStaleGeneration
	}
	if err := tx.Commit(ctx); err != nil {
		return application.CloseRunView{}, fmt.Errorf("commit close failure: %w", err)
	}
	return c.Load(ctx, season)
}

// closeStateOf maps the stored state name to the domain state.
func closeStateOf(stored runRow) seasondomain.CloseRunState {
	state, err := seasondomain.ParseCloseRunState(stored.state)
	if err != nil {
		return seasondomain.CloseStateClosing
	}
	return state
}
