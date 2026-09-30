package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// today is the day every test judges against. A policy about expiry that read
// the clock itself would be a policy no test could hold still.
var today = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// caseDef is one way the policy can be defeated, and the code that has to fire.
// The same table is what proves the bidirectional property the sibling catalog
// tool proves: a code nobody has seen refuse anything guards nothing, and a
// test that expects a code the tool no longer declares would pass against a
// program that has moved on.
type caseDef struct {
	name  string
	code  string
	check func(t *testing.T) []Violation
}

// fixtureDocument is the smallest waiver that stands: a mild finding, a rule
// the catalog calls mild too, an owner that is a role, a compensation that
// exists in the fixture tree, and a window that has not expired.
func fixtureDocument() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"waivers": []map[string]any{
			{
				"id":            "WVR-DOC-001",
				"finding":       "SEC-01",
				"risk":          "Q1",
				"categories":    []any{"documentation"},
				"rules":         []any{"QUAL-REQ-I18N-01"},
				"owner":         "i18n",
				"justification": "the export note is scheduled with the next documentation pass",
				"compensation":  []any{"internal/fixture/service/rules_test.go::TestPlaceholderStands"},
				"created":       "2026-01-01",
				"expires":       "2027-01-01",
			},
		},
	}
}

// fixtureTree is the checkout the fixture's records resolve against: the
// compensating test the waiver names. It is not this repository, so a mutation
// changes one thing and nothing else.
func fixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "internal", "fixture", "service")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("the fixture tree cannot be built: %v", err)
	}
	content := "package service\n\nimport \"testing\"\n\nfunc TestPlaceholderStands(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(path, "rules_test.go"), []byte(content), 0o644); err != nil {
		t.Fatalf("the fixture tree cannot be built: %v", err)
	}
	return root
}

// fixtureRecords is what the fixture waiver resolves against. The rule and the
// finding are both there, so a defect in the document is the only reason a
// mutation can fail.
func fixtureRecords(t *testing.T) Records {
	t.Helper()
	return Records{
		Rules:    map[string]string{"QUAL-REQ-I18N-01": "Q1"},
		Findings: map[string]string{"SEC-01": "docs/SECURITY_AUDIT.md"},
		Root:     fixtureTree(t),
	}
}

func encode(t *testing.T, document map[string]any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("the fixture document cannot be encoded: %v", err)
	}
	return data
}

// changed applies a mutation and refuses to let it be a no-op: a case that
// silently changes nothing would pass for the wrong reason.
func changed(t *testing.T, mutate func(document map[string]any)) []byte {
	t.Helper()
	document := fixtureDocument()
	before := encode(t, document)
	mutate(document)
	after := encode(t, document)
	if string(before) == string(after) {
		t.Fatal("the mutation changed nothing: a case that does not move the document proves nothing")
	}
	return after
}

// waiverOf is the first entry of a fixture document, for the mutations that
// need to reach inside it.
func waiverOf(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	waivers, ok := document["waivers"].([]map[string]any)
	if !ok || len(waivers) == 0 {
		t.Fatal("the fixture document holds no waiver to change")
	}
	return waivers[0]
}

// checkDocument judges a mutated document against the fixture records.
func checkDocument(t *testing.T, mutate func(document map[string]any)) []Violation {
	t.Helper()
	_, violations := Check(changed(t, mutate), fixtureRecords(t), today)
	return violations
}

// TestTheCompleteFixtureIsAccepted is the control. Without it a policy that
// refused everything would pass every mutation below.
func TestTheCompleteFixtureIsAccepted(t *testing.T) {
	_, violations := Check(encode(t, fixtureDocument()), fixtureRecords(t), today)
	if len(violations) > 0 {
		t.Fatalf("the complete fixture is refused: %v", violations)
	}
}

// TestTheEmptyRegisterIsAccepted is the difference from the catalog, stated as
// a test: an empty catalog verifies nothing, and an empty register is the state
// a healthy tree is in. One tool refuses the empty document, the other accepts
// it, and both refusals are the right one for what they carry.
func TestTheEmptyRegisterIsAccepted(t *testing.T) {
	document := encode(t, map[string]any{"schema_version": 1, "waivers": []map[string]any{}})
	waivers, violations := Check(document, fixtureRecords(t), today)
	if len(violations) > 0 {
		t.Fatalf("the empty register is refused: %v", violations)
	}
	if len(waivers.Waivers) != 0 {
		t.Fatalf("the empty register decoded to %d waiver(s)", len(waivers.Waivers))
	}
}

// TestEveryDefectIsRefused drives one mutation per code.
func TestEveryDefectIsRefused(t *testing.T) {
	for _, testCase := range cases() {
		t.Run(testCase.name, func(t *testing.T) {
			violations := testCase.check(t)
			if !hasCode(violations, testCase.code) {
				t.Fatalf("`%s` was not refused with `%s`: %v", testCase.name, testCase.code, violations)
			}
		})
	}
}

// cases is the table of the ways the policy can be defeated.
func cases() []caseDef {
	set := func(field string, value any) func(*testing.T) []Violation {
		return func(t *testing.T) []Violation {
			return checkDocument(t, func(document map[string]any) {
				waiverOf(t, document)[field] = value
			})
		}
	}
	return []caseDef{
		{
			name: "a document that is not JSON",
			code: codeWaiversInvalid,
			check: func(t *testing.T) []Violation {
				_, violations := Check([]byte("{not json"), fixtureRecords(t), today)
				return violations
			},
		},
		{
			name: "a key the contract does not know",
			code: codeFieldUnknown,
			check: func(t *testing.T) []Violation {
				return checkDocument(t, func(document map[string]any) {
					document["reviewed_by"] = "somebody"
				})
			},
		},
		{
			name: "a key inside a waiver the contract does not know",
			code: codeFieldUnknown,
			check: func(t *testing.T) []Violation {
				return checkDocument(t, func(document map[string]any) {
					waiverOf(t, document)["ticket"] = "SUP-1"
				})
			},
		},
		{
			name: "a required field that is absent",
			code: codeFieldMissing,
			check: func(t *testing.T) []Violation {
				return checkDocument(t, func(document map[string]any) {
					delete(waiverOf(t, document), "owner")
				})
			},
		},
		{
			name: "a required field that is blank",
			code: codeFieldEmpty,
			check: func(t *testing.T) []Violation {
				return checkDocument(t, func(document map[string]any) {
					waiverOf(t, document)["owner"] = "   "
				})
			},
		},
		{
			name:  "a list that is empty",
			code:  codeListEmpty,
			check: set("compensation", []any{}),
		},
		{
			name:  "a categories list that is empty",
			code:  codeListEmpty,
			check: set("categories", []any{}),
		},
		{
			name: "a version that is absent",
			code: codeFieldMissing,
			check: func(t *testing.T) []Violation {
				return checkDocument(t, func(document map[string]any) {
					delete(document, "schema_version")
				})
			},
		},
		{
			name: "a version this loader does not understand",
			code: codeSchemaVersion,
			check: func(t *testing.T) []Violation {
				return checkDocument(t, func(document map[string]any) {
					document["schema_version"] = 2
				})
			},
		},
		{
			name:  "an identifier outside the convention",
			code:  codeIDFormat,
			check: set("id", "waiver-1"),
		},
		{
			name: "an identifier that names two waivers",
			code: codeIDDuplicate,
			check: func(t *testing.T) []Violation {
				return checkDocument(t, func(document map[string]any) {
					waivers := document["waivers"].([]map[string]any)
					document["waivers"] = append(waivers, waivers[0])
				})
			},
		},
		{
			name:  "a risk class outside the vocabulary",
			code:  codeRiskInvalid,
			check: set("risk", "high"),
		},
		{
			name:  "a category outside the vocabulary",
			code:  codeCategoryUnknown,
			check: set("categories", []any{"documentation", "vibes"}),
		},
		{
			name: "a critical waiver in an area the phase refuses to trade",
			code: codeCategoryForbidden,
			check: func(t *testing.T) []Violation {
				return checkDocument(t, func(document map[string]any) {
					waiver := waiverOf(t, document)
					waiver["risk"] = "Q0"
					waiver["categories"] = []any{"security"}
					waiver["rules"] = []any{}
				})
			},
		},
		{
			name: "a waiver milder than the rule it suspends",
			code: codeRiskUnderstated,
			check: func(t *testing.T) []Violation {
				records := fixtureRecords(t)
				records.Rules["QUAL-REQ-I18N-01"] = "Q0"
				_, violations := Check(encode(t, fixtureDocument()), records, today)
				return violations
			},
		},
		{
			name:  "a rule the catalog does not declare",
			code:  codeRuleUnknown,
			check: set("rules", []any{"QUAL-REQ-NOWHERE-01"}),
		},
		{
			name:  "a finding no audit recorded",
			code:  codeFindingUnknown,
			check: set("finding", "SEC-99"),
		},
		{
			name:  "an owner that is a person",
			code:  codeOwnerAddress,
			check: set("owner", "ana.silva@example-business.com"),
		},
		{
			name:  "a compensation that does not resolve",
			code:  codeCompensationUnknown,
			check: set("compensation", []any{"internal/fixture/service/rules_test.go::TestThatWasRenamed"}),
		},
		{
			name:  "a compensation that is not a reference",
			code:  codeCompensationUnknown,
			check: set("compensation", []any{"we test it manually"}),
		},
		{
			name:  "a date outside the format",
			code:  codeDateInvalid,
			check: set("expires", "31/12/2027"),
		},
		{
			name:  "a window that runs backwards",
			code:  codeDateInvalid,
			check: set("expires", "2025-12-31"),
		},
		{
			name:  "a waiver that expired",
			code:  codeWaiverExpired,
			check: set("expires", "2026-05-31"),
		},
		{
			name:  "a justification that says nothing",
			code:  codeJustificationShort,
			check: set("justification", "temporary"),
		},
		{
			name: "a register that is not in the checkout",
			code: codeWaiversMissing,
			check: func(t *testing.T) []Violation {
				_, violations := ReadWaivers(t.TempDir(), "quality/waivers.json", fixtureRecords(t), today)
				return violations
			},
		},
		{
			name: "a register that lives outside the checkout",
			code: codeSourceUnknown,
			check: func(t *testing.T) []Violation {
				_, violations := ReadWaivers(fixtureTree(t), "../waivers.json", fixtureRecords(t), today)
				return violations
			},
		},
		{
			name: "a catalog the records cannot read",
			code: codeSourceUnknown,
			check: func(t *testing.T) []Violation {
				_, violations := ReadRecords(t.TempDir())
				return violations
			},
		},
		{
			name: "an audit with no machine-readable register",
			code: codeSourceUnknown,
			check: func(t *testing.T) []Violation {
				root := t.TempDir()
				writeFile(t, root, CatalogPath, `{"schema_version":1,"rules":[]}`)
				for _, path := range strings.Split(AuditDocuments, ",") {
					writeFile(t, root, path, "# Audit\n\nProse only, no register.\n")
				}
				_, violations := ReadRecords(root)
				return violations
			},
		},
		{
			name: "a schema that is not in the checkout",
			code: codeSchemaMissing,
			check: func(t *testing.T) []Violation {
				return ReadSchema(t.TempDir())
			},
		},
		{
			name: "a schema that is not JSON",
			code: codeSchemaInvalid,
			check: func(t *testing.T) []Violation {
				return schemaViolations(t, "{not json")
			},
		},
		{
			name: "a schema that describes no waivers array",
			code: codeSchemaInvalid,
			check: func(t *testing.T) []Violation {
				return schemaViolations(t, `{"properties":{"something":{"type":"array"}}}`)
			},
		},
		{
			name: "a schema whose reference resolves to nothing",
			code: codeSchemaInvalid,
			check: func(t *testing.T) []Violation {
				return schemaViolations(t, `{"properties":{"waivers":{"type":"array","items":{"$ref":"#/$defs/entry"}}}}`)
			},
		},
		{
			name: "a schema that requires a different set of fields",
			code: codeSchemaFields,
			check: func(t *testing.T) []Violation {
				schema := fixtureSchema()
				entry := schema["$defs"].(map[string]any)["waiver"].(map[string]any)
				entry["required"] = []any{"id", "finding"}
				return schemaViolationsOf(t, schema)
			},
		},
		{
			name: "a schema that admits keys the loader refuses",
			code: codeSchemaStrictness,
			check: func(t *testing.T) []Violation {
				schema := fixtureSchema()
				entry := schema["$defs"].(map[string]any)["waiver"].(map[string]any)
				entry["additionalProperties"] = true
				return schemaViolationsOf(t, schema)
			},
		},
		{
			name: "a schema whose risk vocabulary is not the loader's",
			code: codeSchemaVocabulary,
			check: func(t *testing.T) []Violation {
				schema := fixtureSchema()
				entry := schema["$defs"].(map[string]any)["waiver"].(map[string]any)
				properties := entry["properties"].(map[string]any)
				properties["risk"].(map[string]any)["enum"] = []any{"Q0", "Q1", "Q2", "Q3"}
				return schemaViolationsOf(t, schema)
			},
		},
		{
			name: "a schema whose categories are not the loader's",
			code: codeSchemaVocabulary,
			check: func(t *testing.T) []Violation {
				schema := fixtureSchema()
				entry := schema["$defs"].(map[string]any)["waiver"].(map[string]any)
				properties := entry["properties"].(map[string]any)
				items := properties["categories"].(map[string]any)["items"].(map[string]any)
				items["enum"] = []any{"documentation", "security"}
				return schemaViolationsOf(t, schema)
			},
		},
	}
}

// fixtureSchema is the schema the mutations above start from: the smallest one
// that agrees with the loader. A schema mutation that starts from a schema
// already refusing something would prove the wrong thing.
func fixtureSchema() map[string]any {
	categories := make([]any, 0, len(Categories))
	for _, category := range Categories {
		categories = append(categories, category)
	}
	risks := make([]any, 0, len(Risks))
	for _, risk := range Risks {
		risks = append(risks, risk)
	}
	fields := make([]any, 0, len(WaiverFields))
	for _, field := range WaiverFields {
		fields = append(fields, field)
	}
	return map[string]any{
		"properties": map[string]any{
			"waivers": map[string]any{
				"type":  "array",
				"items": map[string]any{"$ref": "#/$defs/waiver"},
			},
		},
		"$defs": map[string]any{
			"waiver": map[string]any{
				"required":             fields,
				"additionalProperties": false,
				"properties": map[string]any{
					"risk":       map[string]any{"enum": risks},
					"categories": map[string]any{"type": "array", "items": map[string]any{"enum": categories}},
				},
			},
		},
	}
}

// schemaViolations writes a schema into a synthetic root and judges it.
func schemaViolations(t *testing.T, raw string) []Violation {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, SchemaPath, raw)
	return ReadSchema(root)
}

// schemaViolationsOf is the same for a schema built as data.
func schemaViolationsOf(t *testing.T, schema map[string]any) []Violation {
	t.Helper()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("the fixture schema cannot be encoded: %v", err)
	}
	return schemaViolations(t, string(encoded))
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
	pattern := regexp.MustCompile(`(?m)^\s*code[A-Za-z]+\s*=\s*"([^"]+)"`)
	declared := map[string]string{}
	for _, source := range []string{"waivers.go", "records.go", "schema.go"} {
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

// TestTheOutputNeverCarriesTheRegisterText is the rule the phase states in one
// line: the gate prints identifiers, and the register's prose stays in the
// register. The waiver below is refused, so it is named in the output — and the
// marker in its justification and its owner must not be.
func TestTheOutputNeverCarriesTheRegisterText(t *testing.T) {
	const marker = "marker-that-must-not-be-printed"
	root := fixtureRoot(t, func(document map[string]any) {
		waiver := waiverOf(t, document)
		waiver["expires"] = "2026-05-31"
		waiver["owner"] = marker
		waiver["justification"] = marker + " and the reason it exists"
	})

	var stdout, stderr bytes.Buffer
	status := run([]string{"-root", root}, &stdout, &stderr, today)
	if status == exitOK {
		t.Fatalf("the expired fixture waiver was accepted: %s", stdout.String())
	}
	for name, output := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if strings.Contains(output, marker) {
			t.Fatalf("%s carries the register's own text: %s", name, output)
		}
	}
	if !strings.Contains(stderr.String(), "WVR-DOC-001") {
		t.Fatalf("stderr names no waiver id, so the failure cannot be acted on: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), codeWaiverExpired) {
		t.Fatalf("stderr names no code: %s", stderr.String())
	}
}

// TestTheDeliveredRegisterIsJudgedAsItIs holds the tool to the files in this
// checkout: the register as delivered, the catalog the rules come from, the
// audits the findings come from, and the published schema.
func TestTheDeliveredRegisterIsJudgedAsItIs(t *testing.T) {
	root := filepath.Join("..", "..")
	records, violations := ReadRecords(root)
	if len(violations) > 0 {
		t.Fatalf("the delivered records are unreadable: %v", violations)
	}
	if len(records.Rules) == 0 {
		t.Fatal("the delivered catalog declares no rule: a policy compared against nothing accepts everything")
	}
	if len(records.Findings) == 0 {
		t.Fatal("no delivered audit records a finding: a waiver could not accept anything, and the check would pass for the wrong reason")
	}
	register, violations := ReadWaivers(root, waiversPath, records, time.Now().UTC())
	if len(violations) > 0 {
		t.Fatalf("the delivered register is refused: %v", violations)
	}
	register, violations = Check(mustRead(t, root, waiversPath), records, time.Now().UTC())
	if len(violations) > 0 {
		t.Fatalf("the delivered register is refused: %v", violations)
	}
	if len(register.Waivers) != 0 {
		t.Fatalf("the delivered register holds %d waiver(s): this test exists to hold the empty one, and the day the first real waiver arrives it must be replaced by the judgement of that waiver", len(register.Waivers))
	}
	if violations := ReadSchema(root); len(violations) > 0 {
		t.Fatalf("the delivered schema does not agree with the loader: %v", violations)
	}
}

// fixtureRoot builds a whole checkout for the command: the register, the
// catalog, the audits and the schema, plus the tree the compensation lives in.
// The command is judged end to end here, and on a synthetic tree the failure of
// one document cannot be confused with the state of the repository.
func fixtureRoot(t *testing.T, mutate func(document map[string]any)) string {
	t.Helper()
	root := t.TempDir()
	document := fixtureDocument()
	if mutate != nil {
		mutate(document)
	}
	writeFile(t, root, waiversPath, string(encode(t, document)))
	writeFile(t, root, CatalogPath, `{"schema_version":1,"rules":[{"id":"QUAL-REQ-I18N-01","risk":"Q1"}]}`)
	writeFile(t, root, SchemaPath, string(mustEncode(t, fixtureSchema())))
	writeFile(t, root, "docs/SECURITY_AUDIT.md", "# Auditoria de segurança\n\n```json\n{\"version\":1,\"findings\":[{\"id\":\"SEC-01\"}]}\n```\n")
	writeFile(t, root, "docs/PRIVACY_AUDIT.md", "# Auditoria de privacidade\n\n```json\n{\"version\":1,\"findings\":[{\"id\":\"PRV-01\"}]}\n```\n")
	writeFile(t, root, "docs/I18N_AUDIT.md", "# Auditoria de i18n\n\n```json\n{\"version\":1,\"findings\":[{\"id\":\"I18N-01\"}]}\n```\n")
	writeFile(t, root, "internal/fixture/service/rules_test.go", "package service\n\nimport \"testing\"\n\nfunc TestPlaceholderStands(t *testing.T) {}\n")
	return root
}

// TestTheCommandJudgesARegister is the wiring: the command reads the register,
// the records and the schema, and reports the count it judged.
func TestTheCommandJudgesARegister(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := run([]string{"-root", fixtureRoot(t, nil)}, &stdout, &stderr, today)
	if status != exitOK {
		t.Fatalf("the command refused the complete fixture with status %d: %s%s", status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 waiver(s)") {
		t.Fatalf("the command did not report what it judged: %s", stdout.String())
	}
}

func hasCode(violations []Violation, code string) bool {
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
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("the fixture cannot be encoded: %v", err)
	}
	return data
}

func mustRead(t *testing.T, root, path string) []byte {
	t.Helper()
	data, err := readFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("the fixture file is unreadable: %v", err)
	}
	return data
}
