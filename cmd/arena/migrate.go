package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
)

const migrateUsage = `manage the database schema of Regnovum.

Usage:

  arena migrate status    show every migration and its database state
  arena migrate up        apply all pending migrations, in order

Both commands read ARENA_* configuration from the environment
(ARENA_DATABASE_URL selects the database). Migrations are forward-only:
the up command never rewrites or reverts applied history.`

// runMigrate implements `arena migrate status|up` (P03-T02). It is a
// process edge: it loads the typed configuration, opens the database
// connection described by ARENA_DATABASE_URL and delegates to the shared
// migration runner.
func runMigrate(args []string, stdout *os.File) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		fmt.Fprintln(stdout, migrateUsage)
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("migrate requires a subcommand\n\n%s", migrateUsage)
	}

	cfg := config.MustLoad()
	dsn := string(cfg.DatabaseURL().Unredacted())
	if dsn == "" {
		return fmt.Errorf("ARENA_DATABASE_URL is required for arena migrate (set it to the PostgreSQL DSN)")
	}

	switch args[0] {
	case "status":
		return runMigrateStatus(args[1:], dsn, stdout)
	case "up":
		return runMigrateUp(args[1:], dsn, stdout)
	default:
		return fmt.Errorf("unknown migrate subcommand %q\n\n%s", args[0], migrateUsage)
	}
}

// openDatabase opens the pgx-backed database/sql connection. The
// migration runner closes nothing by itself; the subcommand owns the
// handle for the duration of the invocation.
func openDatabase(dsn string) (*sql.DB, error) {
	db, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

func runMigrateStatus(args []string, dsn string, stdout *os.File) error {
	if len(args) > 0 {
		return fmt.Errorf("migrate status takes no arguments (got %q)", args)
	}

	db, err := openDatabase(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := dbmigrate.Status(context.Background(), db)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, "migration\tfile\tstate\tapplied at")
	for _, row := range rows {
		fmt.Fprintln(stdout, row.String())
	}
	return nil
}

func runMigrateUp(args []string, dsn string, stdout *os.File) error {
	if len(args) > 0 {
		return fmt.Errorf("migrate up takes no arguments (got %q)", args)
	}

	db, err := openDatabase(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	applied, err := dbmigrate.Up(context.Background(), db, stdout)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, strconv.Itoa(applied)+" migration(s) applied")
	return nil
}
