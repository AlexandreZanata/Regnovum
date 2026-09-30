// Tests of the executable evidence taxonomy (P21-T05).
//
// A taxonomy is a vocabulary, and a vocabulary is only worth what it refuses:
// the day a row can carry a kind nobody declared, or claim a class softer than
// the rules it proves, the classification stops meaning anything and every
// report built on it starts lying quietly. The tests below hold four
// properties, and the first alone would be worthless:
//
//   - the complete fixture passes, so a register of real identities is admitted
//     — including an anonymous suite, because the phase requires an owner of
//     the suite that proves a critical rule and of nothing else;
//   - the classification follows the environment and not the name: the same
//     tests on PostgreSQL are admitted as integration, and in a unit suite they
//     are refused;
//   - one mutation per violation code makes that code fire, each editing the
//     fixture by field name and failing when the field it means to change is
//     not there, so no case can pass by mutating nothing;
//   - every code the tool declares is named by at least one mutation, and every
//     mutation names a code the tool declares. A code nobody has seen refuse
//     anything is a green light with a comment.
//
// The fixture checkout is a small tree of its own — two documents, a schema and
// the tests the register cites — instead of this repository, so a mutation
// breaks exactly one thing.
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

// caseDef is one way the taxonomy can be defeated, and the code that has to
// fire. Most cases mutate the fixture register and let Check judge it; the ones
// about a document being absent, unreadable or outside the checkout use their
// own entry point, which is why the case carries the judgement rather than the
// bytes.
type caseDef struct {
	name  string
	code  string
	judge func(t *testing.T) Violations
}

// fixtureDocument is the smallest register that stands: three suites — one of
// them deliberately anonymous — and three evidence rows, one of which proves a
// critical rule so that the class is exercised end to end. It is built as a
// mutable map rather than a string because a text fragment that appears twice is
// how a mutation edits the wrong row and still passes.
func fixtureDocument() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"suites": []any{
			map[string]any{
				"id":          "SUITE-FIXTURE-UNIT",
				"owner":       "fixture",
				"environment": "unit",
				"command":     "make test-unit",
				"modules":     []any{"internal/fixture/service"},
			},
			map[string]any{
				"id":          "SUITE-FIXTURE-BROWSER",
				"owner":       "fixture",
				"environment": "browser",
				"command":     "make test-e2e",
				"modules":     []any{"tools/fixture"},
			},
			map[string]any{
				// No owner, and allowed to have none: nothing critical is
				// proved here. The moment a Q0 rule moves in it is refused,
				// and that is the property the phase states in one line.
				"id":          "SUITE-FIXTURE-QUIET",
				"environment": "unit",
				"command":     "make test-unit",
				"modules":     []any{"internal/fixture/service"},
			},
		},
		"evidence": []any{
			map[string]any{
				"id":     "EVD-FIXTURE-UNIT-01",
				"kind":   "unit",
				"suite":  "SUITE-FIXTURE-UNIT",
				"risk":   "Q0",
				"rules":  []any{"QUAL-REQ-FIX-01"},
				"tests":  []any{"internal/fixture/service/rules_test.go::TestRuleHolds"},
				"proves": "a regra crítica é provada pelo teste declarado, e a classe vem do catálogo",
			},
			map[string]any{
				"id":     "EVD-FIXTURE-BROWSER-01",
				"kind":   "e2e",
				"suite":  "SUITE-FIXTURE-BROWSER",
				"risk":   "Q1",
				"rules":  []any{"QUAL-REQ-FIX-02"},
				"tests":  []any{"tools/fixture/e2e/spec.js"},
				"proves": "a jornada percorre o caminho entregue no navegador que a suíte declara",
			},
			map[string]any{
				"id":     "EVD-FIXTURE-QUIET-01",
				"kind":   "unit",
				"suite":  "SUITE-FIXTURE-QUIET",
				"risk":   "Q1",
				"rules":  []any{"QUAL-REQ-FIX-02"},
				"tests":  []any{"internal/fixture/service/fixture_test.go::TestQuietRuleHolds"},
				"proves": "uma suíte sem dono é admitida enquanto não prova regra crítica alguma",
			},
		},
	}
}

// fixtureCatalog is the catalog's half of the fixture: the two rules the
// register cites, one critical and one not, so the class is read from here
// rather than from the row that declares it.
const fixtureCatalog = `{"schema_version":1,"rules":[{"id":"QUAL-REQ-FIX-01","risk":"Q0"},{"id":"QUAL-REQ-FIX-02","risk":"Q1"}]}`

// fixtureSchema is the published contract, written here from the loader's own
// vocabulary so that the agreement check has something to agree with.
func fixtureSchema() map[string]any {
	return map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"title":                "Registro de evidências de teste",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"schema_version", "suites", "evidence"},
		"properties": map[string]any{
			"schema_version": map[string]any{"type": "integer", "const": 1},
			"suites":         map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/suite"}},
			"evidence":       map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/evidence"}},
		},
		"$defs": map[string]any{
			"suite": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"id", "environment", "command", "modules"},
				"properties": map[string]any{
					"id":          map[string]any{"type": "string"},
					"owner":       map[string]any{"type": "string"},
					"environment": map[string]any{"type": "string", "enum": anyList(Environments)},
					"command":     map[string]any{"type": "string"},
					"modules":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
			},
			"evidence": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"id", "kind", "suite", "risk", "rules", "tests", "proves"},
				"properties": map[string]any{
					"id":     map[string]any{"type": "string"},
					"kind":   map[string]any{"type": "string", "enum": anyList(Kinds)},
					"suite":  map[string]any{"type": "string"},
					"risk":   map[string]any{"type": "string", "enum": anyList(Risks)},
					"rules":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"tests":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"proves": map[string]any{"type": "string"},
				},
			},
		},
	}
}

func anyList(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// fixtureRoot builds a whole checkout for the cases that need one: the register,
// the catalog, the published schema and the tree the cited tests live in. The
// command is judged end to end here, and on a synthetic tree the failure of one
// document cannot be confused with the state of the repository.
func fixtureRoot(t *testing.T, mutate func(document map[string]any)) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, registerPath, string(changed(t, mutate)))
	writeFile(t, root, CatalogPath, fixtureCatalog)
	writeFile(t, root, SchemaPath, string(mustEncode(t, fixtureSchema())))
	writeFile(t, root, "internal/fixture/service/rules_test.go", "package service\n\nimport \"testing\"\n\nfunc TestRuleHolds(t *testing.T) {}\n")
	writeFile(t, root, "internal/fixture/service/fixture_test.go", "package service\n\nimport \"testing\"\n\nfunc TestQuietRuleHolds(t *testing.T) {}\n")
	writeFile(t, root, "tools/fixture/e2e/spec.js", "// a journey\n")
	return root
}

// fixtureRecords is what the fixture register resolves against: the catalog's
// classes and the tree the cited tests live in. A defect in the document is
// then the only reason a mutation can fail.
func fixtureRecords(t *testing.T) Records {
	t.Helper()
	records, violations := ReadRecords(fixtureRoot(t, nil))
	if len(violations) > 0 {
		t.Fatalf("the fixture records are unreadable: %v", violations)
	}
	return records
}

// changed applies a mutation and refuses to let it be a no-op: a case that
// silently changes nothing would pass for the wrong reason.
func changed(t *testing.T, mutate func(document map[string]any)) []byte {
	t.Helper()
	document := fixtureDocument()
	before := mustEncode(t, document)
	if mutate == nil {
		return before
	}
	mutate(document)
	after := mustEncode(t, document)
	if bytes.Equal(before, after) {
		t.Fatal("the mutation changed nothing: a case that does not move the document proves nothing")
	}
	return after
}

// judged mutates the fixture register and judges it against the fixture
// records, which is how every case about the document itself is expressed.
func judged(t *testing.T, mutate func(document map[string]any)) Violations {
	t.Helper()
	_, violations := Check(changed(t, mutate), fixtureRecords(t))
	return violations
}

func evidenceAt(t *testing.T, document map[string]any, index int) map[string]any {
	t.Helper()
	rows, ok := document["evidence"].([]any)
	if !ok || index >= len(rows) {
		t.Fatalf("the fixture document holds no evidence row %d to change", index)
	}
	row, ok := rows[index].(map[string]any)
	if !ok {
		t.Fatalf("the fixture evidence row %d is not an object", index)
	}
	return row
}

func suiteAt(t *testing.T, document map[string]any, index int) map[string]any {
	t.Helper()
	rows, ok := document["suites"].([]any)
	if !ok || index >= len(rows) {
		t.Fatalf("the fixture document holds no suite %d to change", index)
	}
	row, ok := rows[index].(map[string]any)
	if !ok {
		t.Fatalf("the fixture suite %d is not an object", index)
	}
	return row
}

// withoutEvidence removes one evidence row, which is how a suite that nobody
// classifies is built.
func withoutEvidence(t *testing.T, document map[string]any, index int) {
	t.Helper()
	rows, ok := document["evidence"].([]any)
	if !ok || index >= len(rows) {
		t.Fatalf("the fixture document holds no evidence row %d to remove", index)
	}
	document["evidence"] = append(rows[:index:index], rows[index+1:]...)
}

// schemaMutation applies a mutation to the fixture schema and returns it
// encoded, refusing a no-op for the same reason `changed` does.
func schemaMutation(t *testing.T, mutate func(schema map[string]any)) string {
	t.Helper()
	schema := fixtureSchema()
	before := mustEncode(t, schema)
	mutate(schema)
	after := mustEncode(t, schema)
	if bytes.Equal(before, after) {
		t.Fatal("the mutation changed nothing: a case that does not move the schema proves nothing")
	}
	return string(after)
}

func definitionOf(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("the fixture schema declares no $defs")
	}
	definition, ok := definitions[name].(map[string]any)
	if !ok {
		t.Fatalf("the fixture schema declares no %q definition", name)
	}
	return definition
}

func propertiesOf(t *testing.T, definition map[string]any) map[string]any {
	t.Helper()
	properties, ok := definition["properties"].(map[string]any)
	if !ok {
		t.Fatal("the fixture definition declares no properties")
	}
	return properties
}

func propertyOf(t *testing.T, definition map[string]any, name string) map[string]any {
	t.Helper()
	property, ok := propertiesOf(t, definition)[name].(map[string]any)
	if !ok {
		t.Fatalf("the fixture definition declares no %q property", name)
	}
	return property
}

// dropFromRequired removes one name from a definition's required array.
func dropFromRequired(t *testing.T, definition map[string]any, name string) {
	t.Helper()
	required, ok := definition["required"].([]any)
	if !ok {
		t.Fatal("the fixture definition declares no required array")
	}
	kept := make([]any, 0, len(required))
	for _, field := range required {
		if field != name {
			kept = append(kept, field)
		}
	}
	if len(kept) == len(required) {
		t.Fatalf("the fixture definition does not require %q to drop", name)
	}
	definition["required"] = kept
}

// dropFromEnum removes one entry from a property's enum.
func dropFromEnum(t *testing.T, property map[string]any, value string) {
	t.Helper()
	values, ok := property["enum"].([]any)
	if !ok {
		t.Fatal("the fixture property declares no enum")
	}
	kept := make([]any, 0, len(values))
	for _, entry := range values {
		if entry != value {
			kept = append(kept, entry)
		}
	}
	if len(kept) == len(values) {
		t.Fatalf("the fixture property does not admit %q to drop", value)
	}
	property["enum"] = kept
}

// judgeSchema writes a schema into a checkout of its own and judges it.
func judgeSchema(t *testing.T, raw string) Violations {
	t.Helper()
	root := fixtureRoot(t, nil)
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(SchemaPath)), []byte(raw), 0o644); err != nil {
		t.Fatalf("the fixture schema is not writable: %v", err)
	}
	return ReadSchema(root)
}

// cases is the whole table of defects. It is read twice: once to drive the
// assertions and once by the bidirectional check below.
func cases() []caseDef {
	return []caseDef{
		{
			name: "a register that is not in the checkout",
			code: codeEvidenceMissing,
			judge: func(t *testing.T) Violations {
				root := fixtureRoot(t, nil)
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(registerPath))); err != nil {
					t.Fatalf("the fixture register cannot be removed: %v", err)
				}
				records, violations := ReadRecords(root)
				if len(violations) > 0 {
					t.Fatalf("the fixture records are unreadable: %v", violations)
				}
				_, violations = ReadRegister(root, registerPath, records)
				return violations
			},
		},
		{
			name: "a register whose top level is not an object",
			code: codeEvidenceInvalid,
			judge: func(t *testing.T) Violations {
				_, violations := Check([]byte("[1, 2, 3]"), fixtureRecords(t))
				return violations
			},
		},
		{
			name: "a row that is not an object",
			code: codeEvidenceInvalid,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					document["evidence"] = []any{"not a row"}
				})
			},
		},
		{
			name: "a key the contract does not declare, on an evidence row",
			code: codeFieldUnknown,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["weight"] = 3
				})
			},
		},
		{
			name: "a key the contract does not declare, on a suite",
			code: codeFieldUnknown,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					suiteAt(t, document, 0)["tier"] = "gold"
				})
			},
		},
		{
			name: "an evidence row without its suite",
			code: codeFieldMissing,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					delete(evidenceAt(t, document, 0), "suite")
				})
			},
		},
		{
			name: "a suite without its command",
			code: codeFieldMissing,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					delete(suiteAt(t, document, 0), "command")
				})
			},
		},
		{
			name: "an evidence row whose declaration is blank",
			code: codeFieldEmpty,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["proves"] = "   "
				})
			},
		},
		{
			name: "a suite whose command is blank",
			code: codeFieldEmpty,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					suiteAt(t, document, 0)["command"] = ""
				})
			},
		},
		{
			name: "evidence that proves no rule",
			code: codeListEmpty,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["rules"] = []any{}
				})
			},
		},
		{
			name: "evidence that cites no test",
			code: codeListEmpty,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["tests"] = []any{}
				})
			},
		},
		{
			name: "a suite that names no module",
			code: codeListEmpty,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					suiteAt(t, document, 0)["modules"] = []any{}
				})
			},
		},
		{
			name: "a register written against a version this loader does not know",
			code: codeSchemaVersion,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					document["schema_version"] = 2
				})
			},
		},
		{
			name: "a version that is not a number",
			code: codeSchemaVersion,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					document["schema_version"] = "um"
				})
			},
		},
		{
			name: "an evidence identifier outside the convention",
			code: codeIDFormat,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["id"] = "evidence-1"
				})
			},
		},
		{
			name: "a suite identifier outside the convention",
			code: codeIDFormat,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					suiteAt(t, document, 0)["id"] = "FIXTURE-UNIT"
				})
			},
		},
		{
			name: "two evidence rows that share an identifier",
			code: codeIDDuplicate,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 1)["id"] = "EVD-FIXTURE-UNIT-01"
				})
			},
		},
		{
			name: "two suites that share an identifier",
			code: codeIDDuplicate,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					suiteAt(t, document, 1)["id"] = "SUITE-FIXTURE-UNIT"
				})
			},
		},
		{
			name: "a kind the vocabulary does not declare",
			code: codeKindUnknown,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["kind"] = "smoke"
				})
			},
		},
		{
			name: "an environment the vocabulary does not declare",
			code: codeEnvironmentUnknown,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					suiteAt(t, document, 0)["environment"] = "staging"
				})
			},
		},
		{
			name: "an integration test classified in a unit suite",
			code: codeKindEnvironment,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["kind"] = "integration"
				})
			},
		},
		{
			name: "a kind that could run anywhere",
			code: codeKindEnvironment,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					// A chaos exercise on a unit suite: the table says the
					// daemon, and the table is what decides.
					evidenceAt(t, document, 0)["kind"] = "chaos"
				})
			},
		},
		{
			name: "a critical rule proved by a suite nobody answers for",
			code: codeQ0WithoutOwner,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["suite"] = "SUITE-FIXTURE-QUIET"
				})
			},
		},
		{
			name: "evidence in a suite nobody declared",
			code: codeSuiteUnknown,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["suite"] = "SUITE-FIXTURE-DOCKER"
				})
			},
		},
		{
			name: "a suite no evidence classifies",
			code: codeSuiteUnknown,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					withoutEvidence(t, document, 2)
				})
			},
		},
		{
			name: "evidence for a rule the catalog does not declare",
			code: codeRuleUnknown,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["rules"] = []any{"QUAL-REQ-NOPE-01"}
				})
			},
		},
		{
			name: "a class the vocabulary does not declare",
			code: codeRiskMismatch,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["risk"] = "Q9"
				})
			},
		},
		{
			name: "a class softer than the rules the evidence proves",
			code: codeRiskMismatch,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["risk"] = "Q2"
				})
			},
		},
		{
			name: "a class stricter than the rules the evidence proves",
			code: codeRiskMismatch,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 2)["risk"] = "Q0"
				})
			},
		},
		{
			name: "a cited test that the file does not declare",
			code: codeTestUnknown,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["tests"] = []any{"internal/fixture/service/rules_test.go::TestNobodyWrote"}
				})
			},
		},
		{
			name: "a cited file that is not in the checkout",
			code: codeTestUnknown,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["tests"] = []any{"internal/fixture/service/absent_test.go::TestRuleHolds"}
				})
			},
		},
		{
			name: "a cited file outside the checkout",
			code: codeTestUnknown,
			judge: func(t *testing.T) Violations {
				return judged(t, func(document map[string]any) {
					evidenceAt(t, document, 0)["tests"] = []any{"../elsewhere/rules_test.go::TestRuleHolds"}
				})
			},
		},
		{
			name: "a register path outside the checkout",
			code: codeSourceUnknown,
			judge: func(t *testing.T) Violations {
				root := fixtureRoot(t, nil)
				_, violations := ReadRegister(root, "../register.json", Records{Rules: map[string]string{}, Root: root})
				return violations
			},
		},
		{
			name: "a catalog that is not in the checkout",
			code: codeSourceUnknown,
			judge: func(t *testing.T) Violations {
				root := fixtureRoot(t, nil)
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(CatalogPath))); err != nil {
					t.Fatalf("the fixture catalog cannot be removed: %v", err)
				}
				_, violations := ReadRecords(root)
				return violations
			},
		},
		{
			name: "a catalog that declares no rule",
			code: codeSourceUnknown,
			judge: func(t *testing.T) Violations {
				root := fixtureRoot(t, nil)
				writeFile(t, root, CatalogPath, `{"schema_version":1,"rules":[]}`)
				_, violations := ReadRecords(root)
				return violations
			},
		},
		{
			name: "a published schema that is not in the checkout",
			code: codeSchemaMissing,
			judge: func(t *testing.T) Violations {
				root := fixtureRoot(t, nil)
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(SchemaPath))); err != nil {
					t.Fatalf("the fixture schema cannot be removed: %v", err)
				}
				return ReadSchema(root)
			},
		},
		{
			name: "a published schema that does not decode",
			code: codeSchemaInvalid,
			judge: func(t *testing.T) Violations {
				return judgeSchema(t, "not a schema at all")
			},
		},
		{
			name: "a published schema with no evidence array",
			code: codeSchemaInvalid,
			judge: func(t *testing.T) Violations {
				return judgeSchema(t, schemaMutation(t, func(schema map[string]any) {
					delete(propertiesOf(t, schema), "evidence")
				}))
			},
		},
		{
			name: "a published schema whose reference resolves to nothing",
			code: codeSchemaInvalid,
			judge: func(t *testing.T) Violations {
				return judgeSchema(t, schemaMutation(t, func(schema map[string]any) {
					propertyOf(t, schema, "evidence")["items"] = map[string]any{"$ref": "#/$defs/absent"}
				}))
			},
		},
		{
			name: "a published schema that requires less of an evidence than the loader",
			code: codeSchemaFields,
			judge: func(t *testing.T) Violations {
				return judgeSchema(t, schemaMutation(t, func(schema map[string]any) {
					dropFromRequired(t, definitionOf(t, schema, "evidence"), "proves")
				}))
			},
		},
		{
			name: "a published schema that requires less of a suite than the loader",
			code: codeSchemaFields,
			judge: func(t *testing.T) Violations {
				return judgeSchema(t, schemaMutation(t, func(schema map[string]any) {
					dropFromRequired(t, definitionOf(t, schema, "suite"), "modules")
				}))
			},
		},
		{
			name: "a published schema that admits a key of a suite the loader refuses",
			code: codeSchemaFields,
			judge: func(t *testing.T) Violations {
				return judgeSchema(t, schemaMutation(t, func(schema map[string]any) {
					propertiesOf(t, definitionOf(t, schema, "suite"))["tier"] = map[string]any{"type": "string"}
				}))
			},
		},
		{
			name: "a published schema that admits a kind the loader does not know",
			code: codeSchemaVocabulary,
			judge: func(t *testing.T) Violations {
				return judgeSchema(t, schemaMutation(t, func(schema map[string]any) {
					dropFromEnum(t, propertyOf(t, definitionOf(t, schema, "evidence"), "kind"), KindChaos)
				}))
			},
		},
		{
			name: "a published schema that admits an environment the loader does not know",
			code: codeSchemaVocabulary,
			judge: func(t *testing.T) Violations {
				return judgeSchema(t, schemaMutation(t, func(schema map[string]any) {
					dropFromEnum(t, propertyOf(t, definitionOf(t, schema, "suite"), "environment"), EnvironmentDocker)
				}))
			},
		},
		{
			name: "a published schema that admits keys the loader refuses",
			code: codeSchemaStrictness,
			judge: func(t *testing.T) Violations {
				return judgeSchema(t, schemaMutation(t, func(schema map[string]any) {
					definitionOf(t, schema, "evidence")["additionalProperties"] = true
				}))
			},
		},
	}
}

// TestTheCompleteFixtureIsAccepted is the control. Without it a classifier that
// refused everything would look like a classifier, and the anonymous suite
// would be caught by a check nobody meant to write.
func TestTheCompleteFixtureIsAccepted(t *testing.T) {
	register, violations := Check(changed(t, nil), fixtureRecords(t))
	if len(violations) > 0 {
		t.Fatalf("the complete fixture is refused: %v", violations)
	}
	if len(register.Evidence) != 3 || len(register.Suites) != 3 {
		t.Fatalf("the fixture declares %d evidence in %d suites, and the control exists to hold three of each", len(register.Evidence), len(register.Suites))
	}
}

// TestTheClassificationFollowsTheEnvironmentNotTheName is the property the
// phase asks for in one line: the same identities on PostgreSQL are integration
// tests, and in a unit suite they are refused. Nothing here reads a function
// name — only the declared kind and the declared environment.
func TestTheClassificationFollowsTheEnvironmentNotTheName(t *testing.T) {
	onPostgres := judged(t, func(document map[string]any) {
		suiteAt(t, document, 0)["environment"] = EnvironmentPostgres
		evidenceAt(t, document, 0)["kind"] = KindIntegration
	})
	if len(onPostgres) > 0 {
		t.Fatalf("an integration evidence on postgres is refused: %v", onPostgres)
	}
	inUnit := judged(t, func(document map[string]any) {
		evidenceAt(t, document, 0)["kind"] = KindIntegration
	})
	if !hasCode(inUnit, codeKindEnvironment) {
		t.Fatalf("the same evidence in a unit suite is admitted: %v", inUnit)
	}
}

// TestTheAnonymousSuiteIsAcceptedUntilItProvesSomethingCritical states the
// conditional owner as two measurements instead of a comment: a suite that
// proves nothing critical needs no owner, and the same suite is refused the
// moment a critical rule moves in.
func TestTheAnonymousSuiteIsAcceptedUntilItProvesSomethingCritical(t *testing.T) {
	if _, violations := Check(changed(t, nil), fixtureRecords(t)); hasCode(violations, codeQ0WithoutOwner) {
		t.Fatalf("a suite with no owner is refused while it proves nothing critical: %v", violations)
	}
	moved := judged(t, func(document map[string]any) {
		evidenceAt(t, document, 0)["suite"] = "SUITE-FIXTURE-QUIET"
	})
	if !hasCode(moved, codeQ0WithoutOwner) {
		t.Fatalf("a critical rule proved by a suite nobody answers for is admitted: %v", moved)
	}
}

// TestEveryVocabularyIsSelfConsistent holds the tables to each other, which is
// what makes them a taxonomy rather than three lists: every kind has a label
// and at least one environment, every environment a kind names is one the
// vocabulary declares, and every field a row must carry is one it may carry.
func TestEveryVocabularyIsSelfConsistent(t *testing.T) {
	if len(Labels) != len(Kinds) {
		t.Errorf("the label table names %d kind(s) and the vocabulary declares %d", len(Labels), len(Kinds))
	}
	if len(KindEnvironments) != len(Kinds) {
		t.Errorf("the environment table names %d kind(s) and the vocabulary declares %d", len(KindEnvironments), len(Kinds))
	}
	for _, kind := range Kinds {
		if strings.TrimSpace(Labels[kind]) == "" {
			t.Errorf("the kind `%s` has no label: a vocabulary a report cannot print in words is a vocabulary for machines only", kind)
		}
		admitted := KindEnvironments[kind]
		if len(admitted) == 0 {
			t.Errorf("the kind `%s` admits no environment: a test that runs nowhere classifies nothing", kind)
		}
		for _, environment := range admitted {
			if !contains(Environments, environment) {
				t.Errorf("the kind `%s` admits `%s`, which is not an environment: %v", kind, environment, Environments)
			}
		}
	}
	for kind := range Labels {
		if !contains(Kinds, kind) {
			t.Errorf("the label table names `%s`, which is not a kind: %v", kind, Kinds)
		}
	}
	for kind := range KindEnvironments {
		if !contains(Kinds, kind) {
			t.Errorf("the environment table names `%s`, which is not a kind: %v", kind, Kinds)
		}
	}
	for _, field := range SuiteRequired {
		if !contains(SuiteFields, field) {
			t.Errorf("a suite must declare `%s` and may not declare it at all: the required list has to be inside the admitted one", field)
		}
	}
	if !sameSet(sorted(EvidenceRequired), sorted(EvidenceFields)) {
		t.Errorf("the evidence rows must declare %v and may declare %v: every field an evidence carries is one it must carry", EvidenceRequired, EvidenceFields)
	}
}

// TestEveryDefectIsRefused drives one mutation per defect and names the code it
// has to make the classifier emit.
func TestEveryDefectIsRefused(t *testing.T) {
	for _, testCase := range cases() {
		t.Run(testCase.name, func(t *testing.T) {
			violations := testCase.judge(t)
			if !hasCode(violations, testCase.code) {
				t.Fatalf("the defect did not make `%s` fire: %v", testCase.code, violations)
			}
		})
	}
}

// TestEveryDeclaredCodeHasAMutation runs in both directions: a code the tool
// declares and no test makes fire is a rule nobody has seen work, and a code a
// test expects and the tool no longer declares is a test that would pass
// against a program that has moved on.
func TestEveryDeclaredCodeHasAMutation(t *testing.T) {
	declared := declaredCodes(t)
	proven := map[string]string{}
	for _, testCase := range cases() {
		proven[testCase.code] = "TestEveryDefectIsRefused"
	}
	for code, source := range declared {
		if _, ok := proven[code]; !ok {
			t.Errorf("`%s` is declared in %s and no test makes it fire: a code nobody has seen refuse anything guards nothing", code, source)
		}
	}
	for code, test := range proven {
		if _, ok := declared[code]; !ok {
			t.Errorf("`%s` is expected by %s and the tool does not declare it", code, test)
		}
	}
}

// declaredCodes reads the violation codes out of the tool's own sources instead
// of listing them again here: a list repeated in the test is a list that drifts,
// and the point of this check is that it cannot.
func declaredCodes(t *testing.T) map[string]string {
	t.Helper()
	// The name is deliberately loose — `codeQ0WithoutOwner` is a code like any
	// other, and a pattern that could not see it would report the code as
	// untested while the test that fires it sits three hundred lines away.
	pattern := regexp.MustCompile(`(?m)^\s*code[A-Za-z][A-Za-z0-9]*\s*=\s*"([^"]+)"`)
	declared := map[string]string{}
	for _, source := range []string{"taxonomy.go", "records.go", "schema.go"} {
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("the source %s is unreadable: %v", source, err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
			declared[match[1]] = source
		}
	}
	return declared
}

// TestTheDeliveredRegisterIsJudgedAsItIs holds the tool to the files in this
// checkout: the register as delivered, the catalog its classes are read from,
// and the published schema. It is also where the minimal validation of the
// phase is measured — the valid examples have to come from more than one
// module, and a register that classified one package would satisfy every rule
// above while describing a repository nobody has.
func TestTheDeliveredRegisterIsJudgedAsItIs(t *testing.T) {
	root := filepath.Join("..", "..")
	records, violations := ReadRecords(root)
	if len(violations) > 0 {
		t.Fatalf("the delivered records are unreadable: %v", violations)
	}
	if len(records.Rules) == 0 {
		t.Fatal("the delivered catalog declares no rule: a taxonomy compared against nothing accepts everything")
	}
	register, violations := ReadRegister(root, registerPath, records)
	if len(violations) > 0 {
		t.Fatalf("the delivered register is refused: %v", violations)
	}
	if len(register.Evidence) == 0 || len(register.Suites) == 0 {
		t.Fatal("the delivered register classifies nothing: this test exists to hold what is delivered, not an empty document")
	}
	if violations := ReadSchema(root); len(violations) > 0 {
		t.Fatalf("the delivered schema does not agree with the loader: %v", violations)
	}

	modules := map[string]bool{}
	kinds := map[string]bool{}
	for _, evidence := range register.Evidence {
		kinds[evidence.Kind] = true
	}
	for _, suite := range register.Suites {
		for _, module := range suite.Modules {
			modules[module] = true
		}
	}
	if len(modules) < 3 {
		t.Errorf("the delivered register covers %d module(s) and the phase asks for valid examples in at least three", len(modules))
	}
	if len(kinds) < 3 {
		t.Errorf("the delivered register classifies %d kind(s) and the phase asks for a taxonomy, not a single word", len(kinds))
	}
}

// TestTheClassesAreTheOnesTheCatalogUses closes the vocabulary in the other
// direction: every class the loader knows has to be a class the catalog
// actually uses, because a class nothing is filed under is a column that will
// be filled by whoever guesses hardest.
func TestTheClassesAreTheOnesTheCatalogUses(t *testing.T) {
	records, violations := ReadRecords(filepath.Join("..", ".."))
	if len(violations) > 0 {
		t.Fatalf("the delivered records are unreadable: %v", violations)
	}
	used := map[string]bool{}
	for _, class := range records.Rules {
		used[class] = true
	}
	for _, class := range Risks {
		if !used[class] {
			t.Errorf("the class `%s` is declared by the taxonomy and no rule of the catalog is filed under it", class)
		}
	}
	for class := range used {
		if !contains(Risks, class) {
			t.Errorf("the catalog files a rule under `%s`, which is not a class the taxonomy declares: %v", class, Risks)
		}
	}
}

// TestTheCommandJudgesARegister is the wiring: the command reads the register,
// the records and the schema, and reports what it judged.
func TestTheCommandJudgesARegister(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := run([]string{"-root", fixtureRoot(t, nil)}, &stdout, &stderr)
	if status != exitOK {
		t.Fatalf("the command refused the complete fixture with status %d: %s%s", status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "3 evidence row(s)") {
		t.Fatalf("the command did not report what it judged: %s", stdout.String())
	}
}

// TestTheCommandRefusesABrokenRegister is the other half of the wiring: the
// failure has to be an exit status, and it has to name the row and the code so
// that whoever reads the CI log knows where to look.
func TestTheCommandRefusesABrokenRegister(t *testing.T) {
	root := fixtureRoot(t, func(document map[string]any) {
		evidenceAt(t, document, 1)["id"] = "EVD-FIXTURE-UNIT-01"
	})
	var stdout, stderr bytes.Buffer
	status := run([]string{"-root", root}, &stdout, &stderr)
	if status == exitOK {
		t.Fatalf("the command accepted a register with a duplicated identity: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "EVD-FIXTURE-UNIT-01") || !strings.Contains(stderr.String(), codeIDDuplicate) {
		t.Fatalf("stderr names neither the row nor the code: %s", stderr.String())
	}
}

func hasCode(violations Violations, code string) bool {
	for _, violation := range violations {
		if violation.Code == code {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("the fixture root is not writable: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("the fixture file is not writable: %v", err)
	}
}

func mustEncode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("the fixture cannot be encoded: %v", err)
	}
	return data
}
