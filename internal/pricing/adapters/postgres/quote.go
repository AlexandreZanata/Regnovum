package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/application"
	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// Repository persists pricing snapshots. Reads and writes touch the
// pricing registry only: the INK journal is never read here, and fiat
// amounts never enter these rows.
type Repository struct {
	pool *pgxpool.Pool
}

var _ application.QuoteRepository = (*Repository)(nil)

// NewRepository creates a pricing repository over the pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Store persists one sealed snapshot with its sightings in a single
// transaction: the quote row and every sighting commit together, or
// nothing is stored at all. Sightings arrive already sealed by the
// use case; the adapter binds them to rows without re-judging them.
// Season travels beside the seal: seasonal books cap the expiry by
// the book end, verified here against the registry before any write.
func (r *Repository) Store(ctx context.Context, quote domain.Quote) (string, error) {
	season := quote.Season
	if season.String() == "" {
		season = domain.SeasonKey(domain.CompatSeasonKey)
	}
	if season.String() != domain.CompatSeasonKey {
		var endsAt time.Time
		if err := r.pool.QueryRow(ctx,
			`SELECT ends_at FROM app.seasons WHERE season_key = $1`, season.String()).Scan(&endsAt); err != nil {
			return "", fmt.Errorf("read season end: %w", err)
		}
		if quote.ExpiresAt.After(endsAt.UTC()) {
			return "", fmt.Errorf("quote expiry passes the book end: %w", domain.ErrInvalidQuote)
		}
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin store quote transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var id string
	err = tx.QueryRow(ctx,
		`INSERT INTO app.pricing_quotes (price_minor, observed_at, accepted_at, expires_at, quote_hash, season_key)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
		quote.Price.Int64(), quote.ObservedAt, quote.AcceptedAt, quote.ExpiresAt, quote.Hash, season.String()).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert quote: %w", err)
	}
	for _, sighting := range quote.Sightings {
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.pricing_quote_sources (quote_id, source, price_minor, observed_at, payload_hash)
			 VALUES ($1::uuid, $2, $3, $4, $5)`,
			id, sighting.Source.String(), sighting.Price.Int64(), sighting.ObservedAt, sighting.PayloadHash); err != nil {
			return "", fmt.Errorf("insert quote sighting: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit store quote: %w", err)
	}
	return id, nil
}

// Find resolves one stored snapshot with its sightings in source
// order, or reports its absence. The frozen source set serves even
// after a source withdraws: reads never re-resolve the registry.
func (r *Repository) Find(ctx context.Context, id string) (*domain.Quote, error) {
	var quote domain.Quote
	var price int64
	var seasonKey string
	err := r.pool.QueryRow(ctx,
		`SELECT price_minor, observed_at, accepted_at, expires_at, quote_hash, season_key
		 FROM app.pricing_quotes WHERE id = $1::uuid`,
		id).Scan(&price, &quote.ObservedAt, &quote.AcceptedAt, &quote.ExpiresAt, &quote.Hash, &seasonKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read quote: %w", err)
	}
	millis, err := domain.NewPriceMinor(price)
	if err != nil {
		return nil, fmt.Errorf("stored quote holds %d minor: %w", price, err)
	}
	quote.ID = id
	quote.Price = millis
	season, err := domain.ParseSeasonKey(seasonKey)
	if err != nil {
		return nil, fmt.Errorf("stored quote book %q: %w", seasonKey, err)
	}
	quote.Season = season
	rows, err := r.pool.Query(ctx,
		`SELECT source, price_minor, observed_at, payload_hash
		 FROM app.pricing_quote_sources WHERE quote_id = $1::uuid ORDER BY source`,
		id)
	if err != nil {
		return nil, fmt.Errorf("read quote sightings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var sightingPrice int64
		var observed time.Time
		var hash string
		if err := rows.Scan(&raw, &sightingPrice, &observed, &hash); err != nil {
			return nil, fmt.Errorf("scan quote sighting: %w", err)
		}
		source, err := domain.ParseSourceID(raw)
		if err != nil {
			return nil, fmt.Errorf("stored sighting source %q: %w", raw, err)
		}
		amount, err := domain.NewPriceMinor(sightingPrice)
		if err != nil {
			return nil, fmt.Errorf("stored sighting holds %d minor: %w", sightingPrice, err)
		}
		quote.Sightings = append(quote.Sightings, domain.Observation{
			Source:      source,
			Price:       amount,
			ObservedAt:  observed.UTC(),
			PayloadHash: hash,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate quote sightings: %w", err)
	}
	if err := quote.VerifyHash(); err != nil {
		return nil, fmt.Errorf("stored quote fails its seal: %w", err)
	}
	return &quote, nil
}
