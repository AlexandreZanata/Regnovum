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

// FundContract seals one trade contract locking the buyer amount in
// exclusive escrow, keyed idempotently by buyer and token. Replays
// resolve the original contract untouched; divergent terms under one
// key conflict instead of locking twice.
func (r *EscrowRepository) FundContract(ctx context.Context, request application.FundRequest) (*application.ContractView, error) {
	sealed, err := commercedomain.FundContract(commercedomain.ContractRequest{
		Key: request.Key, Object: request.Object, Buyer: request.Buyer,
		Provider: request.Provider, AmountMill: request.AmountMill,
		ExpiresAt: request.ExpiresAt, Now: request.Now,
	})
	if err != nil {
		return nil, err
	}
	if found, err := r.lookupContract(ctx, r.pool, request.Buyer, request.Key); err != nil || found != nil {
		if err != nil {
			return nil, err
		}
		if found.Hash != sealed.Hash || found.Object != sealed.Object ||
			found.Provider != sealed.Provider || found.AmountMill != sealed.AmountMill {
			return nil, commercedomain.ErrIntentionConflict
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
// so concurrent runs of one key resolve the single contract.
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
	transferID, err := recordFundLegs(ctx, tx, buyerID, escrowID, request.AmountMill)
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
// both sides in id order before any balance is read.
func lockBuyerEscrow(ctx context.Context, tx pgx.Tx, request application.FundRequest, contractID string) (string, string, error) {
	buyerID, found, err := platformpg.ResolveLedgerCustody(ctx, tx, "user", request.Buyer)
	if err != nil || !found {
		if err == nil {
			return "", "", commercedomain.ErrUnknownAccount
		}
		return "", "", err
	}
	var escrowID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('escrow', $1) RETURNING id::text`,
		"commerce-escrow-"+contractID).Scan(&escrowID); err != nil {
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

// recordFundLegs writes the buyer debit into exclusive escrow
// inside the caller transaction.
func recordFundLegs(ctx context.Context, tx pgx.Tx, buyerID, escrowID string, millis int64) (string, error) {
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return "", fmt.Errorf("generate transfer id: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4), ($1::uuid, $3::uuid, 'credit', $4)`,
		transferID, buyerID, escrowID, millis); err != nil {
		return "", fmt.Errorf("record escrow legs: %w", err)
	}
	return transferID, nil
}

// errShortFunds marks an uncovered funding for the caller re-lookup.

// recordFundedContract stores the contract row naming the locking
// transfer. A unique collision reports a worthwhile retry.
func recordFundedContract(ctx context.Context, tx pgx.Tx, request application.FundRequest, sealed commercedomain.TradeContract, contractID, transferID string) (application.ContractView, bool, error) {
	var posted time.Time
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.commerce_contracts
		 (id, contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at)
		 VALUES ($1::uuid, $2, 'trade', $3::uuid, $4::uuid, $5, $6, $7, $8::uuid, $9)
		 RETURNING posted_at`,
		contractID, request.Key, request.Buyer, request.Provider, request.Object,
		request.AmountMill, sealed.Hash, transferID, sealed.ExpiresAt).Scan(&posted); err != nil {
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
		Hash: sealed.Hash, PostedAt: posted,
	}, false, nil
}

// scanEscrowRow resolves one buyer contract with the status derived
// from its latest settlement step: funded with no steps yet.
func scanEscrowRow(ctx context.Context, q platformpg.LedgerQuerier, buyer, key string) (*escrowRow, error) {
	var row escrowRow
	var provider, termsHash, escrowTransfer string
	var expires, posted time.Time
	err := q.QueryRow(ctx,
		`SELECT id::text, object, provider_id::text, amount_milli, terms_hash,
		  escrow_transfer_id::text, expires_at, posted_at
		 FROM app.commerce_contracts WHERE buyer_id = $1::uuid AND contract_key = $2`,
		buyer, key).Scan(&row.view.ID, &row.view.Object, &provider, &row.view.AmountMill,
		&termsHash, &escrowTransfer, &expires, &posted)
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
