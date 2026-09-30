// Command legacyinventory snapshots the legacy books (P33-T01) into a
// synthetic-safe report: aggregates by contract, origin and status, with
// orphans and ambiguous rows blocking the migration.
//
// Usage: go run ./tools/legacyinventory -dsn <postgres> [-out report.json]
//
// The database is only read, never written. Exit 0 holds a reconciled
// book, 1 lists every violated rule, 2 is a usage error. Account
// identifiers, emails and provider identifiers never enter the report.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitOK        = 0
	exitViolation = 1
	exitUsage     = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("legacyinventory", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dsn := flags.String("dsn", dbtest.DefaultAdminDSN, "PostgreSQL to snapshot (read-only; loopback dev by default)")
	out := flags.String("out", "", "report file (default stdout)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "legacyinventory takes no positional arguments")
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		fmt.Fprintf(stderr, "legacyinventory: connect: %v\n", err)
		return exitViolation
	}
	defer pool.Close()

	report, err := Audit(ctx, pool)
	if err != nil {
		fmt.Fprintf(stderr, "legacyinventory: snapshot: %v\n", err)
		return exitViolation
	}
	encoded, err := Render(report)
	if err != nil {
		fmt.Fprintf(stderr, "legacyinventory: render: %v\n", err)
		return exitViolation
	}
	if *out == "" {
		fmt.Fprint(stdout, string(encoded))
	} else if err := os.WriteFile(*out, encoded, 0o644); err != nil {
		fmt.Fprintf(stderr, "legacyinventory: write report: %v\n", err)
		return exitViolation
	}
	if len(report.Findings) == 0 {
		fmt.Fprintf(stderr, "legacyinventory: books reconcile (%d contracts)\n", len(report.Contracts))
		return exitOK
	}
	for _, finding := range report.Findings {
		fmt.Fprintf(stdout, "legacyinventory: %s: %s\n", finding.Rule, finding.Detail)
	}
	return exitViolation
}
