package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRegister is a minimal valid register: one measured package meeting
// every risk floor, one deferred package below it, and the phase thresholds.
const fixtureRegister = `{
  "schema_version": 1,
  "thresholds": {"Q0": 95, "Q1": 90, "Q2": 80, "global": 85, "diff": 95},
  "targets": [
    {"package": "internal/alpha/domain", "risk": ["Q0", "Q1", "Q2"], "floor": 95.0, "status": "measured"},
    {"package": "internal/alpha/application", "risk": ["Q0"], "floor": 80.0, "status": "deferred", "reason": "needs a budget", "gate": "P45"}
  ],
  "allowlist": []
}`

// fixtureCatalog backs the fixture risks: alpha carries Q0, Q1 and Q2.
const fixtureCatalog = `{"rules": [{"modules": ["internal/alpha"], "risk": "Q0"}, {"modules": ["internal/alpha"], "risk": "Q1"}, {"modules": ["internal/alpha"], "risk": "Q2"}]}`

// fixtureProfile is a two-block coverprofile: one covered statement, one
// uncovered, for a measured percent of 50%.
const fixtureProfile = `mode: set
github.com/AlexandreZanata/Regnovum/internal/alpha/domain/allow.go:10.2,12.1 1 1
github.com/AlexandreZanata/Regnovum/internal/alpha/domain/allow.go:14.2,16.1 1 0
`

// fullProfile is a single covered block: 100% line coverage.
const fullProfile = `mode: set
github.com/AlexandreZanata/Regnovum/internal/alpha/domain/allow.go:10.2,16.1 2 1
`

// writeTree lays a root with a register, a catalog and Go sources for the
// listed packages, the way the real tree is laid out.
func writeTree(t *testing.T, register, catalog string, packages []string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "quality"), 0o755); err != nil {
		t.Fatalf("prepare quality: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, RegisterPath), []byte(register), 0o644); err != nil {
		t.Fatalf("prepare register: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, CatalogPath), []byte(catalog), 0o644); err != nil {
		t.Fatalf("prepare catalog: %v", err)
	}
	for _, pkg := range packages {
		dir := filepath.Join(root, pkg)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("prepare package: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "allow.go"), []byte("package domain\n"), 0o644); err != nil {
			t.Fatalf("prepare source: %v", err)
		}
	}
	return root
}

// fakeCoverRunner replays one profile body for every package: the judgment
// is proved without running the suite.
func fakeCoverRunner(profile string) packageRunner {
	return func(_ string, path string) (string, error) {
		if err := os.WriteFile(path, []byte(profile), 0o644); err != nil {
			return "", err
		}
		return "ok", nil
	}
}

// fakeGitEmpty replays an empty diff: no added lines, so the diff ratchet
// passes vacuously and the test isolates the floor judgment.
func fakeGitEmpty(args ...string) (string, error) {
	if strings.HasPrefix(strings.Join(args, " "), "merge-base") {
		return "abc123\n", nil
	}
	return "", nil
}

// fakeGitUncovered replays one added statement line that the fixture
// profile leaves uncovered (line 15 sits in the second, uncovered block).
func fakeGitUncovered(args ...string) (string, error) {
	joined := strings.Join(args, " ")
	if strings.HasPrefix(joined, "merge-base") {
		return "abc123\n", nil
	}
	if strings.Contains(joined, "--name-only") {
		return "internal/alpha/domain/allow.go\n", nil
	}
	return "diff --git a/internal/alpha/domain/allow.go\n@@ -0,0 +15,1 @@\n+uncovered\n", nil
}

// fakeGitCovered replays one added statement line that the fixture profile
// covers (line 11 sits in the first, covered block).
func fakeGitCovered(args ...string) (string, error) {
	joined := strings.Join(args, " ")
	if strings.HasPrefix(joined, "merge-base") {
		return "abc123\n", nil
	}
	if strings.Contains(joined, "--name-only") {
		return "internal/alpha/domain/allow.go\n", nil
	}
	return "diff --git a/internal/alpha/domain/allow.go\n@@ -0,0 +11,1 @@\n+covered\n", nil
}

// auditIn runs the gate with the fixture tree as the working directory,
// because the gate judges relative paths like the Makefile target does.
func auditIn(t *testing.T, root string, runner packageRunner, git gitRunner) []string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	}()
	return audit(RegisterPath, runner, git)
}

// violationsContain reports whether any violation opens with a rule name.
func violationsContain(violations []string, rule string) bool {
	for _, violation := range violations {
		if strings.HasPrefix(violation, rule+":") {
			return true
		}
	}
	return false
}

// TestCleanTreePassesEveryRule is the fixture's own verdict: floors hold,
// risks clear over measured, global clears, and the diff is covered.
func TestCleanTreePassesEveryRule(t *testing.T) {
	root := writeTree(t, fixtureRegister, fixtureCatalog, []string{"internal/alpha/domain", "internal/alpha/application"})
	if violations := auditIn(t, root, fakeCoverRunner(fullProfile), fakeGitEmpty); len(violations) != 0 {
		t.Fatalf("violations = %v, want clean", violations)
	}
}

// TestLoweredFloorFailsRatchet is the falsification of the ratchet: a floor
// above the measured percent is a refusal.
func TestLoweredFloorFailsRatchet(t *testing.T) {
	root := writeTree(t, fixtureRegister, fixtureCatalog, []string{"internal/alpha/domain", "internal/alpha/application"})
	violations := auditIn(t, root, fakeCoverRunner(fixtureProfile), fakeGitEmpty)
	if !violationsContain(violations, "floor-breach") {
		t.Fatalf("violations = %v, want floor-breach", violations)
	}
}

// TestOmittedPackageFailsCoherence is the falsification of completeness: a
// covered package the register does not name is a refusal.
func TestOmittedPackageFailsCoherence(t *testing.T) {
	dropped := `{
  "schema_version": 1,
  "thresholds": {"Q0": 95, "Q1": 90, "Q2": 80, "global": 85, "diff": 95},
  "targets": [
    {"package": "internal/alpha/domain", "risk": ["Q0", "Q1", "Q2"], "floor": 95.0, "status": "measured"}
  ],
  "allowlist": []
}`
	catalog := `{"rules": [{"modules": ["internal/alpha"], "risk": "Q0"}, {"modules": ["internal/alpha"], "risk": "Q1"}, {"modules": ["internal/alpha"], "risk": "Q2"}, {"modules": ["internal/identity"], "risk": "Q0"}]}`
	root := writeTree(t, dropped, catalog, []string{"internal/alpha/domain", "internal/alpha/application", "internal/identity/domain"})
	violations := auditIn(t, root, fakeCoverRunner(fullProfile), fakeGitEmpty)
	if !violationsContain(violations, "unknown-target") {
		t.Fatalf("violations = %v, want unknown-target", violations)
	}
}

// TestUncoveredNewLineFailsDiff is the falsification of the diff ratchet: a
// new statement line outside every covered block is a refusal, and a
// covered one is silence.
func TestUncoveredNewLineFailsDiff(t *testing.T) {
	low := `{
  "schema_version": 1,
  "thresholds": {"Q0": 40, "Q1": 40, "Q2": 40, "global": 40, "diff": 95},
  "targets": [
    {"package": "internal/alpha/domain", "risk": ["Q0", "Q1", "Q2"], "floor": 50.0, "status": "measured"},
    {"package": "internal/alpha/application", "risk": ["Q0"], "floor": 40.0, "status": "deferred", "reason": "needs a budget", "gate": "P45"}
  ],
  "allowlist": []
}`
	root := writeTree(t, low, fixtureCatalog, []string{"internal/alpha/domain", "internal/alpha/application"})
	if violations := auditIn(t, root, fakeCoverRunner(fixtureProfile), fakeGitUncovered); !violationsContain(violations, "diff-breach") {
		t.Fatalf("violations = %v, want diff-breach", violations)
	}
	if violations := auditIn(t, root, fakeCoverRunner(fixtureProfile), fakeGitCovered); len(violations) != 0 {
		t.Fatalf("a covered added line was refused: %v", violations)
	}
}

// TestMeasuredFloorBelowThresholdNeedsDeferral proves the debt rule: a
// measured target whose floor sits under its risk peak is a refusal.
func TestMeasuredFloorBelowThresholdNeedsDeferral(t *testing.T) {
	low := strings.Replace(fixtureRegister, `"floor": 95.0, "status": "measured"`, `"floor": 80.0, "status": "measured"`, 1)
	root := writeTree(t, low, fixtureCatalog, []string{"internal/alpha/domain", "internal/alpha/application"})
	violations := auditIn(t, root, fakeCoverRunner(fullProfile), fakeGitEmpty)
	if !violationsContain(violations, "floor-below-threshold") {
		t.Fatalf("violations = %v, want floor-below-threshold", violations)
	}
}

// TestCoverageAndMutationAreIndependentSignals proves the report's claim
// with the two registers on disk: covered lines host lived mutants, so high
// line coverage does not imply a high kill rate and the gates cannot be one.
func TestCoverageAndMutationAreIndependentSignals(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "quality", "mutations.json"))
	if err != nil {
		t.Fatalf("read mutations register: %v", err)
	}
	if !strings.Contains(string(raw), `"class": "equivalent"`) {
		t.Fatalf("the mutation register lists no equivalent, so covered-but-lived would be unproved")
	}
	coverage, err := os.ReadFile(filepath.Join(root, "quality", "coverage-floors.json"))
	if err != nil {
		t.Fatalf("read coverage register: %v", err)
	}
	if !strings.Contains(string(coverage), `"floor"`) {
		t.Fatalf("the coverage register names no floor, so the comparison would prove nothing")
	}
	fixture := writeTree(t, fixtureRegister, fixtureCatalog, []string{"internal/alpha/domain", "internal/alpha/application"})
	if violations := auditIn(t, fixture, fakeCoverRunner(fullProfile), fakeGitEmpty); len(violations) != 0 {
		t.Fatalf("full coverage should clear the fixture floors: %v", violations)
	}
}
