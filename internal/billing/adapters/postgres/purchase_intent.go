package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
	pricingdomain "github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// Purchase intent surface of the billing adapter (P35-T05): the buyer
// names key, account, quotation and fiat ticket, and one transaction
// seals the intent with the commercial-stock hold that backs it.
// Price, INK quantity and stock resolve on the server from the stored
// quotation and the deployment fee schedule, so a forged request can
// never mint INK or move another holder's funds. Fiat and INK books
// stay separate: this row names amounts, the INK itself moves only in
// economy legs the same transaction writes.
var _ application.PurchaseIntentRepository = (*PurchaseIntentRepository)(nil)

// PurchaseIntentRepository accepts INK purchases against PostgreSQL.
type PurchaseIntentRepository struct {
	pool     *pgxpool.Pool
	clock    ports.Clock
	schedule pricingdomain.FeeSchedule
}

// NewPurchaseIntentRepository builds the repository with explicit
// wiring: the pool, the clock judging quotation liveness and the
// server fee schedule pricing every ticket. No schedule field ever
// arrives from a buyer.
func NewPurchaseIntentRepository(pool *pgxpool.Pool, clock ports.Clock, schedule pricingdomain.FeeSchedule) (*PurchaseIntentRepository, error) {
	if pool == nil || clock == nil {
		return nil, application.ErrInvalidPurchaseIntentConfig
	}
	if !schedule.Valid() {
		return nil, application.ErrInvalidPurchaseIntentConfig
	}
	return &PurchaseIntentRepository{pool: pool, clock: clock, schedule: schedule}, nil
}

// AcceptPurchase seals one acceptance keyed idempotently by account
// and token: the stored quotation prices the ticket, the commercial
// vault covers the derived INK, and intent row plus hold legs commit
// together. Replays resolve the original acceptance untouched,
// divergent terms under one key conflict, and uncovered stock refuses
// without writing.
func (r *PurchaseIntentRepository) AcceptPurchase(ctx context.Context, request application.AcceptPurchaseRequest) (*application.PurchaseIntentResult, error) {
	account, err := pgUUIDFromAccountID(request.Account)
	if err != nil {
		return nil, application.ErrPurchaserNotFound
	}
	accountID := uuidToString(account)
	quote, err := r.readQuote(ctx, request.QuoteID)
	if err != nil {
		return nil, err
	}
	now := r.clock.Now().UTC()
	if !quote.Live(now) {
		return nil, application.ErrPurchaseQuoteExpired
	}
	terms, err := pricingdomain.QuoteTerms(request.Fiat.MinorUnits(), quote.Price, r.schedule)
	if err != nil {
		return nil, fmt.Errorf("price purchase terms: %w", err)
	}
	if replayed, err := r.lookupIntent(ctx, accountID, request, terms); err != nil || replayed != nil {
		return replayed, err
	}
	var last error
	for range 2 {
		result, err := r.createIntent(ctx, accountID, request, quote, terms)
		if err == nil {
			return result, nil
		}
		if !isIntentConflict(err) {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupIntent(ctx, accountID, request, terms); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("purchase intent unsettled after conflict: %w", last)
}

// readQuote resolves one sealed snapshot with its sightings, or
// reports its absence. The seal verifies: stored terms that fail it
// never price.
func (r *PurchaseIntentRepository) readQuote(ctx context.Context, quoteID string) (*pricingdomain.Quote, error) {
	var quote pricingdomain.Quote
	var price int64
	err := r.pool.QueryRow(ctx,
		`SELECT price_minor, observed_at, accepted_at, expires_at, quote_hash
		 FROM app.pricing_quotes WHERE id = $1::uuid`,
		quoteID).Scan(&price, &quote.ObservedAt, &quote.AcceptedAt, &quote.ExpiresAt, &quote.Hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrPurchaseQuoteNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, application.ErrPurchaseQuoteNotFound
		}
		return nil, fmt.Errorf("read quote: %w", err)
	}
	amount, err := pricingdomain.NewPriceMinor(price)
	if err != nil {
		return nil, fmt.Errorf("stored quote holds %d minor: %w", price, err)
	}
	quote.ID = quoteID
	quote.Price = amount
	rows, err := r.pool.Query(ctx,
		`SELECT source, price_minor, observed_at, payload_hash
		 FROM app.pricing_quote_sources WHERE quote_id = $1::uuid ORDER BY source`,
		quoteID)
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
		source, err := pricingdomain.ParseSourceID(raw)
		if err != nil {
			return nil, fmt.Errorf("stored sighting source %q: %w", raw, err)
		}
		units, err := pricingdomain.NewPriceMinor(sightingPrice)
		if err != nil {
			return nil, fmt.Errorf("stored sighting holds %d minor: %w", sightingPrice, err)
		}
		quote.Sightings = append(quote.Sightings, pricingdomain.Observation{
			Source:      source,
			Price:       units,
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

// lookupIntent resolves a settled acceptance without writing: the
// post-commit retry path that makes crash recovery exactly-once.
// Terms that settle nothing resolve to absence, and divergent terms
// under one key are a conflict, never a replay.
func (r *PurchaseIntentRepository) lookupIntent(ctx context.Context, account string, request application.AcceptPurchaseRequest, terms pricingdomain.Terms) (*application.PurchaseIntentResult, error) {
	var result application.PurchaseIntentResult
	var quoteID, holdID string
	var fiat, ink int64
	var hash string
	err := r.pool.QueryRow(ctx,
		`SELECT id::text, quote_id::text, fiat_minor, ink_milli, terms_hash, hold_id::text
		 FROM app.billing_ink_intents WHERE account_id = $1::uuid AND intent_key = $2`,
		account, request.Key.String()).Scan(
		&result.IntentID, &quoteID, &fiat, &ink, &hash, &holdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup purchase intent: %w", err)
	}
	if quoteID != request.QuoteID || fiat != request.Fiat.MinorUnits() ||
		ink != terms.InkMilli || hash != terms.Hash {
		return nil, application.ErrPurchaseIntentConflict
	}
	money, err := domain.NewMoney(fiat, domain.CurrencyBRL)
	if err != nil {
		return nil, fmt.Errorf("stored intent holds %d minor: %w", fiat, err)
	}
	result.QuoteID = quoteID
	result.Fiat = money
	result.InkMilli = ink
	result.HoldID = holdID
	result.Replayed = true
	return &result, nil
}

// createIntent attempts the acceptance once: the commercial vault
// covers the derived INK, the hold locks it out of the vault legs,
// and the intent row names the hold beside the server terms, sharing
// one transaction. Short vaults refuse instead of minting, unknown
// accounts resolve to absence instead of a guess, and a unique
// collision retries once through re-lookup, so concurrent runs of one
// key resolve the single acceptance instead of provisioning twice.
func (r *PurchaseIntentRepository) createIntent(ctx context.Context, account string, request application.AcceptPurchaseRequest, quote *pricingdomain.Quote, terms pricingdomain.Terms) (*application.PurchaseIntentResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin accept purchase transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var vault string
	err = tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies
		 WHERE kind = 'treasury' AND label = 'commercial_stock' FOR UPDATE`).Scan(&vault)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrInsufficientCommercialStock
		}
		return nil, fmt.Errorf("lock commercial stock: %w", err)
	}
	var stock int64
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE custody_id = $1::uuid`, vault).Scan(&stock)
	if err != nil {
		return nil, fmt.Errorf("read commercial stock: %w", err)
	}
	if stock < terms.InkMilli {
		// A concurrent run of the same key may have settled while this
		// attempt waited on the vault lock: resolve it before refusing,
		// so losers replay instead of mistaking a won race for an
		// empty vault. Roll back first: nothing was written yet.
		if err := tx.Rollback(ctx); err != nil {
			return nil, fmt.Errorf("abort uncovered acceptance: %w", err)
		}
		if replayed, err := r.lookupIntent(ctx, account, request, terms); err != nil || replayed != nil {
			return replayed, err
		}
		return nil, application.ErrInsufficientCommercialStock
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
		holdID, vault, holdCustody, terms.InkMilli,
		"ink-intent "+request.Key.String(), quote.ExpiresAt); err != nil {
		return nil, fmt.Errorf("record hold: %w", err)
	}
	var transfer string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4), ($1::uuid, $3::uuid, 'credit', $4)`,
		transfer, vault, holdCustody, terms.InkMilli); err != nil {
		return nil, fmt.Errorf("record hold legs: %w", err)
	}
	var intentID string
	now := r.clock.Now().UTC()
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, hold_id, decided_at)
		 VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, 'pending', $7::uuid, $8)`,
		request.Key.String(), account, request.QuoteID,
		request.Fiat.MinorUnits(), terms.InkMilli, terms.Hash, holdID, now); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, application.ErrPurchaserNotFound
		}
		return nil, fmt.Errorf("record purchase intent: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM app.billing_ink_intents WHERE account_id = $1::uuid AND intent_key = $2`,
		account, request.Key.String()).Scan(&intentID); err != nil {
		return nil, fmt.Errorf("resolve purchase intent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit accept purchase: %w", err)
	}
	return &application.PurchaseIntentResult{
		IntentID: intentID, QuoteID: request.QuoteID, Fiat: request.Fiat,
		InkMilli: terms.InkMilli, HoldID: holdID,
	}, nil
}

// isIntentConflict reports whether err is a unique collision that a
// re-lookup can resolve: a concurrent run of the same key.
func isIntentConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "billing_ink_intents_account_key_unique"
}
