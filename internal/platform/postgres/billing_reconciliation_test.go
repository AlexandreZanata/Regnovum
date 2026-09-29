package postgres_test

// P35-T07 — the reconciliation registries hold their shape on real
// PostgreSQL.
//
// app.billing_ink_charge_events records one gateway verdict per
// event, and app.billing_ink_intents now travels the closed
// PENDING|SETTLED|FAILED|REVIEW lifecycle: non-positive amounts,
// foreign currencies and verdicts, empty identities and duplicated
// events die at CHECKs and UNIQUEs, off-edge transitions and history
// rewrites die at triggers and grants, and the runtime role appends
// and reads but never rewrites. The suite runs on a disposable
// database and moves no money.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func reconcileSchemaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func reconcileSchemaIntent(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string) (account, intent string) {
	t.Helper()
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('reconcile-schema-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&account); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	var quote string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.pricing_quotes (price_minor, observed_at, accepted_at, expires_at, quote_hash)
		 VALUES (35000000, '2026-10-02T12:00:00Z', '2026-10-02T12:00:03Z', '2026-10-02T12:05:03Z', 'schema-seal')
		 RETURNING id::text`).Scan(&quote); err != nil {
		t.Fatalf("seed quote: %v", err)
	}
	if err := db.QueryRow(ctx,
		`INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ($1, $2::uuid, $3::uuid, 10000, 253833, 'schema-terms', 'pending', now())
		 RETURNING id::text`, key, account, quote).Scan(&intent); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	return account, intent
}

// TestChargeEventRegistryKeysAndVerdicts proves one row per gateway
// verdict with a closed status vocabulary: non-positive amounts,
// foreign currencies and verdicts, empty identities and duplicated
// events die at CHECKs and UNIQUEs.
func TestChargeEventRegistryKeysAndVerdicts(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := reconcileSchemaCtx()
	defer cancel()

	account, _ := reconcileSchemaIntent(t, ctx, db, "schema-rec-1")
	mustExecReconcileSchema(t, ctx, db,
		`INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ('schema-cevt-1', 'schema-rec-1', $1::uuid, 10000, 'BRL', 'paid', now())`, account)

	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"duplicate event", `INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ('schema-cevt-1', 'schema-rec-1', $1::uuid, 10000, 'BRL', 'paid', now())`,
			[]any{account}, "23505"},
		{"zero amount", `INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ('schema-cevt-2', 'schema-rec-1', $1::uuid, 0, 'BRL', 'paid', now())`,
			[]any{account}, "23514"},
		{"foreign currency", `INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ('schema-cevt-3', 'schema-rec-1', $1::uuid, 10000, 'USD', 'paid', now())`,
			[]any{account}, "23514"},
		{"foreign verdict", `INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ('schema-cevt-4', 'schema-rec-1', $1::uuid, 10000, 'BRL', 'success_page', now())`,
			[]any{account}, "23514"},
		{"empty event", `INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ('  ', 'schema-rec-1', $1::uuid, 10000, 'BRL', 'paid', now())`,
			[]any{account}, "23514"},
		{"empty key", `INSERT INTO app.billing_ink_charge_events
		 (event_id, intent_key, account_id, amount_minor, currency, status, received_at)
		 VALUES ('schema-cevt-6', '  ', $1::uuid, 10000, 'BRL', 'paid', now())`,
			[]any{account}, "23514"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}
}

// TestIntentLifecycleTravelsClosedEdges proves the pending lifecycle:
// pending settles, fails or awaits review, review settles or fails,
// and every other write dies at the transition trigger.
func TestIntentLifecycleTravelsClosedEdges(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := reconcileSchemaCtx()
	defer cancel()

	moves := []struct {
		name string
		from string
		to   string
		code string
	}{
		{"pending settles", "pending", "settled", ""},
		{"pending fails", "pending", "failed", ""},
		{"pending reviews", "pending", "review", ""},
		{"pending stays", "pending", "pending", "23514"},
		{"settled reopens", "settled", "failed", "23514"},
		{"failed settles", "failed", "settled", "23514"},
		{"review settles", "review", "settled", ""},
		{"review fails", "review", "failed", ""},
		{"review pends", "review", "pending", "23514"},
	}
	for i, move := range moves {
		key := "schema-edge-" + string(rune('a'+i))
		_, intent := reconcileSchemaIntent(t, ctx, db, key)
		if move.from != "pending" {
			mustExecReconcileSchema(t, ctx, db,
				`UPDATE app.billing_ink_intents SET status = $1 WHERE id = $2::uuid`, move.from, intent)
		}
		_, err := db.Exec(ctx,
			`UPDATE app.billing_ink_intents SET status = $1 WHERE id = $2::uuid`, move.to, intent)
		if move.code == "" {
			if err != nil {
				t.Fatalf("%s: %v", move.name, err)
			}
			continue
		}
		assertPgCode(t, err, move.code)
	}
	_, intent := reconcileSchemaIntent(t, ctx, db, "schema-edge-del")
	_, err := db.Exec(ctx, `DELETE FROM app.billing_ink_intents WHERE id = $1::uuid`, intent)
	assertPgCode(t, err, "23514")
}

// TestReconciliationRuntimeLeastPrivilege proves arena_app appends
// and reads but never rewrites: UPDATE and DELETE die at the grant
// level on both registries.
func TestReconciliationRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update intent", `UPDATE app.billing_ink_intents SET status = 'failed'`, "42501"},
			{"delete intent", `DELETE FROM app.billing_ink_intents`, "42501"},
			{"update event", `UPDATE app.billing_ink_charge_events SET status = 'failed'`, "42501"},
			{"delete event", `DELETE FROM app.billing_ink_charge_events`, "42501"},
		}
		for _, probe := range probes {
			if _, err := tx.Exec(ctx, `SAVEPOINT cell`); err != nil {
				t.Fatalf("savepoint: %v", err)
			}
			_, err := tx.Exec(ctx, probe.sql)
			assertPgCode(t, err, probe.want)
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT cell`); err != nil {
				t.Fatalf("rollback to savepoint: %v", err)
			}
		}
		var events int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.billing_ink_charge_events`).Scan(&events); err != nil {
			t.Fatalf("read events: %v", err)
		}
	})
}

func mustExecReconcileSchema(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
