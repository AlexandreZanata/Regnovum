package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/seasons/application"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

// terminalSettlementActions are the money-moving escrow steps: a
// contract carrying any of them is settled history with its receipt
// preserved.
var terminalSettlementActions = []string{"release", "refund", "resolve-release", "resolve-refund"}

// drainFence loads the stored run locked and judges one drain write:
// same generation, same owner, never sealed.
func drainFence(ctx context.Context, tx pgx.Tx, drain seasondomain.CloseDrain) (runRow, error) {
	if err := drain.Valid(); err != nil {
		return runRow{}, err
	}
	stored, err := scanRunRowTx(ctx, tx, drain.Season, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return runRow{}, seasondomain.ErrInvalidSeason
		}
		return runRow{}, err
	}
	if err := fenceRun(stored, drain); err != nil {
		return runRow{}, err
	}
	return stored, nil
}

// scanRunRowTx reads one barrier run row, optionally locking it.
// Lock order is always the run row first, then item rows, with no
// network under lock.
func scanRunRowTx(ctx context.Context, q closeQuerier, season string, forUpdate bool) (runRow, error) {
	query := `SELECT season_key, generation, lease_owner, leased_until, state, cutoff_at,
		        last_hold_key, last_escrow_key, drained, blocked,
		        snapshot_milli, snapshot_legs, snapshot_intentions
		 FROM app.season_close_runs WHERE season_key = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var stored runRow
	err := q.QueryRow(ctx, query, season).Scan(
		&stored.view.Season, &stored.view.Generation, &stored.view.LeaseOwner, &stored.view.LeasedUntil,
		&stored.view.State, &stored.view.CutoffAt, &stored.view.LastHoldKey, &stored.view.LastEscrowKey,
		&stored.view.Drained, &stored.view.Blocked, &stored.view.SnapshotMilli, &stored.view.SnapshotLegs,
		&stored.view.SnapshotIntentions)
	if err != nil {
		return runRow{}, fmt.Errorf("read close run: %w", err)
	}
	stored.leasedUntil = stored.view.LeasedUntil
	stored.state = stored.view.State
	return stored, nil
}

// holdBatch lists the next releasable holds of one book in key
// order, skipping rows a concurrent worker already locked. Ordering
// batches determinism only: commit order is never inferred from ids.
func holdBatch(ctx context.Context, tx pgx.Tx, season, after string, limit int) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT id::text FROM app.economy_holds
		 WHERE season_key = $1 AND status IN ('active', 'expired') AND id::text > $2
		 ORDER BY id LIMIT $3 FOR UPDATE SKIP LOCKED`, season, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list holds batch: %w", err)
	}
	defer rows.Close()
	var holds []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan hold batch: %w", err)
		}
		holds = append(holds, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read hold batch: %w", err)
	}
	return holds, nil
}

// escrowItem is one scanned escrow with its derived status name.
type escrowItem struct {
	key    string
	buyer  string
	status string
}

// escrowBatch lists the next escrows of one book in key order with
// each status derived from its newest settlement step, skipping rows
// a concurrent worker already locked.
func escrowBatch(ctx context.Context, tx pgx.Tx, season, after string, limit int) ([]escrowItem, error) {
	rows, err := tx.Query(ctx,
		`SELECT c.contract_key, c.buyer_id::text,
		        COALESCE((SELECT s.action FROM app.commerce_settlements s
		                  WHERE s.contract_id = c.id ORDER BY s.posted_at DESC, s.id DESC LIMIT 1), 'funded')
		 FROM app.commerce_contracts c
		 WHERE c.season_key = $1 AND c.contract_key > $2
		 ORDER BY c.contract_key LIMIT $3 FOR UPDATE OF c SKIP LOCKED`, season, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list escrows batch: %w", err)
	}
	defer rows.Close()
	var items []escrowItem
	for rows.Next() {
		var item escrowItem
		if err := rows.Scan(&item.key, &item.buyer, &item.status); err != nil {
			return nil, fmt.Errorf("scan escrow batch: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read escrow batch: %w", err)
	}
	return items, nil
}

// escrowStatusName maps a settlement action to the mirrored drain
// status vocabulary.
func escrowStatusName(action string) string {
	switch action {
	case "accept":
		return seasondomain.CloseEscrowAccepted
	case "release", "resolve-release":
		return seasondomain.CloseEscrowReleased
	case "refund", "resolve-refund":
		return seasondomain.CloseEscrowRefunded
	case "expire":
		return seasondomain.CloseEscrowExpired
	case "funded":
		return seasondomain.CloseEscrowFunded
	default:
		return action
	}
}

// drainScan is one fenced batch scan: the holds to release and the
// escrows to classify with the cursors they were read from.
type drainScan struct {
	holds       []string
	items       []escrowItem
	holdsRemain bool
	itemsRemain bool
}

// scanBatch reads one fenced batch in a single transaction: holds
// first, then escrows with the remaining budget. A nil port with
// pending holds is an explicit error before anything is released.
func scanBatch(ctx context.Context, pool poolBeginner, drain seasondomain.CloseDrain, limit int, releaseHold application.ReleaseHoldFunc) (drainScan, runRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return drainScan{}, runRow{}, fmt.Errorf("begin drain scan: %w", err)
	}
	defer tx.Rollback(ctx)
	stored, err := drainFence(ctx, tx, drain)
	if err != nil {
		return drainScan{}, runRow{}, err
	}
	var scan drainScan
	scan.holds, err = holdBatch(ctx, tx, drain.Season, stored.view.LastHoldKey, limit)
	if err != nil {
		return drainScan{}, runRow{}, err
	}
	if len(scan.holds) > 0 && releaseHold == nil {
		return drainScan{}, runRow{}, fmt.Errorf("seasons: drain needs a hold port: %w", seasondomain.ErrInvalidSeason)
	}
	remain := limit - len(scan.holds)
	if remain > 0 {
		scan.items, err = escrowBatch(ctx, tx, drain.Season, stored.view.LastEscrowKey, remain)
		if err != nil {
			return drainScan{}, runRow{}, err
		}
	}
	scan.holdsRemain = len(scan.holds) == limit
	scan.itemsRemain = remain > 0 && len(scan.items) == remain
	if err := tx.Commit(ctx); err != nil {
		return drainScan{}, runRow{}, fmt.Errorf("commit drain scan: %w", err)
	}
	return scan, stored, nil
}

// poolBeginner is the minimum to open scan and cursor transactions.
type poolBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// DrainBatch releases one batch of active holds through the port and
// classifies one batch of escrows, advancing the cursors in the same
// transaction. Holds already settled replay as drained without
// failing, so crash resume never double-pays; non-terminal escrows
// block with custody preserved. Stale generations and foreign owners
// refuse before any row is read.
func (c *Closer) DrainBatch(ctx context.Context, req application.DrainRequest, releaseHold application.ReleaseHoldFunc) (application.BatchView, error) {
	drain := seasondomain.CloseDrain{Season: req.Season, Generation: req.Generation, Owner: req.Owner}
	if err := drain.Valid(); err != nil {
		return application.BatchView{}, err
	}
	if req.Limit < 1 {
		return application.BatchView{}, seasondomain.ErrInvalidSeason
	}
	scan, stored, err := scanBatch(ctx, c.pool, drain, req.Limit, releaseHold)
	if err != nil {
		return application.BatchView{}, err
	}

	var batch application.BatchView
	lastHold := stored.view.LastHoldKey
	for _, hold := range scan.holds {
		// Every processed hold counts drained exactly once: released
		// now or replayed settled, never twice.
		if _, err := releaseHold(ctx, hold, drain); err != nil {
			return application.BatchView{}, err
		}
		batch.Processed++
		batch.Drained++
		lastHold = hold
	}
	lastEscrow := stored.view.LastEscrowKey
	for _, item := range scan.items {
		if seasondomain.ClassifyCloseEscrow(escrowStatusName(item.status)) == seasondomain.CloseNoEffect {
			batch.Drained++
		} else {
			batch.Blocked++
		}
		batch.Processed++
		lastEscrow = item.key
	}
	if err := c.commitCursors(ctx, drain, stored, cursorProgress{
		holdKey: lastHold, escrowKey: lastEscrow,
		drained: batch.Drained, blocked: batch.Blocked,
	}); err != nil {
		return application.BatchView{}, err
	}
	batch.HoldsDone = !scan.holdsRemain
	batch.EscrowsDone = !scan.itemsRemain
	return batch, nil
}

// cursorProgress is one batch checkpoint: the forward-most keys
// with the batch counters.
type cursorProgress struct {
	holdKey   string
	escrowKey string
	drained   int64
	blocked   int64
}

// commitCursors advances the checkpoint with the fence re-checked:
// stale workers advance nothing. Counters are progress hints while
// Seal decides by live scan.
func (c *Closer) commitCursors(ctx context.Context, drain seasondomain.CloseDrain, stored runRow, progress cursorProgress) error {
	next, err := seasondomain.AdvanceCursor(seasondomain.CloseRun{
		LastHoldKey: stored.view.LastHoldKey, LastEscrowKey: stored.view.LastEscrowKey,
		Drained: stored.view.Drained, Blocked: stored.view.Blocked,
	}, forwardKey(stored.view.LastHoldKey, progress.holdKey), forwardKey(stored.view.LastEscrowKey, progress.escrowKey), progress.drained, progress.blocked)
	if err != nil {
		return err
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin cursor transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := drainFence(ctx, tx, drain); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE app.season_close_runs
		 SET last_hold_key = $2, last_escrow_key = $3,
		     drained = $4, blocked = $5, updated_at = now()
		 WHERE season_key = $1 AND generation = $6 AND lease_owner = $7 AND state <> 'sealed'`,
		drain.Season, next.LastHoldKey, next.LastEscrowKey, next.Drained, next.Blocked,
		drain.Generation, drain.Owner)
	if err != nil {
		return fmt.Errorf("advance close cursor: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return seasondomain.ErrStaleGeneration
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit close cursor: %w", err)
	}
	return nil
}

// forwardKey keeps the forward-most cursor of a batch side: an empty
// batch keeps the stored cursor.
func forwardKey(stored, batch string) string {
	if batch == "" || batch < stored {
		return stored
	}
	return batch
}
