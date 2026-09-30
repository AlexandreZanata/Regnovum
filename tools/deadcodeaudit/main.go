// Command deadcodeaudit is the gate of P23-T04: dead code, silent placeholders
// and the paths that cannot happen.
//
// It refuses, naming the rule:
//
//   - a panic whose message is the placeholder vocabulary — "not implemented",
//     "stub", "por enquanto", an upper-case TODO. The cheapest line to write is
//     the one that says the work is not there, and it is the one a reader
//     believes;
//   - a function that announces itself as unfinished and whose body is a single
//     success return: the caller follows as if it had worked;
//   - a deferral without a reference. A marker at the head of a comment has to
//     name the task of the plan (Pnn-Tnn) or the issue (#nn) that resolves it,
//     and the ones that do are printed by every run — a silence nobody sees is a
//     silence nobody revisits;
//   - a statement after an unconditional terminator in the same block, and a
//     branch decided by the literal `true` or `false`;
//   - configuration that lies in any of its five directions: a key the accepted
//     set holds and nothing reads, a key the template documents and the loader
//     refuses, a key the loader accepts and nobody documents, a key the loader
//     reads that its own rejection loop has already dropped, and a literal
//     ARENA_* key read straight from the environment outside the configuration
//     package, where the variable can never arrive;
//   - a Go file the gate cannot read: a file nobody measures is the hole a gate
//     exists to prevent.
//
// Three things about the measurement are worth stating where the numbers are
// read:
//
//   - generated code is excluded by **provenance**, never by directory. The
//     marker of the Go toolchain ("Code generated ... DO NOT EDIT.") is read
//     where the toolchain reads it: in the comments before the package clause.
//     The generators of this repository carry that text inside a string, and a
//     gate that scanned the bytes would exclude the very tools that must be
//     judged while looking like it had done the right thing;
//   - the fixtures under testdata/ are exercised on purpose. The Go toolchain
//     skips testdata in every ./... pattern, so the proof that each rule still
//     bites has to ask for the fixture by name — the same shape the gates of
//     P23-T02 and P23-T03 use, for the same reason;
//   - what this gate does **not** enforce is printed with the tree in hand
//     instead of being left to the reader: an exported declaration no other
//     package of this tree mentions, and an environment read whose key comes
//     from a variable. Both are stated with their measurement.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// Where the configuration surface lives. The three paths are named here, once,
// because they are the subject of the rule and not an accident of the walk.
const (
	configurationDirectory = "internal/platform/config"
	configurationTemplate  = ".env.example"
)

// readRoots are the trees where a direct read of the process environment is
// judged: the composition root and everything it wires.
var readRoots = []string{"cmd", "internal"}

// fixtureRoot is where the fixtures of this gate live. It is a constant because
// two callers asking for "the fixture" have to be asking for the same directory,
// and because the proof of every rule is a directory the tree walk refuses to
// enter: the Go toolchain skips testdata in every ./... pattern.
const fixtureRoot = "tools/deadcodeaudit/testdata"

// family is one rule with the fixture that proves it still bites, in the shape
// every gate of the phase declares: the rule, the fixture under fixtureRoot and
// why the family is part of the gate.
type family = auditkit.Family

// families is the executable map of the rules. Every family has a fixture that
// must be refused: a rule that stopped biting has to fail here, by name, instead
// of disappearing from a green run. The configuration family is asked for
// through the same function the delivered tree goes through, with the fixture's
// own registry, template and roots.
var families = []family{
	{
		Name: RulePlaceholderPanic, Target: "placeholder",
		Reason: "a mensagem que diz que o trabalho não foi feito é o que sobra quando ele não é feito",
	},
	{
		Name: RuleFalseSuccess, Target: "falsesuccess",
		Reason: "a função que se anuncia como não pronta e devolve sucesso faz o chamador seguir como se tivesse funcionado",
	},
	{
		Name: RuleDeferred, Target: "deferred",
		Reason: "o adiamento sem dono é uma frase que ninguém cobra",
	},
	{
		Name: RuleUnreachable, Target: "unreachable",
		Reason: "o código depois do término nunca roda e o leitor paga por ele em toda revisão",
	},
	{
		Name: RuleConstantBranch, Target: "constantbranch",
		Reason: "o desvio sobre literal já está decidido e ficou escrito como se não estivesse",
	},
	{
		Name: RuleConfigurationKey, Target: "configuration",
		Reason: "a variável que o operador define e o processo ignora é configuração que mente",
	},
}

// cleanFixtures are the fixtures that must NOT be refused. A rule proved only in
// the direction that refuses is a rule that could be refusing everything while
// looking strict, so every rule that has a legal shape next to it carries the
// legal shape here, under testdata/clean/.
var cleanFixtures = []family{
	{
		Name: RulePlaceholderPanic, Target: "clean/placeholder",
		Reason: "o panic que diz a verdade sobre o que o programa não serve não é um placeholder",
	},
	{
		Name: RuleFalseSuccess, Target: "clean/falsesuccess",
		Reason: "a função inacabada que devolve erro está recusando o trabalho, como deve",
	},
	{
		Name: RuleDeferred, Target: "clean/deferred",
		Reason: "o comentário que discute o marcador não é um adiamento",
	},
	{
		Name: RuleUnreachable, Target: "clean/unreachable",
		Reason: "o rótulo é alvo de salto: o que vem depois dele roda",
	},
	{
		Name: RuleConstantBranch, Target: "clean/constantbranch",
		Reason: "a condição que é um valor é uma decisão que alguém ainda pode tomar",
	},
}

// provenanceProbe is one proof about the exclusion itself, in both directions:
// the marked file has to be excluded, and the file that only mentions the marker
// has to be judged. A provenance rule proved in one direction only is a rule that
// could be excluding the wrong files while looking correct.
type provenanceProbe = auditkit.ProvenanceProbe

var provenanceProbes = []provenanceProbe{
	{Name: "um arquivo gerado é excluído pelo marcador", Target: "generated", Excluded: true},
	{Name: "um arquivo que só menciona o marcador é julgado", Target: "decoy", Excluded: false},
}

func main() {
	root := flag.String("root", ".", "the repository root")
	flag.Parse()
	if err := run(*root); err != nil {
		fmt.Fprintf(os.Stderr, "deadcodeaudit: %v\n", err)
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
	if tree.Files == 0 {
		return fmt.Errorf("the walk judged no file: a gate that measured nothing is a gate that refuses nothing")
	}
	if len(tree.Generated) == 0 {
		return fmt.Errorf("no file was excluded by provenance: the marker this gate reads is the Go toolchain's, and a tree with no generated file means the marker stopped matching rather than that the generator stopped running")
	}
	if len(tree.Unreadable) > 0 {
		paths := []string{}
		for path := range tree.Unreadable {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		return fmt.Errorf("the gate cannot read %s: a file whose silence nobody can vouch for is a hole", strings.Join(paths, ", "))
	}

	configuration, err := configurationFindings(configurationDirectory, configurationTemplate, readRoots)
	if err != nil {
		return err
	}
	tree.Findings = append(tree.Findings, configuration.comparison...)
	auditkit.SortFindings(tree.Findings)

	// The report has no bucket called "other": a finding whose rule is not the
	// table is a programming error, and it is refused as one instead of being
	// printed as an unclassified line.
	unclassified := []string{}
	for _, entry := range append(append([]finding{}, tree.Findings...), tree.Accepted...) {
		if !ruleKnown(entry.Rule) {
			unclassified = append(unclassified, entry.String())
		}
	}
	if len(unclassified) > 0 {
		return fmt.Errorf("the gate produced a finding it cannot classify:\n  - %s", strings.Join(unclassified, "\n  - "))
	}

	report(tree, configuration)
	if len(tree.Findings) > 0 {
		lines := make([]string, 0, len(tree.Findings))
		for _, entry := range tree.Findings {
			lines = append(lines, entry.String())
		}
		return fmt.Errorf("the dead code gate refused the tree:\n  - %s", strings.Join(lines, "\n  - "))
	}
	fmt.Printf("deadcodeaudit: OK — nenhum achado, e cada família segue recusando a própria fixture\n")
	return nil
}

// proveFamilies runs every fixture and requires its rule to refuse it, requires
// the clean fixtures to be silent and holds the two directions of the provenance
// exclusion. The root is a parameter because the proof of the rules and the tree
// the gate judges are asked for from two working directories; the configuration
// family goes through the same entry point the delivered tree does, with the
// fixture's own registry, template and read roots.
func proveFamilies(root string) error {
	if err := auditkit.ProveRefused(root, families, readFixture); err != nil {
		return err
	}
	if err := auditkit.ProveAccepted(root, cleanFixtures, readFixture); err != nil {
		return err
	}
	return auditkit.ProveProvenance(root, provenanceProbes, readFixture)
}

// readFixture reads one fixture of this gate. Every family goes through the tree
// scan except the configuration family, whose subject is a registry, a template
// and the roots where a direct read of the environment is judged: it is asked for
// through the function the delivered tree goes through, so the proof and the
// delivery read the same code.
func readFixture(entry family, directory string) (auditkit.ScanResult, error) {
	if entry.Name == RuleConfigurationKey {
		measured, err := configurationFindings(
			directory+"/registry", directory+"/env.example",
			[]string{directory + "/outside", directory + "/inside"})
		if err != nil {
			return auditkit.ScanResult{}, fmt.Errorf("a família %q não pôde ser provada: %w", entry.Name, err)
		}
		return auditkit.ScanResult{Findings: measured.comparison}, nil
	}
	measured, err := scanDirectory(directory)
	if err != nil {
		return auditkit.ScanResult{}, err
	}
	return auditkit.ScanResult{Generated: measured.Generated, Findings: measured.Findings}, nil
}

// report prints everything this run measured: the corpus, the rules, what the
// configuration surface holds, the deferrals it accepted and the gaps it
// declares. A number that is not printed is a number nobody reviews.
func report(tree measured, configuration configuration) {
	fmt.Printf("deadcodeaudit: julgou %d arquivo(s), %d função(ões) e %d comentário(s); %d arquivo(s) excluído(s) por proveniência (o marcador do Go)\n",
		tree.Files, tree.Functions, tree.Comments, len(tree.Generated))
	for _, entry := range rules {
		fmt.Printf("deadcodeaudit: regra %s — %s\n", entry.Name, entry.Reason)
	}
	for _, entry := range families {
		fmt.Printf("deadcodeaudit: família %s — recusa a fixture %s/%s\n", entry.Name, fixtureRoot, entry.Target)
	}
	for _, entry := range cleanFixtures {
		fmt.Printf("deadcodeaudit: família %s — aceita a fixture %s/%s (%s)\n", entry.Name, fixtureRoot, entry.Target, entry.Reason)
	}
	for _, probe := range provenanceProbes {
		fmt.Printf("deadcodeaudit: proveniência — %s (%s/%s)\n", probe.Name, fixtureRoot, probe.Target)
	}
	fmt.Printf("deadcodeaudit: configuração — %d chave(s) aceita(s) por Load, %d lida(s) e %d documentada(s) em %s\n",
		configuration.accepted, configuration.read, configuration.documented, configurationTemplate)
	for _, entry := range tree.Accepted {
		fmt.Printf("deadcodeaudit: adiamento aceito %s — %s\n", entry.Path, entry.Detail)
	}
	fmt.Printf("deadcodeaudit: %d achado(s)\n", len(tree.Findings))
	fmt.Printf("deadcodeaudit: não julgado por este portão — %s\n", unenforced(tree.Exported, tree.Unconsumed))
}

// unenforced states the two gaps with the tree in hand. The first one is the
// reason the scope of this task cannot be read literally: "unused export" is not
// a question a syntax tree can answer here, because the consumers of this
// repository's exports are the packages that import it, and the packages that
// import it are not the whole contract. The number of declarations no other
// package mentions is printed with a sample, so the reader sees what a rule
// would have to be willing to refuse.
func unenforced(exported int, unconsumed []string) string {
	sample := unconsumed
	if len(sample) > 8 {
		sample = sample[:8]
	}
	return fmt.Sprintf(
		"export sem consumo não é decidível por sintaxe: %d de %d declaração(ões) exportada(s) não são mencionadas por nenhum outro pacote desta árvore (amostra: %s) — o consumidor de um export está fora da árvore, e uma regra aqui recusaria a superfície pública inteira; leitura indireta do ambiente (os.Getenv com uma variável) também não é resolvida, e só a chave literal é julgada",
		len(unconsumed), exported, strings.Join(sample, ", "))
}
