// Command migrationaudit is the migration-lifecycle gate of the release
// readiness phase (P20-T03).
//
// It exercises a real PostgreSQL — never SQLite, never a mock — through the
// runner the application ships: an empty database, a ladder that applies one
// migration at a time while a reader holds the schema, a snapshot per version
// rolled forward to head, a migration that fails halfway, and the catalog rules
// the phase asks for (grants, indexes, foreign keys, cascades).
//
// It reads and measures; it never writes to the repository. The report it
// prints is the versioned evidence the phase requires.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
)

// Exit statuses, as a vocabulary the tests and the gate can name.
const (
	exitOK    = 0
	exitAudit = 1
)

// dsnEnvironment is where the gate passes the throwaway cluster's connection
// string, so the DSN is never a command-line argument that ends up in a process
// listing.
const dsnEnvironment = "ARENA_MIGRATION_AUDIT_DSN"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole command with its exit status as a value: the gate is a
// contract about exit codes, and a contract that only exists inside main is one
// no test can hold.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migrationaudit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root holding the migrations")
	dsn := flags.String("admin-dsn", os.Getenv(dsnEnvironment), "administrative PostgreSQL DSN of a throwaway cluster")
	prefix := flags.String("prefix", "arena_migaudit", "name prefix of the databases this run creates")
	report := flags.String("report", "", "write the Markdown report to this file")
	if err := flags.Parse(args); err != nil {
		return exitAudit
	}
	_ = logging.RedactValue
	if *dsn == "" {
		fmt.Fprintf(stderr, "migrationaudit: a PostgreSQL DSN is required (set %s or pass -admin-dsn)\n", dsnEnvironment)
		return exitAudit
	}

	// The prefix carries the process id, so two runs against the same cluster
	// never collide and a run that was killed leaves databases that say which
	// run they belonged to.
	options := options{
		root:   *root,
		dsn:    *dsn,
		prefix: *prefix + "_" + itoa(int64(os.Getpid())),
		log:    stderr,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	started := time.Now()
	data, err := measureWithRules(ctx, options)
	if err != nil {
		fmt.Fprintf(stderr, "migrationaudit: %s\n", logging.RedactValue(err.Error()))
		return exitAudit
	}
	data.Commit = commit(*root)
	data.Duration = time.Since(started)

	summarize(data, stdout)
	for _, advisory := range data.Advisories {
		fmt.Fprintf(stderr, "migrationaudit: advisory: %s\n", oneLine(advisory))
	}
	if len(data.Advisories) > 0 {
		fmt.Fprintf(stderr, "migrationaudit: %d advisory finding(s) — recorded in the report; they do not refuse a promotion\n", len(data.Advisories))
	}
	if *report != "" {
		file, err := os.Create(*report)
		if err != nil {
			fmt.Fprintf(stderr, "migrationaudit: write the report: %s\n", logging.RedactValue(err.Error()))
			return exitAudit
		}
		defer file.Close()
		render(data, file)
		fmt.Fprintf(stdout, "migrationaudit: report written to %s\n", *report)
	}

	for _, finding := range data.Findings {
		fmt.Fprintf(stderr, "migrationaudit: %s\n", oneLine(finding))
	}
	if len(data.Findings) > 0 {
		fmt.Fprintf(stderr, "migrationaudit: %d finding(s) — a migration history that cannot prove itself is not ready to be promoted\n", len(data.Findings))
		return exitAudit
	}
	return exitOK
}

// measureWithRules is the exercise plus the rules applied to what it measured.
// The two are separate so the rules can be stated (and tested) over a run that
// did not touch a database, and so the report can print the measurements even
// when a rule fails.
func measureWithRules(ctx context.Context, options options) (*reportData, error) {
	data, err := measure(ctx, options)
	if err != nil {
		return nil, err
	}
	applyRules(data)
	data.Findings, data.Advisories = evaluateRules(data)
	return data, nil
}

// commit is the revision the audit ran on, best effort: a report that cannot
// name the commit it measured is a report nobody can reproduce. A repository
// without git — an extracted tarball, a build directory — is not an error.
func commit(root string) string {
	command := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
