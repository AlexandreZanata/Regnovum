package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

// Trade receipt surface of the commerce adapter (P37-T07): one owned
// formal trade resolves with its floor(10%) tithe split and its
// compensations, and the participant extract lists owned lines. Reads
// never mutate and never leak: another participant's contracts and
// unknown ids resolve to absence.
var _ application.TradeReceiptsRepository = (*TradeReceiptRepository)(nil)

// TradeReceiptRepository resolves owned trade receipts against
// PostgreSQL.
type TradeReceiptRepository struct {
	pool *pgxpool.Pool
}

// NewTradeReceiptRepository builds the repository with explicit
// wiring.
func NewTradeReceiptRepository(pool *pgxpool.Pool) (*TradeReceiptRepository, error) {
	if pool == nil {
		return nil, application.ErrInvalidTransferConfig
	}
	return &TradeReceiptRepository{pool: pool}, nil
}

// receiptQuerier covers pool and transaction reads for the receipt
// lookup: single-row and multi-row.
type receiptQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// scanInstant converts one scanned timestamptz value to UTC.
func scanInstant(value interface{}) (time.Time, bool) {
	switch instant := value.(type) {
	case time.Time:
		return instant.UTC(), true
	case *time.Time:
		if instant != nil {
			return instant.UTC(), true
		}
	}
	return time.Time{}, false
}

// receiptRow is one stored contract of the requesting participant.
type receiptRow struct {
	id           string
	key          string
	role         string
	counterparty string
	object       string
	amount       int64
	expires      interface{}
	posted       interface{}
	escrow       string
}

// GetReceipt resolves one owned formal trade with its tithe split
// and its compensations, if any. Another participant's contracts and
// unknown ids resolve to absence: receipts never leak across owners.
func (r *TradeReceiptRepository) GetReceipt(ctx context.Context, account, id string) (*application.TradeReceipt, error) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(id) == "" {
		return nil, commercedomain.ErrContractNotFound
	}
	row, err := readReceiptRow(ctx, r.pool, account, id)
	if err != nil || row == nil {
		return nil, err
	}
	return assembleReceipt(ctx, r.pool, *row)
}

// Statement resolves the participant extract: owned trade lines in
// reverse posting order. The limit stays within (0, 100]; unknown
// participants read an empty extract, never an error.
func (r *TradeReceiptRepository) Statement(ctx context.Context, account string, limit int) (*application.TradeStatement, error) {
	if strings.TrimSpace(account) == "" {
		return nil, commercedomain.ErrContractNotFound
	}
	if limit <= 0 || limit > 100 {
		return nil, commercedomain.ErrInvalidReceipt
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id::text, contract_key, buyer_id::text, provider_id::text, amount_milli, posted_at
		 FROM app.commerce_contracts
		 WHERE buyer_id = $1::uuid OR provider_id = $1::uuid
		 ORDER BY posted_at DESC, id DESC
		 LIMIT $2`, account, limit)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return &application.TradeStatement{}, nil
		}
		return nil, fmt.Errorf("list trade contracts: %w", err)
	}
	defer rows.Close()
	statement := &application.TradeStatement{}
	for rows.Next() {
		var entry application.TradeStatementEntry
		var buyer, provider string
		var amount int64
		var posted interface{}
		if err := rows.Scan(&entry.ContractID, &entry.ContractKey, &buyer, &provider, &amount, &posted); err != nil {
			return nil, fmt.Errorf("scan statement entry: %w", err)
		}
		entry.Role = receiptRole(account, buyer, provider)
		entry.GrossMilli = amount
		status, err := latestReceiptStatus(ctx, r.pool, entry.ContractID)
		if err != nil {
			return nil, err
		}
		entry.Status = status
		if t, ok := scanInstant(posted); ok {
			entry.PostedAt = t
		}
		statement.Entries = append(statement.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate statement: %w", err)
	}
	if statement.Entries == nil {
		statement.Entries = []application.TradeStatementEntry{}
	}
	return statement, nil
}

// receiptRole names the requesting side of one contract.
func receiptRole(account, buyer, provider string) string {
	if account == provider {
		return "provider"
	}
	return "buyer"
}

// readReceiptRow resolves one stored contract of the requesting
// participant without joining third parties.
func readReceiptRow(ctx context.Context, q receiptQuerier, account, id string) (*receiptRow, error) {
	var row receiptRow
	var buyer, provider string
	err := q.QueryRow(ctx,
		`SELECT id::text, contract_key, buyer_id::text, provider_id::text, object,
		  amount_milli, expires_at, posted_at, escrow_transfer_id::text
		 FROM app.commerce_contracts
		 WHERE id = $1::uuid AND (buyer_id = $2::uuid OR provider_id = $2::uuid)`,
		id, account).Scan(&row.id, &row.key, &buyer, &provider, &row.object,
		&row.amount, &row.expires, &row.posted, &row.escrow)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, commercedomain.ErrContractNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, commercedomain.ErrContractNotFound
		}
		return nil, fmt.Errorf("read trade contract: %w", err)
	}
	row.role = receiptRole(account, buyer, provider)
	if row.role == "provider" {
		row.counterparty = buyer
	} else {
		row.counterparty = provider
	}
	return &row, nil
}

// latestReceiptStep resolves the latest lifecycle step of one
// contract: the action with its transfer and instant, if any.
func latestReceiptStep(ctx context.Context, q receiptQuerier, contractID string) (action string, transfer *string, posted interface{}, found bool, err error) {
	var transferOut *string
	var postedOut interface{}
	err = q.QueryRow(ctx,
		`SELECT action, transfer_id::text, posted_at
		 FROM app.commerce_settlements WHERE contract_id = $1::uuid
		 ORDER BY posted_at DESC, id DESC LIMIT 1`,
		contractID).Scan(&action, &transferOut, &postedOut)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, nil, false, nil
		}
		return "", nil, nil, false, fmt.Errorf("read settlement status: %w", err)
	}
	return action, transferOut, postedOut, true, nil
}

// latestReceiptStatus derives the lifecycle status from the latest
// step: funded with no steps yet.
func latestReceiptStatus(ctx context.Context, q receiptQuerier, contractID string) (commercedomain.ContractStatus, error) {
	action, _, _, found, err := latestReceiptStep(ctx, q, contractID)
	if err != nil {
		return "", err
	}
	if !found {
		return commercedomain.ContractFunded, nil
	}
	return statusForAction(action)
}

// assembleReceipt binds one stored contract to its split and its
// compensations: the full evidence of one formal trade. The tithe
// split prices only provider payments (release, resolve-release);
// unreleased escrow and buyer refunds carry zero tithe and net.
func assembleReceipt(ctx context.Context, q receiptQuerier, row receiptRow) (*application.TradeReceipt, error) {
	receipt := &application.TradeReceipt{
		ContractID: row.id, ContractKey: row.key, Role: row.role,
		Counterparty: row.counterparty, Object: row.object,
		GrossMilli: row.amount, EscrowTransferID: row.escrow,
		Status: commercedomain.ContractFunded,
	}
	if t, ok := scanInstant(row.expires); ok {
		receipt.ExpiresAt = t
	}
	if t, ok := scanInstant(row.posted); ok {
		receipt.PostedAt = t
	}
	action, transfer, posted, found, err := latestReceiptStep(ctx, q, row.id)
	if err != nil {
		return nil, err
	}
	if !found {
		return receipt, nil
	}
	status, err := statusForAction(action)
	if err != nil {
		return nil, fmt.Errorf("stored settlement holds action %q: %w", action, err)
	}
	receipt.Status = status
	if posted != nil {
		if t, ok := scanInstant(posted); ok {
			settled := t
			receipt.SettledAt = &settled
		}
	}
	if transfer != nil {
		settlement := *transfer
		receipt.SettlementTransferID = &settlement
	}
	if action == "release" || action == "resolve-release" {
		tithe, net, err := commercedomain.SplitTithe(row.amount)
		if err != nil {
			return nil, err
		}
		receipt.TitheMilli = tithe
		receipt.NetMilli = net
	}
	refunds, refunded, err := scanReceiptRefunds(ctx, q, row.id)
	if err != nil {
		return nil, err
	}
	receipt.Refunds = refunds
	receipt.RefundedMilli = refunded
	return receipt, nil
}

// scanReceiptRefunds lists the proportional compensations of one
// contract in posting order.
func scanReceiptRefunds(ctx context.Context, q receiptQuerier, contractID string) ([]application.RefundLine, int64, error) {
	rows, err := q.Query(ctx,
		`SELECT refund_key, amount_milli, tithe_reversal_milli, provider_share_milli,
		  obligation_milli, posted_at
		 FROM app.commerce_service_refunds WHERE contract_id = $1::uuid
		 ORDER BY posted_at, id`, contractID)
	if err != nil {
		return nil, 0, fmt.Errorf("read service refunds: %w", err)
	}
	defer rows.Close()
	lines := []application.RefundLine{}
	var refunded int64
	for rows.Next() {
		var line application.RefundLine
		var posted interface{}
		if err := rows.Scan(&line.RefundKey, &line.AmountMill, &line.TitheReversal,
			&line.ProviderShare, &line.ObligationMill, &posted); err != nil {
			return nil, 0, fmt.Errorf("scan service refund: %w", err)
		}
		if t, ok := scanInstant(posted); ok {
			line.PostedAt = t
		}
		refunded += line.AmountMill
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate service refunds: %w", err)
	}
	return lines, refunded, nil
}
