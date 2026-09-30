package postgres

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Tithe settlement helpers (P37-T04): formal trade releases split
// the liquidated value in the same transaction into floor(10%) for
// the Treasury and the rest for the provider, in milliINK. Gifts
// never route here, unreleased escrow never splits and buyer
// refunds return whole with no tithe.
//
// The Treasury home is the structural `treasury/main` custody that
// Genesis provisions: no vault share is ratified here (Q23 stays
// PENDENTE), so the mechanics park the tithe in the genesis home
// the metering charges already use.

// PayoutLegs is one payout under a single transfer: the escrow
// debit of the full amount with the beneficiary credit, plus the
// Treasury credit when a positive tithe exists.
type PayoutLegs struct {
	TransferID string
	EscrowID   string
	ToID       string
	TreasuryID string
	Amount     int64
	Tithe      int64
	Net        int64
}

// payoutPlan is the resolved beneficiary of one settlement with
// the split computed for provider payments.
type payoutPlan struct {
	toID       string
	treasuryID string
	tithe      int64
	net        int64
}

// resolveTitheTreasury maps the structural Treasury home custody.
// Absence fails before any lock is taken or leg written.
func resolveTitheTreasury(ctx context.Context, tx pgx.Tx) (string, error) {
	id, found, err := platformpg.ResolveLedgerCustody(ctx, tx, "treasury", "main")
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("resolve tithe treasury: custody treasury/main missing")
	}
	return id, nil
}

// lockPayoutCustodies pins the escrow, the beneficiary and, when a
// positive tithe exists, the Treasury home in canonical id order.
// Canonical ordering keeps concurrent settlements from
// deadlocking: every transaction asks for the same rows in the
// same sequence. A zero tithe locks exactly the two payout
// custodies, so dust releases contend no more than before.
func lockPayoutCustodies(ctx context.Context, tx pgx.Tx, escrowID, toID, treasuryID string, tithe int64) error {
	if tithe <= 0 || treasuryID == "" || treasuryID == escrowID || treasuryID == toID {
		return platformpg.LockLedgerCustodies(ctx, tx, escrowID, toID)
	}
	ids := []string{escrowID, toID, treasuryID}
	sort.Strings(ids)
	if _, err := tx.Exec(ctx,
		`SELECT id FROM app.economy_custodies WHERE id IN ($1::uuid, $2::uuid, $3::uuid) ORDER BY id FOR UPDATE`,
		ids[0], ids[1], ids[2]); err != nil {
		return fmt.Errorf("lock tithe custodies: %w", err)
	}
	return nil
}

// recordPayoutLegs writes the payout legs under one transfer. The
// two outputs always sum to the paid value, so S never moves. A
// zero tithe writes exactly the historical pair, never a zero leg
// (the journal CHECK refuses 0).
func recordPayoutLegs(ctx context.Context, tx pgx.Tx, legs PayoutLegs) error {
	if legs.Tithe <= 0 {
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
			 VALUES ($1::uuid, $2::uuid, 'debit', $4), ($1::uuid, $3::uuid, 'credit', $4)`,
			legs.TransferID, legs.EscrowID, legs.ToID, legs.Amount); err != nil {
			return fmt.Errorf("record escrow legs: %w", err)
		}
		return nil
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4), ($1::uuid, $3::uuid, 'credit', $5), ($1::uuid, $6::uuid, 'credit', $7)`,
		legs.TransferID, legs.EscrowID, legs.ToID, legs.Amount, legs.Net, legs.TreasuryID, legs.Tithe); err != nil {
		return fmt.Errorf("record tithe legs: %w", err)
	}
	return nil
}
