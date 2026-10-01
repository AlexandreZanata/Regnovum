package postgres

import (
	"context"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.GrantPolicyRepository = (*Repository)(nil)

// GenesisHappened reports whether the creation event is attested in one
// book. It is a read: asking never mints, freezes or moves anything.
// Member never mints S: this answer only observes the book.
func (r *Repository) GenesisHappened(ctx context.Context, season domain.SeasonKey) (bool, error) {
	var attested bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app.economy_genesis WHERE season_key = $1)`, season.String()).Scan(&attested)
	if err != nil {
		return false, fmt.Errorf("check genesis attestation: %w", err)
	}
	return attested, nil
}

// TreasuryStock returns the Treasury journal balance of one book:
// credits minus debits of the main Treasury custody in that book.
// It is a read.
func (r *Repository) TreasuryStock(ctx context.Context, season domain.SeasonKey) (domain.MilliInk, error) {
	var balance int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'treasury' AND c.label = 'main' AND c.season_key = $1 AND e.season_key = $1`, season.String()).Scan(&balance)
	if err != nil {
		return domain.MilliInk{}, fmt.Errorf("read treasury stock: %w", err)
	}
	return domain.NewMilliInk(balance)
}
