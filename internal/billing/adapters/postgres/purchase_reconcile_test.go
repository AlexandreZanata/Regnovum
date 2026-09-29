package postgres_test

// P35-T07 — purchase reconciliation without silent fixes on real
// PostgreSQL.
//
// One pass compares gateway events, intents, settlements, holds and
// legs: captured payments without transfer and transfers without
// capture alert without moving anything, proven terminals transition
// with their hold release, and live pending intents wait untouched.
// The tests prove on a disposable database: clean convergence with
// idempotent re-runs, alert plus retry convergence, transfer without
// capture held for review, failed and silent lapses releasing exactly
// once, live intents waiting, and divergences never failing.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func reconcileUseCase(t *testing.T, pool *pgxpool.Pool, clock *intentClock) *application.ReconcilePurchasesUseCase {
	t.Helper()
	repo, err := postgres.NewReconciliationRepository(pool, clock)
	if err != nil {
		t.Fatalf("NewReconciliationRepository: %v", err)
	}
	uc, err := application.NewReconcilePurchasesUseCase(repo)
	if err != nil {
		t.Fatalf("NewReconcilePurchasesUseCase: %v", err)
	}
	return uc
}

func intentStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, intentID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM app.billing_ink_intents WHERE id::text = $1`, intentID).Scan(&status); err != nil {
		t.Fatalf("read intent status: %v", err)
	}
	return status
}

func reconcileIntentID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key, account string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.billing_ink_intents WHERE account_id = $1::uuid AND intent_key = $2`,
		account, key).Scan(&id); err != nil {
		t.Fatalf("resolve intent: %v", err)
	}
	return id
}

// TestReconcileConvergesClean proves the settled path: a liquidated
// intent transitions with no alerts, and a second pass repeats
// nothing.
func TestReconcileConvergesClean(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	settled, err := settleDeliver(t, ctx, world,
		settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-1"), world.clock.now, settleSecret)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	uc := reconcileUseCase(t, pool, world.clock)
	report, err := uc.Execute(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.Alerts) != 0 {
		t.Fatalf("alerts on a clean book: %+v", report.Alerts)
	}
	if len(report.Transitions) != 1 || report.Transitions[0].To != "settled" || report.Transitions[0].Released {
		t.Fatalf("transitions changed: %+v", report.Transitions)
	}
	if got := intentStatus(t, ctx, pool, settled.IntentID); got != "settled" {
		t.Fatalf("intent = %q, want settled", got)
	}
	again, err := uc.Execute(ctx)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(again.Alerts) != 0 || len(again.Transitions) != 0 {
		t.Fatalf("second pass repeated: %+v", again)
	}
}

// TestReconcileAlertsAndRetryConverges proves a captured payment
// without transfer alerts without moving anything, and the event
// retry then liquidates exactly once.
func TestReconcileAlertsAndRetryConverges(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	intentID := reconcileIntentID(t, ctx, pool, world.key, world.account)
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ('evt-orphan', $1, $2::uuid, 10000, 'BRL', 'paid', now())`,
		world.key, world.account); err != nil {
		t.Fatalf("inject captured payment: %v", err)
	}
	uc := reconcileUseCase(t, pool, world.clock)
	report, err := uc.Execute(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.Alerts) != 1 || report.Alerts[0].Kind != application.AlertCapturedWithoutTransfer {
		t.Fatalf("alerts changed: %+v", report.Alerts)
	}
	if len(report.Transitions) != 0 {
		t.Fatalf("alert transitioned: %+v", report.Transitions)
	}
	if got := holdStatus(t, ctx, pool, intentID); got != "active" {
		t.Fatalf("hold moved on alert: %q", got)
	}
	settled, err := settleDeliver(t, ctx, world,
		settleBody(world.key, world.account, world.fiat, "BRL", "paid", "evt-orphan"), world.clock.now, settleSecret)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if settled.Replayed {
		t.Fatalf("retry replayed without settling")
	}
	clean, err := uc.Execute(ctx)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(clean.Alerts) != 0 {
		t.Fatalf("alerts after convergence: %+v", clean.Alerts)
	}
	if len(clean.Transitions) != 1 || clean.Transitions[0].To != "settled" {
		t.Fatalf("transitions changed: %+v", clean.Transitions)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != world.ink {
		t.Fatalf("buyer = %d, want %d once", got, world.ink)
	}
}

// TestReconcileHoldsTransferWithoutCapture proves a settlement
// without a paid event transitions with an alert for human review,
// moving nothing further.
func TestReconcileHoldsTransferWithoutCapture(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	intentID := reconcileIntentID(t, ctx, pool, world.key, world.account)
	var hold string
	var ink int64
	if err := pool.QueryRow(ctx,
		`SELECT h.id::text, h.amount_milli FROM app.economy_holds h
		 JOIN app.billing_ink_intents i ON i.hold_id = h.id
		 WHERE i.id::text = $1`, intentID).Scan(&hold, &ink); err != nil {
		t.Fatalf("read hold: %v", err)
	}
	var buyer string
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1) ON CONFLICT DO NOTHING`, world.account); err != nil {
		t.Fatalf("open buyer: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'user' AND label = $1`, world.account).Scan(&buyer); err != nil {
		t.Fatalf("resolve buyer: %v", err)
	}
	var transfer string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	var holdCustody string
	if err := pool.QueryRow(ctx,
		`SELECT hold_custody_id::text FROM app.economy_holds WHERE id = $1::uuid`, hold).Scan(&holdCustody); err != nil {
		t.Fatalf("resolve hold custody: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $4), ($1::uuid, $3::uuid, 'credit', $4)`,
		transfer, holdCustody, buyer, ink); err != nil {
		t.Fatalf("move legs: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE app.economy_holds SET status = 'captured', closed_at = now() WHERE id = $1::uuid`, hold); err != nil {
		t.Fatalf("capture hold: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, 'evt-ghost', 10000, 'BRL', now())`, intentID); err != nil {
		t.Fatalf("record settlement: %v", err)
	}

	uc := reconcileUseCase(t, pool, world.clock)
	report, err := uc.Execute(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.Alerts) != 1 || report.Alerts[0].Kind != application.AlertTransferWithoutCapture {
		t.Fatalf("alerts changed: %+v", report.Alerts)
	}
	if len(report.Transitions) != 1 || report.Transitions[0].To != "settled" || report.Transitions[0].Released {
		t.Fatalf("transitions changed: %+v", report.Transitions)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != world.ink {
		t.Fatalf("buyer moved on alert: %d", got)
	}
	again, err := uc.Execute(ctx)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(again.Transitions) != 0 {
		t.Fatalf("second pass transitioned again: %+v", again.Transitions)
	}
}

// TestReconcileFailsProvenTerminals proves holds release exactly
// once on terminal failure: a failed verdict with a live quote, and
// a silent lapse past expiry, both return every unit.
func TestReconcileFailsProvenTerminals(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	intentID := reconcileIntentID(t, ctx, pool, world.key, world.account)
	before := vaultBalance(t, ctx, pool, "treasury", "commercial_stock")
	if _, err := settleDeliver(t, ctx, world,
		settleBody(world.key, world.account, world.fiat, "BRL", "failed", "evt-fail"), world.clock.now, settleSecret); !errors.Is(err, application.ErrPurchaseEventNotSettling) {
		t.Fatalf("failed delivery = %v, want ErrPurchaseEventNotSettling", err)
	}
	uc := reconcileUseCase(t, pool, world.clock)
	report, err := uc.Execute(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.Alerts) != 0 {
		t.Fatalf("alerts on a proven failure: %+v", report.Alerts)
	}
	if len(report.Transitions) != 1 || report.Transitions[0].To != "failed" || !report.Transitions[0].Released {
		t.Fatalf("transitions changed: %+v", report.Transitions)
	}
	if got := intentStatus(t, ctx, pool, intentID); got != "failed" {
		t.Fatalf("intent = %q, want failed", got)
	}
	if got := holdStatus(t, ctx, pool, intentID); got != "released" {
		t.Fatalf("hold = %q, want released", got)
	}
	if got := vaultBalance(t, ctx, pool, "treasury", "commercial_stock"); got != before+world.ink {
		t.Fatalf("vault = %d, want every unit back", got)
	}
	if got := vaultBalance(t, ctx, pool, "user", world.account); got != 0 {
		t.Fatalf("buyer paid on failure: %d", got)
	}
	again, err := uc.Execute(ctx)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(again.Transitions) != 0 {
		t.Fatalf("second pass released again: %+v", again.Transitions)
	}
}

// TestReconcileFailsSilentLapse proves silence past expiry is a
// proven terminal too: the hold returns with no human needed.
func TestReconcileFailsSilentLapse(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	intentRepo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	quote := sealQuote(t, ctx, pool, clock, time.Minute)
	account := intentAccount(t, ctx, pool)
	uc, err := application.NewAcceptPurchaseUseCase(intentRepo)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}
	accepted, err := uc.Execute(ctx, acceptCmd("lapse-1", account, quote, 10000))
	if err != nil {
		t.Fatalf("AcceptPurchase: %v", err)
	}
	clock.now = intentInstant().Add(10 * time.Minute)
	report, err := reconcileUseCase(t, pool, clock).Execute(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.Alerts) != 0 || len(report.Transitions) != 1 || !report.Transitions[0].Released {
		t.Fatalf("report changed: %+v", report)
	}
	if got := intentStatus(t, ctx, pool, accepted.IntentID); got != "failed" {
		t.Fatalf("intent = %q, want failed", got)
	}
	if got := vaultBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 300000 {
		t.Fatalf("vault = %d, want every unit back", got)
	}
}

// TestReconcileWaitsLiveIntents proves live pending intents wait
// untouched: no alerts, no transitions, holds active.
func TestReconcileWaitsLiveIntents(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	world := newSettleWorld(t, ctx, pool)
	intentID := reconcileIntentID(t, ctx, pool, world.key, world.account)
	report, err := reconcileUseCase(t, pool, world.clock).Execute(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.Alerts) != 0 || len(report.Transitions) != 0 {
		t.Fatalf("live intent moved: %+v", report)
	}
	if got := intentStatus(t, ctx, pool, intentID); got != "pending" {
		t.Fatalf("intent = %q, want pending", got)
	}
	if got := holdStatus(t, ctx, pool, intentID); got != "active" {
		t.Fatalf("hold = %q, want active", got)
	}
}

// TestReconcileNeverFailsDivergence proves terminal safety: a paid
// event without settlement alerts even past expiry instead of
// failing, so a real payment is never released away.
func TestReconcileNeverFailsDivergence(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := settleCtx()
	defer cancel()

	clock := &intentClock{now: intentInstant().Add(3 * time.Second)}
	intentRepo := mustIntentRepo(t, pool, clock)
	fundCommercialStock(t, ctx, pool, 300000)
	quote := sealQuote(t, ctx, pool, clock, time.Minute)
	account := intentAccount(t, ctx, pool)
	uc, err := application.NewAcceptPurchaseUseCase(intentRepo)
	if err != nil {
		t.Fatalf("NewAcceptPurchaseUseCase: %v", err)
	}
	accepted, err := uc.Execute(ctx, acceptCmd("diverge-1", account, quote, 10000))
	if err != nil {
		t.Fatalf("AcceptPurchase: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ('evt-diverge', 'diverge-1', $1::uuid, 10000, 'BRL', 'paid', now())`, account); err != nil {
		t.Fatalf("inject paid event: %v", err)
	}
	clock.now = intentInstant().Add(10 * time.Minute)
	report, err := reconcileUseCase(t, pool, clock).Execute(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.Alerts) != 1 || report.Alerts[0].Kind != application.AlertCapturedWithoutTransfer {
		t.Fatalf("alerts changed: %+v", report.Alerts)
	}
	if len(report.Transitions) != 0 {
		t.Fatalf("divergence failed: %+v", report.Transitions)
	}
	if got := intentStatus(t, ctx, pool, accepted.IntentID); got != "pending" {
		t.Fatalf("intent = %q, want pending", got)
	}
	if got := holdStatus(t, ctx, pool, accepted.IntentID); got != "active" {
		t.Fatalf("hold released on divergence: %q", got)
	}
	if got := vaultBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 300000-253833 {
		t.Fatalf("vault moved on alert: %d", got)
	}
}
