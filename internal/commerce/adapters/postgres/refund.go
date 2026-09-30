package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Service refund surface of the commerce adapter (P37-T05): a
// liquidated formal payment is compensated in whole or in part by
// a new current transfer reversing the provider share and the
// tithe, linked to its untouched cause. The provider answers up to
// its balance and the shortfall becomes an explicit obligation to
// the buyer, never a hidden negative balance; the Treasury returns
// exactly the tithe reversal or refuses. Every refund shares one
// transaction with the legs and rows it writes, or nothing is
// stored at all.
var _ application.ServiceRefundRepository = (*ServiceRefundRepository)(nil)

// ServiceRefundRepository compensates liquidated service payments
// against PostgreSQL.
type ServiceRefundRepository struct {
	pool *pgxpool.Pool
}

// NewServiceRefundRepository builds the repository with explicit
// wiring.
func NewServiceRefundRepository(pool *pgxpool.Pool) (*ServiceRefundRepository, error) {
	if pool == nil {
		return nil, application.ErrInvalidTransferConfig
	}
	return &ServiceRefundRepository{pool: pool}, nil
}

// RefundService compensates one liquidated contract in whole or in
// part, keyed idempotently by contract and token. Replays resolve
// the original compensation untouched, divergent terms under one
// key conflict, and refunds that would exceed the unrefunded
// remainder refuse without writing.
func (r *ServiceRefundRepository) RefundService(ctx context.Context, request application.ServiceRefundRequest) (*application.ServiceRefundResult, error) {
	if _, _, err := commercedomain.SplitServiceRefund(request.AmountMill); err != nil {
		return nil, err
	}
	if replayed, err := r.lookupServiceRefund(ctx, r.pool, request); err != nil || replayed != nil {
		return replayed, err
	}
	var last error
	for range 2 {
		result, retry, err := r.createServiceRefund(ctx, request)
		if err == nil {
			return result, nil
		}
		if !retry {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupServiceRefund(ctx, r.pool, request); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("refund unsettled after conflict: %w", last)
}

// refundableCause is one liquidated contract with the remainder
// still refundable and the split of the requested refund. Season
// pins the original book: the compensation reverses there, never
// in the current book, and never after the seal.
type refundableCause struct {
	contractID string
	providerID string
	amount     int64
	tithe      int64
	share      int64
	season     commercedomain.SeasonKey
}

// refundCover is the locked funding of one refund: the resolved
// custodies with what the provider can move now and the explicit
// shortfall owed to the buyer.
type refundCover struct {
	providerID string
	buyerID    string
	treasuryID string
	moved      int64
	obligation int64
}

// lookupServiceRefund resolves a settled compensation without
// writing: the post-commit retry path that makes crash recovery
// exactly-once. Unknown contracts refuse; terms that settle
// nothing resolve to absence, and divergent terms under one key
// are a conflict, never a replay.
func (r *ServiceRefundRepository) lookupServiceRefund(ctx context.Context, q rowQuerier, request application.ServiceRefundRequest) (*application.ServiceRefundResult, error) {
	var contractID string
	err := q.QueryRow(ctx,
		`SELECT id::text FROM app.commerce_contracts WHERE buyer_id = $1::uuid AND contract_key = $2`,
		request.Buyer, request.ContractKey).Scan(&contractID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, commercedomain.ErrUnknownAccount
		}
		return nil, fmt.Errorf("lookup refund contract: %w", err)
	}
	var result application.ServiceRefundResult
	var amount, tithe, share, obligation int64
	var transfer *string
	err = q.QueryRow(ctx,
		`SELECT id::text, amount_milli, tithe_reversal_milli, provider_share_milli, obligation_milli, transfer_id::text
		 FROM app.commerce_service_refunds WHERE contract_id = $1::uuid AND refund_key = $2`,
		contractID, request.RefundKey).Scan(
		&result.RefundID, &amount, &tithe, &share, &obligation, &transfer)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup service refund: %w", err)
	}
	if amount != request.AmountMill {
		return nil, commercedomain.ErrIntentionConflict
	}
	result.AmountMill = amount
	result.TitheReversal = tithe
	result.ProviderShare = share
	result.ObligationMill = obligation
	if transfer != nil {
		result.TransferID = *transfer
	}
	result.Replayed = true
	return &result, nil
}

// readRefundableContract resolves the liquidated cause inside the
// writing transaction with the remainder still refundable and the
// split of the requested refund. Only released payments and
// competent releases refund: anything else refuses on the moved
// state, and the cause never reopens.
func readRefundableContract(ctx context.Context, tx pgx.Tx, request application.ServiceRefundRequest) (refundableCause, error) {
	row, err := scanEscrowRow(ctx, tx, request.Buyer, request.ContractKey)
	if err != nil {
		return refundableCause{}, err
	}
	if row == nil {
		return refundableCause{}, commercedomain.ErrContractNotFound
	}
	if err := refuseUnliquidatedRefund(ctx, tx, row, request); err != nil {
		return refundableCause{}, err
	}
	var refunded int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_milli), 0) FROM app.commerce_service_refunds WHERE contract_id = $1::uuid`,
		row.view.ID).Scan(&refunded); err != nil {
		return refundableCause{}, fmt.Errorf("sum refunded: %w", err)
	}
	if err := commercedomain.ValidateRefundAccumulation(row.view.AmountMill, refunded, request.AmountMill); err != nil {
		return refundableCause{}, err
	}
	tithe, share, err := commercedomain.SplitServiceRefund(request.AmountMill)
	if err != nil {
		return refundableCause{}, err
	}
	return refundableCause{
		contractID: row.view.ID, providerID: row.providerID,
		amount: row.view.AmountMill, tithe: tithe, share: share, season: row.view.Season,
	}, nil
}

// refuseUnliquidatedRefund allows refunds only on liquidated
// payments: released escrow and competent releases. Cancellations,
// lapses and competent refunds never held provider value with
// tithe, so they refuse instead of compensating twice.
func refuseUnliquidatedRefund(ctx context.Context, tx pgx.Tx, row *escrowRow, request application.ServiceRefundRequest) error {
	if row.view.Status == commercedomain.ContractReleased {
		return nil
	}
	if row.view.Status != commercedomain.ContractResolved {
		return commercedomain.ErrContractState
	}
	latest, err := latestSettlementAction(ctx, tx, request.Buyer, request.ContractKey)
	if err != nil {
		return err
	}
	if latest != "resolve-release" {
		return commercedomain.ErrContractState
	}
	return nil
}

// coverRefundShares locks provider, buyer and Treasury in id order
// and splits the funding inside the locks: the provider moves up
// to its balance with the shortfall owed explicitly, while the
// Treasury returns the tithe reversal in full or refuses with
// nothing written. All three custodies live in the original book.
func coverRefundShares(ctx context.Context, tx pgx.Tx, cause refundableCause, request application.ServiceRefundRequest) (refundCover, error) {
	providerID, err := resolveSeasonCommerceCustody(ctx, tx, "user", cause.providerID, cause.season)
	if err != nil {
		return refundCover{}, err
	}
	buyerID, err := resolveSeasonCommerceCustody(ctx, tx, "user", request.Buyer, cause.season)
	if err != nil {
		return refundCover{}, err
	}
	seasonStr := cause.season.String()
	if seasonStr == "" {
		seasonStr = commercedomain.CompatSeasonKey
	}
	treasuryID, err := resolveTitheTreasury(ctx, tx, seasonStr)
	if err != nil {
		return refundCover{}, err
	}
	if err := lockRefundCustodies(ctx, tx, providerID, buyerID, treasuryID); err != nil {
		return refundCover{}, err
	}
	providerBalance, err := platformpg.LedgerBalanceMillis(ctx, tx, providerID)
	if err != nil {
		return refundCover{}, err
	}
	treasuryBalance, err := platformpg.LedgerBalanceMillis(ctx, tx, treasuryID)
	if err != nil {
		return refundCover{}, err
	}
	if treasuryBalance < cause.tithe {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return refundCover{}, fmt.Errorf("abort uncovered tithe reversal: %w", rbErr)
		}
		return refundCover{}, economydomain.ErrInsufficientMilliInk
	}
	moved := cause.share
	if providerBalance < moved {
		moved = providerBalance
	}
	return refundCover{
		providerID: providerID, buyerID: buyerID, treasuryID: treasuryID,
		moved: moved, obligation: cause.share - moved,
	}, nil
}

// recordRefundLegs writes the compensation legs under one transfer:
// the provider debit of what it can move now with the Treasury
// debit of the tithe reversal and the buyer credit of their sum.
// A zero side writes no leg (the journal CHECK refuses 0); dust
// with a broke provider moves only the tithe, and a fully broke
// dust refund moves nothing with a NULL transfer. The legs carry
// the original book.
func recordRefundLegs(ctx context.Context, tx pgx.Tx, transferID string, cover refundCover, cause refundableCause) error {
	type leg struct {
		custody   string
		direction string
		millis    int64
	}
	legs := []leg{}
	if cover.moved > 0 {
		legs = append(legs, leg{cover.providerID, "debit", cover.moved})
	}
	if cause.tithe > 0 {
		legs = append(legs, leg{cover.treasuryID, "debit", cause.tithe})
	}
	if cover.moved+cause.tithe > 0 {
		legs = append(legs, leg{cover.buyerID, "credit", cover.moved + cause.tithe})
	}
	seasonStr := cause.season.String()
	if seasonStr == "" {
		seasonStr = commercedomain.CompatSeasonKey
	}
	for _, l := range legs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
			 VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
			transferID, l.custody, l.direction, l.millis, seasonStr); err != nil {
			return fmt.Errorf("record refund legs: %w", err)
		}
	}
	return nil
}

// recordRefundRow stores the compensation linked to its untouched
// cause with the explicit shortfall, reporting whether a
// concurrent run of the same key won the race through the unique
// guards.
func recordRefundRow(ctx context.Context, tx pgx.Tx, request application.ServiceRefundRequest, cause refundableCause, cover refundCover, transferID string) (application.ServiceRefundResult, bool, error) {
	var result application.ServiceRefundResult
	var transferArg any
	if transferID != "" {
		transferArg = transferID
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.commerce_service_refunds
		 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, obligation_milli, transfer_id)
		 VALUES ((SELECT id FROM app.commerce_contracts WHERE buyer_id = $1::uuid AND contract_key = $2), $3, $4, $5, $6, $7, $8::uuid)
		 RETURNING id::text`,
		request.Buyer, request.ContractKey, request.RefundKey,
		request.AmountMill, cause.tithe, cause.share, cover.obligation, transferArg).Scan(&result.RefundID); err != nil {
		if isServiceRefundConflict(err) {
			return application.ServiceRefundResult{}, true, fmt.Errorf("concurrent refund race: %w", err)
		}
		return application.ServiceRefundResult{}, false, fmt.Errorf("record service refund: %w", err)
	}
	if cover.obligation > 0 {
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.commerce_refund_obligations (refund_id, debtor_id, creditor_id, amount_milli)
			 VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`,
			result.RefundID, cause.providerID, request.Buyer, cover.obligation); err != nil {
			return application.ServiceRefundResult{}, false, fmt.Errorf("record refund obligation: %w", err)
		}
	}
	result.TransferID = transferID
	result.AmountMill = request.AmountMill
	result.TitheReversal = cause.tithe
	result.ProviderShare = cause.share
	result.ObligationMill = cover.obligation
	return result, false, nil
}

// lockRefundCustodies pins provider, buyer and Treasury in
// canonical id order. Canonical ordering keeps concurrent refunds
// from deadlocking: every transaction asks for the same rows in
// the same sequence.
func lockRefundCustodies(ctx context.Context, tx pgx.Tx, providerID, buyerID, treasuryID string) error {
	ids := []string{providerID, buyerID, treasuryID}
	sort.Strings(ids)
	if _, err := tx.Exec(ctx,
		`SELECT id FROM app.economy_custodies WHERE id IN ($1::uuid, $2::uuid, $3::uuid) ORDER BY id FOR UPDATE`,
		ids[0], ids[1], ids[2]); err != nil {
		return fmt.Errorf("lock refund custodies: %w", err)
	}
	return nil
}

// genRefundTransfer mints one transfer identifier when the refund
// moves value, or reports absence when dust with a broke provider
// moves nothing and the full share becomes obligation.
func genRefundTransfer(ctx context.Context, tx pgx.Tx, cover refundCover, cause refundableCause) (string, error) {
	if cover.moved+cause.tithe <= 0 {
		return "", nil
	}
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return "", fmt.Errorf("generate refund transfer id: %w", err)
	}
	return transferID, nil
}

// isServiceRefundConflict reports whether err is a unique collision
// that a re-lookup can resolve: a concurrent run of the same key,
// or a transfer id collision retried with a fresh id.
func isServiceRefundConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "commerce_service_refunds_contract_key_unique" ||
		pgErr.ConstraintName == "commerce_service_refunds_transfer_unique"
}

// createServiceRefund attempts the compensation once in a single
// transaction: the liquidated cause, the locked funding, the legs
// and both rows share it, so a refund compensates whole or not at
// all. A unique collision retries once through re-lookup, so
// concurrent runs of one key resolve the single compensation
// instead of reversing twice. The compensation reverses in the
// original book and refuses after the seal.
func (r *ServiceRefundRepository) createServiceRefund(ctx context.Context, request application.ServiceRefundRequest) (*application.ServiceRefundResult, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin refund transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if frozen, err := platformpg.LedgerFrozen(ctx, tx); err != nil || frozen {
		if err != nil {
			return nil, false, err
		}
		return nil, false, economydomain.ErrEconomyFrozen
	}
	if replayed, err := r.lookupServiceRefund(ctx, tx, request); err != nil || replayed != nil {
		return replayed, false, err
	}
	cause, err := readRefundableContract(ctx, tx, request)
	if err != nil {
		return nil, false, err
	}
	if err := requireCommerceBookActiveTx(ctx, tx, cause.season); err != nil {
		return nil, false, err
	}
	cover, err := coverRefundShares(ctx, tx, cause, request)
	if err != nil {
		return nil, false, err
	}
	transferID, err := genRefundTransfer(ctx, tx, cover, cause)
	if err != nil {
		return nil, false, err
	}
	if transferID != "" {
		if err := recordRefundLegs(ctx, tx, transferID, cover, cause); err != nil {
			return nil, false, err
		}
	}
	result, retry, err := recordRefundRow(ctx, tx, request, cause, cover, transferID)
	if err != nil {
		return nil, retry, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit service refund: %w", err)
	}
	return &result, false, nil
}
