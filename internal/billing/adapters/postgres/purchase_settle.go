package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Purchase settlement surface of the billing adapter (P35-T06): one
// authenticated provider event moves the committed INK to the buyer
// exactly once. Amount, currency, intent and timeliness confer
// against the sealed intent and the signature headers; success pages,
// stale, duplicated, out-of-order and unknown deliveries never grant
// extra INK.
var _ application.PurchaseSettlementRepository = (*PurchaseSettlementRepository)(nil)

// PurchaseSettlementRepository settles paid provider events against
// PostgreSQL.
type PurchaseSettlementRepository struct {
	pool     *pgxpool.Pool
	clock    ports.Clock
	verifier application.WebhookPayloadVerifier
}

// NewPurchaseSettlementRepository builds the repository with explicit
// wiring: the pool, the clock stamping settlements and the provider
// verifier authenticating every delivery. No delivery is trusted
// without its signature.
func NewPurchaseSettlementRepository(pool *pgxpool.Pool, clock ports.Clock, verifier application.WebhookPayloadVerifier) (*PurchaseSettlementRepository, error) {
	if pool == nil || clock == nil || verifier == nil {
		return nil, application.ErrInvalidPurchaseIntentConfig
	}
	return &PurchaseSettlementRepository{pool: pool, clock: clock, verifier: verifier}, nil
}

// settleEvent is the wire shape one provider delivery carries: the
// intent key and account it settles for, the charged amount with its
// currency, the provider status and the unique event identifier.
// Unknown fields refuse: a delivery carrying more than this contract
// is not the settlement this port speaks.
type settleEvent struct {
	IntentKey string `json:"intent_key"`
	AccountID string `json:"account_id"`
	Amount    int64  `json:"amount_minor"`
	Currency  string `json:"currency"`
	Status    string `json:"status"`
	EventID   string `json:"event_id"`
}

// settleIntent is one stored acceptance as the settlement reads it.
type settleIntent struct {
	id     string
	fiat   int64
	ink    int64
	holdID string
}

// SettlePurchase settles one paid event with the committed INK in one
// transaction: the hold pays the buyer and the settlement row names
// the event beside the intent, or nothing moves at all. Redeliveries
// and out-of-order events resolve the recorded settlement untouched;
// non-paying, unknown, divergent and stale deliveries refuse.
func (r *PurchaseSettlementRepository) SettlePurchase(ctx context.Context, request application.SettlePurchaseRequest) (*application.SettlePurchaseResult, error) {
	if err := r.verifier.Verify(request.Payload, request.SignatureHeader, request.TimestampHeader); err != nil {
		return nil, err
	}
	event, err := parseSettleEvent(request.Payload)
	if err != nil {
		return nil, err
	}
	if event.Status != "paid" {
		// Gateway verdicts record even when they settle nothing: a
		// failed event is the proof a later reconciliation needs, but
		// only for an intent that exists, and navigations never
		// record. Non-paying deliveries still refuse.
		if event.Status == "failed" {
			if err := r.recordChargeEvent(ctx, event); err != nil {
				return nil, err
			}
		}
		return nil, application.ErrPurchaseEventNotSettling
	}
	if replayed, err := r.lookupSettlement(ctx, event, ""); err != nil || replayed != nil {
		return replayed, err
	}
	intent, err := r.lookupSettleIntent(ctx, event)
	if err != nil {
		return nil, err
	}
	if replayed, err := r.lookupSettlement(ctx, event, intent.id); err != nil || replayed != nil {
		return replayed, err
	}
	if event.Amount != intent.fiat || event.Currency != domain.CurrencyBRL.String() {
		return nil, application.ErrPurchaseSettlementMismatch
	}
	var last error
	for range 2 {
		result, err := r.createSettlement(ctx, event, intent)
		if err == nil {
			return result, nil
		}
		if !isSettlementConflict(err) {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupSettlement(ctx, event, intent.id); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("purchase unsettled after conflict: %w", last)
}

// parseSettleEvent decodes one delivery strictly: every field present
// with an identifiable event, or the delivery is malformed.
func parseSettleEvent(payload []byte) (*settleEvent, error) {
	var event settleEvent
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return nil, fmt.Errorf("%w: delivery is not a settlement event", application.ErrWebhookPayloadMalformed)
	}
	if event.IntentKey == "" || event.AccountID == "" || event.EventID == "" {
		return nil, fmt.Errorf("%w: delivery names no intent, account or event", application.ErrWebhookPayloadMalformed)
	}
	return &event, nil
}

// recordChargeEvent stores one gateway verdict idempotently for an
// existing intent: redeliveries replay, and verdicts for unknown
// intents never record.
func (r *PurchaseSettlementRepository) recordChargeEvent(ctx context.Context, event *settleEvent) error {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT true FROM app.billing_ink_intents WHERE account_id = $1::uuid AND intent_key = $2`,
		event.AccountID, event.IntentKey).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrPurchaseIntentNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return application.ErrPurchaseIntentNotFound
		}
		return fmt.Errorf("resolve event intent: %w", err)
	}
	now := r.clock.Now().UTC()
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ($1, $2, $3::uuid, $4, $5, $6, $7) ON CONFLICT (event_id) DO NOTHING`,
		event.EventID, event.IntentKey, event.AccountID,
		event.Amount, event.Currency, event.Status, now); err != nil {
		return fmt.Errorf("record charge event: %w", err)
	}
	return nil
}

// lookupSettlement resolves a recorded settlement without writing:
// by provider event, or by intent when the event is new to a settled
// intent. Either hit replays untouched, so redeliveries and
// out-of-order events never pay twice.
func (r *PurchaseSettlementRepository) lookupSettlement(ctx context.Context, event *settleEvent, intentID string) (*application.SettlePurchaseResult, error) {
	query := `SELECT id::text, intent_id::text, amount_minor FROM app.billing_ink_settlements WHERE event_id = $1`
	args := []any{event.EventID}
	if intentID != "" {
		query = `SELECT id::text, intent_id::text, amount_minor FROM app.billing_ink_settlements WHERE event_id = $1 OR intent_id = $2::uuid`
		args = append(args, intentID)
	}
	var result application.SettlePurchaseResult
	var amount int64
	err := r.pool.QueryRow(ctx, query, args...).Scan(&result.SettlementID, &result.IntentID, &amount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup settlement: %w", err)
	}
	var ink int64
	if err := r.pool.QueryRow(ctx,
		`SELECT ink_milli FROM app.billing_ink_intents WHERE id = $1::uuid`, result.IntentID).Scan(&ink); err != nil {
		return nil, fmt.Errorf("read settled intent: %w", err)
	}
	result.InkMilli = ink
	result.Replayed = true
	return &result, nil
}

// lookupSettleIntent resolves the acceptance the event settles for,
// or reports its absence. The key scopes by account, so one account
// can never settle through another account's key.
func (r *PurchaseSettlementRepository) lookupSettleIntent(ctx context.Context, event *settleEvent) (*settleIntent, error) {
	var intent settleIntent
	var holdID *string
	err := r.pool.QueryRow(ctx,
		`SELECT id::text, fiat_minor, ink_milli, hold_id::text
		 FROM app.billing_ink_intents WHERE account_id = $1::uuid AND intent_key = $2`,
		event.AccountID, event.IntentKey).Scan(&intent.id, &intent.fiat, &intent.ink, &holdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrPurchaseIntentNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, application.ErrPurchaseIntentNotFound
		}
		return nil, fmt.Errorf("lookup purchase intent: %w", err)
	}
	if holdID == nil {
		return nil, fmt.Errorf("settled intent %s carries no hold", intent.id)
	}
	intent.holdID = *holdID
	return &intent, nil
}

// createSettlement attempts the liquidation once: the hold pays the
// buyer, closes captured, and the settlement row names the event,
// sharing one transaction. A hold that already left active resolves
// through re-lookup instead of paying twice.
func (r *PurchaseSettlementRepository) createSettlement(ctx context.Context, event *settleEvent, intent *settleIntent) (*application.SettlePurchaseResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin settle purchase transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var holdCustody, status string
	var holdAmount int64
	err = tx.QueryRow(ctx,
		`SELECT hold_custody_id::text, amount_milli, status FROM app.economy_holds WHERE id = $1::uuid FOR UPDATE`,
		intent.holdID).Scan(&holdCustody, &holdAmount, &status)
	if err != nil {
		return nil, fmt.Errorf("lock backing hold: %w", err)
	}
	if status != "active" || holdAmount != intent.ink {
		if err := tx.Rollback(ctx); err != nil {
			return nil, fmt.Errorf("abort spent hold: %w", err)
		}
		if replayed, err := r.lookupSettlement(ctx, event, intent.id); err != nil || replayed != nil {
			return replayed, err
		}
		return nil, fmt.Errorf("backing hold left active without a settlement for intent %s", intent.id)
	}
	var buyer string
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1) ON CONFLICT DO NOTHING`,
		event.AccountID); err != nil {
		return nil, fmt.Errorf("open buyer custody: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'user' AND label = $1`,
		event.AccountID).Scan(&buyer); err != nil {
		return nil, fmt.Errorf("resolve buyer custody: %w", err)
	}
	var transfer string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4), ($1::uuid, $3::uuid, 'credit', $4)`,
		transfer, holdCustody, buyer, intent.ink); err != nil {
		return nil, fmt.Errorf("record settlement legs: %w", err)
	}
	now := r.clock.Now().UTC()
	if _, err := tx.Exec(ctx,
		`UPDATE app.economy_holds SET status = 'captured', closed_at = $2 WHERE id = $1::uuid`,
		intent.holdID, now); err != nil {
		return nil, fmt.Errorf("capture backing hold: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ($1, $2, $3::uuid, $4, $5, 'paid', $6) ON CONFLICT (event_id) DO NOTHING`,
		event.EventID, event.IntentKey, event.AccountID,
		event.Amount, event.Currency, now); err != nil {
		return nil, fmt.Errorf("record charge event: %w", err)
	}
	var settlementID string
	err = tx.QueryRow(ctx,
		`INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, $2, $3, $4, $5) RETURNING id::text`,
		intent.id, event.EventID, event.Amount, event.Currency, now).Scan(&settlementID)
	if err != nil {
		return nil, fmt.Errorf("record settlement: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit settle purchase: %w", err)
	}
	return &application.SettlePurchaseResult{
		IntentID: intent.id, SettlementID: settlementID, InkMilli: intent.ink,
	}, nil
}

// isSettlementConflict reports whether err is a unique collision that
// a re-lookup can resolve: a concurrent delivery of the same event,
// or a concurrent event for the same intent.
func isSettlementConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "billing_ink_settlements_event_unique" ||
		pgErr.ConstraintName == "billing_ink_settlements_intent_unique"
}
