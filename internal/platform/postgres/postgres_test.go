package postgres_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
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

func TestQueriesGetHealthMetadata(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	meta, err := q.GetHealthMetadata(ctx)
	if err != nil {
		t.Fatalf("q.GetHealthMetadata() returned error: %v", err)
	}

	// Migrations have been applied by dbmigrate.Up in dbtest.New.
	if meta.VersionID < 1 {
		t.Errorf("meta.VersionID = %d, want >= 1", meta.VersionID)
	}
	if !meta.IsApplied {
		t.Errorf("meta.IsApplied = %v, want true", meta.IsApplied)
	}
	if !meta.Tstamp.Valid || meta.Tstamp.Time.IsZero() {
		t.Errorf("meta.Tstamp is invalid or zero: %+v", meta.Tstamp)
	}
}

func TestQueriesPingHealth(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	ready, err := q.PingHealth(ctx)
	if err != nil {
		t.Fatalf("q.PingHealth() returned error: %v", err)
	}
	if ready != 1 {
		t.Errorf("q.PingHealth() = %d, want 1", ready)
	}
}

func TestQueriesWithTransaction(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	tx, err := db.Pool.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qTx := q.WithTx(tx)

	meta, err := qTx.GetHealthMetadata(ctx)
	if err != nil {
		t.Fatalf("qTx.GetHealthMetadata() returned error: %v", err)
	}
	if meta.VersionID < 1 {
		t.Errorf("meta.VersionID = %d, want >= 1", meta.VersionID)
	}

	ready, err := qTx.PingHealth(ctx)
	if err != nil {
		t.Fatalf("qTx.PingHealth() returned error: %v", err)
	}
	if ready != 1 {
		t.Errorf("qTx.PingHealth() = %d, want 1", ready)
	}
}

func TestQuerierInterfaceImplementation(t *testing.T) {
	// Compile-time assertion that *postgres.Queries implements postgres.Querier
	var _ postgres.Querier = (*postgres.Queries)(nil)
}

func TestSqlcDiffDetectsNoDriftInRepository(t *testing.T) {
	sqlcBin, err := exec.LookPath("sqlc")
	if err != nil {
		// Try GOPATH/bin
		gopath := os.Getenv("GOPATH")
		if gopath == "" {
			home, _ := os.UserHomeDir()
			gopath = filepath.Join(home, "go")
		}
		candidate := filepath.Join(gopath, "bin", "sqlc")
		if _, statErr := os.Stat(candidate); statErr == nil {
			sqlcBin = candidate
		} else {
			t.Fatalf("sqlc is required to prove that the generated SQL has no drift: install it at the version the Makefile pins, or run `make generate-check` instead")
		}
	}

	// Repository root is two levels up from internal/platform/postgres
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", "..", ".."))

	cmd := exec.Command(sqlcBin, "diff")
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sqlc diff reported drift or failed (err: %v):\n%s", err, string(output))
	}
}
