// Package regressionpack owns the fast semantic regression pack (P27-T10):
// the executable slice of every Q0/Q1 catalog rule for local change and PR
// feedback, with the complete suite kept separate and never replaced.
//
// The pack is a catalog, not a runner flag: quality/regression-pack.json
// names, per rule, the quick tests that cover it, plus the historical
// defects and the suites the fast run never replaces. This package judges
// every claim against the checkout (rule exists with the same risk, test
// exists, package is fast, body has no skip or test-retry, defect test
// exists) and, on demand, runs the pack measuring the documented budget.
// A repeated run is never a retry: each package runs once, and a failure
// stops the run instead of repeating it.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SchemaVersion is the only pack version the loader understands.
const SchemaVersion = 1

// Default locations, relative to the repository root.
const (
	PackPath    = "quality/regression-pack.json"
	SchemaPath  = "quality/regression-pack.schema.json"
	CatalogPath = "quality/catalog.json"
)

// RequiredModules are the twelve business modules the phase names, as the
// catalog paths them. Coverage is judged per rule, and per module as the
// question a pack has to answer: every module must appear, or the pack
// silently narrowed. Entitlements has no package of its own: its tests live
// in billing under entitlement names, and the validator credits them.
var RequiredModules = []string{
	"internal/identity", "internal/profiles", "internal/wallet", "internal/entitlements",
	"internal/arenas", "internal/positions", "internal/arguments", "internal/persuasion",
	"internal/billing", "internal/moderation", "internal/transparency", "internal/jobs",
}

// SlowPackages never belong to the fast pack: a test there costs a browser,
// a process tree, a minute-long soak or a flake hunt. The rule is read from
// the path, never from timing, so a fast test moving into one of these
// packages is refused until the pack moves it back.
var SlowPackages = []string{
	"internal/contract",
	"cmd/arena",
	"internal/regression",
	"tools/e2e",
	"tools/flakedetect",
}

// RetryNames is the closed vocabulary of test-retry machinery: skipping the
// test, polling an assertion until it passes, or repeating the run. Business
// idempotency (a second call resolving the first row, IsRetryable* error
// classification) is not test retry and matches none of these names.
var RetryNames = []string{
	"Skip", "SkipNow", "Skipf",
	"Eventually", "EventuallyWithT", "Consistently", "Never",
	"Retry", "Rerun", "Flaky",
}

// Finding is one refused claim of the pack.
type Finding struct {
	Rule   string
	Detail string
}

// RuleEntry links one catalog rule to its fast tests.
type RuleEntry struct {
	Rule  string   `json:"rule"`
	Risk  string   `json:"risk"`
	Tests []string `json:"tests"`
}

// Defect is one historical defect pinned by its regression test.
type Defect struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	FixedIn     string `json:"fixed_in"`
	Test        string `json:"test"`
}

// Pack is the fast-pack document.
type Pack struct {
	Schema         int         `json:"schema"`
	Generator      string      `json:"generator"`
	Scope          string      `json:"scope"`
	BudgetSeconds  int         `json:"budget_seconds"`
	DoesNotReplace []string    `json:"does_not_replace"`
	FullSuite      []string    `json:"full_suite"`
	Rules          []RuleEntry `json:"rules"`
	Defects        []Defect    `json:"historical_defects"`
}

// CatalogRule is the subset of a catalog rule the pack judges.
type CatalogRule struct {
	ID      string   `json:"id"`
	Risk    string   `json:"risk"`
	Modules []string `json:"modules"`
}

func readJSON(root, name string, into any) error {
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// ReadCatalog returns the rules of the rule catalog by id.
func ReadCatalog(root, catalog string) (map[string]CatalogRule, error) {
	var document struct {
		Rules []CatalogRule `json:"rules"`
	}
	if err := readJSON(root, catalog, &document); err != nil {
		return nil, err
	}
	rules := make(map[string]CatalogRule, len(document.Rules))
	for _, rule := range document.Rules {
		rules[rule.ID] = rule
	}
	return rules, nil
}

// ReadPack loads the fast-pack document.
func ReadPack(root, pack string) (Pack, error) {
	var parsed Pack
	if err := readJSON(root, pack, &parsed); err != nil {
		return Pack{}, err
	}
	return parsed, nil
}

// CheckSchema refuses a schema file that does not speak the loader's
// vocabulary: the schema is a second contract, and one promising less than
// the loader judges is a hole.
func CheckSchema(root, schema string) []Finding {
	var document struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := readJSON(root, schema, &document); err != nil {
		return []Finding{{Rule: "bad-schema-file", Detail: err.Error()}}
	}
	var findings []Finding
	required := make(map[string]bool, len(document.Required))
	for _, name := range document.Required {
		required[name] = true
	}
	for _, name := range []string{"schema", "scope", "budget_seconds", "does_not_replace", "full_suite", "rules", "historical_defects"} {
		if document.Properties[name] == nil {
			findings = append(findings, Finding{Rule: "bad-schema-file", Detail: fmt.Sprintf("schema promises nothing about %q", name)})
		}
		if !required[name] {
			findings = append(findings, Finding{Rule: "bad-schema-file", Detail: fmt.Sprintf("schema does not require %q", name)})
		}
	}
	return findings
}

// splitRef divides a "path::Test" reference.
func splitRef(reference string) (path, name string, ok bool) {
	path, name, ok = strings.Cut(reference, "::")
	if !ok || path == "" || name == "" || !strings.HasSuffix(path, ".go") {
		return "", "", false
	}
	return path, name, true
}

// findTest locates one test function by name in one Go file.
func findTest(root, path, name string) (*ast.FuncDecl, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, path), nil, 0)
	if err != nil {
		return nil, err
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != name {
			continue
		}
		return function, nil
	}
	return nil, fmt.Errorf("%s has no %s", path, name)
}

// bodyRetries reports whether a test body skips itself or repeats until it
// passes. The vocabulary is exact names, never substrings: a business
// classifier such as IsRetryablePaymentGatewayError is not test machinery.
func bodyRetries(function *ast.FuncDecl) (string, bool) {
	matched := ""
	found := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		switch invoked := call.Fun.(type) {
		case *ast.SelectorExpr:
			for _, banned := range RetryNames {
				if invoked.Sel.Name == banned {
					matched, found = banned, true
				}
			}
		case *ast.Ident:
			for _, banned := range RetryNames {
				if invoked.Name == banned {
					matched, found = banned, true
				}
			}
		}
		return !found
	})
	return matched, found
}

func isSlow(path string) bool {
	for _, denied := range SlowPackages {
		if path == denied || strings.HasPrefix(path, denied+"/") {
			return true
		}
	}
	return false
}

// Validate judges the pack against the checkout and returns every finding.
// Modules are the business modules coverage must show; callers pass
// RequiredModules, and tests pass the fixture scope.
func Validate(root, catalog, pack, schema string, modulesRequired []string) []Finding {
	var findings []Finding
	findings = append(findings, CheckSchema(root, schema)...)

	rules, err := ReadCatalog(root, catalog)
	if err != nil {
		return append(findings, Finding{Rule: "bad-pack", Detail: err.Error()})
	}
	parsed, err := ReadPack(root, pack)
	if err != nil {
		return append(findings, Finding{Rule: "bad-pack", Detail: err.Error()})
	}
	findings = append(findings, checkPackHeader(parsed)...)

	covered := make(map[string]bool, len(parsed.Rules))
	modules := make(map[string]bool)
	for _, entry := range parsed.Rules {
		catalogRule, known := rules[entry.Rule]
		if !known || (catalogRule.Risk != "Q0" && catalogRule.Risk != "Q1") {
			findings = append(findings, Finding{Rule: "unknown-rule", Detail: fmt.Sprintf("%s is not a Q0/Q1 catalog rule", entry.Rule)})
			continue
		}
		if catalogRule.Risk != entry.Risk {
			findings = append(findings, Finding{Rule: "unknown-rule", Detail: fmt.Sprintf("%s declares %s, the catalog says %s", entry.Rule, entry.Risk, catalogRule.Risk)})
			continue
		}
		covered[entry.Rule] = true
		for _, module := range catalogRule.Modules {
			modules[module] = true
		}
		findings = append(findings, checkRuleEntry(root, entry, modules)...)
	}
	findings = append(findings, checkCoverage(rules, covered, modules, modulesRequired)...)
	findings = append(findings, checkDefects(root, parsed.Defects)...)

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Rule != findings[j].Rule {
			return findings[i].Rule < findings[j].Rule
		}
		return findings[i].Detail < findings[j].Detail
	})
	return findings
}

// checkPackHeader judges the pack envelope: version, budget, scope, the
// suites it never replaces, and the complete suite that owns certification.
func checkPackHeader(parsed Pack) []Finding {
	var findings []Finding
	if parsed.Schema != SchemaVersion {
		findings = append(findings, Finding{Rule: "bad-pack", Detail: fmt.Sprintf("schema = %d, want %d", parsed.Schema, SchemaVersion)})
	}
	if parsed.BudgetSeconds <= 0 {
		findings = append(findings, Finding{Rule: "bad-pack", Detail: "budget_seconds must be positive"})
	}
	if strings.TrimSpace(parsed.Scope) == "" {
		findings = append(findings, Finding{Rule: "bad-pack", Detail: "scope is empty"})
	}
	kept := make(map[string]bool, len(parsed.DoesNotReplace))
	for _, name := range parsed.DoesNotReplace {
		kept[name] = true
	}
	for _, required := range []string{"nightly", "release"} {
		if !kept[required] {
			findings = append(findings, Finding{Rule: "bad-pack", Detail: fmt.Sprintf("does_not_replace names no %s: the fast pack must say what it never replaces", required)})
		}
	}
	if len(parsed.FullSuite) == 0 {
		findings = append(findings, Finding{Rule: "bad-pack", Detail: "full_suite is empty: certification must name its targets"})
	}
	return findings
}

// checkRuleEntry judges one rule entry and credits its modules. Modules
// credits the catalog modules of the rule; the caller passes the set it
// accumulates across entries.
func checkRuleEntry(root string, entry RuleEntry, modules map[string]bool) []Finding {
	var findings []Finding
	if len(entry.Tests) == 0 {
		findings = append(findings, Finding{Rule: "missing-test", Detail: fmt.Sprintf("%s names no test", entry.Rule)})
	}
	for _, reference := range entry.Tests {
		findings = append(findings, checkTestRef(root, reference, modules)...)
	}
	return findings
}

// checkTestRef judges one test reference: shape, existence, fast package,
// and a body with no skip or test-retry.
func checkTestRef(root, reference string, modules map[string]bool) []Finding {
	path, name, ok := splitRef(reference)
	if !ok {
		return []Finding{{Rule: "missing-test", Detail: fmt.Sprintf("%s: want path/to/file_test.go::TestName", reference)}}
	}
	if strings.Contains(strings.ToLower(path), "entitlement") {
		modules["internal/entitlements"] = true
	}
	function, err := findTest(root, path, name)
	if err != nil {
		return []Finding{{Rule: "missing-test", Detail: err.Error()}}
	}
	if isSlow(path) {
		return []Finding{{Rule: "slow-package", Detail: fmt.Sprintf("%s lives in %s, outside the fast budget", reference, path)}}
	}
	if banned, refused := bodyRetries(function); refused {
		rule := "retried-test"
		if banned == "Skip" || banned == "SkipNow" || banned == "Skipf" {
			rule = "skipped-test"
		}
		return []Finding{{Rule: rule, Detail: fmt.Sprintf("%s uses %s", reference, banned)}}
	}
	return nil
}

// checkCoverage asks the question a pack has to answer: every Q0/Q1 rule
// and every required module appears, or the pack silently narrowed.
func checkCoverage(rules map[string]CatalogRule, covered, modules map[string]bool, required []string) []Finding {
	var findings []Finding
	for id, catalogRule := range rules {
		if (catalogRule.Risk == "Q0" || catalogRule.Risk == "Q1") && !covered[id] {
			findings = append(findings, Finding{Rule: "uncovered-rule", Detail: fmt.Sprintf("%s (%s) has no fast test", id, catalogRule.Risk)})
		}
	}
	for _, module := range required {
		if !modules[module] {
			findings = append(findings, Finding{Rule: "uncovered-rule", Detail: fmt.Sprintf("module %s has no fast test", module)})
		}
	}
	return findings
}

// checkDefects judges the historical defects: identified, described, and
// pinned to a test that exists.
func checkDefects(root string, defects []Defect) []Finding {
	var findings []Finding
	for _, defect := range defects {
		if strings.TrimSpace(defect.ID) == "" || strings.TrimSpace(defect.Description) == "" {
			findings = append(findings, Finding{Rule: "defect-without-test", Detail: "a historical defect without id or description proves nothing"})
			continue
		}
		path, name, ok := splitRef(defect.Test)
		if !ok {
			findings = append(findings, Finding{Rule: "defect-without-test", Detail: fmt.Sprintf("%s: want path/to/file_test.go::TestName", defect.Test)})
			continue
		}
		if _, err := findTest(root, path, name); err != nil {
			findings = append(findings, Finding{Rule: "defect-without-test", Detail: fmt.Sprintf("%s: %v", defect.ID, err)})
		}
	}
	if len(defects) == 0 {
		findings = append(findings, Finding{Rule: "defect-without-test", Detail: "no historical defect is pinned"})
	}
	return findings
}

// PackageRun is one go test invocation of the fast pack.
type PackageRun struct {
	Dir   string
	Tests []string
}

// PlanPack groups the pack tests by package, in a deterministic order. It
// is pure so the execution order is testable without running anything.
func PlanPack(parsed Pack) []PackageRun {
	grouped := make(map[string][]string)
	for _, entry := range parsed.Rules {
		for _, reference := range entry.Tests {
			path, name, ok := splitRef(reference)
			if !ok {
				continue
			}
			dir := filepath.Dir(path)
			seen := false
			for _, existing := range grouped[dir] {
				if existing == name {
					seen = true
				}
			}
			if !seen {
				grouped[dir] = append(grouped[dir], name)
			}
		}
	}
	planned := make([]PackageRun, 0, len(grouped))
	for dir, tests := range grouped {
		sort.Strings(tests)
		planned = append(planned, PackageRun{Dir: dir, Tests: tests})
	}
	sort.Slice(planned, func(i, j int) bool { return planned[i].Dir < planned[j].Dir })
	return planned
}
