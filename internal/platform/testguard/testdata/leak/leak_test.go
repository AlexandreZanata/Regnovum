// Package leak is the fixture that leaks on purpose (P22-T06).
//
// It lives under `testdata/`, which the Go toolchain excludes from `./...`, so
// it never runs with the suite it exists to protect: its first test is designed
// to fail, and the gate that drives it requires exactly that. A leak detector
// nobody has watched fail is a detector nobody knows works — this package is
// the falsification of `internal/platform/testguard`, and the second test is the
// falsification of the leftovers audit beside it (tools/isolationaudit), which
// is why this file also creates a directory and a database it never removes.
//
// It is driven by `make test-isolation` (`tools/isolationaudit/verify.sh`),
// which sets ISOLATION_LEAK_STATE to the file the leftovers are recorded in.
// Running it by hand is allowed and answers the same thing: `go test` on this
// package must fail, naming every leak.
package leak

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testguard"
)

// stateVariable is the file the leftovers are recorded in, so that the audit
// can remove what this fixture deliberately leaves behind. It is set by the
// gate: a fixture that invented its own path would leave the machine dirty the
// first time somebody ran it by hand.
const stateVariable = "ISOLATION_LEAK_STATE"

// leftoverDatabase is the name of the disposable database this fixture creates
// and does not drop. It uses the prefix the harness uses (dbtest) on purpose:
// the audit's question is "did a run leave a database of the harness behind",
// and a name outside the prefix would be a leftover nobody looks for.
const leftoverDatabase = "arena_test_leak_fixture_probe"

// TestThisFixtureLeaksOnPurpose is the falsification of the guard: it owns every
// kind of resource and gives none of them back, so a run of this package must
// fail with one report per rule.
func TestThisFixtureLeaksOnPurpose(t *testing.T) {
	guard := testguard.New(t, testguard.WithGrace(50*time.Millisecond))

	// A directory, a listener and a timer the fixture never releases.
	_ = guard.TempDir()
	listener, err := guard.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_ = listener
	_ = guard.Ticker(time.Hour)

	// A goroutine that never ends, and a variable of the process that the
	// fixture changes and does not put back.
	blocked := make(chan struct{})
	guard.Goroutine("the delivery worker nobody stopped", func() { <-blocked })
	if err := os.Setenv("ISOLATION_LEAK_FIXTURE", "synthetic"); err != nil {
		t.Fatalf("set the variable: %v", err)
	}

	// A process the fixture starts and never stops. It is the POSIX sleep: the
	// product ships as a Linux container and every gate of this repository is a
	// bash script, so the fixture states the platform instead of guessing.
	if err := guard.Command("the server nobody stopped", exec.Command("sleep", "30")); err != nil {
		t.Fatalf("start the process: %v", err)
	}

	guard.Release()
	close(blocked)
}

// TestTheAuditFindsWhatThisFixtureLeaves creates the two leftovers the audit
// exists to find — a directory the guard would have removed, and a disposable
// database — and records them where the gate can remove them again. It passes:
// the failure it produces is the audit's, not its own.
func TestTheAuditFindsWhatThisFixtureLeaves(t *testing.T) {
	statePath := os.Getenv(stateVariable)
	if statePath == "" {
		t.Fatalf("this fixture is driven by tools/isolationaudit/verify.sh, which names %s", stateVariable)
	}

	directory, err := os.MkdirTemp("", testguard.TempDirectoryPrefix())
	if err != nil {
		t.Fatalf("create the directory that stays: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "left.txt"), []byte("synthetic"), 0o600); err != nil {
		t.Fatalf("write inside the directory that stays: %v", err)
	}

	dsn := os.Getenv("ARENA_DATABASE_URL")
	if dsn == "" {
		dsn = dbtest.DefaultAdminDSN
	}
	admin, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		t.Fatalf("open the administrative connection: %v", err)
	}
	defer func() { _ = admin.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("the leftovers audit needs PostgreSQL: %v", err)
	}
	// Dropping first makes the fixture repeatable: the gate can be run twice
	// without the second run stumbling over the first one's leftover.
	if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+leftoverDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("clear the leftover of a previous run: %v", err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+leftoverDatabase); err != nil {
		t.Fatalf("create the database that stays: %v", err)
	}

	state := struct {
		Directories []string `json:"directories"`
		Databases   []string `json:"databases"`
	}{
		Directories: []string{directory},
		Databases:   []string{leftoverDatabase},
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode the state: %v", err)
	}
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		t.Fatalf("record the leftovers in %s: %v", statePath, err)
	}
}

// TestTheFixtureItselfIsSound is the control of this package: the wiring the
// leaking tests depend on works with a real *testing.T, so a red run of this
// package fails for the leak and never for a fixture that cannot even start.
// Everything here is released, so the guard has nothing to report and this test
// passes — which is what makes the red of the test above attributable.
func TestTheFixtureItselfIsSound(t *testing.T) {
	guard := testguard.New(t, testguard.WithGrace(50*time.Millisecond))

	directory := guard.TempDir()
	if directory == "" {
		t.Fatal("the guard handed out no directory")
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatalf("remove the directory: %v", err)
	}

	listener, err := guard.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close the listener: %v", err)
	}

	finished := make(chan struct{})
	guard.Goroutine("a worker that ends", func() { close(finished) })
	<-finished

	command := exec.Command("go", "env", "GOOS")
	if err := guard.Command("a process that ends", command); err != nil {
		t.Fatalf("start the process: %v", err)
	}

	ticker := guard.Ticker(time.Hour)
	ticker.Stop()
	timer := guard.AfterFunc(time.Hour, func() {})
	timer.Stop()

	guard.Setenv("ISOLATION_LEAK_FIXTURE_SOUND", "synthetic")
}
