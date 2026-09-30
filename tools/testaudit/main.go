// Command testaudit is the gate of P23-T07: the quality of the tests
// themselves.
//
// The other gates of this phase judge code and leave the tests out of the corpus
// on purpose — a port that judged a double would refuse the scaffolding that
// makes the failure path reachable — and this is the gate that judges them. The
// program states what it is written against, in one section:
//
//	Proibidos `t.Skip`, suites desabilitadas, exclusão de pacote, `|| true`,
//	retries automáticos e thresholds reduzidos para passar.
//	Tempo, UUID, random, rede e clock devem ser controláveis; `time.Sleep` não
//	sincroniza teste.
//	Testes devem provar efeito observável e conter guarda antivacuidade.
//
// The nine rules, each one decidable by reading a single file:
//
//   - skipped-test — the test that does not run;
//   - vacuous-test — the test whose body cannot fail: it calls nothing, asserts
//     nothing and panics nowhere;
//   - tautological-assertion — the comparison of an expression with itself;
//   - unused-fixture — the file under `testdata/` that no Go file names;
//   - mock-crowd — the test file whose doubles outnumber its assertions;
//   - sleep-synchronization — the bare `time.Sleep` of a test body, the wait
//     that hopes;
//   - unregistered-seed — the unseeded global generator of `math/rand` reached
//     from a test;
//   - ignored-error — the error a test observes and then swallows;
//   - incompatible-outcomes — the condition that asks one subject for both
//     boolean values at once, so the refusal it guards can never decide.
//
// Three things are worth stating where the numbers are read:
//
//   - the exception is a register, not a line: `quality/test-waivers.json` names
//     the site, the rule, a non-critical risk class, an owner, a justification, a
//     date of expiry and a compensating test that has to resolve. The gate refuses
//     a critical exception, an expired one and a stale one — a waiver whose site
//     no longer carries the finding, which is how fixing the site forces the
//     register to shrink. The gate never writes the file;
//   - the corpus is the test files of the tree and the fixtures under `testdata/`,
//     and the report prints how many of each it read. A run that judged nothing
//     is refused rather than green: an empty corpus is the shape of a gate that
//     stopped working;
//   - the gaps are printed with their measurement. A test that delegates every
//     assertion to a harness is counted and not refused (a suite whose subject is
//     a contract harness is a suite with one test and many cases, which is what
//     this tree does); a `time.Sleep` is refused where the test runs it and
//     counted where it does not — the poll inside a loop re-reads the condition
//     it waits for, and the pause inside a double the test hands to the product
//     is the timing of the subject, not the synchronization of the test. The
//     report prints both counts so the boundary is reviewed and not assumed.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// fixtureRoot is where the fixtures of this gate live: a directory the tree walk
// refuses to enter, which is why the proof of every rule asks for it by name.
const fixtureRoot = "tools/testaudit/testdata"

// family is one rule with the fixture that proves it still bites.
type family = auditkit.Family

// rules is the vocabulary of this gate, printed by every run so that the report
// can be read without the source. A rule that is not here cannot be produced: the
// scan returns findings by name, and a name outside this table is a programming
// error the gate turns into a refusal instead of a line.
var rules = []auditkit.Rule{
	{Name: RuleSkipped, Reason: "o teste que a suíte pula é indistinguível de um teste que passou, e o programa proíbe `t.Skip`"},
	{Name: RuleVacuous, Reason: "o teste que não chama, não assere e não entra em pânico passa por não provar nada"},
	{Name: RuleTautology, Reason: "a comparação de uma expressão com ela mesma não lê valor nenhum, então a asserção que ela guarda é decorativa"},
	{Name: RuleUnusedFixture, Reason: "a fixture que nenhum arquivo nomeia parou de verificar algo sem que ninguém visse"},
	{Name: RuleMockCrowd, Reason: "o arquivo cujos dublês são mais numerosos que as asserções passou a ser sobre o andaime"},
	{Name: RuleSleep, Reason: "`time.Sleep` não sincroniza teste: a pausa fixa espera que o trabalho termine em vez de esperar por ele"},
	{Name: RuleUnregisteredSeed, Reason: "a entropia de um gerador global não semeado é decidida pelo processo, e uma falha que ela produz não se reproduz"},
	{Name: RuleIgnoredError, Reason: "o erro que o teste observa e não cobra termina verde, e o caminho de erro fica sem prova"},
	{Name: RuleIncompatibleOutcomes, Reason: "a condição que pede dois resultados incompatíveis ao mesmo sujeito é constante, e a recusa que ela guarda não decide nada"},
}

// families is the executable map of the rules: every rule with the fixture that
// proves it still bites. A rule proved only in the direction that refuses would
// be a rule that could be refusing everything while looking strict, so every one
// of them also has a legal neighbour in cleanFixtures below.
var families = []family{
	{Name: RuleSkipped, Target: "skipped", Reason: "o teste que desliga a si mesmo é o anti-padrão que o programa proíbe por nome"},
	{Name: RuleVacuous, Target: "vacuous", Reason: "o teste cujo corpo só declara variáveis passa sem provar nada"},
	{Name: RuleTautology, Target: "tautology", Reason: "a asserção que compara um valor com ele mesmo é decorativa"},
	{Name: RuleUnusedFixture, Target: "unusedfixture", Reason: "a fixture que nenhum arquivo nomeia parou de verificar algo"},
	{Name: RuleMockCrowd, Target: "mockcrowd", Reason: "o arquivo cujos dublês são mais numerosos que as asserções fala de si mesmo"},
	{Name: RuleSleep, Target: "sleep", Reason: "a pausa fixa no corpo do teste espera que o trabalho termine em vez de esperar por ele"},
	{Name: RuleUnregisteredSeed, Target: "seed", Reason: "o gerador global não semeado decide a entropia pelo processo"},
	{Name: RuleIgnoredError, Target: "ignorederror", Reason: "o erro observado e não cobrado deixa o caminho de erro sem prova"},
	{Name: RuleIncompatibleOutcomes, Target: "outcomes", Reason: "a condição constante aceita qualquer resultado, inclusive o errado"},
}

// cleanFixtures are the fixtures that must NOT be refused: the neighbour of each
// rule, which is the shape the tree is allowed to have.
var cleanFixtures = []family{
	{Name: RuleSkipped, Target: "clean", Reason: "o teste que roda sempre, sem `t.Skip`"},
	{Name: RuleVacuous, Target: "clean/vacuous", Reason: "o teste que entrega o `*testing.T` ao harness e o que assere por si"},
	{Name: RuleTautology, Target: "clean/tautology", Reason: "a comparação entre duas expressões distintas, e a comparação de um valor com uma constante"},
	{Name: RuleUnusedFixture, Target: "clean", Reason: "a fixture que o teste nomeia"},
	{Name: RuleMockCrowd, Target: "clean/mockcrowd", Reason: "o arquivo com dublês que assere muito mais do que declara"},
	{Name: RuleSleep, Target: "clean/sleep", Reason: "a espera pela condição, e a pausa que o próprio produto sob teste encena"},
	{Name: RuleUnregisteredSeed, Target: "clean/seed", Reason: "o teste que tira a entropia da fonte registrada"},
	{Name: RuleIgnoredError, Target: "clean/ignorederror", Reason: "o erro que vira `t.Fatal`, o caso que o `continue` declara e o auxiliar que devolve a falha"},
	{Name: RuleIncompatibleOutcomes, Target: "clean/outcomes", Reason: "a condição que pergunta por dois valores distintos, que é o que uma recusa faz"},
}

func main() {
	root := flag.String("root", ".", "the repository root")
	waivers := flag.String("waivers", waiverPath, "the exception register to read")
	flag.Parse()
	if err := run(*root, *waivers); err != nil {
		fmt.Fprintf(os.Stderr, "testaudit: %v\n", err)
		os.Exit(1)
	}
}

func run(root, waiverFile string) error {
	if err := proveFamilies(); err != nil {
		return err
	}
	findings, result, sites, err := scan(root, auditkit.SkippedDirectories)
	if err != nil {
		return err
	}
	if result.Tests == 0 || result.Files == 0 {
		return fmt.Errorf("a varredura leu %d arquivo(s) de teste e %d teste(s): um corpus vazio é a forma de um portão que parou de funcionar, e este não responde verde sobre nada", result.Files, result.Tests)
	}
	if result.Assertions == 0 {
		return fmt.Errorf("a varredura não viu asserção nenhuma na árvore: o vocabulário que este portão lê deixou de casar com o dos testes")
	}
	register, accepted, err := readWaivers(root, waiverFile, rules, findings, sites, time.Now())
	if err != nil {
		return err
	}
	result.Waived = len(accepted)
	refused := []auditkit.Finding{}
	waived := map[string]bool{}
	for _, entry := range accepted {
		waived[entry] = true
	}
	for _, finding := range findings {
		if waived[fmt.Sprintf("%s (%s)", sites[finding], finding.Rule)] {
			continue
		}
		refused = append(refused, finding)
	}
	unclassified := []string{}
	for _, entry := range refused {
		if !auditkit.RuleKnown(rules, entry.Rule) {
			unclassified = append(unclassified, entry.String())
		}
	}
	if len(unclassified) > 0 {
		return fmt.Errorf("o portão produziu um achado que ele não sabe classificar:\n  - %s", strings.Join(unclassified, "\n  - "))
	}
	report(register, accepted, result, len(findings))
	if len(refused) == 0 {
		fmt.Printf("testaudit: OK — nenhum achado, e cada família segue recusando a própria fixture\n")
		return nil
	}
	lines := make([]string, 0, len(refused))
	for _, entry := range refused {
		lines = append(lines, entry.String())
	}
	return fmt.Errorf("o portão da qualidade dos testes recusou a árvore:\n  - %s", strings.Join(lines, "\n  - "))
}

// proveFamilies runs every fixture and requires its rule to refuse it, and then
// runs the clean fixtures and requires silence. The fixture is read the way the
// tree is; what differs is the skip list, which is nil so that the walk enters
// `testdata` — the fixtures are exactly the files the tree walk refuses.
func proveFamilies() error {
	scanFixture := func(entry family, directory string) (auditkit.ScanResult, error) {
		findings, _, _, err := scan(directory, nil)
		return auditkit.ScanResult{Findings: findings}, err
	}
	if err := auditkit.ProveRefused(fixtureRoot, families, scanFixture); err != nil {
		return err
	}
	return auditkit.ProveAccepted(fixtureRoot, cleanFixtures, scanFixture)
}

// report prints everything this run measured: the corpus, the rules, the
// exceptions that were accepted and the gaps the gate leaves. A number that is
// not printed is a number nobody reviews.
func report(register waiverRegister, accepted []string, result measured, findings int) {
	fmt.Printf("testaudit: julgou %d arquivo(s) de teste, %d teste(s) e %d fixture(s); medido: %d asserção(ões), %d teste(s) sem asserção própria (dos quais %d só delegam o `*testing.T` a um harness), %d `time.Sleep` dentro de laço e %d encenado dentro de um dublê\n",
		result.Files, result.Tests, result.Fixtures, result.Assertions, result.NoAssertionOfItsOwn, result.OnlyDelegated, result.SleepsInLoop, result.SleepsStaged)
	for _, entry := range rules {
		fmt.Printf("testaudit: regra %s — %s\n", entry.Name, entry.Reason)
	}
	fmt.Printf("testaudit: %d achado(s) no total, %d aceito(s) por exceção registrada, %d recusado(s)\n",
		findings, result.Waived, findings-result.Waived)
	fmt.Printf("testaudit: %d exceção(ões) em %s\n", len(register.Waivers), waiverPath)
	for _, entry := range accepted {
		fmt.Printf("testaudit: exceção aplicada a %s\n", entry)
	}
	for _, entry := range families {
		fmt.Printf("testaudit: família %s — recusa a fixture %s/%s\n", entry.Name, fixtureRoot, entry.Target)
	}
	for _, entry := range cleanFixtures {
		fmt.Printf("testaudit: família %s — aceita a fixture %s/%s (%s)\n", entry.Name, fixtureRoot, entry.Target, entry.Reason)
	}
	fmt.Printf("testaudit: não julgado por este portão — %s\n", unenforced())
}

// unenforced states the gaps this gate declares, with the measurement in hand:
// a limitation without a number is a limitation nobody reviews.
func unenforced() string {
	return "o corpo de um teste é lido por sintaxe, sem type-checker, então o efeito de uma chamada que outro pacote implementa não é resolvido: um teste que só delega as asserções a um harness de outra árvore de diretório conta como efeito observável pela entrega do `*testing.T`, e o relatório mede e imprime quantos testes não têm asserção própria e quantos apenas delegam; as regras julgam os arquivos `_test.go` e as fixtures sob `testdata/`, e nada fora deles — um `time.Sleep` num auxiliar de um pacote que não é de teste não é julgado, e o número dos que estão dentro de laço e o dos que estão dentro de um dublê saem em toda execução; a pausa de um dublê é reconhecida pelo lugar da chamada e não pela ligação, de modo que um literal que o teste chama pelo nome que ele mesmo ligou é lido como dublê, e a saída declarada é chamar o dublê pelo nome para que a distinção apareça; a fixture é reconhecida pelo nome (o arquivo, o nome sem extensão, um segmento do nome composto ou a pasta em que ela vive), então uma fixture nomeada por uma string montada em tempo de execução só é reconhecida se algum desses pedaços aparecer no código; e a exceção é julgada pelo par (lugar, regra), de modo que uma exceção cobre exatamente o achado que ela nomeia e nenhum outro"
}
