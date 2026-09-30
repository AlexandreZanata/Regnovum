package dbmigrate_test

// P25-T02 — matriz completa de compatibilidade das migrations.
//
// O runner é forward-only (Up, UpTo, Status, CurrentVersion, Versions) sobre
// a lib goose pinada; cada migration roda em transação própria. A matriz
// prova sobre PostgreSQL descartável real: banco vazio, cada snapshot
// UpTo(v) da história, upgrade N−1→N com dataset sintético e checksum,
// falha no meio sem versão parcial, retry após correção, Up concorrente
// serializado e ausência de downgrade automatizado (superfície de API
// travada + varredura expand/contract das seções Up). O tempo é medido com
// dataset representativo; downgrade destrutivo não existe no runner.

import (
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

func matrixBaseDSN() string {
	if dsn := os.Getenv("ARENA_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return dbtest.DefaultAdminDSN
}

// emptyDatabase provisions a disposable database without migrations.
//
// The returned TestDB owns the database lifecycle (creation and teardown);
// open a fresh *sql.DB per dbmigrate operation with openSQL because the
// goose provider closes the handle it is given.
func emptyDatabase(t *testing.T) *dbtest.TestDB {
	t.Helper()
	return dbtest.New(t, dbtest.WithoutMigrations(), dbtest.WithBaseDSN(matrixBaseDSN()))
}

// openSQL opens a short-lived handle to a test database. Handles handed to
// dbmigrate must not be reused: provider.Close releases them.
func openSQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		t.Fatalf("open test connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func matrixContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func mustVersions(t *testing.T) []int64 {
	t.Helper()
	versions, err := dbmigrate.Versions()
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) == 0 {
		t.Fatal("empty migration history (anti-vacuity guard)")
	}
	return versions
}

func mustCurrentVersion(t *testing.T, ctx context.Context, db *sql.DB) int64 {
	t.Helper()
	version, err := dbmigrate.CurrentVersion(ctx, db)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	return version
}

// TestMigrationMatrixEmptyDatabase proves the virgin path: nothing applied,
// Up brings the full history, and re-running is a no-op.
func TestMigrationMatrixEmptyDatabase(t *testing.T) {
	tdb := emptyDatabase(t)
	ctx, cancel := matrixContext(60 * time.Second)
	defer cancel()

	if version := mustCurrentVersion(t, ctx, openSQL(t, tdb.DSN)); version != 0 {
		t.Fatalf("CurrentVersion on empty database = %d, want 0", version)
	}
	rows, err := dbmigrate.Status(ctx, openSQL(t, tdb.DSN))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	versions := mustVersions(t)
	if len(rows) != len(versions) {
		t.Fatalf("Status rows = %d, want %d (one per embedded migration)", len(rows), len(versions))
	}
	for _, row := range rows {
		if row.State != goose.StatePending {
			t.Fatalf("fresh status of %d = %s, want pending", row.Version, row.State)
		}
	}

	applied, err := dbmigrate.Up(ctx, openSQL(t, tdb.DSN), io.Discard)
	if err != nil {
		t.Fatalf("Up on empty database: %v", err)
	}
	if applied != len(versions) {
		t.Fatalf("Up applied = %d, want %d", applied, len(versions))
	}
	if version := mustCurrentVersion(t, ctx, openSQL(t, tdb.DSN)); version != versions[len(versions)-1] {
		t.Fatalf("CurrentVersion after Up = %d, want head %d", version, versions[len(versions)-1])
	}

	applied, err = dbmigrate.Up(ctx, openSQL(t, tdb.DSN), io.Discard)
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if applied != 0 {
		t.Fatalf("second Up applied = %d, want 0 (idempotent no-op)", applied)
	}
}

// TestMigrationMatrixEverySnapshot advances a virgin database to every
// version of the embedded history through UpTo (the same audited path as
// Up) and proves each snapshot: applied count, version and the
// applied/pending split of the status.
func TestMigrationMatrixEverySnapshot(t *testing.T) {
	versions := mustVersions(t)
	for index, version := range versions {
		t.Run(fmt.Sprintf("v%05d", version), func(t *testing.T) {
			tdb := emptyDatabase(t)
			ctx, cancel := matrixContext(60 * time.Second)
			defer cancel()

			applied, err := dbmigrate.UpTo(ctx, openSQL(t, tdb.DSN), version, io.Discard)
			if err != nil {
				t.Fatalf("UpTo(%d): %v", version, err)
			}
			if applied != index+1 {
				t.Fatalf("UpTo(%d) applied = %d, want %d", version, applied, index+1)
			}
			if got := mustCurrentVersion(t, ctx, openSQL(t, tdb.DSN)); got != version {
				t.Fatalf("CurrentVersion = %d, want %d", got, version)
			}
			rows, err := dbmigrate.Status(ctx, openSQL(t, tdb.DSN))
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			for _, row := range rows {
				want := goose.StatePending
				if row.Version <= version {
					want = goose.StateApplied
				}
				if row.State != want {
					t.Fatalf("status of %d = %s, want %s at snapshot %d", row.Version, row.State, want, version)
				}
			}
		})
	}
}

type tableChecksum struct {
	count int
	hash  string
}

func checksumTable(t *testing.T, ctx context.Context, db *sql.DB, table, key, value string) tableChecksum {
	t.Helper()
	var checksum tableChecksum
	query := fmt.Sprintf("SELECT count(*), COALESCE(md5(string_agg(%s, ',' ORDER BY %s)), '') FROM %s", value, key, table)
	if err := db.QueryRowContext(ctx, query).Scan(&checksum.count, &checksum.hash); err != nil {
		t.Fatalf("checksum %s: %v", table, err)
	}
	return checksum
}

// seedRepresentativeDataset writes a small synthetic dataset across the
// identity, profile, wallet and category surfaces and returns per-table
// checksums. Every value is synthetic (t02- prefix) and the database is
// disposable, so no cleanup beyond the harness teardown is needed.
func seedRepresentativeDataset(t *testing.T, ctx context.Context, db *sql.DB, size int) map[string]tableChecksum {
	t.Helper()
	for i := 0; i < size; i++ {
		email := fmt.Sprintf("t02-check-%04d@arena.example.com", i)
		var accountID string
		if err := db.QueryRowContext(ctx,
			`INSERT INTO app.accounts (email) VALUES ($1) RETURNING id::text`, email).Scan(&accountID); err != nil {
			t.Fatalf("seed account %d: %v", i, err)
		}
		username := fmt.Sprintf("t02user%04d", i)
		if _, err := db.ExecContext(ctx,
			`INSERT INTO app.profiles (account_id, username, username_normalized) VALUES ($1::uuid, $2, $3)`,
			accountID, username, username); err != nil {
			t.Fatalf("seed profile %d: %v", i, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO app.wallet_accounts (account_id) VALUES ($1::uuid)`, accountID); err != nil {
			t.Fatalf("seed wallet %d: %v", i, err)
		}
		if i < 10 {
			if _, err := db.ExecContext(ctx,
				`INSERT INTO app.categories (slug) VALUES ($1)`, fmt.Sprintf("t02cat%04d", i)); err != nil {
				t.Fatalf("seed category %d: %v", i, err)
			}
		}
	}
	return map[string]tableChecksum{
		"app.accounts":        checksumTable(t, ctx, db, "app.accounts", "email", "email"),
		"app.profiles":        checksumTable(t, ctx, db, "app.profiles", "username", "username"),
		"app.wallet_accounts": checksumTable(t, ctx, db, "app.wallet_accounts", "account_id::text", "account_id::text"),
		"app.categories":      checksumTable(t, ctx, db, "app.categories", "slug", "slug"),
	}
}

// TestMigrationMatrixUpgradePreservesData upgrades N−1 to N over a seeded
// dataset and proves preservation by checksum: every seeded row survives
// byte-identical, the version advances by exactly one, and the head schema
// still accepts the writes the N−1 application performs (coexistence
// window: the head migration only adds indexes).
func TestMigrationMatrixUpgradePreservesData(t *testing.T) {
	versions := mustVersions(t)
	previous := versions[len(versions)-2]
	head := versions[len(versions)-1]

	db := emptyDatabase(t)
	ctx, cancel := matrixContext(120 * time.Second)
	defer cancel()

	if applied, err := dbmigrate.UpTo(ctx, openSQL(t, db.DSN), previous, io.Discard); err != nil || applied != len(versions)-1 {
		t.Fatalf("UpTo(%d) = (%d, %v), want (%d, nil)", previous, applied, err, len(versions)-1)
	}

	work := openSQL(t, db.DSN)
	seedStart := time.Now()
	before := seedRepresentativeDataset(t, ctx, work, 50)
	seedCost := time.Since(seedStart)

	upgradeStart := time.Now()
	applied, err := dbmigrate.Up(ctx, openSQL(t, db.DSN), io.Discard)
	upgradeCost := time.Since(upgradeStart)
	if err != nil {
		t.Fatalf("Up N-1 to N: %v", err)
	}
	if applied != 1 {
		t.Fatalf("Up N-1 to N applied = %d, want exactly 1", applied)
	}
	if version := mustCurrentVersion(t, ctx, openSQL(t, db.DSN)); version != head {
		t.Fatalf("CurrentVersion = %d, want head %d", version, head)
	}
	after := map[string]tableChecksum{
		"app.accounts":        checksumTable(t, ctx, work, "app.accounts", "email", "email"),
		"app.profiles":        checksumTable(t, ctx, work, "app.profiles", "username", "username"),
		"app.wallet_accounts": checksumTable(t, ctx, work, "app.wallet_accounts", "account_id::text", "account_id::text"),
		"app.categories":      checksumTable(t, ctx, work, "app.categories", "slug", "slug"),
	}
	for table, want := range before {
		if got := after[table]; got != want {
			t.Fatalf("checksum drift on %s: before %+v, after %+v", table, want, got)
		}
	}

	// Coexistence: the previous application surface still writes and reads
	// on the upgraded schema.
	var id string
	if err := work.QueryRowContext(ctx,
		`INSERT INTO app.accounts (email) VALUES ('t02-coexist@arena.example.com') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("N-1 write on N schema: %v", err)
	}
	var email string
	if err := work.QueryRowContext(ctx, `SELECT email FROM app.accounts WHERE id = $1::uuid`, id).Scan(&email); err != nil {
		t.Fatalf("N-1 read on N schema: %v", err)
	}
	if email != "t02-coexist@arena.example.com" {
		t.Fatalf("round-trip email = %q", email)
	}
	t.Logf("seed 160 rows: %s; upgrade N-1 to N with dataset: %s", seedCost.Round(time.Millisecond), upgradeCost.Round(time.Millisecond))
}

// syntheticHistory builds a two-migration history: a good one and, unless
// fixed, one that fails halfway (valid statement, then a reference to a
// missing table). The failure is a real PostgreSQL error on a real
// database; only the content is synthetic, the mechanism under test — one
// transaction per migration — is the same one the runner uses.
func syntheticHistory(fixed bool) fstest.MapFS {
	bad := "INSERT INTO t02_probe_no_such_table VALUES (1);"
	if fixed {
		bad = "INSERT INTO t02_probe_bad VALUES (2);"
	}
	return fstest.MapFS{
		"00001_probe_ok.sql":  {Data: []byte("-- +goose Up\nCREATE TABLE t02_probe_ok (id integer PRIMARY KEY);\nINSERT INTO t02_probe_ok VALUES (1);\n-- +goose Down\nDROP TABLE t02_probe_ok;")},
		"00002_probe_bad.sql": {Data: []byte("-- +goose Up\nCREATE TABLE t02_probe_bad (id integer PRIMARY KEY);\nINSERT INTO t02_probe_bad VALUES (1);\n" + bad + "\n-- +goose Down\nDROP TABLE t02_probe_bad;")},
	}
}

func syntheticProvider(t *testing.T, db *sql.DB, history fstest.MapFS) *goose.Provider {
	t.Helper()
	store, err := database.NewStore(goose.DialectPostgres, dbmigrate.VersionTable)
	if err != nil {
		t.Fatalf("build version-table store: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectCustom, db, history, goose.WithStore(store))
	if err != nil {
		t.Fatalf("build synthetic provider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

// TestMigrationMatrixFailureLeavesNoPartialVersion proves the mechanism the
// runner relies on (one transaction per migration): a migration that fails
// halfway records no version and leaves no partial effect, and retrying
// with the fixed history succeeds on the same database.
func TestMigrationMatrixFailureLeavesNoPartialVersion(t *testing.T) {
	tdb := emptyDatabase(t)
	ctx, cancel := matrixContext(60 * time.Second)
	defer cancel()

	work := openSQL(t, tdb.DSN)
	if _, err := work.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS app"); err != nil {
		t.Fatalf("ensure app schema: %v", err)
	}
	provider := syntheticProvider(t, openSQL(t, tdb.DSN), syntheticHistory(false))
	if _, err := provider.Up(ctx); err == nil {
		t.Fatal("Up with a halfway failure must fail")
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatalf("GetDBVersion after failure: %v", err)
	}
	if version != 1 {
		t.Fatalf("version after halfway failure = %d, want 1 (only the good migration recorded)", version)
	}
	var partial string
	if err := work.QueryRowContext(ctx, "SELECT COALESCE(to_regclass('t02_probe_bad')::text, '')").Scan(&partial); err != nil {
		t.Fatalf("probe partial table: %v", err)
	}
	if partial != "" {
		t.Fatalf("partial table t02_probe_bad survived its failed migration: %q", partial)
	}

	fixed := syntheticProvider(t, openSQL(t, tdb.DSN), syntheticHistory(true))
	results, err := fixed.Up(ctx)
	if err != nil {
		t.Fatalf("retry with fixed history: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("retry applied = %d, want exactly the fixed migration", len(results))
	}
	version, err = fixed.GetDBVersion(ctx)
	if err != nil {
		t.Fatalf("GetDBVersion after retry: %v", err)
	}
	if version != 2 {
		t.Fatalf("version after retry = %d, want 2", version)
	}
}

// contentionStates are the SQLSTATEs a contended upgrade may abort with.
// Concurrent migrators on one database race on shared catalog rows (the
// namespace index in ensureSchema, pg_authid in the role model, version
// rows in goose): PostgreSQL aborts the loser, each migration stays atomic,
// and retry converges. This is the same abort-and-retry policy 00003
// documents for concurrent CREATE ROLE — never silent partial state.
var contentionStates = []string{"23505", "40P01", "55P03"}

// isContentionError reports whether an upgrade error is a benign
// concurrency abort (contention on shared catalog state) rather than a
// genuine migration failure.
func isContentionError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, state := range contentionStates {
		if strings.Contains(message, state) {
			return true
		}
	}
	return strings.Contains(message, "tuple concurrently updated")
}

// TestMigrationMatrixConcurrentUpIsSafe proves concurrent runners cannot
// corrupt the version history: losers abort with clean contention errors
// (never partial versions), retry converges to the head, and a final Up is
// a no-op.
func TestMigrationMatrixConcurrentUpIsSafe(t *testing.T) {
	tdb := dbtest.New(t, dbtest.WithoutMigrations(), dbtest.WithBaseDSN(matrixBaseDSN()))
	ctx, cancel := matrixContext(120 * time.Second)
	defer cancel()

	versions := mustVersions(t)
	type outcome struct {
		applied int
		err     error
	}
	outcomes := make([]outcome, 4)
	var wg sync.WaitGroup
	for i := range outcomes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := sql.Open(dbmigrate.DriverName, tdb.DSN)
			if err != nil {
				outcomes[i] = outcome{err: err}
				return
			}
			defer db.Close()
			applied, err := dbmigrate.Up(ctx, db, io.Discard)
			outcomes[i] = outcome{applied: applied, err: err}
		}(i)
	}
	wg.Wait()
	for i, result := range outcomes {
		if result.err != nil && !isContentionError(result.err) {
			t.Fatalf("worker %d: %v (only catalog contention may abort a raced upgrade)", i, result.err)
		}
	}

	// Retry converges: every version is recorded exactly once across all
	// attempts, so the converged state is deterministic even though the
	// interleaving is not.
	check := tdb.SQLDB(t)
	deadline := time.Now().Add(60 * time.Second)
	for {
		applied, err := dbmigrate.Up(ctx, openSQL(t, tdb.DSN), io.Discard)
		if err != nil {
			if isContentionError(err) && time.Now().Before(deadline) {
				continue
			}
			t.Fatalf("converging Up: %v", err)
		}
		if applied == 0 {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("upgrade did not converge before the deadline (livelock)")
		}
	}
	if version := mustCurrentVersion(t, ctx, check); version != versions[len(versions)-1] {
		t.Fatalf("version after concurrent upgrade = %d, want head %d", version, versions[len(versions)-1])
	}
	rows, err := dbmigrate.Status(ctx, openSQL(t, tdb.DSN))
	if err != nil {
		t.Fatalf("Status after convergence: %v", err)
	}
	for _, row := range rows {
		if row.State != goose.StateApplied {
			t.Fatalf("status of %d after convergence = %s, want applied", row.Version, row.State)
		}
	}
}

// exportedAPI lists every exported symbol declared by the non-test Go
// sources of the calling package directory.
func exportedAPI(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := map[string]bool{}
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		collectFileDecls(names, file)
	}
	api := make([]string, 0, len(names))
	for name := range names {
		api = append(api, name)
	}
	sort.Strings(api)
	return api
}

func collectFileDecls(names map[string]bool, file *ast.File) {
	for _, decl := range file.Decls {
		switch node := decl.(type) {
		case *ast.FuncDecl:
			if node.Name.IsExported() {
				names[node.Name.Name] = true
			}
		case *ast.GenDecl:
			collectGenSpecs(names, node)
		}
	}
}

func collectGenSpecs(names map[string]bool, decl *ast.GenDecl) {
	for _, spec := range decl.Specs {
		switch specNode := spec.(type) {
		case *ast.TypeSpec:
			if specNode.Name.IsExported() {
				names[specNode.Name.Name] = true
			}
		case *ast.ValueSpec:
			for _, ident := range specNode.Names {
				if ident.IsExported() {
					names[ident.Name] = true
				}
			}
		}
	}
}

// TestMigrationMatrixForbidsAutomatedDowngrade locks the runner's public
// surface: the only operations are forward (Up, UpTo), observation (Status,
// CurrentVersion, Versions) and construction (NewProvider). A destructive
// downgrade can only arrive as an explicit, reviewed API addition.
func TestMigrationMatrixForbidsAutomatedDowngrade(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller cannot locate this test")
	}
	want := []string{"CurrentVersion", "DriverName", "NewProvider", "Status", "StatusRow", "String", "Up", "UpTo", "VersionTable", "Versions"}
	if got := exportedAPI(t, filepath.Dir(file)); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("exported API = %q, want %q: a new entry point (notably any downgrade) needs an explicit task", got, want)
	}
}

// TestMigrationMatrixRequiresExpandContract scans every Up section for
// data-destroying statements. Forward history may only add (tables,
// columns, constraints, indexes, grants, comments, DO blocks) or swap a
// CHECK by DROP+ADD; DROP TABLE/COLUMN, DELETE, TRUNCATE would break the
// N−1 application during the coexistence window and are refused here.
func TestMigrationMatrixRequiresExpandContract(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller cannot locate this test")
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(file), "migrations"))
	if err != nil {
		t.Fatalf("ReadDir(migrations): %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no migration files found (anti-vacuity guard)")
	}
	destructive := regexp.MustCompile(`(?i)\bDROP\s+TABLE\b|\bDROP\s+COLUMN\b|\bDELETE\s+FROM\b|\bTRUNCATE\b`)
	for _, entry := range entries {
		name := entry.Name()
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "migrations", name))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		up := string(raw)
		if index := strings.Index(up, "-- +goose Up"); index >= 0 {
			up = up[index:]
		}
		if index := strings.Index(up, "-- +goose Down"); index >= 0 {
			up = up[:index]
		}
		code := []string{}
		for _, line := range strings.Split(up, "\n") {
			if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "--") {
				code = append(code, line)
			}
		}
		if match := destructive.FindString(strings.Join(code, "\n")); match != "" {
			t.Errorf("migration %s Up section destroys data (%q): expand/contract only", name, match)
		}
	}
}
