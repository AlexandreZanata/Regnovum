package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// The vocabulary of this gate. Every finding names one of these, and a name
// outside the table is a refusal instead of a line in the report.
const (
	RuleUnregistered     = "unregistered-component"
	RuleUnknown          = "unknown-component"
	RuleIncomplete       = "incomplete-entry"
	RuleUnknownStatement = "unknown-statement"
	RuleIncompatible     = "incompatible-license"
	RuleUnpinned         = "unpinned-version"
	RuleUnpinnedImage    = "unpinned-image"
	RuleUnpinnedAction   = "unpinned-action"
	RuleUnusedModule     = "unused-module"
	RuleDuplicatePin     = "duplicate-pin"
	RuleBanned           = "banned-component"
	RuleBrowserRuntime   = "browser-runtime"
	RuleSBOMDrift        = "sbom-drift"
)

// judged is what the judgments measured, next to what the census measured: the
// approvals that held, the pins that were pins, the statements that agreed with
// the register and the components of the bill of materials.
type judged struct {
	Approved       int
	Judgments      int
	Pinned         int
	DigestPins     int
	CommitPins     int
	StatementHeld  int
	SBOMComponents int
	BareImports    int
	UnusedModules  int
}

// judgeContext is everything a judgment reads and writes. One value travels
// instead of six arguments because the judgments differ in what they ask and not
// in what they are given.
type judgeContext struct {
	root     string
	register register
	measured census
	imports  map[string]bool
	findings *[]auditkit.Finding
	counts   *judged
}

// judge runs every judgment over one measurement of the tree.
func judge(root string, document register, measured census) ([]auditkit.Finding, judged, error) {
	imports, err := importedModules(root, document)
	if err != nil {
		return nil, judged{}, err
	}
	findings := []auditkit.Finding{}
	counts := judged{}
	ctx := judgeContext{root: root, register: document, measured: measured, imports: imports, findings: &findings, counts: &counts}
	for _, rule := range []struct {
		name string
		run  func(judgeContext) error
	}{
		{RuleUnregistered, judgeUnregistered},
		{RuleUnknown, judgeUnknown},
		{RuleIncomplete, judgeIncomplete},
		{RuleUnknownStatement, judgeStatements},
		{RuleIncompatible, judgeLicenses},
		{RuleUnpinned, judgePins},
		{RuleUnpinnedImage, judgeImagePins},
		{RuleUnpinnedAction, judgeActionPins},
		{RuleUnusedModule, judgeUnusedModules},
		{RuleDuplicatePin, judgeDuplicatePins},
		{RuleBanned, judgeBanned},
		{RuleBrowserRuntime, judgeBrowser},
		{RuleSBOMDrift, judgeSBOM},
	} {
		ctx.counts.Judgments++
		if err := rule.run(ctx); err != nil {
			return nil, judged{}, fmt.Errorf("regra %s: %w", rule.name, err)
		}
	}
	auditkit.SortFindings(findings)
	return findings, counts, nil
}

// report refuses a judgment with a finding it cannot name.
func refuse(ctx judgeContext, rule, path string, line int, detail string) {
	*ctx.findings = append(*ctx.findings, auditkit.Finding{Rule: rule, Path: path, Line: line, Detail: detail})
}

// judgeUnregistered is the rule the others lean on: a component the tree
// declares that no entry approves. It is what makes a new transitive package,
// a moved version and a new image a refusal on the change that brings them,
// instead of a line somebody notices at release time.
func judgeUnregistered(ctx judgeContext) error {
	for _, component := range ctx.measured.Components {
		if _, approved := approvingEntry(ctx.register, component); approved {
			ctx.counts.Approved++
			continue
		}
		class := "sem classe"
		if drifted, ok := namedEntry(ctx.register, component.Kind, component.Name); ok {
			class = fmt.Sprintf("a versão aprovada é %s e a árvore declara %s", drifted.Version, component.Version)
		}
		refuse(ctx, RuleUnregistered, component.File, component.Line, fmt.Sprintf(
			"o componente %s %q na versão %q não é aprovado pelo registro `%s`: %s — dependência sem aprovação é dependência que ninguém triou, e a saída declarada é entrar no registro com dono, finalidade, alcance e licença, e não a árvore crescer em silêncio",
			component.Kind, component.Name, component.Version, registerPath, class))
	}
	return nil
}

// judgeUnknown is the other direction: an entry the tree no longer declares.
// Removing a dependency without updating the evidence leaves an approval for
// something nobody uses, which is how an allowlist stops describing the tree.
func judgeUnknown(ctx judgeContext) error {
	for _, item := range ctx.register.Entries {
		// The platform and the tool whose class pins the name are declarations
		// the census cannot extract: there is no manifest line to be missing, and
		// the gate would be refusing the declaration it asked for.
		if declaredOnly(ctx.register, item) {
			continue
		}
		if _, approved := approvedAnywhere(ctx.measured, item); approved {
			continue
		}
		refuse(ctx, RuleUnknown, registerPath, 0, fmt.Sprintf(
			"o registro aprova %s %q na versão %q e a árvore não declara esse componente: remover uma dependência é atualizar a evidência dela, e uma aprovação que sobrevive ao componente é a lista que deixou de descrever a árvore",
			item.Kind, item.Name, item.Version))
	}
	return nil
}

// judgeIncomplete is the demand of the fields: an approval without an owner, a
// purpose, a scope, a class or a license is a name, and a name is not an
// approval — nobody can triage what nobody owns.
func judgeIncomplete(ctx judgeContext) error {
	for _, item := range ctx.register.Entries {
		missing := []string{}
		for _, field := range []struct {
			name  string
			value string
		}{
			{"class", item.Class}, {"owner", item.Owner}, {"purpose", item.Purpose},
			{"scope", item.Scope}, {"license", item.License},
		} {
			if strings.TrimSpace(field.value) == "" {
				missing = append(missing, field.name)
			}
		}
		// A tool whose class demands an exact pin has to name where the tree
		// declares it: the version of a tool is not in a manifest, and an
		// approval of a version nobody can find is a sentence without a place.
		// The class whose pin is the name is the declared exception, and the
		// gate measures that population instead of demanding evidence for it.
		class, known := ctx.register.classNamed(item.Class)
		if item.Kind == kindTool && known && class.Pin == pinExact && len(item.VersionEvidence) == 0 {
			missing = append(missing, "version_evidence")
		}
		if len(missing) > 0 {
			refuse(ctx, RuleIncomplete, registerPath, 0, fmt.Sprintf(
				"a entrada %s %q não declara %s: uma entrada sem dono, finalidade e alcance é um nome, e a lista de nomes ninguém consegue triar",
				item.Kind, item.Name, strings.Join(missing, ", ")))
			continue
		}
		if err := judgeStatementHolds(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

// judgeStatementHolds checks the license claim against the document that states
// it: the row that names the component has to carry the license the register
// says. A claim with no statement behind it is a claim nobody can check.
func judgeStatementHolds(ctx judgeContext, item entry) error {
	rows := ctx.measured.Rows
	label := item.labelOf()
	found := false
	for _, row := range rows {
		if !strings.Contains(row.Label, label) && !strings.Contains(row.Text, label) {
			continue
		}
		found = true
		if strings.Contains(strings.ToLower(row.License), strings.ToLower(item.License)) {
			ctx.counts.StatementHeld++
			return nil
		}
	}
	if !found {
		refuse(ctx, RuleIncomplete, ctx.register.Statement, 0, fmt.Sprintf(
			"o registro aprova %s %q com a licença %q e o documento `%s`, que é onde a árvore declara as licenças, não tem linha para ele: a aprovação cita a fonte e a fonte não a tem",
			item.Kind, item.labelOf(), item.License, ctx.register.Statement))
		return nil
	}
	refuse(ctx, RuleIncomplete, ctx.register.Statement, 0, fmt.Sprintf(
		"a linha do componente %q em `%s` não declara a licença %q que o registro aprova: a licença que não está escrita onde a árvore a declara é uma licença que só existe no registro",
		label, ctx.register.Statement, item.License))
	return nil
}

// judgeStatements is the other direction of the same claim: a row of the license
// document that names a component no entry approves. It is how a catalogue row
// survives the tool it describes — the row of a linter the tree stopped using is
// a statement about a dependency that is not there.
func judgeStatements(ctx judgeContext) error {
	labels := map[string]bool{}
	for _, item := range ctx.register.Entries {
		labels[item.labelOf()] = true
		labels[item.Name] = true
	}
	for _, row := range ctx.measured.Rows {
		if labels[row.Label] {
			continue
		}
		declared := false
		for _, item := range ctx.register.Entries {
			if strings.Contains(row.Label, item.labelOf()) || strings.Contains(item.labelOf(), row.Label) {
				declared = true
				break
			}
		}
		if declared {
			continue
		}
		refuse(ctx, RuleUnknownStatement, ctx.register.Statement, row.Line, fmt.Sprintf(
			"a linha %q de `%s` declara uma dependência que o registro `%s` não aprova: o catálogo que sobrevive à dependência descreve um projeto que não existe, e a saída declarada é registrar o componente ou retirar a linha",
			row.Label, ctx.register.Statement, registerPath))
	}
	return nil
}

// judgeLicenses holds the license of every entry against the class that
// approves it: a license is compatible with a class of use and not with a
// project, and the copyleft that is acceptable for a linter executed outside the
// artifact is not acceptable for code compiled into the binary.
func judgeLicenses(ctx judgeContext) error {
	for _, item := range ctx.register.Entries {
		class, ok := ctx.register.classNamed(item.Class)
		if !ok || strings.TrimSpace(item.License) == "" {
			continue
		}
		if contains(class.Licenses, item.License) {
			continue
		}
		refuse(ctx, RuleIncompatible, registerPath, 0, fmt.Sprintf(
			"a entrada %s %q declara a licença %q e a classe %s homologa %s: uma dependência de %s não pode carregar uma licença que a classe não aceita, e o remédio é a licença compatível ou a substituição da dependência, nunca a classe deixar de dizer o que aceita",
			item.Kind, item.Name, item.License, class.Name, strings.Join(class.Licenses, ", "), class.Role))
	}
	return nil
}

// judgePins holds every component to the pin its class demands. A version is a
// pin when it names bytes and not a range: `^1.2.3` follows the vendor, and a
// build that follows the vendor is a build nobody reproduced.
func judgePins(ctx judgeContext) error {
	for _, component := range ctx.measured.Components {
		item, approved := approvingEntry(ctx.register, component)
		if !approved {
			continue
		}
		class, ok := ctx.register.classNamed(item.Class)
		if !ok || class.Pin != pinExact {
			continue
		}
		if pinnedExactly(component.Version) {
			ctx.counts.Pinned++
			continue
		}
		refuse(ctx, RuleUnpinned, component.File, component.Line, fmt.Sprintf(
			"o componente %s %q declara a versão %q e a classe %s exige um pino exato: uma faixa segue o fornecedor, e um build que segue o fornecedor não é o build que alguém reproduziu — a saída declarada é a versão exata no manifesto",
			component.Kind, component.Name, component.Version, class.Name))
	}
	return nil
}

// judgeImagePins demands the digest where the image is deployed: a file that
// runs in production pins the bytes, and a tag is a label somebody can move.
func judgeImagePins(ctx judgeContext) error {
	for _, component := range ctx.measured.Components {
		if component.Kind != kindImage || !matchesAny(ctx.register.Paths.ProductionImages, component.File) {
			continue
		}
		if component.Digest != "" {
			ctx.counts.DigestPins++
			continue
		}
		refuse(ctx, RuleUnpinnedImage, component.File, component.Line, fmt.Sprintf(
			"a imagem %q de `%s` não carrega digest: o arquivo que roda em produção pina os bytes e não o rótulo, e a tag que alguém pode mover é a imagem que muda sem o commit mudar — a saída declarada é `%s@sha256:…`",
			component.Name, component.File, component.Name+":"+component.Version))
	}
	return nil
}

// judgeActionPins demands a full commit of every workflow action: a tag is a
// ref that the owner of the action can point somewhere else, and the workflow
// that runs on every pull request is the one that must not follow it.
func judgeActionPins(ctx judgeContext) error {
	for _, component := range ctx.measured.Components {
		if component.Kind != kindAction {
			continue
		}
		if isCommitSHA(component.Version) {
			ctx.counts.CommitPins++
			continue
		}
		refuse(ctx, RuleUnpinnedAction, component.File, component.Line, fmt.Sprintf(
			"a action %q de `%s` está presa a %q e não a um commit: uma tag é uma ref que o dono da action pode mover para outro conteúdo, e o desenho desta esteira é que a action que roda em cada pull request seja o commit que alguém revisou",
			component.Name, component.File, component.Version))
	}
	return nil
}

// judgeUnusedModules refuses a direct requirement no file of the tree imports.
// A module that nothing imports is a build that carries a dependency for a
// reason nobody can name, and the reason it is checked is that the cheap fix —
// adding a name to the allowlist — is what hides it.
func judgeUnusedModules(ctx judgeContext) error {
	for _, item := range ctx.register.Entries {
		if item.Kind != kindModule || (item.Direct != nil && !*item.Direct) {
			continue
		}
		if ctx.imports[item.Name] {
			continue
		}
		ctx.counts.UnusedModules++
		refuse(ctx, RuleUnusedModule, ctx.register.Paths.GoMod, 0, fmt.Sprintf(
			"o módulo %q é uma exigência direta do `%s` e nenhum arquivo Go desta árvore o importa: uma dependência que ninguém importa é uma dependência que só o manifesto carrega, e a saída declarada é removê-la do manifesto ou provar o consumo",
			item.Name, ctx.register.Paths.GoMod))
	}
	return nil
}

// judgeDuplicatePins refuses a component pinned at two versions: one of the two
// is the truth and the other is a document that lies, and the reader cannot tell
// which is which.
func judgeDuplicatePins(ctx judgeContext) error {
	versions := map[string]map[string]bool{}
	for _, item := range ctx.register.Entries {
		key := componentKey(item.Kind, item.Name)
		if versions[key] == nil {
			versions[key] = map[string]bool{}
		}
		versions[key][item.Version] = true
	}
	for _, component := range ctx.measured.Components {
		key := componentKey(component.Kind, component.Name)
		if versions[key] == nil {
			continue
		}
		versions[key][component.Version] = true
	}
	for key, seen := range versions {
		if len(seen) < 2 {
			continue
		}
		refuse(ctx, RuleDuplicatePin, registerPath, 0, fmt.Sprintf(
			"o componente %s aparece em %s versões (%s): um pino escrito duas vezes é um pino em que uma das duas mentiu, e quem lê não sabe qual das duas a árvore usa",
			key, plural(len(seen)), strings.Join(sortedSet(seen), ", ")))
	}
	return nil
}

// judgeBanned refuses the names the policy bans, in the manifest and in the
// register alike: the ban is about what the project decided not to carry, and a
// decision that can be worked around by writing the name in the other document
// is not a decision.
func judgeBanned(ctx judgeContext) error {
	for _, component := range ctx.measured.Components {
		if bannedMatch(ctx.register.Banned, component.Name) == "" {
			continue
		}
		refuse(ctx, RuleBanned, component.File, component.Line, fmt.Sprintf(
			"o componente %q é banido pela política (`banned` do registro `%s`): %s — a saída declarada é a alternativa nativa, e o nome não volta pela lista de exceções",
			component.Name, registerPath, bannedMatch(ctx.register.Banned, component.Name)))
	}
	for _, item := range ctx.register.Entries {
		if bannedMatch(ctx.register.Banned, item.Name) == "" {
			continue
		}
		refuse(ctx, RuleBanned, registerPath, 0, fmt.Sprintf(
			"o registro aprova %q, que a própria política bane: um documento que se contradiz aprova o que proíbe, e a leitura que fica é a que convém a quem lê por último",
			item.Name))
	}
	return nil
}

// judgeBrowser is the rule of the hard zero of this project: the browser runs no
// third party at all. It refuses both ends — the manifest that declares a runtime
// package and the source that imports a specifier which leaves the tree — because
// a runtime dependency and an import are the same decision read from two files.
func judgeBrowser(ctx judgeContext) error {
	for _, importLine := range ctx.measured.Imports {
		if strings.HasPrefix(importLine.Specifier, ".") || strings.HasPrefix(importLine.Specifier, "/") {
			continue
		}
		if contains(ctx.register.BrowserSpecifiers, importLine.Specifier) {
			continue
		}
		ctx.counts.BareImports++
		refuse(ctx, RuleBrowserRuntime, importLine.File, importLine.Line, fmt.Sprintf(
			"a fonte do browser importa %q, que sai da árvore: o navegador deste projeto não executa nenhuma biblioteca de terceiros, e nem o import nem uma lista de exceções mudam isso — o remédio é a API nativa que faz o mesmo trabalho",
			importLine.Specifier))
	}
	runtime, err := manifestRuntime(ctx.root, ctx.register)
	if err != nil {
		return err
	}
	for _, component := range runtime {
		refuse(ctx, RuleBrowserRuntime, component.File, component.Line, fmt.Sprintf(
			"o manifesto do browser declara %q em `dependencies`, e o bloco de runtime é o que o navegador executa: a única dependência autorizada do frontend é o compilador, e ele é de build — a saída declarada é o bloco `devDependencies` ou a API nativa",
			component.Name))
	}
	return nil
}

// judgeSBOM holds the bill of materials to the tree: it is derived, so the
// document on disk that stopped matching the tree is a refusal and not a stale
// file. The rule compares both directions and every field the document carries,
// because a bill of materials that agrees with the tree about the names and
// disagrees about the versions describes a build nobody has.
func judgeSBOM(ctx judgeContext) error {
	raw, err := os.ReadFile(filepath.Join(ctx.root, ctx.register.Paths.SBOM))
	if err != nil {
		refuse(ctx, RuleSBOMDrift, ctx.register.Paths.SBOM, 0, fmt.Sprintf(
			"a lista de materiais não existe em `%s`: o documento é derivado e não opcional, e uma entrega sem a lista do que a compõe é uma entrega que ninguém consegue conferir — a saída declarada é `$(GO) run ./tools/dependencyaudit -print-sbom`",
			ctx.register.Paths.SBOM))
		return nil
	}
	onDisk, err := decodeSBOM(ctx.register.Paths.SBOM, raw)
	if err != nil {
		refuse(ctx, RuleSBOMDrift, ctx.register.Paths.SBOM, 0, err.Error())
		return nil
	}
	derived := buildSBOM(ctx.root, ctx.register)
	ctx.counts.SBOMComponents = len(derived.Components)
	if len(onDisk.Components) == 0 {
		refuse(ctx, RuleSBOMDrift, ctx.register.Paths.SBOM, 0,
			"o documento não declara componente nenhum: uma lista de materiais vazia é a forma de um documento que parou de ser derivado, e o portão recusa em vez de comparar com nada")
	}
	onDiskByKey := map[string]sbomComponent{}
	for _, line := range onDisk.Components {
		onDiskByKey[sbomKey(line)] = line
	}
	for _, line := range derived.Components {
		committed, ok := onDiskByKey[sbomKey(line)]
		if !ok {
			refuse(ctx, RuleSBOMDrift, ctx.register.Paths.SBOM, 0, fmt.Sprintf(
				"a árvore declara %s %q %q (%s) e o documento não o tem: a lista de materiais é derivada, e a linha que a árvore passou a produzir sem o documento é uma lista que deixou de descrever o que se entrega — a saída declarada é `$(GO) run ./tools/dependencyaudit -print-sbom`",
				line.Kind, line.Name, line.Version, strings.Join(line.Files, ", ")))
			continue
		}
		delete(onDiskByKey, sbomKey(line))
		if difference := sbomDifference(committed, line); difference != "" {
			refuse(ctx, RuleSBOMDrift, ctx.register.Paths.SBOM, 0, fmt.Sprintf(
				"a linha %s %q diverge do que a árvore declara: %s — o documento derivado que discorda do documento que o gerou descreve uma entrega que ninguém tem",
				line.Kind, line.Name, difference))
		}
	}
	for _, key := range sortedComponents(onDiskByKey) {
		refuse(ctx, RuleSBOMDrift, ctx.register.Paths.SBOM, 0, fmt.Sprintf(
			"o documento declara %q e a árvore não o produz mais: a lista de materiais que sobrevive ao componente descreve uma entrega que ninguém faz",
			key))
	}
	return nil
}

// decodeSBOM reads the bill of materials, refusing a schema this gate does not
// write: a document from another generator is a document whose fields are a
// guess.
func decodeSBOM(path string, raw []byte) (sbom, error) {
	var document sbom
	if err := decodeJSON(raw, &document); err != nil {
		return sbom{}, fmt.Errorf("`%s` não decodifica como a lista de materiais que este portão escreve: %w", path, err)
	}
	if document.Schema != registerSchemaVersion {
		return sbom{}, fmt.Errorf("`%s` declara o esquema %d e este portão escreve o %d", path, document.Schema, registerSchemaVersion)
	}
	return document, nil
}

// sbomDifference answers the first field the committed line disagrees about.
func sbomDifference(committed, derived sbomComponent) string {
	for _, field := range []struct {
		name           string
		committedValue string
		derivedValue   string
	}{
		{"version", committed.Version, derived.Version},
		{"class", committed.Class, derived.Class},
		{"license", committed.License, derived.License},
		{"owner", committed.Owner, derived.Owner},
		{"scope", committed.Scope, derived.Scope},
		{"digest", committed.Digest, derived.Digest},
		{"files", strings.Join(committed.Files, ", "), strings.Join(derived.Files, ", ")},
	} {
		if field.committedValue != field.derivedValue {
			return fmt.Sprintf("o campo %s diz %q e a árvore diz %q", field.name, field.committedValue, field.derivedValue)
		}
	}
	return ""
}

// sbomKey identifies one line of the bill of materials: the component, and not
// the file it was read from. Two references of the same image in two files are
// one component of what is delivered, and the files travel in the line.
func sbomKey(line sbomComponent) string {
	return fmt.Sprintf("%s %s %s", line.Kind, line.Name, line.Version)
}

// sortedComponents orders the keys for a stable report.
func sortedComponents(lines map[string]sbomComponent) []string {
	keys := make([]string, 0, len(lines))
	for key := range lines {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// approvedAnywhere answers whether some component is the one an entry approves.
func approvedAnywhere(measured census, item entry) (component, bool) {
	for _, component := range measured.Components {
		if component.Kind != item.Kind || component.Name != item.Name {
			continue
		}
		if versionMatches(item, component) {
			return component, true
		}
	}
	return component{}, false
}

// namedEntry answers the entry that names a component, whatever version it
// approves, which is what turns a refusal into a sentence about drift.
func namedEntry(document register, kind, name string) (entry, bool) {
	for _, item := range document.Entries {
		if item.Kind == kind && item.Name == name {
			return item, true
		}
	}
	return entry{}, false
}

// importedModules answers which approved modules some file of the tree imports.
// The whole tree is read once: a module that is imported by a test is imported,
// and the question this rule asks is about the build and not about the binary.
func importedModules(root string, document register) (map[string]bool, error) {
	files, err := auditkit.GoFiles(root, skippedDirectories)
	if err != nil {
		return nil, err
	}
	imported := map[string]bool{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		body := string(raw)
		for _, item := range document.Entries {
			if item.Kind != kindModule || imported[item.Name] {
				continue
			}
			if strings.Contains(body, `"`+item.Name) {
				imported[item.Name] = true
			}
		}
	}
	return imported, nil
}

// bannedMatch answers the banned token a name carries, or the empty string. The
// name matches exactly, as the family it names (`react` bans `react-dom`), or as
// the scope it opens (`@vue/` bans `@vue/compiler-sfc`) — and a name that merely
// begins with the letters of a banned token (`reactive-streams`) is not the
// thing the policy banned, because a ban that refuses by accident is a ban
// nobody can keep.
func bannedMatch(banned []string, name string) string {
	lowered := strings.ToLower(name)
	for _, token := range banned {
		trimmed := strings.ToLower(strings.TrimSpace(token))
		if trimmed == "" {
			continue
		}
		if lowered == trimmed {
			return token
		}
		if strings.HasSuffix(trimmed, "/") && strings.HasPrefix(lowered, trimmed) {
			return token
		}
		if strings.HasPrefix(lowered, trimmed+"-") || strings.HasPrefix(lowered, trimmed+".") || strings.HasPrefix(lowered, trimmed+"@") {
			return token
		}
	}
	return ""
}

// bannedPrefix answers whether a banned token prefixes a component, which is the
// same question `bannedMatch` answers for a name.
func bannedPrefix(banned []string, name string) bool {
	return bannedMatch(banned, name) != ""
}

// pinnedExactly answers whether a version names bytes: a range, a wildcard and a
// floating tag do not.
func pinnedExactly(version string) bool {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return false
	}
	if strings.EqualFold(trimmed, "latest") || strings.EqualFold(trimmed, "dev") {
		return false
	}
	return !strings.ContainsAny(trimmed, "^~*<>=!| ") && !strings.HasSuffix(trimmed, ".x") && !strings.HasSuffix(trimmed, ".X")
}

// sortedSet orders a set for a stable message.
func sortedSet(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// plural writes the count with the word it needs.
func plural(count int) string {
	if count == 1 {
		return "1 versão"
	}
	return fmt.Sprintf("%d versões", count)
}
