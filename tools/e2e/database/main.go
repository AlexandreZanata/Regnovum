// Command e2e-database provisions and removes the disposable PostgreSQL
// database the browser journeys run against (P18-T07).
//
// Why it exists: `make test-e2e` drives the real binary against real
// PostgreSQL, and a journey that ran inside the shared development database
// would leave state behind, conflict with the next run and make a failure
// depend on whatever was there before. The integration harness of the Go tests
// (`internal/platform/dbtest`) answers the same question, but it is bound to
// `testing.TB` and cannot be called from a shell script; this command is that
// same discipline — a pristine, migrated, disposable database per run — at the
// edge where the harness can use it.
//
// It is test tooling: never part of the delivered application, never referenced
// by `web/` and never imported by `internal/`. The name it is allowed to touch
// is reserved by prefix, so a call cannot drop a database this tooling did not
// create.
//
// Usage:
//
//	e2e-database create --dsn <admin-dsn> --name <database>
//	e2e-database drop   --dsn <admin-dsn> --name <database>
//
// Both commands read the administrative DSN from --dsn (the database named in
// it is only the door: the new database is created beside it, never inside it)
// and print a JSON document describing what they did.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
)

// DatabasePrefix is the prefix every database this tooling may create or drop
// carries. It is the whole safety rule: a name without it is refused, so a
// mistyped --name cannot reach a database that holds anything.
const DatabasePrefix = "arena_e2e_"

// identifier matches a PostgreSQL identifier this tool is willing to
// interpolate: lowercase ASCII, digits and underscore, starting with a letter
// and short enough for the 63-byte limit. Anything else is refused instead of
// quoted, because a name that needs quoting is a name this tooling never
// generates.
var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,45}$`)

const usage = `e2e-database provisions the disposable database the browser journeys run against.

Usage:

  e2e-database create --dsn <admin-dsn> --name <database>
  e2e-database drop   --dsn <admin-dsn> --name <database>

create removes a database of the same name when it exists, creates it empty and
applies every forward migration, so a run always starts from the schema the
migrations describe, never from the leftovers of the previous one.

drop removes it.

The database name must start with "` + DatabasePrefix + `", which is the only
prefix this tool will touch. The administrative DSN names the database the
commands connect through; it is never the database they create or drop.`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "e2e-database:", err)
		os.Exit(1)
	}
}

// errUsage is the sentinel of a bad invocation: the usage text is printed on the
// output, and the error says what is missing without carrying the whole manual.
var errUsage = errors.New("invalid invocation")

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return fmt.Errorf("%w: a subcommand is required", errUsage)
	}

	switch args[0] {
	case "create":
		return runCreate(args[1:], stdout)
	case "drop":
		return runDrop(args[1:], stdout)
	case "help", "-h", "-help", "--help":
		fmt.Fprintln(stdout, usage)
		return nil
	default:
		fmt.Fprint(stdout, usage)
		return fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
	}
}

// target is what every subcommand needs: the door it connects through, the
// database it acts on, and the connection string of that database once it
// exists.
type target struct {
	adminDSN string
	name     string
	dsn      string
}

// parse reads and validates the arguments. Both flags are required, and the
// name is checked against the reserved prefix and the identifier rule before
// anything connects: a refusal that happens before the connection cannot drop
// anything.
func parse(subcommand string, args []string) (target, error) {
	flags := flag.NewFlagSet(subcommand, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dsn := flags.String("dsn", "", "administrative DSN the command connects through")
	name := flags.String("name", "", "database to create or drop")
	if err := flags.Parse(args); err != nil {
		return target{}, fmt.Errorf("%w\n\n%s", err, usage)
	}
	if flags.NArg() > 0 {
		return target{}, fmt.Errorf("%s takes no positional arguments (got %q)", subcommand, flags.Args())
	}
	if strings.TrimSpace(*dsn) == "" {
		return target{}, errors.New("--dsn is required")
	}
	if !strings.HasPrefix(*dsn, "postgres://") && !strings.HasPrefix(*dsn, "postgresql://") {
		return target{}, errors.New("--dsn must be a postgres:// or postgresql:// DSN")
	}
	if !identifier.MatchString(*name) {
		return target{}, fmt.Errorf("--name %q must match %s", *name, identifier)
	}
	if !strings.HasPrefix(*name, DatabasePrefix) {
		return target{}, fmt.Errorf("--name %q must start with %q", *name, DatabasePrefix)
	}

	// The connection string of the new database is the administrative one
	// with its database replaced, so the credentials, the host and the
	// options of the caller are preserved instead of reassembled here.
	parsed, err := url.Parse(*dsn)
	if err != nil {
		return target{}, fmt.Errorf("--dsn is not a valid URL: %w", err)
	}
	parsed.Path = "/" + *name

	return target{adminDSN: *dsn, name: *name, dsn: parsed.String()}, nil
}

// runCreate makes the database pristine: it removes the previous one of the
// same name, creates it and applies the forward migrations that ship inside
// this binary, so the schema of a run is exactly the history the repository
// declares.
func runCreate(args []string, stdout io.Writer) error {
	request, err := parse("create", args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := dropDatabase(ctx, request); err != nil {
		return err
	}

	admin, err := open(request.adminDSN)
	if err != nil {
		return err
	}
	defer admin.Close()

	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+request.name); err != nil {
		return fmt.Errorf("create database %s: %w", request.name, err)
	}

	created, err := open(request.dsn)
	if err != nil {
		return err
	}
	defer created.Close()

	// The migration runner writes its progress where it is told; it is told
	// to write nothing, because the JSON document below is what the harness
	// reads, and a log line between two documents is a parsing failure.
	migrations, err := dbmigrate.Up(ctx, created, io.Discard)
	if err != nil {
		return fmt.Errorf("migrate database %s: %w", request.name, err)
	}

	return writeDocument(stdout, map[string]any{
		"command":    "create",
		"database":   request.name,
		"dsn":        request.dsn,
		"migrations": migrations,
	})
}

// runDrop removes the database and reports whether it was there.
func runDrop(args []string, stdout io.Writer) error {
	request, err := parse("drop", args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := dropDatabase(ctx, request); err != nil {
		return err
	}
	return writeDocument(stdout, map[string]any{
		"command":  "drop",
		"database": request.name,
	})
}

// dropDatabase removes one database through the administrative connection.
// WITH (FORCE) closes the connections the server of the run still holds, which
// is what makes the teardown of a crashed run possible at all.
func dropDatabase(ctx context.Context, request target) error {
	admin, err := open(request.adminDSN)
	if err != nil {
		return err
	}
	defer admin.Close()

	if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+request.name+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop database %s: %w", request.name, err)
	}
	return nil
}

// open connects with the pinned driver, through database/sql, because that is
// the handle the migration runner takes.
func open(dsn string) (*sql.DB, error) {
	connection, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dsn, err)
	}
	return connection, nil
}

func writeDocument(stdout io.Writer, document map[string]any) error {
	encoded, err := json.Marshal(document)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(encoded))
	return err
}
