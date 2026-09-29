package postgres_test

// P35-T08 — the INK purchase chargeback registry holds its shape on
// real PostgreSQL.
//
// app.billing_ink_chargebacks records one reversal per disputed
// liquidation with the revoked holder amount and the treasury cover:
// non-positive disputes, empty events and duplicated liquidations or
// events die at CHECKs and UNIQUEs, unknown liquidations die at
// foreign keys, history rewrites die at triggers and grants, and the
// runtime role appends and reads but never rewrites. The suite runs
// on a disposable database and moves no money.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func chargebackSchemaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func chargebackSchemaSettlement(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var account, quote, intent, settlement string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('chargeback-schema-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&account); err != nil {
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
		 VALUES ('schema-cb-1', $1::uuid, $2::uuid, 10000, 253833, 'schema-terms', 'settled', now())
		 RETURNING id::text`, account, quote).Scan(&intent); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if err := db.QueryRow(ctx,
		`INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, 'schema-evt-1', 10000, 'BRL', now())
		 RETURNING id::text`, intent).Scan(&settlement); err != nil {
		t.Fatalf("seed settlement: %v", err)
	}
	return settlement
}

// TestChargebackRegistryKeysAndCompensation proves one row per
// dispute with linked compensation: non-positive disputes, empty
// events and duplicated liquidations or events die at CHECKs and
// UNIQUEs, unknown liquidations die at foreign keys.
func TestChargebackRegistryKeysAndCompensation(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := chargebackSchemaCtx()
	defer cancel()

	settlement := chargebackSchemaSettlement(t, ctx, db)
	mustExecChargebackSchema(t, ctx, db,
		`INSERT INTO app.billing_ink_chargebacks
		 (settlement_id, event_id, disputed_minor, ink_revoked, treasury_covered, received_at)
		 VALUES ($1::uuid, 'schema-dp-1', 10000, 253833, 0, now())`, settlement)

	var second string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.billing_ink_intents
		 (intent_key, account_id, quote_id, fiat_minor, ink_milli, terms_hash, status, decided_at)
		 SELECT 'schema-cb-2', account_id, quote_id, 10000, 253833, 'schema-terms', 'settled', now()
		 FROM app.billing_ink_intents WHERE intent_key = 'schema-cb-1'
		 RETURNING id::text`).Scan(&second); err != nil {
		t.Fatalf("seed second intent: %v", err)
	}
	var secondSettlement string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.billing_ink_settlements (intent_id, event_id, amount_minor, currency, settled_at)
		 VALUES ($1::uuid, 'schema-evt-2', 10000, 'BRL', now())
		 RETURNING id::text`, second).Scan(&secondSettlement); err != nil {
		t.Fatalf("seed second settlement: %v", err)
	}

	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"duplicate liquidation", `INSERT INTO app.billing_ink_chargebacks
		 (settlement_id, event_id, disputed_minor, ink_revoked, treasury_covered, received_at)
		 VALUES ($1::uuid, 'schema-dp-2', 10000, 253833, 0, now())`,
			[]any{settlement}, "23505"},
		{"duplicate event", `INSERT INTO app.billing_ink_chargebacks
		 (settlement_id, event_id, disputed_minor, ink_revoked, treasury_covered, received_at)
		 VALUES ($1::uuid, 'schema-dp-1', 10000, 253833, 0, now())`,
			[]any{secondSettlement}, "23505"},
		{"zero dispute", `INSERT INTO app.billing_ink_chargebacks
		 (settlement_id, event_id, disputed_minor, ink_revoked, treasury_covered, received_at)
		 VALUES ($1::uuid, 'schema-dp-3', 0, 0, 0, now())`,
			[]any{secondSettlement}, "23514"},
		{"empty event", `INSERT INTO app.billing_ink_chargebacks
		 (settlement_id, event_id, disputed_minor, ink_revoked, treasury_covered, received_at)
		 VALUES ($1::uuid, '  ', 10000, 253833, 0, now())`,
			[]any{secondSettlement}, "23514"},
		{"unknown liquidation", `INSERT INTO app.billing_ink_chargebacks
		 (settlement_id, event_id, disputed_minor, ink_revoked, treasury_covered, received_at)
		 VALUES ('00000000-0000-4000-8000-000000000001', 'schema-dp-4', 10000, 253833, 0, now())`,
			nil, "23503"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}
}

// TestChargebackRegistryImmutableHistory proves triggers refuse
// UPDATE and DELETE on the registry for the owner role itself.
func TestChargebackRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := chargebackSchemaCtx()
	defer cancel()

	settlement := chargebackSchemaSettlement(t, ctx, db)
	mustExecChargebackSchema(t, ctx, db,
		`INSERT INTO app.billing_ink_chargebacks
		 (settlement_id, event_id, disputed_minor, ink_revoked, treasury_covered, received_at)
		 VALUES ($1::uuid, 'schema-dp-8', 10000, 253833, 0, now())`, settlement)

	mutations := []struct {
		name string
		sql  string
	}{
		{"rewrite revocation", `UPDATE app.billing_ink_chargebacks SET ink_revoked = 1 WHERE event_id = 'schema-dp-8'`},
		{"delete chargeback", `DELETE FROM app.billing_ink_chargebacks WHERE event_id = 'schema-dp-8'`},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql)
		assertPgCode(t, err, "23514")
	}
}

// TestChargebackRuntimeLeastPrivilege proves arena_app appends and
// reads but never rewrites: UPDATE and DELETE die at the grant level.
func TestChargebackRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update chargeback", `UPDATE app.billing_ink_chargebacks SET ink_revoked = 1`, "42501"},
			{"delete chargeback", `DELETE FROM app.billing_ink_chargebacks`, "42501"},
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
		var chargebacks int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.billing_ink_chargebacks`).Scan(&chargebacks); err != nil {
			t.Fatalf("read chargebacks: %v", err)
		}
	})
}

func mustExecChargebackSchema(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
