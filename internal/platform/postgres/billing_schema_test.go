package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// billingTestDigest is a well-formed SHA-256 digest of a raw webhook body.
var billingTestDigest = strings.Repeat("ab", 32)

// billingOrphanID is a syntactically valid account identifier that does not
// exist, used to prove the foreign keys.
var billingOrphanID = pgtype.UUID{
	Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xbb, 0xbc},
	Valid: true,
}

// billingProbe is one statement whose expected outcome is either acceptance
// (empty want) or a specific PostgreSQL error code.
type billingProbe struct {
	name string
	sql  string
	args []any
	want string
}

// runBillingProbes executes every probe: acceptance when want is empty, or the
// exact PostgreSQL error code otherwise.
func runBillingProbes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, probes []billingProbe) {
	t.Helper()

	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			var args []any
			if probe.args != nil {
				args = probe.args
			}
			_, err := pool.Exec(ctx, probe.sql, args...)
			if probe.want == "" {
				if err != nil {
					t.Fatalf("statement must be accepted: %v", err)
				}
				return
			}
			assertPgCode(t, err, probe.want)
		})
	}
}

// mustStripeCustomer maps one account to a Stripe customer of the given mode.
func mustStripeCustomer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID, customerID string, livemode bool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode)
		VALUES ($1, $2, $3)`, accountID, customerID, livemode); err != nil {
		t.Fatalf("insert stripe customer %s: %v", customerID, err)
	}
}

// mustStripeEvent records one verified, not yet processed event.
func mustStripeEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.stripe_events
			(stripe_event_id, event_type, livemode, stripe_created_at, payload_sha256, payload_bytes)
		VALUES ($1, 'checkout.session.completed', false, now() - interval '1 minute', $2, 512)`,
		eventID, billingTestDigest); err != nil {
		t.Fatalf("insert stripe event %s: %v", eventID, err)
	}
}

// mustCheckoutIntent records one open intent for the seeded account.
func mustCheckoutIntent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.checkout_intents
			(account_id, market, product_id, catalog_version, currency, amount_minor, livemode, status, stripe_checkout_session_id)
		VALUES ($1, 'BR', 'ink_10000', 1, 'BRL', 990, false, 'open', 'cs_test_seeded1')
		RETURNING id`, accountID).Scan(&id); err != nil {
		t.Fatalf("insert checkout intent: %v", err)
	}
	return id
}

// mustSubscription mirrors one active Member subscription.
func mustSubscription(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID, subscriptionID string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.subscriptions
			(account_id, stripe_subscription_id, status, livemode, market, product_id, catalog_version,
			 stripe_price_id, current_period_start, current_period_end)
		VALUES ($1, $2, 'active', false, 'BR', 'member_monthly', 1, 'price_brMember1',
		        now(), now() + interval '30 days')
		RETURNING id`, accountID, subscriptionID).Scan(&id); err != nil {
		t.Fatalf("insert subscription %s: %v", subscriptionID, err)
	}
	return id
}

// mustReconciliationRun records one finished reconciliation window.
func mustReconciliationRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.billing_reconciliation_runs (livemode, window_start, window_end, status, finished_at)
		VALUES (false, now() - interval '1 day', now(), 'completed', now())
		RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("insert reconciliation run: %v", err)
	}
	return id
}

func TestBillingStripeCustomerConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "billing-customer@arena.example.com")
	other := mustCreateAccount(t, ctx, q, "billing-customer-other@arena.example.com")
	third := mustCreateAccount(t, ctx, q, "billing-customer-third@arena.example.com")

	// One mapping per account, in test or live mode.
	mustStripeCustomer(t, ctx, pool, acc.ID, "cus_arenaTestCustomer1", false)
	mustStripeCustomer(t, ctx, pool, other.ID, "cus_arenaLiveCustomer1", true)

	probes := []billingProbe{
		{
			name: "missing prefix",
			sql:  "INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode) VALUES ('" + third.ID.String() + "', 'arenaCustomer1', false)",
			want: "23514",
		},
		{
			name: "upper case prefix",
			sql:  "INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode) VALUES ('" + third.ID.String() + "', 'CUS_arenaCustomer1', false)",
			want: "23514",
		},
		{
			name: "empty body",
			sql:  "INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode) VALUES ('" + third.ID.String() + "', 'cus_', false)",
			want: "23514",
		},
		{
			name: "underscore in body",
			sql:  "INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode) VALUES ('" + third.ID.String() + "', 'cus_arena_test', false)",
			want: "23514",
		},
		{
			name: "customer already mapped",
			args: []any{third.ID, "cus_arenaTestCustomer1", false},
			sql:  "INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode) VALUES ($1, $2, $3)",
			want: "23505",
		},
		{
			name: "account already mapped",
			sql:  "INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode) VALUES ('" + acc.ID.String() + "', 'cus_arenaSecond1', false)",
			want: "23505",
		},
		{
			name: "unknown account",
			args: []any{billingOrphanID, "cus_arenaOrphan1", false},
			sql:  "INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode) VALUES ($1, $2, $3)",
			want: "23503",
		},
	}
	runBillingProbes(t, ctx, pool, probes)
}

func TestBillingStripeEventConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()

	mustStripeEvent(t, ctx, pool, "evt_arenaSeeded1")

	const insert = `INSERT INTO app.stripe_events
		(stripe_event_id, event_type, livemode, stripe_created_at, payload_sha256, payload_bytes,
		 status, attempts, last_error, processed_at)
		VALUES ($1, $2, false, now(), $3, $4, $5, $6, $7, $8)`

	probes := []billingProbe{
		{name: "received is the default", sql: "INSERT INTO app.stripe_events (stripe_event_id, event_type, livemode, stripe_created_at, payload_sha256, payload_bytes) VALUES ('evt_arenaReceived1', 'invoice.paid', false, now(), '" + billingTestDigest + "', 128)", want: ""},
		{name: "terminal processed with its instant", args: []any{"evt_arenaProcessed1", "invoice.paid", billingTestDigest, 128, "processed", 2, nil, time.Now().UTC()}, sql: insert, want: ""},
		{name: "malformed event id", args: []any{"event_arena1", "invoice.paid", billingTestDigest, 128, "received", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "duplicate event id", args: []any{"evt_arenaSeeded1", "invoice.paid", billingTestDigest, 128, "received", 0, nil, nil}, sql: insert, want: "23505"},
		{name: "event type without an action", args: []any{"evt_arenaType1", "invoice", billingTestDigest, 128, "received", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "event type with upper case", args: []any{"evt_arenaType2", "Checkout.session.completed", billingTestDigest, 128, "received", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "event type with a trailing dot", args: []any{"evt_arenaType3", "checkout.session.", billingTestDigest, 128, "received", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "event type too long", args: []any{"evt_arenaType4", strings.Repeat("checkout.session.", 9), billingTestDigest, 128, "received", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "digest upper case", args: []any{"evt_arenaDigest1", "invoice.paid", strings.ToUpper(billingTestDigest), 128, "received", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "digest too short", args: []any{"evt_arenaDigest2", "invoice.paid", "abcdef", 128, "received", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "digest not hexadecimal", args: []any{"evt_arenaDigest3", "invoice.paid", strings.Repeat("zz", 32), 128, "received", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "empty body", args: []any{"evt_arenaBytes1", "invoice.paid", billingTestDigest, 0, "received", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "negative body size", args: []any{"evt_arenaBytes2", "invoice.paid", billingTestDigest, -1, "received", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "unknown status", args: []any{"evt_arenaStatus1", "invoice.paid", billingTestDigest, 128, "replayed", 1, nil, nil}, sql: insert, want: "23514"},
		{name: "claimed without an attempt", args: []any{"evt_arenaClaim1", "invoice.paid", billingTestDigest, 128, "processing", 0, nil, nil}, sql: insert, want: "23514"},
		{name: "received with a terminal instant", args: []any{"evt_arenaTerminal1", "invoice.paid", billingTestDigest, 128, "received", 0, nil, time.Now().UTC()}, sql: insert, want: "23514"},
		{name: "processed without an instant", args: []any{"evt_arenaTerminal2", "invoice.paid", billingTestDigest, 128, "processed", 1, nil, nil}, sql: insert, want: "23514"},
		{name: "error outside failed", args: []any{"evt_arenaError1", "invoice.paid", billingTestDigest, 128, "received", 0, "provider timeout", nil}, sql: insert, want: "23514"},
		{name: "blank error", args: []any{"evt_arenaError2", "invoice.paid", billingTestDigest, 128, "failed", 1, "   ", nil}, sql: insert, want: "23514"},
		{name: "error too long", args: []any{"evt_arenaError3", "invoice.paid", billingTestDigest, 128, "failed", 1, strings.Repeat("e", 501), nil}, sql: insert, want: "23514"},
	}
	runBillingProbes(t, ctx, pool, probes)
}

func TestBillingCheckoutIntentConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "billing-intent@arena.example.com")
	unmapped := mustCreateAccount(t, ctx, q, "billing-intent-unmapped@arena.example.com")
	mustStripeCustomer(t, ctx, pool, acc.ID, "cus_arenaIntent1", false)

	const insert = `INSERT INTO app.checkout_intents
		(account_id, market, product_id, catalog_version, currency, amount_minor, livemode,
		 status, stripe_checkout_session_id, stripe_payment_intent_id, paid_at, closed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	now := time.Now().UTC()

	probes := []billingProbe{
		{name: "created before the session exists", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: ""},
		{name: "open with a test session", args: []any{acc.ID, "BR", "pass_5", 2, "BRL", 3990, false, "open", "cs_test_intentOpen1", nil, nil, nil}, sql: insert, want: ""},
		{name: "paid with its instant", args: []any{acc.ID, "INTERNATIONAL", "ink_10000", 1, "USD", 199, true, "paid", "cs_live_intentPaid1", "pi_arenaPaid1", now, nil}, sql: insert, want: ""},
		{name: "failed before the session exists", args: []any{acc.ID, "BR", "ink_40000", 1, "BRL", 2490, false, "failed", nil, nil, nil, now}, sql: insert, want: ""},
		{name: "expired with its instant", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "expired", "cs_test_intentExpired1", nil, nil, now}, sql: insert, want: ""},
		{name: "lower case market", args: []any{acc.ID, "br", "ink_10000", 1, "BRL", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: "23514"},
		{name: "unknown market", args: []any{acc.ID, "MARS", "ink_10000", 1, "BRL", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: "23514"},
		{name: "unsupported currency", args: []any{acc.ID, "BR", "ink_10000", 1, "EUR", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: "23514"},
		{name: "currency of another market", args: []any{acc.ID, "BR", "ink_10000", 1, "USD", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: "23514"},
		{name: "international charged in BRL", args: []any{acc.ID, "INTERNATIONAL", "ink_10000", 1, "BRL", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: "23514"},
		{name: "upper case product", args: []any{acc.ID, "BR", "INK_10000", 1, "BRL", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: "23514"},
		{name: "unknown product shape", args: []any{acc.ID, "BR", "ink-10000", 1, "BRL", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: "23514"},
		{name: "catalog version zero", args: []any{acc.ID, "BR", "ink_10000", 0, "BRL", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: "23514"},
		{name: "free intent", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 0, false, "created", nil, nil, nil, nil}, sql: insert, want: "23514"},
		{name: "unknown status", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "refunded", "cs_test_intentRefund1", nil, nil, nil}, sql: insert, want: "23514"},
		{name: "session without a mode", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "open", "cs_arenaSession1", nil, nil, nil}, sql: insert, want: "23514"},
		{name: "live session in test mode", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "open", "cs_live_intentMixed1", nil, nil, nil}, sql: insert, want: "23514"},
		{name: "test session in live mode", args: []any{acc.ID, "INTERNATIONAL", "ink_10000", 1, "USD", 199, true, "open", "cs_test_intentMixed2", nil, nil, nil}, sql: insert, want: "23514"},
		{name: "malformed payment intent", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "open", "cs_test_intentPi1", "pi_arena_paid", nil, nil}, sql: insert, want: "23514"},
		{name: "payment intent without a session", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "created", nil, "pi_arenaNoSession1", nil, nil}, sql: insert, want: "23514"},
		{name: "created carrying a session", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "created", "cs_test_intentCreated1", nil, nil, nil}, sql: insert, want: "23514"},
		{name: "open without a session", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "open", nil, nil, nil, nil}, sql: insert, want: "23514"},
		{name: "paid without an instant", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "paid", "cs_test_intentPaid2", nil, nil, nil}, sql: insert, want: "23514"},
		{name: "paid and closed at once", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "paid", "cs_test_intentPaid3", nil, now, now}, sql: insert, want: "23514"},
		{name: "expired without an instant", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "expired", "cs_test_intentExpired2", nil, nil, nil}, sql: insert, want: "23514"},
		{name: "duplicate session", args: []any{acc.ID, "BR", "ink_10000", 1, "BRL", 990, false, "open", "cs_test_intentOpen1", nil, nil, nil}, sql: insert, want: "23505"},
		{name: "duplicate payment intent", args: []any{acc.ID, "INTERNATIONAL", "pass_1", 1, "USD", 199, true, "open", "cs_live_intentPaid2", "pi_arenaPaid1", nil, nil}, sql: insert, want: "23505"},
		{name: "account without a customer", args: []any{unmapped.ID, "BR", "ink_10000", 1, "BRL", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: "23503"},
		{name: "unknown account", args: []any{billingOrphanID, "BR", "ink_10000", 1, "BRL", 990, false, "created", nil, nil, nil, nil}, sql: insert, want: "23503"},
	}
	runBillingProbes(t, ctx, pool, probes)
}

func TestBillingSubscriptionConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "billing-subscription@arena.example.com")
	mustStripeCustomer(t, ctx, pool, acc.ID, "cus_arenaSubscription1", true)
	mustSubscription(t, ctx, pool, acc.ID, "sub_arenaSeeded1")

	const insert = `INSERT INTO app.subscriptions
		(account_id, stripe_subscription_id, status, livemode, market, product_id, catalog_version,
		 stripe_price_id, current_period_start, current_period_end)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	start := time.Now().UTC()
	end := start.Add(30 * 24 * time.Hour)

	probes := []billingProbe{
		{name: "trialing without a period", args: []any{acc.ID, "sub_arenaTrial1", "trialing", false, "BR", "member_monthly", 1, "price_brMember1", nil, nil}, sql: insert, want: ""},
		{name: "incomplete", args: []any{acc.ID, "sub_arenaIncomplete1", "incomplete", false, "BR", "member_monthly", 1, "price_brMember1", start, end}, sql: insert, want: ""},
		{name: "upper case status", args: []any{acc.ID, "sub_arenaUpper1", "ACTIVE", false, "BR", "member_monthly", 1, "price_brMember1", start, end}, sql: insert, want: "23514"},
		{name: "unknown status", args: []any{acc.ID, "sub_arenaUnknown1", "refunded", false, "BR", "member_monthly", 1, "price_brMember1", start, end}, sql: insert, want: "23514"},
		{name: "lower case market", args: []any{acc.ID, "sub_arenaMarket1", "active", false, "br", "member_monthly", 1, "price_brMember1", start, end}, sql: insert, want: "23514"},
		{name: "unknown market", args: []any{acc.ID, "sub_arenaMarket2", "active", false, "MARS", "member_monthly", 1, "price_brMember1", start, end}, sql: insert, want: "23514"},
		{name: "invalid product", args: []any{acc.ID, "sub_arenaProduct1", "active", false, "BR", "member-monthly", 1, "price_brMember1", start, end}, sql: insert, want: "23514"},
		{name: "catalog version zero", args: []any{acc.ID, "sub_arenaVersion1", "active", false, "BR", "member_monthly", 0, "price_brMember1", start, end}, sql: insert, want: "23514"},
		{name: "price without prefix", args: []any{acc.ID, "sub_arenaPrice1", "active", false, "BR", "member_monthly", 1, "brMember1", start, end}, sql: insert, want: "23514"},
		{name: "price without a body", args: []any{acc.ID, "sub_arenaPrice2", "active", false, "BR", "member_monthly", 1, "price_", start, end}, sql: insert, want: "23514"},
		{name: "price with an underscore", args: []any{acc.ID, "sub_arenaPrice3", "active", false, "BR", "member_monthly", 1, "price_br_member", start, end}, sql: insert, want: "23514"},
		{name: "period without a start", args: []any{acc.ID, "sub_arenaPeriod1", "active", false, "BR", "member_monthly", 1, "price_brMember1", nil, end}, sql: insert, want: "23514"},
		{name: "period without an end", args: []any{acc.ID, "sub_arenaPeriod2", "active", false, "BR", "member_monthly", 1, "price_brMember1", start, nil}, sql: insert, want: "23514"},
		{name: "period that does not advance", args: []any{acc.ID, "sub_arenaPeriod3", "active", false, "BR", "member_monthly", 1, "price_brMember1", end, start}, sql: insert, want: "23514"},
		{name: "duplicate subscription", args: []any{acc.ID, "sub_arenaSeeded1", "active", false, "BR", "member_monthly", 1, "price_brMember1", start, end}, sql: insert, want: "23505"},
		{name: "unknown account", args: []any{billingOrphanID, "sub_arenaOrphan1", "active", false, "BR", "member_monthly", 1, "price_brMember1", start, end}, sql: insert, want: "23503"},
	}
	runBillingProbes(t, ctx, pool, probes)
}

func TestBillingReconciliationConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "billing-reconciliation@arena.example.com")
	runID := mustReconciliationRun(t, ctx, pool)

	runProbes := []billingProbe{
		{name: "running window", sql: "INSERT INTO app.billing_reconciliation_runs (livemode, window_start, window_end) VALUES (false, now() - interval '2 hours', now() - interval '1 hour')", want: ""},
		{name: "window that does not advance", sql: "INSERT INTO app.billing_reconciliation_runs (livemode, window_start, window_end) VALUES (false, now(), now() - interval '1 hour')", want: "23514"},
		{name: "unknown status", sql: "INSERT INTO app.billing_reconciliation_runs (livemode, window_start, window_end, status) VALUES (false, now() - interval '1 hour', now(), 'paused')", want: "23514"},
		{name: "negative counters", sql: "INSERT INTO app.billing_reconciliation_runs (livemode, window_start, window_end, scanned_objects) VALUES (false, now() - interval '1 hour', now(), -1)", want: "23514"},
		{name: "finished without an instant", sql: "INSERT INTO app.billing_reconciliation_runs (livemode, window_start, window_end, status) VALUES (false, now() - interval '1 hour', now(), 'completed')", want: "23514"},
		{name: "running with a finish instant", sql: "INSERT INTO app.billing_reconciliation_runs (livemode, window_start, window_end, finished_at) VALUES (false, now() - interval '1 hour', now(), now())", want: "23514"},
	}
	runBillingProbes(t, ctx, pool, runProbes)

	const insertFinding = `INSERT INTO app.billing_reconciliation_findings
		(run_id, account_id, kind, reference, details, resolved_at, resolution)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	now := time.Now().UTC()
	findingProbes := []billingProbe{
		{name: "unresolved finding", args: []any{runID, acc.ID, "amount_mismatch", "cs_test_intentOpen1", "provider reports 199", nil, nil}, sql: insertFinding, want: ""},
		{name: "provider only finding", args: []any{runID, nil, "missing_local", "pi_arenaRemote1", nil, nil, nil}, sql: insertFinding, want: ""},
		{name: "resolved finding", args: []any{runID, acc.ID, "unprocessed_event", "evt_arenaPending1", nil, now, "event was replayed by hand"}, sql: insertFinding, want: ""},
		{name: "unknown kind", args: []any{runID, acc.ID, "wrong_kind", "cs_test_intentOpen1", nil, nil, nil}, sql: insertFinding, want: "23514"},
		{name: "blank reference", args: []any{runID, acc.ID, "status_mismatch", "   ", nil, nil, nil}, sql: insertFinding, want: "23514"},
		{name: "reference too long", args: []any{runID, acc.ID, "status_mismatch", strings.Repeat("r", 201), nil, nil, nil}, sql: insertFinding, want: "23514"},
		{name: "blank details", args: []any{runID, acc.ID, "currency_mismatch", "cs_test_intentOpen1", "   ", nil, nil}, sql: insertFinding, want: "23514"},
		{name: "details too long", args: []any{runID, acc.ID, "currency_mismatch", "cs_test_intentOpen1", strings.Repeat("d", 501), nil, nil}, sql: insertFinding, want: "23514"},
		{name: "resolution without an instant", args: []any{runID, acc.ID, "missing_remote", "cs_test_intentOpen1", nil, nil, "reviewed"}, sql: insertFinding, want: "23514"},
		{name: "instant without a resolution", args: []any{runID, acc.ID, "missing_remote", "cs_test_intentOpen1", nil, now, nil}, sql: insertFinding, want: "23514"},
		{name: "unknown run", args: []any{billingOrphanID, acc.ID, "missing_remote", "cs_test_intentOpen1", nil, nil, nil}, sql: insertFinding, want: "23503"},
		{name: "unknown account", args: []any{runID, billingOrphanID, "missing_remote", "cs_test_intentOpen1", nil, nil, nil}, sql: insertFinding, want: "23503"},
	}
	runBillingProbes(t, ctx, pool, findingProbes)
}

func TestBillingStatusTransitions(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "billing-transitions@arena.example.com")
	mustStripeCustomer(t, ctx, pool, acc.ID, "cus_arenaTransitions1", false)

	// --- Webhook events ----------------------------------------------------
	insertEvent := func(t *testing.T, eventID, status string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.stripe_events
				(stripe_event_id, event_type, livemode, stripe_created_at, payload_sha256, payload_bytes,
				 status, attempts, last_error, processed_at)
			VALUES ($1, 'invoice.paid', false, now(), $2, 64, $3,
			        CASE WHEN $3 = 'received' THEN 0 ELSE 1 END,
			        CASE WHEN $3 = 'failed' THEN 'provider timeout' END,
			        CASE WHEN $3 IN ('processed', 'ignored') THEN now() END)`,
			eventID, billingTestDigest, status); err != nil {
			t.Fatalf("seed event %s in %s: %v", eventID, status, err)
		}
	}
	updateEvent := func(t *testing.T, eventID, to string) error {
		t.Helper()
		_, err := pool.Exec(ctx, `
			UPDATE app.stripe_events
			SET status = $2,
			    attempts = attempts + CASE WHEN $2 = 'received' THEN 0 ELSE 1 END,
			    last_error = CASE WHEN $2 = 'failed' THEN 'provider timeout' ELSE NULL END,
			    processed_at = CASE WHEN $2 IN ('processed', 'ignored') THEN now() ELSE NULL END
			WHERE stripe_event_id = $1`, eventID, to)
		return err
	}

	eventCases := []struct{ from, to, want string }{
		{from: "received", to: "processing", want: ""},
		{from: "received", to: "ignored", want: ""},
		{from: "received", to: "failed", want: ""},
		{from: "processing", to: "processed", want: ""},
		{from: "processing", to: "failed", want: ""},
		{from: "processing", to: "ignored", want: ""},
		{from: "failed", to: "processing", want: ""},
		{from: "received", to: "processed", want: "23514"},
		{from: "ignored", to: "processing", want: "23514"},
		{from: "processed", to: "processing", want: "23514"},
		{from: "processed", to: "failed", want: "23514"},
		{from: "failed", to: "processed", want: "23514"},
	}
	for index, transition := range eventCases {
		t.Run("event "+transition.from+" to "+transition.to, func(t *testing.T) {
			eventID := "evt_arenaTransition" + string(rune('a'+index))
			insertEvent(t, eventID, transition.from)
			err := updateEvent(t, eventID, transition.to)
			if transition.want == "" {
				if err != nil {
					t.Fatalf("transition should be accepted: %v", err)
				}
				return
			}
			assertPgCode(t, err, transition.want)
		})
	}

	// --- Checkout intents --------------------------------------------------
	// Session identifiers must be unique per intent, so each case seeds its
	// own generated session.
	insertIntent := func(t *testing.T, status, sessionID string) pgtype.UUID {
		t.Helper()
		var session any
		if sessionID != "" {
			session = sessionID
		}
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO app.checkout_intents
				(account_id, market, product_id, catalog_version, currency, amount_minor, livemode,
				 status, stripe_checkout_session_id, paid_at, closed_at)
			VALUES ($1, 'BR', 'ink_10000', 1, 'BRL', 990, false, $2, $3,
			        CASE WHEN $2 = 'paid' THEN now() END,
			        CASE WHEN $2 IN ('expired', 'failed') THEN now() END)
			RETURNING id`, acc.ID, status, session).Scan(&id); err != nil {
			t.Fatalf("seed intent in %s: %v", status, err)
		}
		return id
	}
	updateIntent := func(t *testing.T, intentID pgtype.UUID, to, sessionID string) error {
		t.Helper()
		_, err := pool.Exec(ctx, `
			UPDATE app.checkout_intents
			SET status = $2,
			    stripe_checkout_session_id = CASE
			        WHEN stripe_checkout_session_id IS NULL AND $3 <> '' THEN $3
			        ELSE stripe_checkout_session_id END,
			    paid_at = CASE WHEN $2 = 'paid' THEN now() ELSE NULL END,
			    closed_at = CASE WHEN $2 IN ('expired', 'failed') THEN now() ELSE NULL END
			WHERE id = $1`, intentID, to, sessionID)
		return err
	}

	intentCases := []struct {
		from       string
		to         string
		hasSession bool
		want       string
	}{
		{from: "created", to: "open", want: ""},
		{from: "created", to: "failed", want: ""},
		{from: "open", to: "paid", hasSession: true, want: ""},
		{from: "open", to: "expired", hasSession: true, want: ""},
		{from: "open", to: "failed", hasSession: true, want: ""},
		{from: "open", to: "open", hasSession: true, want: ""},
		{from: "created", to: "paid", want: "23514"},
		{from: "created", to: "expired", want: "23514"},
		{from: "paid", to: "expired", hasSession: true, want: "23514"},
		{from: "paid", to: "open", hasSession: true, want: "23514"},
		{from: "expired", to: "open", hasSession: true, want: "23514"},
		{from: "failed", to: "open", want: "23514"},
		{from: "failed", to: "paid", want: "23514"},
	}
	for index, transition := range intentCases {
		name := "intent " + transition.from + " to " + transition.to
		t.Run(name, func(t *testing.T) {
			sessionID := fmt.Sprintf("cs_test_transition%02d", index)
			seeded := ""
			if transition.hasSession {
				seeded = sessionID
			}
			intentID := insertIntent(t, transition.from, seeded)
			err := updateIntent(t, intentID, transition.to, sessionID)
			if transition.want == "" {
				if err != nil {
					t.Fatalf("transition should be accepted: %v", err)
				}
				return
			}
			assertPgCode(t, err, transition.want)
		})
	}

	// --- Subscriptions -----------------------------------------------------
	insertSubscription := func(t *testing.T, subscriptionID, status string) pgtype.UUID {
		t.Helper()
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO app.subscriptions
				(account_id, stripe_subscription_id, status, livemode, market, product_id, catalog_version,
				 stripe_price_id, current_period_start, current_period_end)
			VALUES ($1, $2, $3, false, 'BR', 'member_monthly', 1, 'price_brMember1',
			        now(), now() + interval '30 days')
			RETURNING id`, acc.ID, subscriptionID, status).Scan(&id); err != nil {
			t.Fatalf("seed subscription in %s: %v", status, err)
		}
		return id
	}
	updateSubscription := func(t *testing.T, subscriptionID pgtype.UUID, to string) error {
		t.Helper()
		_, err := pool.Exec(ctx, `
			UPDATE app.subscriptions
			SET status = $2,
			    current_period_start = current_period_start + interval '30 days',
			    current_period_end = current_period_end + interval '30 days',
			    canceled_at = CASE WHEN $2 = 'canceled' THEN now() END
			WHERE id = $1`, subscriptionID, to)
		return err
	}

	subscriptionCases := []struct{ from, to, want string }{
		{from: "incomplete", to: "active", want: ""},
		{from: "incomplete", to: "incomplete_expired", want: ""},
		{from: "trialing", to: "active", want: ""},
		{from: "trialing", to: "past_due", want: ""},
		{from: "active", to: "past_due", want: ""},
		{from: "active", to: "paused", want: ""},
		{from: "active", to: "active", want: ""},
		{from: "past_due", to: "active", want: ""},
		{from: "past_due", to: "canceled", want: ""},
		{from: "unpaid", to: "canceled", want: ""},
		{from: "paused", to: "active", want: ""},
		{from: "canceled", to: "canceled", want: ""},
		{from: "canceled", to: "active", want: "23514"},
		{from: "canceled", to: "trialing", want: "23514"},
		{from: "incomplete_expired", to: "active", want: "23514"},
		{from: "incomplete_expired", to: "incomplete", want: "23514"},
		{from: "unpaid", to: "incomplete", want: "23514"},
		{from: "paused", to: "incomplete_expired", want: "23514"},
	}
	for index, transition := range subscriptionCases {
		name := "subscription " + transition.from + " to " + transition.to
		t.Run(name, func(t *testing.T) {
			subscriptionID := insertSubscription(t, "sub_arenaTransition"+string(rune('a'+index)), transition.from)
			err := updateSubscription(t, subscriptionID, transition.to)
			if transition.want == "" {
				if err != nil {
					t.Fatalf("transition should be accepted: %v", err)
				}
				return
			}
			assertPgCode(t, err, transition.want)
		})
	}
}

func TestBillingLifecycleImmutability(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "billing-immutable@arena.example.com")
	other := mustCreateAccount(t, ctx, q, "billing-immutable-other@arena.example.com")
	mustStripeCustomer(t, ctx, pool, acc.ID, "cus_arenaImmutable1", false)
	mustStripeEvent(t, ctx, pool, "evt_arenaImmutable1")
	intentID := mustCheckoutIntent(t, ctx, pool, acc.ID)
	subscriptionID := mustSubscription(t, ctx, pool, acc.ID, "sub_arenaImmutable1")
	runID := mustReconciliationRun(t, ctx, pool)
	otherRunID := mustReconciliationRun(t, ctx, pool)

	var findingID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.billing_reconciliation_findings (run_id, account_id, kind, reference)
		VALUES ($1, $2, 'amount_mismatch', 'cs_test_intentOpen1')
		RETURNING id`, runID, acc.ID).Scan(&findingID); err != nil {
		t.Fatalf("seed finding: %v", err)
	}

	probes := []billingProbe{
		// The verified event keeps its identity and its payload evidence.
		{name: "event identity", sql: "UPDATE app.stripe_events SET stripe_event_id = 'evt_arenaRewritten1' WHERE stripe_event_id = 'evt_arenaImmutable1'", want: "23514"},
		{name: "event type", sql: "UPDATE app.stripe_events SET event_type = 'charge.refunded' WHERE stripe_event_id = 'evt_arenaImmutable1'", want: "23514"},
		{name: "event digest", args: []any{strings.Repeat("cd", 32)}, sql: "UPDATE app.stripe_events SET payload_sha256 = $1 WHERE stripe_event_id = 'evt_arenaImmutable1'", want: "23514"},
		{name: "event body size", sql: "UPDATE app.stripe_events SET payload_bytes = payload_bytes + 1 WHERE stripe_event_id = 'evt_arenaImmutable1'", want: "23514"},
		{name: "event mode", sql: "UPDATE app.stripe_events SET livemode = true WHERE stripe_event_id = 'evt_arenaImmutable1'", want: "23514"},
		{name: "event received instant", sql: "UPDATE app.stripe_events SET received_at = now() - interval '1 hour' WHERE stripe_event_id = 'evt_arenaImmutable1'", want: "23514"},
		{name: "event provider instant", sql: "UPDATE app.stripe_events SET stripe_created_at = now() - interval '1 day' WHERE stripe_event_id = 'evt_arenaImmutable1'", want: "23514"},
		{name: "event processing still moves", sql: "UPDATE app.stripe_events SET status = 'processing', attempts = attempts + 1 WHERE stripe_event_id = 'evt_arenaImmutable1'", want: ""},

		// The commercial decision of an intent is historical.
		{name: "intent amount", args: []any{intentID}, sql: "UPDATE app.checkout_intents SET amount_minor = 199 WHERE id = $1", want: "23514"},
		{name: "intent currency", args: []any{intentID}, sql: "UPDATE app.checkout_intents SET currency = 'USD' WHERE id = $1", want: "23514"},
		{name: "intent market", args: []any{intentID}, sql: "UPDATE app.checkout_intents SET market = 'INTERNATIONAL' WHERE id = $1", want: "23514"},
		{name: "intent product", args: []any{intentID}, sql: "UPDATE app.checkout_intents SET product_id = 'pass_5' WHERE id = $1", want: "23514"},
		{name: "intent catalog version", args: []any{intentID}, sql: "UPDATE app.checkout_intents SET catalog_version = 2 WHERE id = $1", want: "23514"},
		{name: "intent account", args: []any{intentID, other.ID}, sql: "UPDATE app.checkout_intents SET account_id = $2 WHERE id = $1", want: "23514"},
		{name: "intent mode", args: []any{intentID}, sql: "UPDATE app.checkout_intents SET livemode = true WHERE id = $1", want: "23514"},
		{name: "intent session", args: []any{intentID}, sql: "UPDATE app.checkout_intents SET stripe_checkout_session_id = 'cs_test_rewritten1' WHERE id = $1", want: "23514"},
		{name: "intent payment intent", args: []any{intentID}, sql: "UPDATE app.checkout_intents SET stripe_payment_intent_id = 'pi_arenaRecorded1' WHERE id = $1", want: ""},
		{name: "intent payment intent rewrite", args: []any{intentID}, sql: "UPDATE app.checkout_intents SET stripe_payment_intent_id = 'pi_arenaOther1' WHERE id = $1", want: "23514"},
		{name: "intent lifecycle still moves", args: []any{intentID}, sql: "UPDATE app.checkout_intents SET status = 'paid', paid_at = now() WHERE id = $1", want: ""},

		// Subscription identity is immutable; the current plan may move.
		{name: "subscription identity", args: []any{subscriptionID}, sql: "UPDATE app.subscriptions SET stripe_subscription_id = 'sub_arenaRewritten1' WHERE id = $1", want: "23514"},
		{name: "subscription account", args: []any{subscriptionID, other.ID}, sql: "UPDATE app.subscriptions SET account_id = $2 WHERE id = $1", want: "23514"},
		{name: "subscription mode", args: []any{subscriptionID}, sql: "UPDATE app.subscriptions SET livemode = true WHERE id = $1", want: "23514"},
		{name: "subscription creation instant", args: []any{subscriptionID}, sql: "UPDATE app.subscriptions SET created_at = now() - interval '1 day' WHERE id = $1", want: "23514"},
		{name: "subscription plan change", args: []any{subscriptionID}, sql: "UPDATE app.subscriptions SET stripe_price_id = 'price_brMember2', catalog_version = 2 WHERE id = $1", want: ""},
		{name: "subscription period change", args: []any{subscriptionID}, sql: "UPDATE app.subscriptions SET current_period_start = now(), current_period_end = now() + interval '31 days' WHERE id = $1", want: ""},

		// The customer mapping pins account and mode; the object may be replaced.
		{name: "customer mapping account", sql: "UPDATE app.stripe_customers SET account_id = '" + other.ID.String() + "' WHERE stripe_customer_id = 'cus_arenaImmutable1'", want: "23514"},
		{name: "customer mapping mode", sql: "UPDATE app.stripe_customers SET livemode = true WHERE stripe_customer_id = 'cus_arenaImmutable1'", want: "23514"},
		{name: "customer mapping creation instant", sql: "UPDATE app.stripe_customers SET created_at = now() - interval '1 day' WHERE stripe_customer_id = 'cus_arenaImmutable1'", want: "23514"},
		{name: "customer object replaced", sql: "UPDATE app.stripe_customers SET stripe_customer_id = 'cus_arenaReplaced1' WHERE stripe_customer_id = 'cus_arenaImmutable1'", want: ""},

		// A finding is preserved: only its resolution is written, once.
		{name: "finding kind", args: []any{findingID}, sql: "UPDATE app.billing_reconciliation_findings SET kind = 'missing_remote' WHERE id = $1", want: "23514"},
		{name: "finding reference", args: []any{findingID}, sql: "UPDATE app.billing_reconciliation_findings SET reference = 'pi_arenaOther1' WHERE id = $1", want: "23514"},
		{name: "finding details", args: []any{findingID}, sql: "UPDATE app.billing_reconciliation_findings SET details = 'reviewed' WHERE id = $1", want: "23514"},
		{name: "finding observation instant", args: []any{findingID}, sql: "UPDATE app.billing_reconciliation_findings SET observed_at = now() WHERE id = $1", want: "23514"},
		{name: "finding account", args: []any{findingID, other.ID}, sql: "UPDATE app.billing_reconciliation_findings SET account_id = $2 WHERE id = $1", want: "23514"},
		{name: "finding run", args: []any{findingID, otherRunID}, sql: "UPDATE app.billing_reconciliation_findings SET run_id = $2 WHERE id = $1", want: "23514"},
		{name: "finding resolution", args: []any{findingID}, sql: "UPDATE app.billing_reconciliation_findings SET resolved_at = now(), resolution = 'amount confirmed by the provider' WHERE id = $1", want: ""},
		{name: "finding resolution rewritten", args: []any{findingID}, sql: "UPDATE app.billing_reconciliation_findings SET resolution = 'silently rewritten' WHERE id = $1", want: "23514"},
		{name: "finding resolution erased", args: []any{findingID}, sql: "UPDATE app.billing_reconciliation_findings SET resolved_at = NULL, resolution = NULL WHERE id = $1", want: "23514"},
	}
	runBillingProbes(t, ctx, pool, probes)
}

func TestBillingRetentionAndLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "billing-retention@arena.example.com")
	mustStripeCustomer(t, ctx, pool, acc.ID, "cus_arenaRetention1", false)
	mustStripeEvent(t, ctx, pool, "evt_arenaRetention1")
	mustCheckoutIntent(t, ctx, pool, acc.ID)
	mustSubscription(t, ctx, pool, acc.ID, "sub_arenaRetention1")
	runID := mustReconciliationRun(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.billing_reconciliation_findings (run_id, account_id, kind, reference)
		VALUES ($1, $2, 'status_mismatch', 'cs_test_intentOpen1')`, runID, acc.ID); err != nil {
		t.Fatalf("seed finding: %v", err)
	}

	tables := []string{
		"app.stripe_customers",
		"app.stripe_events",
		"app.checkout_intents",
		"app.subscriptions",
		"app.billing_reconciliation_runs",
		"app.billing_reconciliation_findings",
	}
	for _, table := range tables {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE"} {
			var allowed bool
			if err := pool.QueryRow(ctx,
				"SELECT has_table_privilege('arena_app', $1, $2)", table, privilege,
			).Scan(&allowed); err != nil {
				t.Fatalf("has_table_privilege(%s, %s): %v", table, privilege, err)
			}
			if !allowed {
				t.Errorf("arena_app must hold %s on %s", privilege, table)
			}
		}

		var deletable bool
		if err := pool.QueryRow(ctx,
			"SELECT has_table_privilege('arena_app', $1, 'DELETE')", table,
		).Scan(&deletable); err != nil {
			t.Fatalf("has_table_privilege(%s, DELETE): %v", table, err)
		}
		if deletable {
			t.Errorf("arena_app must never hold DELETE on %s", table)
		}
	}

	// The runtime is denied outright by the privilege system...
	for _, table := range tables {
		t.Run("runtime delete "+table, func(t *testing.T) {
			withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
				_, err := tx.Exec(ctx, "DELETE FROM "+table)
				assertPgCode(t, err, "42501")
			})
		})
	}

	// ...and even the owner cannot delete: retention is a rule of the schema.
	for _, table := range tables {
		t.Run("owner delete "+table, func(t *testing.T) {
			_, err := pool.Exec(ctx, "DELETE FROM "+table)
			assertPgCode(t, err, "23514")
		})
	}

	// The runtime may resolve a finding, but only through the resolution.
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			UPDATE app.billing_reconciliation_findings
			SET resolved_at = now(), resolution = 'confirmed by the provider'
			WHERE reference = 'cs_test_intentOpen1'`); err != nil {
			t.Fatalf("runtime must be able to resolve a finding: %v", err)
		}
	})
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, `
			UPDATE app.billing_reconciliation_findings
			SET kind = 'missing_remote'
			WHERE reference = 'cs_test_intentOpen1'`)
		assertPgCode(t, err, "23514")
	})

	// Expired intents and pending events remain visible to the runtime.
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `SELECT id FROM app.stripe_events WHERE status = 'received'`); err != nil {
			t.Fatalf("runtime must read pending events: %v", err)
		}
	})
}

// TestBillingVocabularyMatchesTheDomain proves the schema vocabulary is the
// same one the billing domain accepts, so a value can never be valid for one
// side and invalid for the other.
func TestBillingVocabularyMatchesTheDomain(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "billing-vocabulary@arena.example.com")
	mustStripeCustomer(t, ctx, pool, acc.ID, "cus_arenaVocabulary1", false)

	const insertIntent = `INSERT INTO app.checkout_intents
		(account_id, market, product_id, catalog_version, currency, amount_minor, livemode, status)
		VALUES ($1, $2, $3, 1, $4, 990, false, 'created')`

	productIDs := []string{
		"ink_10000", "pass_1", "member_monthly", "abc", "a_1",
		"ab", "INK_10000", "1ink", "ink-10000", "ink_", "ink 10000", "ink.10000", "tinta_ção",
		strings.Repeat("a", 63), strings.Repeat("a", 64), strings.Repeat("a", 65),
	}
	for _, productID := range productIDs {
		_, domainErr := domain.ParseProductID(productID)
		_, databaseErr := pool.Exec(ctx, insertIntent, acc.ID, "BR", productID, "BRL")
		if (domainErr == nil) != (databaseErr == nil) {
			t.Errorf("product id %q: domain error = %v, database error = %v", productID, domainErr, databaseErr)
		}
	}

	// Every market with the currency it charges is accepted; the canonical
	// value is what the database stores, so a lower-case value is refused
	// (canonicalization is the domain's job).
	for _, market := range domain.AllMarkets() {
		currency, err := market.Currency()
		if err != nil {
			t.Fatalf("market %s currency: %v", market, err)
		}
		if _, err := pool.Exec(ctx, insertIntent, acc.ID, market.String(), "ink_10000", currency.String()); err != nil {
			t.Errorf("market %s with %s must be accepted: %v", market, currency, err)
		}
	}
	for _, rejected := range []struct {
		name     string
		market   string
		currency string
	}{
		{name: "lower case market", market: "br", currency: "BRL"},
		{name: "unknown market", market: "MARS", currency: "BRL"},
		{name: "unsupported currency", market: "BR", currency: "EUR"},
		{name: "crossed pair", market: "BR", currency: "USD"},
	} {
		t.Run(rejected.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, insertIntent, acc.ID, rejected.market, "ink_10000", rejected.currency)
			assertPgCode(t, err, "23514")
		})
	}

	// Stripe price identifiers follow the same rule as the domain, except that
	// a subscription always carries one (an unset price is not representable).
	priceIDs := []string{
		"price_brMember1", "price_" + strings.Repeat("a", 194), "prod_123", "price_", "price_ab_cd", "price_ação",
		"price_" + strings.Repeat("a", 195),
	}
	for index, priceID := range priceIDs {
		_, domainErr := domain.ParseStripePriceID(priceID)
		_, databaseErr := pool.Exec(ctx, `
			INSERT INTO app.subscriptions
				(account_id, stripe_subscription_id, status, livemode, market, product_id, catalog_version, stripe_price_id)
			VALUES ($1, $2, 'active', false, 'BR', 'member_monthly', 1, $3)`,
			acc.ID, fmt.Sprintf("sub_arenaVocab%02d", index), priceID)
		if (domainErr == nil) != (databaseErr == nil) {
			t.Errorf("stripe price %q: domain error = %v, database error = %v", priceID, domainErr, databaseErr)
		}
	}
}

// TestBillingIdentifiersArePrivateAndPayloadsAreNotPersisted proves the two
// privacy decisions of the schema: provider identifiers exist only inside the
// billing tables (never in a projection, view, export or public column), and
// the raw webhook payload is never persisted — only its digest and size.
func TestBillingIdentifiersArePrivateAndPayloadsAreNotPersisted(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := db.Pool.Pool()

	rows, err := pool.Query(ctx, `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = 'app' AND column_name ILIKE '%stripe%'
		ORDER BY table_name, column_name`)
	if err != nil {
		t.Fatalf("list stripe columns: %v", err)
	}
	defer rows.Close()

	allowed := map[string]bool{
		"stripe_customers": true,
		"stripe_events":    true,
		"checkout_intents": true,
		"subscriptions":    true,
	}
	found := 0
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		found++
		if !allowed[table] {
			t.Errorf("app.%s.%s carries a provider identifier outside the billing tables", table, column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	if found == 0 {
		t.Fatal("expected the billing tables to carry provider identifiers")
	}

	// The raw payload is never stored: the only payload columns in the whole
	// schema are the digest and the size of the verified body.
	payloadRows, err := pool.Query(ctx, `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = 'app' AND column_name ILIKE '%payload%'
		ORDER BY table_name, column_name`)
	if err != nil {
		t.Fatalf("list payload columns: %v", err)
	}
	defer payloadRows.Close()

	payloadColumns := make([]string, 0, 2)
	for payloadRows.Next() {
		var table, column string
		if err := payloadRows.Scan(&table, &column); err != nil {
			t.Fatalf("scan payload column: %v", err)
		}
		payloadColumns = append(payloadColumns, table+"."+column)
	}
	if err := payloadRows.Err(); err != nil {
		t.Fatalf("iterate payload columns: %v", err)
	}
	want := []string{"stripe_events.payload_bytes", "stripe_events.payload_sha256"}
	if strings.Join(payloadColumns, ",") != strings.Join(want, ",") {
		t.Errorf("payload columns = %v, want %v (the raw body is never persisted)", payloadColumns, want)
	}

	// No view re-exposes a billing table, and the objects belong to the NOLOGIN
	// owner instead of the runtime.
	tables := []string{
		"stripe_customers", "stripe_events", "checkout_intents", "subscriptions",
		"billing_reconciliation_runs", "billing_reconciliation_findings",
	}
	var viewCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.view_table_usage
		WHERE table_schema = 'app' AND table_name = ANY($1)`, tables).Scan(&viewCount); err != nil {
		t.Fatalf("count views over billing tables: %v", err)
	}
	if viewCount != 0 {
		t.Errorf("billing tables are exposed by %d view(s)", viewCount)
	}

	for _, table := range tables {
		var owner string
		if err := pool.QueryRow(ctx, `
			SELECT tableowner FROM pg_tables WHERE schemaname = 'app' AND tablename = $1`, table).Scan(&owner); err != nil {
			t.Fatalf("owner of %s: %v", table, err)
		}
		if owner != "arena_owner" {
			t.Errorf("owner of app.%s = %q, want arena_owner", table, owner)
		}
	}
}

// assertPgConstraint proves a rule was broken by the expected constraint, so a
// probe can never pass because of an unrelated CHECK.
func assertPgConstraint(t *testing.T, err error, wantCode, wantConstraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected PostgreSQL error %s (%s), got nil", wantCode, wantConstraint)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != wantCode || pgErr.ConstraintName != wantConstraint {
		t.Fatalf("error = %s (%s), want %s (%s)", pgErr.Code, pgErr.ConstraintName, wantCode, wantConstraint)
	}
}

// TestBillingTriggerRulesAreNamed proves every defensive rule of the schema is
// broken by its own named constraint, so a probe elsewhere in this file can
// never pass for the wrong reason.
func TestBillingTriggerRulesAreNamed(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "billing-rules@arena.example.com")
	mustStripeCustomer(t, ctx, pool, acc.ID, "cus_arenaRules1", false)
	mustStripeEvent(t, ctx, pool, "evt_arenaRules1")
	intentID := mustCheckoutIntent(t, ctx, pool, acc.ID)
	mustSubscription(t, ctx, pool, acc.ID, "sub_arenaRules1")
	runID := mustReconciliationRun(t, ctx, pool)

	var findingID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.billing_reconciliation_findings (run_id, account_id, kind, reference)
		VALUES ($1, $2, 'amount_mismatch', 'cs_test_intentOpen1')
		RETURNING id`, runID, acc.ID).Scan(&findingID); err != nil {
		t.Fatalf("seed finding: %v", err)
	}

	probes := []struct {
		name           string
		sql            string
		args           []any
		wantConstraint string
	}{
		{
			name:           "retention",
			sql:            "DELETE FROM app.stripe_events",
			wantConstraint: "billing_rows_retained",
		},
		{
			name:           "event identity",
			sql:            "UPDATE app.stripe_events SET payload_bytes = payload_bytes + 1 WHERE stripe_event_id = 'evt_arenaRules1'",
			wantConstraint: "stripe_events_identity_immutable",
		},
		{
			name:           "event transition",
			sql:            "UPDATE app.stripe_events SET status = 'processed', processed_at = now() WHERE stripe_event_id = 'evt_arenaRules1'",
			wantConstraint: "stripe_events_status_transition",
		},
		{
			name:           "customer mapping",
			sql:            "UPDATE app.stripe_customers SET livemode = true WHERE stripe_customer_id = 'cus_arenaRules1'",
			wantConstraint: "stripe_customers_mapping_immutable",
		},
		{
			name:           "intent facts",
			args:           []any{intentID},
			sql:            "UPDATE app.checkout_intents SET amount_minor = 199 WHERE id = $1",
			wantConstraint: "checkout_intents_immutable",
		},
		{
			name:           "intent transition",
			args:           []any{intentID},
			sql:            "UPDATE app.checkout_intents SET status = 'created' WHERE id = $1",
			wantConstraint: "checkout_intents_status_transition",
		},
		{
			name:           "subscription identity",
			sql:            "UPDATE app.subscriptions SET livemode = true WHERE stripe_subscription_id = 'sub_arenaRules1'",
			wantConstraint: "subscriptions_identity_immutable",
		},
		{
			name:           "subscription transition",
			sql:            "UPDATE app.subscriptions SET status = 'incomplete' WHERE stripe_subscription_id = 'sub_arenaRules1'",
			wantConstraint: "subscriptions_status_transition",
		},
		{
			name:           "finding content",
			args:           []any{findingID},
			sql:            "UPDATE app.billing_reconciliation_findings SET kind = 'missing_remote' WHERE id = $1",
			wantConstraint: "billing_reconciliation_findings_immutable",
		},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			var args []any
			if probe.args != nil {
				args = probe.args
			}
			_, err := pool.Exec(ctx, probe.sql, args...)
			assertPgConstraint(t, err, "23514", probe.wantConstraint)
		})
	}

	// The resolution is written once and then final.
	if _, err := pool.Exec(ctx, `
		UPDATE app.billing_reconciliation_findings
		SET resolved_at = now(), resolution = 'confirmed by the provider'
		WHERE id = $1`, findingID); err != nil {
		t.Fatalf("first resolution must be accepted: %v", err)
	}
	_, err := pool.Exec(ctx, `
		UPDATE app.billing_reconciliation_findings
		SET resolution = 'silently rewritten'
		WHERE id = $1`, findingID)
	assertPgConstraint(t, err, "23514", "billing_reconciliation_findings_resolution_final")
}
