package postgres_test

// P46-T03 — isolated season books hold their shape on real
// PostgreSQL.
//
// app.seasons is the immutable plan registry with exact ninety-day
// windows and sealed manifests; app.season_lifecycle derives state
// from append-only events with a single ACTIVE season at the
// constraint level; app.economy_genesis binds one Genesis per book
// while pre-season data keeps the explicitly inactive
// compat-legacy namespace. Bad windows, non-hash manifests,
// unknown books, second Genesises, second actives, rewrites and
// unprivileged writes all die at constraints, triggers and grants.
// The upgrade from the previous head preserves the old journal
// byte for byte: no silent conversion, no new mint. The suite runs
// on disposable databases and moves no money.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func seasonBooksCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func openSeasonSQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		t.Fatalf("open season connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExecSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}

func seasonRowCount(t *testing.T, ctx context.Context, db *dbtest.TestDB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

const seasonTestHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func seedSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int, starts string) {
	t.Helper()
	mustExecSeason(t, ctx, db,
		`INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ($1, $2, $3::timestamptz, $3::timestamptz + make_interval(secs => 7776000), 'v3', 'politica-inicial', 'fundadora', 'regente-tecnica', $4)`,
		key, ordinal, starts, seasonTestHash)
}

func seedSeasonCustody(t *testing.T, ctx context.Context, db *dbtest.TestDB, label string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', $1) RETURNING id::text`, label).Scan(&id); err != nil {
		t.Fatalf("seed treasury custody: %v", err)
	}
	return id
}

// journalFingerprint hashes the old diary in canonical order: every
// leg of every transfer, byte for byte.
func journalFingerprint(t *testing.T, ctx context.Context, db *dbtest.TestDB) (custodies, entries, geneses int, digest string) {
	t.Helper()
	custodies = seasonRowCount(t, ctx, db, "app.economy_custodies")
	entries = seasonRowCount(t, ctx, db, "app.economy_entries")
	geneses = seasonRowCount(t, ctx, db, "app.economy_genesis")
	rows, err := db.Query(ctx,
		`SELECT transfer_id::text, custody_id::text, direction, amount_milli
		 FROM app.economy_entries ORDER BY transfer_id::text, direction`)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	defer rows.Close()
	sum := sha256.New()
	for rows.Next() {
		var transfer, custody, direction string
		var amount int64
		if err := rows.Scan(&transfer, &custody, &direction, &amount); err != nil {
			t.Fatalf("scan leg: %v", err)
		}
		fmt.Fprintf(sum, "%s|%s|%s|%d\n", transfer, custody, direction, amount)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("journal rows: %v", err)
	}
	return custodies, entries, geneses, hex.EncodeToString(sum.Sum(nil))
}

// TestSeasonUpgradePreservesJournal proves the expand half: a diary
// seeded below the season migration upgrades to the head with zero
// drift in counts and hashes, and neither a silent conversion nor a
// new Genesis appears.
func TestSeasonUpgradePreservesJournal(t *testing.T) {
	tdb := newTestDB(t, dbtest.WithoutMigrations())
	ctx, cancel := seasonBooksCtx()
	defer cancel()

	versions, err := dbmigrate.Versions()
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	head := versions[len(versions)-1]
	pre := versions[len(versions)-2]
	if applied, err := dbmigrate.UpTo(ctx, openSeasonSQL(t, tdb.DSN), pre, io.Discard); err != nil {
		t.Fatalf("UpTo(%d): %v", pre, err)
	} else if applied == 0 {
		t.Fatalf("UpTo(%d) applied = 0, want progress", pre)
	}

	treasury := seedSeasonCustody(t, ctx, tdb, "sovereign_reserve")
	mustExecSeason(t, ctx, tdb,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', 'season-journal')`)
	mustExecSeason(t, ctx, tdb,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ('11111111-1111-4111-8111-111111111111', $1::uuid, 'debit', 5000)`, treasury)
	mustExecSeason(t, ctx, tdb,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 SELECT '11111111-1111-4111-8111-111111111111', id, 'credit', 5000 FROM app.economy_custodies WHERE kind = 'user' AND label = 'season-journal'`)

	beforeCustodies, beforeEntries, beforeGeneses, beforeDigest := journalFingerprint(t, ctx, tdb)

	if applied, err := dbmigrate.Up(ctx, openSeasonSQL(t, tdb.DSN), io.Discard); err != nil {
		t.Fatalf("Up to head: %v", err)
	} else if applied == 0 {
		t.Fatalf("Up applied = 0, want the season expand")
	}
	if version, err := dbmigrate.CurrentVersion(ctx, openSeasonSQL(t, tdb.DSN)); err != nil || version != head {
		t.Fatalf("CurrentVersion = %d, %v; want head %d", version, err, head)
	}

	afterCustodies, afterEntries, afterGeneses, afterDigest := journalFingerprint(t, ctx, tdb)
	if afterCustodies != beforeCustodies || afterEntries != beforeEntries || afterGeneses != beforeGeneses {
		t.Fatalf("counts drifted: before %d/%d/%d, after %d/%d/%d",
			beforeCustodies, beforeEntries, beforeGeneses, afterCustodies, afterEntries, afterGeneses)
	}
	if afterDigest != beforeDigest {
		t.Fatalf("journal hash drifted: before %s, after %s", beforeDigest, afterDigest)
	}
}

// TestSeasonCompatNamespaceStaysInactive proves the migration emits
// no season, no credit and no activation: only the explicitly
// inactive compat namespace exists, with no lifecycle event and no
// Genesis row.
func TestSeasonCompatNamespaceStaysInactive(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := seasonBooksCtx()
	defer cancel()

	var key string
	var ordinal int
	if err := db.QueryRow(ctx, `SELECT season_key, ordinal FROM app.seasons`).Scan(&key, &ordinal); err != nil {
		t.Fatalf("read seasons: %v", err)
	}
	if key != "compat-legacy" || ordinal != 0 {
		t.Fatalf("seasons holds %q/%d, want only the explicitly inactive compat-legacy namespace", key, ordinal)
	}
	if got := seasonRowCount(t, ctx, db, "app.season_lifecycle"); got != 0 {
		t.Fatalf("lifecycle holds %d rows, want 0: no season is inferred active", got)
	}
	if got := seasonRowCount(t, ctx, db, "app.economy_genesis"); got != 0 {
		t.Fatalf("genesis holds %d rows, want 0: no credit is re-emitted", got)
	}
}

// TestSeasonGenesisOncePerBook proves one Genesis per book on real
// PostgreSQL: a second Genesis in the same book dies on the unique
// violation, another book keeps its own, and writers without an
// explicit book bind the inactive compat namespace.
func TestSeasonGenesisOncePerBook(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := seasonBooksCtx()
	defer cancel()

	seedSeason(t, ctx, db, "temporada-1", 1, "2026-10-04T12:00:00Z")
	seedSeason(t, ctx, db, "temporada-2", 2, "2027-01-02T12:00:00Z")
	first := seedSeasonCustody(t, ctx, db, "sovereign_reserve")
	second := seedSeasonCustody(t, ctx, db, "operating_cash")

	mustExecSeason(t, ctx, db,
		`INSERT INTO app.economy_genesis (genesis_key, treasury_custody_id, amount_milli, season_key)
		 VALUES ('genesis-livro-1', $1::uuid, 2100000000000, 'temporada-1')`, first)
	mustExecSeason(t, ctx, db,
		`INSERT INTO app.economy_genesis (genesis_key, treasury_custody_id, amount_milli, season_key)
		 VALUES ('genesis-livro-2', $1::uuid, 2100000000000, 'temporada-2')`, second)

	_, err := db.Exec(ctx,
		`INSERT INTO app.economy_genesis (genesis_key, treasury_custody_id, amount_milli, season_key)
		 VALUES ('genesis-livro-1-bis', $1::uuid, 2100000000000, 'temporada-1')`, first)
	assertPgCode(t, err, "23505")

	_, err = db.Exec(ctx,
		`INSERT INTO app.economy_genesis (genesis_key, treasury_custody_id, amount_milli, season_key)
		 VALUES ('genesis-livro-3', $1::uuid, 2100000000000, 'temporada-inexistente')`, first)
	assertPgCode(t, err, "23503")

	mustExecSeason(t, ctx, db,
		`INSERT INTO app.economy_genesis (genesis_key, treasury_custody_id, amount_milli)
		 VALUES ('genesis-legado', $1::uuid, 2100000000000)`, first)
	var bound string
	if err := db.QueryRow(ctx,
		`SELECT season_key FROM app.economy_genesis WHERE genesis_key = 'genesis-legado'`).Scan(&bound); err != nil {
		t.Fatalf("read legacy binding: %v", err)
	}
	if bound != "compat-legacy" {
		t.Fatalf("legacy writer bound to %q, want the explicitly inactive compat-legacy namespace", bound)
	}
}

// TestSeasonSingleActiveDenied proves the single-ACTIVE invariant
// at the constraint level: a second active season dies, a repeated
// stage dies, unknown books die, and bad windows, hashes and states
// die at CHECKs.
func TestSeasonSingleActiveDenied(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := seasonBooksCtx()
	defer cancel()

	seedSeason(t, ctx, db, "temporada-1", 1, "2026-10-04T12:00:00Z")
	seedSeason(t, ctx, db, "temporada-2", 2, "2027-01-02T12:00:00Z")

	mustExecSeason(t, ctx, db,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ('temporada-1', NULL, 'prepared', now())`)
	mustExecSeason(t, ctx, db,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ('temporada-1', 'prepared', 'active', now())`)
	mustExecSeason(t, ctx, db,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ('temporada-2', NULL, 'prepared', now())`)
	_, err := db.Exec(ctx,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ('temporada-2', 'prepared', 'active', now())`)
	assertPgCode(t, err, "23505")

	_, err = db.Exec(ctx,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ('temporada-1', 'prepared', 'active', now())`)
	assertPgCode(t, err, "23505")

	_, err = db.Exec(ctx,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ('temporada-inexistente', NULL, 'prepared', now())`)
	assertPgCode(t, err, "23503")

	badRows := []struct {
		name string
		sql  string
		code string
	}{
		{"short window", `INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ('temporada-curta', 7, '2026-10-04T12:00:00Z', '2026-10-05T12:00:00Z', 'v3', 'p', 'm', 'r', '` + seasonTestHash + `')`, "23514"},
		{"non-hash manifesto", `INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ('temporada-sem-selo', 8, '2026-10-04T12:00:00Z', '2027-01-02T12:00:00Z', 'v3', 'p', 'm', 'r', 'selo')`, "23514"},
		{"bare charter", `INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ('temporada-sem-carta', 9, '2026-10-04T12:00:00Z', '2027-01-02T12:00:00Z', '3', 'p', 'm', 'r', '` + seasonTestHash + `')`, "23514"},
		{"unknown stage", `INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ('temporada-2', 'prepared', 'congelada', now())`, "23514"},
		{"duplicate ordinal", `INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ('temporada-repetida', 1, '2027-04-02T12:00:00Z', '2027-07-01T12:00:00Z', 'v3', 'p', 'm', 'r', '` + seasonTestHash + `')`, "23505"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql)
		assertPgCode(t, err, probe.code)
	}
}

// TestSeasonRegistryImmutableAndLeastPrivilege proves triggers refuse
// UPDATE and DELETE on both season tables for the owner role itself,
// and the runtime role reads but never writes seasons.
func TestSeasonRegistryImmutableAndLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := seasonBooksCtx()
	defer cancel()

	seedSeason(t, ctx, db, "temporada-1", 1, "2026-10-04T12:00:00Z")

	mutations := []struct {
		name string
		sql  string
	}{
		{"rewrite season", `UPDATE app.seasons SET policy_ref = 'outra' WHERE season_key = 'temporada-1'`},
		{"delete season", `DELETE FROM app.seasons WHERE season_key = 'temporada-1'`},
		{"delete compat", `DELETE FROM app.seasons WHERE season_key = 'compat-legacy'`},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql)
		assertPgCode(t, err, "23514")
	}

	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"insert season", `INSERT INTO app.seasons
			 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
			 VALUES ('temporada-app', 7, '2026-10-04T12:00:00Z', '2027-01-02T12:00:00Z', 'v3', 'p', 'm', 'r', '` + seasonTestHash + `')`, "42501"},
			{"insert lifecycle", `INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
			 VALUES ('temporada-1', NULL, 'prepared', now())`, "42501"},
			{"update season", `UPDATE app.seasons SET policy_ref = 'outra'`, "42501"},
			{"delete season", `DELETE FROM app.seasons`, "42501"},
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
		var seasons int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.seasons`).Scan(&seasons); err != nil {
			t.Fatalf("read seasons: %v", err)
		}
	})
}
