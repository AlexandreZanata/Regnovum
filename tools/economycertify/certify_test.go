package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func greenBundle(t *testing.T) Bundle {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "green.json"))
	if err != nil {
		t.Fatalf("read green fixture: %v", err)
	}
	var bundle Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("decode green fixture: %v", err)
	}
	return bundle
}

func writeBundle(t *testing.T, bundle Bundle) string {
	t.Helper()
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("encode bundle: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return path
}

func decideBundle(t *testing.T, bundle Bundle, commit string) Decision {
	t.Helper()
	return Decide(writeBundle(t, bundle), commit)
}

// TestDecisorPassesGreenFixture proves the green fixture passes
// with no reasons: the control every red mutation breaks.
func TestDecisorPassesGreenFixture(t *testing.T) {
	bundle := greenBundle(t)
	decision := decideBundle(t, bundle, "")
	if decision.Decision != DecisionPass || len(decision.Reasons) != 0 {
		t.Fatalf("green fixture: %+v", decision)
	}
	if decision.Commit != bundle.Commit {
		t.Fatalf("commit = %q, want the bundle SHA", decision.Commit)
	}
	// The committed fixture is itself green through the file path
	// the Makefile target judges.
	fileDecision := Decide(filepath.Join("testdata", "green.json"), "")
	if fileDecision.Decision != DecisionPass {
		t.Fatalf("testdata green: %+v", fileDecision)
	}
}

// TestDecisorRefusesEachAxis breaks exactly one axis per case and
// proves the reason names it: missing file, malformed SHA, dirty
// tree, pending decision, open finding, pending country, milli
// drift, active product and every final-decision axis all FAIL.
func TestDecisorRefusesEachAxis(t *testing.T) {
	green := greenBundle(t)
	cases := []struct {
		name   string
		mutate func(*Bundle)
		reason string
	}{
		{"commit-malformed", func(b *Bundle) { b.Commit = "not-a-sha" }, ReasonMissingField + ":commit"},
		{"dirty-tree", func(b *Bundle) { b.TreeClean = false }, ReasonDirtyTree},
		{"decision-pending", func(b *Bundle) { b.Decisions["Q20"] = "PENDENTE" }, ReasonDecisionsPending + ":Q20"},
		{"season-altered", func(b *Bundle) { b.SeasonDigest = "sha256:dead" }, ReasonSeasonMismatch},
		{"toolchain-drift", func(b *Bundle) { b.Toolchain["go"] = "9.9.9" }, ReasonToolchainDrift + ":go"},
		{"coverage-gap", func(b *Bundle) { b.AxesCovered = b.AxesRequired - 1 }, ReasonCoverageGap},
		{"mutation-survivor", func(b *Bundle) { b.MutationSurvivors = 1 }, ReasonMutationSurvivor},
		{"capacity-regression", func(b *Bundle) { b.CapacityRegression = true }, ReasonCapacityReorg},
		{"threat-open", func(b *Bundle) { b.HighFindings = 1 }, ReasonThreatOpen},
		{"country-pending", func(b *Bundle) { b.PendingCountries = []string{"BR"} }, ReasonJurisPending + ":BR"},
		{"milli-drift", func(b *Bundle) { b.Books[0].DriftMilli = 1 }, ReasonMilliDrift + ":S-2077-A"},
		{"merge-missing", func(b *Bundle) { b.MergeP47 = false }, ReasonMergeMissing + ":P47"},
		{"stale-credential", func(b *Bundle) { b.RealCredentialDays = 91 }, ReasonStaleCredential},
		{"product-active", func(b *Bundle) { b.ProhibitedActive = true }, ReasonProductActive},
		{"oracle-short", func(b *Bundle) { b.Oracle.Sequences = 7 }, ReasonOracleScale},
		{"resets-short", func(b *Bundle) { b.SeasonResets.Count = 2 }, ReasonResetsShort},
		{"webhook-unproven", func(b *Bundle) { b.LateWebhook.Proven = false }, ReasonWebhookUnproven},
		{"ties-unjudged", func(b *Bundle) { b.TiedTakes.Proven = false }, ReasonTiesUnjudged},
		{"exking-unrefused", func(b *Bundle) { b.ExKing.Proven = false }, ReasonExKingUnrefused},
		{"purchase-no-term", func(b *Bundle) { b.Purchase.TermDays = 0 }, ReasonPurchaseExpiry},
		{"archive-unkept", func(b *Bundle) { b.Archive.Retained = false }, ReasonArchiveUnkept},
		{"opinion-missing", func(b *Bundle) { b.IndependentOpinion.Reference = "" }, ReasonOpinionMissing},
		{"opinion-open", func(b *Bundle) { b.IndependentOpinion.Critical = 1 }, ReasonOpinionOpen},
		{"legal-pending", func(b *Bundle) { b.Legal.Approvals = map[string]bool{} }, ReasonLegalPending + ":BR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := green
			tc.mutate(&bundle)
			decision := decideBundle(t, bundle, "")
			if decision.Decision != DecisionFail {
				t.Fatalf("%s passed: %+v", tc.name, decision)
			}
			found := false
			for _, reason := range decision.Reasons {
				if reason == tc.reason {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s reasons = %v, want %q", tc.name, decision.Reasons, tc.reason)
			}
		})
	}
}

// TestDecisorHoldsSHA proves the optional commit hold refuses any
// alteration while the unheld green still passes.
func TestDecisorHoldsSHA(t *testing.T) {
	bundle := greenBundle(t)
	held := decideBundle(t, bundle, bundle.Commit)
	if held.Decision != DecisionPass {
		t.Fatalf("held green: %+v", held)
	}
	altered := decideBundle(t, bundle, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if altered.Decision != DecisionFail {
		t.Fatalf("altered SHA passed: %+v", altered)
	}
	found := false
	for _, reason := range altered.Reasons {
		if reason == ReasonSHAMismatch {
			found = true
		}
	}
	if !found {
		t.Fatalf("altered reasons = %v, want sha-mismatch", altered.Reasons)
	}
}

// TestDecisorFailsClosedOnUnreadable proves a missing file and a
// malformed document FAIL instead of passing or crashing.
func TestDecisorFailsClosedOnUnreadable(t *testing.T) {
	missing := Decide(filepath.Join(t.TempDir(), "absent.json"), "")
	if missing.Decision != DecisionFail || len(missing.Reasons) != 1 || missing.Reasons[0] != ReasonFileMissing {
		t.Fatalf("missing file: %+v", missing)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write bad bundle: %v", err)
	}
	malformed := Decide(bad, "")
	if malformed.Decision != DecisionFail {
		t.Fatalf("malformed bundle passed: %+v", malformed)
	}
}

// TestTemplateCarriesNoCertificate proves the unfilled final
// template FAILs: shape without measured data grants nothing, and
// the post-merge execution must fill every field on the frozen SHA.
func TestTemplateCarriesNoCertificate(t *testing.T) {
	decision := Decide(filepath.Join("testdata", "final-template.json"), "")
	if decision.Decision != DecisionFail {
		t.Fatalf("unfilled template passed: %+v", decision)
	}
	found := false
	for _, reason := range decision.Reasons {
		if reason == ReasonOpinionMissing {
			found = true
		}
	}
	if !found {
		t.Fatalf("template reasons = %v, want opinion-missing", decision.Reasons)
	}
}

// TestCLIExitsWithVerdict proves the command exits 0 on PASS, 1 on
// FAIL with the reasons listed, and 2 without a bundle.
func TestCLIExitsWithVerdict(t *testing.T) {
	green := writeBundle(t, greenBundle(t))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-bundle", green}, &stdout, &stderr); code != exitPass {
		t.Fatalf("green exit = %d, want 0 (%s)", code, stdout.String())
	}
	var decision Decision
	if err := json.Unmarshal(stdout.Bytes(), &decision); err != nil || decision.Decision != DecisionPass {
		t.Fatalf("green output = %q, %v", stdout.String(), err)
	}
	bundle := greenBundle(t)
	bundle.ProhibitedActive = true
	red := writeBundle(t, bundle)
	stdout.Reset()
	if code := run([]string{"-bundle", red}, &stdout, &stderr); code != exitFail {
		t.Fatalf("red exit = %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), ReasonProductActive) {
		t.Fatalf("red output misses the reason: %q", stdout.String())
	}
	var usageOut, usageErr bytes.Buffer
	if code := run([]string{}, &usageOut, &usageErr); code != exitUsage {
		t.Fatalf("usage exit = %d, want 2", code)
	}
}
