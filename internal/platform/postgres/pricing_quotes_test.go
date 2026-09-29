package postgres_test

// P35-T03 — the purchase quote registry holds its shape on real
// PostgreSQL.
//
// app.pricing_quotes records one accepted snapshot per row with its
// integer price, instants and content hash, and
// app.pricing_quote_sources records one sighting per source and
// quote: non-positive prices, timeless snapshots, empty hashes and
// duplicated sources die at CHECKs and UNIQUEs, history rewrites die
// at triggers and grants, and the runtime role appends and reads but
// never rewrites. The suite runs on a disposable database and moves
// no money.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func quoteSchemaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func seedQuote(t *testing.T, ctx context.Context, db *dbtest.TestDB, price int64) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.pricing_quotes (price_minor, observed_at, accepted_at, expires_at, quote_hash)
		 VALUES ($1, '2026-10-02T12:00:00Z', '2026-10-02T12:00:03Z', '2026-10-02T12:05:03Z', 'schema-seal')
		 RETURNING id::text`, price).Scan(&id); err != nil {
		t.Fatalf("seed quote: %v", err)
	}
	return id
}

func seedQuoteSighting(t *testing.T, ctx context.Context, db *dbtest.TestDB, quote, source string, price int64) {
	t.Helper()
	mustExecQuoteSchema(t, ctx, db,
		`INSERT INTO app.pricing_quote_sources (quote_id, source, price_minor, observed_at, payload_hash)
		 VALUES ($1::uuid, $2, $3, '2026-10-02T12:00:00Z', 'schema-payload')`,
		quote, source, price)
}

// TestQuoteRegistryKeysAndPrices proves one audit row per acceptance
// with sealed terms: timeless snapshots, non-positive prices, empty
// hashes and duplicated sources die at CHECKs and UNIQUEs.
func TestQuoteRegistryKeysAndPrices(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := quoteSchemaCtx()
	defer cancel()

	id := seedQuote(t, ctx, db, 35000000)
	seedQuoteSighting(t, ctx, db, id, "fonte-1", 35000000)

	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"zero price", `INSERT INTO app.pricing_quotes (price_minor, observed_at, accepted_at, expires_at, quote_hash)
		 VALUES (0, '2026-10-02T12:00:00Z', '2026-10-02T12:00:03Z', '2026-10-02T12:05:03Z', 'seal')`,
			nil, "23514"},
		{"no lifetime", `INSERT INTO app.pricing_quotes (price_minor, observed_at, accepted_at, expires_at, quote_hash)
		 VALUES (35000000, '2026-10-02T12:00:00Z', '2026-10-02T12:05:03Z', '2026-10-02T12:05:03Z', 'seal')`,
			nil, "23514"},
		{"empty hash", `INSERT INTO app.pricing_quotes (price_minor, observed_at, accepted_at, expires_at, quote_hash)
		 VALUES (35000000, '2026-10-02T12:00:00Z', '2026-10-02T12:00:03Z', '2026-10-02T12:05:03Z', '  ')`,
			nil, "23514"},
		{"duplicate source", `INSERT INTO app.pricing_quote_sources (quote_id, source, price_minor, observed_at, payload_hash)
		 VALUES ($1::uuid, 'fonte-1', 35000000, '2026-10-02T12:00:00Z', 'seal')`,
			[]any{id}, "23505"},
		{"empty source", `INSERT INTO app.pricing_quote_sources (quote_id, source, price_minor, observed_at, payload_hash)
		 VALUES ($1::uuid, '  ', 35000000, '2026-10-02T12:00:00Z', 'seal')`,
			[]any{id}, "23514"},
		{"sighting zero price", `INSERT INTO app.pricing_quote_sources (quote_id, source, price_minor, observed_at, payload_hash)
		 VALUES ($1::uuid, 'fonte-2', 0, '2026-10-02T12:00:00Z', 'seal')`,
			[]any{id}, "23514"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}
}

// TestQuoteRegistryImmutableHistory proves triggers refuse UPDATE and
// DELETE on both registries for the owner role itself.
func TestQuoteRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := quoteSchemaCtx()
	defer cancel()

	id := seedQuote(t, ctx, db, 35000000)
	seedQuoteSighting(t, ctx, db, id, "fonte-1", 35000000)

	mutations := []struct {
		name string
		sql  string
	}{
		{"rewrite price", `UPDATE app.pricing_quotes SET price_minor = 1 WHERE id = '` + id + `'::uuid`},
		{"delete quote", `DELETE FROM app.pricing_quotes WHERE id = '` + id + `'::uuid`},
		{"rewrite sighting", `UPDATE app.pricing_quote_sources SET price_minor = 1 WHERE quote_id = '` + id + `'::uuid`},
		{"delete sighting", `DELETE FROM app.pricing_quote_sources WHERE quote_id = '` + id + `'::uuid`},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql)
		assertPgCode(t, err, "23514")
	}
}

// TestQuoteRuntimeLeastPrivilege proves arena_app appends and reads
// but never rewrites: UPDATE and DELETE die at the grant level.
func TestQuoteRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update quote", `UPDATE app.pricing_quotes SET price_minor = 1`, "42501"},
			{"delete quote", `DELETE FROM app.pricing_quotes`, "42501"},
			{"update sighting", `UPDATE app.pricing_quote_sources SET price_minor = 1`, "42501"},
			{"delete sighting", `DELETE FROM app.pricing_quote_sources`, "42501"},
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
		var quotes int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.pricing_quotes`).Scan(&quotes); err != nil {
			t.Fatalf("read quotes: %v", err)
		}
	})
}

func mustExecQuoteSchema(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
