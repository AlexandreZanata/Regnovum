// Command complexityaudit is the gate of P23-T03: budgets per function for
// cyclomatic complexity, nesting, parameters, size and non-generated
// duplication.
//
// It refuses, naming the rule:
//
//   - a function over the declared budget of its measure and its scope, unless
//     the baseline accepts it — and the baseline is a ratchet, so it accepts the
//     number that was measured once and nothing above it;
//   - a copied block shorter than the declared window, or longer than the
//     baseline accepts: duplication is measured over the code that runs, not
//     over the tree, and generated code is out of the corpus by provenance;
//   - a budget looser than its floor: the numbers can be argued with in a
//     review, but not argued into meaninglessness;
//   - a baseline entry below the budget it accepts, wrong about its owner, or
//     outliving the finding it accepted;
//   - a Go file the gate cannot classify or cannot read: a file no budget
//     judges is a hole, and a hole is what a gate exists to prevent.
//
// Two things about the measurement are worth stating where the numbers are read:
//
//   - generated code is excluded by **provenance**, never by directory. The
//     marker of the Go toolchain ("Code generated ... DO NOT EDIT.") is read
//     where the toolchain reads it: in the comments before the package clause.
//     The generators of this repository carry that text inside a string, and a
//     gate that scanned the bytes would exclude the very tools that must be
//     judged while looking like it had done the right thing.
//   - the fixtures under testdata/ are exercised on purpose. The Go toolchain
//     skips testdata in every ./... pattern, so the proof that each rule still
//     bites has to ask for the fixture by name — the same shape the static
//     analysis gate of P23-T02 uses, for the same reason.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// baselinePath is where the accepted findings live. It is a constant because two
// callers asking for "the baseline" have to be asking for the same file.
const baselinePath = "quality/complexity-baseline.json"

// family is one rule with the fixture that proves it still bites.
type family struct {
	Name   string // what the family is, as a person reads it
	Metric string // the measure the fixture has to cross
	Target string // the fixture directory
	Reason string // why the family is part of the gate
}

// families is the executable map of the rules. Every family has a fixture that
// must be refused by the measure it names: a rule that stopped biting has to
// fail here, by name, instead of disappearing from a green run.
var families = []family{
	{
		Name: "function with too many decisions", Metric: MetricCyclomatic,
		Target: "tools/complexityaudit/testdata/complex",
		Reason: "uma função cujo número de caminhos passa do orçamento é onde a revisão deixa de ser leitura",
	},
	{
		Name: "function nested too deep", Metric: MetricNesting,
		Target: "tools/complexityaudit/testdata/nested",
		Reason: "o aninhamento é o termo que faz a carga cognitiva crescer sem que a contagem de decisões mude",
	},
	{
		Name: "function with too many parameters", Metric: MetricParameters,
		Target: "tools/complexityaudit/testdata/parameters",
		Reason: "a chamada passa a ter posições em vez de nomes",
	},
	{
		Name: "function longer than a screen", Metric: MetricLines,
		Target: "tools/complexityaudit/testdata/long",
		Reason: "o tamanho que uma pessoa reconhece abrindo o arquivo",
	},
	{
		Name: "function with too many statements", Metric: MetricStatements,
		Target: "tools/complexityaudit/testdata/long",
		Reason: "o tamanho que não se move com formatação",
	},
	{
		Name: "block copied into more than one place", Metric: MetricDuplication,
		Target: "tools/complexityaudit/testdata/duplicated",
		Reason: "a regra escrita duas vezes é a que diverge na terceira mudança",
	},
}

// provenanceProbe is one proof about the exclusion itself, in both directions:
// the marked file has to be excluded, and the file that only mentions the marker
// has to be judged. A provenance rule proved in one direction only is a rule
// that could be excluding the wrong files while looking correct.
type provenanceProbe struct {
	Name     string
	Target   string
	Excluded bool
}

var provenanceProbes = []provenanceProbe{
	{Name: "a generated file is excluded by its marker", Target: "tools/complexityaudit/testdata/generated", Excluded: true},
	{Name: "a file that only mentions the marker is judged", Target: "tools/complexityaudit/testdata/decoy", Excluded: false},
}

func main() {
	root := flag.String("root", ".", "the repository root")
	print := flag.Bool("print-findings", false, "print the baseline document this tree produces and stop (a human commits it; this gate never rewrites the file)")
	flag.Parse()
	if err := run(*root, *print); err != nil {
		fmt.Fprintf(os.Stderr, "complexityaudit: %v\n", err)
		os.Exit(1)
	}
}

func run(root string, print bool) error {
	if err := os.Chdir(root); err != nil {
		return fmt.Errorf("enter %s: %w", root, err)
	}
	if violations := judgePolicy(); len(violations) > 0 {
		return fmt.Errorf("the budget policy is not the one this gate enforces:\n  - %s", strings.Join(violations, "\n  - "))
	}
	if err := proveFamilies(); err != nil {
		return err
	}

	measured, err := measureTree()
	if err != nil {
		return err
	}
	if measured.Files == 0 {
		return fmt.Errorf("the walk judged no file: a gate that measured nothing is a gate that refuses nothing")
	}
	if len(measured.Generated) == 0 {
		return fmt.Errorf("no file was excluded by provenance: the marker this gate reads is the Go toolchain's, and a tree with no generated file means the marker stopped matching rather than that the generator stopped running")
	}
	// A file the gate cannot place or cannot read is a file no budget judges:
	// skipping it would leave a hole exactly where a new generator could put a
	// file nobody measures.
	if len(measured.Unclassified) > 0 {
		sort.Strings(measured.Unclassified)
		return fmt.Errorf("the gate has no scope for %s: a Go file outside internal/, cmd/ and tools/ is a file no budget judges", strings.Join(measured.Unclassified, ", "))
	}
	if len(measured.Unparsable) > 0 {
		paths := []string{}
		for path := range measured.Unparsable {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		return fmt.Errorf("the gate cannot read %s: a file whose complexity nobody can measure has no budget at all", strings.Join(paths, ", "))
	}

	if print {
		entries := make([]Entry, 0, len(measured.Findings))
		for _, finding := range measured.Findings {
			entries = append(entries, entryOf(finding))
		}
		sort.Slice(entries, func(one, other int) bool { return entries[one].key() < entries[other].key() })
		encoded := render(&Baseline{Schema: baselineSchema, Note: baselineNote, Entries: entries})
		if _, err := os.Stdout.WriteString(encoded); err != nil {
			return err
		}
		return nil
	}

	baseline, baselineViolations := loadBaseline(baselinePath)
	if len(baselineViolations) > 0 {
		return fmt.Errorf("the baseline is not readable:\n  - %s", strings.Join(baselineViolations, "\n  - "))
	}
	violations := judgeEntries(measured.Findings, baseline, baselinePath)

	report(measured, baseline)
	if len(violations) > 0 {
		return fmt.Errorf("the complexity gate refused the tree:\n  - %s", strings.Join(violations, "\n  - "))
	}
	fmt.Printf("complexityaudit: OK — no finding outside the baseline, and the baseline matches the tree\n")
	return nil
}

// judgePolicy holds the budget table to itself: every measure in every scope has
// a budget, and no budget is above its floor.
func judgePolicy() []string { return judgePolicyTable(budgets) }

// judgePolicyTable is the judgement over a table, which is how the test drives
// it: a policy that can only be judged as the delivered one is a policy whose
// refusal nobody can exercise.
func judgePolicyTable(table []budget) []string {
	violations := []string{}
	for _, scope := range []string{ScopeProduct, ScopeTooling, ScopeTest} {
		for _, metric := range append(append([]string{}, measuredMetrics...), MetricDuplication) {
			declared, known := budgetForTable(table, scope, metric)
			if !known {
				if metric == MetricDuplication {
					continue
				}
				violations = append(violations, fmt.Sprintf("the %s scope has no %s budget: a measure nobody is held to is a measure nobody has", scope, metric))
				continue
			}
			if declared.Budget > declared.Floor {
				violations = append(violations, fmt.Sprintf(
					"the %s budget of %s is %d and the floor of this gate is %d: raising a budget is how a green run would be bought, and the floor is what stops it",
					metric, scope, declared.Budget, declared.Floor))
			}
			if declared.Budget < 1 {
				violations = append(violations, fmt.Sprintf("the %s budget of %s is %d", metric, scope, declared.Budget))
			}
		}
	}
	return violations
}

// proveFamilies runs every fixture and requires the measure it names to refuse
// it. It also proves the provenance rule in both directions.
func proveFamilies() error {
	for _, entry := range families {
		measured, err := measureDirectory(entry.Target)
		if err != nil {
			return err
		}
		found := false
		for _, finding := range measured.Findings {
			if finding.Metric == entry.Metric {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf(
				"the family %q is no longer refused: measuring %s produced %d finding(s) and this gate requires one naming %s",
				entry.Name, entry.Target, len(measured.Findings), entry.Metric)
		}
	}
	for _, probe := range provenanceProbes {
		measured, err := measureDirectory(probe.Target)
		if err != nil {
			return err
		}
		excluded := len(measured.Generated) > 0
		if excluded != probe.Excluded {
			return fmt.Errorf(
				"%s: measuring %s excluded %d file(s) by provenance and this gate requires %v",
				probe.Name, probe.Target, len(measured.Generated), probe.Excluded)
		}
		if !probe.Excluded && len(measured.Findings) == 0 {
			return fmt.Errorf("%s: %s produced no finding, so the probe proves nothing about a judged file", probe.Name, probe.Target)
		}
	}
	return nil
}

// report prints everything this run measured: the corpus, the budgets, what the
// baseline accepts, and the gap the gate declares. A number that is not printed
// is a number nobody reviews.
func report(measured measurement, baseline *Baseline) {
	fmt.Printf("complexityaudit: judged %d file(s) and %d function(s); %d file(s) excluded by provenance (the Go marker)\n",
		measured.Files, measured.Functions, len(measured.Generated))
	for _, scope := range []string{ScopeProduct, ScopeTooling, ScopeTest} {
		parts := []string{}
		for _, metric := range measuredMetrics {
			declared, _ := budgetFor(scope, metric)
			parts = append(parts, fmt.Sprintf("%s ≤ %d", metric, declared.Budget))
		}
		parts = append(parts, fmt.Sprintf("%s ≥ %d line(s)", MetricDuplication, duplicationWindow))
		fmt.Printf("complexityaudit: budget %s — %s\n", scope, strings.Join(parts, ", "))
	}
	fmt.Printf("complexityaudit: %d finding(s) accepted by %d line(s) of %s\n", len(measured.Findings), len(baseline.Entries), baselinePath)
	accepted := map[string]Entry{}
	for _, entry := range baseline.Entries {
		accepted[entry.key()] = entry
	}
	for _, finding := range measured.Findings {
		entry, known := accepted[finding.identity()]
		owner := finding.Owner
		if known {
			owner = entry.Owner
		}
		fmt.Printf("complexityaudit:   accepted %s (owner %s)\n", finding.String(), owner)
	}
	for _, entry := range families {
		fmt.Printf("complexityaudit: family %q — refused by %s, fixture %s\n", entry.Name, entry.Metric, entry.Target)
	}
	for _, probe := range provenanceProbes {
		fmt.Printf("complexityaudit: provenance — %s (%s)\n", probe.Name, probe.Target)
	}
	fmt.Printf("complexityaudit: not enforced by this gate — %s\n", unenforced)
	if encoded, err := json.Marshal(measured.Unparsable); err == nil && len(measured.Unparsable) > 0 {
		fmt.Printf("complexityaudit: unreadable files — %s\n", encoded)
	}
}
