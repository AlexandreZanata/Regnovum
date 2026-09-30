package main

import (
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGateRefusesOneFamilyPerRule drives every fixture: the clean tree
// holds, and each mutated tree fails with exactly the pin-drift rule,
// while the malformed declaration fails as bad-toolchain.
func TestGateRefusesOneFamilyPerRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		family string
		rules  []string
	}{
		{family: "clean", rules: nil},
		{family: "drift-go", rules: []string{"pin-drift"}},
		{family: "drift-docker", rules: []string{"pin-drift"}},
		{family: "drift-ts", rules: []string{"pin-drift"}},
		{family: "drift-pg", rules: []string{"pin-drift"}},
		{family: "drift-staticcheck", rules: []string{"pin-drift"}},
	}

	for _, test := range tests {
		t.Run(test.family, func(t *testing.T) {
			t.Parallel()

			findings := Audit(filepath.Join("testdata", test.family))
			if len(findings) != len(test.rules) {
				t.Fatalf("%s: findings = %v, want rules %v", test.family, findings, test.rules)
			}
			for index, rule := range test.rules {
				if findings[index].Rule != rule {
					t.Errorf("%s: finding %d = %q, want rule %q", test.family, index, findings[index].Rule, rule)
				}
			}
		})
	}
}

// TestMalformedDeclarationPoisonsTheVerdict pins the fail-closed rule: a
// declaration missing pins fails as bad-toolchain, and every source
// judged against the missing pins drifts with it — there is no version
// the gate may assume.
func TestMalformedDeclarationPoisonsTheVerdict(t *testing.T) {
	t.Parallel()

	findings := Audit(filepath.Join("testdata", "bad-envelope"))
	seen := map[string]bool{}
	for _, finding := range findings {
		seen[finding.Rule] = true
	}
	if !seen["bad-toolchain"] || !seen["pin-drift"] {
		t.Fatalf("bad-envelope findings = %v, want bad-toolchain plus pin-drift", findings)
	}
}

// TestReportAlwaysReports pins the monitor contract: the report lists
// every pin source plus the empty next set, and exits zero even when a
// pin drifted — incompatibility is reported, never hidden, and no
// lockfile is touched (the fixture tree is read-only to the gate).
func TestReportAlwaysReports(t *testing.T) {
	t.Parallel()

	lines := Report(filepath.Join("testdata", "clean"))
	if len(lines) != 7 {
		t.Fatalf("clean report lines = %v, want six pins plus the empty next", lines)
	}
	for _, line := range lines[:6] {
		if !strings.HasSuffix(line, ": holds") {
			t.Errorf("clean line = %q, want a hold", line)
		}
	}

	drifted := Report(filepath.Join("testdata", "drift-go"))
	found := false
	for _, line := range drifted {
		if strings.Contains(line, "INCOMPATIBLE") {
			found = true
		}
	}
	if !found {
		t.Errorf("drifted report = %v, want an INCOMPATIBLE line", drifted)
	}
	if code := run([]string{"-root", "testdata/drift-go", "-report"}, io.Discard, io.Discard); code != exitOK {
		t.Errorf("report exit on drift = %d, want %d: the monitor never fails", code, exitOK)
	}
}

// TestRealPinsHold pins the shipped tree: the gate holds on the real
// repository, so any future version move fails here instead of reaching
// production undescribed.
func TestRealPinsHold(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if findings := Audit(root); len(findings) != 0 {
		t.Fatalf("real pin findings = %v", findings)
	}
}

// TestCLIExitCodes pins the command contract: clean holds, drift lists
// every finding, usage errors name themselves.
func TestCLIExitCodes(t *testing.T) {
	t.Parallel()

	if code := run([]string{"-root", "testdata/clean"}, io.Discard, io.Discard); code != exitOK {
		t.Errorf("clean exit = %d, want %d", code, exitOK)
	}
	if code := run([]string{"-root", "testdata/drift-pg"}, io.Discard, io.Discard); code != exitViolation {
		t.Errorf("drift exit = %d, want %d", code, exitViolation)
	}
	if code := run([]string{"-root", "testdata/no-such-dir"}, io.Discard, io.Discard); code != exitViolation {
		t.Errorf("unreadable exit = %d, want %d", code, exitViolation)
	}
	if code := run([]string{"-extra"}, io.Discard, io.Discard); code != exitUsage {
		t.Errorf("usage exit = %d, want %d", code, exitUsage)
	}
}
