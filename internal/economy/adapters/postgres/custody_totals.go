package postgres

import (
	"context"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
)

var _ application.CustodyTotalsRepository = (*Repository)(nil)

// ReadCustodyTotals re-derives every custody aggregate from the
// journal without writing anything: supply from the legs, Treasury
// partitions through the same grouped reading as the vault report,
// circulation from holder custodies, locks from active holds and the
// frozen flag from the mode row. No derived table is ever read, so a
// fresh call after any settlement reflects the journal as written;
// the use-case cache bounds how often readers pay for it. Obligation
// custodies never enter any Treasury number, and no identifier beside
// the closed vault labels leaves this function.
func (r *Repository) ReadCustodyTotals(ctx context.Context) (*application.CustodyTotalsReading, error) {
	vaults, total, err := r.ReadTreasuryVaults(ctx)
	if err != nil {
		return nil, err
	}
	supply, err := r.readTotalsSupply(ctx)
	if err != nil {
		return nil, err
	}
	circulation, holders, err := r.readTotalsCirculation(ctx)
	if err != nil {
		return nil, err
	}
	locked, holds, err := r.readTotalsLocked(ctx)
	if err != nil {
		return nil, err
	}
	frozen, err := isFrozen(ctx, r.pool)
	if err != nil {
		return nil, err
	}
	reading := &application.CustodyTotalsReading{
		SupplyMillis:         supply,
		TreasuryMillis:       total,
		Vaults:               make([]application.VaultTotal, 0, len(vaults)),
		CirculationMillis:    circulation,
		CirculationCustodies: holders,
		LockedMillis:         locked,
		LockedHolds:          holds,
		Frozen:               frozen,
	}
	for _, position := range vaults {
		reading.Vaults = append(reading.Vaults, application.VaultTotal{
			Vault:  position.Vault,
			Millis: position.Millis,
		})
		if position.Vault.String() == "sovereign_reserve" {
			reading.ReserveMillis = position.Millis
		}
	}
	return reading, nil
}

// readTotalsSupply recomputes the supply as credits minus debits over
// the whole journal: the Genesis credit is the single lawful excess.
func (r *Repository) readTotalsSupply(ctx context.Context) (int64, error) {
	var credits, debits int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_milli) FILTER (WHERE direction = 'credit'), 0),
		        COALESCE(SUM(amount_milli) FILTER (WHERE direction = 'debit'), 0)
		 FROM app.economy_entries`).Scan(&credits, &debits)
	if err != nil {
		return 0, fmt.Errorf("sum totals supply: %w", err)
	}
	return credits - debits, nil
}

// readTotalsCirculation sums holder custodies with their distinct
// count for the low-count rule. Holder labels never leave: only the
// amount and the count travel.
func (r *Repository) readTotalsCirculation(ctx context.Context) (millis, holders int64, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0),
		        COUNT(DISTINCT c.id)
		 FROM app.economy_custodies c
		 LEFT JOIN app.economy_entries e ON e.custody_id = c.id
		 WHERE c.kind = 'user'`).Scan(&millis, &holders)
	if err != nil {
		return 0, 0, fmt.Errorf("sum totals circulation: %w", err)
	}
	return millis, holders, nil
}

// readTotalsLocked sums active holds with their count for the
// low-count rule. Purposes and owners never leave: only the amount
// and the count travel.
func (r *Repository) readTotalsLocked(ctx context.Context) (millis, holds int64, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_milli), 0), COUNT(*)
		 FROM app.economy_holds WHERE status = 'active'`).Scan(&millis, &holds)
	if err != nil {
		return 0, 0, fmt.Errorf("sum totals locks: %w", err)
	}
	return millis, holds, nil
}
