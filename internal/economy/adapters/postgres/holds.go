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

// holdRow is one locked hold with everything its settlement needs.
type holdRow struct {
	id          string
	ownerID     string
	holdCustody string
	millis      int64
	status      string
	expiresAt   time.Time
}

// lockHold pins one hold row and reads its settlement state.
func lockHold(ctx context.Context, tx pgx.Tx, holdID string) (holdRow, error) {
	var hold holdRow
	err := tx.QueryRow(ctx,
		`SELECT id::text, owner_custody_id::text, hold_custody_id::text, amount_milli, status, expires_at
		 FROM app.economy_holds WHERE id = $1::uuid FOR UPDATE`,
		holdID).Scan(&hold.id, &hold.ownerID, &hold.holdCustody, &hold.millis, &hold.status, &hold.expiresAt)
	if err != nil {
		return holdRow{}, domain.ErrHoldNotFound
	}
	return hold, nil
}

// moveLegs writes one debit/credit pair inside the caller transaction.
func moveLegs(ctx context.Context, tx pgx.Tx, transferID, fromID, toID string, millis int64) error {
	for _, leg := range []struct {
		custody   string
		direction string
	}{
		{fromID, "debit"},
		{toID, "credit"},
	} {
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
			 VALUES ($1::uuid, $2::uuid, $3, $4)`,
			transferID, leg.custody, leg.direction, millis); err != nil {
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
// custody, recording purpose and deadline in the same transaction. The
// legs leave the journal at once, so whatever stays behind is spendable
// and whatever moved can only return by release or capture.
func (r *Repository) Reserve(ctx context.Context, ownerKind domain.CustodyKind, ownerLabel string, purpose domain.HoldPurpose, amount domain.MilliInk, expiresAt time.Time) (*application.HoldView, error) {
	if !ownerKind.CanSpend() {
		return nil, domain.ErrUnauthorizedCustody
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reserve transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	ownerID, err := resolveCustody(ctx, tx, ownerKind.String(), ownerLabel)
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
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('escrow', $1) RETURNING id::text`,
		"hold-"+holdID).Scan(&holdCustody); err != nil {
		return nil, fmt.Errorf("create hold custody: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_holds (id, owner_custody_id, hold_custody_id, amount_milli, purpose, expires_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6)`,
		holdID, ownerID, holdCustody, amount.Millis(), purpose.String(), expiresAt); err != nil {
		return nil, fmt.Errorf("record hold: %w", err)
	}
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	if err := moveLegs(ctx, tx, transferID, ownerID, holdCustody, amount.Millis()); err != nil {
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

	hold, err := lockHold(ctx, tx, holdID)
	if err != nil {
		return nil, err
	}
	if hold.status != "active" && hold.status != "expired" {
		return nil, domain.ErrHoldState
	}
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	if err := moveLegs(ctx, tx, transferID, hold.holdCustody, toCustodyID, hold.millis); err != nil {
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

// Capture pays a hold in full to a beneficiary custody. Two capturers
// never both win: the row lock serializes them, and the loser finds a
// settled hold.
func (r *Repository) Capture(ctx context.Context, holdID, toKind, toLabel string) (*application.HoldView, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin capture probe: %w", err)
	}
	defer tx.Rollback(ctx)

	toID, err := resolveCustody(ctx, tx, toKind, toLabel)
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

	var hold holdRow
	var lapsed bool
	err = tx.QueryRow(ctx,
		`SELECT id::text, owner_custody_id::text, hold_custody_id::text, amount_milli, status,
		        expires_at, now() >= expires_at
		 FROM app.economy_holds WHERE id = $1::uuid FOR UPDATE`,
		holdID).Scan(&hold.id, &hold.ownerID, &hold.holdCustody, &hold.millis, &hold.status, &hold.expiresAt, &lapsed)
	if err != nil {
		return nil, domain.ErrHoldNotFound
	}
	if hold.status != "active" {
		return nil, domain.ErrHoldState
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
