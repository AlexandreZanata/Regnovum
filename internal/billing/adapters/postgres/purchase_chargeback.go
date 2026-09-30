package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Chargeback surface of the billing adapter (P35-T08): one disputed
// provider event reverses its liquidation with linked compensation.
// The holder answers up to their balance, the operator covers the
// remainder from operating cash, and the chargeback row names the
// dispute beside the settlement, or nothing moves at all. Good-faith
// third parties are never debited: only the holder and the two
// Treasury vaults ever appear in these legs, and short treasuries
// refuse instead of minting.
var _ application.ChargebackRepository = (*ChargebackRepository)(nil)

// ChargebackRepository reverses disputed liquidations against
// PostgreSQL.
type ChargebackRepository struct {
	pool     *pgxpool.Pool
	clock    ports.Clock
	verifier application.WebhookPayloadVerifier
}

// NewChargebackRepository builds the repository with explicit wiring:
// the pool, the clock stamping chargebacks and the provider verifier
// authenticating every delivery. No delivery is trusted without its
// signature.
func NewChargebackRepository(pool *pgxpool.Pool, clock ports.Clock, verifier application.WebhookPayloadVerifier) (*ChargebackRepository, error) {
	if pool == nil || clock == nil || verifier == nil {
		return nil, application.ErrInvalidPurchaseIntentConfig
	}
	return &ChargebackRepository{pool: pool, clock: clock, verifier: verifier}, nil
}

// disputeEvent is the wire shape one provider dispute carries: the
// intent key and account it reverses, the contested fiat amount with
// its currency, the dispute status and the unique event identifier.
// Unknown fields refuse: a delivery carrying more than this contract
// is not the dispute this port speaks.
type disputeEvent struct {
	IntentKey string `json:"intent_key"`
	AccountID string `json:"account_id"`
	Amount    int64  `json:"amount_minor"`
	Currency  string `json:"currency"`
	Status    string `json:"status"`
	EventID   string `json:"event_id"`
}

// disputeSettlement is one liquidated acceptance as the dispute reads
// it.
type disputeSettlement struct {
	intentID   string
	fiat       int64
	ink        int64
	settlement string
}

// SettleChargeback reverses one disputed liquidation with linked
// compensation in one transaction: the holder balance pays back up
// to the INK due, operating cash covers the remainder, and the
// chargeback row names the dispute beside the settlement. Redeliveries
// resolve the recorded chargeback untouched; unknown, divergent,
// stale, uncovered and unliquidated disputes refuse.
func (r *ChargebackRepository) SettleChargeback(ctx context.Context, request application.SettleChargebackRequest) (*application.SettleChargebackResult, error) {
	if err := r.verifier.Verify(request.Payload, request.SignatureHeader, request.TimestampHeader); err != nil {
		return nil, err
	}
	event, err := parseDisputeEvent(request.Payload)
	if err != nil {
		return nil, err
	}
	if event.Status != "disputed" {
		return nil, fmt.Errorf("%w: this port settles disputes, not %q", application.ErrPurchaseSettlementMismatch, event.Status)
	}
	if replayed, err := r.lookupChargeback(ctx, event, ""); err != nil || replayed != nil {
		return replayed, err
	}
	settlement, err := r.lookupDisputedSettlement(ctx, event)
	if err != nil {
		return nil, err
	}
	if replayed, err := r.lookupChargeback(ctx, event, settlement.settlement); err != nil || replayed != nil {
		return replayed, err
	}
	due, err := prorateDispute(event.Amount, settlement)
	if err != nil {
		return nil, err
	}
	var last error
	for range 2 {
		result, err := r.createChargeback(ctx, event, settlement, due)
		if err == nil {
			return result, nil
		}
		if !isChargebackConflict(err) {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupChargeback(ctx, event, settlement.settlement); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("chargeback unsettled after conflict: %w", last)
}

// parseDisputeEvent decodes one delivery strictly: every field
// present with an identifiable event, or the delivery is malformed.
func parseDisputeEvent(payload []byte) (*disputeEvent, error) {
	var event disputeEvent
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return nil, fmt.Errorf("%w: delivery is not a dispute event", application.ErrWebhookPayloadMalformed)
	}
	if event.IntentKey == "" || event.AccountID == "" || event.EventID == "" {
		return nil, fmt.Errorf("%w: delivery names no intent, account or event", application.ErrWebhookPayloadMalformed)
	}
	return &event, nil
}

// lookupChargeback resolves a recorded chargeback without writing:
// by provider event, or by settlement when the event is new to a
// disputed liquidation. Either hit replays untouched, so
// redeliveries and second disputes never revoke twice.
func (r *ChargebackRepository) lookupChargeback(ctx context.Context, event *disputeEvent, settlementID string) (*application.SettleChargebackResult, error) {
	query := `SELECT id::text, ink_revoked, treasury_covered FROM app.billing_ink_chargebacks WHERE event_id = $1`
	args := []any{event.EventID}
	if settlementID != "" {
		query = `SELECT id::text, ink_revoked, treasury_covered FROM app.billing_ink_chargebacks WHERE event_id = $1 OR settlement_id = $2::uuid`
		args = append(args, settlementID)
	}
	var result application.SettleChargebackResult
	err := r.pool.QueryRow(ctx, query, args...).Scan(&result.ChargebackID, &result.InkRevoked, &result.TreasuryCover)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup chargeback: %w", err)
	}
	chargeback, err := r.readChargebackEvent(ctx, result.ChargebackID)
	if err != nil {
		return nil, err
	}
	if chargeback != event.EventID {
		return nil, application.ErrChargebackConflict
	}
	result.Replayed = true
	return &result, nil
}

// readChargebackEvent resolves the provider event one chargeback
// recorded, distinguishing a redelivery from a divergent dispute.
func (r *ChargebackRepository) readChargebackEvent(ctx context.Context, chargebackID string) (string, error) {
	var eventID string
	err := r.pool.QueryRow(ctx,
		`SELECT event_id FROM app.billing_ink_chargebacks WHERE id = $1::uuid`, chargebackID).Scan(&eventID)
	if err != nil {
		return "", fmt.Errorf("read chargeback event: %w", err)
	}
	return eventID, nil
}

// lookupDisputedSettlement resolves the liquidation the dispute
// reverses: the intent key scoped by account with its settlement. An
// unknown intent reports absence; an intent without liquidation has
// nothing to reverse.
func (r *ChargebackRepository) lookupDisputedSettlement(ctx context.Context, event *disputeEvent) (*disputeSettlement, error) {
	var settlement disputeSettlement
	var settlementID *string
	err := r.pool.QueryRow(ctx,
		`SELECT i.id::text, i.fiat_minor, i.ink_milli, s.id::text
		 FROM app.billing_ink_intents i LEFT JOIN app.billing_ink_settlements s ON s.intent_id = i.id
		 WHERE i.account_id = $1::uuid AND i.intent_key = $2`,
		event.AccountID, event.IntentKey).Scan(&settlement.intentID, &settlement.fiat, &settlement.ink, &settlementID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrPurchaseIntentNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, application.ErrPurchaseIntentNotFound
		}
		return nil, fmt.Errorf("lookup disputed intent: %w", err)
	}
	if settlementID == nil {
		return nil, application.ErrSettlementNotFound
	}
	settlement.settlement = *settlementID
	return &settlement, nil
}

// prorateDispute prices the INK due from the contested fiat share
// with floor: a full dispute revokes the whole grant, a partial one
// the floored share. Zero, over-full and foreign tickets refuse,
// and overflowing products fail closed instead of wrapping.
func prorateDispute(disputed int64, settlement *disputeSettlement) (int64, error) {
	if disputed <= 0 || disputed > settlement.fiat {
		return 0, fmt.Errorf("%w: disputed %d against ticket %d", application.ErrPurchaseSettlementMismatch, disputed, settlement.fiat)
	}
	if disputed == settlement.fiat {
		return settlement.ink, nil
	}
	hi, lo := bits.Mul64(uint64(disputed), uint64(settlement.ink))
	if hi != 0 {
		return 0, fmt.Errorf("%w: disputed share overflows", application.ErrPurchaseSettlementMismatch)
	}
	return int64(lo / uint64(settlement.fiat)), nil
}

// createChargeback attempts the reversal once: the holder pays back
// up to the due amount, operating cash covers the remainder, and the
// chargeback row names the dispute, sharing one transaction. Short
// treasuries refuse with everything untouched, and a unique collision
// retries once through re-lookup.
func (r *ChargebackRepository) createChargeback(ctx context.Context, event *disputeEvent, settlement *disputeSettlement, due int64) (*application.SettleChargebackResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin settle chargeback transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// A concurrent delivery may have settled while this attempt
	// waited: re-resolve before touching any leg, so losers replay
	// instead of revoking twice. The unique guards below still catch
	// whatever slips between this check and the insert.
	if replayed, err := r.lookupChargeback(ctx, event, ""); err != nil || replayed != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return nil, fmt.Errorf("abort replayed chargeback: %w", rbErr)
		}
		return replayed, err
	}

	balance, err := lockedBalance(ctx, tx, "user", event.AccountID)
	if err != nil {
		return nil, err
	}
	revoked := due
	if balance < revoked {
		revoked = balance
	}
	covered := due - revoked
	if covered > 0 {
		treasury, err := lockedBalance(ctx, tx, "treasury", "operating_cash")
		if err != nil {
			return nil, err
		}
		if treasury < covered {
			// A concurrent delivery may have settled while this
			// attempt waited on the locks: resolve it before
			// refusing, so losers replay instead of mistaking a
			// won race for an empty treasury. Roll back first:
			// nothing was written yet.
			if err := tx.Rollback(ctx); err != nil {
				return nil, fmt.Errorf("abort uncovered chargeback: %w", err)
			}
			if replayed, err := r.lookupChargeback(ctx, event, ""); err != nil || replayed != nil {
				return replayed, err
			}
			return nil, application.ErrInsufficientTreasuryFunds
		}
	}
	if err := recordCompensationLegs(ctx, tx, event, revoked, covered); err != nil {
		return nil, err
	}
	var chargebackID string
	now := r.clock.Now().UTC()
	err = tx.QueryRow(ctx,
		`INSERT INTO app.billing_ink_chargebacks
		 (settlement_id, event_id, disputed_minor, ink_revoked, treasury_covered, received_at)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6)
		 RETURNING id::text`,
		settlement.settlement, event.EventID, event.Amount, revoked, covered, now).Scan(&chargebackID)
	if err != nil {
		return nil, fmt.Errorf("record chargeback: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit settle chargeback: %w", err)
	}
	return &application.SettleChargebackResult{
		ChargebackID: chargebackID, InkRevoked: revoked, TreasuryCover: covered,
	}, nil
}

// lockedBalance resolves one custody balance under lock: missing
// custodies read zero, so buyers that never held INK revoke nothing
// instead of failing, and absent vaults count as empty.
func lockedBalance(ctx context.Context, tx pgx.Tx, kind, label string) (int64, error) {
	var id string
	err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2 FOR UPDATE`,
		kind, label).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("lock custody: %w", err)
	}
	var balance int64
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE custody_id = $1::uuid`, id).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("read locked balance: %w", err)
	}
	return balance, nil
}

// recordCompensationLegs writes the holder restitution with the
// operator cover, each compensation under its own transfer: the two
// never share legs, so the journal pairs each debit with its credit
// exactly once.
func recordCompensationLegs(ctx context.Context, tx pgx.Tx, event *disputeEvent, revoked, covered int64) error {
	if revoked > 0 {
		transfer, err := genTransferID(ctx, tx)
		if err != nil {
			return err
		}
		if err := recordLegs(ctx, tx, transfer, []journalLeg{
			{kind: "user", label: event.AccountID, direction: "debit", millis: revoked},
			{kind: "treasury", label: "commercial_stock", direction: "credit", millis: revoked},
		}); err != nil {
			return err
		}
	}
	if covered > 0 {
		transfer, err := genTransferID(ctx, tx)
		if err != nil {
			return err
		}
		if err := recordLegs(ctx, tx, transfer, []journalLeg{
			{kind: "treasury", label: "operating_cash", direction: "debit", millis: covered},
			{kind: "treasury", label: "commercial_stock", direction: "credit", millis: covered},
		}); err != nil {
			return err
		}
	}
	return nil
}

// journalLeg is one journal leg against a resolved custody.
type journalLeg struct {
	kind      string
	label     string
	direction string
	millis    int64
}

// genTransferID mints one transfer identifier inside the caller
// transaction.
func genTransferID(ctx context.Context, tx pgx.Tx) (string, error) {
	var transfer string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		return "", fmt.Errorf("generate transfer id: %w", err)
	}
	return transfer, nil
}

// recordLegs writes journal legs for resolved custodies: the row
// count proves each custody existed, so silent no-ops never pass.
func recordLegs(ctx context.Context, tx pgx.Tx, transfer string, legs []journalLeg) error {
	for _, leg := range legs {
		tag, err := tx.Exec(ctx,
			`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
			 SELECT $1::uuid, id, $4, $5 FROM app.economy_custodies WHERE kind = $2 AND label = $3`,
			transfer, leg.kind, leg.label, leg.direction, leg.millis)
		if err != nil {
			return fmt.Errorf("record %s leg: %w", leg.direction, err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("record %s leg: custody %s/%s missing", leg.direction, leg.kind, leg.label)
		}
	}
	return nil
}

// isChargebackConflict reports whether err is a unique collision that
// a re-lookup can resolve: a concurrent delivery of the same event,
// or a concurrent dispute for the same liquidation.
func isChargebackConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "billing_ink_chargebacks_event_unique" ||
		pgErr.ConstraintName == "billing_ink_chargebacks_settlement_unique"
}
