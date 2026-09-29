package postgres_test

// P35-T06 — the INK purchase settlement registry holds its shape on
// real PostgreSQL.
//
// app.billing_ink_settlements records one liquidation per intent with
// its provider event: non-positive amounts, foreign currencies, empty
// events and duplicated intents or events die at CHECKs and UNIQUEs,
// unknown intents die at foreign keys, history rewrites die at
// triggers and grants, and the runtime role appends and reads but
// never rewrites. The suite runs on a disposable database and moves
// no money.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func settlementSchemaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func settlementSchemaIntent(t *testing.T, ctx context.Context, db *dbtest.TestDB) (account, quote, intent string) {
	t.Helper()
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('settle-schema-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&account); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := db.QueryRow(ctx,
		`INSERT INTO app.pricing_quotes (price_minor, observed_at, accepted_at, expires_at, quote_hash)
		 VALUES (35000000, '2026-10-02T12:00:00Z', '2026-10-02T12:00:03Z', '2026-10-02T12:05:03Z', 'schema-seal')
		 RETURNING id::text`).Scan(&quote); err != nil {
		t.Fatalf("seed quote: %v", err)
	}
	if err := db.QueryRow(ctx,
		`INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ('schema-settle-1', $1::uuid, $2::uuid, 10000, 253833, 'schema-terms', 'pending', now())
		 RETURNING id::text`, account, quote).Scan(&intent); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	return account, quote, intent
}

// TestSettlementRegistryKeysAndEvents proves one row per intent with
// its provider event: non-positive amounts, foreign currencies, empty
// events and duplicated intents or events die at CHECKs and UNIQUEs,
// unknown intents die at foreign keys.
func TestSettlementRegistryKeysAndEvents(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := settlementSchemaCtx()
	defer cancel()

	_, _, intent := settlementSchemaIntent(t, ctx, db)
	mustExecSettlementSchema(t, ctx, db,
		`INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, 'schema-evt-1', 10000, 'BRL', now())`, intent)

	var second string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 SELECT 'schema-settle-2', account_id, quote_id, 10000, 253833, 'schema-terms', 'pending', now()
		 FROM app.billing_ink_intents WHERE intent_key = 'schema-settle-1'
		 RETURNING id::text`).Scan(&second); err != nil {
		t.Fatalf("seed second intent: %v", err)
	}

	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"duplicate intent", `INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, 'schema-evt-2', 10000, 'BRL', now())`,
			[]any{intent}, "23505"},
		{"duplicate event", `INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, 'schema-evt-1', 10000, 'BRL', now())`,
			[]any{second}, "23505"},
		{"zero amount", `INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, 'schema-evt-3', 0, 'BRL', now())`,
			[]any{intent}, "23514"},
		{"foreign currency", `INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, 'schema-evt-4', 10000, 'USD', now())`,
			[]any{intent}, "23514"},
		{"empty event", `INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, '  ', 10000, 'BRL', now())`,
			[]any{intent}, "23514"},
		{"unknown intent", `INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ('00000000-0000-4000-8000-000000000002', 'schema-evt-5', 10000, 'BRL', now())`,
			nil, "23503"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}
}

// TestSettlementRegistryImmutableHistory proves triggers refuse
// UPDATE and DELETE on the registry for the owner role itself.
func TestSettlementRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := settlementSchemaCtx()
	defer cancel()

	_, _, intent := settlementSchemaIntent(t, ctx, db)
	mustExecSettlementSchema(t, ctx, db,
		`INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, 'schema-evt-8', 10000, 'BRL', now())`, intent)

	mutations := []struct {
		name string
		sql  string
	}{
		{"rewrite event", `UPDATE app.billing_ink_settlements SET event_id = 'schema-evt-9' WHERE event_id = 'schema-evt-8'`},
		{"delete settlement", `DELETE FROM app.billing_ink_settlements WHERE event_id = 'schema-evt-8'`},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql)
		assertPgCode(t, err, "23514")
	}
}

// TestSettlementRuntimeLeastPrivilege proves arena_app appends and
// reads but never rewrites: UPDATE and DELETE die at the grant level.
func TestSettlementRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update settlement", `UPDATE app.billing_ink_settlements SET amount_minor = 1`, "42501"},
			{"delete settlement", `DELETE FROM app.billing_ink_settlements`, "42501"},
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
		var settlements int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.billing_ink_settlements`).Scan(&settlements); err != nil {
			t.Fatalf("read settlements: %v", err)
		}
	})
}

func mustExecSettlementSchema(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
