// Command diffaudit is the gate of P23-T08: a production change owes evidence,
// and the evidence is decided by what the change touches and not by what the
// author says about it.
//
// The other gates of this phase judge the tree. This one judges the **change** —
// the commits a branch brings over the branch it targets — because the questions
// the phase asks are questions about a diff: "this migration arrived without an
// upgrade test", "these routes changed and the contract did not", "a critical
// rule moved in the table without the regression that proves it". The gate reads
// the range from git, classifies every file each commit touches with the policy
// versioned at `quality/diff-policy.json`, and demands the evidence the policy
// attaches to the class.
//
// Eight rules, each one decidable from the change, the policy and the two
// registers the repository already versions:
//
//   - unclassified-change — the file no class claims, which is the refusal that
//     protects the others;
//   - production-without-test — the new production file with no test of the same
//     area in the same change;
//   - migration-without-upgrade-test — the migration with no upgrade proof;
//   - route-without-contract — the change of the route set without the published
//     contract and the generated client;
//   - provenance-without-pair — the generated artifact whose generator input is
//     not in the change, and the input whose artifact was not regenerated;
//   - declared-test-missing — the evidence a rule of the catalog declares and the
//     tree no longer holds;
//   - q0-without-regression — the critical rule written or moved without the
//     nominal and the adversarial regression it promises;
//   - bypass-marker — the commit message that announces the evidence was
//     skipped.
//
// Three things are worth stating where the numbers are read:
//
//   - the demand for a test is about the file **added** to a production area. An
//     edit to an existing file is not, by itself, a demand: the gate measures how
//     many edited production files came with a test of the area and how many did
//     not, and prints both, because that boundary is the design decision of this
//     task and a boundary without a number is a boundary nobody reviews;
//   - the base of the range is the merge base with `origin/main` (or `main`) and
//     a base that cannot be resolved is a refusal: a gate that cannot see the
//     diff must not answer green over it. An empty range is accepted only when
//     base and head are the same commit, and the run prints that it judged
//     nothing and why;
//   - the gate never writes to git and never edits the tree. It reads `show`,
//     `diff`, `rev-list` and `merge-base`, and `-print-document` prints the
//     document it judged so that a refusal can be argued with.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// policySchemaVersion is the only version of the policy document this gate
// reads.
const policySchemaVersion = 1

// fixtureRoot is where the fixtures of this gate live: the directory the tree
// walk of the other gates refuses to enter, which is why the proof of every rule
// asks for it by name.
const fixtureRoot = "tools/diffaudit/testdata"

// family is one rule with the fixture that proves it still bites.
type family = auditkit.Family

// rules is the vocabulary of this gate, printed by every run so that the report
// can be read without the source. A rule that is not here cannot be produced: the
// judgments name their findings from this table, and a name outside it is a
// programming error the gate turns into a refusal instead of a line.
var rules = []auditkit.Rule{
	{Name: RuleUnclassified, Reason: "a política que não classifica um arquivo é a política cuja evidência ninguém consegue exigir, porque o silêncio não tem classe"},
	{Name: RuleProductionWithoutTest, Reason: "o arquivo novo de produção sem teste da mesma área é comportamento novo que ninguém prova"},
	{Name: RuleMigrationWithoutUpgradeTest, Reason: "a migration sem a prova de atualização é a mudança que um `git revert` não desfaz limpo e cujo esquema novo ninguém leu antes do commit"},
	{Name: RuleRouteWithoutContract, Reason: "a rota que muda sem o contrato publicado é a outra vista da mesma lista que ficou para trás, e o cliente gerado mente sobre o que o servidor aceita"},
	{Name: RuleProvenanceWithoutPair, Reason: "o artefato gerado sem o insumo é a consequência sem a causa, e o insumo sem o artefato é a regeneração que ficou para depois"},
	{Name: RuleDeclaredTestMissing, Reason: "a evidência que o catálogo declara e a árvore não tem é a regra que parece coberta e prova nada"},
	{Name: RuleQ0WithoutRegression, Reason: "a regra crítica que muda sem a regressão nominal e adversarial é a promessa sem o par que a prova: o caminho que funciona e o caminho que recusa"},
	{Name: RuleBypassMarker, Reason: "a mensagem de commit que anuncia a evidência dispensada é o único bypass que não deixa rastro na árvore, e o programa o proíbe por nome"},
}

// families is the executable map of the rules: every rule with the fixture that
// proves it still bites. A rule proved only in the direction that refuses would
// be a rule that could be refusing everything while looking strict, so every one
// of them also has a clean neighbour in cleanFixtures below.
var families = []family{
	{Name: RuleUnclassified, Target: "unclassified", Reason: "o arquivo que nenhuma classe da política casa é recusado em vez de passar em silêncio"},
	{Name: RuleProductionWithoutTest, Target: "notest", Reason: "o arquivo novo de produção sem teste na mesma mudança é o comportamento novo sem prova"},
	{Name: RuleMigrationWithoutUpgradeTest, Target: "migration", Reason: "a migration sem prova de atualização é a mudança irreversível sem o esquema lido antes"},
	{Name: RuleRouteWithoutContract, Target: "route", Reason: "a rota nova sem o contrato e o cliente gerado é a lista de rotas com duas vistas que discordam"},
	{Name: RuleProvenanceWithoutPair, Target: "generated", Reason: "o artefato gerado sem o insumo é a consequência sem a causa"},
	{Name: RuleDeclaredTestMissing, Target: "declared", Reason: "a referência do catálogo que não resolve é a evidência que sumiu sem ninguém ver"},
	{Name: RuleQ0WithoutRegression, Target: "q0", Reason: "a regra crítica que mudou sem o par nominal e adversarial é a regra que ninguém prova"},
	{Name: RuleBypassMarker, Target: "bypass", Reason: "a mensagem que anuncia a dispensa é o bypass que a fase proíbe por nome"},
}

// cleanFixtures are the fixtures that must NOT be refused: the neighbour of each
// rule, which is the shape the tree is allowed to have.
var cleanFixtures = []family{
	{Name: RuleUnclassified, Target: "clean", Reason: "a mudança de documento e de configuração, que a política isenta por classe"},
	{Name: RuleProductionWithoutTest, Target: "clean/notest", Reason: "o arquivo novo de produção com o teste da mesma área na mesma mudança"},
	{Name: RuleMigrationWithoutUpgradeTest, Target: "clean/migration", Reason: "a migration com a prova de atualização na área que a política nomeia"},
	{Name: RuleRouteWithoutContract, Target: "clean/route", Reason: "a rota nova com o contrato e o cliente gerado na mesma mudança, e o arquivo de rota cujo conjunto não mudou"},
	{Name: RuleProvenanceWithoutPair, Target: "clean/generated", Reason: "o artefato gerado com o insumo que o produz na mesma mudança"},
	{Name: RuleDeclaredTestMissing, Target: "clean/declared", Reason: "a referência do catálogo que resolve na árvore entregue"},
	{Name: RuleQ0WithoutRegression, Target: "clean/q0", Reason: "a regra crítica com o par nominal e adversarial na mesma mudança"},
	{Name: RuleBypassMarker, Target: "clean/bypass", Reason: "a mensagem que descreve o que mudou sem anunciar dispensa nenhuma"},
}

func main() {
	root := flag.String("root", ".", "the repository root")
	base := flag.String("base", "", "the base of the range; the merge base with origin/main and then main when empty")
	head := flag.String("head", "HEAD", "the head of the range")
	document := flag.String("change", "", "read the change document from a file instead of git")
	policyFile := flag.String("policy", policyPath, "the policy register to read")
	printDocument := flag.Bool("print-document", false, "print the change document the gate would judge and stop")
	flag.Parse()
	if err := run(*root, *base, *head, *document, *policyFile, *printDocument); err != nil {
		fmt.Fprintf(os.Stderr, "diffaudit: %v\n", err)
		os.Exit(1)
	}
}

func run(root, base, head, documentPath, policyFile string, printDocument bool) error {
	pol, err := readPolicy(root, policyFile)
	if err != nil {
		return err
	}
	register, err := readProvenance(root)
	if err != nil {
		return err
	}

	var document changeDocument
	if documentPath != "" {
		document, err = readChange(documentPath)
	} else {
		document, err = buildChange(root, base, head)
	}
	if err != nil {
		return err
	}
	if printDocument {
		raw, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return err
		}
		fmt.Printf("%s\n", raw)
		return nil
	}
	if len(document.Commits) == 0 {
		if document.Head == "" || document.Base == document.Head {
			if err := proveFamilies(root); err != nil {
				return err
			}
			fmt.Printf("diffaudit: a faixa %s..%s está vazia (base e cabeça são o mesmo commit): não há mudança a julgar, e este portão diz isso em vez de responder sobre nada\n",
				shortSHA(document.Base), shortSHA(document.Head))
			fmt.Printf("diffaudit: não julgado por este portão — %s\n", unenforced())
			return nil
		}
		return fmt.Errorf("a faixa %s..%s não trouxe commit nenhum e a base não é a cabeça: uma faixa vazia entre commits distintos é a forma de um construtor que parou de funcionar, e responder verde aqui seria responder sobre nada",
			shortSHA(document.Base), shortSHA(document.Head))
	}

	st, err := newState(root, document, pol, register)
	if err != nil {
		return err
	}
	findings, result, err := judge(document, st)
	if err != nil {
		return err
	}
	if result.Classified == 0 && result.Commits > result.Merges {
		return fmt.Errorf("o portão classificou zero arquivo em %d commit(s) que não são merge: corpus vazio é a forma de um portão que parou de funcionar", result.Commits-result.Merges)
	}
	unclassified := []string{}
	for _, finding := range findings {
		if !auditkit.RuleKnown(rules, finding.Rule) {
			unclassified = append(unclassified, finding.String())
		}
	}
	if len(unclassified) > 0 {
		return fmt.Errorf("o portão produziu um achado que ele não sabe classificar:\n  - %s", strings.Join(unclassified, "\n  - "))
	}

	// The fixtures are proved **after** the range is judged, and the order is the
	// difference between two readings of the same defect: the clean fixtures are
	// judged against the delivered registers, so a catalog reference that stopped
	// resolving is both a finding of the range and a clean fixture that stopped
	// being clean. Judging first lets the repository be refused in its own name,
	// with the fixture proof alongside it, instead of the defect arriving as "a
	// fixture produced a finding and none is expected" — which is what a first run
	// of this gate reported. Neither error is swallowed: both travel.
	proofErr := proveFamilies(root)
	report(result, len(findings), document)
	var refusal error
	if len(findings) > 0 {
		lines := make([]string, 0, len(findings))
		for _, finding := range findings {
			lines = append(lines, finding.String())
		}
		refusal = fmt.Errorf("o portão de diff recusou a mudança:\n  - %s", strings.Join(lines, "\n  - "))
	}
	if err := errors.Join(proofErr, refusal); err != nil {
		return err
	}
	fmt.Printf("diffaudit: OK — nenhuma mudança sem a evidência que a política exige, e cada família segue recusando a própria fixture\n")
	return nil
}

// proveFamilies runs every fixture and requires its rule to refuse it, and then
// runs the clean fixtures and requires silence. The fixture is a change document
// judged by the same code path that judges the repository; what the fixture
// supplies is the diff, and the tree it is judged against is the delivered one.
func proveFamilies(root string) error {
	pol, err := readPolicy(root, policyPath)
	if err != nil {
		return err
	}
	register, err := readProvenance(root)
	if err != nil {
		return err
	}
	scan := func(entry family, directory string) (auditkit.ScanResult, error) {
		document, err := readChange(filepath.Join(directory, "change.json"))
		if err != nil {
			return auditkit.ScanResult{}, err
		}
		st, err := newState(root, document, pol, register)
		if err != nil {
			return auditkit.ScanResult{}, err
		}
		findings, _, err := judge(document, st)
		return auditkit.ScanResult{Findings: findings}, err
	}
	if err := auditkit.ProveRefused(fixtureRoot, families, scan); err != nil {
		return err
	}
	return auditkit.ProveAccepted(fixtureRoot, cleanFixtures, scan)
}

// report prints everything this run measured: the range, the classification, the
// populations behind the demand that is judged and the population of the one
// that is only measured, the rules and the gaps the gate leaves.
func report(result measured, findings int, document changeDocument) {
	fmt.Printf("diffaudit: julgou a faixa %s..%s — %d commit(s), dos quais %d merge (julgado apenas pelo texto), %d arquivo(s) classificado(s) e %d recusado(s) por classe\n",
		shortSHA(document.Base), shortSHA(document.Head), result.Commits, result.Merges, result.Classified, result.Unclassified)
	classes := make([]string, 0, len(result.Classes))
	for name := range result.Classes {
		classes = append(classes, name)
	}
	sortStrings(classes)
	for _, name := range classes {
		fmt.Printf("diffaudit: classe %s — %d arquivo(s)\n", name, result.Classes[name])
	}
	fmt.Printf("diffaudit: medido — %d arquivo(s) novo(s) de produção (dos quais %d com teste na mesma área), %d edição(ões) de arquivo de produção (das quais %d com teste na mesma área), %d arquivo(s) só de formato, %d mudança(s) de conjunto de rotas, %d referência(s) declarada(s) no catálogo resolvida(s), %d regra(s) Q0 na tabela (das quais %d mudou), %d mensagem(ns) lida(s)\n",
		result.AddedProduction, result.AddedProductionWithTest, result.EditedProduction, result.EditedProductionWithTest,
		result.FormatOnly, result.RouteChanges, result.DeclaredRefs, result.Q0Rules, result.Q0RulesChanged, result.BypassChecked)
	fmt.Printf("diffaudit: %d demanda(s) de evidência avaliada(s), %d achado(s)\n", result.Judgments, findings)
	for _, path := range result.FormatPaths {
		fmt.Printf("diffaudit: isento por formato — %s\n", path)
	}
	for _, entry := range rules {
		fmt.Printf("diffaudit: regra %s — %s\n", entry.Name, entry.Reason)
	}
	for _, entry := range families {
		fmt.Printf("diffaudit: família %s — recusa a fixture %s/%s\n", entry.Name, fixtureRoot, entry.Target)
	}
	for _, entry := range cleanFixtures {
		fmt.Printf("diffaudit: família %s — aceita a fixture %s/%s (%s)\n", entry.Name, fixtureRoot, entry.Target, entry.Reason)
	}
	fmt.Printf("diffaudit: não julgado por este portão — %s\n", unenforced())
}

// unenforced states the gaps this gate declares, with the measurement in hand: a
// limitation without a number is a limitation nobody reviews.
func unenforced() string {
	return "a demanda de teste é do arquivo **novo** de uma área de produção; a edição de um arquivo existente é medida e impressa (quantas vieram com teste na mesma área e quantas não) mas não é recusada, porque exigir teste de toda edição recusaria a correção de uma linha em outra área — o que a medição desta fase mostrou ao aplicar a política ao próprio diff; a classe é decidida pelo caminho e pelo conteúdo — o gerado pelo registro `quality/provenance.json`, a rota pelo marcador `RegisterRouteProvider` e pelo registrador, o teste pelo sufixo e pelo segmento `testdata` — e uma classe que a política não declara é recusa e não silêncio; o \"só de formato\" é decidido pelos tokens de um arquivo Go e só quando o documento carrega os dois lados dele, então um arquivo sem o lado anterior é tratado como mudança de comportamento (o lado conservador) e o número dos que ficaram sem esse lado sai em toda execução; o commit de merge é julgado apenas pelo texto, porque o seu diff contra o primeiro pai é o conteúdo do outro lado, que já passou pelo próprio PR — o número deles sai em toda execução; a resolução das referências do catálogo lê o conteúdo que a mudança traz quando o arquivo está nela e a árvore de trabalho quando não está, de modo que uma faixa cuja cabeça não seja o topo da árvore é lida com essa ressalva; e o portão lê o catálogo pelo subconjunto que julga (identidade, risco e evidência) sem ser a autoridade sobre a forma do documento — essa é do `tools/qualitycatalog`, e as categorias de evidência que a política nomeia são as que aquele carregador exige por classe de risco; e as fixtures são julgadas contra os registros **entregues** (a política, o registro de procedência e o catálogo), então um defeito real em qualquer um deles aparece na mesma execução como achado da faixa e como fixture que deixou de ser limpa — a faixa é julgada antes de as fixtures serem provadas para que a recusa nomeie o repositório, e as duas falhas viajam juntas"
}

// sortStrings orders a slice in place; it exists so the report is the same on
// two runs, which is the only way two reports can be compared.
func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for inner := index; inner > 0 && values[inner] < values[inner-1]; inner-- {
			values[inner], values[inner-1] = values[inner-1], values[inner]
		}
	}
}
