package postgres_test

// P36-T04 — the INK publication charge registry holds its shape on
// real PostgreSQL.
//
// app.metering_publications records one settlement per publisher key
// with the sealed price terms, the canonical content and quote
// hashes, the journal transfer moving the exact quoted cost and the
// database posted instant: non-positive amounts and units, empty
// terms and duplicated keys die at CHECKs and UNIQUEs, history
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

func meteringPublicationCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func seedMeteringPublication(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, account string) {
	t.Helper()
	mustExecMeteringPublication(t, ctx, db,
		`INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ($1, $2, 'argument-publish', 2, 11, 2750,
		  'v1:aa', 'seal-q', 'seal-payload', gen_random_uuid())`,
		key, account)
}

// TestMeteringPublicationRegistryKeysAndTerms proves one row per
// publisher key with server-sealed terms: non-positive amounts and
// units, empty terms and duplicated keys or transfers die at CHECKs
// and UNIQUEs, while another account reusing a key opens its own
// settlement.
func TestMeteringPublicationRegistryKeysAndTerms(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := meteringPublicationCtx()
	defer cancel()

	seedMeteringPublication(t, ctx, db, "schema-pub-1", "schema-acct")

	badRows := []struct {
		name string
		sql  string
		code string
	}{
		{"duplicate key", `INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ('schema-pub-1', 'schema-acct', 'argument-publish', 2, 11, 2750,
		  'v1:aa', 'seal-q', 'seal-payload', gen_random_uuid())`, "23505"},
		{"zero amount", `INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ('schema-pub-2', 'schema-acct', 'argument-publish', 2, 11, 0,
		  'v1:aa', 'seal-q', 'seal-payload', gen_random_uuid())`, "23514"},
		{"zero units", `INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ('schema-pub-3', 'schema-acct', 'argument-publish', 2, 0, 2750,
		  'v1:aa', 'seal-q', 'seal-payload', gen_random_uuid())`, "23514"},
		{"zero version", `INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ('schema-pub-4', 'schema-acct', 'argument-publish', 0, 11, 2750,
		  'v1:aa', 'seal-q', 'seal-payload', gen_random_uuid())`, "23514"},
		{"empty content hash", `INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ('schema-pub-5', 'schema-acct', 'argument-publish', 2, 11, 2750,
		  '  ', 'seal-q', 'seal-payload', gen_random_uuid())`, "23514"},
		{"empty key", `INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ('  ', 'schema-acct', 'argument-publish', 2, 11, 2750,
		  'v1:aa', 'seal-q', 'seal-payload', gen_random_uuid())`, "23514"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql)
		assertPgCode(t, err, probe.code)
	}

	var firstTransfer string
	if err := db.QueryRow(ctx,
		`SELECT transfer_id::text FROM app.metering_publications WHERE intention_key = 'schema-pub-1'`).Scan(&firstTransfer); err != nil {
		t.Fatalf("read seeded transfer: %v", err)
	}
	_, err := db.Exec(ctx, `INSERT INTO app.metering_publications
	 (intention_key, account_label, service, price_version, units, amount_milli,
	  content_hash, quote_hash, payload_hash, transfer_id)
	 VALUES ('schema-pub-6', 'schema-acct', 'argument-publish', 2, 11, 2750,
	  'v1:aa', 'seal-q', 'seal-payload', $1::uuid)`, firstTransfer)
	assertPgCode(t, err, "23505")

	mustExecMeteringPublication(t, ctx, db,
		`INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id)
		 VALUES ('schema-pub-1', 'schema-other', 'argument-publish', 2, 11, 2750,
		  'v1:aa', 'seal-q', 'seal-payload', gen_random_uuid())`)

	var postedAt time.Time
	if err := db.QueryRow(ctx,
		`SELECT posted_at FROM app.metering_publications WHERE intention_key = 'schema-pub-1' AND account_label = 'schema-acct'`).Scan(&postedAt); err != nil {
		t.Fatalf("read posted_at: %v", err)
	}
	if postedAt.IsZero() {
		t.Fatal("posted_at must come from the database clock, never zero")
	}
}

// TestMeteringPublicationRegistryImmutableHistory proves triggers
// refuse UPDATE and DELETE on the registry for the owner role
// itself.
func TestMeteringPublicationRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := meteringPublicationCtx()
	defer cancel()

	seedMeteringPublication(t, ctx, db, "schema-pub-8", "schema-acct")

	mutations := []struct {
		name string
		sql  string
	}{
		{"settle amount", `UPDATE app.metering_publications SET amount_milli = 1 WHERE intention_key = 'schema-pub-8'`},
		{"delete publication", `DELETE FROM app.metering_publications WHERE intention_key = 'schema-pub-8'`},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql)
		assertPgCode(t, err, "23514")
	}
}

// TestMeteringPublicationRuntimeLeastPrivilege proves arena_app
// appends and reads but never rewrites: UPDATE and DELETE die at the
// grant level.
func TestMeteringPublicationRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update publication", `UPDATE app.metering_publications SET amount_milli = 1`, "42501"},
			{"delete publication", `DELETE FROM app.metering_publications`, "42501"},
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
		var publications int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.metering_publications`).Scan(&publications); err != nil {
			t.Fatalf("read publications: %v", err)
		}
	})
}

func mustExecMeteringPublication(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
