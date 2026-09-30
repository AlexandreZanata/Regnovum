package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The violation codes. They are the vocabulary the tests name and the operator
// reads, so each one says what was refused rather than where the file went
// wrong. A code that could only be produced by one document and not another is
// a code that hides a class of defect.
const (
	codeCatalogInvalid      = "catalog-invalid"
	codeCatalogEmpty        = "catalog-empty"
	codeCatalogMissing      = "catalog-missing"
	codeFieldUnknown        = "field-unknown"
	codeFieldMissing        = "field-missing"
	codeFieldEmpty          = "field-empty"
	codeListEmpty           = "list-empty"
	codeSchemaVersion       = "schema-version"
	codeIDFormat            = "id-format"
	codeIDDuplicate         = "id-duplicate"
	codeRiskInvalid         = "risk-invalid"
	codeSourceUnknown       = "source-unknown"
	codeModuleUnknown       = "module-unknown"
	codeTestFormat          = "test-format"
	codeTestUnknown         = "test-unknown"
	codeTestCategoryMissing = "test-category-missing"
	codeReasonUnknown       = "not-applicable-unknown"
	codeReasonEmpty         = "not-applicable-reason"
	codeReasonConflict      = "not-applicable-conflict"
	codeEvidenceUnknown     = "evidence-unknown"
)

// SchemaVersion is the only catalog version this loader understands. Two
// versions of a contract that both answer "fine" are worse than one version
// that refuses.
const SchemaVersion = 1

// The risk classes, exactly the vocabulary of the quality program.
const (
	RiskCritical = "Q0"
	RiskHigh     = "Q1"
	RiskNormal   = "Q2"
)

// RuleFields are the keys every rule declares. The list is also the schema's
// `required` array for a rule, and SchemaAgreement holds the two together: a
// loader that demanded less than the schema promises would accept a document
// the schema calls invalid.
var RuleFields = []string{
	"id",
	"source",
	"description",
	"risk",
	"modules",
	"actors",
	"states",
	"tests",
	"authorization",
	"concurrency",
	"idempotency",
	"security",
	"privacy",
	"evidence",
}

// TestCategories are the evidence slots a rule can fill.
var TestCategories = []string{
	"positive",
	"negative",
	"limit",
	"authorization",
	"concurrency",
	"idempotency",
	"failure",
}

// RequiredTestCategories are the slots every rule fills whatever its risk
// class is: a rule with no positive and no negative scenario is a sentence,
// not a rule.
var RequiredTestCategories = []string{"positive", "negative"}

// RiskClasses are the accepted risk values, in the order of the program.
var RiskClasses = []string{RiskCritical, RiskHigh, RiskNormal}

// idPattern is the identity convention: a QUAL- namespace, uppercase segments,
// no spaces. The id is what a waiver and an inventory row are keyed by, so its
// shape is checked here and never renumbered later.
var idPattern = regexp.MustCompile(`^QUAL-[A-Z0-9]+(-[A-Z0-9]+)*$`)

// funcPattern is how a Go test declares itself.
var funcPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Catalog is the whole document.
type Catalog struct {
	SchemaVersion int    `json:"schema_version"`
	Rules         []Rule `json:"rules"`
}

// Rule is one approved rule of the product with the evidence it demands.
type Rule struct {
	ID            string            `json:"id"`
	Source        string            `json:"source"`
	Description   string            `json:"description"`
	Risk          string            `json:"risk"`
	Modules       []string          `json:"modules"`
	Actors        []string          `json:"actors"`
	States        []string          `json:"states"`
	Tests         TestSlots         `json:"tests"`
	Authorization string            `json:"authorization"`
	Concurrency   string            `json:"concurrency"`
	Idempotency   string            `json:"idempotency"`
	Security      string            `json:"security"`
	Privacy       string            `json:"privacy"`
	Evidence      []string          `json:"evidence"`
	NotApplicable map[string]string `json:"not_applicable"`
}

// TestSlots is the evidence of one rule, by category.
type TestSlots struct {
	Positive      []string `json:"positive"`
	Negative      []string `json:"negative"`
	Limit         []string `json:"limit"`
	Authorization []string `json:"authorization"`
	Concurrency   []string `json:"concurrency"`
	Idempotency   []string `json:"idempotency"`
	Failure       []string `json:"failure"`
}

// slot is one category with the references that fill it.
type slot struct {
	name string
	refs []string
}

// list returns the categories in a fixed order, so two runs of the loader over
// the same document produce the same report.
func (s TestSlots) list() []slot {
	return []slot{
		{"positive", s.Positive},
		{"negative", s.Negative},
		{"limit", s.Limit},
		{"authorization", s.Authorization},
		{"concurrency", s.Concurrency},
		{"idempotency", s.Idempotency},
		{"failure", s.Failure},
	}
}

// profile is the evidence a risk class demands. The statements are demanded of
// every rule regardless of class; the categories grow with the risk, and a
// category that cannot be filled needs a written reason instead.
type profile struct {
	categories []string
	statements []string
}

var profiles = map[string]profile{
	RiskCritical: {
		categories: []string{"positive", "negative", "limit", "authorization", "concurrency", "idempotency"},
		statements: []string{"authorization", "concurrency", "idempotency"},
	},
	RiskHigh: {
		categories: []string{"positive", "negative", "limit"},
		statements: nil,
	},
	RiskNormal: {
		categories: []string{"positive", "negative"},
		statements: nil,
	},
}

// universalStatements are demanded of every rule whatever its class. A rule
// whose security or privacy meaning is unstated cannot be reviewed, and the
// cheapest way to keep it reviewable is to refuse the silence.
var universalStatements = []string{"security", "privacy"}

// Violation is one refusal, named by the rule it belongs to so the report
// reads as a list of defects and not as a list of lines.
type Violation struct {
	Rule   string
	Code   string
	Detail string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: %s: %s", v.Rule, v.Code, v.Detail)
}

// Tree is the checkout a catalog is judged against. Every path question the
// loader asks goes through here, so "this path exists" has one definition and
// a caller cannot answer it two ways.
type Tree struct {
	Root string
}

// resolve turns a repository-relative path into an absolute one, refusing the
// two ways a document can leave the tree it is judged against: an absolute
// path, and a parent segment. A catalog that cites ../secrets is not a catalog
// this loader will follow.
func (t Tree) resolve(rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return filepath.Join(t.Root, filepath.FromSlash(clean)), true
}

// read returns the bytes of a file inside the tree.
func (t Tree) read(rel string) ([]byte, bool) {
	abs, ok := t.resolve(rel)
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, false
	}
	return data, true
}

// exists reports whether the path is a file or a directory of the tree.
func (t Tree) exists(rel string) bool {
	abs, ok := t.resolve(rel)
	if !ok {
		return false
	}
	_, err := os.Stat(abs)
	return err == nil
}

// isDir reports whether the path is a directory of the tree.
func (t Tree) isDir(rel string) bool {
	abs, ok := t.resolve(rel)
	if !ok {
		return false
	}
	info, err := os.Stat(abs)
	return err == nil && info.IsDir()
}

// ReadCatalog reads a catalog inside the tree and judges it. A catalog that is
// not there is a refusal like any other: an absent document and an empty one
// both verify nothing, and a gate that treats absence as success is a gate that
// disappears along with the file it guards.
func ReadCatalog(root, path string) (Catalog, []Violation) {
	tree := Tree{Root: root}
	document, ok := tree.read(path)
	if !ok {
		return Catalog{}, []Violation{{
			Rule:   "catalog",
			Code:   codeCatalogMissing,
			Detail: fmt.Sprintf("`%s` is not in the checkout: a catalog nobody can open verifies nothing", path),
		}}
	}
	return Check(document, tree)
}

// Check judges the catalog document against the tree. It returns the parsed
// catalog — zero when the document could not be read at all — and every
// violation found. An empty slice means the catalog stands.
func Check(document []byte, tree Tree) (Catalog, []Violation) {
	var catalog Catalog

	header, violations := readHeader(document)
	if len(violations) > 0 {
		return catalog, violations
	}
	catalog.SchemaVersion = *header.SchemaVersion

	seen := map[string]bool{}
	for index, raw := range header.Rules {
		rule, violationsOfRule := readRule(raw, index, seen, tree)
		violations = append(violations, violationsOfRule...)
		catalog.Rules = append(catalog.Rules, rule)
	}
	return catalog, violations
}

// header is what the document says before any rule is read: the version and
// the raw rules, kept raw so a rule can be judged field by field.
type header struct {
	SchemaVersion *int              `json:"schema_version"`
	Rules         []json.RawMessage `json:"rules"`
}

// readHeader decodes the document strictly. The rules stay as raw JSON on
// purpose: presence and emptiness are different refusals, and a decoded struct
// cannot tell a missing key from an empty one.
func readHeader(document []byte) (header, []Violation) {
	var header header
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&header); err != nil {
		return header, []Violation{{
			Rule:   "catalog",
			Code:   decodeCode(err),
			Detail: fmt.Sprintf("the document does not decode as a catalog: %v", err),
		}}
	}
	if decoder.More() {
		return header, []Violation{{
			Rule:   "catalog",
			Code:   codeCatalogInvalid,
			Detail: "the document carries a second JSON value after the catalog: the loader would judge the first and ignore the rest",
		}}
	}
	if header.SchemaVersion == nil {
		return header, []Violation{{
			Rule:   "catalog",
			Code:   codeFieldMissing,
			Detail: "`schema_version` is absent: a document that does not state its version cannot be judged against one",
		}}
	}
	if *header.SchemaVersion != SchemaVersion {
		return header, []Violation{{
			Rule:   "catalog",
			Code:   codeSchemaVersion,
			Detail: fmt.Sprintf("schema_version %d is not the version this loader implements (%d)", *header.SchemaVersion, SchemaVersion),
		}}
	}
	if header.Rules == nil {
		return header, []Violation{{
			Rule:   "catalog",
			Code:   codeFieldMissing,
			Detail: "`rules` is absent: the catalog has no rules to judge",
		}}
	}
	if len(header.Rules) == 0 {
		return header, []Violation{{
			Rule:   "catalog",
			Code:   codeCatalogEmpty,
			Detail: "the catalog holds no rule: an empty catalog verifies nothing and is refused rather than accepted as vacuously true",
		}}
	}
	return header, nil
}

// readRule decodes and judges one rule.
func readRule(raw json.RawMessage, index int, seen map[string]bool, tree Tree) (Rule, []Violation) {
	var rule Rule
	name := fmt.Sprintf("rules[%d]", index)

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rule); err != nil {
		return rule, []Violation{{
			Rule:   name,
			Code:   decodeCode(err),
			Detail: fmt.Sprintf("the rule does not decode: %v", err),
		}}
	}

	presence, err := fieldsOf(raw)
	if err != nil {
		return rule, []Violation{{
			Rule:   name,
			Code:   codeCatalogInvalid,
			Detail: fmt.Sprintf("the rule is not a JSON object: %v", err),
		}}
	}
	if id, ok := presence["id"]; ok {
		name = string(id)
	}

	var violations []Violation
	refuse := func(code, detail string) {
		violations = append(violations, Violation{Rule: name, Code: code, Detail: detail})
	}

	// Presence first: a key that is absent is a different refusal from a key
	// that is present and blank, and the operator has to fix them differently.
	for _, field := range RuleFields {
		if _, ok := presence[field]; !ok {
			refuse(codeFieldMissing, fmt.Sprintf("`%s` is absent: every rule states it or the rule is incomplete", field))
		}
	}

	for _, text := range []struct {
		field string
		value string
	}{
		{"id", rule.ID},
		{"source", rule.Source},
		{"description", rule.Description},
		{"risk", rule.Risk},
		{"authorization", rule.Authorization},
		{"concurrency", rule.Concurrency},
		{"idempotency", rule.Idempotency},
		{"security", rule.Security},
		{"privacy", rule.Privacy},
	} {
		if strings.TrimSpace(text.value) == "" {
			refuse(codeFieldEmpty, fmt.Sprintf("`%s` is blank: an empty required field reads as a filled one to whoever skims it", text.field))
		}
	}

	for _, list := range []struct {
		field string
		items []string
	}{
		{"modules", rule.Modules},
		{"actors", rule.Actors},
		{"states", rule.States},
		{"evidence", rule.Evidence},
	} {
		if len(list.items) == 0 {
			refuse(codeListEmpty, fmt.Sprintf("`%s` is empty: a rule that names no %s names no one", list.field, list.field))
		}
	}

	if rule.ID != "" && !idPattern.MatchString(rule.ID) {
		refuse(codeIDFormat, fmt.Sprintf("`%s` is not a stable id: the convention is QUAL-<SEGMENT>[-<SEGMENT>] in uppercase", rule.ID))
	}
	if rule.ID != "" {
		if seen[rule.ID] {
			refuse(codeIDDuplicate, fmt.Sprintf("`%s` is already declared: a duplicated id is two rules a waiver cannot tell apart", rule.ID))
		}
		seen[rule.ID] = true
	}

	if _, ok := profiles[rule.Risk]; !ok && strings.TrimSpace(rule.Risk) != "" {
		refuse(codeRiskInvalid, fmt.Sprintf("risk `%s` is not one of %s", rule.Risk, strings.Join(RiskClasses, ", ")))
	}

	if source := strings.TrimSpace(rule.Source); source != "" {
		path := source
		if cut, anchor, found := strings.Cut(source, "#"); found {
			path = cut
			if anchor == "" {
				refuse(codeSourceUnknown, fmt.Sprintf("`%s` names an empty anchor: a citation that points nowhere in particular cites nothing", source))
			}
		}
		if !tree.exists(path) {
			refuse(codeSourceUnknown, fmt.Sprintf("`%s` is not in the checkout: a rule that cites a document nobody can open cites a rumour", path))
		}
	}

	for _, module := range rule.Modules {
		if strings.TrimSpace(module) == "" {
			refuse(codeFieldEmpty, "`modules` holds a blank entry")
			continue
		}
		if !tree.isDir(module) {
			refuse(codeModuleUnknown, fmt.Sprintf("`%s` is not a directory of the checkout: a module that does not exist owns nothing", module))
		}
	}

	violations = append(violations, judgeTests(name, rule, tree, presence)...)
	violations = append(violations, judgeReasons(name, rule, presence)...)

	for _, evidence := range rule.Evidence {
		if !tree.exists(evidence) {
			refuse(codeEvidenceUnknown, fmt.Sprintf("`%s` is not in the checkout: evidence nobody can open is not evidence", evidence))
		}
	}
	return rule, violations
}

// judgeTests checks the evidence slots: the format of every reference, that
// the reference resolves, and that the categories the risk class demands are
// either filled or explained.
func judgeTests(label string, rule Rule, tree Tree, presence map[string]json.RawMessage) []Violation {
	var violations []Violation
	decline := func(code, detail string) {
		violations = append(violations, Violation{Rule: label, Code: code, Detail: detail})
	}

	if raw, ok := presence["tests"]; ok {
		fields, err := fieldsOf(raw)
		if err != nil {
			decline(codeCatalogInvalid, fmt.Sprintf("`tests` is not a JSON object: %v", err))
			return violations
		}
		for _, category := range RequiredTestCategories {
			if _, ok := fields[category]; !ok {
				decline(codeFieldMissing, fmt.Sprintf("`tests.%s` is absent: every rule states it, even when the list is empty", category))
			}
		}
	}

	filled := map[string]bool{}
	for _, category := range rule.Tests.list() {
		filled[category.name] = len(category.refs) > 0
		for _, reference := range category.refs {
			path, name, ok := splitReference(reference)
			if !ok {
				decline(codeTestFormat, fmt.Sprintf("`%s` is not `path::Function`: a reference the loader cannot resolve is a reference nobody can run", reference))
				continue
			}
			data, ok := tree.read(path)
			if !ok {
				decline(codeTestUnknown, fmt.Sprintf("`%s` of `%s` is not in the checkout", path, category.name))
				continue
			}
			if !declares(data, path, name) {
				decline(codeTestUnknown, fmt.Sprintf("`%s` does not declare `%s`: the file exists and the test it is cited for does not", path, name))
			}
		}
	}

	if class, ok := profiles[rule.Risk]; ok {
		for _, category := range class.categories {
			if filled[category] {
				continue
			}
			if _, explained := rule.NotApplicable[category]; explained {
				continue
			}
			decline(codeTestCategoryMissing, fmt.Sprintf("risk %s requires `%s`: fill the slot with a test or state in `not_applicable` why the rule cannot have one", rule.Risk, category))
		}
		for _, statement := range class.statements {
			if strings.TrimSpace(statementOf(rule, statement)) == "" {
				decline(codeFieldEmpty, fmt.Sprintf("risk %s requires the `%s` statement to say what the rule does about it", rule.Risk, statement))
			}
		}
	}
	for _, statement := range universalStatements {
		if strings.TrimSpace(statementOf(rule, statement)) == "" {
			decline(codeFieldEmpty, fmt.Sprintf("the `%s` statement is blank: every rule says what it protects", statement))
		}
	}
	return violations
}

// judgeReasons checks the written reasons a rule gives for a category it
// cannot fill. The three refusals are different mistakes: a reason for a
// category nobody recognises, an empty reason, and a reason that contradicts
// the tests the rule already has.
func judgeReasons(label string, rule Rule, presence map[string]json.RawMessage) []Violation {
	var violations []Violation
	decline := func(code, detail string) {
		violations = append(violations, Violation{Rule: label, Code: code, Detail: detail})
	}

	known := map[string]bool{}
	for _, category := range TestCategories {
		known[category] = true
	}
	filled := map[string]bool{}
	for _, category := range rule.Tests.list() {
		filled[category.name] = len(category.refs) > 0
	}

	for name, reason := range rule.NotApplicable {
		if !known[name] {
			decline(codeReasonUnknown, fmt.Sprintf("`%s` is not an evidence category: a reason for a category that does not exist explains nothing", name))
			continue
		}
		if strings.TrimSpace(reason) == "" {
			decline(codeReasonEmpty, fmt.Sprintf("the reason for `%s` is blank: an unexplained gap is an undiscovered one", name))
			continue
		}
		if filled[name] {
			decline(codeReasonConflict, fmt.Sprintf("`%s` has tests and a reason saying it cannot have any: one of the two is wrong, and the loader will not guess which", name))
		}
	}

	if raw, ok := presence["not_applicable"]; ok {
		if _, err := fieldsOf(raw); err != nil {
			decline(codeCatalogInvalid, fmt.Sprintf("`not_applicable` is not a JSON object: %v", err))
		}
	}
	return violations
}

// statementOf reads a statement by the category name it shares with a test
// slot, so the profile table can name either without a second vocabulary.
func statementOf(rule Rule, name string) string {
	switch name {
	case "authorization":
		return rule.Authorization
	case "concurrency":
		return rule.Concurrency
	case "idempotency":
		return rule.Idempotency
	case "security":
		return rule.Security
	case "privacy":
		return rule.Privacy
	default:
		return ""
	}
}

// fieldsOf decodes an object into its keys, keeping the values raw so presence
// can be asked separately from content.
func fieldsOf(raw json.RawMessage) (map[string]json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// splitReference splits `path::Function` into its two halves, refusing
// anything that is not exactly one pair.
func splitReference(reference string) (string, string, bool) {
	path, name, found := strings.Cut(reference, "::")
	if !found || strings.Count(reference, "::") != 1 {
		return "", "", false
	}
	if strings.TrimSpace(path) == "" || strings.TrimSpace(name) == "" {
		return "", "", false
	}
	if !funcPattern.MatchString(name) {
		return "", "", false
	}
	return path, name, true
}

// declares reports whether the file truly declares the named test. A Go test
// is declared by its func; any other file (the E2E specs, a shell audit) is
// accepted when the name appears as a whole token, because the taxonomy of
// evidence lands in P21-T05 and this loader must not invent it now.
func declares(data []byte, path, name string) bool {
	if strings.HasSuffix(path, ".go") {
		return bytes.Contains(data, []byte("func "+name+"("))
	}
	return tokenPattern(name).Match(data)
}

// tokenPattern matches a name at token boundaries.
func tokenPattern(name string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `([^A-Za-z0-9_]|$)`)
}

// decodeCode classifies a decode error. An unknown field is the mistake the
// schema exists to refuse, and it deserves its own code; anything else is a
// document that is not a catalog.
func decodeCode(err error) string {
	if strings.Contains(err.Error(), "unknown field") {
		return codeFieldUnknown
	}
	return codeCatalogInvalid
}
