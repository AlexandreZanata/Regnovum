package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The violation codes. Each one says what was refused rather than where the
// file went wrong: a classification nobody can read is a test nobody can find
// when the rule it guards breaks.
const (
	codeEvidenceMissing    = "evidence-missing"
	codeEvidenceInvalid    = "evidence-invalid"
	codeFieldUnknown       = "field-unknown"
	codeFieldMissing       = "field-missing"
	codeFieldEmpty         = "field-empty"
	codeListEmpty          = "list-empty"
	codeSchemaVersion      = "schema-version"
	codeIDFormat           = "id-format"
	codeIDDuplicate        = "id-duplicate"
	codeKindUnknown        = "kind-unknown"
	codeEnvironmentUnknown = "environment-unknown"
	codeKindEnvironment    = "kind-environment"
	codeQ0WithoutOwner     = "q0-without-owner"
	codeSuiteUnknown       = "suite-unknown"
	codeRuleUnknown        = "rule-unknown"
	codeRiskMismatch       = "risk-mismatch"
	codeTestUnknown        = "test-unknown"
	codeSourceUnknown      = "source-unknown"
)

// SchemaVersion is the only register version this loader understands.
const SchemaVersion = 1

// Risks is the class vocabulary, the same one the catalog and the waivers use:
// three documents that meant three things by "critical" would make the word
// useless in all three.
var Risks = []string{"Q0", "Q1", "Q2"}

// The evidence kinds, exactly the eleven the phase names. They are a closed
// vocabulary and they are declared data: a kind is not read out of a function
// name, because the day a name changes is the day the classification would
// change with it.
const (
	KindUnit        = "unit"
	KindProperty    = "property"
	KindFuzz        = "fuzz"
	KindIntegration = "integration"
	KindContract    = "contract"
	KindE2E         = "e2e"
	KindSecurity    = "security"
	KindMutation    = "mutation"
	KindPerformance = "performance"
	KindChaos       = "chaos"
	KindRecovery    = "recovery"
)

// Kinds is the whole vocabulary, in the order the phase lists it.
var Kinds = []string{
	KindUnit, KindProperty, KindFuzz, KindIntegration, KindContract, KindE2E,
	KindSecurity, KindMutation, KindPerformance, KindChaos, KindRecovery,
}

// The environments an evidence can run in: what the machine has to have for the
// test to mean anything. They are as closed as the kinds, and for the same
// reason — "ambiente" is what a reader needs in order to reproduce a failure.
const (
	EnvironmentUnit     = "unit"     // Go test, no service
	EnvironmentPostgres = "postgres" // a real, disposable PostgreSQL 18
	EnvironmentBrowser  = "browser"  // Chromium through tools/e2e
	EnvironmentDocker   = "docker"   // the daemon: images, compose, ingress, backups
	EnvironmentNetwork  = "network"  // a local fake provider, or a served instance
)

// Environments is the whole vocabulary.
var Environments = []string{
	EnvironmentUnit, EnvironmentPostgres, EnvironmentBrowser, EnvironmentDocker, EnvironmentNetwork,
}

// Labels is the human name of each kind, for the reports the inventory will
// print. The table is here, next to the vocabulary, so that a kind without a
// label is a test failure rather than a report that reads "unit" in a document
// written for people.
var Labels = map[string]string{
	KindUnit:        "Teste unitário",
	KindProperty:    "Teste de propriedade",
	KindFuzz:        "Teste de fuzz",
	KindIntegration: "Teste de integração",
	KindContract:    "Teste de contrato",
	KindE2E:         "Jornada de navegador",
	KindSecurity:    "Teste de segurança",
	KindMutation:    "Teste de mutação",
	KindPerformance: "Teste de desempenho",
	KindChaos:       "Exercício de caos",
	KindRecovery:    "Exercício de recuperação",
}

// KindEnvironments states where each kind runs. A kind that could run anywhere
// would say nothing: classifying an integration test as a unit test is exactly
// the mislabel this table refuses, and it refuses it without reading a single
// function name.
var KindEnvironments = map[string][]string{
	KindUnit:        {EnvironmentUnit},
	KindProperty:    {EnvironmentUnit},
	KindFuzz:        {EnvironmentUnit},
	KindIntegration: {EnvironmentPostgres, EnvironmentDocker},
	KindContract:    {EnvironmentUnit},
	KindE2E:         {EnvironmentBrowser},
	KindSecurity:    {EnvironmentUnit, EnvironmentPostgres, EnvironmentNetwork},
	KindMutation:    {EnvironmentUnit, EnvironmentPostgres},
	KindPerformance: {EnvironmentNetwork, EnvironmentBrowser},
	KindChaos:       {EnvironmentDocker},
	KindRecovery:    {EnvironmentDocker},
}

// SuiteFields and EvidenceFields are the keys each row declares. The lists are
// also the schema's `required` arrays, and the schema agreement below holds the
// two together.
// SuiteFields are the keys a suite may declare, and SuiteRequired the ones it
// must: `owner` is not required of every suite, because the phase requires an
// owner of the suite that proves a critical rule and of nothing else. A unit
// suite that proves no Q0 rule is allowed to be anonymous; one that does is
// not, and that condition cannot be said in a `required` array — it is the
// q0-without-owner refusal below.
var (
	SuiteFields      = []string{"id", "owner", "environment", "command", "modules"}
	SuiteRequired    = []string{"id", "environment", "command", "modules"}
	EvidenceFields   = []string{"id", "kind", "suite", "risk", "rules", "tests", "proves"}
	EvidenceRequired = EvidenceFields
)

var (
	suiteIDPattern    = regexp.MustCompile(`^SUITE-[A-Z0-9]+(-[A-Z0-9]+)*$`)
	evidenceIDPattern = regexp.MustCompile(`^EVD-[A-Z0-9]+(-[A-Z0-9]+)*$`)
	riskPattern       = regexp.MustCompile(`^Q[012]$`)
)

// Suite is where a set of evidence runs, who answers for it and what it needs.
type Suite struct {
	ID          string   `json:"id"`
	Owner       string   `json:"owner"`
	Environment string   `json:"environment"`
	Command     string   `json:"command"`
	Modules     []string `json:"modules"`
}

// Evidence is one identity: what kind of test it is, in which suite it runs,
// which rules it proves, in which class, and the tests that carry it.
type Evidence struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Suite  string   `json:"suite"`
	Risk   string   `json:"risk"`
	Rules  []string `json:"rules"`
	Tests  []string `json:"tests"`
	Proves string   `json:"proves"`
}

// Register is the whole document.
type Register struct {
	SchemaVersion int        `json:"schema_version"`
	Suites        []Suite    `json:"suites"`
	Evidence      []Evidence `json:"evidence"`
}

// Violation is one refusal, named by the row it belongs to.
type Violation struct {
	Row    string
	Code   string
	Detail string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: %s: %s", v.Row, v.Code, v.Detail)
}

// Violations is the list, with the sort and the summary the command prints.
type Violations []Violation

// Records is what the register resolves against: the catalog's own classes and
// the tree the tests it cites have to exist in.
type Records struct {
	Rules map[string]string // catalog rule id -> risk class
	Root  string
}

// ReadRegister reads the committed register and judges it. A register that is
// not there is a refusal: a taxonomy nobody declared is a taxonomy nobody uses.
func ReadRegister(root, path string, records Records) (Register, Violations) {
	full, ok := safeJoin(root, path)
	if !ok {
		return Register{}, Violations{{
			Row:    "document",
			Code:   codeSourceUnknown,
			Detail: fmt.Sprintf("`%s` is not a path inside the checkout the register is judged against", path),
		}}
	}
	document, err := os.ReadFile(full)
	if err != nil {
		return Register{}, Violations{{
			Row:    "document",
			Code:   codeEvidenceMissing,
			Detail: fmt.Sprintf("`%s` is not in the checkout: a classification nobody declared is a classification nobody can check", path),
		}}
	}
	return Check(document, records)
}

// Check decodes the register strictly and judges every row of it.
func Check(document []byte, records Records) (Register, Violations) {
	var violations Violations
	header, violations := readHeader(document)
	if len(violations) > 0 {
		return Register{}, violations
	}
	register := Register{SchemaVersion: header.version}
	suites := map[string]Suite{}
	seen := map[string]bool{}
	for index, raw := range header.Suites {
		suite, trouble, ok := readSuite(raw, index)
		violations = append(violations, trouble...)
		if !ok {
			continue
		}
		if seen[suite.ID] {
			violations = append(violations, Violation{Row: suite.ID, Code: codeIDDuplicate, Detail: "the identifier is already declared: an id that names two suites names none"})
			continue
		}
		seen[suite.ID] = true
		suites[suite.ID] = suite
		register.Suites = append(register.Suites, suite)
	}
	for index, raw := range header.Evidence {
		evidence, trouble, ok := readEvidence(raw, index)
		violations = append(violations, trouble...)
		if !ok {
			continue
		}
		if seen[evidence.ID] {
			violations = append(violations, Violation{Row: evidence.ID, Code: codeIDDuplicate, Detail: "the identifier is already declared: an id that names two rows names none"})
			continue
		}
		seen[evidence.ID] = true
		violations = append(violations, judge(evidence, suites, records)...)
		register.Evidence = append(register.Evidence, evidence)
	}
	violations = append(violations, relationships(register)...)
	sortViolations(violations)
	return register, violations
}

// header keeps the rows as raw JSON on purpose: presence and emptiness are
// different refusals, and a decoded struct cannot tell an absent field from an
// empty one. The version is kept raw for the same reason and decoded into
// `version` by readHeader.
type header struct {
	SchemaVersion json.RawMessage   `json:"schema_version"`
	Suites        []json.RawMessage `json:"suites"`
	Evidence      []json.RawMessage `json:"evidence"`
	version       int
}

func readHeader(document []byte) (header, Violations) {
	var decoded header
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return header{}, Violations{{
			Row:    "document",
			Code:   decodeCode(err),
			Detail: fmt.Sprintf("the document does not decode as an evidence register: %v", err),
		}}
	}
	if decoded.SchemaVersion == nil {
		return header{}, Violations{{
			Row:    "document",
			Code:   codeFieldMissing,
			Detail: "`schema_version` is absent: a document that does not say which contract it keeps cannot be judged",
		}}
	}
	var version int
	if err := json.Unmarshal(decoded.SchemaVersion, &version); err != nil {
		return header{}, Violations{{
			Row:    "document",
			Code:   codeSchemaVersion,
			Detail: fmt.Sprintf("`schema_version` is not a number: %v", err),
		}}
	}
	if version != SchemaVersion {
		return header{}, Violations{{
			Row:    "document",
			Code:   codeSchemaVersion,
			Detail: fmt.Sprintf("`schema_version` is %d and this loader understands %d", version, SchemaVersion),
		}}
	}
	decoded.version = version
	return decoded, nil
}

// readSuite decodes one suite strictly and refuses what is absent or blank.
func readSuite(raw json.RawMessage, index int) (Suite, Violations, bool) {
	var suite Suite
	if err := decodeRow(raw, &suite); err != nil {
		return Suite{}, Violations{{Row: fmt.Sprintf("suite[%d]", index), Code: decodeCode(err), Detail: fmt.Sprintf("the row does not decode as a suite: %v", err)}}, false
	}
	row := suite.ID
	if row == "" {
		row = fmt.Sprintf("suite[%d]", index)
	}
	var violations Violations
	present, ok := presentFields(raw)
	if !ok {
		return Suite{}, Violations{{Row: row, Code: codeEvidenceInvalid, Detail: "the row does not decode as an object"}}, false
	}
	name := func(code, detail string) {
		violations = append(violations, Violation{Row: row, Code: code, Detail: detail})
	}
	for _, field := range SuiteRequired {
		if _, declared := present[field]; !declared {
			name(codeFieldMissing, fmt.Sprintf("`%s` is absent: every suite states it or the suite is incomplete", field))
		}
	}
	for _, text := range []struct{ field, value string }{
		{"id", suite.ID},
		{"environment", suite.Environment},
		{"command", suite.Command},
	} {
		if _, declared := present[text.field]; declared && strings.TrimSpace(text.value) == "" {
			name(codeFieldEmpty, fmt.Sprintf("`%s` is blank: an empty required field reads as a filled one to whoever skims it", text.field))
		}
	}
	if _, declared := present["modules"]; declared && len(suite.Modules) == 0 {
		name(codeListEmpty, "`modules` is empty: a suite that names no module is a suite nobody can find")
	}
	if !suiteIDPattern.MatchString(suite.ID) {
		name(codeIDFormat, fmt.Sprintf("`%s` is not a suite identifier: the convention is SUITE-<MÓDULO>-<AMBIENTE>", suite.ID))
	}
	if suite.Environment != "" && !contains(Environments, suite.Environment) {
		name(codeEnvironmentUnknown, fmt.Sprintf("`%s` is not an environment: the vocabulary is %s", suite.Environment, strings.Join(Environments, ", ")))
	}
	if len(violations) > 0 {
		sortViolations(violations)
		return Suite{}, violations, false
	}
	return suite, nil, true
}

// readEvidence decodes one evidence row strictly and refuses what is absent or
// blank.
func readEvidence(raw json.RawMessage, index int) (Evidence, Violations, bool) {
	var evidence Evidence
	if err := decodeRow(raw, &evidence); err != nil {
		return Evidence{}, Violations{{Row: fmt.Sprintf("evidence[%d]", index), Code: decodeCode(err), Detail: fmt.Sprintf("the row does not decode as evidence: %v", err)}}, false
	}
	row := evidence.ID
	if row == "" {
		row = fmt.Sprintf("evidence[%d]", index)
	}
	var violations Violations
	present, ok := presentFields(raw)
	if !ok {
		return Evidence{}, Violations{{Row: row, Code: codeEvidenceInvalid, Detail: "the row does not decode as an object"}}, false
	}
	name := func(code, detail string) {
		violations = append(violations, Violation{Row: row, Code: code, Detail: detail})
	}
	for _, field := range EvidenceRequired {
		if _, declared := present[field]; !declared {
			name(codeFieldMissing, fmt.Sprintf("`%s` is absent: every evidence states it or the identity is incomplete", field))
		}
	}
	for _, text := range []struct{ field, value string }{
		{"id", evidence.ID},
		{"kind", evidence.Kind},
		{"suite", evidence.Suite},
		{"risk", evidence.Risk},
		{"proves", evidence.Proves},
	} {
		if _, declared := present[text.field]; declared && strings.TrimSpace(text.value) == "" {
			name(codeFieldEmpty, fmt.Sprintf("`%s` is blank: an empty required field reads as a filled one to whoever skims it", text.field))
		}
	}
	for _, list := range []struct {
		field string
		items []string
	}{
		{"rules", evidence.Rules},
		{"tests", evidence.Tests},
	} {
		if _, declared := present[list.field]; declared && len(list.items) == 0 {
			name(codeListEmpty, fmt.Sprintf("`%s` is empty: evidence that proves no rule and cites no test is a label, not an identity", list.field))
		}
	}
	if !evidenceIDPattern.MatchString(evidence.ID) {
		name(codeIDFormat, fmt.Sprintf("`%s` is not an evidence identifier: the convention is EVD-<MÓDULO>-<TIPO>-<NN>", evidence.ID))
	}
	if evidence.Kind != "" && !contains(Kinds, evidence.Kind) {
		name(codeKindUnknown, fmt.Sprintf("`%s` is not a kind: the vocabulary is %s", evidence.Kind, strings.Join(Kinds, ", ")))
	}
	if evidence.Risk != "" && !riskPattern.MatchString(evidence.Risk) {
		name(codeRiskMismatch, fmt.Sprintf("`%s` is not a risk class: the vocabulary is Q0, Q1, Q2, the same one the catalog uses", evidence.Risk))
	}
	if len(violations) > 0 {
		sortViolations(violations)
		return Evidence{}, violations, false
	}
	return evidence, nil, true
}

// judge is where one evidence row is held to the taxonomy and to the records.
func judge(evidence Evidence, suites map[string]Suite, records Records) Violations {
	var violations Violations
	refuse := func(code, detail string) {
		violations = append(violations, Violation{Row: evidence.ID, Code: code, Detail: detail})
	}

	suite, ok := suites[evidence.Suite]
	if !ok {
		refuse(codeSuiteUnknown, fmt.Sprintf("`%s` is not a declared suite: evidence that runs nowhere cannot be reproduced", evidence.Suite))
	} else if admitted := KindEnvironments[evidence.Kind]; ok && !contains(admitted, suite.Environment) {
		refuse(codeKindEnvironment, fmt.Sprintf("a `%s` evidence runs in %s and the suite declares `%s`: classifying a test as something it is not is the mislabel the taxonomy exists to refuse", evidence.Kind, strings.Join(admitted, " or "), suite.Environment))
	}

	strictest := ""
	for _, rule := range evidence.Rules {
		class, found := records.Rules[rule]
		if !found {
			refuse(codeRuleUnknown, fmt.Sprintf("`%s` is not a rule of the catalog: evidence for a rule nobody declared classifies nothing", rule))
			continue
		}
		if rank(class) > rank(strictest) {
			strictest = class
		}
		if class == "Q0" && ok && strings.TrimSpace(suite.Owner) == "" {
			refuse(codeQ0WithoutOwner, fmt.Sprintf("`%s` is critical and the suite `%s` declares no owner: a critical rule whose suite nobody answers for is a rule nobody revisits", rule, suite.ID))
		}
	}
	if strictest != "" && evidence.Risk != strictest {
		refuse(codeRiskMismatch, fmt.Sprintf("the row declares `%s` and the rules it proves are `%s`: the class is read from the catalog, so the declaration cannot soften it", evidence.Risk, strictest))
	}

	for _, reference := range evidence.Tests {
		if !resolves(records.Root, reference) {
			refuse(codeTestUnknown, fmt.Sprintf("`%s` does not resolve to a test that exists: an identity that names a test nobody can run is a label", reference))
		}
	}
	return violations
}

// relationships judges the register as a whole: a suite no evidence declares is
// a suite the classification does not cover, and it is refused rather than left
// as a row somebody will keep updating for no reason.
func relationships(register Register) Violations {
	var violations Violations
	used := map[string]int{}
	for _, evidence := range register.Evidence {
		used[evidence.Suite]++
	}
	for _, suite := range register.Suites {
		if used[suite.ID] == 0 {
			violations = append(violations, Violation{
				Row:    suite.ID,
				Code:   codeSuiteUnknown,
				Detail: "no evidence declares this suite: a suite nobody classified is a suite the taxonomy does not cover",
			})
		}
	}
	return violations
}

// decodeRow decodes one row strictly: an unknown key is refused rather than
// ignored, because a row written against a newer contract is a row this loader
// does not understand.
func decodeRow(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// presentFields reports which keys a row actually declares.
func presentFields(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return nil, false
	}
	return present, true
}

func decodeCode(err error) string {
	if strings.Contains(err.Error(), "unknown field") {
		return codeFieldUnknown
	}
	return codeEvidenceInvalid
}

// resolves reports whether a reference names something that exists: a Go test
// (`path::Function`, the function declared in the file) or a file of another
// language, which is how the browser journeys are cited.
func resolves(root, reference string) bool {
	path, name, qualified := strings.Cut(reference, "::")
	if path == "" {
		return false
	}
	full, ok := safeJoin(root, path)
	if !ok {
		return false
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return false
	}
	if !qualified {
		return true
	}
	if name == "" || !strings.HasSuffix(path, ".go") {
		return false
	}
	declared := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(`)
	return declared.Match(raw)
}

// rank orders the classes so that "strictest" is a comparison rather than a
// sentence: Q0 is the critical class and the highest rank, and a class the
// vocabulary does not declare ranks below every other rather than above them.
// The direction matters more than the number: an unknown value that compared as
// the strictest would silence the comparison it is part of, and it would do it
// by accident, on the first rule of the first row.
func rank(class string) int {
	position := index(Risks, class)
	if position < 0 {
		return 0
	}
	return len(Risks) - position
}

func index(values []string, wanted string) int {
	for position, value := range values {
		if value == wanted {
			return position
		}
	}
	return -1
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sortViolations(violations Violations) {
	sort.Slice(violations, func(one, other int) bool {
		if violations[one].Row != violations[other].Row {
			return violations[one].Row < violations[other].Row
		}
		if violations[one].Code != violations[other].Code {
			return violations[one].Code < violations[other].Code
		}
		return violations[one].Detail < violations[other].Detail
	})
}

// safeJoin turns a repository-relative path into an absolute one, refusing the
// two ways a document can leave the tree it is judged against.
func safeJoin(root, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return filepath.Join(root, filepath.FromSlash(clean)), true
}
