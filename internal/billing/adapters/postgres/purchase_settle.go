package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	season string
	endsAt time.Time
}

// SettlePurchase settles one paid event with the committed INK in one
// transaction: the hold pays the buyer and the settlement row names
// the event beside the intent, or nothing moves at all. Redeliveries
// and out-of-order events resolve the recorded settlement untouched;
// non-paying, unknown, divergent and stale deliveries refuse. A paid
// event arriving after the book end registers a provider
// refund/reconciliation case instead: the charge event records, the
// intent waits in review, and no credit lands in the new book nor any
// spend leaves the sealed book. No HTTP is awaited under the book
// lock.
func (r *PurchaseSettlementRepository) SettlePurchase(ctx context.Context, request application.SettlePurchaseRequest) (*application.SettlePurchaseResult, error) {
	if err := r.verifier.Verify(request.Payload, request.SignatureHeader, request.TimestampHeader); err != nil {
		return nil, err
	}
	event, err := parseSettleEvent(request.Payload)
	if err != nil {
		return nil, err
	}
	if result, err := r.refuseNonPaying(ctx, event); err != nil || result != nil {
		return result, err
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
	if err := r.refuseAfterCutoff(ctx, event, intent); err != nil {
		return nil, err
	}
	return r.settleWithRetry(ctx, event, intent)
}

// refuseNonPaying records gateway verdicts that settle nothing and
// refuses the delivery. Paid events return nil nil to continue.
func (r *PurchaseSettlementRepository) refuseNonPaying(ctx context.Context, event *settleEvent) (*application.SettlePurchaseResult, error) {
	if event.Status == "paid" {
		return nil, nil
	}
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

// refuseAfterCutoff registers a paid event that arrived after the
// book end as a provider refund/reconciliation case and refuses the
// credit. Open books return nil to continue.
func (r *PurchaseSettlementRepository) refuseAfterCutoff(ctx context.Context, event *settleEvent, intent *settleIntent) error {
	if intent.season == domain.CompatSeasonKey {
		return nil
	}
	if !r.clock.Now().UTC().After(intent.endsAt.UTC()) && !isPurchaseBookSealed(ctx, r.pool, intent.season) {
		return nil
	}
	if err := r.recordLateSettlementCase(ctx, event, intent); err != nil {
		return err
	}
	return application.ErrPurchaseAfterCutoff
}

// settleWithRetry liquidates one intent with one retry through
// re-lookup, so concurrent deliveries of one event collapse to one
// settlement instead of paying twice.
func (r *PurchaseSettlementRepository) settleWithRetry(ctx context.Context, event *settleEvent, intent *settleIntent) (*application.SettlePurchaseResult, error) {
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
	query := `SELECT id::text, intent_id::text, amount_minor, season_key FROM app.billing_ink_settlements WHERE event_id = $1`
	args := []any{event.EventID}
	if intentID != "" {
		query = `SELECT id::text, intent_id::text, amount_minor, season_key FROM app.billing_ink_settlements WHERE event_id = $1 OR intent_id = $2::uuid`
		args = append(args, intentID)
	}
	var result application.SettlePurchaseResult
	var amount int64
	var seasonKey string
	err := r.pool.QueryRow(ctx, query, args...).Scan(&result.SettlementID, &result.IntentID, &amount, &seasonKey)
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
	season, err := domain.ParseSeasonKey(seasonKey)
	if err != nil {
		return nil, fmt.Errorf("stored settlement book %q: %w", seasonKey, err)
	}
	result.InkMilli = ink
	result.Replayed = true
	result.Season = season
	return &result, nil
}

// lookupSettleIntent resolves the acceptance the event settles for,
// or reports its absence. The key scopes by account, so one account
// can never settle through another account's key.
func (r *PurchaseSettlementRepository) lookupSettleIntent(ctx context.Context, event *settleEvent) (*settleIntent, error) {
	var intent settleIntent
	var holdID *string
	err := r.pool.QueryRow(ctx,
		`SELECT i.id::text, i.fiat_minor, i.ink_milli, i.hold_id::text, i.season_key, s.ends_at
		 FROM app.billing_ink_intents i JOIN app.seasons s ON s.season_key = i.season_key
		 WHERE i.account_id = $1::uuid AND i.intent_key = $2`,
		event.AccountID, event.IntentKey).Scan(&intent.id, &intent.fiat, &intent.ink, &holdID, &intent.season, &intent.endsAt)
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
	intent.endsAt = intent.endsAt.UTC()
	return &intent, nil
}

// isPurchaseBookSealed reports whether one purchase book carries a
// terminal lifecycle stage: closing, sealed or archived books never
// settle new credit.
func isPurchaseBookSealed(ctx context.Context, q purchaseBookQuerier, season string) bool {
	var sealed bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.season_lifecycle WHERE season_key = $1 AND to_state IN ('closing','sealed','archived'))`,
		season).Scan(&sealed); err != nil {
		return false
	}
	return sealed
}

// recordLateSettlementCase registers a paid event that arrived after
// the book end as a provider refund/reconciliation case: the charge
// event records, the intent waits in review, and no leg moves. The
// sealed book is never spent and the new book never credited.
func (r *PurchaseSettlementRepository) recordLateSettlementCase(ctx context.Context, event *settleEvent, intent *settleIntent) error {
	now := r.clock.Now().UTC()
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ($1, $2, $3::uuid, $4, $5, 'paid', $6) ON CONFLICT (event_id) DO NOTHING`,
		event.EventID, event.IntentKey, event.AccountID,
		event.Amount, event.Currency, now); err != nil {
		return fmt.Errorf("record late charge event: %w", err)
	}
	if _, err := r.pool.Exec(ctx,
		`UPDATE app.billing_ink_intents SET status = 'review' WHERE id = $1::uuid AND status = 'pending'`,
		intent.id); err != nil {
		return fmt.Errorf("hold late intent for review: %w", err)
	}
	return nil
}

// createSettlement attempts the liquidation once: the hold pays the
// buyer, closes captured, and the settlement row names the event,
// sharing one transaction. A hold that already left active resolves
// through re-lookup instead of paying twice. Buyer, legs and receipt
// all carry the intent book; a cutoff landing between the check and
// the write still refuses inside the same transaction, and no HTTP
// is awaited under the book lock.
func (r *PurchaseSettlementRepository) createSettlement(ctx context.Context, event *settleEvent, intent *settleIntent) (*application.SettlePurchaseResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin settle purchase transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := checkSettlementCutoffTx(ctx, tx, r.clock.Now().UTC(), intent); err != nil {
		return nil, err
	}
	holdCustody, status, holdSeason, holdAmount, err := lockSettlementHoldTx(ctx, tx, intent.holdID)
	if err != nil {
		return nil, err
	}
	if status != "active" || holdAmount != intent.ink || holdSeason != intent.season {
		if err := tx.Rollback(ctx); err != nil {
			return nil, fmt.Errorf("abort spent hold: %w", err)
		}
		if replayed, err := r.lookupSettlement(ctx, event, intent.id); err != nil || replayed != nil {
			return replayed, err
		}
		return nil, fmt.Errorf("backing hold left active without a settlement for intent %s", intent.id)
	}
	buyer, err := openSeasonalBuyer(ctx, tx, event.AccountID, intent.season)
	if err != nil {
		return nil, err
	}
	var transfer string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		return nil, fmt.Errorf("generate transfer id: %w", err)
	}
	if err := recordSettlementLegs(ctx, tx, settlementLegs{transfer: transfer, holdCustody: holdCustody, buyer: buyer, ink: intent.ink, season: intent.season}); err != nil {
		return nil, err
	}
	now := r.clock.Now().UTC()
	settlementID, err := recordSettlementRows(ctx, tx, now, event, intent)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit settle purchase: %w", err)
	}
	season, err := domain.ParseSeasonKey(intent.season)
	if err != nil {
		return nil, fmt.Errorf("settled book %q: %w", intent.season, err)
	}
	return &application.SettlePurchaseResult{
		IntentID: intent.id, SettlementID: settlementID, InkMilli: intent.ink, Season: season,
	}, nil
}

// checkSettlementCutoffTx refuses a liquidation whose book closed
// between the outer check and the write. Open and legacy books pass.
func checkSettlementCutoffTx(ctx context.Context, tx pgx.Tx, now time.Time, intent *settleIntent) error {
	if intent.season == domain.CompatSeasonKey {
		return nil
	}
	if now.After(intent.endsAt.UTC()) {
		return application.ErrPurchaseAfterCutoff
	}
	var sealed bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.season_lifecycle WHERE season_key = $1 AND to_state IN ('closing','sealed','archived'))`,
		intent.season).Scan(&sealed); err != nil {
		return fmt.Errorf("read book lifecycle: %w", err)
	}
	if sealed {
		return application.ErrPurchaseAfterCutoff
	}
	return nil
}

// lockSettlementHoldTx locks the backing hold for one liquidation.
func lockSettlementHoldTx(ctx context.Context, tx pgx.Tx, holdID string) (holdCustody, status, holdSeason string, holdAmount int64, err error) {
	err = tx.QueryRow(ctx,
		`SELECT hold_custody_id::text, amount_milli, status, season_key FROM app.economy_holds WHERE id = $1::uuid FOR UPDATE`,
		holdID).Scan(&holdCustody, &holdAmount, &status, &holdSeason)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("lock backing hold: %w", err)
	}
	return holdCustody, status, holdSeason, holdAmount, nil
}

// openSeasonalBuyer opens the buyer custody in the intent book.
func openSeasonalBuyer(ctx context.Context, tx pgx.Tx, accountID, season string) (string, error) {
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', $1, $2) ON CONFLICT DO NOTHING`,
		accountID, season); err != nil {
		return "", fmt.Errorf("open buyer custody: %w", err)
	}
	var buyer string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'user' AND label = $1 AND season_key = $2`,
		accountID, season).Scan(&buyer); err != nil {
		return "", fmt.Errorf("resolve buyer custody: %w", err)
	}
	return buyer, nil
}

// settlementLegs carries one hold-to-buyer debit/credit pair in the
// intent book.
type settlementLegs struct {
	transfer    string
	holdCustody string
	buyer       string
	ink         int64
	season      string
}

// recordSettlementLegs writes the hold-to-buyer debit/credit pair in
// the intent book.
func recordSettlementLegs(ctx context.Context, tx pgx.Tx, legs settlementLegs) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4, $5), ($1::uuid, $3::uuid, 'credit', $4, $5)`,
		legs.transfer, legs.holdCustody, legs.buyer, legs.ink, legs.season); err != nil {
		return fmt.Errorf("record settlement legs: %w", err)
	}
	return nil
}

// recordSettlementRows captures the hold, records the charge event
// and the settlement receipt in the intent book.
func recordSettlementRows(ctx context.Context, tx pgx.Tx, now time.Time, event *settleEvent, intent *settleIntent) (string, error) {
	if _, err := tx.Exec(ctx,
		`UPDATE app.economy_holds SET status = 'captured', closed_at = $2 WHERE id = $1::uuid`,
		intent.holdID, now); err != nil {
		return "", fmt.Errorf("capture backing hold: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ($1, $2, $3::uuid, $4, $5, 'paid', $6) ON CONFLICT (event_id) DO NOTHING`,
		event.EventID, event.IntentKey, event.AccountID,
		event.Amount, event.Currency, now); err != nil {
		return "", fmt.Errorf("record charge event: %w", err)
	}
	var settlementID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at, season_key)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6) RETURNING id::text`,
		intent.id, event.EventID, event.Amount, event.Currency, now, intent.season).Scan(&settlementID); err != nil {
		return "", fmt.Errorf("record settlement: %w", err)
	}
	return settlementID, nil
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
