package postgres_test

// P37-T06 — the disguise review and step registries hold their shape
// on real PostgreSQL.
//
// app.commerce_disguise_reviews records one flag per gift transfer
// key with the closed reason, the minimized evidence digest and the
// reporter; app.commerce_disguise_steps records one contest and at
// most one terminal outcome per review with the act (basis, trail,
// explicit charge, appeal window) for confirmations. Bad reasons,
// non-hash evidence, malformed steps and duplicated keys die at
// CHECKs, UNIQUEs and foreign keys, history rewrites die at triggers
// and grants, and the runtime role appends and reads but never
// rewrites. No step ever writes a ledger leg. The suite runs on a
// disposable database and moves no money.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func disguiseSchemaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func disguiseSchemaAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('commerce-disguise-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

func seedDisguiseTransfer(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, payer, payee string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.commerce_transfers
		 (intention_key, kind, payer_id, payee_id, amount_milli, consent_ref, payload_hash, transfer_id)
		 VALUES ($1, 'gift', $2::uuid, $3::uuid, 20000, 'consent-schema', 'schema-payload', gen_random_uuid())
		 RETURNING id::text`, key, payer, payee).Scan(&id); err != nil {
		t.Fatalf("seed transfer: %v", err)
	}
	return id
}

func seedDisguiseReview(t *testing.T, ctx context.Context, db *dbtest.TestDB, transfer, key string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.commerce_disguise_reviews
		 (transfer_id, review_key, reason, evidence_hash, reporter)
		 VALUES ($1::uuid, $2, 'contract', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 'watcher')
		 RETURNING id::text`, transfer, key).Scan(&id); err != nil {
		t.Fatalf("seed review: %v", err)
	}
	return id
}

// TestDisguiseRegistryKeysAndReasons proves one flag per transfer key
// with the closed reason and minimized proof sealed by CHECKs: bad
// reasons, non-hash evidence and duplicated keys or unknown transfers
// die, while another transfer reusing a key opens its own review.
func TestDisguiseRegistryKeysAndReasons(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := disguiseSchemaCtx()
	defer cancel()

	payer := disguiseSchemaAccount(t, ctx, db)
	payee := disguiseSchemaAccount(t, ctx, db)
	transfer := seedDisguiseTransfer(t, ctx, db, "schema-disguise-xfer-1", payer, payee)
	review := seedDisguiseReview(t, ctx, db, transfer, "schema-disguise-1")

	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"duplicate key", `INSERT INTO app.commerce_disguise_reviews
		 (transfer_id, review_key, reason, evidence_hash, reporter)
		 VALUES ($1::uuid, 'schema-disguise-1', 'contract', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 'watcher')`,
			[]any{transfer}, "23505"},
		{"unknown reason", `INSERT INTO app.commerce_disguise_reviews
		 (transfer_id, review_key, reason, evidence_hash, reporter)
		 VALUES ($1::uuid, 'schema-disguise-2', 'gift', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 'watcher')`,
			[]any{transfer}, "23514"},
		{"short evidence", `INSERT INTO app.commerce_disguise_reviews
		 (transfer_id, review_key, reason, evidence_hash, reporter)
		 VALUES ($1::uuid, 'schema-disguise-3', 'contract', 'abc', 'watcher')`,
			[]any{transfer}, "23514"},
		{"upper evidence", `INSERT INTO app.commerce_disguise_reviews
		 (transfer_id, review_key, reason, evidence_hash, reporter)
		 VALUES ($1::uuid, 'schema-disguise-4', 'contract', '0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF', 'watcher')`,
			[]any{transfer}, "23514"},
		{"blank reporter", `INSERT INTO app.commerce_disguise_reviews
		 (transfer_id, review_key, reason, evidence_hash, reporter)
		 VALUES ($1::uuid, 'schema-disguise-5', 'contract', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', '  ')`,
			[]any{transfer}, "23514"},
		{"unknown transfer", `INSERT INTO app.commerce_disguise_reviews
		 (transfer_id, review_key, reason, evidence_hash, reporter)
		 VALUES ('00000000-0000-4000-8000-000000000000', 'schema-disguise-6', 'contract', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 'watcher')`,
			nil, "23503"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}

	other := seedDisguiseTransfer(t, ctx, db, "schema-disguise-xfer-2", payer, payee)
	mustExecDisguiseSchema(t, ctx, db,
		`INSERT INTO app.commerce_disguise_reviews
		 (transfer_id, review_key, reason, evidence_hash, reporter)
		 VALUES ($1::uuid, 'schema-disguise-1', 'delivery', 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789', 'watcher')`, other)

	mustExecDisguiseSchema(t, ctx, db,
		`INSERT INTO app.commerce_disguise_steps (review_id, action, decided_by)
		 VALUES ($1::uuid, 'contest', $2)`, review, payer)
	_, err := db.Exec(ctx,
		`INSERT INTO app.commerce_disguise_steps (review_id, action, decided_by)
		 VALUES ($1::uuid, 'contest', $2)`, review, payee)
	assertPgCode(t, err, "23505")
	_, err = db.Exec(ctx,
		`INSERT INTO app.commerce_disguise_steps (review_id, action, decided_by, basis, trail_hash, charge_milli, appeal_until)
		 VALUES ($1::uuid, 'confirm', 'reviewer', 'contrato', 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789', 2000, now() + interval '72 hours')`, review)
	if err != nil {
		t.Fatalf("confirm after contest: %v", err)
	}
	_, err = db.Exec(ctx,
		`INSERT INTO app.commerce_disguise_steps (review_id, action, decided_by, appeal_until)
		 VALUES ($1::uuid, 'dismiss', 'reviewer', now() + interval '72 hours')`, review)
	assertPgCode(t, err, "23505")
	_, err = db.Exec(ctx,
		`INSERT INTO app.commerce_disguise_steps (review_id, action, decided_by, appeal_until)
		 VALUES ($1::uuid, 'dismiss', 'reviewer', NULL)`, review)
	assertPgCode(t, err, "23514")
	_, err = db.Exec(ctx,
		`INSERT INTO app.commerce_disguise_steps (review_id, action, decided_by, basis, trail_hash, charge_milli, appeal_until)
		 VALUES ($1::uuid, 'confirm', 'reviewer', NULL, 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789', 2000, now() + interval '72 hours')`, review)
	assertPgCode(t, err, "23514")
}

// TestDisguiseRegistryImmutableHistory proves triggers refuse UPDATE
// and DELETE on both registries for the owner role itself.
func TestDisguiseRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := disguiseSchemaCtx()
	defer cancel()

	payer := disguiseSchemaAccount(t, ctx, db)
	payee := disguiseSchemaAccount(t, ctx, db)
	transfer := seedDisguiseTransfer(t, ctx, db, "schema-disguise-xfer-8", payer, payee)
	review := seedDisguiseReview(t, ctx, db, transfer, "schema-disguise-8")
	mustExecDisguiseSchema(t, ctx, db,
		`INSERT INTO app.commerce_disguise_steps (review_id, action, decided_by)
		 VALUES ($1::uuid, 'contest', $2)`, review, payer)

	mutations := []struct {
		name string
		sql  string
		args []any
	}{
		{"rewrite review", `UPDATE app.commerce_disguise_reviews SET reason = 'delivery' WHERE review_key = 'schema-disguise-8'`, nil},
		{"delete review", `DELETE FROM app.commerce_disguise_reviews WHERE review_key = 'schema-disguise-8'`, nil},
		{"delete step", `DELETE FROM app.commerce_disguise_steps WHERE review_id = $1::uuid`, []any{review}},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql, mutation.args...)
		assertPgCode(t, err, "23514")
	}
}

// TestDisguiseRuntimeLeastPrivilege proves arena_app appends and reads
// reviews and steps but never rewrites them: UPDATE and DELETE die at
// the grant level.
func TestDisguiseRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update review", `UPDATE app.commerce_disguise_reviews SET reason = 'delivery'`, "42501"},
			{"delete review", `DELETE FROM app.commerce_disguise_reviews`, "42501"},
			{"update step", `UPDATE app.commerce_disguise_steps SET action = 'dismiss'`, "42501"},
			{"delete step", `DELETE FROM app.commerce_disguise_steps`, "42501"},
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
		var reviews int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.commerce_disguise_reviews`).Scan(&reviews); err != nil {
			t.Fatalf("read reviews: %v", err)
		}
	})
}

func mustExecDisguiseSchema(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
