package dbtest_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/jackc/pgx/v5/pgconn"
)

func baseDSN() string {
	if dsn := os.Getenv("ARENA_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable"
}

func newTestDB(t testing.TB, opts ...dbtest.Option) *dbtest.TestDB {
	t.Helper()
	allOpts := append([]dbtest.Option{dbtest.WithBaseDSN(baseDSN())}, opts...)
	return dbtest.New(t, allOpts...)
}

func TestConstraintEnforcement(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a table with explicit CHECK and UNIQUE constraints
	_, err := db.Exec(ctx, `
		CREATE TABLE app.constraint_check_test (
			id integer PRIMARY KEY,
			code text UNIQUE,
			amount bigint CHECK (amount > 0)
		)
	`)
	if err != nil {
		t.Fatalf("create test table: %v", err)
	}

	// 1. Positive insert
	_, err = db.Exec(ctx, "INSERT INTO app.constraint_check_test (id, code, amount) VALUES ($1, $2, $3)", 1, "CODE-A", 100)
	if err != nil {
		t.Fatalf("positive insert failed: %v", err)
	}

	// 2. CHECK constraint violation: amount <= 0
	_, err = db.Exec(ctx, "INSERT INTO app.constraint_check_test (id, code, amount) VALUES ($1, $2, $3)", 2, "CODE-B", -50)
	if err == nil {
		t.Fatal("expected CHECK constraint violation for negative amount, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected *pgconn.PgError, got %T: %v", err, err)
	}
	// PostgreSQL code 23514 = check_violation
	if pgErr.Code != "23514" {
		t.Errorf("pgErr.Code = %q, want 23514 (check_violation)", pgErr.Code)
	}

	// 3. UNIQUE constraint violation: duplicate code
	_, err = db.Exec(ctx, "INSERT INTO app.constraint_check_test (id, code, amount) VALUES ($1, $2, $3)", 3, "CODE-A", 200)
	if err == nil {
		t.Fatal("expected UNIQUE constraint violation for duplicate code, got nil")
	}
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected *pgconn.PgError, got %T: %v", err, err)
	}
	// PostgreSQL code 23505 = unique_violation
	if pgErr.Code != "23505" {
		t.Errorf("pgErr.Code = %q, want 23505 (unique_violation)", pgErr.Code)
	}
}

func TestParallelIsolationAndCleanTeardown(t *testing.T) {
	for i := 1; i <= 5; i++ {
		id := i
		t.Run(fmt.Sprintf("Worker_%d", id), func(t *testing.T) {
			t.Parallel()

			db := newTestDB(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// Create worker table and insert unique worker token
			_, err := db.Exec(ctx, "CREATE TABLE app.worker_data (worker_id integer PRIMARY KEY)")
			if err != nil {
				t.Fatalf("worker %d: create table: %v", id, err)
			}

			_, err = db.Exec(ctx, "INSERT INTO app.worker_data (worker_id) VALUES ($1)", id)
			if err != nil {
				t.Fatalf("worker %d: insert: %v", id, err)
			}

			// Verify isolation: only 1 row present with exactly this worker's ID
			var count int
			if err := db.QueryRow(ctx, "SELECT count(*) FROM app.worker_data").Scan(&count); err != nil {
				t.Fatalf("worker %d: count: %v", id, err)
			}
			if count != 1 {
				t.Errorf("worker %d: saw %d rows, want 1 (isolation breach)", id, count)
			}

			var readID int
			if err := db.QueryRow(ctx, "SELECT worker_id FROM app.worker_data").Scan(&readID); err != nil {
				t.Fatalf("worker %d: read ID: %v", id, err)
			}
			if readID != id {
				t.Errorf("worker %d: read ID = %d, want %d", id, readID, id)
			}
		})
	}
}

type failCaptureTB struct {
	testing.TB
	failed   bool
	failMsg  string
	cleanups []func()
}

func (f *failCaptureTB) Helper()      {}
func (f *failCaptureTB) Name() string { return "test_mock_driver" }
func (f *failCaptureTB) Fatalf(format string, args ...any) {
	f.failed = true
	f.failMsg = fmt.Sprintf(format, args...)
}
func (f *failCaptureTB) Cleanup(fn func()) {
	f.cleanups = append(f.cleanups, fn)
}

func TestProhibitsNonPostgresOrMockDrivers(t *testing.T) {
	t.Parallel()

	forbiddenDSNs := []string{
		"sqlite://test.db",
		"sqlite3://:memory:",
		"mock://database",
		"file::memory:?cache=shared",
		"mysql://root@localhost/db",
	}

	for _, dsn := range forbiddenDSNs {
		tb := &failCaptureTB{}
		_ = dbtest.New(tb, dbtest.WithBaseDSN(dsn))
		if !tb.failed {
			t.Errorf("dbtest.New with forbidden DSN %q should fail immediately", dsn)
		}
		if !strings.Contains(tb.failMsg, "prohibited") && !strings.Contains(tb.failMsg, "unsupported") {
			t.Errorf("dbtest error should explain prohibition, got: %s", tb.failMsg)
		}
	}
}

func TestNoSQLiteOrMockInDependencies(t *testing.T) {
	t.Parallel()

	goMod, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	content := string(goMod)
	forbiddenPatterns := []string{
		"sqlite",
		"go-sqlite3",
		"sqlmock",
		"DATA-DOG",
	}

	for _, pattern := range forbiddenPatterns {
		if strings.Contains(strings.ToLower(content), pattern) {
			t.Errorf("go.mod contains forbidden dependency %q: SQLite/mock substitutes are banned", pattern)
		}
	}
}
