// Tests of the policy and the baseline: the budget table holds itself, the
// ratchet holds the tree, and the delivered document holds the delivered tree.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheBudgetTableIsCompleteAndFloored holds the policy to itself. A measure
// with no budget in a scope is a hole, and a budget above its floor is a green
// run bought by moving the number.
func TestTheBudgetTableIsCompleteAndFloored(t *testing.T) {
	if violations := judgePolicy(); len(violations) > 0 {
		t.Errorf("the delivered budget table is not the one this gate enforces: %v", violations)
	}

	// The control: a budget moved above its floor is refused.
	loosened := append([]budget{}, budgets...)
	for index := range loosened {
		if loosened[index].Metric == MetricCyclomatic && loosened[index].Scope == ScopeProduct {
			loosened[index].Budget = loosened[index].Floor + 1
		}
	}
	if violations := judgePolicyTable(loosened); !contains(violations, "raising a budget is how a green run would be bought") {
		t.Errorf("a budget above its floor was accepted: %v", violations)
	}

	// The other control: a scope with no budget for a measure is refused.
	missing := []budget{}
	for _, entry := range budgets {
		if entry.Metric == MetricNesting && entry.Scope == ScopeTest {
			continue
		}
		missing = append(missing, entry)
	}
	if violations := judgePolicyTable(missing); !contains(violations, "has no nesting budget") {
		t.Errorf("a scope without a budget for a measure was accepted: %v", violations)
	}
}

// TestTheBaselineIsARatchet drives both directions of the comparison and every
// floor the document is held to.
func TestTheBaselineIsARatchet(t *testing.T) {
	accepted := finding{
		Kind: kindFunction, Path: "internal/wallet/application/send.go", Function: "Send", Line: 40,
		Metric: MetricCyclomatic, Value: 30, Owner: "internal/wallet/application",
	}
	entries := []Entry{entryOf(accepted)}
	if violations := judgeEntries([]finding{accepted}, &Baseline{Schema: baselineSchema, Entries: entries}, baselinePath); len(violations) > 0 {
		t.Errorf("the baseline refused a finding it accepts: %v", violations)
	}

	cases := []struct {
		name     string
		findings []finding
		entries  []Entry
		expect   string
	}{
		{
			name: "a finding nobody accepted",
			findings: []finding{accepted, {
				Kind: kindFunction, Path: "internal/wallet/application/close.go", Function: "Close",
				Metric: MetricNesting, Value: 5, Owner: "internal/wallet/application"}},
			entries: entries,
			expect:  "not in the baseline",
		},
		{
			name:     "a finding that grew",
			findings: []finding{{Kind: kindFunction, Path: "internal/wallet/application/send.go", Function: "Send", Metric: MetricCyclomatic, Value: 31, Owner: "internal/wallet/application"}},
			entries:  entries,
			expect:   "it only goes down",
		},
		{
			name:     "a finding that shrank",
			findings: []finding{{Kind: kindFunction, Path: "internal/wallet/application/send.go", Function: "Send", Metric: MetricCyclomatic, Value: 29, Owner: "internal/wallet/application"}},
			entries:  entries,
			expect:   "lower the line to the measured value",
		},
		{
			name:     "an entry the tree no longer produces",
			findings: nil,
			entries:  entries,
			expect:   "which the tree no longer produces",
		},
		{
			name:    "an entry below the budget it accepts",
			entries: []Entry{{Kind: kindFunction, Path: "internal/wallet/application/send.go", Function: "Send", Metric: MetricCyclomatic, Value: 12, Owner: "internal/wallet/application"}},
			expect:  "is a hole, not a floor",
		},
		{
			name:    "an entry with the wrong owner",
			entries: []Entry{{Kind: kindFunction, Path: "internal/wallet/application/send.go", Function: "Send", Metric: MetricCyclomatic, Value: 30, Owner: "internal/identity/application"}},
			expect:  "a debt nobody can be asked about",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := &Baseline{Schema: baselineSchema, Entries: testCase.entries}
			if violations := judgeDocument(document); len(violations) > 0 {
				t.Fatalf("the document itself was refused before the comparison: %v", violations)
			}
			violations := judgeEntries(testCase.findings, document, baselinePath)
			if !contains(violations, testCase.expect) {
				t.Errorf("expected a violation mentioning %q, got %v", testCase.expect, violations)
			}
		})
	}

	// A block copied inside one file is one path and still a copy: the document
	// has to hold it, and the comparison has to accept it.
	inside := Entry{Kind: kindDuplication, Value: 26, Owner: "internal/transparency/application", Digest: "151ea97ce56e1c59", Paths: []string{"internal/transparency/application/metrics.go"}}
	insideDocument := &Baseline{Schema: baselineSchema, Entries: []Entry{inside}}
	if violations := judgeDocument(insideDocument); len(violations) > 0 {
		t.Errorf("a block copied inside one file was refused by the document rules: %v", violations)
	}
	copied := finding{Kind: kindDuplication, Path: "internal/transparency/application/metrics.go", Line: 19, Metric: MetricDuplication, Value: 26, Owner: "internal/transparency/application", Digest: "151ea97ce56e1c59", Places: []string{"internal/transparency/application/metrics.go:19", "internal/transparency/application/metrics.go:53"}}
	if violations := judgeEntries([]finding{copied}, insideDocument, baselinePath); len(violations) > 0 {
		t.Errorf("the baseline refused the block it accepts: %v", violations)
	}

	// The document's own shape: an unknown schema, an entry that accepts
	// nothing, and the same identity twice are refused before any comparison.
	documents := []struct {
		name     string
		document Baseline
		expect   string
	}{
		{"an unknown schema", Baseline{Schema: 99}, "this gate reads schema"},
		{"an entry that accepts nothing", Baseline{Schema: baselineSchema, Entries: []Entry{{Kind: kindFunction, Path: "internal/wallet/application/send.go", Function: "Send", Metric: MetricCyclomatic, Value: 0, Owner: "internal/wallet/application"}}}, "an accepted finding is one or more"},
		{"the same entry twice", Baseline{Schema: baselineSchema, Entries: []Entry{entries[0], entries[0]}}, "one finding, one line"},
		{"an entry with no owner", Baseline{Schema: baselineSchema, Entries: []Entry{{Kind: kindFunction, Path: "internal/wallet/application/send.go", Function: "Send", Metric: MetricCyclomatic, Value: 30, Owner: "  "}}}, "names no owner"},
		{"an entry whose measure no budget covers", Baseline{Schema: baselineSchema, Entries: []Entry{{Kind: kindFunction, Path: "internal/wallet/application/send.go", Function: "Send", Metric: "vibes", Value: 30, Owner: "internal/wallet/application"}}}, "which no budget covers"},
		{"a file no budget can place", Baseline{Schema: baselineSchema, Entries: []Entry{{Kind: kindFunction, Path: "scripts/tool.go", Function: "Main", Metric: MetricCyclomatic, Value: 30, Owner: "scripts"}}}, "which no budget covers"},
		{"a copied block with no place", Baseline{Schema: baselineSchema, Entries: []Entry{{Kind: kindDuplication, Value: 30, Owner: "internal/wallet/application", Digest: "abc"}}}, "without a digest or without a place"},
		{"a kind this gate does not know", Baseline{Schema: baselineSchema, Entries: []Entry{{Kind: "vibe", Value: 30, Owner: "internal/wallet/application"}}}, `declares the kind "vibe"`},
	}
	for _, testCase := range documents {
		if violations := judgeDocument(&testCase.document); !contains(violations, testCase.expect) {
			t.Errorf("%s: expected a violation mentioning %q, got %v", testCase.name, testCase.expect, violations)
		}
	}
}

// TestTheDeliveredTreeIsInsideItsBaseline reads the committed document and the
// delivered tree and requires the two to agree. The gate makes the same
// judgement on every merge; here it fails in the unit suite, next to the code
// that changed.
func TestTheDeliveredTreeIsInsideItsBaseline(t *testing.T) {
	withRoot(t, func() {
		measured, err := measureTree()
		if err != nil {
			t.Fatalf("measure the delivered tree: %v", err)
		}
		if len(measured.Findings) == 0 {
			t.Fatal("the delivered tree has no finding at all: a gate that measured nothing would prove nothing")
		}
		if len(measured.Generated) == 0 {
			t.Fatal("no file of the delivered tree was excluded by provenance")
		}
		baseline, violations := loadBaseline(baselinePath)
		if len(violations) > 0 {
			t.Fatalf("the delivered baseline is not readable: %v", violations)
		}
		for _, violation := range judgeEntries(measured.Findings, baseline, baselinePath) {
			t.Errorf("delivered tree: %s", violation)
		}
	})
}

// withRoot runs a function with the working directory at the repository root,
// which is where the gate's own paths are written from.
func withRoot(t *testing.T, run func()) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("read the working directory: %v", err)
	}
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatalf("enter the repository root: %v", err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("return to the package directory: %v", err)
		}
	}()
	run()
}

func contains(violations []string, expected string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, expected) {
			return true
		}
	}
	return false
}
