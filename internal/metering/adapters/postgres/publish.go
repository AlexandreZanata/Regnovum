package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Publication surface of the metering adapter (P36-T04): the
// publisher names key, account, measured content, price and quote,
// and one transaction seals the publication with the citizen-to-
// Treasury legs moving the exact quoted cost. Cost, version and
// hashes resolve from the sealed quote, so a forged request can
// never invent a price or move another holder's funds. The database
// clock stamps the posting inside the writing transaction: the row
// is only visible after commit, never at request or browser time.
var _ application.PublishRepository = (*Repository)(nil)

// Repository settles INK publication charges against PostgreSQL.
type Repository struct {
	pool  *pgxpool.Pool
	clock ports.Clock
}

// NewRepository builds the repository with explicit wiring: the pool
// and the clock judging quote liveness. No price, cost or instant
// ever arrives from a publisher.
func NewRepository(pool *pgxpool.Pool, clock ports.Clock) (*Repository, error) {
	if pool == nil || clock == nil {
		return nil, application.ErrInvalidPublishConfig
	}
	return &Repository{pool: pool, clock: clock}, nil
}

// rowQuerier covers pool and transaction reads for the settlement
// lookup.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Publish settles one publication keyed idempotently by account and
// token: the publication row and the ledger legs commit together.
// Replays resolve the original settlement untouched, divergent terms
// under one key conflict, and uncovered balances refuse without
// writing.
func (r *Repository) Publish(ctx context.Context, request application.PublishRequest) (*application.PublishResult, error) {
	if replayed, err := r.lookupSettlement(ctx, r.pool, request); err != nil || replayed != nil {
		return replayed, err
	}
	var last error
	for range 2 {
		result, retry, err := r.createSettlement(ctx, request)
		if err == nil {
			return result, nil
		}
		if !retry {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupSettlement(ctx, r.pool, request); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("publication unsettled after conflict: %w", last)
}

// lookupSettlement resolves a settled intention without writing:
// the post-commit retry path that makes crash recovery exactly-once.
// Terms that settle nothing resolve to absence, and divergent terms
// under one key are a conflict, never a replay.
func (r *Repository) lookupSettlement(ctx context.Context, q rowQuerier, request application.PublishRequest) (*application.PublishResult, error) {
	var result application.PublishResult
	var amount int64
	var contentHash, quoteHash, payloadHash, transfer string
	err := q.QueryRow(ctx,
		`SELECT id::text, amount_milli, content_hash, quote_hash, payload_hash, transfer_id::text, posted_at
		 FROM app.metering_publications WHERE account_label = $1 AND intention_key = $2`,
		request.Account, request.Key.String()).Scan(
		&result.PublicationID, &amount, &contentHash, &quoteHash, &payloadHash, &transfer, &result.PostedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup publication: %w", err)
	}
	if payloadHash != request.PayloadHash || amount != request.Quote.TotalMilli ||
		contentHash != request.Quote.ContentHash.String() || quoteHash != request.Quote.Hash {
		return nil, meteringdomain.ErrPublishConflict
	}
	result.TransferID = transfer
	result.TotalMilli = amount
	result.Replayed = true
	return &result, nil
}

// createSettlement attempts the publication once in a single
// transaction: the legs move the exact quoted cost from citizen to
// Treasury and the publication row names the same transfer, so
// statement and content always point at one intention. A unique
// collision retries once through re-lookup, so concurrent runs of
// one key resolve the single settlement instead of charging twice.
//
// Fault staging is exercised per failure point, not through the
// shared TxManager port: this adapter owns its transaction like the
// economy and billing adapters, and each test case below dies at its
// own stage with journal and registry unchanged.
func (r *Repository) createSettlement(ctx context.Context, request application.PublishRequest) (*application.PublishResult, bool, error) {
	fromKind, err := economydomain.ParseCustodyKind(request.FromKind)
	if err != nil {
		return nil, false, err
	}
	toKind, err := economydomain.ParseCustodyKind(request.ToKind)
	if err != nil {
		return nil, false, err
	}
	amount, err := economydomain.NewMilliInk(request.Quote.TotalMilli)
	if err != nil {
		return nil, false, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin publish transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := requireLedgerOpen(ctx, tx); err != nil {
		return nil, false, err
	}
	if replayed, err := r.lookupSettlement(ctx, tx, request); err != nil || replayed != nil {
		return replayed, false, err
	}
	fromID, err := resolveLedgerCustody(ctx, tx, fromKind.String(), request.FromLabel)
	if err != nil {
		return nil, false, err
	}
	toID, err := resolveLedgerCustody(ctx, tx, toKind.String(), request.ToLabel)
	if err != nil {
		return nil, false, err
	}
	if fromID == toID {
		return nil, false, economydomain.ErrSameCustody
	}
	if !fromKind.CanSpend() {
		return nil, false, economydomain.ErrUnauthorizedCustody
	}
	if err := lockLedgerCustodies(ctx, tx, fromID, toID); err != nil {
		return nil, false, err
	}
	balance, err := ledgerBalance(ctx, tx, fromID)
	if err != nil {
		return nil, false, err
	}
	if _, err := balance.Sub(amount); err != nil {
		return nil, false, err
	}
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return nil, false, fmt.Errorf("generate transfer id: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4), ($1::uuid, $3::uuid, 'credit', $4)`,
		transferID, fromID, toID, amount.Millis()); err != nil {
		return nil, false, fmt.Errorf("record charge legs: %w", err)
	}
	var result application.PublishResult
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid)
		 RETURNING id::text, posted_at`,
		request.Key.String(), request.Account, request.Quote.Service.String(),
		request.Quote.Version, request.Quote.Units, amount.Millis(),
		request.Quote.ContentHash.String(), request.Quote.Hash,
		request.PayloadHash, transferID).Scan(&result.PublicationID, &result.PostedAt); err != nil {
		if isPublishConflict(err) {
			return nil, true, fmt.Errorf("concurrent publication race: %w", err)
		}
		return nil, false, fmt.Errorf("record publication: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit publication: %w", err)
	}
	result.TransferID = transferID
	result.TotalMilli = amount.Millis()
	return &result, false, nil
}

// isPublishConflict reports whether err is a unique collision that a
// re-lookup can resolve: a concurrent run of the same key, or a
// transfer id collision retried with a fresh id.
func isPublishConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "metering_publications_account_key_unique" ||
		pgErr.ConstraintName == "metering_publications_transfer_unique"
}

// requireLedgerOpen refuses any mutation while the book is frozen on
// a conservation break. Reads never call it. A missing mode row
// means a book that never froze, which is open.
func requireLedgerOpen(ctx context.Context, tx pgx.Tx) error {
	var frozen bool
	if err := tx.QueryRow(ctx, `SELECT frozen FROM app.economy_mode`).Scan(&frozen); err != nil {
		return nil
	}
	if frozen {
		return economydomain.ErrEconomyFrozen
	}
	return nil
}

// resolveLedgerCustody maps a (kind, label) pair to its registry id.
// Unknown pairs fail before any lock is taken or leg written.
func resolveLedgerCustody(ctx context.Context, tx pgx.Tx, kind, label string) (string, error) {
	var id string
	err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2`,
		kind, label).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", economydomain.ErrUnknownCustody
		}
		return "", fmt.Errorf("resolve custody: %w", err)
	}
	return id, nil
}

// lockLedgerCustodies pins both custody rows in id order. Canonical
// ordering is what keeps two settlements in opposite directions from
// deadlocking: every transaction asks for the same rows in the same
// sequence.
func lockLedgerCustodies(ctx context.Context, tx pgx.Tx, firstID, secondID string) error {
	if _, err := tx.Exec(ctx,
		`SELECT id FROM app.economy_custodies WHERE id IN ($1::uuid, $2::uuid) ORDER BY id FOR UPDATE`,
		firstID, secondID); err != nil {
		return fmt.Errorf("lock custodies: %w", err)
	}
	return nil
}

// ledgerBalance rebuilds the balance from the journal: credits minus
// debits. Balances are never stored, only derived, so they cannot
// drift from the legs.
func ledgerBalance(ctx context.Context, tx pgx.Tx, custodyID string) (economydomain.MilliInk, error) {
	var balance int64
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE custody_id = $1::uuid`,
		custodyID).Scan(&balance)
	if err != nil {
		return economydomain.MilliInk{}, fmt.Errorf("read balance: %w", err)
	}
	return economydomain.NewMilliInk(balance)
}
