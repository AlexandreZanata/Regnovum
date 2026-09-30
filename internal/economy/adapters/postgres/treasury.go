package postgres

import (
	"context"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.TreasuryRepository = (*Repository)(nil)

// ReadTreasuryVaults resolves one position per Treasury vault and the
// Treasury total recomputed independently of the per-vault sums: the
// vaults come from a grouped reading over the closed label list, the
// total from an ungrouped one, so agreement proves exclusivity instead
// of restating one query. Empty vaults read zero with zero legs. It is
// a read: asking never mints, freezes or moves anything, and obligation
// custodies (escrow, contract, title) never enter either reading.
func (r *Repository) ReadTreasuryVaults(ctx context.Context) ([]application.TreasuryVaultsView, int64, error) {
	vaults := []application.TreasuryVaultsView{}
	for _, vault := range domain.AllTreasuryVaults() {
		var millis, legs int64
		err := r.pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0),
			        COUNT(e.id)
			 FROM app.economy_custodies c
			 LEFT JOIN app.economy_entries e ON e.custody_id = c.id
			 WHERE c.kind = 'treasury' AND c.label = $1`,
			vault.String()).Scan(&millis, &legs)
		if err != nil {
			return nil, 0, fmt.Errorf("read vault %q: %w", vault, err)
		}
		vaults = append(vaults, application.TreasuryVaultsView{Vault: vault, Millis: millis, Legs: legs})
	}
	var total int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'treasury'`).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("read treasury total: %w", err)
	}
	return vaults, total, nil
}
