package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var decideInstant = time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)

// fixtureBundle writes a bundle directory: files by bundle-relative path
// plus a manifest listing them with correct checksums, sealed. Mutations
// break exactly one axis each.
func fixtureBundle(t *testing.T, manifest map[string]any, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	if err := os.WriteFile(filepath.Join(dir, "manifest.sha256"), []byte(hex.EncodeToString(sum[:])+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func artifactEntry(name, path, content string) map[string]any {
	sum := sha256.Sum256([]byte(content))
	return map[string]any{"name": name, "path": path, "sha256": hex.EncodeToString(sum[:]), "bytes": len(content)}
}

func cleanManifest(t *testing.T, tiers, waivers string, evidence map[string]string) (map[string]any, map[string]string) {
	t.Helper()
	files := map[string]string{
		"artifacts/tiers":   tiers,
		"artifacts/waivers": waivers,
	}
	entries := []any{artifactEntry("tiers", "artifacts/tiers", tiers), artifactEntry("waivers", "artifacts/waivers", waivers)}
	var evidenceEntries []any
	names := make([]string, 0, len(evidence))
	for name := range evidence {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		content := evidence[name]
		files["evidence/"+name] = content
		evidenceEntries = append(evidenceEntries, artifactEntry(name, "evidence/"+name, content))
	}
	if evidenceEntries == nil {
		evidenceEntries = []any{}
	}
	manifest := map[string]any{
		"schema":     1,
		"commit":     "abc1234",
		"clean":      true,
		"toolchains": map[string]string{"go": "1.27.1"},
		"artifacts":  entries,
		"evidence":   evidenceEntries,
	}
	return manifest, files
}

const fixtureTiers = `{"schema":1,"tiers":{"fast":{"gates":["fmt-check"]},"certify":{"gates":["fast","verify"]}}}`

func passVerdict(gate string) string {
	encoded, _ := json.Marshal(gate)
	return `{"gate":` + string(encoded) + `,"verdict":"pass","commit":"abc1234"}`
}

// TestDecidePassesCompleteEvidence proves the only PASS: a verified
// bundle whose required gates all passed and whose register is empty.
func TestDecidePassesCompleteEvidence(t *testing.T) {
	t.Parallel()

	manifest, files := cleanManifest(t, fixtureTiers, `{"waivers":[]}`, map[string]string{
		"fmt-check.json": passVerdict("fmt-check"),
		"verify.json":    passVerdict("verify"),
	})
	dir := fixtureBundle(t, manifest, files)
	decision := Decide(dir, decideInstant)
	if decision.Decision != DecisionPass || len(decision.Reasons) != 0 || decision.Commit != "abc1234" {
		t.Fatalf("decision = %+v, want clean PASS", decision)
	}
}

// TestTierReferencesExpandToLeaves proves tier composition resolves
// through the quality- prefix of the Makefile targets: evidence covers
// leaf gates, never aggregates.
func TestTierReferencesExpandToLeaves(t *testing.T) {
	t.Parallel()

	tiers := `{"schema":1,"tiers":{"outer":{"gates":["quality-inner","direct"]},"inner":{"gates":["deep"]}}}`
	manifest, files := cleanManifest(t, tiers, `{"waivers":[]}`, map[string]string{
		"direct.json": passVerdict("direct"),
		"deep.json":   passVerdict("deep"),
	})
	dir := fixtureBundle(t, manifest, files)
	if decision := Decide(dir, decideInstant); decision.Decision != DecisionPass {
		t.Fatalf("decision = %+v, want PASS", decision)
	}
}

// TestDecideFailsClosed drives every refusal: a missing or failed Q0, a
// forbidden or expired waiver, a dirty tree and every evidence mismatch.
func TestDecideFailsClosed(t *testing.T) {
	t.Parallel()

	future, past := "2099-01-01", "2020-01-01"
	forbidden := `{"waivers":[{"id":"WVR-1","risk":"Q0","categories":["security"],"expires":"` + future + `"}]}`
	expired := `{"waivers":[{"id":"WVR-2","risk":"Q1","categories":["documentation"],"expires":"` + past + `"}]}`
	tests := []struct {
		name    string
		mutate  func(map[string]any, map[string]string)
		reasons []string
	}{
		{name: "missing gate", mutate: func(manifest map[string]any, files map[string]string) {
			delete(files, "evidence/verify.json")
			manifest["evidence"] = []any{manifest["evidence"].([]any)[0]}
		}, reasons: []string{"q0-missing:verify"}},
		{name: "failed gate", mutate: func(manifest map[string]any, files map[string]string) {
			files["evidence/verify.json"] = `{"gate":"verify","verdict":"fail","commit":"abc1234"}`
			manifest["evidence"] = []any{artifactEntry("verify.json", "evidence/verify.json", files["evidence/verify.json"])}
		}, reasons: []string{"q0-failed:verify"}},
		{name: "forbidden waiver", mutate: func(manifest map[string]any, files map[string]string) {
			files["artifacts/waivers"] = forbidden
			manifest["artifacts"] = []any{artifactEntry("tiers", "artifacts/tiers", files["artifacts/tiers"]), artifactEntry("waivers", "artifacts/waivers", forbidden)}
		}, reasons: []string{"waiver-forbidden:WVR-1"}},
		{name: "expired waiver", mutate: func(manifest map[string]any, files map[string]string) {
			files["artifacts/waivers"] = expired
			manifest["artifacts"] = []any{artifactEntry("tiers", "artifacts/tiers", files["artifacts/tiers"]), artifactEntry("waivers", "artifacts/waivers", expired)}
		}, reasons: []string{"waiver-expired:WVR-2"}},
		{name: "dirty tree", mutate: func(manifest map[string]any, files map[string]string) {
			manifest["clean"] = false
		}, reasons: []string{"dirty-tree"}},
		{name: "foreign commit", mutate: func(manifest map[string]any, files map[string]string) {
			files["evidence/verify.json"] = `{"gate":"verify","verdict":"pass","commit":"zzz"}`
			manifest["evidence"] = []any{manifest["evidence"].([]any)[0], artifactEntry("verify.json", "evidence/verify.json", files["evidence/verify.json"])}
		}, reasons: []string{"evidence-mismatch:foreign-commit:evidence/verify.json"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			manifest, files := cleanManifest(t, fixtureTiers, `{"waivers":[]}`, map[string]string{
				"fmt-check.json": passVerdict("fmt-check"),
				"verify.json":    passVerdict("verify"),
			})
			test.mutate(manifest, files)
			dir := fixtureBundle(t, manifest, files)
			decision := Decide(dir, decideInstant)
			if decision.Decision != DecisionFail {
				t.Fatalf("decision = %+v, want FAIL", decision)
			}
			for _, reason := range test.reasons {
				found := false
				for _, got := range decision.Reasons {
					found = found || got == reason
				}
				if !found {
					t.Errorf("reasons = %v, want %q", decision.Reasons, reason)
				}
			}
		})
	}

	t.Run("tampered bytes", func(t *testing.T) {
		t.Parallel()

		manifest, files := cleanManifest(t, fixtureTiers, `{"waivers":[]}`, map[string]string{
			"fmt-check.json": passVerdict("fmt-check"),
			"verify.json":    passVerdict("verify"),
		})
		dir := fixtureBundle(t, manifest, files)
		raw, err := os.ReadFile(filepath.Join(dir, "artifacts", "tiers"))
		if err != nil {
			t.Fatal(err)
		}
		raw[0] ^= 0xff
		if err := os.WriteFile(filepath.Join(dir, "artifacts", "tiers"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		decision := Decide(dir, decideInstant)
		if decision.Decision != DecisionFail {
			t.Fatalf("tampered decision = %+v, want FAIL", decision)
		}
	})

	t.Run("resealed manifest", func(t *testing.T) {
		t.Parallel()

		manifest, files := cleanManifest(t, fixtureTiers, `{"waivers":[]}`, map[string]string{
			"fmt-check.json": passVerdict("fmt-check"),
			"verify.json":    passVerdict("verify"),
		})
		dir := fixtureBundle(t, manifest, files)
		if err := os.WriteFile(filepath.Join(dir, "manifest.sha256"), []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		decision := Decide(dir, decideInstant)
		if decision.Decision != DecisionFail {
			t.Fatalf("resealed decision = %+v, want FAIL", decision)
		}
	})
}

// TestExpiryFollowsTheInstantNotTheWallClock proves expiry judges the
// injected instant: the same waiver passes before its day and fails
// after, so tests never depend on the wall clock and production passes
// it explicitly.
func TestExpiryFollowsTheInstantNotTheWallClock(t *testing.T) {
	t.Parallel()

	waivers := `{"waivers":[{"id":"WVR-9","risk":"Q1","categories":["documentation"],"expires":"2026-01-02"}]}`
	build := func() string {
		manifest, files := cleanManifest(t, fixtureTiers, waivers, map[string]string{
			"fmt-check.json": passVerdict("fmt-check"),
			"verify.json":    passVerdict("verify"),
		})
		return fixtureBundle(t, manifest, files)
	}
	dir := build()
	if decision := Decide(dir, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); decision.Decision != DecisionPass {
		t.Fatalf("day-before decision = %+v, want PASS", decision)
	}
	if decision := Decide(build(), time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)); decision.Decision != DecisionFail {
		t.Fatalf("day-after decision = %+v, want FAIL", decision)
	}
}

// TestReasonsAreStableVocabulary proves the output contract: sorted
// reasons from the fixed vocabulary, and extra evidence for unknown
// gates stays inert instead of failing.
func TestReasonsAreStableVocabulary(t *testing.T) {
	t.Parallel()

	manifest, files := cleanManifest(t, fixtureTiers, `{"waivers":[]}`, map[string]string{
		"extra.json": `{"gate":"informational","verdict":"fail","commit":"abc1234"}`,
	})
	dir := fixtureBundle(t, manifest, files)
	decision := Decide(dir, decideInstant)
	if decision.Decision != DecisionFail {
		t.Fatalf("decision = %+v, want FAIL", decision)
	}
	known := map[string]bool{
		ReasonBundleCorrupt: true, ReasonMissingField: true, ReasonDirtyTree: true,
		ReasonEvidenceMismatch: true, ReasonQ0Missing: true, ReasonQ0Failed: true,
		ReasonWaiverForbidden: true, ReasonWaiverExpired: true,
	}
	previous := ""
	for _, reason := range decision.Reasons {
		if reason < previous {
			t.Fatalf("reasons = %v, want sorted", decision.Reasons)
		}
		previous = reason
		head := reason
		if index := strings.Index(reason, ":"); index >= 0 {
			head = reason[:index]
		}
		if !known[head] {
			t.Fatalf("reason %q outside the vocabulary", reason)
		}
	}
}

// TestNoManualOverrideVectors proves the prohibition structurally: the
// command reads no environment, and its only flag names the bundle —
// there is no input that can flip a verdict.
func TestNoManualOverrideVectors(t *testing.T) {
	t.Parallel()

	for _, file := range []string{"decide.go", "main.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, vector := range []string{"os.Getenv", "LookupEnv", "os.Environ", "os.Stdin"} {
			if strings.Contains(string(raw), vector) {
				t.Errorf("%s references %s: the decision must not read the environment", file, vector)
			}
		}
	}
	if code := run([]string{"-bundle", t.TempDir(), "-pass"}, io.Discard, io.Discard, decideInstant); code != exitUsage {
		t.Errorf("unknown flag exit = %d, want %d", code, exitUsage)
	}
}

// TestCLIExitCodes pins the command contract on a hermetic bundle.
func TestCLIExitCodes(t *testing.T) {
	t.Parallel()

	manifest, files := cleanManifest(t, fixtureTiers, `{"waivers":[]}`, map[string]string{
		"fmt-check.json": passVerdict("fmt-check"),
		"verify.json":    passVerdict("verify"),
	})
	dir := fixtureBundle(t, manifest, files)
	if code := run([]string{"-bundle", dir}, io.Discard, io.Discard, decideInstant); code != exitPass {
		t.Errorf("clean exit = %d, want %d", code, exitPass)
	}
	if code := run([]string{}, io.Discard, io.Discard, decideInstant); code != exitUsage {
		t.Errorf("missing bundle exit = %d, want %d", code, exitUsage)
	}
	if code := run([]string{"-bundle", filepath.Join(t.TempDir(), "absent")}, io.Discard, io.Discard, decideInstant); code != exitFail {
		t.Errorf("absent bundle exit = %d, want %d (unreadable is FAIL, not usage)", code, exitFail)
	}
}
