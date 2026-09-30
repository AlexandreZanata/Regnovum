package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRegister is a minimal valid register: one measured target with
// one proven equivalent, one deferred target, and thresholds the fixture
// reports clear.
const fixtureRegister = `{
  "schema_version": 1,
  "tool": {"module": "example.com/tool/cmd/tool", "version": "v0.1.0", "timeout_coefficient": 100, "workers": 8},
  "operators": {"enabled": ["CONDITIONALS_BOUNDARY"], "deferred": {"INVERT_LOGICAL": "P45 decides"}},
  "thresholds": {"Q0": 90, "Q1": 80},
  "targets": [
    {"package": "internal/alpha/domain", "risk": ["Q0", "Q1"], "areas": ["authorization"], "status": "measured"},
    {"package": "internal/alpha/application", "risk": ["Q0"], "status": "deferred", "reason": "needs a budget", "gate": "P45"}
  ],
  "zero_areas": [{"area": "authorization", "paths": ["internal/alpha/domain/allow.go"]}],
  "equivalents": [
    {"package": "internal/alpha/domain", "file": "allow.go", "mutant": "CONDITIONALS_BOUNDARY", "class": "equivalent", "count": 1, "reason": "clamp identity", "owner": "P24-T10"}
  ],
  "exclusions": []
}`

// fixtureCatalog backs the fixture risks: alpha carries Q0 and Q1.
const fixtureCatalog = `{"schema_version": 1, "rules": [
  {"id": "R-1", "source": "t", "description": "d", "risk": "Q0", "modules": ["internal/alpha"], "actors": [], "states": [], "tests": [], "authorization": false, "concurrency": false, "idempotency": false, "security": false, "privacy": false, "evidence": [], "not_applicable": false},
  {"id": "R-2", "source": "t", "description": "d", "risk": "Q1", "modules": ["internal/alpha"], "actors": [], "states": [], "tests": [], "authorization": false, "concurrency": false, "idempotency": false, "security": false, "privacy": false, "evidence": [], "not_applicable": false}
]}`

// fixtureReport is nine kills and one listed equivalent: efficacy clears
// every threshold with the equivalent counted loudly.
const fixtureReport = `{"files": [{"file_name": "allow.go", "mutations": [
  {"type": "CONDITIONALS_BOUNDARY", "status": "KILLED", "line": 10, "column": 1},
  {"type": "CONDITIONALS_BOUNDARY", "status": "KILLED", "line": 11, "column": 1},
  {"type": "CONDITIONALS_BOUNDARY", "status": "KILLED", "line": 12, "column": 1},
  {"type": "CONDITIONALS_BOUNDARY", "status": "KILLED", "line": 13, "column": 1},
  {"type": "CONDITIONALS_BOUNDARY", "status": "KILLED", "line": 14, "column": 1},
  {"type": "CONDITIONALS_BOUNDARY", "status": "KILLED", "line": 15, "column": 1},
  {"type": "CONDITIONALS_BOUNDARY", "status": "KILLED", "line": 16, "column": 1},
  {"type": "CONDITIONALS_BOUNDARY", "status": "KILLED", "line": 17, "column": 1},
  {"type": "CONDITIONALS_BOUNDARY", "status": "KILLED", "line": 18, "column": 1},
  {"type": "CONDITIONALS_BOUNDARY", "status": "LIVED", "line": 19, "column": 1}
]}]}`

func fixturePins() toolPins {
	return toolPins{
		module:             "example.com/tool/cmd/tool",
		version:            "v0.1.0",
		timeoutCoefficient: 100,
		workers:            8,
	}
}

func writeFixtureTree(t *testing.T, register, catalog string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write := func(path, content string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("quality/mutations.json", register)
	write("quality/catalog.json", catalog)
	for path, body := range files {
		if strings.HasSuffix(path, ".go") {
			write(path, "package domain\n\n"+body)
		} else {
			write(path+"/live.go", "package domain\n\n"+body)
		}
	}
	return root
}

func validPackages() map[string]string {
	return map[string]string{
		"internal/alpha/domain":          "func Allow() bool { return true }\n",
		"internal/alpha/domain/allow.go": "func AllowGuard() bool { return true }\n",
		"internal/alpha/application":     "func Serve() bool { return true }\n",
	}
}

func fakeRunner(t *testing.T, report string) func(args []string) error {
	t.Helper()
	return func(args []string) error {
		output := args[len(args)-1]
		if err := os.WriteFile(output, []byte(report), 0o644); err != nil {
			t.Fatal(err)
		}
		return nil
	}
}

// auditIn runs the gate with the fixture tree as the working directory,
// because the gate judges relative paths like the Makefile target does.
func auditIn(t *testing.T, root string, pins toolPins, report string) []string {
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
	return audit(pins, RegisterPath, fakeRunner(t, report))
}

func violationsContain(violations []string, rule string) bool {
	for _, violation := range violations {
		if strings.HasPrefix(violation, rule+":") {
			return true
		}
	}
	return false
}

func TestRegisterRefusesMalformedDocuments(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(map[string]any)
		wantRule string
	}{
		{name: "bad version", mutate: func(m map[string]any) { m["schema_version"] = 2 }, wantRule: "unsupported-register"},
		{name: "unknown operator", mutate: func(m map[string]any) {
			m["operators"].(map[string]any)["enabled"] = []any{"MIND_CONTROL"}
		}, wantRule: "unknown-operator"},
		{name: "missing threshold", mutate: func(m map[string]any) {
			delete(m["thresholds"].(map[string]any), "Q1")
		}, wantRule: "missing-threshold"},
		{name: "unknown status", mutate: func(m map[string]any) {
			m["targets"].([]any)[0].(map[string]any)["status"] = "eventually"
		}, wantRule: "unknown-status"},
		{name: "bare deferral", mutate: func(m map[string]any) {
			target := m["targets"].([]any)[1].(map[string]any)
			delete(target, "reason")
		}, wantRule: "unjustified-deferral"},
		{name: "countless manifest", mutate: func(m map[string]any) {
			m["equivalents"].([]any)[0].(map[string]any)["count"] = 0
		}, wantRule: "countless-manifest"},
		{name: "unproven manifest", mutate: func(m map[string]any) {
			m["equivalents"].([]any)[0].(map[string]any)["reason"] = ""
		}, wantRule: "unproven-manifest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var decoded map[string]any
			if err := json.Unmarshal([]byte(fixtureRegister), &decoded); err != nil {
				t.Fatal(err)
			}
			tc.mutate(decoded)
			raw, err := json.Marshal(decoded)
			if err != nil {
				t.Fatal(err)
			}
			root := writeFixtureTree(t, string(raw), fixtureCatalog, validPackages())
			_, violations := ReadRegister(root, RegisterPath)
			if !violationsContain(violations, tc.wantRule) {
				t.Fatalf("violations = %v, want %s", violations, tc.wantRule)
			}
		})
	}
}

func TestAuditAcceptsCleanTree(t *testing.T) {
	root := writeFixtureTree(t, fixtureRegister, fixtureCatalog, validPackages())
	if violations := auditIn(t, root, fixturePins(), fixtureReport); len(violations) != 0 {
		t.Fatalf("violations = %v, want clean", violations)
	}
}

func TestAuditRefusesPinDrift(t *testing.T) {
	root := writeFixtureTree(t, fixtureRegister, fixtureCatalog, validPackages())
	pins := fixturePins()
	pins.version = "v9.9.9"
	if violations := auditIn(t, root, pins, fixtureReport); !violationsContain(violations, "tool-pin-mismatch") {
		t.Fatalf("violations = %v, want tool-pin-mismatch", violations)
	}
}

func TestAuditRefusesUnknownTarget(t *testing.T) {
	packages := validPackages()
	packages["internal/identity/domain"] = "func Check() bool { return true }\n"
	catalog := strings.Replace(fixtureCatalog, `"modules": ["internal/alpha"]`, `"modules": ["internal/alpha", "internal/identity"]`, 1)
	root := writeFixtureTree(t, fixtureRegister, catalog, packages)
	if violations := auditIn(t, root, fixturePins(), fixtureReport); !violationsContain(violations, "unknown-target") {
		t.Fatalf("violations = %v, want unknown-target", violations)
	}
}

func TestAuditRefusesUnlistedSurvivor(t *testing.T) {
	root := writeFixtureTree(t, fixtureRegister, fixtureCatalog, validPackages())
	var decoded map[string]any
	if err := json.Unmarshal([]byte(fixtureReport), &decoded); err != nil {
		t.Fatal(err)
	}
	// A lived mutant of a kind the manifest never names is unlisted even
	// under a zero path, and the area names it.
	decoded["files"] = append(decoded["files"].([]any), map[string]any{
		"file_name": "allow.go",
		"mutations": []any{
			map[string]any{
				"type":   "ARITHMETIC_BASE",
				"status": "LIVED",
				"line":   5,
				"column": 1,
			},
		},
	})
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if violations := auditIn(t, root, fixturePins(), string(raw)); !violationsContain(violations, "zero-survivor-authorization") {
		t.Fatalf("violations = %v, want zero-survivor-authorization", violations)
	}
}

func TestAuditRefusesStaleManifest(t *testing.T) {
	root := writeFixtureTree(t, fixtureRegister, fixtureCatalog, validPackages())
	report := strings.Replace(fixtureReport, `"status": "LIVED", "line": 19`,
		`"status": "KILLED", "line": 19`, 1)
	if violations := auditIn(t, root, fixturePins(), report); !violationsContain(violations, "survivor-count-drift") {
		t.Fatalf("violations = %v, want survivor-count-drift", violations)
	}
}

func TestAuditRefusesThresholdBreach(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(fixtureRegister), &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["thresholds"].(map[string]any)["Q0"] = 100.0
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	// Nine kills and one listed equivalent score 90%: a bar of 100 refuses.
	root := writeFixtureTree(t, string(raw), fixtureCatalog, validPackages())
	if violations := auditIn(t, root, fixturePins(), fixtureReport); !violationsContain(violations, "threshold-breach-Q0") {
		t.Fatalf("violations = %v, want threshold-breach-Q0", violations)
	}
}

func TestOperatorArgsPinEveryKnownOperator(t *testing.T) {
	var register Register
	if err := json.Unmarshal([]byte(fixtureRegister), &register); err != nil {
		t.Fatal(err)
	}
	args, violations := operatorArgs(register)
	if len(violations) != 0 {
		t.Fatalf("violations = %v", violations)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--conditionals-boundary", "--invert-logical=false"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args = %q, want %s pinned", joined, want)
		}
	}
}
