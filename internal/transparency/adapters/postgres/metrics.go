// Package postgres derives privacy-safe platform metrics from source
// rows (P14-T02). Every figure is rebuilt on read: the adapter holds no
// materialized table, so reconstruction from the same sources always
// agrees. Only integer counts leave this adapter.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/application"
)

// Repository implements the transparency application port using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *postgres.Queries
}

var _ application.MetricsSource = (*Repository)(nil)

// NewRepository creates a PostgreSQL repository adapter for transparency.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: postgres.New(pool),
	}
}

// RawCounts reads one atomic snapshot of every source family inside the
// half-open window. The read mutates nothing.
func (r *Repository) RawCounts(ctx context.Context, start, end time.Time) (*application.RawCounts, error) {
	row, err := r.queries.GetTransparencyMetrics(ctx, postgres.GetTransparencyMetricsParams{
		PublishedAt:   pgtype.Timestamptz{Time: start.UTC(), Valid: true},
		PublishedAt_2: pgtype.Timestamptz{Time: end.UTC(), Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("derive transparency metrics: %w", err)
	}

	return &application.RawCounts{
		EligibleAccounts:        row.EligibleAccounts,
		ArenasPublished:         row.ArenasPublished,
		ArenasClosed:            row.ArenasClosed,
		ArenasRestricted:        row.ArenasRestricted,
		ArenasRemoved:           row.ArenasRemoved,
		ArgumentsPublished:      row.ArgumentsPublished,
		ArgumentsWithdrawn:      row.ArgumentsWithdrawn,
		PositionChanges:         row.PositionChanges,
		AttributionsValid:       row.AttributionsValid,
		AttributionsInvalidated: row.AttributionsInvalidated,
		InfluencedAuthors:       row.InfluencedAuthors,
		InkFreeGranted:          row.InkFreeGranted,
		InkFreeExpired:          row.InkFreeExpired,
		InkFreeConsumed:         row.InkFreeConsumed,
		InkPurchasedGranted:     row.InkPurchasedGranted,
		InkPurchasedConsumed:    row.InkPurchasedConsumed,
		InkRefunded:             row.InkRefunded,
		InkAdminAdjusted:        row.InkAdminAdjusted,
		PassesPurchaseGranted:   row.PassesPurchaseGranted,
		PassesMemberGranted:     row.PassesMemberGranted,
		PassesConsumed:          row.PassesConsumed,
		ReportsFiled:            row.ReportsFiled,
		ActionsRecorded:         row.ActionsRecorded,
		AppealsFiled:            row.AppealsFiled,
		AppealsReversed:         row.AppealsReversed,
	}, nil
}
