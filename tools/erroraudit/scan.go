package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// The rules of this gate. Every finding names one of them, and the report has no
// bucket called "other": a finding the gate cannot classify is a finding the gate
// refuses to have.
const (
	RuleUnclosedResource = "unclosed-resource"
	RuleTransaction      = "transaction-without-rollback"
	RuleClientTimeout    = "client-without-timeout"
	RuleContextDropped   = "context-dropped"
	RuleContextTodo      = "context-todo"
	RuleUnwrappedError   = "unwrapped-error"
	RuleDiscardedError   = "discarded-error"
	RulePublicLeak       = "public-error-leak"
	RuleGoroutine        = "goroutine-without-owner"
)

// rules is the declared vocabulary, printed by every run so that the report can
// be read without the source. A rule that is not here cannot be produced: the
// scan returns findings by name, and a name outside this table is a programming
// error the gate turns into a refusal instead of a line.
var rules = []rule{
	{Name: RuleUnclosedResource, Reason: "o recurso adquirido que ninguém fecha vaza uma conexão, uma linha ou um arquivo por requisição até o processo parar de atender"},
	{Name: RuleTransaction, Reason: "a transação sem rollback deixa a trava aberta até o fim da conexão, e o erro que a abriria fica escrito na linha seguinte"},
	{Name: RuleClientTimeout, Reason: "o cliente HTTP sem teto espera para sempre e transforma uma lentidão do outro lado em indisponibilidade deste"},
	{Name: RuleContextDropped, Reason: "o contexto que chega e não é usado é o cancelamento do chamador que ninguém escuta"},
	{Name: RuleContextTodo, Reason: "o contexto que ninguém escolheu é o adiamento escrito em forma de argumento"},
	{Name: RuleUnwrappedError, Reason: "o erro construído de outro erro sem `%w` perde a cadeia, e `errors.Is` do chamador passa a dizer que não é o mesmo"},
	{Name: RuleDiscardedError, Reason: "a chamada cujo erro é descartado é a falha que o programa decide não saber"},
	{Name: RulePublicLeak, Reason: "a mensagem pública que carrega o erro interno ou a credencial é o vazamento que o chamador lê na resposta"},
	{Name: RuleGoroutine, Reason: "a goroutine que não nomeia dono nenhum é a que ninguém pode esperar, parar ou cobrar"},
}

// rule and finding are the vocabulary every gate of the phase prints, shared so
// that a finding of this gate reads exactly like a finding of the next one. What
// stays here is the table: the rules of *this* gate are the ones this file
// declares.
type rule = auditkit.Rule
type finding = auditkit.Finding

func ruleKnown(name string) bool {
	return auditkit.RuleKnown(rules, name)
}

// measured is everything one run observed, including the counts the report
// prints: a number that is not printed is a number nobody reviews. The counts
// are not decoration — they are the evidence that a rule has something to judge
// (`Acquired`, `Transactions`, `Clients`, `ContextParameters`, `Goroutines`,
// `Messages`, `FormatCalls`) and the evidence of what the gate had to leave out
// (`DiscardedCalls` against `DiscardedResolved`).
type measured struct {
	Files         int
	Tests         int
	Functions     int
	Generated     []string
	Unreadable    map[string]string
	Findings      []finding
	Acquired      int
	Transactions  int
	Clients       int
	ContextParams int
	Goroutines    int
	Messages      int
	FormatCalls   int
	Discarded     int
	Resolved      int
}

// source is one parsed file the gate judges.
type source struct {
	path string
	file *ast.File
}

// scanTree judges the repository, and the package it judges is the delivered
// one: `internal/`, `cmd/` and `tools/`. Generated code is excluded by
// provenance, inside the scan, and not by where it lives — the sqlc output of
// this repository owns rows, transactions and contexts, and judging it would
// bury the functions this gate exists to find.
func scanTree() (measured, error) {
	files, err := auditkit.GoFiles(".", auditkit.SkippedDirectories)
	if err != nil {
		return measured{}, err
	}
	return scan(files)
}

// scanDirectory judges one directory and everything under it, which is how the
// gate exercises its own fixtures: a fixture lives under testdata, exactly the
// place the tree walk refuses to enter.
func scanDirectory(directory string) (measured, error) {
	files, err := auditkit.GoFiles(directory, nil)
	if err != nil {
		return measured{}, err
	}
	return scan(files)
}

// scan parses every file, builds the index of what the module declares, and then
// judges. The index comes first because several rules are questions about the
// tree and not about the file: whether a called function answers with an error,
// and whether the method a goroutine starts belongs to a type that can be
// stopped, are both answered by looking at the declaration.
func scan(files []string) (measured, error) {
	result := measured{Unreadable: map[string]string{}}
	positions := token.NewFileSet()
	sources := []source{}
	index := newModuleIndex()
	for _, path := range files {
		raw, err := os.ReadFile(filepath.FromSlash(path))
		if err != nil {
			return measured{}, fmt.Errorf("read %s: %w", path, err)
		}
		file, err := parser.ParseFile(positions, path, raw, parser.ParseComments)
		if err != nil {
			result.Unreadable[path] = err.Error()
			continue
		}
		result.Files++
		if auditkit.IsGenerated(file) {
			result.Generated = append(result.Generated, path)
			continue
		}
		if strings.HasSuffix(path, "_test.go") {
			// The tests are the subject of P23-T07, and a rule that judged a
			// double, a fixture or a fake would be refusing the scaffolding
			// that makes the failure paths reachable. They are counted, not
			// judged.
			result.Tests++
			continue
		}
		sources = append(sources, source{path: path, file: file})
		index.read(path, file)
		index.readAliases(path, file)
	}
	if err := index.finish(); err != nil {
		return measured{}, err
	}
	for _, entry := range sources {
		result.Functions += countFunctions(entry.file)
		judge(&result, positions, index, entry.path, entry.file)
	}
	auditkit.SortFindings(result.Findings)
	return result, nil
}

// countFunctions counts the functions with a body the gate could judge, so that
// the report says how much code the numbers came from.
func countFunctions(file *ast.File) int {
	count := 0
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Body != nil {
			count++
		}
	}
	return count
}

// funcName names a function the way a reviewer reads it.
func funcName(declaration *ast.FuncDecl) string {
	name := declaration.Name.Name
	if declaration.Recv == nil || len(declaration.Recv.List) == 0 {
		return name
	}
	switch receiver := declaration.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return receiver.Name + "." + name
	case *ast.StarExpr:
		if identifier, ok := receiver.X.(*ast.Ident); ok {
			return identifier.Name + "." + name
		}
	}
	return name
}

// selectorText renders a selector chain as the source reads it —
// `response.Body.Close` — which is how the gate recognizes a cleanup on the
// value it acquired.
func selectorText(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		prefix := selectorText(typed.X)
		if prefix == "" {
			return typed.Sel.Name
		}
		return prefix + "." + typed.Sel.Name
	}
	return ""
}

// literalText reads the text of an expression when it is a literal, and answers
// with a description when it is not: the report shows what the gate read.
func literalText(expression ast.Expr) string {
	literal, ok := expression.(*ast.BasicLit)
	if !ok {
		return ""
	}
	if literal.Kind != token.STRING {
		return literal.Value
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return literal.Value
	}
	return value
}

// mentions reports whether the identifier appears anywhere in a node.
func mentions(node ast.Node, name string) bool {
	if node == nil || name == "" {
		return false
	}
	found := false
	ast.Inspect(node, func(candidate ast.Node) bool {
		identifier, ok := candidate.(*ast.Ident)
		if ok && identifier.Name == name {
			found = true
		}
		return !found
	})
	return found
}
