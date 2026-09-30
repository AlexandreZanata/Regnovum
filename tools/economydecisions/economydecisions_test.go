package main

import (
	"io"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGateGreenAndRedFamilies drives every fixture: the green tree
// holds, and each mutated tree fails with exactly its own rules. One
// family per rule is what makes a green gate mean the rule still
// bites; the addendum family proves the pending block survives even
// when the addendum claims the approval.
func TestGateGreenAndRedFamilies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		family string
		rules  []string
	}{
		{family: "green", rules: nil},
		{family: "red-pending", rules: []string{"critical-pending"}},
		{family: "red-price", rules: []string{"price-unapproved"}},
		{family: "red-time", rules: []string{"ambiguous-time"}},
		{family: "red-threat", rules: []string{"threat-without-control"}},
		{family: "red-offer", rules: []string{"prohibited-offer"}},
		{family: "red-addendum", rules: []string{"critical-pending", "addendum-bypass"}},
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

// TestRealDocsStayBlocked pins the shipped state: critical decisions are
// still pending and prices unapproved, so the gate refuses financial
// implementation. Time, threats and offers already hold.
func TestRealDocsStayBlocked(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))

	seen := map[string]bool{}
	for _, finding := range Audit(root) {
		seen[finding.Rule] = true
	}
	for _, rule := range []string{"critical-pending", "price-unapproved"} {
		if !seen[rule] {
			t.Errorf("real docs do not report %q: a pending block nobody names is a block nobody enforces", rule)
		}
	}
	for _, rule := range []string{"ambiguous-time", "threat-without-control", "prohibited-offer"} {
		if seen[rule] {
			t.Errorf("real docs report %q: the delivered time, threat and offer contracts hold", rule)
		}
	}
}

// TestCLIExitCodes pins the command contract: green holds, violations
// list every finding, usage errors name themselves.
func TestCLIExitCodes(t *testing.T) {
	t.Parallel()

	if code := run([]string{"-root", "testdata/green"}, io.Discard, io.Discard); code != exitOK {
		t.Errorf("green exit = %d, want %d", code, exitOK)
	}
	if code := run([]string{"-root", "testdata/red-price"}, io.Discard, io.Discard); code != exitViolation {
		t.Errorf("violation exit = %d, want %d", code, exitViolation)
	}
	if code := run([]string{"-root", "testdata/no-such-dir"}, io.Discard, io.Discard); code != exitViolation {
		t.Errorf("unreadable exit = %d, want %d", code, exitViolation)
	}
	if code := run([]string{"-extra"}, io.Discard, io.Discard); code != exitUsage {
		t.Errorf("usage exit = %d, want %d", code, exitUsage)
	}
}
