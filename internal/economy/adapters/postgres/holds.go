package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.HoldsRepository = (*Repository)(nil)

// holdRow is one locked hold with everything its settlement needs,
// including the book every leg and destination must stay inside.
type holdRow struct {
	id          string
	ownerID     string
	holdCustody string
	season      string
	millis      int64
	status      string
	expiresAt   time.Time
}

// lockHold pins one hold row and reads its settlement state.
func lockHold(ctx context.Context, tx pgx.Tx, holdID string) (holdRow, error) {
	var hold holdRow
	err := tx.QueryRow(ctx,
		`SELECT id::text, owner_custody_id::text, hold_custody_id::text, season_key, amount_milli, status, expires_at
		 FROM app.economy_holds WHERE id = $1::uuid FOR UPDATE`,
		holdID).Scan(&hold.id, &hold.ownerID, &hold.holdCustody, &hold.season, &hold.millis, &hold.status, &hold.expiresAt)
	if err != nil {
		return holdRow{}, domain.ErrHoldNotFound
	}
	return hold, nil
}

// legMove is one debit/credit pair of one book: the transfer, the
// endpoints, the amount and the season every leg stays inside.
type legMove struct {
	transferID string
	fromID     string
	toID       string
	millis     int64
	season     string
}

// moveLegs writes one debit/credit pair of one book inside the caller
// transaction. Legs never cross books: the composite key refuses a
// custody of another book.
func moveLegs(ctx context.Context, tx pgx.Tx, move legMove) error {
	for _, leg := range []struct {
		custody   string
		direction string
	}{
		{move.fromID, "debit"},
		{move.toID, "credit"},
	} {
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
			 VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
			move.transferID, leg.custody, leg.direction, move.millis, move.season); err != nil {
			return fmt.Errorf("record %s leg: %w", leg.direction, err)
		}
	}
	return nil
}

// holdView renders one recorded or settled hold. Amounts stored by CHECK
// are always in range, so rendering cannot fail on delivered rows.
func holdView(hold holdRow, status string) (*application.HoldView, error) {
	amount, err := domain.NewMilliInk(hold.millis)
	if err != nil {
		return nil, fmt.Errorf("hold carries %d milliINK: %w", hold.millis, err)
	}
	return &application.HoldView{
		HoldID: hold.id, OwnerCustodyID: hold.ownerID, HoldCustodyID: hold.holdCustody,
		Amount: amount, Status: status, ExpiresAt: hold.expiresAt,
	}, nil
}

// Reserve locks value out of the owner custody into a dedicated hold
// custody of one book, recording purpose and deadline in the same
// transaction. The legs leave the journal at once, so whatever stays
// behind is spendable and whatever moved can only return by release
// or capture inside the same book.
func (r *Repository) Reserve(ctx context.Context, reservation application.HoldReservation) (*application.HoldView, error) {
	ownerKind, ownerLabel, purpose, amount := reservation.OwnerKind, reservation.OwnerLabel, reservation.Purpose, reservation.Amount
	season, expiresAt := reservation.Season, reservation.ExpiresAt
	if !ownerKind.CanSpend() {
		return nil, domain.ErrUnauthorizedCustody
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reserve transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Frozen books open no holds.
	if err := requireUnfrozen(ctx, tx); err != nil {
		return nil, err
	}

	// Sealed books open no holds either, judged in this same
	// transaction.
	if err := requireActiveTx(ctx, tx, season); err != nil {
		return nil, err
	}

	ownerID, err := resolveCustody(ctx, tx, ownerKind.String(), ownerLabel, season.String())
	if err != nil {
		return nil, err
	}
	if err := lockCustodies(ctx, tx, ownerID, ownerID); err != nil {
		return nil, err
	}
	balance, err := custodyBalance(ctx, tx, ownerID)
	if err != nil {
		return nil, err
	}
	if _, err := balance.Sub(amount); err != nil {
		return nil, err
	}

	var holdID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&holdID); err != nil {
		return nil, fmt.Errorf("generate hold id: %w", err)
	}
	var holdCustody string
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('escrow', $1, $2) RETURNING id::text`,
		"hold-"+holdID, season.String()).Scan(&holdCustody); err != nil {
		return nil, fmt.Errorf("create hold custody: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_holds (id, owner_custody_id, hold_custody_id, amount_milli, purpose, expires_at, season_key)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7)`,
		holdID, ownerID, holdCustody, amount.Millis(), purpose.String(), expiresAt, season.String()); err != nil {
		return nil, fmt.Errorf("record hold: %w", err)
	}
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	if err := moveLegs(ctx, tx, legMove{transferID: transferID, fromID: ownerID, toID: holdCustody, millis: amount.Millis(), season: season.String()}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reserve: %w", err)
	}
	return &application.HoldView{
		HoldID: holdID, OwnerCustodyID: ownerID, HoldCustodyID: holdCustody,
		Amount: amount, Purpose: purpose.String(), Status: "active", ExpiresAt: expiresAt,
	}, nil
}

// settleHold moves the full locked amount to the destination and closes
// the hold in one transaction. Settled holds never reopen: only active
// and expired holds leave, exactly once.
func (r *Repository) settleHold(ctx context.Context, holdID, toCustodyID, status string) (*application.HoldView, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin settle transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Frozen books settle nothing.
	if err := requireUnfrozen(ctx, tx); err != nil {
		return nil, err
	}

	hold, err := lockHold(ctx, tx, holdID)
	if err != nil {
		return nil, err
	}
	if hold.status != "active" && hold.status != "expired" {
		return nil, domain.ErrHoldState
	}
	if closed, err := bookHasStage(ctx, tx, domain.SeasonKey(hold.season), terminalStages); err != nil {
		return nil, err
	} else if closed {
		return nil, domain.ErrBookSealed
	}
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	if err := moveLegs(ctx, tx, legMove{transferID: transferID, fromID: hold.holdCustody, toID: toCustodyID, millis: hold.millis, season: hold.season}); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE app.economy_holds SET status = $2, closed_at = now() WHERE id = $1::uuid`,
		holdID, status); err != nil {
		return nil, fmt.Errorf("close hold: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit settle: %w", err)
	}
	return holdView(hold, status)
}

// Release settles a hold back to its owner in full.
func (r *Repository) Release(ctx context.Context, holdID string) (*application.HoldView, error) {
	owner, err := r.settleDestination(ctx, holdID)
	if err != nil {
		return nil, err
	}
	return r.settleHold(ctx, holdID, owner, "released")
}

// Capture pays a hold in full to a beneficiary custody of the hold's
// own book: the beneficiary resolves inside the hold's season, so a
// capture never pays across books. Two capturers never both win: the
// row lock serializes them, and the loser finds a settled hold.
func (r *Repository) Capture(ctx context.Context, holdID, toKind, toLabel string) (*application.HoldView, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin capture probe: %w", err)
	}
	defer tx.Rollback(ctx)

	var season string
	if err := tx.QueryRow(ctx,
		`SELECT season_key FROM app.economy_holds WHERE id = $1::uuid`,
		holdID).Scan(&season); err != nil {
		return nil, domain.ErrHoldNotFound
	}
	toID, err := resolveCustody(ctx, tx, toKind, toLabel, season)
	if err != nil {
		return nil, err
	}
	if err := tx.Rollback(ctx); err != nil {
		return nil, fmt.Errorf("abort capture probe: %w", err)
	}
	return r.settleHold(ctx, holdID, toID, "captured")
}

// settleDestination resolves the release destination: the hold owner,
// read without locking because settleHold locks the row itself.
func (r *Repository) settleDestination(ctx context.Context, holdID string) (string, error) {
	var owner string
	err := r.pool.QueryRow(ctx,
		`SELECT owner_custody_id::text FROM app.economy_holds WHERE id = $1::uuid`,
		holdID).Scan(&owner)
	if err != nil {
		return "", domain.ErrHoldNotFound
	}
	return owner, nil
}

// Expire marks a lapsed hold without moving any leg. Holds whose
// deadline has not passed are refused: expiry is observed, never
// anticipated.
func (r *Repository) Expire(ctx context.Context, holdID string) (*application.HoldView, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin expire transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Frozen books mark nothing: expiry waits for the resolution too.
	if err := requireUnfrozen(ctx, tx); err != nil {
		return nil, err
	}

	var hold holdRow
	var lapsed bool
	err = tx.QueryRow(ctx,
		`SELECT id::text, owner_custody_id::text, hold_custody_id::text, season_key, amount_milli, status,
		        expires_at, now() >= expires_at
		 FROM app.economy_holds WHERE id = $1::uuid FOR UPDATE`,
		holdID).Scan(&hold.id, &hold.ownerID, &hold.holdCustody, &hold.season, &hold.millis, &hold.status, &hold.expiresAt, &lapsed)
	if err != nil {
		return nil, domain.ErrHoldNotFound
	}
	if hold.status != "active" {
		return nil, domain.ErrHoldState
	}
	if closed, err := bookHasStage(ctx, tx, domain.SeasonKey(hold.season), terminalStages); err != nil {
		return nil, err
	} else if closed {
		return nil, domain.ErrBookSealed
	}
	if !lapsed {
		return nil, domain.ErrHoldNotExpired
	}
	if _, err := tx.Exec(ctx,
		`UPDATE app.economy_holds SET status = 'expired', closed_at = now() WHERE id = $1::uuid`,
		holdID); err != nil {
		return nil, fmt.Errorf("mark expired: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit expire: %w", err)
	}
	return holdView(hold, "expired")
}
