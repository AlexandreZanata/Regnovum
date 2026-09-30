package main

// P30-T06 — independent dual execution: the decisor runs twice over
// small independent fixtures, one valid and one with missing or tampered
// evidence, and decisions plus manifests are compared.
//
// The fixtures are hermetic by construction: synthetic commits that can
// never match a real SHA, synthetic gate names scoped to the fixture
// tiers, evidence covering exactly the required leaves (no phantom
// results), and bundle directories that only ever live under the test
// temporary directory — so no fixture can be confused with a real
// certificate, and no fixture claims a gate it did not run.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fixtureCommit = "fixture-valid-01"
	dualTiers     = `{"schema":1,"tiers":{"probe":{"gates":["probe-a","probe-b"]}}}`
)

func fixtureVerdict(gate, verdict string) string {
	return `{"gate":` + quoted(gate) + `,"verdict":` + quoted(verdict) + `,"commit":` + quoted(fixtureCommit) + `}`
}

func quoted(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// dualFixture builds one independent valid fixture bundle: the tiers,
// an empty register and one passing verdict per required leaf.
func dualFixture(t *testing.T) string {
	t.Helper()
	manifest, files := dualManifest(t, map[string]string{
		"probe-a.json": fixtureVerdict("probe-a", "pass"),
		"probe-b.json": fixtureVerdict("probe-b", "pass"),
	})
	return fixtureBundle(t, manifest, files)
}

func dualManifest(t *testing.T, evidence map[string]string) (map[string]any, map[string]string) {
	t.Helper()
	tiers := dualTiers
	waivers := `{"waivers":[]}`
	files := map[string]string{"artifacts/tiers": tiers, "artifacts/waivers": waivers}
	entries := []any{artifactEntry("tiers", "artifacts/tiers", tiers), artifactEntry("waivers", "artifacts/waivers", waivers)}
	var evidenceEntries []any
	names := make([]string, 0, len(evidence))
	for name := range evidence {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		files["evidence/"+name] = evidence[name]
		evidenceEntries = append(evidenceEntries, artifactEntry(name, "evidence/"+name, evidence[name]))
	}
	manifest := map[string]any{
		"schema":     1,
		"commit":     fixtureCommit,
		"clean":      true,
		"toolchains": map[string]string{"go": "1.27.1"},
		"artifacts":  entries,
		"evidence":   evidenceEntries,
	}
	return manifest, files
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func decideJSON(t *testing.T, dir string) (Decision, string) {
	t.Helper()
	decision := Decide(dir, decideInstant)
	encoded, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	return decision, string(encoded)
}

func manifestBytes(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestDualExecutionReproducesValidFixture runs the decisor twice over
// two independently built valid fixtures: both PASS, with identical
// decisions and identical manifests.
func TestDualExecutionReproducesValidFixture(t *testing.T) {
	t.Parallel()

	first, second := dualFixture(t), dualFixture(t)
	if first == second {
		t.Fatal("independent fixtures share a directory")
	}
	decisionOne, jsonOne := decideJSON(t, first)
	decisionTwo, jsonTwo := decideJSON(t, second)
	if decisionOne.Decision != DecisionPass || decisionTwo.Decision != DecisionPass {
		t.Fatalf("decisions = %+v / %+v, want dual PASS", decisionOne, decisionTwo)
	}
	if jsonOne != jsonTwo {
		t.Fatalf("decisions diverge:\n%s\n%s", jsonOne, jsonTwo)
	}
	if manifestBytes(t, first) != manifestBytes(t, second) {
		t.Fatal("manifests diverge on identical evidence")
	}
	if decisionOne.Commit != fixtureCommit {
		t.Fatalf("commit = %q, want the synthetic fixture commit", decisionOne.Commit)
	}
}

// TestDualExecutionFailsInvalidFixtureStably runs the decisor twice over
// independently built invalid fixtures — one missing evidence, one
// tampered — and proves both FAIL with the identical stable reason.
func TestDualExecutionFailsInvalidFixtureStably(t *testing.T) {
	t.Parallel()

	t.Run("missing evidence", func(t *testing.T) {
		t.Parallel()

		build := func(t *testing.T) string {
			manifest, files := dualManifest(t, map[string]string{
				"probe-a.json": fixtureVerdict("probe-a", "pass"),
			})
			return fixtureBundle(t, manifest, files)
		}
		one, jsonOne := decideJSON(t, build(t))
		two, jsonTwo := decideJSON(t, build(t))
		if one.Decision != DecisionFail || two.Decision != DecisionFail {
			t.Fatalf("decisions = %+v / %+v, want dual FAIL", one, two)
		}
		if jsonOne != jsonTwo {
			t.Fatalf("reasons diverge:\n%s\n%s", jsonOne, jsonTwo)
		}
		if len(one.Reasons) != 1 || one.Reasons[0] != "q0-missing:probe-b" {
			t.Fatalf("reasons = %v, want the stable q0-missing:probe-b", one.Reasons)
		}
	})

	t.Run("tampered evidence", func(t *testing.T) {
		t.Parallel()

		build := func(t *testing.T) string {
			manifest, files := dualManifest(t, map[string]string{
				"probe-a.json": fixtureVerdict("probe-a", "pass"),
				"probe-b.json": fixtureVerdict("probe-b", "pass"),
			})
			dir := fixtureBundle(t, manifest, files)
			raw, err := os.ReadFile(filepath.Join(dir, "evidence", "probe-b.json"))
			if err != nil {
				t.Fatal(err)
			}
			raw[0] ^= 0xff
			if err := os.WriteFile(filepath.Join(dir, "evidence", "probe-b.json"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			return dir
		}
		one, jsonOne := decideJSON(t, build(t))
		two, jsonTwo := decideJSON(t, build(t))
		if one.Decision != DecisionFail || two.Decision != DecisionFail {
			t.Fatalf("decisions = %+v / %+v, want dual FAIL", one, two)
		}
		if jsonOne != jsonTwo {
			t.Fatalf("reasons diverge:\n%s\n%s", jsonOne, jsonTwo)
		}
	})
}

// TestFixturesCannotPassAsCertificates proves the anti-confusion
// contract: fixture bundles live only in temporary directories, carry
// only synthetic commits, cover exactly their required leaves with no
// phantom results, and name no production gate.
func TestFixturesCannotPassAsCertificates(t *testing.T) {
	t.Parallel()

	dir := dualFixture(t)
	if !strings.HasPrefix(dir, os.TempDir()) {
		t.Fatalf("fixture bundle outside the temporary directory: %s", dir)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Commit   string `json:"commit"`
		Evidence []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(manifest.Commit, "fixture-") {
		t.Fatalf("fixture commit = %q, want the synthetic prefix", manifest.Commit)
	}
	if len(manifest.Evidence) != 2 {
		t.Fatalf("fixture evidence holds %d results, want exactly the two required leaves", len(manifest.Evidence))
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Evidence {
		content, err := os.ReadFile(filepath.Join(dir, entry.Path))
		if err != nil {
			t.Fatal(err)
		}
		var verdict struct {
			Gate    string `json:"gate"`
			Verdict string `json:"verdict"`
		}
		if err := json.Unmarshal(content, &verdict); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(verdict.Gate, "probe-") {
			t.Fatalf("fixture verdict names %q, outside the fixture scope", verdict.Gate)
		}
		if verdict.Verdict != "pass" {
			t.Fatalf("valid fixture carries %q, not an executed pass", verdict.Verdict)
		}
		seen[verdict.Gate] = true
	}
	if !seen["probe-a"] || !seen["probe-b"] {
		t.Fatalf("fixture verdicts = %v, want exactly the required leaves", seen)
	}
}
