// Command provenanceaudit is the gate of P23-T06: the provenance of generated
// artifacts and the drift of what they were generated from.
//
// The repository generates four kinds of artifact — the typed SQL of sqlc, the
// locale catalogs of i18ngen, the TypeScript contracts of contractgen and the
// fingerprinted assets of assetgen —, and `make generate-check` already refuses
// an artifact that does not match what its generator writes. That target needs
// sqlc, Node and a full regeneration to answer; this gate answers the questions
// that need no toolchain, so they can run in the fast path of every PR:
//
//   - what the register declares and the tree does not have (missing-output),
//     what the tree has and the register does not declare (unknown-artifact),
//   - an artifact whose bytes are not the recorded ones, which is what a hand
//     edit looks like (output-drift), and a declared artifact that does not
//     announce that it is generated (missing-header),
//   - an input or a generator that moved since the regeneration
//     (stale-input), and a version pin edited in the register without the
//     artifact that proves it (version-drift),
//   - the determinism of a generator this gate can run in process
//     (not-reproducible), and the family that leaves its determinism neither
//     stated nor owned (uncovered-determinism).
//
// Three things are worth stating where the numbers are read:
//
//   - the register is data, and it is the review of a regeneration: a human reads
//     a diff of digests instead of a diff of a thousand generated lines. The gate
//     never rewrites it — `-print-register` prints a refreshed document, the same
//     decision the other gates of the phase record for their baselines;
//   - determinism is proven where it can be proven cheaply: the gate re-runs the
//     i18n renderer twice in process and compares it with the tree. For the
//     generators it does not run, the family declares who proves it, and the gate
//     checks that the proof is there;
//   - the census reads the disclosure of a Go file the way the toolchain does —
//     in the comments before the package clause — and the first three lines of
//     the other text files. A generated file that discloses it further down, or
//     in another vocabulary, is a file this gate does not see.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// registerPath is the record this gate reads and the file a human edits.
const registerPath = "quality/provenance.json"

// renderI18N is the one renderer this gate knows how to call in process.
const renderI18N = "i18ngen"

// phaseFamilies are the pipelines the phase names. A register without one of them
// is a register with a hole: the phase asks for the provenance of sqlc, of the
// locale catalogs, of the OpenAPI contract and of the assets, and a family that
// is not here is a generator nobody records.
var phaseFamilies = []string{"i18n", "contracts", "sqlc", "assets"}

// fixtureRoot is where the fixtures of this gate live: a directory the tree walk
// refuses to enter, which is why the proof of every rule asks for it by name.
const fixtureRoot = "tools/provenanceaudit/testdata"

// family fixture and clean fixture are the same type as the register's, because
// what a fixture is here is a register and the tree it describes.
type family = auditkit.Family

// families is the executable map of the rules: every rule with the fixture that
// proves it still bites.
var families = []family{
	{Name: RuleMissingFamily, Target: "undeclared",
		Reason: "um pipeline que a fase nomeia e o registro esquece é um artefato cujo gerador ninguém sabe qual é"},
	{Name: RuleUnknownArtifact, Target: "undeclared",
		Reason: "o arquivo que se anuncia gerado e nenhuma família declara foi produzido por algo, e esse algo não está escrito"},
	{Name: RuleUncoveredProof, Target: "undeclared",
		Reason: "a família que não diz como a reprodutibilidade do seu artefato é provada não tem resposta para \"o gerador é determinístico?\""},
	{Name: RuleMissingOutput, Target: "drift",
		Reason: "o artefato declarado e ausente da árvore é o registro descrevendo uma árvore que não existe"},
	{Name: RuleOutputDrift, Target: "drift",
		Reason: "o artefato cujos bytes não são os registrados é o artefato editado à mão, ou o registro que ninguém atualizou"},
	{Name: RuleMissingHeader, Target: "drift",
		Reason: "o gerado que não se anuncia é o gerado que alguém edita à mão e ninguém percebe"},
	{Name: RuleStaleInput, Target: "drift",
		Reason: "o insumo que mudou e o artefato que ficou é a regeneração que ninguém rodou"},
	{Name: RuleVersionDrift, Target: "drift",
		Reason: "a versão fixada no registro sem o artefato que a declara é o pino que ninguém conferiu"},
	{Name: RuleNotReproducible, Target: "render",
		Reason: "o artefato que o próprio gerador não reproduz não pode ser conferido por checksum"},
	{Name: RuleUnignoredProduct, Target: "product",
		Reason: "o produto de build que o repositório aceita é o produto de build que envelhece dentro dele"},
}

// cleanFixtures are the fixtures that must NOT be refused. A rule proved only in
// the direction that refuses is a rule that could be refusing everything while
// looking strict, and every rule of this gate has a legal neighbour: a register
// that declares the pipeline, an artifact that is what it says it is.
var cleanFixtures = []family{
	{Name: RuleMissingFamily, Target: "clean", Reason: "o registro que cobre toda família que a fase nomeia"},
	{Name: RuleUnknownArtifact, Target: "clean", Reason: "o artefato declarado não é um desconhecido"},
	{Name: RuleUncoveredProof, Target: "clean", Reason: "a família que declara quem prova a sua reprodutibilidade"},
	{Name: RuleMissingOutput, Target: "clean", Reason: "o artefato que está onde o registro diz"},
	{Name: RuleOutputDrift, Target: "clean", Reason: "o artefato cujos bytes são os registrados"},
	{Name: RuleMissingHeader, Target: "clean", Reason: "o artefato que se anuncia gerado"},
	{Name: RuleStaleInput, Target: "clean", Reason: "o insumo e o gerador que não se moveram desde a regeneração"},
	{Name: RuleVersionDrift, Target: "clean", Reason: "o pino cuja evidência a árvore declara"},
	{Name: RuleNotReproducible, Target: "clean", Reason: "a árvore que é o que o gerador produz, entre duas execuções iguais"},
	{Name: RuleUnignoredProduct, Target: "clean", Reason: "o produto de build que o .gitignore cobre"},
}

// The rules of this gate, printed by every run so that the report can be read
// without the source. A rule that is not here cannot be produced: the scan
// returns findings by name, and a name outside this table is a programming error
// the gate turns into a refusal instead of a line.
var rules = []auditkit.Rule{
	{Name: RuleMissingFamily, Reason: "a família que a fase nomeia e o registro não declara é o pipeline sem procedência"},
	{Name: RuleUnknownArtifact, Reason: "o arquivo que se anuncia gerado e nenhuma família declara é o artefato de origem desconhecida"},
	{Name: RuleMissingOutput, Reason: "o artefato declarado e ausente da árvore é o registro descrevendo o que não existe"},
	{Name: RuleMissingHeader, Reason: "o gerado que não se anuncia é o gerado que alguém edita à mão"},
	{Name: RuleOutputDrift, Reason: "o artefato cujos bytes não são os registrados foi editado à mão ou o registro ficou para trás"},
	{Name: RuleStaleInput, Reason: "o insumo ou o gerador que mudou sem regenerar deixa o artefato descrevendo uma entrada que não existe mais"},
	{Name: RuleVersionDrift, Reason: "a versão fixada sem a evidência na árvore é o pino que ninguém conferiu"},
	{Name: RuleNotReproducible, Reason: "o artefato que o gerador não reproduz, ou a árvore que não é o que ele produz agora, é drift que o checksum pegaria tarde"},
	{Name: RuleUncoveredProof, Reason: "a família que não declara como a sua reprodutibilidade é provada é a que ninguém sabe se é determinística"},
	{Name: RuleUnignoredProduct, Reason: "o produto de build fora do .gitignore é o produto de build que envelhece dentro do repositório"},
}

func main() {
	root := flag.String("root", ".", "the repository root")
	register := flag.String("register", registerPath, "the provenance register to read")
	print := flag.Bool("print-register", false, "recompute every digest and print the register, and change nothing")
	flag.Parse()
	if *print {
		if err := printRegister(*root, *register); err != nil {
			fmt.Fprintf(os.Stderr, "provenanceaudit: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*root, *register); err != nil {
		fmt.Fprintf(os.Stderr, "provenanceaudit: %v\n", err)
		os.Exit(1)
	}
}

func run(root, registerFile string) error {
	if err := proveFamilies(); err != nil {
		return err
	}
	record, err := readRegister(registerFile)
	if err != nil {
		return err
	}
	if err := os.Chdir(root); err != nil {
		return fmt.Errorf("enter %s: %w", root, err)
	}
	findings, result, err := judge(".", record, phaseFamilies, registerFile)
	if err != nil {
		return err
	}
	if result.Disclosed == 0 {
		return fmt.Errorf("the census read no artifact that announces itself as generated: the vocabulary this gate reads stopped matching the tree")
	}
	unclassified := []string{}
	for _, entry := range findings {
		if !auditkit.RuleKnown(rules, entry.Rule) {
			unclassified = append(unclassified, entry.String())
		}
	}
	if len(unclassified) > 0 {
		return fmt.Errorf("the gate produced a finding it cannot classify:\n  - %s", strings.Join(unclassified, "\n  - "))
	}
	report(result)
	if len(findings) == 0 {
		fmt.Printf("provenanceaudit: OK — nenhum achado, e cada família segue recusando a própria fixture\n")
		return nil
	}
	lines := make([]string, 0, len(findings))
	for _, entry := range findings {
		lines = append(lines, entry.String())
	}
	return fmt.Errorf("the provenance gate refused the tree:\n  - %s", strings.Join(lines, "\n  - "))
}

// proveFamilies runs every fixture and requires its rule to refuse it, and then
// runs the clean fixtures and requires silence. The fixture is read the way the
// tree is: the register of the fixture describes the tree of the fixture.
func proveFamilies() error {
	scan := func(entry family, directory string) (auditkit.ScanResult, error) {
		record, err := readRegister(directory + "/provenance.json")
		if err != nil {
			return auditkit.ScanResult{}, fmt.Errorf("a fixture %s: %w", directory, err)
		}
		findings, _, err := judge(directory, record, phaseFamilies, directory+"/provenance.json")
		return auditkit.ScanResult{Findings: findings}, err
	}
	if err := auditkit.ProveRefused(fixtureRoot, families, scan); err != nil {
		return err
	}
	return auditkit.ProveAccepted(fixtureRoot, cleanFixtures, scan)
}

// report prints everything this run measured: the corpus, the rules, what the
// register declares and the gaps the gate leaves. A number that is not printed is
// a number nobody reviews.
func report(result measured) {
	fmt.Printf("provenanceaudit: julgou %d família(s), %d artefato(s), %d insumo(s) e %d fonte(s) de gerador; %d arquivo(s) se anunciam gerados e %d são declarados; %d produto(s) de build declarado(s)\n",
		result.Families, result.Outputs, result.Inputs, result.Sources, result.Disclosed, result.Claimed, result.BuildOutputs)
	for _, entry := range rules {
		fmt.Printf("provenanceaudit: regra %s — %s\n", entry.Name, entry.Reason)
	}
	fmt.Printf("provenanceaudit: medido — %d comparação(ões) de digest e %d família(s) re-gerada(s) em processo (%s)\n",
		result.Compared, len(result.Rendered), strings.Join(result.Rendered, ", "))
	for _, entry := range families {
		fmt.Printf("provenanceaudit: família %s — recusa a fixture %s/%s\n", entry.Name, fixtureRoot, entry.Target)
	}
	for _, entry := range cleanFixtures {
		fmt.Printf("provenanceaudit: família %s — aceita a fixture %s/%s (%s)\n", entry.Name, fixtureRoot, entry.Target, entry.Reason)
	}
	fmt.Printf("provenanceaudit: não julgado por este portão — %s\n", unenforced())
}

// unenforced states the gaps this gate declares. The first one is the reason the
// gate exists next to `make generate-check` instead of inside it: the target
// answers the same questions with the real toolchain, and this one answers the
// questions that need no toolchain, in the fast path.
func unenforced() string {
	return "o gerador externo e os geradores fora de `internal/` não são executados aqui: sqlc, contractgen e assetgen têm a reprodutibilidade provada por `make generate-check` (que roda o gerador e compara com a árvore) ou pelo teste que a família declara, e este portão apenas exige que a prova exista e esteja nomeada; o censo lê o aviso de um arquivo Go nos comentários antes da cláusula `package` e o das outras extensões nas três primeiras linhas, então um gerado que se anuncia mais abaixo, ou em outro vocabulário, não é visto; e o `.gitignore` lido é o da raiz, então um produto de build ignorado por um arquivo aninhado não é reconhecido"
}
