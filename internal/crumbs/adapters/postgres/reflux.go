package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/crumbs/application"
	crumbsdomain "github.com/AlexandreZanata/Regnovum/internal/crumbs/domain"
)

// Regular reflux surface of the crumbs adapter (P38-T02): own
// services plus tithe minus corresponding refunds, summed by the
// database posted instant inside one [start, end) window. Reads name
// only the four regular origins; sales, seizures, death,
// corrections, vault moves, gifts, holds and unknown transfers never
// enter, fail-closed by construction.
var _ application.RefluxRepository = (*RefluxRepository)(nil)

// RefluxRepository sums classified regular reflux against
// PostgreSQL. It writes nothing.
type RefluxRepository struct {
	pool *pgxpool.Pool
}

// NewRefluxRepository builds the repository with explicit wiring.
func NewRefluxRepository(pool *pgxpool.Pool) (*RefluxRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("crumbs: reflux repository needs a pool")
	}
	return &RefluxRepository{pool: pool}, nil
}

// RegularReflux nets the regular Treasury reflux posted inside
// [start, end): publication charges plus liquidating tithes minus
// metering reversals and tithe reversals.
func (r *RefluxRepository) RegularReflux(ctx context.Context, start, end time.Time) (*application.RefluxResult, error) {
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return nil, crumbsdomain.ErrInvalidReflux
	}
	services, err := sumPublications(ctx, r.pool, start, end)
	if err != nil {
		return nil, err
	}
	tithe, err := sumTitheLegs(ctx, r.pool, start, end)
	if err != nil {
		return nil, err
	}
	meteringBack, err := sumMeteringRefunds(ctx, r.pool, start, end)
	if err != nil {
		return nil, err
	}
	titheBack, err := sumTitheReversals(ctx, r.pool, start, end)
	if err != nil {
		return nil, err
	}
	return &application.RefluxResult{
		ServiceCharges: services, Tithe: tithe,
		MeteringReversals: meteringBack, TitheReversals: titheBack,
		Net: services + tithe - meteringBack - titheBack,
	}, nil
}

// refluxQuerier covers pool and transaction reads for the reflux
// lookup: single-row only, never writing.
type refluxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// sumPublications totals settled service charges posted in the
// window: the platform's own service revenue.
func sumPublications(ctx context.Context, q refluxQuerier, start, end time.Time) (int64, error) {
	var total int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_milli), 0) FROM app.metering_publications
		 WHERE posted_at >= $1 AND posted_at < $2`, start, end).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum publications: %w", err)
	}
	return total, nil
}

// sumTitheLegs totals Treasury credits under liquidating settlement
// transfers posted in the window: release and resolve-release pay
// the provider net with floor(10%) to the Treasury.
func sumTitheLegs(ctx context.Context, q refluxQuerier, start, end time.Time) (int64, error) {
	var total int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(e.amount_milli), 0)
		 FROM app.commerce_settlements s
		 JOIN app.economy_entries e ON e.transfer_id = s.transfer_id
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE s.action IN ('release', 'resolve-release')
		   AND s.posted_at >= $1 AND s.posted_at < $2
		   AND c.kind = 'treasury' AND e.direction = 'credit'`, start, end).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum tithe legs: %w", err)
	}
	return total, nil
}

// sumMeteringRefunds totals publication compensations posted in the
// window: the whole reversed charge leaves the Treasury.
func sumMeteringRefunds(ctx context.Context, q refluxQuerier, start, end time.Time) (int64, error) {
	var total int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_milli), 0) FROM app.metering_refunds
		 WHERE posted_at >= $1 AND posted_at < $2`, start, end).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum metering refunds: %w", err)
	}
	return total, nil
}

// sumTitheReversals totals service tithe reversals posted in the
// window: only the Treasury share of each compensation leaves.
func sumTitheReversals(ctx context.Context, q refluxQuerier, start, end time.Time) (int64, error) {
	var total int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(tithe_reversal_milli), 0) FROM app.commerce_service_refunds
		 WHERE posted_at >= $1 AND posted_at < $2`, start, end).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum tithe reversals: %w", err)
	}
	return total, nil
}
