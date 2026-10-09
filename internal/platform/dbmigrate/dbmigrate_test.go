package dbmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func TestVersionTableAndDriverConstants(t *testing.T) {
	t.Parallel()

	if VersionTable != "app.schema_metadata" {
		t.Errorf("VersionTable = %q, want the plan's schema-qualified schema_metadata", VersionTable)
	}
	if DriverName != "pgx" {
		t.Errorf("DriverName = %q, want the pgx stdlib driver", DriverName)
	}
}

// TestMigrationSourcesAreWellFormed pins the shape every forward-only
// migration must have: ordered numeric prefix, one Up section and a Down
// section, and no extension bootstrap (extensions must be justified by an
// explicit task, none is needed on PostgreSQL 18 core).
func TestMigrationSourcesAreWellFormed(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("ReadDir(migrations) error = %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no embedded migrations found (anti-vacuity guard)")
	}

	namePattern := regexp.MustCompile(`^(\d{5})_[a-z0-9_]+\.sql$`)
	seen := map[int64]bool{}
	for _, entry := range entries {
		name := entry.Name()
		match := namePattern.FindStringSubmatch(name)
		if match == nil {
			t.Errorf("migration file %q does not match the 5-digit naming convention", name)
			continue
		}
		content, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", name, err)
		}
		text := string(content)

		upCount := strings.Count(text, "-- +goose Up")
		downCount := strings.Count(text, "-- +goose Down")
		if upCount != 1 {
			t.Errorf("migration %q must have exactly one Up section, got %d", name, upCount)
		}
		if downCount != 1 {
			t.Errorf("migration %q must have exactly one Down section, got %d", name, downCount)
		}
		if strings.Contains(strings.ToLower(text), "create extension") {
			t.Errorf("migration %q bootstrap extensions; extensions require an explicit plan task and ADR-level justification", name)
		}
		if !strings.Contains(text, "goose") && strings.TrimSpace(text) == "" {
			t.Errorf("migration %q is empty", name)
		}

		var version int64
		if _, err := fmt.Sscanf(match[1], "%d", &version); err != nil {
			t.Errorf("migration %q version parse: %v", name, err)
			continue
		}
		if seen[version] {
			t.Errorf("migration version %d is duplicated", version)
		}
		seen[version] = true
	}
}

// offlineDB returns a *sql.DB handle for the registered pgx driver that
// never dials during sql.Open; the DSN points at a loopback port that
// refuses connections, so first use fails fast without external services.
func offlineDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(DriverName, "postgres://arena:offline@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestProviderSourcesMatchEmbeddedFiles proves the registry the runner
// feeds to goose sees exactly the embedded files, with versions derived
// from the filenames.
func TestProviderSourcesMatchEmbeddedFiles(t *testing.T) {
	t.Parallel()

	provider, err := NewProvider(offlineDB(t))
	if err != nil {
		t.Fatalf("NewProvider error = %v (construction must not need a live database)", err)
	}
	defer provider.Close()

	sources := provider.ListSources()
	if len(sources) == 0 {
		t.Fatal("provider listed zero sources (anti-vacuity guard)")
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("ReadDir(migrations) error = %v", err)
	}
	if len(sources) != len(entries) {
		t.Fatalf("provider sources = %d, embedded files = %d", len(sources), len(entries))
	}
	for index, source := range sources {
		if source.Type != goose.TypeSQL {
			t.Errorf("source %d: type = %v, want SQL", index, source.Type)
		}
		if source.Path == "" || source.Version <= 0 {
			t.Errorf("source %d: path = %q version = %d", index, source.Path, source.Version)
		}
		if !strings.HasSuffix(source.Path, entries[index].Name()) {
			t.Errorf("source %d: path %q out of order vs file %q", index, source.Path, entries[index].Name())
		}
	}
}

// TestStatusAgainstUnavailableDatabase proves the no-database path fails
// with a clean error instead of panicking or silently succeeding.
func TestStatusAgainstUnavailableDatabase(t *testing.T) {
	t.Parallel()

	db := offlineDB(t)

	if _, err := Status(context.Background(), db); err == nil {
		t.Error("Status against an unusable database must fail")
	}
	if _, err := CurrentVersion(context.Background(), db); err == nil {
		t.Error("CurrentVersion against an unusable database must fail")
	}
	if _, err := Up(context.Background(), db, nil); err == nil {
		t.Error("Up against an unusable database must fail")
	}
}

func TestStatusRowString(t *testing.T) {
	t.Parallel()

	pending := StatusRow{Version: 2, Path: "migrations/00002_comments.sql", State: goose.StatePending}
	if got := pending.String(); !strings.HasSuffix(got, "\tpending") {
		t.Errorf("pending row = %q, want a pending suffix", got)
	}
	applied := StatusRow{
		Version:   1,
		Path:      "migrations/00001_app_schema.sql",
		State:     goose.StateApplied,
		AppliedAt: time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC),
	}
	got := applied.String()
	if !strings.Contains(got, "applied") || !strings.Contains(got, "00001_app_schema.sql") {
		t.Errorf("applied row = %q, want version, name and state", got)
	}
}

func TestAccountKeyCredentialsMigration63Source(t *testing.T) {
	t.Parallel()

	content, err := fs.ReadFile(migrationsFS, "migrations/00063_account_key_credentials.sql")
	if err != nil {
		t.Fatalf("ReadFile(00063_account_key_credentials.sql) error = %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"CREATE TABLE IF NOT EXISTS app.account_key_credentials",
		"key_lookup bytea NOT NULL",
		"key_salt text NOT NULL",
		"key_hash bytea NOT NULL",
		"account_key_credentials_key_lookup_unique UNIQUE (key_lookup)",
		"DROP TABLE IF EXISTS app.account_key_credentials",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("migration 63 missing expected clause: %q", expected)
		}
	}
}
