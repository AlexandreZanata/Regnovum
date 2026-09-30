package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// The rules of this gate. Every finding names one of them, and the report has no
// bucket called "other".
const (
	// RuleSkipped is the test that does not run. The program prohibits `t.Skip`
	// (`QUALITY_PROGRAM.md`, "Regras anti-falso-verde"): a suite that skipped is a
	// suite nobody can tell apart from one that passed.
	RuleSkipped = "skipped-test"
	// RuleVacuous is the test whose body cannot fail: it calls nothing, asserts
	// nothing and panics nowhere. It is the shape a generated test has when
	// nobody read the rule it was supposed to prove.
	RuleVacuous = "vacuous-test"
	// RuleTautology is the comparison of an expression with itself: the condition
	// ignores the value, so the assertion that guards it proves nothing.
	RuleTautology = "tautological-assertion"
	// RuleUnusedFixture is the file kept under `testdata/` that no Go file names.
	// A fixture nobody reads is a fixture that stopped checking something and
	// nobody noticed.
	RuleUnusedFixture = "unused-fixture"
	// RuleMockCrowd is the test file whose doubles outnumber what it checks: the
	// scaffolding is then the subject.
	RuleMockCrowd = "mock-crowd"
	// RuleSleep is the bare `time.Sleep` of a test body, the wait that hopes
	// instead of waiting for the thing it claims to prove. The program states the
	// rule in one line: `time.Sleep` does not synchronize a test.
	RuleSleep = "sleep-synchronization"
	// RuleUnregisteredSeed is the unseeded global generator of `math/rand` (or of
	// `/v2`) reached from a test. The registered source of the test platform is
	// `testsource`, whose seed is logged on every run and replayed with
	// `ARENA_TEST_SEED`: an entropy that is not registered cannot be replayed.
	RuleUnregisteredSeed = "unregistered-seed"
	// RuleIgnoredError is the error a test observes and then swallows: a guard
	// that neither fails the test nor promotes the failure.
	RuleIgnoredError = "ignored-error"
	// RuleIncompatibleOutcomes is the condition that is true (or false) whatever
	// the subject holds, because it asks one subject for two outcomes that cannot
	// both be its value. A test whose refusal can never fire accepts everything.
	RuleIncompatibleOutcomes = "incompatible-outcomes"
)

// measured is everything one run observed, including the counts behind the gap
// the report prints: a number that is not printed is a number nobody reviews.
type measured struct {
	Files      int
	Tests      int
	Assertions int
	Fixtures   int
	// OnlyDelegated counts the tests whose only effect is handing the *testing.T
	// to somebody else, and NoAssertionOfItsOwn the tests with no assertion call
	// of their own. Both are printed because they are the honest boundary of the
	// `vacuous-test` rule.
	OnlyDelegated       int
	NoAssertionOfItsOwn int
	// SleepsInLoop is the poll-shaped wait the gate measures and does not refuse,
	// and SleepsStaged the pause the test hands to a double instead of running
	// itself. Both are printed because they are the honest boundary of the sleep
	// rule.
	SleepsInLoop int
	SleepsStaged int
	Waived       int
}

// testSuffix and the assertion vocabulary are the two halves of the corpus: a
// file is judged as a test when the toolchain builds it as one, and a call is an
// assertion when its name says so. The vocabulary is deliberately a list of
// verbs and not a list of helpers: `assertError`, `mustCreateAccount` and
// `requireStatus` all announce what they do, and a helper that hides a failure
// without saying so is what the transitive rule below exists for.
const testSuffix = "_test.go"

var assertionVerbs = []string{
	"Error", "Errorf", "Fatal", "Fatalf", "Fail", "FailNow", "Check", "Checkf",
	"Expect", "Expected", "Require", "Assert", "Must", "Assertion",
}

// doublePrefixes are how a test names the test doubles it declares.
var doublePrefixes = []string{"fake", "stub", "spy", "mock", "double", "dummy", "seam", "fixture"}

// unseededGenerators are the global functions of the unseeded generator: the
// ones whose sequence is decided by the process and not by the registered seed.
var unseededGenerators = []string{"Int", "Intn", "Int31", "Int31n", "Int63", "Int63n", "Int64", "Float32", "Float64", "Shuffle", "Perm", "Seed", "Read", "NewSource", "Uint32", "Uint64", "Uint32N", "Uint64N"}

// configPackage and configGenerator are the import paths whose global generator
// the rule refuses.
var unseededPackages = []string{"math/rand", "math/rand/v2"}

// timeSleep names the call the sleep rule refuses.
const sleepFunction = "Sleep"

// fileState is one Go file the scan read, with the questions answered once. The
// file set travels with it because a finding names a line and the line is read
// from the parse that produced the tree, not from a second one.
type fileState struct {
	relative  string
	parsed    *ast.File
	body      []byte
	positions *token.FileSet
	imports   map[string]string // import alias -> path
}

// line is where a finding points, in the file it names.
func (f *fileState) line(pos token.Pos) int {
	return f.positions.Position(pos).Line
}

// scan walks a root and judges every test file and every fixture in it. The skip
// list is what makes the delivered tree and a fixture two different corpora: the
// tree refuses to enter `testdata` while the fixture *is* `testdata`.
func scan(root string, skipped map[string]bool) ([]auditkit.Finding, measured, map[auditkit.Finding]string, error) {
	result := measured{}
	findings := []auditkit.Finding{}
	sites := map[auditkit.Finding]string{}
	states, err := readTestFiles(root, skipped, &result)
	if err != nil {
		return nil, result, nil, err
	}
	for _, state := range states {
		fileFindings, err := judgeFile(state, &result, sites)
		if err != nil {
			return nil, result, nil, err
		}
		findings = append(findings, fileFindings...)
	}
	fixtures, fixtureFindings, err := judgeFixtures(root, skipped, states, &result)
	if err != nil {
		return nil, result, nil, err
	}
	result.Fixtures = fixtures
	for _, finding := range fixtureFindings {
		sites[finding] = finding.Path
	}
	findings = append(findings, fixtureFindings...)
	auditkit.SortFindings(findings)
	return findings, result, sites, nil
}

// readTestFiles parses every test file under a root, in a fixed order.
func readTestFiles(root string, skipped map[string]bool, result *measured) ([]*fileState, error) {
	paths, err := auditkit.Files(root, skipped, func(name string) bool { return strings.HasSuffix(name, testSuffix) })
	if err != nil {
		return nil, err
	}
	states := []*fileState{}
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil, fmt.Errorf("relate %s: %w", path, err)
		}
		relative = filepath.ToSlash(relative)
		state, err := readFile(relative, path)
		if err != nil {
			return nil, err
		}
		if state == nil {
			continue
		}
		states = append(states, state)
	}
	result.Files = len(states)
	return states, nil
}

// readFile parses one Go file. A file that does not parse is a refusal rather
// than a silence: the gate cannot judge what it cannot read, and a gate that
// skipped it would answer green over a file it never saw.
func readFile(relative, path string) (*fileState, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relative, err)
	}
	positions := token.NewFileSet()
	parsed, err := parser.ParseFile(positions, relative, body, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", relative, err)
	}
	return &fileState{relative: relative, parsed: parsed, body: body, positions: positions, imports: importsOf(parsed)}, nil
}

func importsOf(parsed *ast.File) map[string]string {
	imports := map[string]string{}
	for _, spec := range parsed.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = path
	}
	return imports
}

// judgeFile runs every rule that is a question about one file, and counts what
// the report prints.
func judgeFile(state *fileState, result *measured, sites map[auditkit.Finding]string) ([]auditkit.Finding, error) {
	findings := []auditkit.Finding{}
	tests := testFunctions(state.parsed)
	result.Tests += len(tests)
	assertions := countAssertions(state.parsed)
	result.Assertions += assertions
	findings = append(findings, judgeTautology(state)...)
	findings = append(findings, judgeIncompatible(state)...)
	findings = append(findings, judgeUnseeded(state)...)
	findings = append(findings, judgeSleep(state, result)...)
	findings = append(findings, judgeMockCrowd(state, assertions)...)
	for _, test := range tests {
		effect := effectOf(state, test)
		if !effect.observable {
			findings = append(findings, auditkit.Finding{
				Rule: RuleVacuous, Path: state.relative, Line: state.line(test.Pos()),
				Detail: fmt.Sprintf("a função %s não chama nada, não assere nada e não entra em pânico: um teste assim passa por não provar nada, e a P23-T07 o recusa antes que ele entre na árvore", test.Name.Name),
			})
		}
		if !effect.asserts {
			result.NoAssertionOfItsOwn++
		}
		if !effect.asserts && effect.observable {
			result.OnlyDelegated++
		}
	}
	findings = append(findings, judgeIgnoredError(state, tests)...)
	findings = append(findings, judgeSkip(state)...)
	for _, finding := range findings {
		sites[finding] = siteOf(state, tests, finding.Line)
	}
	return findings, nil
}

// siteOf names the place a finding is about, which is what an exception has to
// name: the test function the line belongs to, or the file when the finding is
// about the file itself. An exception that named a function it does not belong to
// would suspend nothing, and the gate compares the two strings exactly.
func siteOf(state *fileState, tests []*ast.FuncDecl, line int) string {
	for _, test := range tests {
		if line >= state.line(test.Pos()) && line <= state.line(test.End()) {
			return state.relative + "::" + test.Name.Name
		}
	}
	return state.relative
}

// effect answers what a test function does: whether it can fail at all, and
// whether it is the test itself that asserts.
type effect struct {
	observable bool
	asserts    bool
}

// effectOf reads a test body. A test is observable when it asserts, panics,
// hands the *testing.T to somebody else (the contract harnesses of this tree are
// called that way), declares a compile-time assertion, or sends on a channel —
// the effect the type system and the scheduler enforce. It asserts when the
// assertion call is its own: the difference is what the report prints, because a
// suite that delegates every assertion is a suite whose subject is a harness.
func effectOf(state *fileState, test *ast.FuncDecl) effect {
	result := effect{}
	receiver := testReceiver(test)
	ast.Inspect(test.Body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CallExpr:
			// Any call is an effect: it can fail, it can block, and it can panic,
			// and the type system is what decides whether it does. The rule refuses
			// the body that does nothing, not the body whose proof is a call.
			result.observable = true
			if isAssertion(typed) {
				result.asserts = true
			}
			if ident, ok := typed.Fun.(*ast.Ident); ok && ident.Name == "panic" {
				result.observable = true
			}
			if receiver != "" && passesIdentifier(typed, receiver) {
				result.observable = true
			}
			if isCompileTimeAssertion(typed) {
				result.observable = true
			}
		case *ast.AssignStmt:
			if isCompileTimeAssignment(typed) {
				result.observable = true
			}
		case *ast.DeclStmt:
			if declaresCompileTimeAssertion(typed) {
				result.observable = true
			}
		case *ast.SendStmt:
			result.observable = true
		case *ast.DeferStmt:
			result.observable = true
		}
		return true
	})
	return result
}

// testFunctions lists the functions the toolchain runs as tests, in the order
// they are declared.
func testFunctions(parsed *ast.File) []*ast.FuncDecl {
	functions := []*ast.FuncDecl{}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Recv != nil {
			continue
		}
		if !isTestName(fn.Name.Name) {
			continue
		}
		functions = append(functions, fn)
	}
	return functions
}

// isTestName answers whether a name is one the `go test` tool runs. The second
// character is a letter or a digit and not a lower-case one, which is the
// toolchain's own rule: `Testify` is a test and `Testing` is not.
func isTestName(name string) bool {
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		if rest == "" || rest[0] < 'A' || rest[0] > 'Z' {
			return false
		}
		return true
	}
	return false
}

// testReceiver answers the name of the testing value a test receives: the
// receiver of `t.Error`, and what tells a delegation apart from a call.
func testReceiver(test *ast.FuncDecl) string {
	if test.Type.Params == nil || len(test.Type.Params.List) == 0 {
		return ""
	}
	names := test.Type.Params.List[0].Names
	if len(names) == 0 {
		return ""
	}
	return names[0].Name
}

func passesIdentifier(call *ast.CallExpr, name string) bool {
	for _, argument := range call.Args {
		if ident, ok := argument.(*ast.Ident); ok && ident.Name == name {
			return true
		}
	}
	return false
}

// isCompileTimeAssertion answers whether a call is the assertion the compiler
// checks: `(*Type)(nil)` or a conversion of one to the other, which is how this
// tree states that a type satisfies a port.
func isCompileTimeAssertion(call *ast.CallExpr) bool {
	if _, ok := call.Fun.(*ast.ParenExpr); !ok {
		return false
	}
	for _, argument := range call.Args {
		if ident, ok := argument.(*ast.Ident); ok && ident.Name == "nil" {
			return true
		}
	}
	return false
}

// isCompileTimeAssignment answers whether an assignment states a compile-time
// assertion: `_ = (*Type)(nil)` or `_ = value.(Interface)`.
func isCompileTimeAssignment(statement *ast.AssignStmt) bool {
	if len(statement.Lhs) != 1 || len(statement.Rhs) != 1 {
		return false
	}
	ident, ok := statement.Lhs[0].(*ast.Ident)
	if !ok || ident.Name != "_" {
		return false
	}
	switch typed := statement.Rhs[0].(type) {
	case *ast.TypeAssertExpr:
		return true
	case *ast.CallExpr:
		return isCompileTimeAssertion(typed)
	}
	return false
}

// declaresCompileTimeAssertion answers whether a declaration inside a test states
// one: `var _ Interface = (*Type)(nil)`, which is the shape the port contracts of
// this tree use, and which fails the build rather than the run.
func declaresCompileTimeAssertion(declaration *ast.DeclStmt) bool {
	general, ok := declaration.Decl.(*ast.GenDecl)
	if !ok || general.Tok != token.VAR {
		return false
	}
	for _, spec := range general.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok || len(value.Names) != 1 || value.Names[0].Name != "_" {
			continue
		}
		for _, expression := range value.Values {
			if call, ok := expression.(*ast.CallExpr); ok && isCompileTimeAssertion(call) {
				return true
			}
		}
	}
	return false
}

// countAssertions counts the assertion calls of a file, which is the denominator
// of the mock rule.
func countAssertions(parsed *ast.File) int {
	count := 0
	ast.Inspect(parsed, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && isAssertion(call) {
			count++
		}
		return true
	})
	return count
}

// isAssertion answers whether a call announces that it can fail a test.
func isAssertion(call *ast.CallExpr) bool {
	name := calleeName(call)
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	for _, verb := range assertionVerbs {
		if lower == strings.ToLower(verb) || strings.HasPrefix(lower, strings.ToLower(verb)) {
			return true
		}
	}
	// `t.Fatalf` and `t.Helper` are told apart by the verb, and the harnesses of
	// this tree name themselves after it: `assertError`, `mustCreateAccount`,
	// `requireStatus`, `contract.RunSenderContract` is the one that does not, and
	// it is covered by the delegation half of the rule.
	for _, prefix := range []string{"assert", "must", "require", "expect", "check"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// judgeSkip refuses every `t.Skip`, which is what the program prohibits: a test
// the pipeline does not run is indistinguishable from a test that passed.
func judgeSkip(state *fileState) []auditkit.Finding {
	findings := []auditkit.Finding{}
	ast.Inspect(state.parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call)
		if name != "Skip" && name != "SkipNow" && name != "Skipf" {
			return true
		}
		findings = append(findings, auditkit.Finding{
			Rule: RuleSkipped, Path: state.relative, Line: state.line(call.Pos()),
			Detail: "a suíte pula este teste: o programa proíbe `t.Skip` porque um teste que não roda é indistinguível de um teste que passou, e a saída declarada é a exceção registrada em `quality/test-waivers.json` com dono e expiração, ou a correção da condição que hoje o desliga",
		})
		return true
	})
	return findings
}

// judgeTautology refuses a comparison of an expression with itself.
func judgeTautology(state *fileState) []auditkit.Finding {
	findings := []auditkit.Finding{}
	ast.Inspect(state.parsed, func(node ast.Node) bool {
		binary, ok := node.(*ast.BinaryExpr)
		if !ok || !isComparison(binary.Op) {
			return true
		}
		if expressionText(binary.X) != expressionText(binary.Y) {
			return true
		}
		findings = append(findings, auditkit.Finding{
			Rule: RuleTautology, Path: state.relative, Line: state.line(binary.Pos()),
			Detail: fmt.Sprintf("a comparação `%s %s %s` compara uma expressão com ela mesma: a condição não lê valor nenhum, então a asserção que ela guarda não prova nada", expressionText(binary.X), binary.Op, expressionText(binary.Y)),
		})
		return true
	})
	return findings
}

// judgeIncompatible refuses a condition that asks one subject for two outcomes
// it cannot hold at the same time — the two boolean values, or a comparison and
// its own negation. Such a condition is true (or false) whatever the subject is,
// so the assertion it guards can never distinguish anything.
func judgeIncompatible(state *fileState) []auditkit.Finding {
	findings := []auditkit.Finding{}
	ast.Inspect(state.parsed, func(node ast.Node) bool {
		binary, ok := node.(*ast.BinaryExpr)
		if !ok || (binary.Op != token.LOR && binary.Op != token.LAND) {
			return true
		}
		subject, outcomes, ok := booleanOutcomes(binary)
		if !ok {
			return true
		}
		findings = append(findings, auditkit.Finding{
			Rule: RuleIncompatibleOutcomes, Path: state.relative, Line: state.line(binary.Pos()),
			Detail: fmt.Sprintf("a condição pergunta por %s e por %s ao mesmo sujeito `%s`: os dois resultados não podem ser o valor dele, então a condição é constante e a recusa que ela guarda ou nunca dispara ou dispara sempre", outcomes[0], outcomes[1], subject),
		})
		return true
	})
	return findings
}

// booleanOutcomes answers whether a disjunction or conjunction is built from two
// comparisons of one subject against the two boolean values.
func booleanOutcomes(binary *ast.BinaryExpr) (string, []string, bool) {
	one, okOne := binary.X.(*ast.BinaryExpr)
	two, okTwo := binary.Y.(*ast.BinaryExpr)
	if !okOne || !okTwo || !isComparison(one.Op) || !isComparison(two.Op) || one.Op != two.Op {
		return "", nil, false
	}
	if expressionText(one.X) != expressionText(two.X) {
		return "", nil, false
	}
	first, second := expressionText(one.Y), expressionText(two.Y)
	if !isBoolean(first) || !isBoolean(second) || first == second {
		return "", nil, false
	}
	return expressionText(one.X), []string{first, second}, true
}

func isBoolean(text string) bool { return text == "true" || text == "false" }

// judgeUnseeded refuses the global generator of `math/rand` inside a test: the
// sequence of an unseeded generator is decided by the process, so a failure it
// produces cannot be replayed from the registered seed.
func judgeUnseeded(state *fileState) []auditkit.Finding {
	findings := []auditkit.Finding{}
	aliases := map[string]bool{}
	for alias, path := range state.imports {
		for _, wanted := range unseededPackages {
			if path == wanted {
				aliases[alias] = true
			}
		}
	}
	if len(aliases) == 0 {
		return findings
	}
	ast.Inspect(state.parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok || !aliases[ident.Name] {
			return true
		}
		matched := false
		for _, name := range unseededGenerators {
			if selector.Sel.Name == name {
				matched = true
				break
			}
		}
		if !matched {
			return true
		}
		findings = append(findings, auditkit.Finding{
			Rule: RuleUnregisteredSeed, Path: state.relative, Line: state.line(call.Pos()),
			Detail: fmt.Sprintf("o teste chama `%s` do gerador global: a entropia de um gerador não semeado é decidida pelo processo e uma falha que ela produz não se reproduz; a fonte registrada é `testsource`, cuja semente é impressa em toda execução e reexecutada com `ARENA_TEST_SEED`", expressionText(call.Fun)),
		})
		return true
	})
	return findings
}

// judgeSleep refuses the bare `time.Sleep` of a test body: the wait that hopes
// the background work finished instead of waiting for it. Every sleep the body
// of the test runs is judged, at whatever depth it sits — the pause does not
// stop hoping for being inside an `if` — and two shapes are measured and printed
// rather than refused, because neither is the test waiting for its subject: the
// poll inside a loop re-reads the condition it waits for, and the pause inside a
// double the test hands over is the timing of the subject, not of the test. The
// gap says so, and the report prints both counts.
func judgeSleep(state *fileState, result *measured) []auditkit.Finding {
	findings := []auditkit.Finding{}
	for _, test := range testFunctions(state.parsed) {
		nesting := enclosing(test.Body)
		staged := stagedNodes(test.Body)
		ast.Inspect(test.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isTimeSleep(call, state) {
				return true
			}
			if staged[call] {
				result.SleepsStaged++
				return true
			}
			if nesting.inLoop[call] {
				result.SleepsInLoop++
				return true
			}
			findings = append(findings, auditkit.Finding{
				Rule: RuleSleep, Path: state.relative, Line: state.line(call.Pos()),
				Detail: fmt.Sprintf("o corpo de %s espera com `%s`: uma pausa fixa não sincroniza nada — ou o teste espera pela condição que ele afirma, ou ele afirma o que pode observar", test.Name.Name, expressionText(call)),
			})
			return true
		})
	}
	return findings
}

// isTimeSleep answers whether a call is the sleep of the standard library, which
// is read from the imports of the file and never from the name alone: a method
// that happens to be called `Sleep` is not the one the rule is about.
func isTimeSleep(call *ast.CallExpr, state *fileState) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != sleepFunction {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	return state.imports[ident.Name] == "time"
}

// judgeMockCrowd refuses a test file whose doubles outnumber its assertions: the
// scaffolding is then what the file is about.
func judgeMockCrowd(state *fileState, assertions int) []auditkit.Finding {
	doubles := []string{}
	for _, declaration := range state.parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.TYPE {
			continue
		}
		for _, spec := range general.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			lower := strings.ToLower(typeSpec.Name.Name)
			for _, prefix := range doublePrefixes {
				if strings.HasPrefix(lower, prefix) {
					doubles = append(doubles, typeSpec.Name.Name)
					break
				}
			}
		}
	}
	if len(doubles) == 0 || len(doubles) <= assertions {
		return nil
	}
	return []auditkit.Finding{{
		Rule: RuleMockCrowd, Path: state.relative, Line: state.line(state.parsed.Package),
		Detail: fmt.Sprintf("o arquivo declara %d dublê(s) (%s) e faz %d asserção(ões): quando o andaime é maior que o que ele verifica, o teste passou a ser sobre o andaime", len(doubles), strings.Join(doubles, ", "), assertions),
	}}
}

// judgeIgnoredError refuses an error a test observes and then swallows: the
// guard does not fail the test, and it does not promote the failure to the
// caller either. `continue` and `break` are legal answers — they say the case
// does not apply — and a guard that fails the test is what the rule asks for.
func judgeIgnoredError(state *fileState, tests []*ast.FuncDecl) []auditkit.Finding {
	findings := []auditkit.Finding{}
	for _, test := range tests {
		inside := closureNodes(test.Body)
		ast.Inspect(test.Body, func(node ast.Node) bool {
			guard, ok := node.(*ast.IfStmt)
			if !ok || inside[guard] || !testsAnError(guard.Cond) || guard.Body == nil {
				return true
			}
			// A guard that skips is owned by the rule that refuses the skip: one
			// defect is one finding, and `skipped-test` is the rule that names it.
			if skipsTest(guard.Body) {
				return true
			}
			if promotesFailure(guard.Body) {
				return true
			}
			// The failure may be handled in the `else`: `if statErr == nil { use it }
			// else { t.Fatalf }` is a guard that does answer for the error.
			if guard.Else != nil && promotesFailure(guard.Else) {
				return true
			}
			findings = append(findings, auditkit.Finding{
				Rule: RuleIgnoredError, Path: state.relative, Line: state.line(guard.Pos()),
				Detail: fmt.Sprintf("o teste de %s guarda um erro e não o cobra: o corpo do `if` não falha o teste nem devolve a falha, então o caminho de erro termina verde — o erro observado tem de virar `t.Fatal`/`t.Error`, ou o caso tem de ser declarado com `continue`/`break`", test.Name.Name),
			})
			return true
		})
	}
	return findings
}

// skipsTest answers whether a body turns a test off with `t.Skip`. The shape is
// reported once, by the rule that exists for it, instead of twice.
func skipsTest(body *ast.BlockStmt) bool {
	skips := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch calleeName(call) {
		case "Skip", "SkipNow", "Skipf":
			skips = true
		}
		return true
	})
	return skips
}

// closureNodes is every node inside a function literal of a test body. The guard
// of a worker goroutine or of a fuzz body is left out of the error rule on
// purpose: the closure collects the failure and the test asserts the collection
// afterwards, which is the shape the concurrency tests of this tree have, and a
// gate that refused it would be refusing the way a race is proved.
func closureNodes(body *ast.BlockStmt) map[ast.Node]bool {
	inside := map[ast.Node]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		literal, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}
		ast.Inspect(literal.Body, func(inner ast.Node) bool {
			if inner != nil {
				inside[inner] = true
			}
			return true
		})
		return true
	})
	return inside
}

// testsAnError answers whether a condition is a comparison whose operands name an
// error: the identifier itself or a call that produces one.
func testsAnError(condition ast.Expr) bool {
	binary, ok := condition.(*ast.BinaryExpr)
	if !ok || !isComparison(binary.Op) {
		return false
	}
	for _, operand := range []ast.Expr{binary.X, binary.Y} {
		if mentionsError(operand) {
			return true
		}
	}
	return false
}

func mentionsError(expression ast.Expr) bool {
	switch typed := expression.(type) {
	case *ast.Ident:
		lower := strings.ToLower(typed.Name)
		return lower == "err" || strings.HasSuffix(lower, "err") || strings.HasSuffix(lower, "error")
	case *ast.CallExpr:
		for _, argument := range typed.Args {
			if mentionsError(argument) {
				return true
			}
		}
		return mentionsError(typed.Fun)
	case *ast.SelectorExpr:
		return mentionsError(typed.X) || mentionsError(typed.Sel)
	}
	return false
}

// promotesFailure answers whether the body of a guard fails the test, ends the
// case or hands the failure on: an assertion call, a panic, a `continue` of a
// table, a `break`, or a `return` that carries the error out of a helper.
func promotesFailure(node ast.Node) bool {
	if node == nil {
		return false
	}
	promotes := false
	ast.Inspect(node, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CallExpr:
			if isAssertion(typed) {
				promotes = true
			}
			switch calleeName(typed) {
			case "panic", "Exit", "Fatalf":
				// `os.Exit` is how the child process of this tree answers: it is a
				// process that reports through its status, and its guard does decide.
				promotes = true
			}
		case *ast.BranchStmt:
			if typed.Tok == token.CONTINUE || typed.Tok == token.BREAK {
				promotes = true
			}
		case *ast.ReturnStmt:
			if len(typed.Results) > 0 {
				promotes = true
			}
		}
		return true
	})
	return promotes
}

// judgeFixtures refuses the file kept under `testdata/` that no Go file of the
// tree names. A fixture is named by its file name, by the name without the
// extension, by any segment of a composed name, or by the directory it lives in
// — the three ways this tree reads one — and a fixture named by none of them is
// a fixture nobody reads.
func judgeFixtures(root string, skipped map[string]bool, states []*fileState, result *measured) (int, []auditkit.Finding, error) {
	fixtures, err := fixtureFiles(root, skipped)
	if err != nil {
		return 0, nil, err
	}
	mentions := mentionCorpus(root, states)
	findings := []auditkit.Finding{}
	for _, fixture := range fixtures {
		if namedBy(fixture, mentions) {
			continue
		}
		findings = append(findings, auditkit.Finding{
			Rule: RuleUnusedFixture, Path: fixture,
			Detail: "nenhum arquivo Go desta árvore nomeia esta fixture: uma fixture que ninguém lê é uma fixture que parou de verificar algo sem que ninguém visse; remova o arquivo, ou registre o teste que o lê",
		})
	}
	return len(fixtures), findings, nil
}

// fixtureFiles lists the files under `testdata/`, which is the corpus the tree
// walk refuses to enter and this rule exists for.
func fixtureFiles(root string, skipped map[string]bool) ([]string, error) {
	entered := map[string]bool{}
	for name := range skipped {
		if name == "testdata" {
			continue
		}
		entered[name] = true
	}
	paths, err := auditkit.Files(root, entered, func(string) bool { return true })
	if err != nil {
		return nil, err
	}
	fixtures := []string{}
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil, fmt.Errorf("relate %s: %w", path, err)
		}
		relative = filepath.ToSlash(relative)
		if underTestdata(relative) {
			fixtures = append(fixtures, relative)
		}
	}
	return fixtures, nil
}

// underTestdata answers whether a path lives inside a `testdata` directory. The
// question is asked of the segments and not of the text, because the fixture of
// this gate is itself a `testdata` directory and its own paths start with the
// name: a substring search would miss exactly the corpus this rule is proved on.
func underTestdata(relative string) bool {
	for _, segment := range strings.Split(relative, "/") {
		if segment == "testdata" {
			return true
		}
	}
	return false
}

// mentionCorpus is the text a fixture name is looked for in: every Go file of the
// tree (the corpus plus the files that are not tests) and the Makefile, which is
// where a fixture produced by a target is named.
func mentionCorpus(root string, states []*fileState) []string {
	corpus := []string{}
	for _, state := range states {
		corpus = append(corpus, string(state.body))
	}
	goFiles, err := auditkit.Files(root, auditkit.SkippedDirectories, func(name string) bool {
		return strings.HasSuffix(name, ".go")
	})
	if err == nil {
		for _, path := range goFiles {
			body, err := os.ReadFile(path)
			if err == nil {
				corpus = append(corpus, string(body))
			}
		}
	}
	if body, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil {
		corpus = append(corpus, string(body))
	}
	return corpus
}

// namedBy answers whether any of the names a fixture can be read by appears in
// the corpus.
func namedBy(fixture string, corpus []string) bool {
	base := filepath.Base(fixture)
	names := map[string]bool{base: true}
	if dot := strings.LastIndex(base, "."); dot > 0 {
		names[base[:dot]] = true
	}
	for _, segment := range strings.Split(base, ".") {
		if segment != "" {
			names[segment] = true
		}
	}
	for _, part := range strings.Split(filepath.Dir(fixture), "/") {
		if part != "" && part != "testdata" && part != "." {
			names[part] = true
		}
	}
	for _, text := range corpus {
		for name := range names {
			if strings.Contains(text, name) {
				return true
			}
		}
	}
	return false
}

// enclosing answers, for every call of a body, whether it sits inside a loop: a
// `time.Sleep` there is a poll, which the rule measures instead of refusing.
type nesting struct {
	inLoop map[ast.Node]bool
}

func enclosing(body *ast.BlockStmt) nesting {
	result := nesting{inLoop: map[ast.Node]bool{}}
	ast.Inspect(body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.ForStmt:
			markAll(result.inLoop, typed.Body)
		case *ast.RangeStmt:
			markAll(result.inLoop, typed.Body)
		}
		return true
	})
	return result
}

// stagedNodes is every node inside a function literal the test hands to somebody
// else — the job handler, the provider stub, the slow handler of a deadline
// test. The test does not run that body: the product does. A pause inside it is
// therefore the timing of the subject under test and not the synchronization of
// the test, and the rule counts it instead of refusing it. A literal the test
// itself calls — an immediate invocation, a `go` statement, a `defer` — is the
// test's own flow, and a pause there is judged like any other. The exception is
// the double the test calls through a name it bound itself, which the gap
// declares: the rule reads the call site, not the binding.
func stagedNodes(body *ast.BlockStmt) map[ast.Node]bool {
	called := map[ast.Node]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if _, ok := call.Fun.(*ast.FuncLit); ok {
			called[call.Fun] = true
		}
		return true
	})
	staged := map[ast.Node]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		literal, ok := node.(*ast.FuncLit)
		if !ok || called[literal] {
			return true
		}
		markAll(staged, literal.Body)
		return true
	})
	return staged
}

func markAll(set map[ast.Node]bool, node ast.Node) {
	ast.Inspect(node, func(inner ast.Node) bool {
		if inner != nil {
			set[inner] = true
		}
		return true
	})
}

func calleeName(call *ast.CallExpr) string {
	switch typed := call.Fun.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return typed.Sel.Name
	}
	return ""
}

func isComparison(operator token.Token) bool {
	switch operator {
	case token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ:
		return true
	}
	return false
}

func expressionText(node ast.Node) string {
	var out strings.Builder
	if err := printer.Fprint(&out, token.NewFileSet(), node); err != nil {
		return ""
	}
	return out.String()
}
