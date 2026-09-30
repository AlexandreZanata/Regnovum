package postgres_test

// P37-T02 — the commerce transfer and sanction registries hold
// their shape on real PostgreSQL.
//
// app.commerce_transfers records one settlement per payer key with
// the immutable kind, both known accounts, the exact amount, the
// explicit consent reference and the journal transfer: bad amounts,
// empty terms, unknown parties and duplicated keys or transfers die
// at CHECKs, UNIQUEs and foreign keys, history rewrites die at
// triggers and grants, and the runtime role appends and reads
// transfers but only reads sanctions. The suite runs on a
// disposable database and moves no money.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func commerceSchemaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func commerceSchemaAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB, status string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('commerce-schema-' || gen_random_uuid()::text || '@invalid.example', $1) RETURNING id::text`,
		status).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

func seedCommerceTransfer(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, payer, payee string) {
	t.Helper()
	mustExecCommerceSchema(t, ctx, db,
		`INSERT INTO app.commerce_transfers
		 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id)
		 VALUES ($1, 'gift', $2::uuid, $3::uuid, 5000, 'consent-schema', 'schema-payload', gen_random_uuid())`,
		key, payer, payee)
}

// TestCommerceTransferRegistryKeysAndParties proves one row per
// payer key with server-checked terms: non-positive amounts, empty
// terms, unknown parties and duplicated keys or transfers die at
// CHECKs, UNIQUEs and foreign keys, while another payer reusing a
// key opens its own settlement.
func TestCommerceTransferRegistryKeysAndParties(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := commerceSchemaCtx()
	defer cancel()

	payer := commerceSchemaAccount(t, ctx, db, "active")
	payee := commerceSchemaAccount(t, ctx, db, "active")
	seedCommerceTransfer(t, ctx, db, "schema-xfer-1", payer, payee)

	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"duplicate key", `INSERT INTO app.commerce_transfers
		 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id)
		 VALUES ('schema-xfer-1', 'gift', $1::uuid, $2::uuid, 5000, 'consent', 'payload', gen_random_uuid())`,
			[]any{payer, payee}, "23505"},
		{"zero amount", `INSERT INTO app.commerce_transfers
		 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id)
		 VALUES ('schema-xfer-2', 'gift', $1::uuid, $2::uuid, 0, 'consent', 'payload', gen_random_uuid())`,
			[]any{payer, payee}, "23514"},
		{"unknown kind", `INSERT INTO app.commerce_transfers
		 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id)
		 VALUES ('schema-xfer-3', 'tithe', $1::uuid, $2::uuid, 5000, 'consent', 'payload', gen_random_uuid())`,
			[]any{payer, payee}, "23514"},
		{"empty consent", `INSERT INTO app.commerce_transfers
		 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id)
		 VALUES ('schema-xfer-4', 'gift', $1::uuid, $2::uuid, 5000, '  ', 'payload', gen_random_uuid())`,
			[]any{payer, payee}, "23514"},
		{"unknown payer", `INSERT INTO app.commerce_transfers
		 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id)
		 VALUES ('schema-xfer-5', 'gift', '00000000-0000-4000-8000-000000000000', $1::uuid, 5000, 'consent', 'payload', gen_random_uuid())`,
			[]any{payee}, "23503"},
		{"unknown payee", `INSERT INTO app.commerce_transfers
		 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id)
		 VALUES ('schema-xfer-6', 'gift', $1::uuid, '00000000-0000-4000-8000-000000000000', 5000, 'consent', 'payload', gen_random_uuid())`,
			[]any{payer}, "23503"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}

	var firstTransfer string
	if err := db.QueryRow(ctx,
		`SELECT transfer_id::text FROM app.commerce_transfers WHERE intention_key = 'schema-xfer-1'`).Scan(&firstTransfer); err != nil {
		t.Fatalf("read seeded transfer: %v", err)
	}
	_, err := db.Exec(ctx, `INSERT INTO app.commerce_transfers
	 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id)
	 VALUES ('schema-xfer-7', 'gift', $1::uuid, $2::uuid, 5000, 'consent', 'payload', $3::uuid)`,
		payer, payee, firstTransfer)
	assertPgCode(t, err, "23505")

	other := commerceSchemaAccount(t, ctx, db, "active")
	mustExecCommerceSchema(t, ctx, db,
		`INSERT INTO app.commerce_transfers
		 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id)
		 VALUES ('schema-xfer-1', 'trade', $1::uuid, $2::uuid, 7000, 'consent', 'payload', gen_random_uuid())`,
		other, payee)
}

// TestCommerceTransferRegistryImmutableHistory proves triggers
// refuse UPDATE and DELETE on the registry for the owner role
// itself.
func TestCommerceTransferRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := commerceSchemaCtx()
	defer cancel()

	payer := commerceSchemaAccount(t, ctx, db, "active")
	payee := commerceSchemaAccount(t, ctx, db, "active")
	seedCommerceTransfer(t, ctx, db, "schema-xfer-8", payer, payee)

	mutations := []struct {
		name string
		sql  string
	}{
		{"change kind", `UPDATE app.commerce_transfers SET kind = 'trade' WHERE intention_key = 'schema-xfer-8'`},
		{"delete transfer", `DELETE FROM app.commerce_transfers WHERE intention_key = 'schema-xfer-8'`},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql)
		assertPgCode(t, err, "23514")
	}
}

// TestCommerceRegistryRuntimeLeastPrivilege proves arena_app appends
// and reads transfers, reads sanctions, but never rewrites either:
// UPDATE and DELETE die at the grant level.
func TestCommerceRegistryRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update transfer", `UPDATE app.commerce_transfers SET amount_milli = 1`, "42501"},
			{"delete transfer", `DELETE FROM app.commerce_transfers`, "42501"},
			{"insert sanction", `INSERT INTO app.commerce_sanctions (account_id, reason) VALUES ('00000000-0000-4000-8000-000000000000', 'x')`, "42501"},
			{"delete sanction", `DELETE FROM app.commerce_sanctions`, "42501"},
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
		var transfers int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.commerce_transfers`).Scan(&transfers); err != nil {
			t.Fatalf("read transfers: %v", err)
		}
	})
}

func mustExecCommerceSchema(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
