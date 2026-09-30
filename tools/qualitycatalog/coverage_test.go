package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// coverageMutation is one way the coverage check can be defeated. Each case
// names the code it must make fire, and the same table is what proves the
// bidirectional property the loader's own tests prove: a code nobody has seen
// refuse anything guards nothing, and a test that expects a code the tool no
// longer declares would pass against a program that has moved on.
type coverageMutation struct {
	name   string
	code   string
	mutate func(catalog *Catalog, rules *declared)
}

// coverageFixture is the smallest pair that stands: one requirement with its
// entry, one critical threat with its entry, each filed under the document that
// declares it.
func coverageFixture() (Catalog, declared) {
	catalog := Catalog{
		SchemaVersion: SchemaVersion,
		Rules: []Rule{
			{ID: "QUAL-REQ-POS-01", Source: RequirementsDocument},
			{ID: "QUAL-THR-WAL-01", Source: ThreatModelDocument},
		},
	}
	rules := declared{
		requirements: map[string]string{"REQ-POS-01": RequirementsDocument},
		threats:      map[string]string{"THR-WAL-01": ThreatModelDocument},
	}
	return catalog, rules
}

// fingerprint renders the pair so a mutation can prove it changed something. A
// mutation that silently changes nothing would make the case pass for the wrong
// reason, which is the one way a table of defects lies.
func fingerprint(catalog Catalog, rules declared) string {
	var builder strings.Builder
	for _, rule := range catalog.Rules {
		fmt.Fprintf(&builder, "rule %s of %s\n", rule.ID, rule.Source)
	}
	for id, path := range rules.requirements {
		fmt.Fprintf(&builder, "requirement %s of %s\n", id, path)
	}
	for id, path := range rules.threats {
		fmt.Fprintf(&builder, "threat %s of %s\n", id, path)
	}
	return builder.String()
}

// mutatedCoverage applies a mutation and refuses to let it be a no-op.
func mutatedCoverage(t *testing.T, mutate func(catalog *Catalog, rules *declared)) (Catalog, declared) {
	t.Helper()
	catalog, rules := coverageFixture()
	before := fingerprint(catalog, rules)
	mutate(&catalog, &rules)
	if fingerprint(catalog, rules) == before {
		t.Fatal("the mutation changed nothing: a case that does not move the pair proves nothing")
	}
	return catalog, rules
}

// TestTheCompletePairIsAccepted is the control. Without it, a check that
// refused everything would pass every mutation below.
func TestTheCompletePairIsAccepted(t *testing.T) {
	catalog, rules := coverageFixture()
	if violations := CheckCoverage(catalog, rules); len(violations) > 0 {
		t.Fatalf("the complete pair is refused: %v", violations)
	}
}

// TestEveryCoverageDefectIsRefused drives one mutation per defect.
func TestEveryCoverageDefectIsRefused(t *testing.T) {
	for _, testCase := range coverageMutations() {
		t.Run(testCase.name, func(t *testing.T) {
			catalog, rules := mutatedCoverage(t, testCase.mutate)
			violations := CheckCoverage(catalog, rules)
			if !hasCode(violations, testCase.code) {
				t.Fatalf("`%s` was not refused with `%s`: %v", testCase.name, testCase.code, violations)
			}
		})
	}
}

// coverageMutations is the table of the ways completeness can be lost.
func coverageMutations() []coverageMutation {
	return []coverageMutation{
		{
			name: "an entry the documents do not declare",
			code: codeCoverageOrphan,
			mutate: func(catalog *Catalog, rules *declared) {
				catalog.Rules = append(catalog.Rules, Rule{ID: "QUAL-REQ-ZZZ-99", Source: RequirementsDocument})
			},
		},
		{
			name: "an entry filed under the document that does not declare it",
			code: codeCoverageOrphan,
			mutate: func(catalog *Catalog, rules *declared) {
				catalog.Rules[1].Source = RequirementsDocument
			},
		},
		{
			name: "a requirement with no entry",
			code: codeCoverageMissing,
			mutate: func(catalog *Catalog, rules *declared) {
				rules.requirements["REQ-POS-02"] = RequirementsDocument
			},
		},
		{
			name: "a critical threat with no entry",
			code: codeCoverageMissing,
			mutate: func(catalog *Catalog, rules *declared) {
				rules.threats["THR-WAL-02"] = ThreatModelDocument
			},
		},
		{
			name: "a high threat with no entry",
			code: codeCoverageMissing,
			mutate: func(catalog *Catalog, rules *declared) {
				rules.threats["THR-AUTH-04"] = ThreatModelDocument
			},
		},
	}
}

// TestTheDeliveredCatalogCoversTheDeliveredDocuments is the test that matters
// most: it holds the catalog in the tree to the documents in the same tree. A
// requirement added to the matrix and not mapped here fails this test, which is
// the only thing that keeps "100% of the MVP requirements have an entry" from
// being a sentence in a report.
func TestTheDeliveredCatalogCoversTheDeliveredDocuments(t *testing.T) {
	root := filepath.Join("..", "..")
	catalog, violations := ReadCatalog(root, filepath.Join("quality", "catalog.json"))
	if len(violations) > 0 {
		t.Fatalf("the delivered catalog is refused by the loader: %v", violations)
	}
	rules, violations := declaredRules(root)
	if len(violations) > 0 {
		t.Fatalf("the delivered documents are unreadable: %v", violations)
	}
	if len(rules.requirements) == 0 || len(rules.threats) == 0 {
		t.Fatalf("the documents declare %d requirement(s) and %d critical or high threat(s): a check with nothing to compare accepts everything",
			len(rules.requirements), len(rules.threats))
	}
	if violations := CheckCoverage(catalog, rules); len(violations) > 0 {
		t.Fatalf("the delivered catalog does not cover the delivered documents: %v", violations)
	}
	if len(catalog.Rules) != len(rules.requirements)+len(rules.threats) {
		t.Fatalf("the catalog holds %d rule(s) for %d declared rule(s): the two lists are not the same set",
			len(catalog.Rules), len(rules.requirements)+len(rules.threats))
	}
}

// TestAThreatRowWithoutASeverityIsRefused covers the reading of the severity
// itself. The threat model is read by value rather than by column position, so
// the check has to know what to do with a row that states no severity at all:
// it refuses it, because "does this threat demand an entry" would otherwise be
// answered by silence.
func TestAThreatRowWithoutASeverityIsRefused(t *testing.T) {
	root := t.TempDir()
	writeDocument(t, root, RequirementsDocument, "| ID | Descrição |\n|---|---|\n| **REQ-POS-01** | uma regra |\n")
	writeDocument(t, root, ThreatModelDocument, "| ID | Ameaça | Severidade |\n|---|---|---|\n| **THR-WAL-01** | gasto duplo |  |\n")

	_, violations := declaredRules(root)
	if !hasCode(violations, codeSeverityUnreadable) {
		t.Fatalf("a threat row with no severity was not refused with `%s`: %v", codeSeverityUnreadable, violations)
	}
}

// TestTheRuleSetIsReadByValueNotByPosition is the regression the delivered
// threat model earned: one of its rows has no actor cell, so every cell after
// the missing one sits a place to the left. A reader that trusted the column
// position would take that row's mitigation for its severity and the threat
// would drop out of the rule set without a word.
func TestTheRuleSetIsReadByValueNotByPosition(t *testing.T) {
	root := t.TempDir()
	writeDocument(t, root, RequirementsDocument, "| ID | Descrição |\n|---|---|\n| **REQ-POS-01** | uma regra |\n")
	writeDocument(t, root, ThreatModelDocument,
		"| ID | Ameaça | STRIDE | Ator | Severidade | Mitigação |\n|---|---|---|---|---|---|\n"+
			"| **THR-AUTH-01** | sequestro de sessão | Spoofing | A-01 | **Crítica** | cookies opacos |\n"+
			"| **THR-AUTH-04** | replay de token | Tampering | **Alta** | token CSPRNG de uso único | hash no banco |\n")

	rules, violations := declaredRules(root)
	if len(violations) > 0 {
		t.Fatalf("the rows were refused: %v", violations)
	}
	for _, id := range []string{"THR-AUTH-01", "THR-AUTH-04"} {
		if _, ok := rules.threats[id]; !ok {
			t.Fatalf("`%s` is not in the rule set: a row offset by a missing cell was read by position", id)
		}
	}
}

// TestARowWhoseSeverityIsNotTheDangerousOneIsNotDemanded keeps the other side
// honest: a Média row is read as a severity, and a severity that is not Crítica
// or Alta is not part of the set the catalog must cover. Without this, the
// vocabulary could quietly mean "every threat" and the table below would still
// pass.
func TestARowWhoseSeverityIsNotTheDangerousOneIsNotDemanded(t *testing.T) {
	root := t.TempDir()
	writeDocument(t, root, RequirementsDocument, "| ID | Descrição |\n|---|---|\n| **REQ-POS-01** | uma regra |\n")
	writeDocument(t, root, ThreatModelDocument,
		"| ID | Ameaça | Severidade |\n|---|---|---|\n| **THR-AUTH-02** | força bruta | **Média** |\n")

	rules, violations := declaredRules(root)
	if len(violations) > 0 {
		t.Fatalf("the row was refused: %v", violations)
	}
	if _, ok := rules.threats["THR-AUTH-02"]; ok {
		t.Fatal("a Média threat was demanded of the catalog: the set is meant to be the dangerous threats, not every threat")
	}
}

// writeDocument writes one document into a synthetic root, creating the
// directories it needs. The real tree is the subject of one test; every other
// test builds what it judges, so that a document defect found tomorrow is a
// case here and not a change in the checkout.
func writeDocument(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("the fixture root is not writable: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("the fixture document is not writable: %v", err)
	}
}
