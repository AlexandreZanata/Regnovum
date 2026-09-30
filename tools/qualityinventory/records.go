package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The documents the inventory crosses, and the artifacts it reads them from.
// Each path is relative to the repository root and is a constant because two
// callers asking for "the contract" have to mean the same file.
const (
	CatalogPath    = "quality/catalog.json"
	EvidencePath   = "quality/evidence.json"
	MatrixPath     = "docs/REQUIREMENTS.md"
	ContractPath   = "api/openapi.json"
	MigrationsDir  = "internal/platform/dbmigrate/migrations"
	JobsSourcePath = "internal/jobs/domain/types.go"
	CLISourcePath  = "cmd/arena/main.go"
)

// The violation codes. Each names what was refused: a citation that points at
// nothing, a rule nothing proves, an artifact nothing traces, or a report that
// stopped describing the tree it was generated from.
const (
	codeSourceUnreadable   = "source-unreadable"
	codeRuleAbsent         = "rule-absent"
	codeCiteObsolete       = "cite-obsolete"
	codeReportMissing      = "report-missing"
	codeReportDrift        = "report-drift"
	codeReportInconsistent = "report-inconsistent"
)

// Family is one of the six artifact families the phase names. The vocabulary is
// closed so that the report's sections cannot drift from the join.
const (
	FamilyRoute     = "route"
	FamilyMigration = "migration"
	FamilyUseCase   = "use-case"
	FamilyJob       = "job"
	FamilyCommand   = "command"
	FamilyTest      = "test"
)

// Families is the whole vocabulary, in the order the report prints it.
var Families = []string{FamilyRoute, FamilyMigration, FamilyUseCase, FamilyJob, FamilyCommand, FamilyTest}

// Labels are the Portuguese names the Markdown report prints. A family without
// a label is a failure of the report, not of the tree: this document is read by
// people.
var Labels = map[string]string{
	FamilyRoute:     "Rotas do contrato",
	FamilyMigration: "Migrations",
	FamilyUseCase:   "Casos de uso",
	FamilyJob:       "Tipos de job",
	FamilyCommand:   "Comandos de CLI",
	FamilyTest:      "Referências de teste",
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

// Violations is the list, with the sort the command prints.
type Violations []Violation

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

// Rule is one rule of the catalog as the inventory needs it: its class, the
// packages it claims, and the test references it declares.
type Rule struct {
	ID      string
	Risk    string
	Modules []string
	Tests   []string
}

// Evidence is one identity of the register: the rules it proves and the tests
// that carry it.
type Evidence struct {
	ID    string
	Risk  string
	Suite string
	Rules []string
	Tests []string
}

// MatrixRow is one row of the traceability matrix: what the document says the
// requirement has in the code. The columns are read by name, so a table with a
// different layout is read as it is instead of being misparsed by position.
type MatrixRow struct {
	ID         string
	Endpoints  []string
	UseCases   []string
	Migrations []string
	Tests      []string
}

// Artifact is one member of one family, with the package that owns it. The
// owner is what lets the join ask the second half of the question: not only
// "does a document cite this by name?" but "does any rule cover the package
// this lives in?".
type Artifact struct {
	Family string
	Name   string
	Owner  string
}

// Citation is one claim a document makes about something in the tree. The
// inventory resolves every one of them, because a citation that outlived its
// artifact is exactly the rot a coverage report exists to catch.
type Citation struct {
	Document  string
	Row       string
	Family    string
	Reference string
}

// Records is everything the join reads, as it is.
type Records struct {
	Root      string
	Rules     []Rule
	Evidence  []Evidence
	Matrix    []MatrixRow
	Artifacts []Artifact
	Citations []Citation
	Contract  int
	Documents map[string]bool
}

// ReadRecords reads the six families and the three documents. Every read is
// strict about absence: a source that is not there is a refusal rather than an
// empty family, because a report generated from a missing document would say
// "nothing is untraced" for the most uninteresting reason possible.
func ReadRecords(root string) (Records, Violations) {
	records := Records{Root: root, Documents: map[string]bool{}}
	var violations Violations
	refuse := func(path string, err error) {
		violations = append(violations, Violation{
			Row:    "records",
			Code:   codeSourceUnreadable,
			Detail: fmt.Sprintf("`%s` is unreadable: %v", path, err),
		})
	}

	rules, err := readCatalog(root)
	if err != nil {
		refuse(CatalogPath, err)
	}
	records.Rules = rules
	evidence, err := readEvidence(root)
	if err != nil {
		refuse(EvidencePath, err)
	}
	records.Evidence = evidence
	matrix, err := readMatrix(root)
	if err != nil {
		refuse(MatrixPath, err)
	}
	records.Matrix = matrix
	if len(violations) > 0 {
		return records, violations
	}

	owners, err := readRouteOwners(root)
	if err != nil {
		refuse("internal", err)
	}
	routes, err := readContract(root, owners)
	if err != nil {
		refuse(ContractPath, err)
	}
	migrations, err := readMigrations(root)
	if err != nil {
		refuse(MigrationsDir, err)
	}
	useCases, err := readUseCases(root, rules)
	if err != nil {
		refuse("internal", err)
	}
	jobs, err := readJobs(root)
	if err != nil {
		refuse(JobsSourcePath, err)
	}
	commands, err := readCommands(root)
	if err != nil {
		refuse(CLISourcePath, err)
	}
	if len(violations) > 0 {
		return records, violations
	}

	records.Contract = len(routes)
	records.Artifacts = append(records.Artifacts, routes...)
	records.Artifacts = append(records.Artifacts, migrations...)
	records.Artifacts = append(records.Artifacts, useCases...)
	records.Artifacts = append(records.Artifacts, jobs...)
	records.Artifacts = append(records.Artifacts, commands...)
	records.Citations = citationsOf(matrix, rules, evidence)
	records.Documents[CatalogPath] = true
	records.Documents[EvidencePath] = true
	records.Documents[MatrixPath] = true
	return records, nil
}

// readCatalog reads the rule set: the class, the packages and the test
// references of every rule. The inventory does not restate any of them.
func readCatalog(root string) ([]Rule, error) {
	raw, err := readWithin(root, CatalogPath)
	if err != nil {
		return nil, err
	}
	var document struct {
		Rules []struct {
			ID      string              `json:"id"`
			Risk    string              `json:"risk"`
			Modules []string            `json:"modules"`
			Tests   map[string][]string `json:"tests"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("the catalog does not decode: %w", err)
	}
	if len(document.Rules) == 0 {
		return nil, fmt.Errorf("the catalog declares no rule")
	}
	rules := make([]Rule, 0, len(document.Rules))
	for _, rule := range document.Rules {
		tests := []string{}
		for _, references := range rule.Tests {
			tests = append(tests, references...)
		}
		sort.Strings(tests)
		rules = append(rules, Rule{ID: rule.ID, Risk: rule.Risk, Modules: rule.Modules, Tests: tests})
	}
	sort.Slice(rules, func(one, other int) bool { return rules[one].ID < rules[other].ID })
	return rules, nil
}

// readEvidence reads the register: which rules each identity declares and the
// tests that carry it.
func readEvidence(root string) ([]Evidence, error) {
	raw, err := readWithin(root, EvidencePath)
	if err != nil {
		return nil, err
	}
	var document struct {
		Evidence []struct {
			ID    string   `json:"id"`
			Risk  string   `json:"risk"`
			Suite string   `json:"suite"`
			Rules []string `json:"rules"`
			Tests []string `json:"tests"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("the register does not decode: %w", err)
	}
	rows := make([]Evidence, 0, len(document.Evidence))
	for _, row := range document.Evidence {
		rules := append([]string(nil), row.Rules...)
		tests := append([]string(nil), row.Tests...)
		sort.Strings(rules)
		sort.Strings(tests)
		rows = append(rows, Evidence{ID: row.ID, Risk: row.Risk, Suite: row.Suite, Rules: rules, Tests: tests})
	}
	sort.Slice(rows, func(one, other int) bool { return rows[one].ID < rows[other].ID })
	return rows, nil
}

// readMatrix reads the traceability matrix by column name. The document has two
// tables with different layouts — requirements carry an endpoint and a use case
// and the invariants do not — so the columns are read from the header that
// precedes each table rather than from a position that holds for one and not
// the other.
func readMatrix(root string) ([]MatrixRow, error) {
	raw, err := readWithin(root, MatrixPath)
	if err != nil {
		return nil, err
	}
	var rows []MatrixRow
	var columns map[string]int
	for _, line := range strings.Split(string(raw), "\n") {
		cells := splitRow(line)
		if len(cells) == 0 {
			continue
		}
		if !strings.HasPrefix(cells[0], "**REQ-") {
			if index, ok := headerColumns(cells); ok {
				columns = index
				continue
			}
			if isSeparator(cells) {
				continue
			}
			columns = nil
			continue
		}
		if columns == nil {
			return nil, fmt.Errorf("the row %q appears before any header the columns can be read from", cells[0])
		}
		row := MatrixRow{ID: unstyle(cells[0])}
		row.Endpoints = cellValues(cells, columns, "Endpoint")
		row.UseCases = cellValues(cells, columns, "Caso de uso")
		row.Migrations = cellValues(cells, columns, "Migration")
		row.Tests = cellValues(cells, columns, "Teste")
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("the matrix declares no requirement row")
	}
	sort.Slice(rows, func(one, other int) bool { return rows[one].ID < rows[other].ID })
	return rows, nil
}

// headerColumns is the map of column name to position, read from the first cell
// of a table header. It is how the invariants table is read without pretending
// it has the requirements' columns.
func headerColumns(cells []string) (map[string]int, bool) {
	if len(cells) < 4 || unstyle(cells[0]) != "ID" {
		return nil, false
	}
	columns := map[string]int{}
	for index, name := range cells {
		columns[unstyle(name)] = index
	}
	return columns, true
}

// cellValues reads one column of a row by name, splitting the `<br>`-separated
// list the matrix uses and dropping the declared absences of the form
// "— (razão)", which are a statement about the requirement and not an artifact.
func cellValues(cells []string, columns map[string]int, name string) []string {
	position, ok := columns[name]
	if !ok || position >= len(cells) {
		return nil
	}
	values := []string{}
	for _, value := range splitCell(cells[position]) {
		if strings.HasPrefix(value, "—") {
			continue
		}
		values = append(values, value)
	}
	return values
}

// readContract reads the operations the served contract declares: the route and
// the method, because a route without its method traces half a surface.
func readContract(root string, owners routeOwners) ([]Artifact, error) {
	raw, err := readWithin(root, ContractPath)
	if err != nil {
		return nil, err
	}
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("the contract does not decode: %w", err)
	}
	if len(document.Paths) == 0 {
		return nil, fmt.Errorf("the contract declares no path")
	}
	artifacts := []Artifact{}
	for path, operations := range document.Paths {
		for method := range operations {
			upper := strings.ToUpper(method)
			if !isMethod(upper) {
				continue
			}
			artifacts = append(artifacts, Artifact{Family: FamilyRoute, Name: upper + " " + path, Owner: owners.of(path)})
		}
	}
	sort.Slice(artifacts, func(one, other int) bool { return artifacts[one].Name < artifacts[other].Name })
	return artifacts, nil
}

func isMethod(method string) bool {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE":
		return true
	}
	return false
}

// readMigrations reads the schema's own history: the file names that make up
// the migration surface.
func readMigrations(root string) ([]Artifact, error) {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(MigrationsDir)))
	if err != nil {
		return nil, err
	}
	artifacts := []Artifact{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		artifacts = append(artifacts, Artifact{Family: FamilyMigration, Name: entry.Name(), Owner: "internal/platform/dbmigrate"})
	}
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("the directory %s holds no migration", MigrationsDir)
	}
	sort.Slice(artifacts, func(one, other int) bool { return artifacts[one].Name < artifacts[other].Name })
	return artifacts, nil
}

// readUseCases reads the application layer of every module: the use cases are
// the files the matrix cites by that name, and their universe is what is under
// internal/<module>/application.
func readUseCases(root string, rules []Rule) ([]Artifact, error) {
	ownerModules := map[string]bool{}
	for _, rule := range rules {
		for _, module := range rule.Modules {
			ownerModules[module] = true
		}
	}
	artifacts := []Artifact{}
	modules, err := os.ReadDir(filepath.Join(root, "internal"))
	if err != nil {
		return nil, err
	}
	for _, module := range modules {
		if !module.IsDir() {
			continue
		}
		directory := filepath.Join(root, "internal", module.Name(), "application")
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			artifacts = append(artifacts, Artifact{
				Family: FamilyUseCase,
				Name:   filepath.ToSlash(filepath.Join("internal", module.Name(), "application", name)),
				Owner:  "internal/" + module.Name(),
			})
		}
	}
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("no module declares an application layer")
	}
	sort.Slice(artifacts, func(one, other int) bool { return artifacts[one].Name < artifacts[other].Name })
	return artifacts, nil
}

// jobTypePattern reads the closed workload vocabulary out of the domain
// constants. The vocabulary is data of the domain, and the inventory reads it
// as it is rather than repeating the six names here where they would rot.
var jobTypePattern = regexp.MustCompile(`(?m)^\s*Type[A-Za-z0-9]+\s+JobType\s*=\s*"([^"]+)"`)

func readJobs(root string) ([]Artifact, error) {
	raw, err := readWithin(root, JobsSourcePath)
	if err != nil {
		return nil, err
	}
	names := jobTypePattern.FindAllStringSubmatch(string(raw), -1)
	if len(names) == 0 {
		return nil, fmt.Errorf("`%s` declares no job type", JobsSourcePath)
	}
	artifacts := []Artifact{}
	for _, match := range names {
		artifacts = append(artifacts, Artifact{Family: FamilyJob, Name: match[1], Owner: "internal/jobs"})
	}
	sort.Slice(artifacts, func(one, other int) bool { return artifacts[one].Name < artifacts[other].Name })
	return artifacts, nil
}

// commandPattern reads the subcommands the binary offers, out of the switch
// that dispatches them. It is the same idiom the contract reader uses: the
// surface is read from where it is defined.
var commandPattern = regexp.MustCompile(`(?m)^\s*case\s+"([a-z][a-z0-9-]*)"`)

func readCommands(root string) ([]Artifact, error) {
	raw, err := readWithin(root, CLISourcePath)
	if err != nil {
		return nil, err
	}
	names := commandPattern.FindAllStringSubmatch(string(raw), -1)
	if len(names) == 0 {
		return nil, fmt.Errorf("`%s` dispatches no subcommand", CLISourcePath)
	}
	seen := map[string]bool{}
	artifacts := []Artifact{}
	for _, match := range names {
		if seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		artifacts = append(artifacts, Artifact{Family: FamilyCommand, Name: match[1], Owner: "cmd/arena"})
	}
	sort.Slice(artifacts, func(one, other int) bool { return artifacts[one].Name < artifacts[other].Name })
	return artifacts, nil
}

// citationsOf collects every claim the three documents make about the tree.
func citationsOf(matrix []MatrixRow, rules []Rule, evidence []Evidence) []Citation {
	citations := []Citation{}
	for _, row := range matrix {
		for _, endpoint := range row.Endpoints {
			citations = append(citations, Citation{Document: MatrixPath, Row: row.ID, Family: FamilyRoute, Reference: endpoint})
		}
		for _, useCase := range row.UseCases {
			citations = append(citations, Citation{Document: MatrixPath, Row: row.ID, Family: FamilyUseCase, Reference: useCase})
		}
		for _, migration := range row.Migrations {
			citations = append(citations, Citation{Document: MatrixPath, Row: row.ID, Family: FamilyMigration, Reference: migration})
		}
		for _, test := range row.Tests {
			citations = append(citations, Citation{Document: MatrixPath, Row: row.ID, Family: FamilyTest, Reference: test})
		}
	}
	for _, rule := range rules {
		for _, test := range rule.Tests {
			citations = append(citations, Citation{Document: CatalogPath, Row: rule.ID, Family: FamilyTest, Reference: test})
		}
	}
	for _, row := range evidence {
		for _, test := range row.Tests {
			citations = append(citations, Citation{Document: EvidencePath, Row: row.ID, Family: FamilyTest, Reference: test})
		}
	}
	sort.Slice(citations, func(one, other int) bool {
		if citations[one].Document != citations[other].Document {
			return citations[one].Document < citations[other].Document
		}
		if citations[one].Row != citations[other].Row {
			return citations[one].Row < citations[other].Row
		}
		if citations[one].Reference != citations[other].Reference {
			return citations[one].Reference < citations[other].Reference
		}
		return citations[one].Family < citations[other].Family
	})
	return citations
}

// splitRow reads the cells of a Markdown table row, or nothing when the line is
// not one.
func splitRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return nil
	}
	cells := strings.Split(strings.Trim(trimmed, "|"), "|")
	for index := range cells {
		cells[index] = strings.TrimSpace(cells[index])
	}
	return cells
}

// isSeparator reports whether a row is the |---|---| line that separates a
// header from its table.
func isSeparator(cells []string) bool {
	for _, cell := range cells {
		if strings.Trim(cell, "-: ") != "" {
			return false
		}
	}
	return len(cells) > 0
}

// unstyle removes the Markdown emphasis and the code ticks from a cell, because
// a citation is compared against the tree and not against its formatting.
func unstyle(cell string) string {
	return strings.TrimSpace(strings.NewReplacer("**", "", "`", "").Replace(strings.TrimSpace(cell)))
}

// splitCell reads the `<br>`-separated list a matrix cell holds.
func splitCell(cell string) []string {
	parts := regexp.MustCompile(`<br\s*/?>`).Split(cell, -1)
	values := []string{}
	for _, part := range parts {
		if value := unstyle(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// readWithin reads a file of the checkout. An absolute path, or one that climbs
// out of the root, is refused: a report generated from outside the tree would
// compare the documents against something that is not the tree they describe,
// and the failure would look like a finding.
func readWithin(root, relative string) ([]byte, error) {
	if !inside(root, relative) {
		return nil, fmt.Errorf("`%s` is not a path inside the checkout", relative)
	}
	return os.ReadFile(filepath.Join(root, filepath.FromSlash(filepath.ToSlash(filepath.Clean(relative)))))
}

// inside reports whether a repository-relative path stays in the repository.
func inside(root, relative string) bool {
	if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(relative))
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

// declaresTest reports whether a `path::Name` reference resolves. The rule is
// the one `tools/reqaudit` already applies to the same corpus, and it is the
// same rule on purpose: two tools that disagreed about what "this test exists"
// means would make the word useless in both. A Go test is a function; the
// browser journeys of tools/e2e are `test("...")` calls whose title is the name
// cited. A reference that is only a path — the register cites journeys that way
// — resolves when the file is there.
func declaresTest(root, reference string) bool {
	path, symbol, qualified := strings.Cut(reference, "::")
	if path == "" {
		return false
	}
	raw, err := readWithin(root, path)
	if err != nil {
		return false
	}
	if !qualified {
		return true
	}
	if symbol == "" {
		return false
	}
	body := string(raw)
	if strings.HasSuffix(path, ".go") {
		return strings.Contains(body, "func "+symbol+"(")
	}
	for _, quote := range []string{`"`, `'`, "`"} {
		if strings.Contains(body, "test("+quote+symbol+quote) {
			return true
		}
	}
	return false
}

// exists reports whether a repository-relative path is in the tree.
func exists(root, path string) bool {
	if !inside(root, path) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(clean)))
	return err == nil
}
