package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// skippedDirectories are the directories the walk does not enter. `testdata` is
// the Go toolchain's own convention — the `go` command skips it in every `./...`
// pattern — and the rest are the caches and dependencies of the checkout. None
// of them is a list of "files we would rather not measure": generated code is
// excluded by provenance (the marker in the file), never by where it lives.
var skippedDirectories = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "testdata": true, ".local": true,
}

// finding is one refusal, in the two shapes the gate produces: a function that
// crossed a budget, or a block that exists in more than one place. The two
// share a type because they share everything that matters to the baseline — an
// identity, a measured value and an owner.
type finding struct {
	Kind     string // "function" or "duplication"
	Path     string // the file the finding is reported at
	Function string // empty for a duplicated block
	Line     int
	Metric   string
	Value    int      // the measurement
	Owner    string   // the package directory that answers for it
	Digest   string   // duplicated blocks only: the identity of the shared text
	Places   []string // duplicated blocks only: every place the block appears
}

// identity is the key the baseline holds a finding by. The line is deliberately
// absent from it: moving code above a function does not make its complexity a
// new finding, and a baseline keyed by line would file a new entry on every
// unrelated edit.
func (f finding) identity() string {
	if f.Kind == kindDuplication {
		return f.Kind + "\x00" + f.Digest + "\x00" + strings.Join(f.Paths(), "\x00")
	}
	return f.Kind + "\x00" + f.Path + "\x00" + f.Function + "\x00" + f.Metric
}

// Paths lists the files of a duplicated block, in order and without repetition.
func (f finding) Paths() []string {
	seen := map[string]bool{}
	paths := []string{}
	for _, place := range f.Places {
		path := place
		if index := strings.LastIndex(place, ":"); index > 0 {
			path = place[:index]
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

// String renders a finding the way the report prints it.
func (f finding) String() string {
	if f.Kind == kindDuplication {
		return fmt.Sprintf("%d line(s) copied at %s", f.Value, strings.Join(f.Places, ", "))
	}
	return fmt.Sprintf("%s:%d %s — %s %d", f.Path, f.Line, f.Function, f.Metric, f.Value)
}

// measurement is everything one run observed: what it judged, what it excluded
// by provenance, and every finding that came out of it.
type measurement struct {
	Files        int
	Functions    int
	Generated    []string
	Unparsable   map[string]string
	Unclassified []string
	Findings     []finding
}

// measureTree judges the repository: every Go file of the checkout, minus the
// directories the Go toolchain itself does not read.
func measureTree() (measurement, error) {
	files := []string{}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != "." && skippedDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".go") {
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		return measurement{}, fmt.Errorf("walk the tree: %w", err)
	}
	sort.Strings(files)
	return measure(files)
}

// measureDirectory judges one directory and everything under it. It is how the
// gate exercises its own fixtures: a fixture lives under testdata, which is
// exactly the place the tree walk refuses to enter, so the proof that a rule
// still bites has to ask for it by name.
func measureDirectory(directory string) (measurement, error) {
	files := []string{}
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".go") {
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		return measurement{}, fmt.Errorf("walk %s: %w", directory, err)
	}
	sort.Strings(files)
	return measure(files)
}

// measure takes every declared measure of every file it was given, and returns
// the findings. A file it cannot read is a file whose complexity nobody knows,
// and the answer to that is a refusal instead of a skipped line.
func measure(files []string) (measurement, error) {
	result := measurement{Unparsable: map[string]string{}, Unclassified: []string{}}
	positions := token.NewFileSet()
	duplication := []dupLine{}
	for _, path := range files {
		source, err := os.ReadFile(filepath.FromSlash(path))
		if err != nil {
			return measurement{}, fmt.Errorf("read %s: %w", path, err)
		}
		file, err := parser.ParseFile(positions, path, source, parser.ParseComments)
		if err != nil {
			result.Unparsable[path] = err.Error()
			continue
		}
		result.Files++
		if isGenerated(file) {
			result.Generated = append(result.Generated, path)
			continue
		}
		scope, classified := scopeOf(path)
		if !classified {
			result.Unclassified = append(result.Unclassified, path)
			continue
		}
		functions := 0
		for _, declaration := range file.Decls {
			declared, ok := declaration.(*ast.FuncDecl)
			if !ok || declared.Body == nil {
				continue
			}
			functions++
			values := functionValues(positions, declared)
			name := funcName(declared)
			line := positions.Position(declared.Pos()).Line
			for _, metric := range measuredMetrics {
				declaredBudget, known := budgetFor(scope, metric)
				if !known {
					return measurement{}, fmt.Errorf("the budget table has no %s budget for the %s scope: a measure with no ceiling is a measure nobody is held to", metric, scope)
				}
				if values[metric] > declaredBudget.Budget {
					result.Findings = append(result.Findings, finding{
						Kind: kindFunction, Path: path, Function: name, Line: line,
						Metric: metric, Value: values[metric], Owner: ownerOf(path),
					})
				}
			}
		}
		result.Functions += functions
		if judgesDuplication(scope) {
			duplication = append(duplication, normalizedLines(path, string(source))...)
		}
	}
	if len(duplication) > 0 {
		result.Findings = append(result.Findings, duplicatedBlocks(duplication)...)
	}
	sortFindings(result.Findings)
	return result, nil
}

// measuredMetrics is the order the report and the budget table are read in. The
// duplication measure is not here: it is not taken per function.
var measuredMetrics = []string{MetricCyclomatic, MetricParameters, MetricNesting, MetricLines, MetricStatements}

// functionValues takes every declared measure of one function. It is the single
// place the measures are attached to the names they are declared under, so the
// report, the baseline and the tests cannot disagree about what a number means.
func functionValues(positions *token.FileSet, declaration *ast.FuncDecl) map[string]int {
	return map[string]int{
		MetricCyclomatic: decisionPoints(declaration.Body) + 1,
		MetricNesting:    nestingDepth(declaration.Body),
		MetricParameters: parameterCount(declaration.Type.Params),
		MetricStatements: statementCount(declaration.Body),
		MetricLines:      spanOf(positions, declaration),
	}
}

// decisionPoints counts what a reader has to hold: every branch, every loop,
// every non-default case, and every short-circuit that makes one expression two
// paths. The cyclomatic complexity of a function is this count plus one.
func decisionPoints(body *ast.BlockStmt) int {
	count := 0
	ast.Inspect(body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			count++
		case *ast.CaseClause:
			if typed.List != nil {
				count++
			}
		case *ast.CommClause:
			if typed.Comm != nil {
				count++
			}
		case *ast.BinaryExpr:
			if typed.Op == token.LAND || typed.Op == token.LOR {
				count++
			}
		}
		return true
	})
	return count
}

// nestingDepth answers how deep the control flow of a function goes. An `else`
// stays at the depth of its `if`: reading `} else {` does not require holding
// one more level, which is why the depth is a property of the block, not of the
// statement.
func nestingDepth(body *ast.BlockStmt) int {
	depth := 0
	var walk func(node ast.Node, level int)
	walk = func(node ast.Node, level int) {
		ast.Inspect(node, func(inner ast.Node) bool {
			blocks := []ast.Node{}
			switch typed := inner.(type) {
			case *ast.IfStmt:
				blocks = append(blocks, typed.Body)
				if typed.Else != nil {
					blocks = append(blocks, typed.Else)
				}
			case *ast.ForStmt:
				blocks = append(blocks, typed.Body)
			case *ast.RangeStmt:
				blocks = append(blocks, typed.Body)
			case *ast.SwitchStmt:
				blocks = append(blocks, typed.Body)
			case *ast.TypeSwitchStmt:
				blocks = append(blocks, typed.Body)
			case *ast.SelectStmt:
				blocks = append(blocks, typed.Body)
			default:
				return true
			}
			if level+1 > depth {
				depth = level + 1
			}
			for _, block := range blocks {
				walk(block, level+1)
			}
			return false
		})
	}
	walk(body, 0)
	return depth
}

// parameterCount counts the names a caller has to fill, not the fields the
// declaration grouped: `f(a, b int)` is two parameters to the person writing the
// call, and one field to the parser.
func parameterCount(params *ast.FieldList) int {
	if params == nil {
		return 0
	}
	count := 0
	for _, field := range params.List {
		if len(field.Names) == 0 {
			count++
			continue
		}
		count += len(field.Names)
	}
	return count
}

// statementCount counts every statement in the body, nested blocks included, and
// not the blocks themselves: braces are not statements. It is the size measure
// that does not move when the file is reformatted, which the line count cannot
// promise.
func statementCount(body *ast.BlockStmt) int {
	count := 0
	ast.Inspect(body, func(node ast.Node) bool {
		if _, isBlock := node.(*ast.BlockStmt); isBlock {
			return true
		}
		if _, isStatement := node.(ast.Stmt); isStatement {
			count++
		}
		return true
	})
	return count
}

// spanOf answers the lines a function occupies, from its `func` keyword to its
// closing brace.
func spanOf(positions *token.FileSet, declaration *ast.FuncDecl) int {
	return positions.Position(declaration.End()).Line - positions.Position(declaration.Pos()).Line + 1
}

// funcName names a function the way a reviewer would: the receiver joined to the
// method, so `(s *Store) Save` reads as `Store.Save` and two methods of the same
// name in one file are not one identity in the baseline.
func funcName(declaration *ast.FuncDecl) string {
	name := declaration.Name.Name
	if declaration.Recv == nil || len(declaration.Recv.List) == 0 {
		return name
	}
	return receiverName(declaration.Recv.List[0].Type) + "." + name
}

func receiverName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.StarExpr:
		return receiverName(typed.X)
	case *ast.Ident:
		return typed.Name
	case *ast.IndexExpr:
		return receiverName(typed.X)
	case *ast.IndexListExpr:
		return receiverName(typed.X)
	case *ast.ParenExpr:
		return receiverName(typed.X)
	}
	return "?"
}

// dupLine is one line of the duplication corpus, with its place in the corpus so
// that a match names where it found the text.
type dupLine struct {
	Index int
	Path  string
	Line  int
	Text  string
}

// normalizedLines reduces a file to the lines a copy is made of: blank lines and
// comment-only lines are dropped (a copied block with a different comment above
// it is the same copy), and the indentation is collapsed (the same block pasted
// one level deeper is the same copy). Every line keeps the position it had, so a
// match can be reported at the file and line a person can open.
func normalizedLines(path, source string) []dupLine {
	lines := []dupLine{}
	for index, raw := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		lines = append(lines, dupLine{Path: path, Line: index + 1, Text: strings.Join(strings.Fields(trimmed), " ")})
	}
	return lines
}

// duplicatedBlocks finds the blocks that exist in more than one place. A window
// of `duplicationWindow` normalized lines is the smallest thing that counts, and
// overlapping windows are extended into one region: without that, a forty-line
// block shared by three files would be reported twenty-one times, and a report
// nobody reads is the same as no report.
func duplicatedBlocks(lines []dupLine) []finding {
	for index := range lines {
		lines[index].Index = index
	}
	occurrences := map[string][]int{}
	for start := 0; start+duplicationWindow <= len(lines); start++ {
		window := lines[start : start+duplicationWindow]
		if !oneFile(window) {
			continue
		}
		text := windowText(window)
		occurrences[text] = append(occurrences[text], start)
	}

	// A region is a maximal run of windows that all occur in the same places.
	// `heads` maps the key of *every* window of a run to that run, so that the
	// window right after one of them finds the run it continues without any
	// bookkeeping: a window extends a run exactly when its own key is the key of
	// the window that came one line earlier, shifted.
	type region struct {
		starts []int
		length int
	}
	heads := map[string]*region{}
	told := []*region{}
	for start := 0; start+duplicationWindow <= len(lines); start++ {
		window := lines[start : start+duplicationWindow]
		if !oneFile(window) {
			continue
		}
		starts := occurrences[windowText(window)]
		if len(starts) < 2 {
			continue
		}
		key := joinInts(starts)
		if _, known := heads[key]; known {
			// The same window, reached from another of its occurrences: it is
			// already inside a region this loop built.
			continue
		}
		if previous, ok := heads[shiftKey(starts, -1)]; ok {
			previous.length++
			heads[key] = previous
			continue
		}
		entry := &region{starts: starts, length: duplicationWindow}
		heads[key] = entry
		told = append(told, entry)
	}

	regions := []finding{}
	for _, entry := range told {
		places := []string{}
		for _, start := range entry.starts {
			places = append(places, fmt.Sprintf("%s:%d", lines[start].Path, lines[start].Line))
		}
		sort.Strings(places)
		digest := digestOf(windowText(lines[entry.starts[0] : entry.starts[0]+duplicationWindow]))
		regions = append(regions, finding{
			Kind:   kindDuplication,
			Path:   lines[entry.starts[0]].Path,
			Line:   lines[entry.starts[0]].Line,
			Metric: MetricDuplication,
			Value:  entry.length,
			Owner:  ownerOf(places[0]),
			Digest: digest,
			Places: places,
		})
	}
	sort.Slice(regions, func(one, other int) bool {
		if regions[one].Path != regions[other].Path {
			return regions[one].Path < regions[other].Path
		}
		return regions[one].Line < regions[other].Line
	})
	return regions
}

// oneFile reports whether a window stayed inside one file: a "copy" that spans
// two files is the boundary between them, not a block.
func oneFile(window []dupLine) bool {
	for _, line := range window[1:] {
		if line.Path != window[0].Path {
			return false
		}
	}
	return true
}

func windowText(window []dupLine) string {
	builder := strings.Builder{}
	for _, line := range window {
		builder.WriteString(line.Text)
		builder.WriteString("\n")
	}
	return builder.String()
}

// digestOf names a copied block by its text, so that the baseline holds the copy
// and not the line it happens to start at.
func digestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func shift(values []int, by int) []int {
	shifted := make([]int, 0, len(values))
	for _, value := range values {
		shifted = append(shifted, value+by)
	}
	return shifted
}

func shiftKey(values []int, by int) string { return joinInts(shift(values, by)) }

// sortFindings orders the findings the way the report and the baseline read
// them: by file, then by kind, then by the position inside the file.
func sortFindings(findings []finding) {
	sort.Slice(findings, func(one, other int) bool {
		if findings[one].Path != findings[other].Path {
			return findings[one].Path < findings[other].Path
		}
		if findings[one].Kind != findings[other].Kind {
			return findings[one].Kind < findings[other].Kind
		}
		if findings[one].Line != findings[other].Line {
			return findings[one].Line < findings[other].Line
		}
		return findings[one].Metric < findings[other].Metric
	})
}
