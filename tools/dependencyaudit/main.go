// Command dependencyaudit is the gate of P23-T09: every dependency, action and
// image the tree carries is approved with an owner, a purpose, a scope and a
// license, and the approval is a version and not a name.
//
// The other gates of this phase judge code. This one judges what the code is
// made of: the modules the build requires — the direct ones and the transitive
// ones nobody chose — the packages the frontend installs, the images the
// container files name, the actions the workflows run and the tools the
// Makefile demands. The inventory is versioned at `quality/dependencies.json`
// together with the policy it is judged by: the classes with the licenses each
// one homologates, the pin each one demands, and the vocabulary the project bans
// by name. The bill of materials at `quality/sbom.json` is **derived** from the
// same census, so a document that stopped matching the tree is a refusal.
//
// Thirteen rules, each one decidable from the tree, the register and the
// document that states the licenses:
//
//   - unregistered-component — the component no entry approves, which covers the
//     new transitive package and the version that moved without the register;
//   - unknown-component — the entry the tree no longer declares, because
//     removing a dependency is updating its evidence;
//   - incomplete-entry — the approval without class, owner, purpose, scope,
//     license or version evidence;
//   - unknown-statement — the catalogue row that survives the dependency it
//     describes;
//   - incompatible-license — the license the class does not homologate;
//   - unpinned-version — the range where the class demands an exact pin;
//   - unpinned-image — the image without a digest in a file that runs in
//     production;
//   - unpinned-action — the workflow action behind a tag instead of a commit;
//   - unused-module — the direct requirement nothing imports;
//   - duplicate-pin — the component pinned at two versions;
//   - banned-component — the name the policy bans;
//   - browser-runtime — the runtime package and the specifier that leaves the
//     tree, read from the manifest and from the source;
//   - sbom-drift — the bill of materials that disagrees with the tree.
//
// Two things are worth stating where the numbers are read:
//
//   - the gate never writes. `-print-register` prints the inventory the tree
//     declares, with the class, the owner, the purpose and the license of the
//     entries that already exist and empty for the component nobody approved —
//     exactly the fields the next run refuses — and `-print-sbom` prints the
//     bill of materials. An approval is a human decision and the printed
//     document is what a human commits;
//   - the tools the tree requires by name and does not pin are measured and
//     printed: their version is chosen by the machine that runs the target, and
//     the target refuses the absence but not the version.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// fixtureRoot is where the proofs of this gate live: the directory the tree walk
// of the other gates refuses to enter, which is why the proof of every rule asks
// for it by name.
const fixtureRoot = "tools/dependencyaudit/testdata"

// family is one rule with the fixture that proves it still bites.
type family = auditkit.Family

// rules is the vocabulary of this gate, printed by every run so that the report
// can be read without the source.
var rules = []auditkit.Rule{
	{Name: RuleUnregistered, Reason: "o componente que nenhuma entrada aprova é a dependência que ninguém triou, e a versão que se moveu sem o registro é a mesma falha vista de dentro do manifesto"},
	{Name: RuleUnknown, Reason: "a aprovação que sobrevive ao componente descreve uma árvore que não existe, e remover uma dependência é atualizar a evidência dela"},
	{Name: RuleIncomplete, Reason: "a entrada sem dono, finalidade, alcance, classe ou licença é um nome, e uma lista de nomes ninguém consegue triar"},
	{Name: RuleUnknownStatement, Reason: "a linha do catálogo que sobrevive à dependência descreve um projeto que não existe, e o catálogo é a prova de que a licença foi lida"},
	{Name: RuleIncompatible, Reason: "a licença é compatível com uma classe de uso e não com um projeto: a reciprocidade aceitável para uma ferramenta executada fora é a que não pode entrar no binário"},
	{Name: RuleUnpinned, Reason: "a faixa segue o fornecedor, e o build que segue o fornecedor não é o build que alguém reproduziu"},
	{Name: RuleUnpinnedImage, Reason: "a tag é um rótulo que alguém pode mover, e o arquivo que roda em produção tem de pinar os bytes"},
	{Name: RuleUnpinnedAction, Reason: "a action presa a uma tag é a action que roda em cada pull request e obedece a quem move a tag"},
	{Name: RuleUnusedModule, Reason: "o módulo que ninguém importa é a dependência que só o manifesto carrega, e a lista de exceções é onde ela se esconde"},
	{Name: RuleDuplicatePin, Reason: "o pino escrito duas vezes é o pino em que uma das duas mentiu, e quem lê não sabe qual das duas a árvore usa"},
	{Name: RuleBanned, Reason: "o nome que a política bane é a decisão que o projeto tomou, e a decisão que se contorna escrevendo o nome no outro documento não é decisão"},
	{Name: RuleBrowserRuntime, Reason: "o navegador deste projeto não executa nenhuma biblioteca de terceiros, e o import que sai da árvore e o pacote de runtime do manifesto são a mesma decisão lida em dois arquivos"},
	{Name: RuleSBOMDrift, Reason: "a lista de materiais é derivada, então o documento que discorda da árvore descreve uma entrega que ninguém tem"},
}

// families is the executable map of the rules: every rule with the fixture that
// proves it still bites.
var families = []family{
	{Name: RuleUnregistered, Target: "unregistered", Reason: "o módulo que a árvore passou a exigir e o registro não aprova"},
	{Name: RuleUnknown, Target: "unknown", Reason: "a aprovação do módulo que a árvore não exige mais"},
	{Name: RuleIncomplete, Target: "incomplete", Reason: "a aprovação sem dono, finalidade e alcance"},
	{Name: RuleUnknownStatement, Target: "statement", Reason: "a linha do catálogo de licenças que não corresponde a componente nenhum"},
	{Name: RuleIncompatible, Target: "license", Reason: "a licença copyleft forte numa classe de runtime"},
	{Name: RuleUnpinned, Target: "unpinned", Reason: "o manifesto que declara uma faixa em vez de uma versão"},
	{Name: RuleUnpinnedImage, Target: "image", Reason: "a imagem de produção sem digest"},
	{Name: RuleUnpinnedAction, Target: "action", Reason: "a action presa a uma tag em vez de a um commit"},
	{Name: RuleUnusedModule, Target: "unused", Reason: "a exigência direta que nenhum arquivo importa"},
	{Name: RuleDuplicatePin, Target: "duplicate", Reason: "o componente aprovado em duas versões ao mesmo tempo"},
	{Name: RuleBanned, Target: "banned", Reason: "o pacote de framework que a política bane por nome"},
	{Name: RuleBrowserRuntime, Target: "browser", Reason: "a fonte do browser que importa um specifier que sai da árvore"},
	{Name: RuleSBOMDrift, Target: "sbom", Reason: "a lista de materiais que perdeu uma linha que a árvore ainda produz"},
}

// cleanFixtures are the fixtures that must NOT be refused: the legal shape next
// to every rule. One tree is enough because the rules are judged over the same
// census, and a clean neighbour that satisfied only one of them would be a
// neighbour of that rule and not of the gate.
var cleanFixtures = []family{
	{Name: RuleUnregistered, Target: "clean", Reason: "todo componente da árvore aprovado com dono, finalidade, alcance e licença"},
	{Name: RuleUnknown, Target: "clean", Reason: "nenhuma aprovação sem componente"},
	{Name: RuleIncomplete, Target: "clean", Reason: "cada entrada completa e cada licença declarada onde a árvore a declara"},
	{Name: RuleUnknownStatement, Target: "clean", Reason: "cada linha do catálogo com o componente que ela descreve"},
	{Name: RuleIncompatible, Target: "clean", Reason: "as licenças dentro do que a classe de cada componente homologa"},
	{Name: RuleUnpinned, Target: "clean", Reason: "versões exatas no manifesto"},
	{Name: RuleUnpinnedImage, Target: "clean", Reason: "a imagem de produção com digest"},
	{Name: RuleUnpinnedAction, Target: "clean", Reason: "a action presa a um commit"},
	{Name: RuleUnusedModule, Target: "clean", Reason: "a exigência direta que um arquivo importa"},
	{Name: RuleDuplicatePin, Target: "clean", Reason: "um pino por componente"},
	{Name: RuleBanned, Target: "clean", Reason: "nenhum nome banido na árvore nem no registro"},
	{Name: RuleBrowserRuntime, Target: "clean", Reason: "nenhum runtime de terceiros e nenhum specifier que sai da árvore"},
	{Name: RuleSBOMDrift, Target: "clean", Reason: "a lista de materiais igual ao que a árvore declara"},
}

func main() {
	root := flag.String("root", ".", "the repository root")
	registerFile := flag.String("register", registerPath, "the register to read")
	printRegisterDocument := flag.Bool("print-register", false, "print the register the tree declares and stop")
	printSBOMDocument := flag.Bool("print-sbom", false, "print the bill of materials of the tree and stop")
	flag.Parse()
	if err := run(*root, *registerFile, *printRegisterDocument, *printSBOMDocument); err != nil {
		fmt.Fprintf(os.Stderr, "dependencyaudit: %v\n", err)
		os.Exit(1)
	}
}

func run(root, registerFile string, printRegisterDocument, printSBOMDocument bool) error {
	document, err := readRegister(root, registerFile)
	if err != nil {
		return err
	}
	if printRegisterDocument {
		return printRegister(root, document)
	}
	if printSBOMDocument {
		return printSBOM(root, document)
	}
	if err := proveFamilies(root); err != nil {
		return err
	}
	measured, err := readCensus(root, document)
	if err != nil {
		return err
	}
	if measured.Counts.Modules+measured.Counts.Packages+measured.Counts.Images+measured.Counts.Actions == 0 {
		return fmt.Errorf("o censo não leu componente nenhum em %d arquivo(s) de manifesto, imagem e workflow: corpus vazio é a forma de um portão que parou de funcionar", measured.Counts.Manifests)
	}
	findings, counts, err := judge(root, document, measured)
	if err != nil {
		return err
	}
	for _, finding := range findings {
		if !auditkit.RuleKnown(rules, finding.Rule) {
			return fmt.Errorf("o portão produziu um achado que ele não sabe classificar: %s", finding.String())
		}
	}
	report(measured, counts, len(findings), document.Statement)
	if len(findings) > 0 {
		lines := make([]string, 0, len(findings))
		for _, finding := range findings {
			lines = append(lines, finding.String())
		}
		return fmt.Errorf("o portão de dependências recusou a árvore:\n  - %s", strings.Join(lines, "\n  - "))
	}
	fmt.Printf("dependencyaudit: OK — toda dependência, action e imagem da árvore está aprovada com dono, finalidade, alcance e licença, e cada família segue recusando a própria fixture\n")
	return nil
}

// proveFamilies runs every fixture and requires its rule to refuse it, and then
// runs the clean fixture and requires silence. The fixture is a tree document
// judged by the same code path that judges the repository: what the fixture
// supplies is the tree, and the parsers read the materialized files.
func proveFamilies(root string) error {
	scan := func(_ family, directory string) (auditkit.ScanResult, error) {
		document, err := readFixture(filepath.Join(directory, "tree.json"))
		if err != nil {
			return auditkit.ScanResult{}, err
		}
		// The fixture is judged against a register the gate accepted: a proof
		// against a document the loader would have refused proves nothing about
		// the rules.
		if err := validateRegister(directory+"/tree.json", document.Register); err != nil {
			return auditkit.ScanResult{}, err
		}
		fixtureRoot, cleanup, err := materialize(document)
		if err != nil {
			return auditkit.ScanResult{}, err
		}
		defer cleanup()
		measured, err := readCensus(fixtureRoot, document.Register)
		if err != nil {
			return auditkit.ScanResult{}, err
		}
		findings, _, err := judge(fixtureRoot, document.Register, measured)
		return auditkit.ScanResult{Findings: findings}, err
	}
	if err := auditkit.ProveRefused(fixtureRoot, families, scan); err != nil {
		return err
	}
	return auditkit.ProveAccepted(fixtureRoot, cleanFixtures, scan)
}

// report prints everything this run measured: the census, the approvals that
// held, the pins that were pins, the rules and the gaps the gate leaves.
func report(measured census, counts judged, findings int, statement string) {
	fmt.Printf("dependencyaudit: censo — %d módulo(s) (%d direto(s), %d indireto(s)), %d pacote(s) em %d manifesto(s) (dos quais %d no manifesto do browser), %d referência(s) de imagem (das quais %d em produção e %d com digest), %d action(s) (%d presas a commit) e %d sítio(s) de versão de ferramenta\n",
		measured.Counts.Modules, measured.Counts.Direct, measured.Counts.Indirect,
		measured.Counts.Packages, measured.Counts.Manifests, measured.Counts.BrowserManifests,
		measured.Counts.Images, measured.Counts.ProductionImages, measured.Counts.Digests,
		measured.Counts.Actions, measured.Counts.CommitPins, measured.Counts.EvidenceSites)
	fmt.Printf("dependencyaudit: registro — %d entrada(s), das quais %d plataforma(s), %d componente(s) aprovado(s), %d licença(s) conferida contra `%s` (%d linha(s)) e %d componente(s) na lista de materiais\n",
		measured.Counts.Entries, measured.Counts.Platforms, counts.Approved, counts.StatementHeld,
		statement, measured.Counts.StatementRows, counts.SBOMComponents)
	fmt.Printf("dependencyaudit: medido — %d import(s) nas fontes do browser (dos quais %d sai da árvore), %d pino(s) exato(s) conferido(s), %d exigência(s) direta(s) sem import, %d nome(s) banido(s) e %d demanda(s) de evidência avaliada(s)\n",
		measured.Counts.Imports, measured.Counts.BareImports, counts.Pinned, counts.UnusedModules,
		measured.Counts.Banned, counts.Judgments)
	fmt.Printf("dependencyaudit: %d achado(s)\n", findings)
	fmt.Printf("dependencyaudit: %d pino(s) de digest e %d pino(s) de commit conferido(s)\n", counts.DigestPins, counts.CommitPins)
	for _, entry := range rules {
		fmt.Printf("dependencyaudit: regra %s — %s\n", entry.Name, entry.Reason)
	}
	for _, entry := range families {
		fmt.Printf("dependencyaudit: família %s — recusa a fixture %s/%s (%s)\n", entry.Name, fixtureRoot, entry.Target, entry.Reason)
	}
	for _, entry := range cleanFixtures {
		fmt.Printf("dependencyaudit: família %s — aceita a fixture %s/%s (%s)\n", entry.Name, fixtureRoot, entry.Target, entry.Reason)
	}
	fmt.Printf("dependencyaudit: não julgado por este portão — %s\n", unenforced())
}

// sortStrings orders a slice in place; it exists so the manifest the census
// reads produces the same component order on two machines.
func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for inner := index; inner > 0 && values[inner] < values[inner-1]; inner-- {
			values[inner], values[inner-1] = values[inner-1], values[inner]
		}
	}
}

// unenforced states the gaps this gate declares, with the measurement in hand.
func unenforced() string {
	return "o censo lê os manifestos versionados e não a resolução de versões: o módulo que o `go.mod` exige indiretamente é julgado como o `go.mod` o escreve, e o pacote que o npm instala abaixo do lockfile não é componente deste portão, porque a árvore de resolução é do npm e não do repositório — o que se aprova aqui é o que se escreve; a licença é conferida contra o documento que a árvore declara (`docs/DEPENDENCIES.md`) e não contra o arquivo de licença dentro do módulo, que vive no cache de uma máquina e não no repositório, então este portão prova que a licença está **declarada com dono** e não que o texto dela foi lido; as ferramentas que a árvore exige pelo nome e não pina — k6 e trivy hoje — são medidas e impressas, e a versão delas é escolhida por quem roda o alvo: o alvo recusa a ausência e não a versão; a imagem declarada por variável de ambiente (`${COMPOSE_ARENA_IMAGE}`) não é componente, porque o digest chega no deploy e não no repositório; e o SCIP/SPDX formal não é gerado: o documento é a lista de materiais desta árvore, com os campos que este portão julga, e a conversão para um formato de terceiros seria uma dependência nova para carregar os mesmos bytes"
}
