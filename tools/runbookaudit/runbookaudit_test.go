package main

import (
	"io"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGateRefusesOneFamilyPerRule drives every fixture: the clean tree
// holds, and each mutated tree fails with exactly its own rule. One
// family per rule is what makes a green gate mean the rule still bites.
func TestGateRefusesOneFamilyPerRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		family string
		rules  []string
	}{
		{family: "clean", rules: nil},
		{family: "bad-destructive", rules: []string{"unmarked-destructive"}},
		{family: "bad-host", rules: []string{"unknown-host"}},
		{family: "bad-link", rules: []string{"broken-link"}},
		{family: "bad-anchor", rules: []string{"broken-anchor"}},
		{family: "bad-path", rules: []string{"unknown-path"}},
		{family: "bad-script", rules: []string{"missing-script"}},
		{family: "bad-target", rules: []string{"missing-target"}},
		{family: "bad-compose", rules: []string{"missing-file"}},
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

// TestRealRunbookIsClean pins the shipped document: the gate holds on
// the real tree, so any future procedure that breaks a rule fails here
// instead of reaching an operator.
func TestRealRunbookIsClean(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if findings := Audit(root); len(findings) != 0 {
		t.Fatalf("real runbook findings = %v", findings)
	}
}

// TestCLIExitCodes pins the command contract: clean holds, violations
// list every finding, usage errors name themselves.
func TestCLIExitCodes(t *testing.T) {
	t.Parallel()

	if code := run([]string{"-root", "testdata/clean"}, io.Discard, io.Discard); code != exitOK {
		t.Errorf("clean exit = %d, want %d", code, exitOK)
	}
	if code := run([]string{"-root", "testdata/bad-host"}, io.Discard, io.Discard); code != exitViolation {
		t.Errorf("violation exit = %d, want %d", code, exitViolation)
	}
	if code := run([]string{"-root", "testdata/no-such-dir"}, io.Discard, io.Discard); code != exitViolation {
		t.Errorf("unreadable exit = %d, want %d", code, exitViolation)
	}
	if code := run([]string{"-extra"}, io.Discard, io.Discard); code != exitUsage {
		t.Errorf("usage exit = %d, want %d", code, exitUsage)
	}
}
