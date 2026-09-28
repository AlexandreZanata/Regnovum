// Package capacityratchet owns the capacity ratchet (P28-T08): the pinned
// numbers every load and budget gate enforces, judged against the tree.
//
// quality/capacity-baseline.json names, per k6 scenario, Go budget table
// and product constant, the bound that holds. This package re-reads each
// bound from the checkout — k6 thresholds by text, Go numbers by syntax —
// and refuses any divergence: a loosened bound is a regression beyond
// tolerance, a tightened one is a baseline the tree outgrew, and both move
// only by human commit. It never writes: there is no flag, no output path
// and no print function that produces the baseline, so a green run can
// never rewrite what it judges. A hardware mismatch fails the comparison
// instead of reporting green on foreign numbers.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SchemaVersion is the only baseline version the loader understands.
const SchemaVersion = 1

// Default locations, relative to the repository root.
const (
	BaselinePath = "quality/capacity-baseline.json"
	SchemaPath   = "quality/capacity-baseline.schema.json"
	ReportPath   = "docs/CAPACITY.md"
)

// Finding is one refused claim of the baseline.
type Finding struct {
	Rule   string
	Detail string
}

// TimeBudget is one named median/allocs budget.
type TimeBudget struct {
	MedianNs int64 `json:"median_ns"`
	Allocs   int   `json:"allocs"`
}

// K6File pins one scenario file: global counters plus p95 bounds in
// milliseconds keyed by workload (and stage where the file tags one).
type K6File struct {
	Globals []string         `json:"globals"`
	P95Ms   map[string]int64 `json:"p95_ms"`
}

// Environment pins where the numbers were measured.
type Environment struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	HardwareClass string `json:"hardware_class"`
	HardwareNote  string `json:"hardware_note"`
	GoVersion     string `json:"go_version"`
	K6Version     string `json:"k6_version"`
	Postgres      string `json:"postgres"`
}

// Baseline is the capacity baseline document.
type Baseline struct {
	Schema        int                              `json:"schema"`
	Generator     string                           `json:"generator"`
	Commit        string                           `json:"commit"`
	Environment   Environment                      `json:"environment"`
	Datasets      map[string]string                `json:"datasets"`
	K6Thresholds  map[string]K6File                `json:"k6_thresholds"`
	GoBudgets     map[string]map[string]TimeBudget `json:"go_budgets"`
	ByteBudgets   map[string]map[string]int64      `json:"byte_budgets"`
	ProductBounds map[string]int64                 `json:"product_bounds"`
	Tolerance     int64                            `json:"tolerance"`
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

// SchemaKeys are the top-level keys the loader enforces, mirrored by
// quality/capacity-baseline.schema.json (a test holds the two together).
var SchemaKeys = []string{
	"schema", "generator", "commit", "environment", "datasets",
	"k6_thresholds", "go_budgets", "byte_budgets", "product_bounds", "tolerance",
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
	for _, name := range SchemaKeys {
		if document.Properties[name] == nil {
			findings = append(findings, Finding{Rule: "bad-schema-file", Detail: fmt.Sprintf("schema promises nothing about %q", name)})
		}
		if !required[name] {
			findings = append(findings, Finding{Rule: "bad-schema-file", Detail: fmt.Sprintf("schema does not require %q", name)})
		}
	}
	return findings
}

// ReadBaseline loads the capacity baseline document.
func ReadBaseline(root, baseline string) (Baseline, error) {
	var parsed Baseline
	if err := readJSON(root, baseline, &parsed); err != nil {
		return Baseline{}, err
	}
	return parsed, nil
}

var (
	k6P95Pattern     = regexp.MustCompile(`'([^']+)':\s*\['p\(95\)<(\d+)'\]`)
	k6CounterPattern = regexp.MustCompile(`^\s*([A-Za-z0-9_]+_failed_total):\s*\['count==0'\]`)
	k6ChecksPattern  = regexp.MustCompile(`"checks\{kind:invariant\}":\s*\['rate==1'\]`)
)

// readK6Thresholds re-reads one scenario file: global counters plus p95
// bounds keyed by the exact tag string the file declares.
func readK6Thresholds(root, file string) (globals []string, p95 map[string]int64, err error) {
	raw, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return nil, nil, err
	}
	p95 = make(map[string]int64)
	for _, line := range strings.Split(string(raw), "\n") {
		if matches := k6P95Pattern.FindStringSubmatch(line); matches != nil {
			bound, convErr := strconv.ParseInt(matches[2], 10, 64)
			if convErr != nil {
				return nil, nil, fmt.Errorf("%s: bad bound %q", file, matches[2])
			}
			key := matches[1]
			if start := strings.Index(key, "{"); start >= 0 {
				if end := strings.LastIndex(key, "}"); end > start {
					key = key[start+1 : end]
				}
			}
			p95[strings.TrimPrefix(key, "workload:")] = bound
		}
		if matches := k6CounterPattern.FindStringSubmatch(line); matches != nil {
			globals = append(globals, matches[1]+":count==0")
		}
		if k6ChecksPattern.MatchString(line) {
			globals = append(globals, "checks{kind:invariant}:rate==1")
		}
	}
	sort.Strings(globals)
	return globals, p95, nil
}

// evalConstInt reads one integer constant expression: plain literals,
// shifts such as 512 << 10, and int64() wrapped durations built from the
// time package units. Anything else is refused instead of guessed.
func evalConstInt(expression ast.Expr) (int64, error) {
	switch node := expression.(type) {
	case *ast.BasicLit:
		return strconv.ParseInt(node.Value, 0, 64)
	case *ast.BinaryExpr:
		left, err := evalConstInt(node.X)
		if err != nil {
			return 0, err
		}
		right, err := evalConstInt(node.Y)
		if err != nil {
			return 0, err
		}
		switch node.Op {
		case token.SHL:
			return left << uint(right), nil
		case token.MUL:
			return left * right, nil
		}
		return 0, fmt.Errorf("unsupported operator %s", node.Op)
	case *ast.CallExpr:
		identifier, ok := node.Fun.(*ast.Ident)
		if !ok || identifier.Name != "int64" || len(node.Args) != 1 {
			return 0, fmt.Errorf("unsupported call in constant")
		}
		return evalConstInt(node.Args[0])
	case *ast.SelectorExpr:
		prefix, ok := node.X.(*ast.Ident)
		if !ok || prefix.Name != "time" {
			return 0, fmt.Errorf("unsupported selector in constant")
		}
		units := map[string]int64{
			"Nanosecond": 1, "Microsecond": 1000, "Millisecond": 1000000,
			"Second": 1000000000, "Minute": 60000000000, "Hour": 3600000000000,
		}
		unit, ok := units[node.Sel.Name]
		if !ok {
			return 0, fmt.Errorf("unsupported time unit %s", node.Sel.Name)
		}
		return unit, nil
	}
	return 0, fmt.Errorf("unsupported constant shape %T", expression)
}

// parseGoFile parses one Go file for syntax-level reading. The tree is
// judged, never type-checked: the budgets live in test tables the compiler
// never constrains.
func parseGoFile(root, file string) (*ast.File, error) {
	return parser.ParseFile(token.NewFileSet(), filepath.Join(root, file), nil, 0)
}

// readGoBudgets re-reads one budget table: entries carrying name,
// maxMedianNs and maxAllocs, like internal/performance/budget_test.go.
func readGoBudgets(root, file string) (map[string]TimeBudget, error) {
	parsed, err := parseGoFile(root, file)
	if err != nil {
		return nil, err
	}
	found := make(map[string]TimeBudget)
	visit := func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var name string
		var median, allocs int64
		var hasMedian, hasAllocs bool
		for _, element := range literal.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := pair.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "name":
				if literal, ok := pair.Value.(*ast.BasicLit); ok && literal.Kind == token.STRING {
					name, _ = strconv.Unquote(literal.Value)
				}
			case "maxMedianNs":
				if value, err := evalConstInt(pair.Value); err == nil {
					median, hasMedian = value, true
				}
			case "maxAllocs":
				if value, err := evalConstInt(pair.Value); err == nil {
					allocs, hasAllocs = value, true
				}
			}
		}
		if name != "" && hasMedian && hasAllocs {
			found[name] = TimeBudget{MedianNs: median, Allocs: int(allocs)}
		}
		return true
	}
	ast.Inspect(parsed, visit)
	return found, nil
}

// readGoConsts re-reads named integer constants from one Go file.
func readGoConsts(root, file string, names []string) (map[string]int64, error) {
	parsed, err := parseGoFile(root, file)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	found := make(map[string]int64)
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, specification := range general.Specs {
			value, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range value.Names {
				if !wanted[name.Name] || index >= len(value.Values) {
					continue
				}
				number, err := evalConstInt(value.Values[index])
				if err != nil {
					return nil, fmt.Errorf("%s: %s: %w", file, name.Name, err)
				}
				found[name.Name] = number
			}
		}
	}
	return found, nil
}

// splitBound divides a "file:name" product bound reference.
func splitBound(reference string) (file, name string, ok bool) {
	file, name, ok = strings.Cut(reference, ":")
	if !ok || file == "" || name == "" || !strings.HasSuffix(file, ".go") {
		return "", "", false
	}
	return file, name, true
}

// compareBound judges one pinned number against the live one. A loosened
// bound is a regression beyond tolerance; a tightened one is a baseline
// the tree outgrew: both move only by human commit.
func compareBound(rule, name string, pinned, live, tolerance int64) *Finding {
	if live == pinned {
		return nil
	}
	if live > pinned {
		return &Finding{Rule: rule, Detail: fmt.Sprintf("%s loosened %d -> %d (tolerance %d): regression, update the code, not the baseline", name, pinned, live, tolerance)}
	}
	return &Finding{Rule: rule, Detail: fmt.Sprintf("%s tightened %d -> %d: the baseline must move with the tree by human commit", name, live, pinned)}
}

// Validate judges the baseline against the checkout and returns every
// finding. The runtime platform travels as parameters so fixtures stay
// platform-independent; main passes the real one.
func Validate(root, baseline, schema, report string, goos, garch string) []Finding {
	var findings []Finding
	findings = append(findings, CheckSchema(root, schema)...)

	parsed, err := ReadBaseline(root, baseline)
	if err != nil {
		return append(findings, Finding{Rule: "bad-baseline", Detail: err.Error()})
	}
	if parsed.Schema != SchemaVersion {
		findings = append(findings, Finding{Rule: "bad-baseline", Detail: fmt.Sprintf("schema = %d, want %d", parsed.Schema, SchemaVersion)})
	}
	if parsed.Tolerance < 0 {
		findings = append(findings, Finding{Rule: "bad-baseline", Detail: "tolerance must not be negative"})
	}
	if strings.TrimSpace(parsed.Commit) == "" {
		findings = append(findings, Finding{Rule: "bad-baseline", Detail: "commit is empty: pin the measured commit"})
	}
	if strings.TrimSpace(parsed.Environment.OS) == "" || strings.TrimSpace(parsed.Environment.Arch) == "" {
		findings = append(findings, Finding{Rule: "bad-baseline", Detail: "environment os/arch is empty"})
	}

	if parsed.Environment.OS != goos || parsed.Environment.Arch != garch {
		findings = append(findings, Finding{Rule: "hardware-mismatch", Detail: fmt.Sprintf("baseline measured on %s/%s, running on %s/%s: re-baseline instead of comparing", parsed.Environment.OS, parsed.Environment.Arch, goos, garch)})
	}

	findings = append(findings, checkK6(root, parsed)...)
	findings = append(findings, checkGoBudgets(root, parsed)...)
	findings = append(findings, checkBytes(root, parsed)...)
	findings = append(findings, checkProductBounds(root, parsed)...)
	findings = append(findings, checkReport(root, parsed, report)...)

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Rule != findings[j].Rule {
			return findings[i].Rule < findings[j].Rule
		}
		return findings[i].Detail < findings[j].Detail
	})
	return findings
}

// checkK6 judges every pinned scenario file: same globals, same p95 bounds,
// nothing added, nothing missing.
func checkK6(root string, parsed Baseline) []Finding {
	var findings []Finding
	for file, pinned := range parsed.K6Thresholds {
		globals, bounds, err := readK6Thresholds(root, file)
		if err != nil {
			findings = append(findings, Finding{Rule: "bad-baseline", Detail: err.Error()})
			continue
		}
		want := append([]string(nil), pinned.Globals...)
		sort.Strings(want)
		if strings.Join(globals, ",") != strings.Join(want, ",") {
			findings = append(findings, Finding{Rule: "k6-drift", Detail: fmt.Sprintf("%s globals changed: live [%s], baseline [%s]", file, strings.Join(globals, ","), strings.Join(want, ","))})
		}
		for key, bound := range pinned.P95Ms {
			live, ok := bounds[key]
			if !ok {
				findings = append(findings, Finding{Rule: "k6-drift", Detail: fmt.Sprintf("%s threshold %q vanished: update the baseline with the tree", file, key)})
				continue
			}
			if finding := compareBound("k6-drift", fmt.Sprintf("%s %s", file, key), bound, live, parsed.Tolerance); finding != nil {
				findings = append(findings, *finding)
			}
		}
		for key := range bounds {
			if _, ok := pinned.P95Ms[key]; !ok {
				findings = append(findings, Finding{Rule: "k6-drift", Detail: fmt.Sprintf("%s threshold %q is not pinned: update the baseline with the tree", file, key)})
			}
		}
	}
	return findings
}

// checkGoBudgets judges every pinned time budget against its test table.
func checkGoBudgets(root string, parsed Baseline) []Finding {
	var findings []Finding
	for file, pinned := range parsed.GoBudgets {
		live, err := readGoBudgets(root, file)
		if err != nil {
			findings = append(findings, Finding{Rule: "bad-baseline", Detail: err.Error()})
			continue
		}
		for name, bound := range pinned {
			current, ok := live[name]
			if !ok {
				findings = append(findings, Finding{Rule: "budget-drift", Detail: fmt.Sprintf("%s budget %q vanished: update the baseline with the tree", file, name)})
				continue
			}
			if finding := compareBound("budget-drift", fmt.Sprintf("%s %s median_ns", file, name), bound.MedianNs, current.MedianNs, parsed.Tolerance); finding != nil {
				findings = append(findings, *finding)
			}
			if finding := compareBound("budget-drift", fmt.Sprintf("%s %s allocs", file, name), int64(bound.Allocs), int64(current.Allocs), parsed.Tolerance); finding != nil {
				findings = append(findings, *finding)
			}
		}
		for name := range live {
			if _, ok := pinned[name]; !ok {
				findings = append(findings, Finding{Rule: "budget-drift", Detail: fmt.Sprintf("%s budget %q is not pinned: update the baseline with the tree", file, name)})
			}
		}
	}
	return findings
}

// checkBytes judges every pinned byte ceiling against its const block.
func checkBytes(root string, parsed Baseline) []Finding {
	var findings []Finding
	for file, pinned := range parsed.ByteBudgets {
		names := make([]string, 0, len(pinned))
		for name := range pinned {
			names = append(names, name)
		}
		live, err := readGoConsts(root, file, names)
		if err != nil {
			findings = append(findings, Finding{Rule: "bad-baseline", Detail: err.Error()})
			continue
		}
		for name, bound := range pinned {
			current, ok := live[name]
			if !ok {
				findings = append(findings, Finding{Rule: "budget-drift", Detail: fmt.Sprintf("%s ceiling %q vanished: update the baseline with the tree", file, name)})
				continue
			}
			if finding := compareBound("budget-drift", fmt.Sprintf("%s %s bytes", file, name), bound, current, parsed.Tolerance); finding != nil {
				findings = append(findings, *finding)
			}
		}
	}
	return findings
}

// checkProductBounds judges every pinned product constant at its file.
func checkProductBounds(root string, parsed Baseline) []Finding {
	var findings []Finding
	byFile := make(map[string][]string)
	for reference := range parsed.ProductBounds {
		file, name, ok := splitBound(reference)
		if !ok {
			findings = append(findings, Finding{Rule: "bad-baseline", Detail: fmt.Sprintf("product bound %q: want file/name.go:ConstName", reference)})
			continue
		}
		byFile[file] = append(byFile[file], name)
	}
	for file, names := range byFile {
		live, err := readGoConsts(root, file, names)
		if err != nil {
			findings = append(findings, Finding{Rule: "bad-baseline", Detail: err.Error()})
			continue
		}
		for _, name := range names {
			current, ok := live[name]
			if !ok {
				findings = append(findings, Finding{Rule: "product-drift", Detail: fmt.Sprintf("%s:%s vanished: update the baseline with the tree", file, name)})
				continue
			}
			if finding := compareBound("product-drift", file+":"+name, parsed.ProductBounds[file+":"+name], current, parsed.Tolerance); finding != nil {
				findings = append(findings, *finding)
			}
		}
	}
	return findings
}

// checkReport proves the report was generated from this baseline: it must
// cite the pinned commit and hardware class. A report describing other
// numbers is a document that drifted from its evidence.
func checkReport(root string, parsed Baseline, report string) []Finding {
	raw, err := os.ReadFile(filepath.Join(root, report))
	if err != nil {
		return []Finding{{Rule: "stale-report", Detail: err.Error()}}
	}
	var findings []Finding
	if !strings.Contains(string(raw), parsed.Commit) {
		findings = append(findings, Finding{Rule: "stale-report", Detail: fmt.Sprintf("report does not cite baseline commit %s", parsed.Commit)})
	}
	if !strings.Contains(string(raw), parsed.Environment.HardwareClass) {
		findings = append(findings, Finding{Rule: "stale-report", Detail: fmt.Sprintf("report does not cite hardware class %s", parsed.Environment.HardwareClass)})
	}
	return findings
}
