// Tests of the executable rule catalog (P21-T02).
//
// The catalog is a document that makes claims about the repository — this
// document states, this package owns it, this test proves it — and every claim
// is the kind that keeps looking true after the code moves. The loader is only
// worth what it refuses, so the tests below hold three properties, and the
// first one alone would be worthless:
//
//   - the complete fixture passes, so a catalogue of real rules is admitted;
//   - one mutation per violation code makes that code fire. Each mutation edits
//     the fixture by field name and fails when the field it means to change is
//     not there, so no case can pass by mutating nothing;
//   - every code the loader declares is named by at least one mutation. A code
//     nobody has seen refuse anything is a green light with a comment.
//
// The fixture tree is a small checkout of its own — the documents it cites, the
// package it names and the two test files whose functions it claims — instead
// of this repository, so a mutation breaks exactly one thing.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mutationCase is one defect: the code it has to make the loader emit, and the
// document that carries the defect.
type mutationCase struct {
	name  string
	code  string
	build func(t *testing.T) []byte
}

// fixtureDocument is the complete catalog: a Q0 rule that fills every slot its
// class demands, and a Q2 rule that fills the two every rule shares and writes
// down why the limit does not apply. It is built as a mutable map rather than a
// string because a text fragment that appears twice is how a mutation edits the
// wrong rule and still passes.
func fixtureDocument() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"rules": []map[string]any{
			{
				"id":          "QUAL-ORDERS-PLACEMENT",
				"source":      "docs/SPEC.md#placement",
				"description": "A placement outside the arena boundary is refused.",
				"risk":        "Q0",
				"modules":     []any{"internal/fixture/service"},
				"actors":      []any{"account"},
				"states":      []any{"open", "placed"},
				"tests": map[string]any{
					"positive":      []any{"internal/fixture/service/rules_test.go::TestPlacementInsideIsAccepted"},
					"negative":      []any{"internal/fixture/service/rules_test.go::TestPlacementOutsideIsRefused"},
					"limit":         []any{"internal/fixture/service/rules_test.go::TestPlacementAtTheEdgeIsAccepted"},
					"authorization": []any{"internal/fixture/service/rules_test.go::TestPlacementNeedsTheOwner"},
					"concurrency":   []any{"internal/fixture/service/rules_test.go::TestTwoPlacementsDoNotOversell"},
					"idempotency":   []any{"internal/fixture/service/rules_test.go::TestReplayedPlacementDoesNotCountTwice"},
				},
				"authorization":  "Only the owner of the position may place it.",
				"concurrency":    "Two placements of the same position are serialized by the ledger.",
				"idempotency":    "A replayed placement with the same key returns the first outcome.",
				"security":       "The boundary is checked server-side, never by the client.",
				"privacy":        "The position is game state and holds no personal data.",
				"evidence":       []any{"docs/EVIDENCE.md"},
				"not_applicable": map[string]any{},
			},
			{
				"id":          "QUAL-ACCOUNT-CONFIRMATION",
				"source":      "docs/SPEC.md",
				"description": "A confirmation link is consumed once.",
				"risk":        "Q2",
				"modules":     []any{"internal/fixture/service"},
				"actors":      []any{"visitor"},
				"states":      []any{"pending", "confirmed"},
				"tests": map[string]any{
					"positive":      []any{"internal/fixture/service/orders_test.go::TestConfirmationSucceeds"},
					"negative":      []any{"internal/fixture/service/orders_test.go::TestSecondConfirmationIsRefused"},
					"limit":         []any{},
					"authorization": []any{},
					"concurrency":   []any{},
					"idempotency":   []any{},
					"failure":       []any{},
				},
				"authorization": "The link itself is the authorization; no session is required.",
				"concurrency":   "Not applicable: a single-use token has one consumer.",
				"idempotency":   "The second use is the refusal the rule describes.",
				"security":      "The token is single-use and stored hashed.",
				"privacy":       "The mailbox is never copied into the catalog.",
				"evidence":      []any{"docs/POLICY.md"},
				"not_applicable": map[string]any{
					"limit": "A single-use token has no boundary between accepted and refused.",
				},
			},
		},
	}
}

// fixtureTree is the small checkout the fixture catalog is judged against: the
// three documents it cites, the package it names, and the functions it claims.
// It is not this repository, so a mutation changes one thing and nothing else.
func fixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("the fixture tree cannot be built: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("the fixture tree cannot be built: %v", err)
		}
	}
	write("docs/SPEC.md", "# Especificacao (fixture)\n\n## Placement\n\nA colocacao fora da fronteira e recusada.\n")
	write("docs/EVIDENCE.md", "# Evidencia (fixture)\n\nO teste cita esta pagina.\n")
	write("docs/POLICY.md", "# Politica (fixture)\n\nO link de confirmacao e de uso unico.\n")
	// The pair the coverage half of the gate compares against. The fixture
	// catalog cites the spec rather than these documents, so this table declares
	// no row: what the command test holds is that the command reads the pair and
	// judges the catalog against it, while rows — present, missing and
	// unreadable — are exercised by the coverage tests.
	write("docs/REQUIREMENTS.md", "# Matriz de requisitos (fixture)\n\n## 1. Visao geral\n\nNenhum requisito nesta tabela.\n")
	write("docs/THREAT_MODEL.md", "# Modelo de ameacas (fixture)\n\n## 5. Analise STRIDE\n\nNenhuma ameaca nesta tabela.\n")
	write("internal/fixture/service/rules_test.go", `package service

import "testing"

func TestPlacementInsideIsAccepted(t *testing.T) {}
func TestPlacementOutsideIsRefused(t *testing.T) {}
func TestPlacementAtTheEdgeIsAccepted(t *testing.T) {}
func TestPlacementNeedsTheOwner(t *testing.T) {}
func TestTwoPlacementsDoNotOversell(t *testing.T) {}
func TestReplayedPlacementDoesNotCountTwice(t *testing.T) {}
`)
	write("internal/fixture/service/orders_test.go", `package service

import "testing"

func TestConfirmationSucceeds(t *testing.T) {}
func TestSecondConfirmationIsRefused(t *testing.T) {}
`)
	return root
}

// TestTheCompleteFixtureIsAccepted is the control. Without it every mutation
// below could pass because the fixture was already being refused, and the
// catalogue the phase asks for would be a document no real rule can satisfy.
func TestTheCompleteFixtureIsAccepted(t *testing.T) {
	catalog, violations := Check(encode(t, fixtureDocument()), Tree{Root: fixtureTree(t)})
	if len(violations) > 0 {
		t.Fatalf("the complete fixture is refused: %v", violations)
	}
	if catalog.SchemaVersion != SchemaVersion {
		t.Errorf("the accepted catalog reports version %d, want %d", catalog.SchemaVersion, SchemaVersion)
	}
	if len(catalog.Rules) != 2 {
		t.Fatalf("the accepted catalog holds %d rule(s), want 2", len(catalog.Rules))
	}
	if catalog.Rules[0].ID != "QUAL-ORDERS-PLACEMENT" {
		t.Errorf("the accepted catalog read `%s` as the first rule", catalog.Rules[0].ID)
	}
}

// TestEveryDefectIsRefused runs one mutation per violation code. The mutation
// is applied to the complete fixture, so the code that fires is the code for
// the defect and not for a fixture that was broken in some other way.
func TestEveryDefectIsRefused(t *testing.T) {
	for _, testCase := range catalogMutations() {
		t.Run(testCase.name, func(t *testing.T) {
			_, violations := Check(testCase.build(t), Tree{Root: fixtureTree(t)})
			if !hasCode(violations, testCase.code) {
				t.Fatalf("the defect was not refused with `%s`; the codes that fired were %v", testCase.code, codesOf(violations))
			}
		})
	}
}

// TestAMissingCatalogIsRefused is the refusal that has no document to mutate: a
// catalog that is not there. An absent file and an empty one both verify
// nothing, and a gate that treats absence as success is a gate that disappears
// with the file it guards.
func TestAMissingCatalogIsRefused(t *testing.T) {
	_, violations := ReadCatalog(t.TempDir(), filepath.Join("quality", "catalog.json"))
	if !hasCode(violations, codeCatalogMissing) {
		t.Fatalf("a checkout without the catalog was not refused with `%s`: %v", codeCatalogMissing, violations)
	}
}

// TestTheCommandJudgesACatalog runs the command the way a pipeline does: over a
// catalog on disk, once as it stands and once with one field emptied. The exit
// status is the contract, so the contract is what the test reads.
func TestTheCommandJudgesACatalog(t *testing.T) {
	root := fixtureTree(t)
	path := filepath.Join(root, "quality", "catalog.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("the catalog cannot be written: %v", err)
	}
	if err := os.WriteFile(path, encode(t, fixtureDocument()), 0o644); err != nil {
		t.Fatalf("the catalog cannot be written: %v", err)
	}

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if status := run([]string{"-root", root}, stdout, stderr); status != exitOK {
		t.Fatalf("the command refused the complete catalog with status %d: %s", status, stderr)
	}
	if !strings.Contains(stdout.String(), "2 rule(s)") {
		t.Errorf("the command reported `%s`, and the catalog holds two rules", stdout.String())
	}

	broken := changed(t, func(t *testing.T, document map[string]any) {
		set(t, rulesOf(t, document)[0], "description", "")
	})
	if err := os.WriteFile(path, broken, 0o644); err != nil {
		t.Fatalf("the catalog cannot be written: %v", err)
	}
	stderr.Reset()
	if status := run([]string{"-root", root}, &bytes.Buffer{}, stderr); status != exitViolation {
		t.Fatalf("the command accepted a catalog with a blank description: status %d, stderr %s", status, stderr)
	}
	if !strings.Contains(stderr.String(), codeFieldEmpty) {
		t.Errorf("the refusal does not name `%s`: %s", codeFieldEmpty, stderr)
	}
}

// codesWithNoDocumentToMutate are the codes proved by a test that cannot run in
// front of a fixture: a document that is absent has nothing to change, so the
// proof is an empty tree instead of a mutation. Naming them here keeps the
// coverage check below honest — the exception is written down, not skipped.
var codesWithNoDocumentToMutate = map[string]string{
	codeCatalogMissing: "TestAMissingCatalogIsRefused",
	codeSchemaMissing:  "TestAMissingSchemaIsRefused",
}

// TestEveryDeclaredCodeHasAMutation is the check that runs in both directions.
// A code the loader declares and no test makes fire is a rule nobody has seen
// work; a code a test expects and the loader no longer declares is a test that
// would pass against a program that has moved on.
func TestEveryDeclaredCodeHasAMutation(t *testing.T) {
	declared := declaredCodes(t)
	proven := map[string]string{}
	for _, testCase := range catalogMutations() {
		proven[testCase.code] = "TestEveryDefectIsRefused"
	}
	for _, testCase := range schemaMutations() {
		proven[testCase.code] = "TestEverySchemaRuleIsFalsified"
	}
	for _, testCase := range coverageMutations() {
		proven[testCase.code] = "TestEveryCoverageDefectIsRefused"
	}
	proven[codeSeverityUnreadable] = "TestAThreatRowWithoutASeverityIsRefused"
	for code, test := range codesWithNoDocumentToMutate {
		proven[code] = test
	}

	for code, source := range declared {
		if _, ok := proven[code]; !ok {
			t.Errorf("`%s` is declared in %s and no test makes it fire: a code nobody has seen refuse anything guards nothing", code, source)
		}
	}
	for code, test := range proven {
		if _, ok := declared[code]; !ok {
			t.Errorf("`%s` is expected by %s and the loader does not declare it", code, test)
		}
	}
}

// declaredCodes reads the violation codes out of the loader's own sources
// instead of listing them again here: a list repeated in the test is a list
// that drifts, and the point of this check is that it cannot.
func declaredCodes(t *testing.T) map[string]string {
	t.Helper()
	pattern := regexp.MustCompile(`(?m)^\s*code[A-Za-z]+\s*=\s*"([^"]+)"`)
	declared := map[string]string{}
	for _, source := range []string{"catalog.go", "schema.go", "coverage.go"} {
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("the loader source %s is unreadable: %v", source, err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
			declared[match[1]] = source
		}
	}
	return declared
}

// catalogMutations is one defect per code of the loader.
func catalogMutations() []mutationCase {
	return []mutationCase{
		{
			name: "a document that is not JSON",
			code: codeCatalogInvalid,
			build: func(t *testing.T) []byte {
				return []byte("{")
			},
		},
		{
			name: "a second JSON value after the catalog",
			code: codeCatalogInvalid,
			build: func(t *testing.T) []byte {
				return append(encode(t, fixtureDocument()), []byte("\n{\"trailing\": true}")...)
			},
		},
		{
			name: "rules that are not a list",
			code: codeCatalogInvalid,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, document, "rules", map[string]any{"first": true})
				})
			},
		},
		{
			name: "an empty catalog",
			code: codeCatalogEmpty,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, document, "rules", []any{})
				})
			},
		},
		{
			name: "a catalog without a version",
			code: codeFieldMissing,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					remove(t, document, "schema_version")
				})
			},
		},
		{
			name: "a catalog with no rule list",
			code: codeFieldMissing,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					remove(t, document, "rules")
				})
			},
		},
		{
			name: "a version the loader does not implement",
			code: codeSchemaVersion,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, document, "schema_version", SchemaVersion+1)
				})
			},
		},
		{
			name: "a rule with an undeclared field",
			code: codeFieldUnknown,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					add(t, rulesOf(t, document)[0], "owner", "the one who wrote it")
				})
			},
		},
		{
			name: "a rule missing a required field",
			code: codeFieldMissing,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					remove(t, rulesOf(t, document)[0], "evidence")
				})
			},
		},
		{
			name: "a required field that is blank",
			code: codeFieldEmpty,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, rulesOf(t, document)[0], "description", "")
				})
			},
		},
		{
			name: "a statement that is blank",
			code: codeFieldEmpty,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, rulesOf(t, document)[0], "security", " ")
				})
			},
		},
		{
			name: "a list that names nobody",
			code: codeListEmpty,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, rulesOf(t, document)[0], "actors", []any{})
				})
			},
		},
		{
			name: "an id outside the convention",
			code: codeIDFormat,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, rulesOf(t, document)[0], "id", "qual-orders-placement")
				})
			},
		},
		{
			name: "two rules answering to the same id",
			code: codeIDDuplicate,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					rules := rulesOf(t, document)
					set(t, rules[1], "id", rules[0]["id"])
				})
			},
		},
		{
			name: "a risk class the program does not have",
			code: codeRiskInvalid,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, rulesOf(t, document)[0], "risk", "Q3")
				})
			},
		},
		{
			name: "a source document that is not in the checkout",
			code: codeSourceUnknown,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, rulesOf(t, document)[0], "source", "docs/UNKNOWN.md#placement")
				})
			},
		},
		{
			name: "a source citation with an empty anchor",
			code: codeSourceUnknown,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, rulesOf(t, document)[0], "source", "docs/SPEC.md#")
				})
			},
		},
		{
			name: "a module that is not a directory",
			code: codeModuleUnknown,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, rulesOf(t, document)[0], "modules", []any{"internal/fixture/ghosts"})
				})
			},
		},
		{
			name: "a reference that is not path::function",
			code: codeTestFormat,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, testsOf(t, rulesOf(t, document)[0]), "positive", []any{"internal/fixture/service/rules_test.go"})
				})
			},
		},
		{
			name: "a test file that is not in the checkout",
			code: codeTestUnknown,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, testsOf(t, rulesOf(t, document)[0]), "positive", []any{"internal/fixture/service/missing_test.go::TestPlacementInsideIsAccepted"})
				})
			},
		},
		{
			name: "a function the cited file does not declare",
			code: codeTestUnknown,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, testsOf(t, rulesOf(t, document)[0]), "positive", []any{"internal/fixture/service/rules_test.go::TestGone"})
				})
			},
		},
		{
			name: "a Q0 rule with no limit scenario",
			code: codeTestCategoryMissing,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					remove(t, testsOf(t, rulesOf(t, document)[0]), "limit")
				})
			},
		},
		{
			name: "a reason for a category that does not exist",
			code: codeReasonUnknown,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					add(t, reasonsOf(t, rulesOf(t, document)[1]), "chaos", "not a category")
				})
			},
		},
		{
			name: "a reason that is blank",
			code: codeReasonEmpty,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, reasonsOf(t, rulesOf(t, document)[1]), "limit", " ")
				})
			},
		},
		{
			name: "a reason for a category that has tests",
			code: codeReasonConflict,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, testsOf(t, rulesOf(t, document)[1]), "limit", []any{"internal/fixture/service/orders_test.go::TestConfirmationSucceeds"})
				})
			},
		},
		{
			name: "evidence that is not in the checkout",
			code: codeEvidenceUnknown,
			build: func(t *testing.T) []byte {
				return changed(t, func(t *testing.T, document map[string]any) {
					set(t, rulesOf(t, document)[0], "evidence", []any{"docs/MISSING.md"})
				})
			},
		},
	}
}

// encode renders a document the way the loader reads it.
func encode(t *testing.T, document map[string]any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("the fixture does not encode: %v", err)
	}
	return data
}

// changed applies a mutation to a fresh fixture and refuses when the document
// came out identical: a case that changes nothing proves nothing.
func changed(t *testing.T, mutate func(t *testing.T, document map[string]any)) []byte {
	t.Helper()
	document := fixtureDocument()
	before := encode(t, document)
	mutate(t, document)
	after := encode(t, document)
	if bytes.Equal(before, after) {
		t.Fatal("the mutation left the document unchanged")
	}
	return after
}

// set changes a field that has to be there, so a mutation cannot pass by
// writing a field the fixture never declared.
func set(t *testing.T, parent map[string]any, key string, value any) {
	t.Helper()
	if _, ok := parent[key]; !ok {
		t.Fatalf("the fixture has no `%s` to change", key)
	}
	parent[key] = value
}

// remove deletes a field that has to be there.
func remove(t *testing.T, parent map[string]any, key string) {
	t.Helper()
	if _, ok := parent[key]; !ok {
		t.Fatalf("the fixture has no `%s` to remove", key)
	}
	delete(parent, key)
}

// add writes a field that must not be there yet, so a mutation cannot pass by
// overwriting something the fixture already declared.
func add(t *testing.T, parent map[string]any, key string, value any) {
	t.Helper()
	if _, ok := parent[key]; ok {
		t.Fatalf("the fixture already has `%s`", key)
	}
	parent[key] = value
}

// rulesOf reaches the rule list of a fixture that has one.
func rulesOf(t *testing.T, document map[string]any) []map[string]any {
	t.Helper()
	rules, ok := document["rules"].([]map[string]any)
	if !ok {
		t.Fatal("the fixture has no rule list to reach into")
	}
	return rules
}

// testsOf reaches the evidence slots of a rule.
func testsOf(t *testing.T, rule map[string]any) map[string]any {
	t.Helper()
	tests, ok := rule["tests"].(map[string]any)
	if !ok {
		t.Fatal("the fixture rule declares no tests object")
	}
	return tests
}

// reasonsOf reaches the written reasons of a rule.
func reasonsOf(t *testing.T, rule map[string]any) map[string]any {
	t.Helper()
	reasons, ok := rule["not_applicable"].(map[string]any)
	if !ok {
		t.Fatal("the fixture rule declares no reasons object")
	}
	return reasons
}

// hasCode reports whether some violation carries the code.
func hasCode(violations []Violation, code string) bool {
	for _, violation := range violations {
		if violation.Code == code {
			return true
		}
	}
	return false
}

// codesOf lists the codes that fired, so a failure names what did happen
// instead of only what did not.
func codesOf(violations []Violation) []string {
	codes := make([]string, 0, len(violations))
	for _, violation := range violations {
		codes = append(codes, violation.Code)
	}
	return codes
}
