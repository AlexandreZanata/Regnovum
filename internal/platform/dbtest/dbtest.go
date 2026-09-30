// Package dbtest provides an isolated, disposable PostgreSQL integration
// test harness for Regnovum (P03-T05).
//
// Per docs/ARCHITECTURE.md and the master plan:
//   - Tests must run against real PostgreSQL;
//   - SQLite and mock databases are strictly prohibited as substitutes
//     for PostgreSQL semantics (constraints, types, concurrency);
//   - Each test receives a pristine, isolated database with forward-only
//     migrations pre-applied, and teardown is guaranteed via testing.TB.Cleanup.
package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbpool"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DefaultAdminDSN points to the dev PostgreSQL Compose service on loopback. It
// is exported so that the tooling which audits what a run leaves behind
// (tools/isolationaudit) connects to the same server this harness creates its
// disposable databases on: two literals would be two servers the day one of them
// changes.
const DefaultAdminDSN = "postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable"

var (
	nonAlphaNum = regexp.MustCompile(`[^a-zA-Z0-9]+`)
	dbSequence  uint64
)

// TestDB represents an isolated, disposable PostgreSQL database instance
// provisioned for a single test.
type TestDB struct {
	Pool   *dbpool.Pool
	DBName string
	DSN    string
}

// Option configures test database provisioning.
type Option func(*options)

type options struct {
	baseDSN          string
	applyMigrations  bool
	poolMaxConns     int32
	poolMinConns     int32
	healthCheckLimit time.Duration
}

// WithBaseDSN overrides the administrative connection string used to create
// and drop the test database.
func WithBaseDSN(dsn string) Option {
	return func(o *options) {
		o.baseDSN = dsn
	}
}

// WithoutMigrations provisions an empty database without applying migrations.
func WithoutMigrations() Option {
	return func(o *options) {
		o.applyMigrations = false
	}
}

// WithPoolLimits overrides connection limits for the returned pool.
func WithPoolLimits(maxConns, minConns int32) Option {
	return func(o *options) {
		o.poolMaxConns = maxConns
		o.poolMinConns = minConns
	}
}

// New provisions an isolated PostgreSQL database for the current test,
// applies all forward migrations, initializes a bounded connection pool,
// and registers automatic teardown with t.Cleanup.
func New(t testing.TB, opts ...Option) *TestDB {
	t.Helper()

	cfg := options{
		baseDSN:          DefaultAdminDSN,
		applyMigrations:  true,
		poolMaxConns:     5,
		poolMinConns:     1,
		healthCheckLimit: 2 * time.Second,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := validatePostgresDSN(cfg.baseDSN); err != nil {
		t.Fatalf("dbtest: %s", err.Error())
		return nil
	}

	adminURL, err := url.Parse(cfg.baseDSN)
	if err != nil {
		t.Fatalf("dbtest: invalid base DSN: %s", logging.RedactValue(err.Error()))
	}

	seq := atomic.AddUint64(&dbSequence, 1)
	dbName := generateDBName(t.Name(), seq)

	testURL := *adminURL
	testURL.Path = "/" + dbName
	testDSN := testURL.String()

	adminDB, err := sql.Open(dbmigrate.DriverName, cfg.baseDSN)
	if err != nil {
		t.Fatalf("dbtest: open admin connection: %s", logging.RedactValue(err.Error()))
	}
	defer adminDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := adminDB.PingContext(ctx); err != nil {
		t.Fatalf("dbtest: PostgreSQL is required for integration tests (start with 'docker compose up -d db'). SQLite and mocks are prohibited by the master plan. Connection failed: %s",
			logging.RedactValue(err.Error()))
	}

	quotedDB := quoteIdentifier(dbName)
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE "+quotedDB); err != nil {
		t.Fatalf("dbtest: create database %s failed: %s", dbName, logging.RedactValue(err.Error()))
	}

	t.Cleanup(func() {
		cleanupDatabase(t, cfg.baseDSN, dbName)
	})

	if cfg.applyMigrations {
		applyAllMigrations(t, testDSN, dbName)
	}

	poolCfg := dbpool.DefaultConfig()
	poolCfg.MaxConns = cfg.poolMaxConns
	poolCfg.MinConns = cfg.poolMinConns
	poolCfg.PingTimeout = cfg.healthCheckLimit

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	clock := clockseed.NewClock()

	pool, err := dbpool.New(context.Background(), testDSN, poolCfg, logger, clock)
	if err != nil {
		t.Fatalf("dbtest: initialize pool for %s: %s", dbName, logging.RedactValue(err.Error()))
	}

	t.Cleanup(func() {
		pool.Close()
	})

	return &TestDB{
		Pool:   pool,
		DBName: dbName,
		DSN:    testDSN,
	}
}

// SQLDB opens a standard library *sql.DB handle for callers needing database/sql,
// automatically closed via t.Cleanup.
func (tdb *TestDB) SQLDB(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open(dbmigrate.DriverName, tdb.DSN)
	if err != nil {
		t.Fatalf("dbtest: open sql.DB for %s: %s", tdb.DBName, logging.RedactValue(err.Error()))
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

// Exec executes a statement on the test database.
func (tdb *TestDB) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return tdb.Pool.Exec(ctx, sql, arguments...)
}

// Query executes a query returning rows from the test database.
func (tdb *TestDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return tdb.Pool.Query(ctx, sql, args...)
}

// QueryRow executes a query returning a single row from the test database.
func (tdb *TestDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return tdb.Pool.QueryRow(ctx, sql, args...)
}

func validatePostgresDSN(dsn string) error {
	lower := strings.ToLower(dsn)
	if strings.HasPrefix(lower, "sqlite") || strings.Contains(lower, "mock") || strings.Contains(lower, ":memory:") {
		return fmt.Errorf("SQLite and mock drivers are strictly prohibited by the master plan (got %q). Only real PostgreSQL is permitted", logging.RedactValue(dsn))
	}
	if !strings.HasPrefix(lower, "postgres://") && !strings.HasPrefix(lower, "postgresql://") {
		return fmt.Errorf("unsupported scheme in %s (must be postgres:// or postgresql://)", logging.RedactValue(dsn))
	}
	return nil
}

func generateDBName(testName string, seq uint64) string {
	cleaned := strings.ToLower(nonAlphaNum.ReplaceAllString(testName, "_"))
	cleaned = strings.Trim(cleaned, "_")
	if len(cleaned) > 38 {
		cleaned = cleaned[:38]
	}
	return fmt.Sprintf("arena_test_%s_%d", cleaned, seq)
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func applyAllMigrations(t testing.TB, dsn, dbName string) {
	t.Helper()
	db, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		t.Fatalf("dbtest: open migration connection for %s: %s", dbName, logging.RedactValue(err.Error()))
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := dbmigrate.Up(ctx, db, io.Discard); err != nil {
		t.Fatalf("dbtest: apply migrations to %s failed: %s", dbName, logging.RedactValue(err.Error()))
	}
}

// cleanupDatabase removes the disposable database and refuses to remove it in
// silence (P22-T06).
//
// The earlier version returned on every failure: an administrative connection
// it could not open, a termination that did not take, a DROP the server
// refused. A test whose database survived was a test that stayed green and a
// server that kept every database of every run until somebody dropped them by
// hand — and because the next run creates a *new* name, nothing ever failed.
// The teardown is now the measurement: it reports, and it answers whether the
// database is still there.
func cleanupDatabase(t testing.TB, adminDSN, dbName string) {
	t.Helper()

	adminDB, err := sql.Open(dbmigrate.DriverName, adminDSN)
	if err != nil {
		t.Errorf("dbtest: open the administrative connection to drop %s: %v", dbName, logging.RedactValue(err.Error()))
		return
	}
	defer func() {
		if err := adminDB.Close(); err != nil {
			t.Errorf("dbtest: close the administrative connection that dropped %s: %v", dbName, logging.RedactValue(err.Error()))
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Terminate active connections to allow clean DROP
	if _, err := adminDB.ExecContext(ctx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()",
		dbName,
	); err != nil {
		t.Errorf("dbtest: terminate the connections to %s before dropping it: %v", dbName, logging.RedactValue(err.Error()))
		return
	}

	// PostgreSQL 13+ supports WITH (FORCE)
	quotedDB := quoteIdentifier(dbName)
	if _, err := adminDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quotedDB+" WITH (FORCE)"); err != nil {
		t.Errorf("dbtest: drop %s: %v", dbName, logging.RedactValue(err.Error()))
		return
	}

	// The question of the teardown is not "did the statement run" but "is the
	// database gone": a DROP that answered nothing and removed nothing is the
	// failure this check exists for.
	exists, err := databaseExists(ctx, adminDB, dbName)
	if err != nil {
		t.Errorf("dbtest: ask whether %s survived its teardown: %v", dbName, logging.RedactValue(err.Error()))
		return
	}
	if exists {
		t.Errorf("dbtest: the disposable database %s survived its teardown; a run must leave no database behind", dbName)
	}
}

// databaseExists answers whether a database is still there. The catalogue is
// read rather than trusted: pg_database is the server's own answer.
func databaseExists(ctx context.Context, adminDB *sql.DB, dbName string) (bool, error) {
	var found bool
	err := adminDB.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", dbName,
	).Scan(&found)
	if err != nil {
		return false, err
	}
	return found, nil
}
