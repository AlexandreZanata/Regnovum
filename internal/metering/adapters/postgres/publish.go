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
// The approved price table arrives at construction from the
// deployment: fees settle only for priced services, and unapproved
// services stay unavailable even with a self-sealed quote.
type Repository struct {
	pool   *pgxpool.Pool
	clock  ports.Clock
	prices meteringdomain.Catalog
}

// NewRepository builds the repository with explicit wiring: the pool,
// the clock judging quote liveness and the approved price table
// pricing every settlement. No price, cost or instant ever arrives
// from a publisher.
func NewRepository(pool *pgxpool.Pool, clock ports.Clock, prices meteringdomain.Catalog) (*Repository, error) {
	if pool == nil || clock == nil {
		return nil, application.ErrInvalidPublishConfig
	}
	return &Repository{pool: pool, clock: clock, prices: prices}, nil
}

// rowQuerier covers pool and transaction reads for the settlement
// lookup.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Publish settles one publication keyed idempotently by account and
// token: the publication row and the ledger legs commit together.
// Replays resolve the original settlement untouched, divergent terms
// under one key conflict, uncovered balances refuse without writing,
// and services outside the approved table refuse before any lock.
func (r *Repository) Publish(ctx context.Context, request application.PublishRequest) (*application.PublishResult, error) {
	if err := r.resolveApprovedPrice(request); err != nil {
		return nil, err
	}
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

// resolveApprovedPrice re-prices the request against the approved
// table instead of trusting the caller price: the entry covering the
// acceptance instant must name the quoted service, version and unit
// price, and the recomputed total must match the sealed quote.
// Services outside the table stay unavailable; forged or stale terms
// refuse before any lock is taken or leg written. Bonds never enter
// here: refundable reservations live in holds, never in this table.
func (r *Repository) resolveApprovedPrice(request application.PublishRequest) error {
	approved, err := r.prices.PriceAt(request.Quote.Service, request.Quote.AcceptedAt)
	if err != nil {
		return err
	}
	if approved.Version != request.Quote.Version || approved.PriceMilli != request.Quote.PriceMilli {
		return meteringdomain.ErrInvalidQuote
	}
	total, err := meteringdomain.TotalFor(request.Quote.Units, approved.PriceMilli)
	if err != nil {
		return err
	}
	if total != request.Quote.TotalMilli {
		return meteringdomain.ErrInvalidQuote
	}
	return nil
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

// settlementEnds are the validated ledger endpoints of one
// publication: spendable source and destination kinds with the exact
// quoted amount.
type settlementEnds struct {
	fromKind economydomain.CustodyKind
	toKind   economydomain.CustodyKind
	amount   economydomain.MilliInk
}

// validateSettlementEnds parses the opaque custody kinds and the
// quoted total before any transaction opens.
func validateSettlementEnds(request application.PublishRequest) (settlementEnds, error) {
	fromKind, err := economydomain.ParseCustodyKind(request.FromKind)
	if err != nil {
		return settlementEnds{}, err
	}
	toKind, err := economydomain.ParseCustodyKind(request.ToKind)
	if err != nil {
		return settlementEnds{}, err
	}
	amount, err := economydomain.NewMilliInk(request.Quote.TotalMilli)
	if err != nil {
		return settlementEnds{}, err
	}
	return settlementEnds{fromKind: fromKind, toKind: toKind, amount: amount}, nil
}

// coverSettlement locks both custodies in id order and rechecks the
// source balance inside the locks.
func coverSettlement(ctx context.Context, tx pgx.Tx, fromID, toID string, amount economydomain.MilliInk) error {
	if err := lockLedgerCustodies(ctx, tx, fromID, toID); err != nil {
		return err
	}
	balance, err := ledgerBalance(ctx, tx, fromID)
	if err != nil {
		return err
	}
	_, err = balance.Sub(amount)
	return err
}

// recordSettlementLegs writes the debit/credit pair moving the exact
// quoted cost inside the caller transaction.
func recordSettlementLegs(ctx context.Context, tx pgx.Tx, fromID, toID string, millis int64) (string, error) {
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return "", fmt.Errorf("generate transfer id: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4), ($1::uuid, $3::uuid, 'credit', $4)`,
		transferID, fromID, toID, millis); err != nil {
		return "", fmt.Errorf("record charge legs: %w", err)
	}
	return transferID, nil
}

// recordPublicationRow stores the publication naming the same
// transfer as the legs and returns the database posted instant. A
// unique collision reports a worthwhile retry: a concurrent run may
// have committed the same intention first.
func recordPublicationRow(ctx context.Context, tx pgx.Tx, request application.PublishRequest, transferID string, millis int64) (application.PublishResult, bool, error) {
	var result application.PublishResult
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid)
		 RETURNING id::text, posted_at`,
		request.Key.String(), request.Account, request.Quote.Service.String(),
		request.Quote.Version, request.Quote.Units, millis,
		request.Quote.ContentHash.String(), request.Quote.Hash,
		request.PayloadHash, transferID).Scan(&result.PublicationID, &result.PostedAt); err != nil {
		if isPublishConflict(err) {
			return application.PublishResult{}, true, fmt.Errorf("concurrent publication race: %w", err)
		}
		return application.PublishResult{}, false, fmt.Errorf("record publication: %w", err)
	}
	result.TransferID = transferID
	result.TotalMilli = millis
	return result, false, nil
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
	ends, err := validateSettlementEnds(request)
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
	fromID, err := resolveLedgerCustody(ctx, tx, ends.fromKind.String(), request.FromLabel)
	if err != nil {
		return nil, false, err
	}
	toID, err := resolveLedgerCustody(ctx, tx, ends.toKind.String(), request.ToLabel)
	if err != nil {
		return nil, false, err
	}
	if fromID == toID {
		return nil, false, economydomain.ErrSameCustody
	}
	if !ends.fromKind.CanSpend() {
		return nil, false, economydomain.ErrUnauthorizedCustody
	}
	if err := coverSettlement(ctx, tx, fromID, toID, ends.amount); err != nil {
		// A concurrent run of the same key may have settled while
		// this attempt waited on the custody locks: resolve it
		// before refusing, so losers replay instead of mistaking a
		// won race for an empty balance. Roll back first: nothing
		// was written yet.
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return nil, false, fmt.Errorf("abort uncovered publication: %w", rbErr)
		}
		if replayed, err := r.lookupSettlement(ctx, r.pool, request); err != nil || replayed != nil {
			return replayed, false, err
		}
		return nil, false, err
	}
	transferID, err := recordSettlementLegs(ctx, tx, fromID, toID, ends.amount.Millis())
	if err != nil {
		return nil, false, err
	}
	result, retry, err := recordPublicationRow(ctx, tx, request, transferID, ends.amount.Millis())
	if err != nil {
		return nil, retry, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit publication: %w", err)
	}
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
