package postgres_test

// P35-T05 — the INK purchase intent registry holds its shape on real
// PostgreSQL.
//
// app.billing_ink_intents records one acceptance per buyer key with
// the sealed quotation, the server-derived amounts, the terms hash,
// the backing hold and a pending status: non-positive amounts, empty
// hashes, non-pending statuses and duplicated keys die at CHECKs and
// UNIQUEs, unknown buyers and quotes die at foreign keys, history
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

func inkIntentSchemaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func inkIntentSchemaQuote(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.pricing_quotes (price_minor, observed_at, accepted_at, expires_at, quote_hash)
		 VALUES (35000000, '2026-10-02T12:00:00Z', '2026-10-02T12:00:03Z', '2026-10-02T12:05:03Z', 'schema-seal')
		 RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("seed quote: %v", err)
	}
	return id
}

func inkIntentSchemaAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('intent-schema-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

func seedInkIntent(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, account, quote string) {
	t.Helper()
	mustExecInkIntentSchema(t, ctx, db,
		`INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ($1, $2::uuid, $3::uuid, 10000, 253833, 'schema-terms', 'pending', now())`,
		key, account, quote)
}

// TestInkIntentRegistryKeysAndTerms proves one row per buyer key with
// server-sealed terms: non-positive amounts, empty hashes,
// non-pending statuses and duplicated keys die at CHECKs and UNIQUEs,
// unknown buyers and quotes die at foreign keys.
func TestInkIntentRegistryKeysAndTerms(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := inkIntentSchemaCtx()
	defer cancel()

	account := inkIntentSchemaAccount(t, ctx, db)
	quote := inkIntentSchemaQuote(t, ctx, db)
	seedInkIntent(t, ctx, db, "schema-intent-1", account, quote)

	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"duplicate key", `INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ('schema-intent-1', $1::uuid, $2::uuid, 10000, 253833, 'seal', 'pending', now())`,
			[]any{account, quote}, "23505"},
		{"zero fiat", `INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ('schema-intent-2', $1::uuid, $2::uuid, 0, 253833, 'seal', 'pending', now())`,
			[]any{account, quote}, "23514"},
		{"zero ink", `INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ('schema-intent-3', $1::uuid, $2::uuid, 10000, 0, 'seal', 'pending', now())`,
			[]any{account, quote}, "23514"},
		{"empty hash", `INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ('schema-intent-4', $1::uuid, $2::uuid, 10000, 253833, '  ', 'pending', now())`,
			[]any{account, quote}, "23514"},
		{"non-pending status", `INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ('schema-intent-5', $1::uuid, $2::uuid, 10000, 253833, 'seal', 'settled', now())`,
			[]any{account, quote}, "23514"},
		{"unknown account", `INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ('schema-intent-6', '00000000-0000-4000-8000-000000000000', $1::uuid, 10000, 253833, 'seal', 'pending', now())`,
			[]any{quote}, "23503"},
		{"unknown quote", `INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ('schema-intent-7', $1::uuid, '00000000-0000-4000-8000-000000000000', 10000, 253833, 'seal', 'pending', now())`,
			[]any{account}, "23503"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}

	other := inkIntentSchemaAccount(t, ctx, db)
	mustExecInkIntentSchema(t, ctx, db,
		`INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 VALUES ('schema-intent-1', $1::uuid, $2::uuid, 10000, 253833, 'seal', 'pending', now())`,
		other, quote)
}

// TestInkIntentRegistryImmutableHistory proves triggers refuse UPDATE
// and DELETE on the registry for the owner role itself.
func TestInkIntentRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := inkIntentSchemaCtx()
	defer cancel()

	account := inkIntentSchemaAccount(t, ctx, db)
	quote := inkIntentSchemaQuote(t, ctx, db)
	seedInkIntent(t, ctx, db, "schema-intent-8", account, quote)

	mutations := []struct {
		name string
		sql  string
	}{
		{"settle status", `UPDATE app.billing_ink_intents SET status = 'settled' WHERE intent_key = 'schema-intent-8'`},
		{"delete intent", `DELETE FROM app.billing_ink_intents WHERE intent_key = 'schema-intent-8'`},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql)
		assertPgCode(t, err, "23514")
	}
}

// TestInkIntentRuntimeLeastPrivilege proves arena_app appends and
// reads but never rewrites: UPDATE and DELETE die at the grant level.
func TestInkIntentRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update intent", `UPDATE app.billing_ink_intents SET fiat_minor = 1`, "42501"},
			{"delete intent", `DELETE FROM app.billing_ink_intents`, "42501"},
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
		var intents int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.billing_ink_intents`).Scan(&intents); err != nil {
			t.Fatalf("read intents: %v", err)
		}
	})
}

func mustExecInkIntentSchema(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
