package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Escrow surface of the commerce adapter (P37-T03): formal trades
// lock the buyer amount in exclusive per-contract escrow and settle
// exactly once by acceptance, cancellation or competent resolution.
// Expiry marks without moving. Every transition shares one
// transaction with the legs it moves, or nothing is stored at all.
var _ application.EscrowRepository = (*EscrowRepository)(nil)

// EscrowRepository funds trade contracts and settles their escrows
// against PostgreSQL.
type EscrowRepository struct {
	pool *pgxpool.Pool
}

// NewEscrowRepository builds the repository with explicit wiring.
func NewEscrowRepository(pool *pgxpool.Pool) (*EscrowRepository, error) {
	if pool == nil {
		return nil, application.ErrInvalidTransferConfig
	}
	return &EscrowRepository{pool: pool}, nil
}

// escrowRow is one stored contract with its derived status.
type escrowRow struct {
	view       application.ContractView
	escrowID   string
	providerID string
}

// sealFundRequest binds one funding to its book and terminal clause
// before any lock: the dormant book seals the formal terms alone,
// seasonal books additionally seal the 5.1 clause beside them.
func sealFundRequest(request *application.FundRequest) (commercedomain.TradeContract, error) {
	if request.Season.String() == "" {
		request.Season = commercedomain.SeasonKey(commercedomain.CompatSeasonKey)
	}
	if request.Season == commercedomain.SeasonKey(commercedomain.CompatSeasonKey) {
		return commercedomain.FundContract(commercedomain.ContractRequest{
			Key: request.Key, Object: request.Object, Buyer: request.Buyer,
			Provider: request.Provider, AmountMill: request.AmountMill,
			ExpiresAt: request.ExpiresAt, Now: request.Now,
		})
	}
	return commercedomain.FundSeasonalContract(commercedomain.SeasonalContractRequest{
		ContractRequest: commercedomain.ContractRequest{
			Key: request.Key, Object: request.Object, Buyer: request.Buyer,
			Provider: request.Provider, AmountMill: request.AmountMill,
			ExpiresAt: request.ExpiresAt, Now: request.Now,
		},
		Season: request.Season, PolicyRef: request.PolicyRef, PolicyHash: request.PolicyHash,
		BuyerAccept: request.BuyerAccept, ProviderAccept: request.ProviderAccept,
		SeasonEndsAt: request.SeasonEndsAt, ResetAcknowledged: true,
	})
}

// matchFundReplay refuses a divergent reuse under one key: the seal,
// the object, the provider, the amount and the book must all agree,
// or the second writer conflicts instead of locking twice.
func matchFundReplay(found *application.ContractView, sealed commercedomain.TradeContract, season commercedomain.SeasonKey) error {
	if found.Hash != sealed.Hash || found.Object != sealed.Object ||
		found.Provider != sealed.Provider || found.AmountMill != sealed.AmountMill {
		return commercedomain.ErrIntentionConflict
	}
	if found.Season.String() != "" && found.Season != season {
		return commercedomain.ErrIntentionConflict
	}
	return nil
}

// FundContract seals one trade contract locking the buyer amount in
// exclusive escrow, keyed idempotently by buyer and token. Replays
// resolve the original contract untouched; divergent terms under one
// key conflict instead of locking twice. The book and the terminal
// clause travel beside the seal: absence or divergence refuses new
// seasonal funding, and a sealed book admits nothing new.
func (r *EscrowRepository) FundContract(ctx context.Context, request application.FundRequest) (*application.ContractView, error) {
	sealed, err := sealFundRequest(&request)
	if err != nil {
		return nil, err
	}
	if err := requireCommerceBookActive(ctx, r.pool, request.Season); err != nil {
		return nil, err
	}
	if found, err := r.lookupContract(ctx, r.pool, request.Buyer, request.Key); err != nil || found != nil {
		if err != nil {
			return nil, err
		}
		if err := matchFundReplay(found, sealed, request.Season); err != nil {
			return nil, err
		}
		return found, nil
	}
	var last error
	for range 2 {
		result, retry, err := r.createContract(ctx, request, sealed)
		if err == nil {
			return result, nil
		}
		if !retry {
			return nil, err
		}
		last = err
		if found, err := r.lookupContract(ctx, r.pool, request.Buyer, request.Key); err != nil || found != nil {
			if err != nil {
				return nil, err
			}
			if found.Hash != sealed.Hash {
				return nil, commercedomain.ErrIntentionConflict
			}
			return found, nil
		}
	}
	return nil, fmt.Errorf("contract unfunded after conflict: %w", last)
}

// lookupContract resolves one buyer contract with its derived
// status without writing. Unknown keys resolve to absence.
func (r *EscrowRepository) lookupContract(ctx context.Context, q platformpg.LedgerQuerier, buyer, key string) (*application.ContractView, error) {
	row, err := scanEscrowRow(ctx, q, buyer, key)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return &row.view, nil
}

// createContract attempts the funding once: eligibility, locks, the
// buyer debit into exclusive escrow and the contract row share one
// transaction. A unique collision retries once through re-lookup,
// so concurrent runs of one key resolve the single contract. The
// book, its ACTIVE stage and its half-open window are judged on
// the database clock inside the same transaction.
func (r *EscrowRepository) createContract(ctx context.Context, request application.FundRequest, sealed commercedomain.TradeContract) (*application.ContractView, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin fund transaction: %w", err)
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
	if found, err := r.lookupContract(ctx, tx, request.Buyer, request.Key); err != nil || found != nil {
		return found, false, err
	}
	if err := checkFundingParties(ctx, tx, request); err != nil {
		return nil, false, err
	}
	var contractID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&contractID); err != nil {
		return nil, false, fmt.Errorf("generate contract id: %w", err)
	}
	buyerID, escrowID, err := lockBuyerEscrow(ctx, tx, request, contractID)
	if err != nil {
		return nil, false, err
	}
	cover, err := platformpg.LedgerBalanceMillis(ctx, tx, buyerID)
	if err != nil {
		return nil, false, err
	}
	if cover < request.AmountMill {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return nil, false, fmt.Errorf("abort uncovered funding: %w", rbErr)
		}
		if found, err := r.lookupContract(ctx, r.pool, request.Buyer, request.Key); err != nil || found != nil {
			return found, false, err
		}
		return nil, false, economydomain.ErrInsufficientMilliInk
	}
	transferID, err := recordFundLegs(ctx, tx, buyerID, escrowID, request.AmountMill, request.Season)
	if err != nil {
		return nil, false, err
	}
	result, retry, err := recordFundedContract(ctx, tx, request, sealed, contractID, transferID)
	if err != nil {
		return nil, retry, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit funding: %w", err)
	}
	return &result, false, nil
}

// checkFundingParties resolves every party-side fact before any
// lock is taken: active accounts, sanction clearance and a
// provisioned buyer custody.
func checkFundingParties(ctx context.Context, tx pgx.Tx, request application.FundRequest) error {
	if err := requireActiveAccount(ctx, tx, request.Buyer); err != nil {
		return err
	}
	if err := requireActiveAccount(ctx, tx, request.Provider); err != nil {
		return err
	}
	return requireUnsanctioned(ctx, tx, request.Buyer, request.Provider)
}

// lockBuyerEscrow creates the exclusive escrow custody and locks
// both sides in id order before any balance is read. The buyer and
// the escrow custodies live in the funding book: the same label in
// another book is another custody.
func lockBuyerEscrow(ctx context.Context, tx pgx.Tx, request application.FundRequest, contractID string) (string, string, error) {
	buyerID, err := resolveSeasonCommerceCustody(ctx, tx, "user", request.Buyer, request.Season)
	if err != nil {
		return "", "", err
	}
	var escrowID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('escrow', $1, $2) RETURNING id::text`,
		"commerce-escrow-"+contractID, request.Season.String()).Scan(&escrowID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", "", fmt.Errorf("concurrent escrow race: %w", err)
		}
		return "", "", fmt.Errorf("create escrow custody: %w", err)
	}
	if err := platformpg.LockLedgerCustodies(ctx, tx, buyerID, escrowID); err != nil {
		return "", "", err
	}
	return buyerID, escrowID, nil
}

// resolveSeasonCommerceCustody maps a (kind, label, book) triple to
// its registry id for escrow funding.
func resolveSeasonCommerceCustody(ctx context.Context, tx pgx.Tx, kind, label string, season commercedomain.SeasonKey) (string, error) {
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

// recordFundLegs writes the buyer debit into exclusive escrow
// inside the caller transaction. The legs carry the funding book.
func recordFundLegs(ctx context.Context, tx pgx.Tx, buyerID, escrowID string, millis int64, season commercedomain.SeasonKey) (string, error) {
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return "", fmt.Errorf("generate transfer id: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4, $5), ($1::uuid, $3::uuid, 'credit', $4, $5)`,
		transferID, buyerID, escrowID, millis, season.String()); err != nil {
		return "", fmt.Errorf("record escrow legs: %w", err)
	}
	return transferID, nil
}

// errShortFunds marks an uncovered funding for the caller re-lookup.

// recordFundedContract stores the contract row naming the locking
// transfer. A unique collision reports a worthwhile retry. The row
// pins the commerce book with the terminal clause beside the seal.
func recordFundedContract(ctx context.Context, tx pgx.Tx, request application.FundRequest, sealed commercedomain.TradeContract, contractID, transferID string) (application.ContractView, bool, error) {
	var posted time.Time
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.commerce_contracts
		 (id, contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at,
		  season_key, terminal_policy_ref, terminal_policy_hash, buyer_accept_ref, provider_accept_ref)
		 VALUES ($1::uuid, $2, 'trade', $3::uuid, $4::uuid, $5, $6, $7, $8::uuid, $9, $10, $11, $12, $13, $14)
		 RETURNING posted_at`,
		contractID, request.Key, request.Buyer, request.Provider, request.Object,
		request.AmountMill, sealed.Hash, transferID, sealed.ExpiresAt,
		request.Season.String(), request.PolicyRef, request.PolicyHash, request.BuyerAccept, request.ProviderAccept).Scan(&posted); err != nil {
		if isContractConflict(err) {
			return application.ContractView{}, true, fmt.Errorf("concurrent fund race: %w", err)
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return application.ContractView{}, false, commercedomain.ErrUnknownAccount
		}
		return application.ContractView{}, false, fmt.Errorf("record contract: %w", err)
	}
	return application.ContractView{
		ID: contractID, Key: request.Key, Object: request.Object,
		Buyer: request.Buyer, Provider: request.Provider, AmountMill: request.AmountMill,
		ExpiresAt: sealed.ExpiresAt, Status: commercedomain.ContractFunded,
		Hash: sealed.Hash, PostedAt: posted, Season: request.Season,
		PolicyRef: request.PolicyRef, BuyerAccept: request.BuyerAccept, ProviderAccept: request.ProviderAccept,
	}, false, nil
}

// scanEscrowRow resolves one buyer contract with the status derived
// from its latest settlement step: funded with no steps yet. The
// book and the terminal clause travel beside the seal.
func scanEscrowRow(ctx context.Context, q platformpg.LedgerQuerier, buyer, key string) (*escrowRow, error) {
	var row escrowRow
	var provider, termsHash, escrowTransfer, seasonKey, policyRef, policyHash, buyerAccept, providerAccept string
	var expires, posted time.Time
	err := q.QueryRow(ctx,
		`SELECT id::text, object, provider_id::text, amount_milli, terms_hash,
		  escrow_transfer_id::text, expires_at, posted_at, season_key,
		  terminal_policy_ref, terminal_policy_hash, buyer_accept_ref, provider_accept_ref
		 FROM app.commerce_contracts WHERE buyer_id = $1::uuid AND contract_key = $2`,
		buyer, key).Scan(&row.view.ID, &row.view.Object, &provider, &row.view.AmountMill,
		&termsHash, &escrowTransfer, &expires, &posted, &seasonKey, &policyRef, &policyHash, &buyerAccept, &providerAccept)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, commercedomain.ErrContractNotFound
		}
		return nil, fmt.Errorf("read contract: %w", err)
	}
	row.view.Key = key
	row.view.Buyer = buyer
	row.view.Provider = provider
	row.view.ExpiresAt = expires
	row.view.PostedAt = posted
	row.providerID = provider
	season, err := commercedomain.ParseSeasonKey(seasonKey)
	if err != nil {
		return nil, fmt.Errorf("stored contract book %q: %w", seasonKey, err)
	}
	row.view.Season = season
	row.view.PolicyRef = policyRef
	row.view.BuyerAccept = buyerAccept
	row.view.ProviderAccept = providerAccept
	var action string
	err = q.QueryRow(ctx,
		`SELECT action FROM app.commerce_settlements WHERE contract_id = $1::uuid
		 ORDER BY posted_at DESC, id DESC LIMIT 1`, row.view.ID).Scan(&action)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("read contract status: %w", err)
		}
		row.view.Status = commercedomain.ContractFunded
	} else if status, err := statusForAction(action); err != nil {
		return nil, fmt.Errorf("stored settlement holds action %q: %w", action, err)
	} else {
		row.view.Status = status
	}
	var escrowID string
	err = q.QueryRow(ctx,
		`SELECT c.id::text FROM app.economy_custodies c
		 JOIN app.economy_entries e ON e.custody_id = c.id
		 WHERE e.transfer_id = $1::uuid AND e.direction = 'credit' LIMIT 1`,
		escrowTransfer).Scan(&escrowID)
	if err != nil {
		return nil, fmt.Errorf("resolve escrow custody: %w", err)
	}
	row.escrowID = escrowID
	row.view.Hash = termsHash
	sealed := commercedomain.TradeContract{
		Key: key, Object: row.view.Object, Buyer: buyer, Provider: provider,
		AmountMill: row.view.AmountMill, ExpiresAt: expires,
		Status: row.view.Status, Hash: termsHash,
	}
	if err := sealed.VerifyHash(); err != nil {
		return nil, fmt.Errorf("stored contract fails its seal: %w", err)
	}
	return &row, nil
}

// statusForAction maps the latest settlement step to the lifecycle
// status.
func statusForAction(action string) (commercedomain.ContractStatus, error) {
	switch action {
	case "accept":
		return commercedomain.ContractAccepted, nil
	case "release":
		return commercedomain.ContractReleased, nil
	case "refund":
		return commercedomain.ContractRefunded, nil
	case "expire":
		return commercedomain.ContractExpired, nil
	case "resolve-release", "resolve-refund":
		return commercedomain.ContractResolved, nil
	default:
		return "", commercedomain.ErrInvalidContract
	}
}

// isContractConflict reports whether err is a unique collision that
// a re-lookup can resolve: a concurrent run of the same key.
func isContractConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "commerce_contracts_buyer_key_unique" ||
		pgErr.ConstraintName == "commerce_contracts_escrow_transfer_unique"
}
