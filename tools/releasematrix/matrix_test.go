// Tests of the release evidence matrix gate (P45-T02).
//
// Three properties hold, and each of the first two alone would be
// worthless:
//
//   - the manifest this repository commits passes every rule,
//     judged as the file on disk against the Makefile on disk;
//   - each rule refuses a manifest that breaks exactly it. The
//     mutations below change one thing in the green fixture and
//     require the finding, so a rule that stopped working fails
//     here instead of certifying a release. Every mutation is
//     applied through a replacement that fails the test when the
//     text it targets is gone, so no case can pass by mutating
//     nothing;
//   - every rule the gate declares is named by at least one
//     mutation, recorded in exercised and closed by the last test.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exercised records every rule a mutation or result case has seen
// refuse something. TestAllRulesExercised closes the set.
var exercised = map[string]bool{}

func mark(rule string) {
	base := rule
	if index := strings.Index(rule, ":"); index >= 0 {
		base = rule[:index]
	}
	exercised[base] = true
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

func greenText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "quality", "release-matrix.json"))
	if err != nil {
		t.Fatalf("read green manifest: %v", err)
	}
	return string(raw)
}

// mutate applies one text replacement to the green fixture and
// judges the result against the real Makefile. It fails when the
// target text is gone, so the case cannot pass by mutating nothing.
func mutate(t *testing.T, old, new, rule string) []string {
	t.Helper()
	text := greenText(t)
	if !strings.Contains(text, old) {
		t.Fatalf("mutation target gone: %q", old)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "release-matrix.json")
	if err := os.WriteFile(path, []byte(strings.Replace(text, old, new, 1)), 0o644); err != nil {
		t.Fatalf("write mutated manifest: %v", err)
	}
	manifest, findings := LoadManifest(path)
	if len(findings) != 0 {
		t.Fatalf("mutated manifest unreadable: %v", findings)
	}
	got := JudgeManifest(repoRoot(t), manifest)
	mark(rule)
	found := false
	for _, finding := range got {
		if finding == rule {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings = %v, want %q", got, rule)
	}
	return got
}

// TestDeliveredManifestPasses judges the file on disk against the
// Makefile on disk: the matrix this repository commits holds.
func TestDeliveredManifestPasses(t *testing.T) {
	root := repoRoot(t)
	manifest, findings := LoadManifest(filepath.Join(root, "quality", "release-matrix.json"))
	if len(findings) != 0 {
		t.Fatalf("green manifest unreadable: %v", findings)
	}
	if len(manifest.Gates) != 22 {
		t.Fatalf("gates = %d, want 22", len(manifest.Gates))
	}
	if got := JudgeManifest(root, manifest); len(got) != 0 {
		t.Fatalf("green manifest findings = %v", got)
	}
}

// TestManifestMutationsRefuse proves each manifest rule refuses the
// manifest that breaks exactly it.
func TestManifestMutationsRefuse(t *testing.T) {
	mutate(t, `"schema": 1,`, `"schema": 2,`, RuleSchemaUnknown)
	mutate(t, `"id": "P45-G22",`, `"id": "P45-G01",`, RuleDuplicateID+":P45-G01")
	mutate(t, `"area": "supply-chain",`, `"area": "supply-magic",`, RuleAreaUnknown+":supply-magic")
	mutate(t, `"owner": "supply-chain",`, `"owner": "",`, RuleMissingField+":gates.P45-G20.owner")
	mutate(t, `"command": "make image-scan",`, `"command": "make nope-missing-target",`, RuleCommandMissing+":P45-G22")
	mutate(t, `"evidence": "image-scan.json"`, `"evidence": "vuln.json"`, RuleEvidenceDuplicate+":vuln.json:P45-G20+P45-G22")
	mutate(t, `"path": "release-evidence/{commit}/{gate}.json",`, `"path": "release-evidence/latest.json",`, RuleArtifactTemplate)
	mutate(t, `"allowedFor": []`, `"allowedFor": ["load"]`, RuleSkipAllowed)
	mutate(t, `"path": "release-evidence/{commit}/{gate}.json",`, `"path": "",`, RuleMissingField+":artifactFormat")
}

// TestAreaCoverageRefusesDroppedGate removes one whole gate block:
// the area it covered alone must fail as uncovered.
func TestAreaCoverageRefusesDroppedGate(t *testing.T) {
	text := greenText(t)
	start := strings.Index(text, `    {
      "id": "P45-G15",`)
	if start < 0 {
		t.Fatal("gate P45-G15 block gone")
	}
	end := strings.Index(text[start:], "    },\n")
	if end < 0 {
		t.Fatal("gate P45-G15 block end gone")
	}
	block := text[start : start+end+len("    },\n")]
	dir := t.TempDir()
	path := filepath.Join(dir, "release-matrix.json")
	if err := os.WriteFile(path, []byte(strings.Replace(text, block, "", 1)), 0o644); err != nil {
		t.Fatalf("write dropped manifest: %v", err)
	}
	manifest, findings := LoadManifest(path)
	if len(findings) != 0 {
		t.Fatalf("dropped manifest unreadable: %v", findings)
	}
	got := JudgeManifest(repoRoot(t), manifest)
	rule := RuleAreaUncovered + ":restore"
	mark(rule)
	found := false
	for _, finding := range got {
		if finding == rule {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings = %v, want %q", got, rule)
	}
}

// TestOptionalGateRefuses proves a gate flagged required:false is
// refused: the matrix never shrinks silently.
func TestOptionalGateRefuses(t *testing.T) {
	text := greenText(t)
	old := `      "evidence": "verify.json"`
	if !strings.Contains(text, old) {
		t.Fatalf("mutation target gone: %q", old)
	}
	patched := strings.Replace(text, old, old+",\n      \"required\": false", 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "release-matrix.json")
	if err := os.WriteFile(path, []byte(patched), 0o644); err != nil {
		t.Fatalf("write optional manifest: %v", err)
	}
	manifest, findings := LoadManifest(path)
	if len(findings) != 0 {
		t.Fatalf("optional manifest unreadable: %v", findings)
	}
	got := JudgeManifest(repoRoot(t), manifest)
	rule := RuleGateOptional + ":P45-G01"
	mark(rule)
	found := false
	for _, finding := range got {
		if finding == rule {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings = %v, want %q", got, rule)
	}
}

// TestMissingMakefileRefuses proves the manifest cannot pass
// without the Makefile it resolves commands against.
func TestMissingMakefileRefuses(t *testing.T) {
	manifest, findings := LoadManifest(filepath.Join(repoRoot(t), "quality", "release-matrix.json"))
	if len(findings) != 0 {
		t.Fatalf("green manifest unreadable: %v", findings)
	}
	got := JudgeManifest(t.TempDir(), manifest)
	rule := RuleManifestUnreadable + ":makefile"
	mark(rule)
	found := false
	for _, finding := range got {
		if finding == rule {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings = %v, want %q", got, rule)
	}
}

// TestUnreadableManifestRefuses proves a missing file is a finding,
// never a pass.
func TestUnreadableManifestRefuses(t *testing.T) {
	_, findings := LoadManifest(filepath.Join(t.TempDir(), "absent.json"))
	mark(RuleManifestUnreadable)
	if len(findings) != 1 || findings[0] != RuleManifestUnreadable {
		t.Fatalf("findings = %v, want [manifest-unreadable]", findings)
	}
}

const frozenSHA = "0123456789abcdef0123456789abcdef01234567"

func greenResults(t *testing.T) (Manifest, string) {
	t.Helper()
	root := repoRoot(t)
	manifest, findings := LoadManifest(filepath.Join(root, "quality", "release-matrix.json"))
	if len(findings) != 0 {
		t.Fatalf("green manifest unreadable: %v", findings)
	}
	dir := t.TempDir()
	for _, gate := range manifest.Gates {
		result := Result{
			Schema: 1, Gate: gate.ID, Commit: frozenSHA,
			Verdict: "pass", ExitCode: 0, Command: gate.Command,
			Environment: gate.Environment,
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("encode result: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, gate.ID+".json"), raw, 0o644); err != nil {
			t.Fatalf("write result: %v", err)
		}
	}
	return manifest, dir
}

func breakResult(t *testing.T, dir, id string, change func(*Result)) {
	t.Helper()
	path := filepath.Join(dir, id+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result %s: %v", id, err)
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result %s: %v", id, err)
	}
	change(&result)
	patched, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result %s: %v", id, err)
	}
	if err := os.WriteFile(path, patched, 0o644); err != nil {
		t.Fatalf("write result %s: %v", id, err)
	}
}

func requireFinding(t *testing.T, got []string, rule string) {
	t.Helper()
	mark(rule)
	for _, finding := range got {
		if finding == rule {
			return
		}
	}
	t.Fatalf("findings = %v, want %q", got, rule)
}

// TestGreenResultsPass proves a complete passing run on the frozen
// SHA holds with no findings.
func TestGreenResultsPass(t *testing.T) {
	manifest, dir := greenResults(t)
	if got := JudgeResults(manifest, dir, frozenSHA); len(got) != 0 {
		t.Fatalf("green results findings = %v", got)
	}
}

// TestResultsRefuse proves each results rule: a missing gate, a
// skipped gate, a declared pass with a non-zero exit, a failed
// gate, a wrong SHA, a malformed file and an unknown gate each
// fail with their own finding.
func TestResultsRefuse(t *testing.T) {
	manifest, dir := greenResults(t)
	if err := os.Remove(filepath.Join(dir, "P45-G14.json")); err != nil {
		t.Fatalf("drop result: %v", err)
	}
	requireFinding(t, JudgeResults(manifest, dir, frozenSHA), RuleGateMissing+":P45-G14")

	manifest, dir = greenResults(t)
	breakResult(t, dir, "P45-G14", func(result *Result) { result.Skipped = true })
	requireFinding(t, JudgeResults(manifest, dir, frozenSHA), RuleGateSkipped+":P45-G14")

	manifest, dir = greenResults(t)
	breakResult(t, dir, "P45-G13", func(result *Result) { result.ExitCode = 1 })
	requireFinding(t, JudgeResults(manifest, dir, frozenSHA), RuleFalseGreen+":P45-G13")

	manifest, dir = greenResults(t)
	breakResult(t, dir, "P45-G12", func(result *Result) { result.Verdict = "fail"; result.ExitCode = 2 })
	requireFinding(t, JudgeResults(manifest, dir, frozenSHA), RuleGateFailed+":P45-G12")

	manifest, dir = greenResults(t)
	breakResult(t, dir, "P45-G11", func(result *Result) { result.Commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" })
	requireFinding(t, JudgeResults(manifest, dir, frozenSHA), RuleSHAMismatch+":P45-G11")

	manifest, dir = greenResults(t)
	if err := os.WriteFile(filepath.Join(dir, "P45-G10.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write malformed result: %v", err)
	}
	requireFinding(t, JudgeResults(manifest, dir, frozenSHA), RuleResultMalformed+":P45-G10.json")

	manifest, dir = greenResults(t)
	ghost := Result{Schema: 1, Gate: "P45-G99", Commit: frozenSHA, Verdict: "pass"}
	raw, err := json.Marshal(ghost)
	if err != nil {
		t.Fatalf("encode ghost result: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "P45-G99.json"), raw, 0o644); err != nil {
		t.Fatalf("write ghost result: %v", err)
	}
	requireFinding(t, JudgeResults(manifest, dir, frozenSHA), RuleResultUnknown+":P45-G99")

	manifest, dir = greenResults(t)
	requireFinding(t, JudgeResults(manifest, dir, ""), RuleMissingField+":commit")
	if got := JudgeResults(manifest, filepath.Join(t.TempDir(), "absent"), frozenSHA); len(got) == 0 {
		t.Fatal("absent results dir passed")
	} else {
		requireFinding(t, got, RuleManifestUnreadable+":results")
	}
}

// TestCLIReturnsVerdict proves exit 0 holds, exit 1 lists findings
// and exit 2 is a usage error.
func TestCLIReturnsVerdict(t *testing.T) {
	root := repoRoot(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", root}, &stdout, &stderr); code != exitOK {
		t.Fatalf("green exit = %d, want 0 (%s)", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "matrix holds") {
		t.Fatalf("green output = %q", stdout.String())
	}
	stdout.Reset()
	if code := run([]string{"-root", root, "-manifest", "quality/nope.json"}, &stdout, &stderr); code != exitAudit {
		t.Fatalf("missing manifest exit = %d, want 1", code)
	}
	mark(RuleManifestUnreadable)
	var usageOut, usageErr bytes.Buffer
	if code := run([]string{"-root"}, &usageOut, &usageErr); code != exitUsage {
		t.Fatalf("usage exit = %d, want 2", code)
	}
	_, dir := greenResults(t)
	stdout.Reset()
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		t.Fatalf("rel results dir: %v", err)
	}
	if code := run([]string{"-root", root, "-results", rel, "-commit", frozenSHA}, &stdout, &stderr); code != exitOK {
		t.Fatalf("green results exit = %d, want 0 (%s)", code, stdout.String())
	}
	if code := run([]string{"-root", root, "-results", rel}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("results without commit exit = %d, want 2", code)
	}
}

// TestAllRulesExercised closes the vocabulary: every declared rule
// refused something above. It runs last and never in parallel.
func TestAllRulesExercised(t *testing.T) {
	for _, rule := range AllRules() {
		if !exercised[rule] {
			t.Errorf("rule %q never refused anything", rule)
		}
	}
}
