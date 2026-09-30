// Command erroraudit is the gate of P23-T05: errors, contexts and resources.
//
// It refuses, naming the rule:
//
//   - a resource acquired and never released: an HTTP response, a row set, a
//     file, a connection, a transaction. The value has to be closed, rolled back
//     or handed to another call in the function that acquired it, and the finding
//     says which of the three is missing;
//   - a transaction that is never rolled back, which is the error path of a
//     transaction whose commit is the only path written;
//   - an `http.Client` with no `Timeout`, the client that waits for the other
//     side forever;
//   - a context parameter the body never uses, and `context.TODO()`, the context
//     nobody chose;
//   - `fmt.Errorf` over an error without `%w`, which breaks the chain the caller's
//     `errors.Is` walks;
//   - a call whose error is thrown away, when the call is one this module
//     declares — the failure of a use case or of a query;
//   - a public message that carries the internal error or a credential into the
//     RFC 9457 detail the caller reads;
//   - a goroutine that names no owner: no context, no channel, no wait group and
//     no method of a type that knows how to stop.
//
// Three things about the measurement are worth stating where the numbers are
// read:
//
//   - generated code is excluded by **provenance**, never by directory, and it
//     matters more here than anywhere: the sqlc output owns rows, transactions
//     and contexts, and judging it would bury the functions this gate exists to
//     find. The marker is the Go toolchain's, read where the toolchain reads it,
//     and the two fixtures prove the exclusion in both directions;
//   - the tests are out of the corpus by role and counted, because the quality of
//     a test is the subject of P23-T07 and a gate that judged a double would be
//     refusing the scaffolding that makes the failure paths reachable;
//   - what this gate cannot judge is printed with the tree in hand: a call whose
//     receiver is an interface, a standard library function or a function held in
//     a variable is not resolved without a type-checker, so the number of calls
//     the gate could trace is printed next to the number of calls it discarded.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// fixtureRoot is where the fixtures of this gate live: a directory the tree walk
// refuses to enter, which is why the proof of every rule has to ask for it by
// name.
const fixtureRoot = "tools/erroraudit/testdata"

// family is one rule with the fixture that proves it still bites, in the shape
// every gate of the phase declares.
type family = auditkit.Family

// families is the executable map of the rules. Every family has a fixture that
// must be refused: a rule that stopped biting has to fail here, by name, instead
// of disappearing from a green run.
var families = []family{
	{Name: RuleUnclosedResource, Target: "unclosed",
		Reason: "o recurso adquirido e não liberado vaza uma conexão por chamada"},
	{Name: RuleTransaction, Target: "transaction",
		Reason: "a transação sem rollback deixa a trava e o erro sem caminho de volta"},
	{Name: RuleClientTimeout, Target: "client",
		Reason: "o cliente HTTP sem teto transforma a lentidão do outro lado em indisponibilidade deste"},
	{Name: RuleContextDropped, Target: "contextdropped",
		Reason: "o contexto que chega e não é usado deixa o cancelamento do chamador sem ouvido"},
	{Name: RuleContextTodo, Target: "contexttodo",
		Reason: "o contexto que ninguém escolheu é o adiamento escrito em forma de argumento"},
	{Name: RuleUnwrappedError, Target: "wrapping",
		Reason: "sem `%w` a cadeia do erro se perde e `errors.Is` do chamador deixa de casar"},
	{Name: RuleDiscardedError, Target: "discarded",
		Reason: "o erro descartado é a falha que o programa decide não saber"},
	{Name: RulePublicLeak, Target: "leak",
		Reason: "a mensagem pública que carrega o erro interno ou o segredo vaza na resposta"},
	{Name: RuleGoroutine, Target: "goroutine",
		Reason: "a goroutine sem dono é a que ninguém pode esperar, parar ou cobrar"},
}

// cleanFixtures are the fixtures that must NOT be refused. A rule proved only in
// the direction that refuses is a rule that could be refusing everything while
// looking strict, and every rule of this gate has a legal neighbour: a resource
// that is closed or handed over, a context that is used, a wrap that keeps the
// chain, a goroutine that names its owner.
var cleanFixtures = []family{
	{Name: RuleUnclosedResource, Target: "clean/resource",
		Reason: "o recurso fechado, e o recurso entregue a quem o fecha, não são achado"},
	{Name: RuleTransaction, Target: "clean/transaction",
		Reason: "a transação com `defer Rollback` é a forma que o gate exige"},
	{Name: RuleClientTimeout, Target: "clean/client",
		Reason: "o cliente com teto declarado é o que o gate exige"},
	{Name: RuleContextDropped, Target: "clean/context",
		Reason: "o contexto usado, e o parâmetro em branco declarado de propósito, não são achado"},
	{Name: RuleContextTodo, Target: "clean/context",
		Reason: "o contexto derivado do chamador é o que o gate exige"},
	{Name: RuleUnwrappedError, Target: "clean/wrapping",
		Reason: "o `%w` que preserva a cadeia, e o argumento que não é erro, não são achado"},
	{Name: RuleDiscardedError, Target: "clean/discarded",
		Reason: "a chamada que não responde com erro não é achado"},
	{Name: RulePublicLeak, Target: "clean/leak",
		Reason: "a mensagem literal, o campo público do erro de domínio e o erro entregue a WriteProblem (que o recolhe) não são achado"},
	{Name: RuleGoroutine, Target: "clean/goroutine",
		Reason: "os donos que a árvore usa — contexto, canal, valor de tipo que sabe parar e função que fecha o que recebe — não são achado"},
}

// provenanceProbe is one proof about the exclusion itself, in both directions:
// the marked file has to be excluded, and the file that only mentions the marker
// has to be judged.
type provenanceProbe = auditkit.ProvenanceProbe

var provenanceProbes = []provenanceProbe{
	{Name: "um arquivo gerado é excluído pelo marcador", Target: "generated", Excluded: true},
	{Name: "um arquivo que só menciona o marcador é julgado", Target: "decoy", Excluded: false},
}

func main() {
	root := flag.String("root", ".", "the repository root")
	flag.Parse()
	if err := run(*root); err != nil {
		fmt.Fprintf(os.Stderr, "erroraudit: %v\n", err)
		os.Exit(1)
	}
}

func run(root string) error {
	if err := os.Chdir(root); err != nil {
		return fmt.Errorf("enter %s: %w", root, err)
	}
	if err := proveFamilies(fixtureRoot); err != nil {
		return err
	}
	tree, err := scanTree()
	if err != nil {
		return err
	}
	if tree.Files == 0 || tree.Functions == 0 {
		return fmt.Errorf("the walk judged %d file(s) and %d function(s): a gate that measured nothing is a gate that refuses nothing", tree.Files, tree.Functions)
	}
	if len(tree.Generated) == 0 {
		return fmt.Errorf("no file was excluded by provenance: the marker this gate reads is the Go toolchain's, and a tree with no generated file means the marker stopped matching")
	}
	if len(tree.Unreadable) > 0 {
		return fmt.Errorf("the gate cannot read %d file(s): a file whose silence nobody can vouch for is a hole", len(tree.Unreadable))
	}
	unclassified := []string{}
	for _, entry := range tree.Findings {
		if !ruleKnown(entry.Rule) {
			unclassified = append(unclassified, entry.String())
		}
	}
	if len(unclassified) > 0 {
		return fmt.Errorf("the gate produced a finding it cannot classify:\n  - %s", strings.Join(unclassified, "\n  - "))
	}
	report(tree)
	if len(tree.Findings) == 0 {
		fmt.Printf("erroraudit: OK — nenhum achado, e cada família segue recusando a própria fixture\n")
		return nil
	}
	lines := make([]string, 0, len(tree.Findings))
	for _, entry := range tree.Findings {
		lines = append(lines, entry.String())
	}
	return fmt.Errorf("the error and resource gate refused the tree:\n  - %s", strings.Join(lines, "\n  - "))
}

// proveFamilies runs every fixture and requires its rule to refuse it, then runs
// the clean fixtures and requires silence, and holds the two directions of the
// provenance exclusion. The three proofs live in `auditkit` because the gate of
// P23-T03 measured them copied between the tools of the phase, and a gate that
// refuses duplicated code cannot keep its own; what stays here is the fixture
// table and the scanner the proofs read the fixtures with.
func proveFamilies(root string) error {
	if err := auditkit.ProveRefused(root, families, readFixture); err != nil {
		return err
	}
	if err := auditkit.ProveAccepted(root, cleanFixtures, readFixture); err != nil {
		return err
	}
	return auditkit.ProveProvenance(root, provenanceProbes, readFixture)
}

// readFixture reads one fixture of this gate the way the tree is read.
func readFixture(_ family, directory string) (auditkit.ScanResult, error) {
	measured, err := scanDirectory(directory)
	if err != nil {
		return auditkit.ScanResult{}, err
	}
	return auditkit.ScanResult{Generated: measured.Generated, Findings: measured.Findings}, nil
}

// report prints everything this run measured: the corpus, the rules, the counts
// each rule had to work with, and the gaps the gate declares. A number that is not
// printed is a number nobody reviews.
func report(tree measured) {
	fmt.Printf("erroraudit: julgou %d arquivo(s) e %d função(ões) do código entregue; %d arquivo(s) de teste fora do corpus (P23-T07) e %d excluído(s) por proveniência\n",
		tree.Files-tree.Tests, tree.Functions, tree.Tests, len(tree.Generated))
	for _, entry := range rules {
		fmt.Printf("erroraudit: regra %s — %s\n", entry.Name, entry.Reason)
	}
	fmt.Printf("erroraudit: medido — %d recurso(s) adquirido(s), %d transação(ões), %d cliente(s) HTTP, %d contexto(s) de parâmetro, %d goroutine(s), %d mensagem(ns) pública(s), %d fmt.Errorf, %d chamada(s) com resultado descartado (%d resolvida(s) no módulo)\n",
		tree.Acquired, tree.Transactions, tree.Clients, tree.ContextParams, tree.Goroutines,
		tree.Messages, tree.FormatCalls, tree.Discarded, tree.Resolved)
	for _, entry := range families {
		fmt.Printf("erroraudit: família %s — recusa a fixture %s/%s\n", entry.Name, fixtureRoot, entry.Target)
	}
	for _, entry := range cleanFixtures {
		fmt.Printf("erroraudit: família %s — aceita a fixture %s/%s (%s)\n", entry.Name, fixtureRoot, entry.Target, entry.Reason)
	}
	for _, probe := range provenanceProbes {
		fmt.Printf("erroraudit: proveniência — %s (%s/%s)\n", probe.Name, fixtureRoot, probe.Target)
	}
	fmt.Printf("erroraudit: %d achado(s)\n", len(tree.Findings))
	fmt.Printf("erroraudit: não julgado por este portão — %s\n", unenforced(tree))
}

// unenforced states the gaps with the tree in hand. The first one is the boundary
// of the whole gate: a syntax tree without types cannot tell what a call answers
// when the call is a method on an interface, a function of the standard library or
// a function held in a variable, and the number of calls the gate could trace is
// printed for the reader to judge the coverage.
func unenforced(tree measured) string {
	coverage := "nenhuma chamada com resultado descartado foi resolvida"
	if tree.Discarded > 0 {
		coverage = fmt.Sprintf("%d de %d chamada(s) com resultado descartado foram resolvidas contra as declarações do módulo", tree.Resolved, tree.Discarded)
	}
	return fmt.Sprintf(
		"o erro descartado só é julgado quando a chamada é uma função deste módulo (%s), porque um método de interface, uma função da biblioteca padrão e uma função guardada numa variável não se resolvem sem type-checker; um tipo nomeado que responde `error` sem ser o `error` da linguagem também não é resolvido; as aquisições julgadas são arquivo, transação, resposta HTTP, conexão e consulta com contexto, e as demais não entram no vocabulário; o dono de uma goroutine é lido por sintaxe — contexto, canal, WaitGroup, valor de tipo que sabe parar ou função que fecha o que recebe —, não por fluxo; e da mensagem pública só o campo `Message` e o `Code` de um erro de domínio são tratados como texto curado — um campo público com outro nome não é reconhecido",
		coverage)
}
