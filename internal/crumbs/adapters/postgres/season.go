package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/crumbs/application"
	crumbsdomain "github.com/AlexandreZanata/Regnovum/internal/crumbs/domain"
)

// crumbBookQuerier is the minimum to judge one crumb book from a
// pool or a transaction: reads never mutate, and sealed books stay
// readable history for R4 and audit.
type crumbBookQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// requireCrumbBookExists refuses reflux of unknown books before any
// read. Sealed books stay readable: R4 needs history after the seal,
// so closing, sealed or archived stages never block a read. The
// compat-legacy namespace exists as an explicit row and reads like
// any other book here.
func requireCrumbBookExists(ctx context.Context, q crumbBookQuerier, season crumbsdomain.SeasonKey) error {
	if _, err := crumbsdomain.ParseSeasonKey(season.String()); err != nil {
		return err
	}
	var present bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.seasons WHERE season_key = $1)`,
		season.String()).Scan(&present); err != nil {
		return fmt.Errorf("read book registry: %w", err)
	}
	if !present {
		return crumbsdomain.ErrInvalidNewcomer
	}
	return nil
}

// RegularRefluxForSeason nets the regular Treasury reflux of one
// book posted inside [start, end): own services plus tithe minus
// corresponding refunds of that book only. Refunds derive their
// book from the original publication or contract via the existing
// references, so a late refund of a previous book never feeds the
// new one: the parent book decides, never the posted week alone.
// Sealed books stay readable; unknown books refuse before any read.
func (r *RefluxRepository) RegularRefluxForSeason(ctx context.Context, season crumbsdomain.SeasonKey, start, end time.Time) (*application.RefluxResult, error) {
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return nil, crumbsdomain.ErrInvalidReflux
	}
	if err := requireCrumbBookExists(ctx, r.pool, season); err != nil {
		return nil, err
	}
	services, err := sumSeasonPublications(ctx, r.pool, season, start, end)
	if err != nil {
		return nil, err
	}
	tithe, err := sumSeasonTitheLegs(ctx, r.pool, season, start, end)
	if err != nil {
		return nil, err
	}
	meteringBack, err := sumSeasonMeteringRefunds(ctx, r.pool, season, start, end)
	if err != nil {
		return nil, err
	}
	titheBack, err := sumSeasonTitheReversals(ctx, r.pool, season, start, end)
	if err != nil {
		return nil, err
	}
	return &application.RefluxResult{
		ServiceCharges: services, Tithe: tithe,
		MeteringReversals: meteringBack, TitheReversals: titheBack,
		Net: services + tithe - meteringBack - titheBack,
	}, nil
}

// sumSeasonPublications totals settled service charges of one book
// posted in the window.
func sumSeasonPublications(ctx context.Context, q crumbBookQuerier, season crumbsdomain.SeasonKey, start, end time.Time) (int64, error) {
	var total int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_milli), 0) FROM app.metering_publications
		 WHERE season_key = $1 AND posted_at >= $2 AND posted_at < $3`,
		season.String(), start, end).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum season publications: %w", err)
	}
	return total, nil
}

// sumSeasonTitheLegs totals Treasury credits of one book under
// liquidating settlement transfers posted in the window. Both the
// leg book and the contract book must match: a cross-book
// settlement never leaks into another book net.
func sumSeasonTitheLegs(ctx context.Context, q crumbBookQuerier, season crumbsdomain.SeasonKey, start, end time.Time) (int64, error) {
	var total int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(e.amount_milli), 0)
		 FROM app.commerce_settlements s
		 JOIN app.economy_entries e ON e.transfer_id = s.transfer_id
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 JOIN app.commerce_contracts k ON k.id = s.contract_id
		 WHERE s.action IN ('release', 'resolve-release')
		   AND s.posted_at >= $1 AND s.posted_at < $2
		   AND c.kind = 'treasury' AND e.direction = 'credit'
		   AND e.season_key = $3 AND k.season_key = $3`,
		start, end, season.String()).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum season tithe legs: %w", err)
	}
	return total, nil
}

// sumSeasonMeteringRefunds totals publication compensations of one
// book posted in the window, by parent book: a refund posted in a
// new week for an old publication stays in the old book and never
// feeds the new one.
func sumSeasonMeteringRefunds(ctx context.Context, q crumbBookQuerier, season crumbsdomain.SeasonKey, start, end time.Time) (int64, error) {
	var total int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(r.amount_milli), 0)
		 FROM app.metering_refunds r
		 JOIN app.metering_publications p ON p.id = r.original_id
		 WHERE p.season_key = $1 AND r.posted_at >= $2 AND r.posted_at < $3`,
		season.String(), start, end).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum season metering refunds: %w", err)
	}
	return total, nil
}

// sumSeasonTitheReversals totals service tithe reversals of one book
// posted in the window, by contract book.
func sumSeasonTitheReversals(ctx context.Context, q crumbBookQuerier, season crumbsdomain.SeasonKey, start, end time.Time) (int64, error) {
	var total int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(r.tithe_reversal_milli), 0)
		 FROM app.commerce_service_refunds r
		 JOIN app.commerce_contracts c ON c.id = r.contract_id
		 WHERE c.season_key = $1 AND r.posted_at >= $2 AND r.posted_at < $3`,
		season.String(), start, end).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum season tithe reversals: %w", err)
	}
	return total, nil
}
