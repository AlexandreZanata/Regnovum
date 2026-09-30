package postgres

import (
	"context"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.StockRepository = (*Repository)(nil)

// ReadStock resolves one Treasury vault position without moving value:
// the journal balance of the vault and the amount its active
// commitments still lock. Vaults never opened read zero with zero
// locked, and obligation custodies never enter either number. It is a
// read: asking never mints, freezes or moves anything.
func (r *Repository) ReadStock(ctx context.Context, vault domain.TreasuryVault) (balance, committed int64, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_custodies c
		 LEFT JOIN app.economy_entries e ON e.custody_id = c.id
		 WHERE c.kind = 'treasury' AND c.label = $1`,
		vault.String()).Scan(&balance)
	if err != nil {
		return 0, 0, fmt.Errorf("read vault %q: %w", vault, err)
	}
	err = r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(h.amount_milli), 0)
		 FROM app.economy_holds h
		 JOIN app.economy_custodies c ON c.id = h.owner_custody_id
		 WHERE c.kind = 'treasury' AND c.label = $1 AND h.status = 'active'`,
		vault.String()).Scan(&committed)
	if err != nil {
		return 0, 0, fmt.Errorf("read committed %q: %w", vault, err)
	}
	return balance, committed, nil
}
