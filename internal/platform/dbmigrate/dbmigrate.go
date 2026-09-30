// Package dbmigrate runs the forward-only PostgreSQL migrations of Goyim
// Arena (P03-T02). The SQL sources are embedded in the binary, applied by
// the pinned goose library through database/sql, and tracked in the
// schema-qualified app.schema_metadata version table.
//
// The runner is the only place in the codebase that owns the migrations:
// the migrate subcommand and the integration test harness (P03-T05) both
// go through it, so the database always evolves through one audited path.
package dbmigrate

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	// pgx/v5 is the approved PostgreSQL dependency (master plan); its
	// stdlib package registers the database/sql driver used here. The
	// dedicated pgx adapter (pool, timeouts) arrives with P03-T04.
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

// migrationsFS embeds the forward-only migration sources. New migrations
// are added as plain files under internal/platform/dbmigrate/migrations
// and ship inside the binary.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// DriverName is the database/sql driver registered by pgx stdlib.
const DriverName = "pgx"

// VersionTable is the schema-qualified goose version table (the plan's
// schema_metadata table). goose interpolates the name verbatim into its
// version-table SQL, so the schema qualification is preserved everywhere.
const VersionTable = "app.schema_metadata"

// providerDialect must be DialectCustom whenever a custom store is given:
// goose derives the actual SQL dialect from the store itself (here, the
// postgres store built in NewProvider).
const providerDialect = goose.DialectCustom

// NewProvider builds a goose migration provider for the given database.
// The caller owns db and stays responsible for closing it; the returned
// provider must be closed with Provider.Close.
func NewProvider(db *sql.DB) (*goose.Provider, error) {
	store, err := database.NewStore(goose.DialectPostgres, VersionTable)
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: build version-table store: %w", err)
	}
	// goose collects *.sql at the root of the filesystem it is given, so
	// hand it the migrations sub-filesystem, not the package root.
	sqls, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: open embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(providerDialect, db, sqls,
		goose.WithStore(store),
		goose.WithVerbose(false),
	)
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: build migration provider: %w", err)
	}
	return provider, nil
}

// StatusRow is one line of `arena migrate status`.
type StatusRow struct {
	Version   int64
	Path      string
	State     goose.State
	AppliedAt time.Time
}

// String renders the row the way the CLI prints it.
func (row StatusRow) String() string {
	name := row.Path
	if index := strings.LastIndexByte(name, '/'); index >= 0 {
		name = name[index+1:]
	}
	if row.State == goose.StateApplied && !row.AppliedAt.IsZero() {
		return fmt.Sprintf("%d\t%s\t%s\t%s", row.Version, name, row.State, row.AppliedAt.Format(time.RFC3339))
	}
	return fmt.Sprintf("%d\t%s\t%s", row.Version, name, row.State)
}

// schemaName is the application schema the version table lives in. goose
// auto-creates the version table itself, but never its parent schema; the
// runner therefore ensures the empty namespace container exists before
// any operation, so a virgin database works end to end. Migration 00001
// keeps the same creation in the forward history, idempotently.
const schemaName = "app"

func ensureSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+schemaName); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Lost the race with a concurrent migrator creating the same
			// schema: CREATE SCHEMA IF NOT EXISTS is check-then-act, so two
			// runners starting together can collide on the namespace index.
			// The schema exists, which is all the runner needs (the same
			// tolerance 00003 documents for concurrent CREATE ROLE).
			return nil
		}
		return fmt.Errorf("dbmigrate: ensure %s schema: %w", schemaName, err)
	}
	return nil
}

// Status lists every known migration with its database state. An empty
// database yields all pending rows; nothing is applied by asking for
// status.
func Status(ctx context.Context, db *sql.DB) ([]StatusRow, error) {
	if err := ensureSchema(ctx, db); err != nil {
		return nil, err
	}
	provider, err := NewProvider(db)
	if err != nil {
		return nil, err
	}
	defer provider.Close()

	statuses, err := provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: status: %w", err)
	}
	rows := make([]StatusRow, 0, len(statuses))
	for _, status := range statuses {
		row := StatusRow{State: status.State}
		if status.Source != nil {
			row.Version = status.Source.Version
			row.Path = status.Source.Path
		}
		if !status.AppliedAt.IsZero() {
			row.AppliedAt = status.AppliedAt
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Versions lists the forward migration versions embedded in the binary, in
// ascending order. Reading the history is the runner's question, not the
// caller's: an operator or an audit that walked the directory itself could
// describe a migration the binary never applies, and the two would only
// disagree in production.
func Versions() ([]int64, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: list embedded migrations: %w", err)
	}
	versions := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		underscore := strings.IndexByte(entry.Name(), '_')
		if underscore <= 0 {
			return nil, fmt.Errorf("dbmigrate: %s carries no version", entry.Name())
		}
		version, err := strconv.ParseInt(entry.Name()[:underscore], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("dbmigrate: %s: version is not a number: %w", entry.Name(), err)
		}
		versions = append(versions, version)
	}
	if len(versions) == 0 {
		return nil, errors.New("dbmigrate: the embedded history is empty")
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	for i := 1; i < len(versions); i++ {
		if versions[i] == versions[i-1] {
			return nil, fmt.Errorf("dbmigrate: version %d appears twice", versions[i])
		}
	}
	return versions, nil
}

// Up applies all pending migrations in order and reports how many were
// newly applied. Re-running with nothing pending is a no-op (returns 0).
// A failed migration is rolled back and never recorded as applied.
func Up(ctx context.Context, db *sql.DB, logWriter io.Writer) (int, error) {
	return up(ctx, db, logWriter, func(provider *goose.Provider) ([]*goose.MigrationResult, error) {
		return provider.Up(ctx)
	})
}

// UpTo applies every pending migration up to and including version and reports
// how many were newly applied. It exists so that a database can be advanced to
// a chosen point of the history through the same audited path as Up — the
// migration audit builds its snapshots with it, and an operator restoring a
// snapshot rolls forward with it — instead of running migration files by hand.
func UpTo(ctx context.Context, db *sql.DB, version int64, logWriter io.Writer) (int, error) {
	versions, err := Versions()
	if err != nil {
		return 0, err
	}
	known := false
	for _, candidate := range versions {
		if candidate == version {
			known = true
			break
		}
	}
	if !known {
		// A version the history does not carry would otherwise stop at
		// whatever precedes it, which reads like success.
		return 0, fmt.Errorf("dbmigrate: %d is not a version of the embedded history", version)
	}
	return up(ctx, db, logWriter, func(provider *goose.Provider) ([]*goose.MigrationResult, error) {
		return provider.UpTo(ctx, version)
	})
}

// up runs one forward operation through the provider and reports it the way
// the operator sees it: one line per migration, with what it cost.
func up(ctx context.Context, db *sql.DB, logWriter io.Writer, apply func(*goose.Provider) ([]*goose.MigrationResult, error)) (int, error) {
	if logWriter == nil {
		logWriter = io.Discard
	}
	if err := ensureSchema(ctx, db); err != nil {
		return 0, err
	}
	provider, err := NewProvider(db)
	if err != nil {
		return 0, err
	}
	defer provider.Close()

	results, err := apply(provider)
	applied := 0
	for _, result := range results {
		if result.Error != nil {
			fmt.Fprintf(logWriter, "dbmigrate: migration %d (%s) failed: %v\n",
				result.Source.Version, result.Source.Path, result.Error)
			continue
		}
		applied++
		fmt.Fprintf(logWriter, "dbmigrate: applied %d\t%s\t(%s)\n",
			result.Source.Version, result.Source.Path, result.Duration)
	}
	if err != nil {
		return applied, fmt.Errorf("dbmigrate: up: %w", err)
	}
	return applied, nil
}

// CurrentVersion reports the latest applied migration version, or 0 on a
// fresh database.
func CurrentVersion(ctx context.Context, db *sql.DB) (int64, error) {
	if err := ensureSchema(ctx, db); err != nil {
		return 0, err
	}
	provider, err := NewProvider(db)
	if err != nil {
		return 0, err
	}
	defer provider.Close()

	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		if errors.Is(err, goose.ErrVersionNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("dbmigrate: current version: %w", err)
	}
	return version, nil
}
