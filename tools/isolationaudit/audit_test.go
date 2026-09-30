// Fixtures of the leftovers audit (P22-T06).
//
// Every rule the audit can report is exercised here with the two snapshots that
// produce it, and every refusal of the state file is exercised too: the
// comparison is the whole logic of the tool, and a rule nobody has watched fire
// is a rule nobody knows works. Nothing in this file needs PostgreSQL or a
// temporary directory — the audit takes its photographs elsewhere, and what is
// measured here is what it does with them.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// snapshot builds the two moments a fixture compares, with the fields it cares
// about and nothing else.
func snapshot(databases []string, connections map[string]int, temporary []string) Snapshot {
	if databases == nil {
		databases = []string{}
	}
	if connections == nil {
		connections = map[string]int{}
	}
	if temporary == nil {
		temporary = []string{}
	}
	return Snapshot{
		Databases:   databases,
		Connections: connections,
		Temporary:   temporary,
		TakenAt:     time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

// TestTheAuditStaysQuietWhenTheRunLeftNothing is the control: the same machine
// before and after, and a run that dropped more than it created, are both
// clean. Without it, "the audit reported a leftover" could be the answer of an
// audit that reports everything.
func TestTheAuditStaysQuietWhenTheRunLeftNothing(t *testing.T) {
	t.Parallel()

	stable := snapshot(
		[]string{"arena_test_identity_1"},
		map[string]int{"arena_test_identity_1": 0},
		nil,
	)
	if violations := Compare(stable, stable); len(violations) != 0 {
		t.Fatalf("an unchanged machine was reported as %v", violations)
	}

	// A database that disappeared is the harness doing its job, not a finding.
	before := snapshot([]string{"arena_test_identity_1", "arena_test_identity_2"}, map[string]int{"arena_test_identity_1": 1}, nil)
	after := snapshot([]string{"arena_test_identity_1"}, map[string]int{"arena_test_identity_1": 0}, nil)
	if violations := Compare(before, after); len(violations) != 0 {
		t.Fatalf("a run that cleaned up after itself was reported as %v", violations)
	}
}

// TestEachLeftoverIsReportedByItsOwnRule walks the rules: each fixture must
// produce the rule it exists for and nothing else.
func TestEachLeftoverIsReportedByItsOwnRule(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		rule   string
		before Snapshot
		after  Snapshot
	}{
		{
			rule:   RuleLeftoverDatabase,
			before: snapshot([]string{"arena_test_identity_1"}, nil, nil),
			after:  snapshot([]string{"arena_test_identity_1", "arena_test_identity_2"}, nil, nil),
		},
		{
			rule:   RuleLeftoverConnection,
			before: snapshot([]string{"arena_test_identity_1"}, map[string]int{"arena_test_identity_1": 0}, nil),
			after:  snapshot([]string{"arena_test_identity_1"}, map[string]int{"arena_test_identity_1": 5}, nil),
		},
		{
			rule:   RuleLeftoverTemporary,
			before: snapshot(nil, nil, nil),
			after:  snapshot(nil, nil, []string{filepath.Join(os.TempDir(), "testguard-1234567")}),
		},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.rule, func(t *testing.T) {
			violations := Compare(fixture.before, fixture.after)
			if len(violations) == 0 {
				t.Fatalf("the fixture produced no finding, want %s", fixture.rule)
			}
			for _, violation := range violations {
				if violation.Rule != fixture.rule {
					t.Fatalf("the fixture reported %s as well, which is another fixture's finding", violation.Rule)
				}
				if violation.Detail == "" {
					t.Fatalf("the finding %s says nothing a reader can act on", violation.Rule)
				}
			}
		})
	}
}

// TestTheAuditReportsEveryLeftoverInAFixedOrder holds the determinism of the
// report: the same two snapshots produce the same lines in the same order, so
// that a run can be compared with the one before it.
func TestTheAuditReportsEveryLeftoverInAFixedOrder(t *testing.T) {
	t.Parallel()

	before := snapshot([]string{"arena_test_a"}, map[string]int{"arena_test_a": 0}, nil)
	after := snapshot(
		[]string{"arena_test_a", "arena_test_b", "arena_test_c"},
		map[string]int{"arena_test_a": 2},
		[]string{"/tmp/testguard-1", "/tmp/testguard-2"},
	)

	first := Compare(before, after)
	second := Compare(before, after)
	// One connection finding, two databases and two directories: the fixture is
	// built so that every rule appears at least once.
	if len(first) != 5 {
		t.Fatalf("the comparison reported %d findings, want 5: %v", len(first), first)
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("the report is not deterministic: %v then %v", first, second)
		}
	}
}

// TestTheStateFileCannotNameSomethingTheAuditMustNotRemove is the safety rule
// of the tool: a state file is an input, and an input that names the product's
// database — or a directory of somebody's home — must be refused instead of
// dropped.
func TestTheStateFileCannotNameSomethingTheAuditMustNotRemove(t *testing.T) {
	t.Parallel()

	safe := Leftovers{
		Directories: []string{filepath.Join(os.TempDir(), "testguard-1234567")},
		Databases:   []string{"arena_test_leak_fixture_probe"},
	}
	if refusals := safe.Refusals(); len(refusals) != 0 {
		t.Fatalf("a state file of the harness was refused: %v", refusals)
	}

	unsafe := Leftovers{
		Directories: []string{"/home/somebody/important"},
		Databases:   []string{"arena", "postgres"},
	}
	refusals := unsafe.Refusals()
	if len(refusals) != 3 {
		t.Fatalf("the state file was refused %d times, want three: %v", len(refusals), refusals)
	}
	for _, refusal := range refusals {
		if !strings.Contains(refusal, "refused") && !strings.Contains(refusal, "is not") {
			t.Fatalf("the refusal does not say why: %s", refusal)
		}
	}
}

// TestTheCommandRefusesASnapshotItCannotRead holds the last rule: an unreadable
// or malformed snapshot is a usage error and never an empty measurement. An
// empty baseline would turn a clean run into a violation of everything, and an
// empty result into silence about a leak.
func TestTheCommandRefusesASnapshotItCannotRead(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	missing := filepath.Join(directory, "absent.json")
	malformed := filepath.Join(directory, "malformed.json")
	if err := os.WriteFile(malformed, []byte("not a snapshot"), 0o600); err != nil {
		t.Fatalf("write the malformed snapshot: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"compare", "-before", missing, "-after", missing}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("compare answered %d for a missing snapshot, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "read") {
		t.Fatalf("the refusal does not name the file: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"compare", "-before", malformed, "-after", malformed}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("compare answered %d for a malformed snapshot, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "is not a snapshot") {
		t.Fatalf("the refusal does not say what is wrong: %s", stderr.String())
	}

	// The positive control: two sound snapshots of an unchanged machine answer
	// zero and say so.
	sound := filepath.Join(directory, "sound.json")
	if err := os.WriteFile(sound, []byte(`{"databases":[],"connections":{},"temporary_directories":[]}`), 0o600); err != nil {
		t.Fatalf("write the sound snapshot: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"compare", "-before", sound, "-after", sound}, &stdout, &stderr); code != exitOK {
		t.Fatalf("compare answered %d for two sound snapshots, want 0: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "left no database") {
		t.Fatalf("the clean answer does not say what it measured: %s", stdout.String())
	}
}

// TestTheCommandRefusesAnUnknownSubcommand keeps the tool honest about its own
// vocabulary: a typo is a usage error, not a run that measured nothing.
func TestTheCommandRefusesAnUnknownSubcommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"snapshotting"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("an unknown subcommand answered %d, want %d", code, exitUsage)
	}
	if code := run(nil, &stdout, &stderr); code != exitUsage {
		t.Fatalf("no subcommand answered %d, want %d", code, exitUsage)
	}
}
