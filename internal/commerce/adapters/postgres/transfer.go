package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Transfer surface of the commerce adapter (P37-T02): the payer
// names key, kind, payee, amount and consent, and one transaction
// settles the transfer with the peer-to-peer legs moving the exact
// amount. Eligibility, sanctions, limits, balances and custodies
// resolve server-side, so a forged request can neither spend
// another holder's funds nor bypass a block. The payer is the
// authenticated caller by construction: no separate actor field
// exists to confuse.
var _ application.TransferRepository = (*Repository)(nil)

// TransferLimits carries the deployment-approved bounds: the
// per-transfer ceiling, the payer count cap and its window in
// minutes. No ratified value lives here; wiring supplies them.
type TransferLimits struct {
	MaxAmountMilli int64
	MaxPerWindow   int
	WindowMinutes  int
}

// Repository settles voluntary peer transfers against PostgreSQL.
type Repository struct {
	pool   *pgxpool.Pool
	limits TransferLimits
}

// NewRepository builds the repository with explicit wiring: the pool
// and the approved limits. No amount, count or window ever arrives
// from a payer.
func NewRepository(pool *pgxpool.Pool, limits TransferLimits) (*Repository, error) {
	if pool == nil {
		return nil, application.ErrInvalidTransferConfig
	}
	if limits.MaxAmountMilli <= 0 || limits.MaxPerWindow <= 0 || limits.WindowMinutes <= 0 {
		return nil, application.ErrInvalidTransferConfig
	}
	return &Repository{pool: pool, limits: limits}, nil
}

// rowQuerier covers pool and transaction reads for the settlement
// lookup.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Transfer settles one transfer keyed idempotently by payer and
// token: the commerce row and the ledger legs commit together.
// Replays resolve the original settlement untouched, divergent terms
// under one key conflict, and ineligible parties or uncovered
// balances refuse without writing. The book travels beside the
// seal: the same key in another book settles its own outcome, and
// a sealed book admits nothing new.
func (r *Repository) Transfer(ctx context.Context, request application.TransferRequest) (*application.TransferResult, error) {
	if request.Season.String() == "" {
		request.Season = commercedomain.SeasonKey(commercedomain.CompatSeasonKey)
	}
	if err := requireCommerceBookActive(ctx, r.pool, request.Season); err != nil {
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
	return nil, fmt.Errorf("transfer unsettled after conflict: %w", last)
}

// lookupSettlement resolves a settled intention without writing:
// the post-commit retry path that makes crash recovery exactly-once.
// Terms that settle nothing resolve to absence, and divergent terms
// under one key are a conflict, never a replay. The book travels
// with the receipt: a cross-book reuse conflicts instead of
// redirecting another book outcome.
func (r *Repository) lookupSettlement(ctx context.Context, q rowQuerier, request application.TransferRequest) (*application.TransferResult, error) {
	var result application.TransferResult
	var kind, payee string
	var amount int64
	var hash, seasonKey string
	err := q.QueryRow(ctx,
		`SELECT id::text, kind, payee_id::text, amount_milli, payload_hash, transfer_id::text, season_key
		 FROM app.commerce_transfers WHERE payer_id = $1::uuid AND intention_key = $2`,
		request.Payer, request.Key).Scan(
		&result.TransferRowID, &kind, &payee, &amount, &hash, &result.TransferID, &seasonKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, commercedomain.ErrUnknownAccount
		}
		return nil, fmt.Errorf("lookup transfer: %w", err)
	}
	if kind != request.Kind.String() || payee != request.Payee ||
		amount != request.AmountMilli || hash != request.PayloadHash {
		return nil, commercedomain.ErrIntentionConflict
	}
	season, err := commercedomain.ParseSeasonKey(seasonKey)
	if err != nil {
		return nil, fmt.Errorf("stored transfer book %q: %w", seasonKey, err)
	}
	if err := commercedomain.CheckSeasonMatch(season, request.Season); err != nil {
		return nil, commercedomain.ErrIntentionConflict
	}
	if _, err := commercedomain.ParseTransferKind(kind); err != nil {
		return nil, fmt.Errorf("stored transfer holds kind %q: %w", kind, err)
	}
	result.AmountMilli = amount
	result.Replayed = true
	result.Season = season
	return &result, nil
}

// createSettlement attempts the transfer once in a single
// transaction: eligibility, sanctions, limits, locks, balance and
// legs share it, so a transfer settles whole or not at all. A
// unique collision retries once through re-lookup, so concurrent
// runs of one key resolve the single settlement instead of paying
// twice. The book, its ACTIVE stage and its half-open window are
// judged on the database clock inside the same transaction.
func (r *Repository) createSettlement(ctx context.Context, request application.TransferRequest) (*application.TransferResult, bool, error) {
	ends, err := validateTransferEnds(request)
	if err != nil {
		return nil, false, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin transfer transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if frozen, err := platformpg.LedgerFrozen(ctx, tx); err != nil || frozen {
		if err != nil {
			return nil, false, err
		}
		return nil, false, economydomain.ErrEconomyFrozen
	}
	if err := requireCommerceBookActiveTx(ctx, tx, request.Season); err != nil {
		return nil, false, err
	}
	if replayed, err := r.lookupSettlement(ctx, tx, request); err != nil || replayed != nil {
		return replayed, false, err
	}
	fromID, toID, err := r.authorizeSeasonTransferParties(ctx, tx, request)
	if err != nil {
		return nil, false, err
	}
	if replayed, err := r.coverTransferFunds(ctx, tx, request, fromID, toID, ends.amountMilli); err != nil || replayed != nil {
		return replayed, false, err
	}
	transferID, err := recordSeasonTransferLegs(ctx, tx, fromID, toID, ends.amountMilli, request.Season)
	if err != nil {
		return nil, false, err
	}
	result, retry, err := recordSeasonTransferRow(ctx, tx, request, transferID)
	if err != nil {
		return nil, retry, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit transfer: %w", err)
	}
	return &result, false, nil
}

// transferEnds are the validated settlement terms: the exact amount
// in minor units.
type transferEnds struct {
	amountMilli int64
}

// validateTransferEnds parses the closed kind and the positive
// amount before any transaction opens.
func validateTransferEnds(request application.TransferRequest) (transferEnds, error) {
	if !request.Kind.IsValid() {
		return transferEnds{}, commercedomain.ErrInvalidTransferKind
	}
	if _, err := economydomain.NewMilliInk(request.AmountMilli); err != nil {
		return transferEnds{}, err
	}
	return transferEnds{amountMilli: request.AmountMilli}, nil
}

// authorizeSeasonTransferParties resolves every party-side fact in
// one commerce book: the same label in another book is another
// custody, and a leg mixing a custody of another book never writes.
func (r *Repository) authorizeSeasonTransferParties(ctx context.Context, tx pgx.Tx, request application.TransferRequest) (string, string, error) {
	if err := requireActiveAccount(ctx, tx, request.Payer); err != nil {
		return "", "", err
	}
	if err := requireActiveAccount(ctx, tx, request.Payee); err != nil {
		return "", "", err
	}
	if err := requireUnsanctioned(ctx, tx, request.Payer, request.Payee); err != nil {
		return "", "", err
	}
	if err := r.checkLimits(ctx, tx, request); err != nil {
		return "", "", err
	}
	fromID, err := resolveSeasonCustody(ctx, tx, "user", request.Payer, request.Season)
	if err != nil {
		return "", "", err
	}
	toID, err := resolveSeasonCustody(ctx, tx, "user", request.Payee, request.Season)
	if err != nil {
		return "", "", err
	}
	if fromID == toID {
		return "", "", commercedomain.ErrSelfTransfer
	}
	return fromID, toID, nil
}

// resolveSeasonCustody maps a (kind, label, book) triple to its
// registry id: the same label in another book is another custody.
func resolveSeasonCustody(ctx context.Context, tx pgx.Tx, kind, label string, season commercedomain.SeasonKey) (string, error) {
	var id string
	err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2 AND season_key = $3`,
		kind, label, season.String()).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", commercedomain.ErrUnknownAccount
		}
		return "", fmt.Errorf("resolve custody: %w", err)
	}
	return id, nil
}

// coverTransferFunds locks both custodies in id order and rechecks
// the source balance inside the locks, returning a replayed
// settlement when a concurrent run of the same key won the race. A
// nil result with a nil error means covered: proceed to the legs.
func (r *Repository) coverTransferFunds(ctx context.Context, tx pgx.Tx, request application.TransferRequest, fromID, toID string, millis int64) (*application.TransferResult, error) {
	if err := platformpg.LockLedgerCustodies(ctx, tx, fromID, toID); err != nil {
		return nil, err
	}
	balance, err := platformpg.LedgerBalanceMillis(ctx, tx, fromID)
	if err != nil {
		return nil, err
	}
	if balance >= millis {
		return nil, nil
	}
	// A concurrent run of the same key may have settled while this
	// attempt waited on the custody locks: resolve it before
	// refusing, so losers replay instead of mistaking a won race
	// for an empty balance. Roll back first: nothing was written
	// yet.
	if rbErr := tx.Rollback(ctx); rbErr != nil {
		return nil, fmt.Errorf("abort uncovered transfer: %w", rbErr)
	}
	if replayed, err := r.lookupSettlement(ctx, r.pool, request); err != nil || replayed != nil {
		return replayed, err
	}
	available, err := economydomain.NewMilliInk(balance)
	if err != nil {
		return nil, err
	}
	cover, err := economydomain.NewMilliInk(millis)
	if err != nil {
		return nil, err
	}
	if _, err := available.Sub(cover); err != nil {
		return nil, err
	}
	return nil, nil
}

// requireActiveAccount refuses unknown accounts and any status but
// active: pending, suspended and deleted holders move nothing. A
// malformed id is an unknown account, never a guess.
func requireActiveAccount(ctx context.Context, q rowQuerier, account string) error {
	var status string
	err := q.QueryRow(ctx, `SELECT status FROM app.accounts WHERE id = $1::uuid`, account).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commercedomain.ErrUnknownAccount
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return commercedomain.ErrUnknownAccount
		}
		return fmt.Errorf("read account status: %w", err)
	}
	if status != "active" {
		return commercedomain.ErrAccountNotActive
	}
	return nil
}

// requireUnsanctioned refuses any transfer touching a sanctioned
// account, paying or receiving: blocks apply in both directions.
func requireUnsanctioned(ctx context.Context, q rowQuerier, payer, payee string) error {
	var blocked bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.commerce_sanctions WHERE account_id IN ($1::uuid, $2::uuid))`,
		payer, payee).Scan(&blocked)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return commercedomain.ErrUnknownAccount
		}
		return fmt.Errorf("read sanctions: %w", err)
	}
	if blocked {
		return commercedomain.ErrSanctionedAccount
	}
	return nil
}

// checkLimits enforces the deployment-approved bounds: the amount
// ceiling and the payer count cap inside the trailing window, both
// read against committed rows.
func (r *Repository) checkLimits(ctx context.Context, q rowQuerier, request application.TransferRequest) error {
	if request.AmountMilli > r.limits.MaxAmountMilli {
		return commercedomain.ErrLimitExceeded
	}
	var recent int
	err := q.QueryRow(ctx,
		`SELECT count(*) FROM app.commerce_transfers
		 WHERE payer_id = $1::uuid AND posted_at > now() - make_interval(mins => $2)`,
		request.Payer, r.limits.WindowMinutes).Scan(&recent)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return commercedomain.ErrUnknownAccount
		}
		return fmt.Errorf("count recent transfers: %w", err)
	}
	if recent >= r.limits.MaxPerWindow {
		return commercedomain.ErrRateLimited
	}
	return nil
}

// isTransferConflict reports whether err is a unique collision that
// a re-lookup can resolve: a concurrent run of the same key, or a
// transfer id collision retried with a fresh id.
func isTransferConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "commerce_transfers_payer_key_unique" ||
		pgErr.ConstraintName == "commerce_transfers_transfer_unique"
}

// recordTransferLegs writes the debit/credit pair moving the exact
// amount inside the caller transaction. The legacy writer stays
// for callers that already scope by book; seasonal settlements use
// recordSeasonTransferLegs below.
// recordSeasonTransferLegs writes the debit/credit pair in one
// commerce book: the legs carry the book, so statement and row
// always point at one book.
func recordSeasonTransferLegs(ctx context.Context, tx pgx.Tx, fromID, toID string, millis int64, season commercedomain.SeasonKey) (string, error) {
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return "", fmt.Errorf("generate transfer id: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4, $5), ($1::uuid, $3::uuid, 'credit', $4, $5)`,
		transferID, fromID, toID, millis, season.String()); err != nil {
		return "", fmt.Errorf("record transfer legs: %w", err)
	}
	return transferID, nil
}

// recordSeasonTransferRow stores the settlement pinning the commerce
// book beside the seal: the receipt never leaves its book.
func recordSeasonTransferRow(ctx context.Context, tx pgx.Tx, request application.TransferRequest, transferID string) (application.TransferResult, bool, error) {
	var result application.TransferResult
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.commerce_transfers
		 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id, season_key)
		 VALUES ($1, $2, $3::uuid, $4::uuid, $5, $6, $7, $8::uuid, $9)
		 RETURNING id::text`,
		request.Key, request.Kind.String(), request.Payer, request.Payee,
		request.AmountMilli, request.ConsentRef, request.PayloadHash, transferID, request.Season.String()).Scan(&result.TransferRowID); err != nil {
		if isTransferConflict(err) {
			return application.TransferResult{}, true, fmt.Errorf("concurrent transfer race: %w", err)
		}
		return application.TransferResult{}, false, fmt.Errorf("record transfer: %w", err)
	}
	result.TransferID = transferID
	result.AmountMilli = request.AmountMilli
	result.Season = request.Season
	return result, false, nil
}
