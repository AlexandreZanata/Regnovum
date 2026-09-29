package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
)

var _ application.ReceiptsRepository = (*Repository)(nil)

// GetReceipt resolves one owned publication with its legs and its
// compensation, if any. Another account's publications and unknown
// ids resolve to absence: receipts never leak across owners.
func (r *Repository) GetReceipt(ctx context.Context, account, id string) (*application.Receipt, error) {
	publication, err := scanPublication(ctx, r.pool, account, id)
	if err != nil {
		return nil, err
	}
	legs, err := scanLegs(ctx, r.pool, publication.TransferID)
	if err != nil {
		return nil, err
	}
	receipt := &application.Receipt{Publication: *publication, Legs: legs}
	refund, err := scanRefund(ctx, r.pool, publication.ID)
	if err != nil {
		return nil, err
	}
	receipt.Refund = refund
	return receipt, nil
}

// Statement resolves the owner extract: up to limit publications in
// reverse posting order with the refund flag each, plus the current
// ('user', label) balance. Unknown owners read an empty extract
// with a zero balance, never an error.
func (r *Repository) Statement(ctx context.Context, account string, limit int) (*application.Statement, error) {
	if limit <= 0 || limit > 100 {
		return nil, meteringdomain.ErrInvalidQuote
	}
	rows, err := r.pool.Query(ctx,
		`SELECT p.id::text, p.intention_key, p.account_label, p.service, p.price_version,
		  p.units, p.amount_milli, p.content_hash, p.quote_hash, p.transfer_id::text, p.posted_at,
		  (r.id IS NOT NULL)
		 FROM app.metering_publications p
		 LEFT JOIN app.metering_refunds r ON r.original_id = p.id
		 WHERE p.account_label = $1
		 ORDER BY p.posted_at DESC, p.id DESC
		 LIMIT $2`, account, limit)
	if err != nil {
		return nil, fmt.Errorf("list publications: %w", err)
	}
	defer rows.Close()
	statement := &application.Statement{}
	for rows.Next() {
		var entry application.StatementEntry
		var publication application.Publication
		if err := rows.Scan(&publication.ID, &publication.IntentionKey, &publication.Account,
			&publication.Service, &publication.Version, &publication.Units, &publication.AmountMilli,
			&publication.ContentHash, &publication.QuoteHash, &publication.TransferID,
			&publication.PostedAt, &entry.Refunded); err != nil {
			return nil, fmt.Errorf("scan statement entry: %w", err)
		}
		entry.Publication = publication
		statement.Entries = append(statement.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate statement: %w", err)
	}
	if err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'user' AND c.label = $1`, account).Scan(&statement.BalanceMilli); err != nil {
		return nil, fmt.Errorf("read statement balance: %w", err)
	}
	return statement, nil
}

func scanPublication(ctx context.Context, q rowQuerier, account, id string) (*application.Publication, error) {
	var publication application.Publication
	err := q.QueryRow(ctx,
		`SELECT id::text, intention_key, account_label, service, price_version, units,
		  amount_milli, content_hash, quote_hash, transfer_id::text, posted_at
		 FROM app.metering_publications WHERE account_label = $1 AND id = $2::uuid`,
		account, id).Scan(&publication.ID, &publication.IntentionKey, &publication.Account,
		&publication.Service, &publication.Version, &publication.Units, &publication.AmountMilli,
		&publication.ContentHash, &publication.QuoteHash, &publication.TransferID, &publication.PostedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, meteringdomain.ErrUnknownPublication
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, meteringdomain.ErrUnknownPublication
		}
		return nil, fmt.Errorf("read publication: %w", err)
	}
	return &publication, nil
}

func scanLegs(ctx context.Context, q rowQuerier, transfer string) ([]application.Leg, error) {
	rows, err := q.Query(ctx,
		`SELECT c.kind, c.label, e.direction, e.amount_milli FROM app.economy_entries e
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE e.transfer_id = $1::uuid ORDER BY e.direction`, transfer)
	if err != nil {
		return nil, fmt.Errorf("read legs: %w", err)
	}
	defer rows.Close()
	var legs []application.Leg
	for rows.Next() {
		var leg application.Leg
		if err := rows.Scan(&leg.Kind, &leg.Label, &leg.Direction, &leg.Amount); err != nil {
			return nil, fmt.Errorf("scan leg: %w", err)
		}
		legs = append(legs, leg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legs: %w", err)
	}
	if len(legs) == 0 {
		return nil, fmt.Errorf("transfer %s holds no legs", transfer)
	}
	return legs, nil
}

func scanRefund(ctx context.Context, q rowQuerier, publicationID string) (*application.Refund, error) {
	var refund application.Refund
	err := q.QueryRow(ctx,
		`SELECT id::text, refund_key, amount_milli, transfer_id::text, posted_at
		 FROM app.metering_refunds WHERE original_id = $1::uuid`,
		publicationID).Scan(&refund.ID, &refund.Key, &refund.Amount, &refund.TransferID, &refund.PostedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read refund: %w", err)
	}
	return &refund, nil
}
