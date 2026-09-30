package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// The rules of this gate. Every finding names one of them and the report has no
// bucket called "other".
const (
	// RuleUnclassified is the change the policy does not classify. It is the
	// first rule because it protects the others: a file nobody classifies is a
	// file whose evidence nobody can demand, and silence is how a change slips
	// past a policy that only knows the shapes it was written for.
	RuleUnclassified = "unclassified-change"
	// RuleProductionWithoutTest is the new production file with no test in the
	// same area in the same change: new behavior arrives with its proof.
	RuleProductionWithoutTest = "production-without-test"
	// RuleMigrationWithoutUpgradeTest is the migration with no upgrade proof in
	// the same change. A migration is the one change that no `git revert` undoes
	// cleanly, and the proof that the upgrade works belongs to the change that
	// writes it.
	RuleMigrationWithoutUpgradeTest = "migration-without-upgrade-test"
	// RuleRouteWithoutContract is the change that alters the routes the process
	// registers without the published contract and the generated client in the
	// same change: the contract is the other view of the same list, and a route
	// nobody published is a contract that lies.
	RuleRouteWithoutContract = "route-without-contract"
	// RuleProvenanceWithoutPair is the generated artifact — or the input that
	// produces it — that moved alone: a generated file is never the change, it is
	// the consequence of one, and an input that changed without the artifact
	// regenerated is the drift the other direction.
	RuleProvenanceWithoutPair = "provenance-without-pair"
	// RuleDeclaredTestMissing is the evidence a rule of the catalog declares and
	// the tree no longer holds. A rule whose proof disappeared is a rule that
	// reads as covered and proves nothing.
	RuleDeclaredTestMissing = "declared-test-missing"
	// RuleQ0WithoutRegression is the change to the rule table of a critical rule
	// without the nominal and the adversarial regression it declares. This is the
	// demand the phase states for Q0 in one line: the path that works and the
	// path that refuses, in the change that writes the rule.
	RuleQ0WithoutRegression = "q0-without-regression"
	// RuleBypassMarker is the commit message that announces the evidence was
	// skipped. It is the one bypass the program forbids by name, because it is
	// the only one that leaves no trace in the tree.
	RuleBypassMarker = "bypass-marker"
)

// measured is everything one run observed. The numbers that are not part of a
// judgment are printed on purpose: the boundary of this gate is an edit to an
// existing production file, and a boundary without a number is a boundary nobody
// reviews.
type measured struct {
	Commits      int
	Merges       int
	Classified   int
	Unclassified int
	FormatOnly   int
	Classes      map[string]int

	// AddedProduction and AddedProductionWithTest are the population of the
	// `same-area-test` demand, and EditedProduction and EditedProductionWithTest
	// the population the gate measures without demanding: an edit to an existing
	// production file is not, by itself, a demand for a test.
	AddedProduction          int
	AddedProductionWithTest  int
	EditedProduction         int
	EditedProductionWithTest int

	Judgments      int
	RouteChanges   int
	BypassChecked  int
	DeclaredRefs   int
	Q0Rules        int
	Q0RulesChanged int

	// FormatPaths is every file the gate exempted as formatting. The exemption
	// is printed one path per line and never summarized: a class that hides a file
	// from the demands is a class a reviewer has to be able to read.
	FormatPaths []string
}

// state is everything the judgments read besides the change: the policy, the two
// registers, the two sides of the rule table, and the content of the head — the
// latest side of every file the change touched, falling back to the tree for
// everything else.
type state struct {
	root     string
	pol      policy
	register provenanceRegister
	head     catalog
	base     catalog
	headSide map[string]string
}

// newState builds the state of one run. The head catalog comes from the change
// when the change carries it — a fixture says what the world looks like — and
// from the tree otherwise, which is the delivered case; the base catalog is only
// needed when the change touches the rule table, and a change that touches it
// without carrying the previous side is refused rather than compared with
// itself.
func newState(root string, document changeDocument, pol policy, register provenanceRegister) (state, error) {
	built := state{root: root, pol: pol, register: register, headSide: map[string]string{}}
	for _, commit := range document.Commits {
		for _, file := range commit.Files {
			if file.After != "" {
				built.headSide[file.Path] = file.After
			}
		}
	}
	headText := document.CatalogHead
	if headText == "" {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(catalogPath)))
		if err != nil {
			return state{}, fmt.Errorf("%s: %w", catalogPath, err)
		}
		headText = string(raw)
	}
	head, err := readCatalog("o catálogo da cabeça", headText)
	if err != nil {
		return state{}, err
	}
	built.head = head

	touches := false
	for _, commit := range document.Commits {
		for _, file := range commit.Files {
			if file.Path == catalogPath && file.Status != deletedFile {
				touches = true
			}
		}
	}
	switch {
	case touches && document.CatalogBase == "":
		return state{}, fmt.Errorf("a mudança toca %s e o documento não carrega o lado anterior do catálogo: a pergunta Q0 compara as duas tabelas, e comparar a tabela com ela mesma respondería que nada mudou", catalogPath)
	case touches:
		base, err := readCatalog("o catálogo da base", document.CatalogBase)
		if err != nil {
			return state{}, err
		}
		built.base = base
	default:
		built.base = head
	}
	return built, nil
}

// countTable measures the rule table once, so the report says how many critical
// rules the catalog holds even when the change touches none of them: a zero that
// reads as "the table has no Q0 rule" would be a measurement nobody should
// believe.
func (result *measured) countTable(head catalog) {
	for _, rule := range head.Rules {
		if rule.Risk == "Q0" {
			result.Q0Rules++
		}
	}
}

// content answers the head content of a path: the side the change brought, or
// the tree when the change did not touch it.
func (s state) content(path string) (string, bool) {
	if text, ok := s.headSide[path]; ok {
		return text, true
	}
	raw, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(path)))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// judge reads the whole change: every commit for what it touches, and the head
// tree for the evidence the catalog declares.
func judge(document changeDocument, st state) ([]auditkit.Finding, measured, error) {
	result := measured{Classes: map[string]int{}}
	result.countTable(st.head)
	findings := []auditkit.Finding{}

	for _, commit := range document.Commits {
		result.Commits++
		result.BypassChecked++
		classes, err := classifyAll(changeDocument{Commits: []commitRecord{commit}}, st.pol, st.register)
		if err != nil {
			return nil, result, err
		}
		if commit.Merge {
			result.Merges++
		}
		commitFindings, err := judgeCommit(commit, classes, st, &result)
		if err != nil {
			return nil, result, err
		}
		findings = append(findings, commitFindings...)
	}

	declared, err := judgeDeclaredTests(st, &result)
	if err != nil {
		return nil, result, err
	}
	findings = append(findings, declared...)

	auditkit.SortFindings(findings)
	return findings, result, nil
}

// judgeCommit judges one commit: its message, its files and the demands its
// classes owe.
func judgeCommit(commit commitRecord, classes []classification, st state, result *measured) ([]auditkit.Finding, error) {
	findings := []auditkit.Finding{}

	if word := bypassWord(commit.Message, st.pol); word != "" {
		findings = append(findings, auditkit.Finding{
			Rule: RuleBypassMarker, Path: shortSHA(commit.SHA),
			Detail: fmt.Sprintf("a mensagem do commit carrega %q, que anuncia que a evidência deste portão foi dispensada: o programa não admite bypass por mensagem de commit — a saída declarada é registrar a exceção onde ela é julgada (com dono e expiração) ou mudar o desenho da mudança", word),
		})
	}
	if commit.Merge {
		return findings, nil
	}

	for _, entry := range classes {
		result.Classified++
		result.Classes[entry.Class]++
		switch entry.Role {
		case roleUnclassified:
			result.Unclassified++
			findings = append(findings, auditkit.Finding{
				Rule: RuleUnclassified, Path: entry.Path,
				Detail: fmt.Sprintf("a política `%s` não classifica este arquivo (status %s): um diff que a política não classifica é um diff cuja evidência ninguém consegue exigir, e a saída declarada é dar-lhe uma classe — a de produção, a de isenção ou a de evidência — em vez de a mudança passar em silêncio", policyPath, entry.Status),
			})
		case roleFormat:
			result.FormatOnly++
			result.FormatPaths = append(result.FormatPaths, entry.Path)
		}
	}

	for _, entry := range classes {
		record := recordOf(commit, entry.Path)
		if record.Status == deletedFile {
			continue
		}
		class, ok := st.pol.classNamed(entry.Class)
		if !ok {
			continue
		}
		for _, owed := range class.Demands {
			result.Judgments++
			if err := judgeDemand(demandContext{
				st: st, commit: commit, classes: classes, entry: entry, record: record,
				owed: owed, result: result, findings: &findings,
			}); err != nil {
				return nil, err
			}
		}
	}

	if touchesCatalog(commit) {
		if err := judgeQ0(commit, classes, st, result, &findings); err != nil {
			return nil, err
		}
	}
	return findings, nil
}

// demandContext is everything a demand judgment reads and writes. One value
// travels instead of seven arguments because the judgments differ in what they
// ask and not in what they are given, and a signature of seven parameters is a
// function the complexity gate of this phase refuses.
type demandContext struct {
	st       state
	commit   commitRecord
	classes  []classification
	entry    classification
	record   fileRecord
	owed     demand
	result   *measured
	findings *[]auditkit.Finding
}

// judgeDemand dispatches one demand to the judgment that knows it. The
// vocabulary is closed and the policy loader refuses a kind outside it, so the
// default arm is unreachable and says so instead of silence.
func judgeDemand(ctx demandContext) error {
	switch ctx.owed.Kind {
	case demandSameAreaTest:
		judgeSameAreaTest(ctx)
	case demandMigration:
		judgeMigration(ctx)
	case demandContract:
		return judgeContract(ctx)
	case demandProvenancePair:
		judgeProvenancePair(ctx)
	default:
		return fmt.Errorf("a demanda %q chegou ao julgamento sem quem a julgue", ctx.owed.Kind)
	}
	return nil
}

// judgeSameAreaTest is the demand of new behavior: the file added to a
// production area owes a test of the same area in the same change. The demand is
// about the added file and not about every touch, and the report prints both
// populations — the one judged and the one measured — because that boundary is
// the design decision of this gate.
func judgeSameAreaTest(ctx demandContext) {
	added := ctx.record.Status == newFile
	if added {
		ctx.result.AddedProduction++
	} else {
		ctx.result.EditedProduction++
	}
	if evidenceInArea(ctx.commit, ctx.classes, ctx.entry.Area) {
		if added {
			ctx.result.AddedProductionWithTest++
		} else {
			ctx.result.EditedProductionWithTest++
		}
		return
	}
	// The demand is the policy's to attach: this gate measures both populations
	// and refuses the one the policy asks about. With `when: added` an edit to an
	// existing file is measured and printed, never refused, which is the boundary
	// the phase's own history drew.
	if ctx.owed.When != "" && ((ctx.owed.When == whenAdded) != added) {
		return
	}
	description := "o arquivo novo de produção"
	if !added {
		description = "o arquivo de produção editado"
	}
	*ctx.findings = append(*ctx.findings, auditkit.Finding{
		Rule: RuleProductionWithoutTest, Path: ctx.entry.Path,
		Detail: fmt.Sprintf("%s não veio com teste na área %s: comportamento novo chega com a prova dele, e a área é o que a política compara (`%s`) — a evidência que satisfaz a demanda é um arquivo de teste ou uma fixture sob `testdata/` da mesma área, na mesma mudança", description, ctx.entry.Area, policyPath),
	})
}

// judgeMigration is the demand of the migration: the proof of the upgrade in the
// area the policy names. The areas of the policy are prefixes and not derived
// areas: `internal/platform/dbmigrate` has to accept a test at any depth below
// it, which the depth-limited area of the `same-area-test` demand cannot express.
func judgeMigration(ctx demandContext) {
	for _, area := range ctx.owed.Areas {
		if evidenceUnder(ctx.commit, ctx.classes, area) {
			return
		}
	}
	*ctx.findings = append(*ctx.findings, auditkit.Finding{
		Rule: RuleMigrationWithoutUpgradeTest, Path: ctx.entry.Path,
		Detail: fmt.Sprintf("a migration mudou sem a prova de atualização na mesma mudança: ela é a única mudança que um `git revert` não desfaz limpo, e a evidência que a satisfaz é um arquivo de teste da área %s — o esquema novo tem de subir e ser lido antes de o commit existir", strings.Join(ctx.owed.Areas, ", ")),
	})
}

// judgeContract is the demand of the route surface: when the set of routes the
// process declares changes, the published contract and the generated client
// change with it. Merely touching the file is not the trigger — the sets of the
// two sides are compared — so fixing a handler or a comment in a route file is
// not a refusal.
func judgeContract(ctx demandContext) error {
	added, removed, err := routeSetDiff(ctx.record)
	if err != nil {
		return err
	}
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	ctx.result.RouteChanges++
	if contractInChange(ctx) {
		return nil
	}
	description := []string{}
	if len(added) > 0 {
		description = append(description, "acrescenta "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		description = append(description, "remove "+strings.Join(removed, ", "))
	}
	*ctx.findings = append(*ctx.findings, auditkit.Finding{
		Rule: RuleRouteWithoutContract, Path: ctx.entry.Path,
		Detail: fmt.Sprintf("o conjunto de rotas mudou (%s) e o contrato não: `api/openapi.json` e o cliente gerado são a outra vista da mesma lista, e a mudança que altera uma rota publica as duas — a família %q de `%s` diz quais artefatos são, e o portão exige todos eles na mesma mudança",
			strings.Join(description, " e "), ctx.st.pol.Contract.Family, provenancePath),
	})
	return nil
}

// judgeProvenancePair is the demand of provenance in both directions: an output
// that moved owes the input that produces it, and an input that moved owes the
// artifact regenerated. The pair is the unit — a generated file edited by hand
// and an input that changed without regenerating are the same defect seen from
// the two ends — and the family that declares which files pair with which is the
// register of P23-T06, so this gate and `make generate-check` never disagree
// about what belongs together.
func judgeProvenancePair(ctx demandContext) {
	family, side, ok := familyOf(ctx.st.register, ctx.entry.Path)
	if !ok || side == "source" {
		return
	}
	wanted := []string{}
	detail := ""
	switch side {
	case "output":
		wanted = append(wanted, producedBy(family)...)
		detail = fmt.Sprintf("o artefato gerado da família %q mudou sem o insumo que o produz: um arquivo gerado não é a mudança, é a consequência dela — a mudança edita o insumo (%s) e regenera, e o portão de procedência é quem confere os bytes depois",
			family.Name, strings.Join(wanted, ", "))
	case "input":
		for _, output := range family.Outputs {
			wanted = append(wanted, output.Path)
		}
		detail = fmt.Sprintf("o insumo da família %q mudou sem o artefato regenerado: o que sai do gerador é a outra ponta da mesma mudança, e deixar a regeneração para depois publica um cliente que não corresponde ao documento — a mudança regenera (%s), e o `make generate-check` confere o resto",
			family.Name, strings.Join(wanted, ", "))
	}
	for _, path := range wanted {
		if pathInChange(ctx.commit, ctx.classes, path) {
			return
		}
	}
	*ctx.findings = append(*ctx.findings, auditkit.Finding{
		Rule: RuleProvenanceWithoutPair, Path: ctx.entry.Path, Detail: detail,
	})
}

// judgeDeclaredTests resolves the evidence every rule of the catalog declares.
// It is the tree-wide half of this gate: the diff says what arrived, and this
// says that what the catalog promised is still there.
func judgeDeclaredTests(st state, result *measured) ([]auditkit.Finding, error) {
	findings := []auditkit.Finding{}
	cache := map[string]map[string]bool{}
	for _, rule := range st.head.Rules {
		for _, ref := range rule.references() {
			result.DeclaredRefs++
			declared, ok := cache[ref.Path]
			if !ok {
				declared = declaredFunctions(st, ref.Path)
				cache[ref.Path] = declared
			}
			if declared[ref.Func] {
				continue
			}
			findings = append(findings, auditkit.Finding{
				Rule: RuleDeclaredTestMissing, Path: ref.Path,
				Detail: fmt.Sprintf("a regra %s (%s, categoria %s) declara `%s`, e o teste não resolve na árvore: a evidência que sumiu deixa a regra parecendo coberta e provando nada — a saída declarada é corrigir a referência no catálogo ou restaurar o teste, e não deixar a linha apontando para o vazio", rule.ID, rule.Risk, ref.Kind, ref.Ref),
			})
		}
	}
	return findings, nil
}

// declaredFunctions answers the function names one file declares at the head
// state. A file the change brought is read from the change; a file it did not
// touch is read from the tree. A file that does not read answers the empty set,
// which is the conservative side: the declared test is then reported missing.
func declaredFunctions(st state, path string) map[string]bool {
	names := map[string]bool{}
	text, ok := st.content(path)
	if !ok {
		return names
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, text, 0)
	if err != nil {
		return names
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name == nil {
			continue
		}
		names[function.Name.Name] = true
	}
	return names
}

// judgeQ0 is the demand the phase states for critical rules: a change to the
// rule table that adds a Q0 rule or moves its evidence owes the nominal and the
// adversarial regression in the same change.
func judgeQ0(commit commitRecord, classes []classification, st state, result *measured, findings *[]auditkit.Finding) error {
	baseByID := map[string]catalogRule{}
	for _, rule := range st.base.Rules {
		baseByID[rule.ID] = rule
	}
	nominal := map[string]bool{}
	for _, kind := range st.pol.Kinds.Nominal {
		nominal[kind] = true
	}
	for _, rule := range st.head.Rules {
		if rule.Risk != "Q0" {
			continue
		}
		previous, existed := baseByID[rule.ID]
		if existed && sameEvidence(previous, rule) {
			continue
		}
		result.Q0RulesChanged++
		nominalMissing := !ruleDeclaresIn(rule, nominal, commit, classes)
		adversarialMissing := !ruleDeclaresAdversarial(rule, nominal, commit, classes)
		if !nominalMissing && !adversarialMissing {
			continue
		}
		absent := []string{}
		if nominalMissing {
			absent = append(absent, "nominal ("+strings.Join(st.pol.Kinds.Nominal, ", ")+")")
		}
		if adversarialMissing {
			absent = append(absent, "adversarial ("+strings.Join(st.pol.Kinds.Adversarial, ", ")+")")
		}
		*findings = append(*findings, auditkit.Finding{
			Rule: RuleQ0WithoutRegression, Path: catalogPath,
			Detail: fmt.Sprintf("a regra crítica %s mudou na tabela e a mudança não traz a regressão %s que ela declara: uma regra Q0 vive do par que prova que ela vale — o caminho que funciona e o caminho que recusa —, e os dois têm de estar na mudança que escreve a regra; o catálogo declara esses testes no campo `tests` e o portão exige que os arquivos deles estejam aqui", rule.ID, strings.Join(absent, " e ")),
		})
	}
	return nil
}

// ruleDeclaresIn answers whether the change brings the files of the rule's
// categories in the given set.
func ruleDeclaresIn(rule catalogRule, wanted map[string]bool, commit commitRecord, classes []classification) bool {
	for kind := range wanted {
		for _, path := range rule.ruleFiles(kind) {
			if pathInChange(commit, classes, path) {
				return true
			}
		}
	}
	return false
}

// ruleDeclaresAdversarial is the other half: at least one of the categories that
// are not nominal.
func ruleDeclaresAdversarial(rule catalogRule, nominal map[string]bool, commit commitRecord, classes []classification) bool {
	for kind := range rule.Tests {
		if nominal[kind] {
			continue
		}
		for _, path := range rule.ruleFiles(kind) {
			if pathInChange(commit, classes, path) {
				return true
			}
		}
	}
	return false
}

// sameEvidence answers whether the two sides of a rule declare the same
// evidence, category by category and order-insensitively: the question is what
// the rule promises, not how the promise was typed.
func sameEvidence(previous, current catalogRule) bool {
	kinds := map[string]bool{}
	for kind := range previous.Tests {
		kinds[kind] = true
	}
	for kind := range current.Tests {
		kinds[kind] = true
	}
	ordered := make([]string, 0, len(kinds))
	for kind := range kinds {
		ordered = append(ordered, kind)
	}
	sort.Strings(ordered)
	for _, kind := range ordered {
		first := append([]string{}, previous.Tests[kind]...)
		second := append([]string{}, current.Tests[kind]...)
		sort.Strings(first)
		sort.Strings(second)
		if strings.Join(first, "\n") != strings.Join(second, "\n") {
			return false
		}
	}
	return true
}

// evidenceInArea answers whether the change brings an evidence file of an area,
// which is the derived area of the policy and not a prefix: the two files are in
// the same area when they belong to the same module, whatever their depth.
func evidenceInArea(commit commitRecord, classes []classification, area string) bool {
	for _, entry := range classes {
		if entry.Role == roleEvidence && entry.Area == area {
			return true
		}
	}
	return false
}

// evidenceUnder answers whether the change brings an evidence file under a path
// prefix, which is how the policy names where the proof of a migration lives.
func evidenceUnder(commit commitRecord, classes []classification, prefix string) bool {
	for _, entry := range classes {
		if entry.Role == roleEvidence && strings.HasPrefix(entry.Path, prefix) {
			return true
		}
	}
	return false
}

// contractInChange answers whether the change brings every artifact of the
// contract family: the published document and the generated client. Both are
// demanded, because a contract regenerated on one side only is the drift the
// contract test exists to catch.
func contractInChange(ctx demandContext) bool {
	family, ok := familyNamed(ctx.st.register, ctx.st.pol.Contract.Family)
	if !ok {
		return false
	}
	wanted := []string{}
	for _, input := range family.Inputs {
		wanted = append(wanted, input.Path)
	}
	for _, output := range family.Outputs {
		wanted = append(wanted, output.Path)
	}
	if len(wanted) == 0 {
		return false
	}
	for _, path := range wanted {
		if !pathInChange(ctx.commit, ctx.classes, path) {
			return false
		}
	}
	return true
}

// producedBy answers the inputs and the generator sources of a family: what a
// change has to touch for the artifact to be the consequence of it.
func producedBy(family provenanceFamily) []string {
	paths := []string{}
	for _, input := range family.Inputs {
		paths = append(paths, input.Path)
	}
	for _, source := range family.Sources {
		paths = append(paths, source.Path)
	}
	sort.Strings(paths)
	return paths
}

// pathInChange answers whether a path is one the change touches. A source that
// is a directory is satisfied by a file inside it, which is how the generator
// directories of this tree are declared.
func pathInChange(commit commitRecord, classes []classification, path string) bool {
	for _, entry := range classes {
		if entry.Path == path {
			return true
		}
		if strings.HasSuffix(path, "/") && strings.HasPrefix(entry.Path, path) {
			return true
		}
	}
	return false
}

// recordOf answers the file record of a path in a commit.
func recordOf(commit commitRecord, path string) fileRecord {
	for _, file := range commit.Files {
		if file.Path == path {
			return file
		}
	}
	return fileRecord{}
}

// touchesCatalog answers whether a commit changes the rule table.
func touchesCatalog(commit commitRecord) bool {
	for _, file := range commit.Files {
		if file.Path == catalogPath && file.Status != deletedFile {
			return true
		}
	}
	return false
}

// classNamed answers the class of a name.
func (document policy) classNamed(name string) (class, bool) {
	for _, entry := range document.Classes {
		if entry.Name == name {
			return entry, true
		}
	}
	return class{}, false
}

// bypassWord answers the bypass token a message carries, in a fixed order so two
// runs name the same word.
func bypassWord(message string, pol policy) string {
	lowered := strings.ToLower(message)
	tokens := append([]string{}, pol.Bypass.Tokens...)
	sort.Strings(tokens)
	for _, token := range tokens {
		if strings.Contains(lowered, strings.ToLower(token)) {
			return token
		}
	}
	return ""
}

// shortSHA is how a finding about a commit names it: the seven characters a
// reader sees in `git log --oneline`.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
