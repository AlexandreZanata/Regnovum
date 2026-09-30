package main

import (
	"go/ast"
	"regexp"
	"strings"
)

// The measures the gate takes of one function. Each one has a declared budget in
// every scope, because a metric with a budget in one scope and none in another
// is a metric that stops biting exactly where nobody looked.
const (
	MetricCyclomatic = "cyclomatic"
	MetricNesting    = "nesting"
	MetricParameters = "parameters"
	MetricLines      = "lines"
	MetricStatements = "statements"

	// MetricDuplication is not a per-function measure: it is the length in
	// normalized lines of a block that appears in more than one place. It is a
	// budget only because the gate has to compare it with something.
	MetricDuplication = "duplication"
)

// The three kinds of code the gate judges. A test file is its own scope and not
// a sub-case of the code it tests: a table test is a long linear function by
// construction, and a budget that ignored that would push the suite towards
// worse tests to satisfy a number (P23-T07 is the task that judges tests).
const (
	ScopeProduct = "product"
	ScopeTooling = "tooling"
	ScopeTest    = "test"
)

// scopeRoots names where each root scope lives. The gate refuses a Go file that
// belongs to none of them: a file no scope classifies is a file no budget
// judges, and a silent hole is what a gate exists to prevent.
var scopeRoots = []struct {
	Prefix string
	Scope  string
}{
	{"internal/", ScopeProduct},
	{"cmd/", ScopeProduct},
	{"tools/", ScopeTooling},
}

// scopeOf classifies one file. Tests win over the root they live under, so a
// test of a tool is judged by the test budgets like any other test.
func scopeOf(path string) (string, bool) {
	if strings.HasSuffix(path, "_test.go") {
		return ScopeTest, true
	}
	for _, root := range scopeRoots {
		if strings.HasPrefix(path, root.Prefix) {
			return root.Scope, true
		}
	}
	return "", false
}

// ownerOf answers who answers for one file: the package directory, which is the
// unit that can be asked to change. It is derived instead of declared because a
// debt whose owner is typed by hand is a debt nobody can check.
func ownerOf(path string) string {
	if index := strings.LastIndex(path, "/"); index > 0 {
		return path[:index]
	}
	return path
}

// generatedLine is the Go toolchain's own marker: a file whose first comment
// block carries it is generated, whatever directory it lives in and whatever
// name it has. The convention is the one `go/ast` and every reader of Go code
// already agrees on.
var generatedLine = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// isGenerated reads the marker as the Go toolchain does: in the comments that
// precede the package clause, never as text anywhere in the file. A generator
// that carries the marker in a *string* — the tools that emit generated files
// do exactly that — is product code and is judged like any other; and a marker
// separated from the package clause by a blank line still counts, because that
// is how sqlc writes it.
func isGenerated(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, comment := range group.List {
			if generatedLine.MatchString(strings.TrimRight(comment.Text, " \t")) {
				return true
			}
		}
	}
	return false
}

// budget is one declared ceiling for one measure in one scope, with the loosest
// value the gate still accepts for it.
//
// Two knobs, and they hold each other: `Budget` is what the tree is held to
// today, and `Floor` is the ceiling on the budget itself. Raising the budget is
// how a green run would be bought, so the gate refuses a budget above its floor
// — the number can be argued with, but not into meaninglessness.
type budget struct {
	Metric string
	Scope  string
	Budget int
	Floor  int
	Why    string
}

// budgets is the policy, and it is a table so that every number is reviewable in
// one diff. The floors are the review band the project accepts for a tree that
// ships: a function a person can hold in their head (cyclomatic 20 in product
// code, the classic McCabe threshold), four levels of nesting, six parameters,
// one screen of lines, and sixty statements.
//
// The numbers were measured before they were chosen — the tree carries 7.208
// functions, and product code is already inside the tight band at the top (15
// functions above cyclomatic 20, none above nesting 4) while the tail is real
// (23 functions above six parameters, 24 above sixty statements). What the gate
// buys is not today's number: it is that the tail cannot grow, and that the next
// generated function meets the same bar.
var budgets = []budget{
	// Product code: the code that runs in production, held to the tight band.
	{MetricCyclomatic, ScopeProduct, 20, 25, "vinte pontos de decisão numa função é o limiar clássico de McCabe: acima disso a revisão deixa de ser leitura e vira simulação"},
	{MetricNesting, ScopeProduct, 4, 4, "quatro níveis é o ponto em que uma pessoa precisa manter o estado dos blocos anteriores na cabeça para ler a linha de dentro"},
	{MetricParameters, ScopeProduct, 6, 6, "acima de seis parâmetros a chamada deixa de caber numa linha e o leitor precisa contar posições em vez de ler nomes"},
	{MetricLines, ScopeProduct, 120, 150, "uma função que não cabe numa tela deixa de ser lida inteira, e o que não é lido inteiro não é revisado"},
	{MetricStatements, ScopeProduct, 60, 80, "sessenta statements é a medida de tamanho que não se move com formatação: a mesma função reindentada continua com o mesmo tamanho"},

	// Tooling: the auditors are switch-heavy judges over documents, and the
	// gate judges them so that the same bar reaches the code that judges.
	{MetricCyclomatic, ScopeTooling, 25, 30, "as ferramentas de auditoria são julgamentos sobre documentos: um pouco mais de decisão é inerente ao trabalho, e não é licença para o dobro"},
	{MetricNesting, ScopeTooling, 4, 4, "o mesmo limite do produto: aninhar um auditor não o torna mais claro"},
	{MetricParameters, ScopeTooling, 6, 6, "as ferramentas recebem o que precisam por struct, não por lista de argumentos"},
	{MetricLines, ScopeTooling, 150, 180, "um auditor acumula os casos que ele recusa; a folga é declarada, e é folga, não teto"},
	{MetricStatements, ScopeTooling, 60, 80, "a medida de tamanho que não depende de formatação"},

	// Tests: looser on purpose, and the reason is a property of the shape, not
	// a discount. A table test is a long linear function with one branch per
	// case, and forcing it under the product budgets would break it into
	// functions whose names nobody reads.
	{MetricCyclomatic, ScopeTest, 40, 60, "um teste de tabela cresce com os casos, e cada caso é uma linha: partir o teste para baixar o número piora o que ele prova"},
	{MetricNesting, ScopeTest, 5, 6, "o teste monta um cenário, e um nível a mais de cenário é mais legível que um helper que esconde o que o caso prepara"},
	{MetricParameters, ScopeTest, 8, 10, "o helper de teste recebe o que a tabela declara por caso"},
	{MetricLines, ScopeTest, 200, 250, "um teste com muitos casos é longo; o limite existe para o teste que perdeu a forma, não para o que tem trinta casos reais"},
	{MetricStatements, ScopeTest, 120, 160, "a medida de tamanho do teste, folgada pelo mesmo motivo"},
}

// budgetFor answers the declared budget of one measure in one scope.
func budgetFor(scope, metric string) (budget, bool) { return budgetForTable(budgets, scope, metric) }

// budgetForTable answers the same question over a table, so that the judgement
// of the policy can be exercised over a table that is not the delivered one.
func budgetForTable(table []budget, scope, metric string) (budget, bool) {
	for _, entry := range table {
		if entry.Scope == scope && entry.Metric == metric {
			return entry, true
		}
	}
	return budget{}, false
}

// duplicationWindow is the shortest run of normalized lines that counts as a
// copied block. Twenty lines is where a copy stops being a coincidence of shape
// — two functions that both validate a request agree for two or three lines and
// diverge after — and starts being a decision made twice.
const duplicationWindow = 20

// judgesDuplication reports whether a scope takes part in the duplication
// corpus. Tests are out on purpose: a fixture copied between tests is usually
// the case that makes the test readable, and the task that judges test quality
// is P23-T07, which can hold a test to something better than a line count.
func judgesDuplication(scope string) bool {
	return scope == ScopeProduct || scope == ScopeTooling
}

// unenforced is what the gate does not measure, printed by every run. The phase
// names cyclomatic or cognitive complexity; the gate measures cyclomatic and
// nesting, and nesting is the term that makes a cognitive score differ from a
// cyclomatic one (weight by depth). A weighted score would be a third number
// doing the work of two, and a gap that nobody prints reads like coverage.
const unenforced = "cognitive complexity as a weighted score: this gate measures the two terms it is built from (decision points and nesting depth), and a single weighted number would be a third budget for the same finding"
