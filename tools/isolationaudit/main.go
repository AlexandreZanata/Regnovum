// Package main is the leftovers audit of the test platform (P22-T06).
//
// `internal/platform/testguard` refuses what one test leaves behind. This tool
// asks the other question, the one no test can ask about itself: what did the
// whole run leave behind on the machine? A disposable database the harness did
// not drop, a connection still attached to one, a temporary directory of a
// killed run — none of them is visible from inside the suite that produced
// them, and all of them accumulate silently.
//
// It is a tool and not a test because it has to outlive the process it judges:
// a snapshot before the run, a snapshot after, and a comparison between them.
// Nothing here reaches the product: it reads the server's catalogue and the
// temporary directory, and it is never part of the delivered application.
//
// Usage (driven by tools/isolationaudit/verify.sh):
//
//	isolationaudit snapshot -out before.json
//	isolationaudit compare  -before before.json -after after.json
//	isolationaudit clean    -state leak-state.json
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testguard"
)

const (
	exitOK = 0
	// exitViolation is the answer of an audit that found a leftover. It is not
	// a crash: the run is measured, not broken.
	exitViolation = 1
	// exitUsage is the answer of a command that cannot do its job.
	exitUsage = 2

	// databasePrefix is the prefix the disposable databases of the harness
	// carry (dbtest). The audit looks for this prefix and for nothing else: a
	// database of the product is not a leftover of a test run.
	databasePrefix = "arena_test_"
)

// Snapshot is what the machine held at one moment: the disposable databases,
// the connections attached to them, and the temporary directories of the guard.
type Snapshot struct {
	Databases   []string       `json:"databases"`
	Connections map[string]int `json:"connections"`
	Temporary   []string       `json:"temporary_directories"`
	TakenAt     time.Time      `json:"taken_at"`
}

// Violation is one leftover, named the way a reader acts on it.
type Violation struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

// The rules of the audit. They are the vocabulary of its failure and of the
// gate that reads it, and each one has a fixture in audit_test.go.
const (
	// RuleLeftoverDatabase is a disposable database that appeared and stayed.
	RuleLeftoverDatabase = "leftover-database"
	// RuleLeftoverConnection is a baseline database with more connections
	// attached after the run: the pool somebody forgot to close.
	RuleLeftoverConnection = "leftover-connection"
	// RuleLeftoverTemporary is a directory of the guard that appeared and
	// stayed, which is what a killed run leaves.
	RuleLeftoverTemporary = "leftover-temporary-directory"
	// RuleUnreadable is a snapshot the tool cannot compare, which is reported
	// instead of being treated as an empty one.
	RuleUnreadable = "unreadable-snapshot"
)

// Compare answers what the second snapshot has that the first did not. A
// database that disappeared between the two is not a violation: that is the
// harness doing its job.
func Compare(before, after Snapshot) []Violation {
	violations := []Violation{}
	baseline := map[string]bool{}
	for _, database := range before.Databases {
		baseline[database] = true
	}
	for _, database := range after.Databases {
		if !baseline[database] {
			violations = append(violations, Violation{
				Rule:   RuleLeftoverDatabase,
				Detail: fmt.Sprintf("the disposable database %s was left behind", database),
			})
		}
	}
	for _, database := range before.Databases {
		grew := after.Connections[database] - before.Connections[database]
		if grew > 0 {
			violations = append(violations, Violation{
				Rule:   RuleLeftoverConnection,
				Detail: fmt.Sprintf("%s has %d more connection(s) after the run than before", database, grew),
			})
		}
	}
	existed := map[string]bool{}
	for _, directory := range before.Temporary {
		existed[directory] = true
	}
	for _, directory := range after.Temporary {
		if !existed[directory] {
			violations = append(violations, Violation{
				Rule:   RuleLeftoverTemporary,
				Detail: fmt.Sprintf("the temporary directory %s was left behind", directory),
			})
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Rule != violations[j].Rule {
			return violations[i].Rule < violations[j].Rule
		}
		return violations[i].Detail < violations[j].Detail
	})
	return violations
}

// auditRules answers the rules this tool reports, in a fixed order. The guard's
// rules come from the package itself; these are declared here, and the `rules`
// subcommand prints both so that the gate iterates the vocabulary instead of
// repeating it.
func auditRules() []string {
	return []string{RuleUnreadable, RuleLeftoverDatabase, RuleLeftoverConnection, RuleLeftoverTemporary}
}

// Take measures the machine.
//
// The temporary directories are read from the directory the operating system
// hands tests, filtered by the prefix the guard owns: a directory nobody
// recognizes is not a finding this audit can act on.
func Take(ctx context.Context, admin *sql.DB, temporaryRoot string) (Snapshot, error) {
	snapshot := Snapshot{
		Databases:   []string{},
		Connections: map[string]int{},
		Temporary:   []string{},
		TakenAt:     time.Now().UTC(),
	}

	rows, err := admin.QueryContext(ctx,
		"SELECT datname FROM pg_database WHERE datname LIKE $1 ORDER BY datname", databasePrefix+"%")
	if err != nil {
		return Snapshot{}, fmt.Errorf("read the catalogue: %w", err)
	}
	err = collect(rows, func(scan func(...any) error) error {
		var name string
		if err := scan(&name); err != nil {
			return err
		}
		snapshot.Databases = append(snapshot.Databases, name)
		return nil
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("read the catalogue: %w", err)
	}

	connectionRows, err := admin.QueryContext(ctx, `
		SELECT datname, count(*) FROM pg_stat_activity
		WHERE datname LIKE $1 GROUP BY datname ORDER BY datname`, databasePrefix+"%")
	if err != nil {
		return Snapshot{}, fmt.Errorf("read the connections: %w", err)
	}
	err = collect(connectionRows, func(scan func(...any) error) error {
		var name string
		var count int
		if err := scan(&name, &count); err != nil {
			return err
		}
		snapshot.Connections[name] = count
		return nil
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("read the connections: %w", err)
	}

	entries, err := os.ReadDir(temporaryRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, fmt.Errorf("read %s: %w", temporaryRoot, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), testguard.TempDirectoryPrefix()) {
			snapshot.Temporary = append(snapshot.Temporary, filepath.Join(temporaryRoot, entry.Name()))
		}
	}
	sort.Strings(snapshot.Temporary)
	return snapshot, nil
}

// collect walks rows and closes them, so that a read that fails half way does
// not keep a connection of the audit itself attached to the server it measures.
func collect(rows *sql.Rows, each func(scan func(...any) error) error) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := each(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Leftovers are the resources a state file names for removal.
type Leftovers struct {
	Directories []string `json:"directories"`
	Databases   []string `json:"databases"`
}

// Refusals answers why a state file cannot be acted on. A name outside the
// harness prefix is refused: a state file is an input, and an input that names
// the product's database must not be able to drop it.
func (l Leftovers) Refusals() []string {
	refusals := []string{}
	for _, database := range l.Databases {
		if !strings.HasPrefix(database, databasePrefix) {
			refusals = append(refusals, fmt.Sprintf("%q is not a database of the harness (%s*)", database, databasePrefix))
		}
	}
	for _, directory := range l.Directories {
		if !strings.HasPrefix(filepath.Base(directory), testguard.TempDirectoryPrefix()) {
			refusals = append(refusals, fmt.Sprintf("%q is not a directory of the guard (%s*)", directory, testguard.TempDirectoryPrefix()))
		}
	}
	sort.Strings(refusals)
	return refusals
}

// Clean removes what a state file names: the leftovers of the fixture that
// proves the audit bites. It refuses to remove anything it does not recognize
// and it answers what it could not remove.
func Clean(ctx context.Context, admin *sql.DB, leftovers Leftovers) []string {
	failures := []string{}
	for _, directory := range leftovers.Directories {
		if err := os.RemoveAll(directory); err != nil {
			failures = append(failures, fmt.Sprintf("remove %s: %v", directory, err))
			continue
		}
		if _, err := os.Stat(directory); err == nil || !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fmt.Sprintf("the directory %s survived its removal", directory))
		}
	}
	for _, database := range leftovers.Databases {
		quoted := `"` + strings.ReplaceAll(database, `"`, `""`) + `"`
		if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)"); err != nil {
			failures = append(failures, fmt.Sprintf("drop %s: %v", database, err))
			continue
		}
		var exists bool
		if err := admin.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", database).Scan(&exists); err != nil {
			failures = append(failures, fmt.Sprintf("ask whether %s survived: %v", database, err))
			continue
		}
		if exists {
			failures = append(failures, fmt.Sprintf("the database %s survived its removal", database))
		}
	}
	sort.Strings(failures)
	return failures
}

func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

// run is the command line, separated from main so that the arguments can be
// exercised where the behaviour matters and not only in a shell.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "isolationaudit: a subcommand is required (snapshot, compare, clean)")
		return exitUsage
	}
	switch args[0] {
	case "snapshot":
		return runSnapshot(args[1:], stdout, stderr)
	case "compare":
		return runCompare(args[1:], stdout, stderr)
	case "clean":
		return runClean(args[1:], stdout, stderr)
	case "rules":
		// The declared vocabulary, printed for the gate: it asserts that the
		// fixtures produce the rules they exist for, and a list written twice —
		// once in Go and once in the script — is a list that drifts. The origin
		// is the first word, so the script can ask about one family at a time.
		for _, rule := range testguard.Rules() {
			fmt.Fprintf(stdout, "guard %s\n", rule)
		}
		for _, rule := range auditRules() {
			fmt.Fprintf(stdout, "audit %s\n", rule)
		}
		return exitOK
	default:
		fmt.Fprintf(stderr, "isolationaudit: unknown subcommand %q\n", args[0])
		return exitUsage
	}
}

func runSnapshot(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("isolationaudit snapshot", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "file the snapshot is written to (empty writes it to stdout)")
	dsn := flags.String("dsn", "", "administrative connection string (empty uses the harness default)")
	temporaryRoot := flags.String("temporary", "", "root of the temporary directories (empty uses the operating system's)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	admin, closeAdmin, err := openAdmin(*dsn)
	if err != nil {
		fmt.Fprintf(stderr, "isolationaudit: %v\n", err)
		return exitUsage
	}
	defer closeAdmin()

	root := *temporaryRoot
	if root == "" {
		root = os.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	snapshot, err := Take(ctx, admin, root)
	if err != nil {
		fmt.Fprintf(stderr, "isolationaudit: %v\n", err)
		return exitUsage
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "isolationaudit: %v\n", err)
		return exitUsage
	}
	if *out == "" {
		fmt.Fprintf(stdout, "%s\n", encoded)
		return exitOK
	}
	if err := os.WriteFile(*out, append(encoded, '\n'), 0o600); err != nil {
		fmt.Fprintf(stderr, "isolationaudit: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "isolationaudit: snapshot written to %s\n", *out)
	return exitOK
}

func runCompare(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("isolationaudit compare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	beforePath := flags.String("before", "", "snapshot taken before the run")
	afterPath := flags.String("after", "", "snapshot taken after the run")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	before, err := readSnapshot(*beforePath)
	if err != nil {
		fmt.Fprintf(stderr, "isolationaudit: %v\n", err)
		return exitUsage
	}
	after, err := readSnapshot(*afterPath)
	if err != nil {
		fmt.Fprintf(stderr, "isolationaudit: %v\n", err)
		return exitUsage
	}

	violations := Compare(before, after)
	if len(violations) == 0 {
		fmt.Fprintln(stdout, "isolationaudit: the run left no database, connection or directory behind")
		return exitOK
	}
	for _, violation := range violations {
		fmt.Fprintf(stderr, "isolationaudit: %s: %s\n", violation.Rule, violation.Detail)
	}
	return exitViolation
}

func runClean(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("isolationaudit clean", flag.ContinueOnError)
	flags.SetOutput(stderr)
	statePath := flags.String("state", "", "file naming the leftovers to remove")
	dsn := flags.String("dsn", "", "administrative connection string (empty uses the harness default)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	raw, err := os.ReadFile(*statePath)
	if err != nil {
		fmt.Fprintf(stderr, "isolationaudit: %v\n", err)
		return exitUsage
	}
	var leftovers Leftovers
	if err := json.Unmarshal(raw, &leftovers); err != nil {
		fmt.Fprintf(stderr, "isolationaudit: %s does not name leftovers: %v\n", *statePath, err)
		return exitUsage
	}
	if refusals := leftovers.Refusals(); len(refusals) != 0 {
		for _, refusal := range refusals {
			fmt.Fprintf(stderr, "isolationaudit: refused: %s\n", refusal)
		}
		return exitUsage
	}
	admin, closeAdmin, err := openAdmin(*dsn)
	if err != nil {
		fmt.Fprintf(stderr, "isolationaudit: %v\n", err)
		return exitUsage
	}
	defer closeAdmin()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if failures := Clean(ctx, admin, leftovers); len(failures) != 0 {
		for _, failure := range failures {
			fmt.Fprintf(stderr, "isolationaudit: %s\n", failure)
		}
		return exitViolation
	}
	fmt.Fprintf(stdout, "isolationaudit: removed %d director(y/ies) and %d database(s)\n", len(leftovers.Directories), len(leftovers.Databases))
	return exitOK
}

// openAdmin opens the connection the audit measures through.
func openAdmin(dsn string) (*sql.DB, func(), error) {
	if dsn == "" {
		dsn = os.Getenv("ARENA_DATABASE_URL")
	}
	if dsn == "" {
		dsn = dbtest.DefaultAdminDSN
	}
	admin, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open the administrative connection: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		return nil, func() {}, fmt.Errorf("the leftovers audit needs PostgreSQL: %w", err)
	}
	return admin, func() { _ = admin.Close() }, nil
}

// readSnapshot reads a snapshot file, and refuses an unreadable one instead of
// treating it as an empty snapshot: an empty baseline turns every measurement
// into a violation, and an empty measurement turns every leak into silence.
func readSnapshot(path string) (Snapshot, error) {
	if path == "" {
		return Snapshot{}, errors.New("a snapshot path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read %s: %w", path, err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%s is not a snapshot: %w", path, err)
	}
	if snapshot.Connections == nil {
		snapshot.Connections = map[string]int{}
	}
	return snapshot, nil
}
