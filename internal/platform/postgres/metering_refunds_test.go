package postgres_test

// P36-T07 — the INK publication compensation registry holds its
// shape on real PostgreSQL.
//
// app.metering_refunds records one compensation per refunded
// publication with the refund key, the owning account, the causal
// link, the reversed amount, the compensating transfer and the
// database posted instant: non-positive amounts, empty terms,
// duplicated keys, doubled compensations and orphan causes die at
// CHECKs, UNIQUEs and foreign keys, history rewrites die at triggers
// and grants, and the runtime role appends and reads but never
// rewrites. The suite runs on a disposable database and moves no
// money.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func meteringRefundCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func seedRefundCause(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, account string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ($1, $2, 'argument-publish', 2, 11, 2750,
		  'v1:aa', 'seal-q', 'seal-payload', gen_random_uuid())
		 RETURNING id::text`, key, account).Scan(&id); err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	return id
}

func seedMeteringRefund(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, account, original string) {
	t.Helper()
	mustExecMeteringRefund(t, ctx, db,
		`INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason)
		 VALUES ($1, $2, $3::uuid, 2750, gen_random_uuid(), 'publication-error')`,
		key, account, original)
}

// TestMeteringRefundRegistryKeysAndCauses proves one compensation per
// refund key with a live cause: non-positive amounts, empty terms,
// duplicated keys, doubled compensations and orphan causes die at
// CHECKs, UNIQUEs and foreign keys, while another account reusing a
// key opens its own compensation.
func TestMeteringRefundRegistryKeysAndCauses(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := meteringRefundCtx()
	defer cancel()

	original := seedRefundCause(t, ctx, db, "schema-cause-1", "schema-acct")
	seedMeteringRefund(t, ctx, db, "schema-refund-1", "schema-acct", original)
	other := seedRefundCause(t, ctx, db, "schema-cause-2", "schema-acct")

	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"duplicate key", `INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason)
		 VALUES ('schema-refund-1', 'schema-acct', $1::uuid, 2750, gen_random_uuid(), 'retry')`,
			[]any{other}, "23505"},
		{"doubled compensation", `INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason)
		 VALUES ('schema-refund-2', 'schema-acct', $1::uuid, 2750, gen_random_uuid(), 'again')`,
			[]any{original}, "23505"},
		{"zero amount", `INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason)
		 VALUES ('schema-refund-3', 'schema-acct', $1::uuid, 0, gen_random_uuid(), 'zero')`,
			[]any{other}, "23514"},
		{"empty reason", `INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason)
		 VALUES ('schema-refund-4', 'schema-acct', $1::uuid, 2750, gen_random_uuid(), '  ')`,
			[]any{other}, "23514"},
		{"empty key", `INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason)
		 VALUES ('  ', 'schema-acct', $1::uuid, 2750, gen_random_uuid(), 'blank')`,
			[]any{other}, "23514"},
		{"orphan cause", `INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason)
		 VALUES ('schema-refund-5', 'schema-acct', '00000000-0000-4000-8000-000000000000', 2750, gen_random_uuid(), 'orphan')`,
			nil, "23503"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}

	mustExecMeteringRefund(t, ctx, db,
		`INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason)
		 VALUES ('schema-refund-1', 'schema-other', $1::uuid, 2750, gen_random_uuid(), 'theirs')`,
		other)

	var postedAt time.Time
	if err := db.QueryRow(ctx,
		`SELECT posted_at FROM app.metering_refunds WHERE refund_key = 'schema-refund-1' AND account_label = 'schema-acct'`).Scan(&postedAt); err != nil {
		t.Fatalf("read posted_at: %v", err)
	}
	if postedAt.IsZero() {
		t.Fatal("posted_at must come from the database clock, never zero")
	}
}

// TestMeteringRefundRegistryImmutableHistory proves triggers refuse
// UPDATE and DELETE on the registry for the owner role itself.
func TestMeteringRefundRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := meteringRefundCtx()
	defer cancel()

	original := seedRefundCause(t, ctx, db, "schema-cause-8", "schema-acct")
	seedMeteringRefund(t, ctx, db, "schema-refund-8", "schema-acct", original)

	mutations := []struct {
		name string
		sql  string
	}{
		{"rewrite reason", `UPDATE app.metering_refunds SET reason = 'rewritten' WHERE refund_key = 'schema-refund-8'`},
		{"delete refund", `DELETE FROM app.metering_refunds WHERE refund_key = 'schema-refund-8'`},
		{"rewrite cause", `UPDATE app.metering_publications SET amount_milli = 1 WHERE intention_key = 'schema-cause-8'`},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql)
		assertPgCode(t, err, "23514")
	}
}

// TestMeteringRefundRuntimeLeastPrivilege proves arena_app appends
// and reads but never rewrites: UPDATE and DELETE die at the grant
// level.
func TestMeteringRefundRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update refund", `UPDATE app.metering_refunds SET amount_milli = 1`, "42501"},
			{"delete refund", `DELETE FROM app.metering_refunds`, "42501"},
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
		var refunds int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.metering_refunds`).Scan(&refunds); err != nil {
			t.Fatalf("read refunds: %v", err)
		}
	})
}

func mustExecMeteringRefund(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
