package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// TestTheDeliveredRegisterApprovesTheTree is the ratchet of this task: the tree
// the phase delivers carries no component that nobody approved, no approval the
// tree does not declare, no license outside the class that homologates it and no
// bill of materials that disagrees with what is delivered.
func TestTheDeliveredRegisterApprovesTheTree(t *testing.T) {
	t.Chdir("../..")
	if err := run(".", registerPath, false, false); err != nil {
		t.Fatalf("run(.) = %v", err)
	}
}

// TestTheDeliveredInventoryIsWhatTheTreeDeclares is the first half of the
// reproducibility of this gate: the document on disk is exactly the document the
// tree implies, so a run that refreshes the inventory changes nothing.
func TestTheDeliveredInventoryIsWhatTheTreeDeclares(t *testing.T) {
	t.Chdir("../..")
	document, err := readRegister(".", registerPath)
	if err != nil {
		t.Fatalf("readRegister = %v", err)
	}
	refreshed := syncEntries(".", document)
	if len(refreshed) != len(document.Entries) {
		t.Fatalf("o registro em disco tem %d entrada(s) e a árvore implica %d: `-print-register` mudaria o documento",
			len(document.Entries), len(refreshed))
	}
	for index := range refreshed {
		one, other := refreshed[index], document.Entries[index]
		if componentKey(one.Kind, one.Name) != componentKey(other.Kind, other.Name) || one.Version != other.Version {
			t.Errorf("entrada %d: a árvore implica %s %q %q e o disco diz %s %q %q",
				index, one.Kind, one.Name, one.Version, other.Kind, other.Name, other.Version)
		}
	}
}

// TestTheDeliveredBillOfMaterialsMatchesTheTree is the second half: the bill of
// materials is derived, so the bytes on disk are the bytes the census and the
// register produce.
func TestTheDeliveredBillOfMaterialsMatchesTheTree(t *testing.T) {
	t.Chdir("../..")
	document, err := readRegister(".", registerPath)
	if err != nil {
		t.Fatalf("readRegister = %v", err)
	}
	derived, err := marshalDocument(buildSBOM(".", document))
	if err != nil {
		t.Fatalf("marshalDocument = %v", err)
	}
	raw, err := os.ReadFile(document.Paths.SBOM)
	if err != nil {
		t.Fatalf("read %s = %v", document.Paths.SBOM, err)
	}
	if kept := strings.TrimRight(string(raw), "\n"); kept != derived {
		t.Fatalf("`%s` não é a lista que a árvore implica: rode `$(GO) run ./tools/dependencyaudit -print-sbom`", document.Paths.SBOM)
	}
}

// TestTheDeliveredTreeMeasuresWhatTheRulesJudge is the antivacuity guard: a run
// whose census is empty answers green over nothing, and every count below is one
// a rule reads.
func TestTheDeliveredTreeMeasuresWhatTheRulesJudge(t *testing.T) {
	t.Chdir("../..")
	document, err := readRegister(".", registerPath)
	if err != nil {
		t.Fatalf("readRegister = %v", err)
	}
	measured, err := readCensus(".", document)
	if err != nil {
		t.Fatalf("readCensus = %v", err)
	}
	for _, counted := range []struct {
		name  string
		count int
	}{
		{"módulos", measured.Counts.Modules},
		{"módulos diretos", measured.Counts.Direct},
		{"módulos indiretos", measured.Counts.Indirect},
		{"pacotes npm", measured.Counts.Packages},
		{"manifestos", measured.Counts.Manifests},
		{"imagens", measured.Counts.Images},
		{"actions", measured.Counts.Actions},
		{"sítios de versão de ferramenta", measured.Counts.EvidenceSites},
		{"entradas", measured.Counts.Entries},
		{"linhas do catálogo", measured.Counts.StatementRows},
		{"imports do browser", measured.Counts.Imports},
	} {
		if counted.count == 0 {
			t.Errorf("a árvore entregue mediu 0 %s: a regra que depende dessa contagem não julga nada", counted.name)
		}
	}
	findings, counts, err := judge(".", document, measured)
	if err != nil {
		t.Fatalf("judge = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a árvore entregue tem %d achado(s): %s", len(findings), findings[0])
	}
	if counts.Approved != len(measured.Components) {
		t.Errorf("componentes aprovados = %d de %d: o registro e o censo discordam sobre o que existe",
			counts.Approved, len(measured.Components))
	}
	if counts.DigestPins != measured.Counts.ProductionImages {
		t.Errorf("digests = %d e referências de imagem em produção = %d: toda imagem que roda em produção pina os bytes",
			counts.DigestPins, measured.Counts.ProductionImages)
	}
	if counts.CommitPins != measured.Counts.Actions {
		t.Errorf("commits = %d e actions = %d: toda action é presa a um commit",
			counts.CommitPins, measured.Counts.Actions)
	}
	if counts.BareImports != 0 || measured.Counts.BareImports != 0 {
		t.Errorf("imports fora da árvore = %d: o browser deste projeto não executa terceiros", counts.BareImports)
	}
	if counts.SBOMComponents == 0 {
		t.Error("a lista de materiais tem 0 componente: o documento derivado não deriva nada")
	}
}

// TestEveryRuleIsProvenInBothDirections keeps the vocabulary and the fixtures
// from drifting apart.
func TestEveryRuleIsProvenInBothDirections(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range rules {
		if seen[entry.Name] {
			t.Errorf("a regra %s aparece duas vezes no vocabulário", entry.Name)
		}
		seen[entry.Name] = true
		if entry.Reason == "" {
			t.Errorf("a regra %s não diz por que quebrá-la custa algo", entry.Name)
		}
	}
	if len(rules) != len(families) || len(rules) != len(cleanFixtures) {
		t.Fatalf("o portão declara %d regra(s), %d fixture(s) recusada(s) e %d limpa(s): as três tabelas têm de andar juntas",
			len(rules), len(families), len(cleanFixtures))
	}
	proved := map[string]bool{}
	for _, entry := range families {
		if !auditkit.RuleKnown(rules, entry.Name) {
			t.Errorf("a fixture %s prova a regra %q, que não está no vocabulário", entry.Target, entry.Name)
		}
		proved[entry.Name] = true
		if entry.Reason == "" {
			t.Errorf("a família %s não diz o que ela recusa", entry.Name)
		}
	}
	for _, entry := range rules {
		if !proved[entry.Name] {
			t.Errorf("a regra %s não tem fixture que a recuse", entry.Name)
		}
	}
}

// TestEachRuleRefusesItsOwnFixtureSeparately names one rule per fixture, so that
// a fixture which started producing a different finding is a failure and not a
// silence.
func TestEachRuleRefusesItsOwnFixtureSeparately(t *testing.T) {
	t.Chdir("../..")
	if err := proveFamilies("."); err != nil {
		t.Fatalf("proveFamilies(.) = %v", err)
	}
}

// TestTheDeliveredRegisterRefusesItsOwnDefects drives the loader: every refusal
// below is a document that promised less than the judgments read into it.
func TestTheDeliveredRegisterRefusesItsOwnDefects(t *testing.T) {
	t.Chdir("../..")
	raw, err := os.ReadFile(registerPath)
	if err != nil {
		t.Fatalf("read %s = %v", registerPath, err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode %s = %v", registerPath, err)
	}
	for _, fixture := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"o esquema de outra versão", func(d map[string]any) { d["schema"] = float64(9) }},
		{"o catálogo de licenças sem caminho", func(d map[string]any) { d["statement"] = "" }},
		{"nenhuma classe", func(d map[string]any) { d["classes"] = []any{} }},
		{"o papel inventado", func(d map[string]any) { mutateFirstClass(d, func(c map[string]any) { c["role"] = "diretor" }) }},
		{"o pino inventado", func(d map[string]any) { mutateFirstClass(d, func(c map[string]any) { c["pin"] = "mais-ou-menos" }) }},
		{"a classe sem licença", func(d map[string]any) {
			mutateFirstClass(d, func(c map[string]any) { c["licenses"] = []any{} })
		}},
		{"a classe sem razão", func(d map[string]any) { mutateFirstClass(d, func(c map[string]any) { c["reason"] = "" }) }},
		{"a classe sem nome", func(d map[string]any) { mutateFirstClass(d, func(c map[string]any) { c["name"] = "" }) }},
		{"a classe duplicada", func(d map[string]any) {
			classes := d["classes"].([]any)
			d["classes"] = append(classes, classes[0])
		}},
		{"o tipo inventado", func(d map[string]any) { mutateFirstEntry(d, func(e map[string]any) { e["kind"] = "nuvem" }) }},
		{"a entrada sem nome", func(d map[string]any) { mutateFirstEntry(d, func(e map[string]any) { e["name"] = "" }) }},
		{"a classe que não existe", func(d map[string]any) { mutateFirstEntry(d, func(e map[string]any) { e["class"] = "inexistente" }) }},
		{"a evidência sem padrão", func(d map[string]any) {
			mutateFirstEntry(d, func(e map[string]any) {
				e["version_evidence"] = []any{map[string]any{"path": "Makefile"}}
			})
		}},
		{"o padrão que não compila", func(d map[string]any) {
			mutateFirstEntry(d, func(e map[string]any) {
				e["version_evidence"] = []any{map[string]any{"path": "Makefile", "regex": "("}}
			})
		}},
		{"o padrão sem grupo", func(d map[string]any) {
			mutateFirstEntry(d, func(e map[string]any) {
				e["version_evidence"] = []any{map[string]any{"path": "Makefile", "regex": "govulncheck"}}
			})
		}},
		{"nenhuma entrada", func(d map[string]any) { d["entries"] = []any{} }},
		{"nenhum caminho de manifesto", func(d map[string]any) {
			paths := d["paths"].(map[string]any)
			paths["manifests"] = []any{}
		}},
		{"o `go.mod` sem caminho", func(d map[string]any) {
			paths := d["paths"].(map[string]any)
			paths["go_mod"] = ""
		}},
		{"a chave desconhecida", func(d map[string]any) { d["extra"] = true }},
	} {
		mutated := map[string]any{}
		for key, value := range document {
			mutated[key] = value
		}
		fixture.mutate(mutated)
		var decoded register
		body, err := json.Marshal(mutated)
		if err != nil {
			t.Fatalf("%s: marshal = %v", fixture.name, err)
		}
		if err := decodeJSON(body, &decoded); err != nil {
			continue
		}
		if err := validateRegister(registerPath, decoded); err == nil {
			t.Errorf("%s: o registro foi aceito", fixture.name)
		}
	}
}

// TestTheDeliveredRegisterIsAccepted is the other direction of the loader, and
// it names the classes the tree needs: a class that disappears from the
// vocabulary would make its components unapprovable.
func TestTheDeliveredRegisterIsAccepted(t *testing.T) {
	t.Chdir("../..")
	document, err := readRegister(".", registerPath)
	if err != nil {
		t.Fatalf("readRegister(%s) = %v", registerPath, err)
	}
	for _, wanted := range []string{"runtime-library", "build-tooling", "tooling-isolated", "tooling-external", "container-image", "ci-action", "platform-service"} {
		if _, ok := document.classNamed(wanted); !ok {
			t.Errorf("o registro entregue não declara a classe %q", wanted)
		}
	}
	if len(document.Banned) < 20 {
		t.Errorf("a lista de nomes banidos tem %d entrada(s): a política do frontend é o zero de frameworks, e a lista é onde isso está escrito", len(document.Banned))
	}
	for _, banned := range []string{"react", "@vue/", "tailwindcss"} {
		if !contains(document.Banned, banned) {
			t.Errorf("a política não bane %q", banned)
		}
	}
	for _, kind := range []string{kindModule, kindNPM, kindTool, kindImage, kindAction, kindPlatform} {
		found := false
		for _, item := range document.Entries {
			if item.Kind == kind {
				found = true
			}
		}
		if !found {
			t.Errorf("o inventário entregue não tem componente do tipo %q", kind)
		}
	}
}

// TestThePinsAreReadTheWayTheClassesMean pins the shape a version has to have to
// be a pin: a range follows the vendor, and a build that follows the vendor is
// not the build someone reproduced.
func TestThePinsAreReadTheWayTheClassesMean(t *testing.T) {
	for _, fixture := range []struct {
		version string
		want    bool
	}{
		{"7.0.2", true},
		{"v5.11.0", true},
		{"18.4", true},
		{"v0.0.0-20240606120523-5a60cdf6a761", true},
		{"^7.0.2", false},
		{"~7.0.2", false},
		{"7.x", false},
		{">=7", false},
		{"latest", false},
		{"*", false},
		{"", false},
	} {
		if got := pinnedExactly(fixture.version); got != fixture.want {
			t.Errorf("pinnedExactly(%q) = %v, want %v", fixture.version, got, fixture.want)
		}
	}
	for _, fixture := range []struct {
		reference string
		want      bool
	}{
		{strings.Repeat("z", 40), false},
		{"3d3c42e5aac5ba805825da76410c181273ba90b1", true},
		{"v7.0.1", false},
		{"main", false},
		{"", false},
	} {
		if got := isCommitSHA(fixture.reference); got != fixture.want {
			t.Errorf("isCommitSHA(%q) = %v, want %v", fixture.reference, got, fixture.want)
		}
	}
}

// TestTheManifestsAreReadWhereTheyDeclare is the pin of the census: the module
// that arrives indirectly is a component like the direct one, and a package of
// the runtime block of the browser manifest is what the browser executes.
func TestTheManifestsAreReadWhereTheyDeclare(t *testing.T) {
	root, cleanup, err := materialize(fixture{
		Note: "o censo dos manifestos",
		Files: map[string]string{
			"go.mod":           "module example.com/probe\n\ngo 1.27.1\n\nrequire example.com/direct v1.0.0\n\nrequire (\n\texample.com/indirect v2.0.0 // indirect\n\texample.com/other v3.0.0\n)\n",
			"web/package.json": "{\n  \"name\": \"probe\",\n  \"devDependencies\": {\"typescript\": \"7.0.2\"},\n  \"dependencies\": {\"some-runtime\": \"1.0.0\"}\n}\n",
		},
	})
	if err != nil {
		t.Fatalf("materialize = %v", err)
	}
	defer cleanup()

	modules, err := goModules(root, "go.mod")
	if err != nil {
		t.Fatalf("goModules = %v", err)
	}
	if len(modules) != 3 {
		t.Fatalf("goModules leu %d módulo(s), want 3", len(modules))
	}
	indirect := 0
	for _, module := range modules {
		if module.Indirect {
			indirect++
		}
	}
	if indirect != 1 {
		t.Errorf("módulos indiretos = %d, want 1: a marca `// indirect` é o que distingue o que alguém escolheu do que chegou junto", indirect)
	}

	document := register{Paths: paths{Manifests: []string{"*/package.json"}, BrowserManifests: []string{"web/package.json"}}}
	packages, err := npmPackages(root, document)
	if err != nil {
		t.Fatalf("npmPackages = %v", err)
	}
	runtime := 0
	for _, pkg := range packages {
		if pkg.Runtime {
			runtime++
		}
	}
	if len(packages) != 2 || runtime != 1 {
		t.Errorf("pacotes = %d (runtime %d), want 2 (runtime 1)", len(packages), runtime)
	}
	runtimePackages, err := manifestRuntime(root, document)
	if err != nil {
		t.Fatalf("manifestRuntime = %v", err)
	}
	if len(runtimePackages) != 1 || runtimePackages[0].Name != "some-runtime" {
		t.Errorf("runtime do manifesto do browser = %v, want `some-runtime`", runtimePackages)
	}
}

// TestTheImageReferenceIgnoresStagesAndVariables pins the reader of the
// container references: a stage is not an image and a variable is not a pin.
func TestTheImageReferenceIgnoresStagesAndVariables(t *testing.T) {
	for _, fixture := range []struct {
		line   string
		stages []string
		want   string
	}{
		{"FROM node:24-bookworm-slim AS web", nil, "node:24-bookworm-slim"},
		{"FROM build", []string{"build"}, ""},
		{"FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm AS build", nil, "golang:1.27.1-bookworm"},
		{"    image: postgres:18.4", nil, "postgres:18.4"},
		{"    image: ${COMPOSE_ARENA_IMAGE:?define it}", nil, ""},
		{"RUN echo hello", nil, ""},
	} {
		stages := map[string]bool{}
		for _, stage := range fixture.stages {
			stages[stage] = true
		}
		got, ok := imageReference(fixture.line, stages)
		if !ok {
			got = ""
		}
		if got != fixture.want {
			t.Errorf("imageReference(%q) = %q, want %q", fixture.line, got, fixture.want)
		}
	}
	name, version, digest := splitImage("postgres:18.4@sha256:abc")
	if name != "postgres" || version != "18.4" || digest != "sha256:abc" {
		t.Errorf("splitImage = %q, %q, %q", name, version, digest)
	}
	if name, version, digest := splitImage("gcr.io/distroless/static-debian12:nonroot"); name != "gcr.io/distroless/static-debian12" || version != "nonroot" || digest != "" {
		t.Errorf("splitImage sem digest = %q, %q, %q", name, version, digest)
	}
}

// TestTheBrowserSpecifiersAreReadFromTheLine pins the reader of the imports: the
// line that leaves the tree has to be found wherever it is written.
func TestTheBrowserSpecifiersAreReadFromTheLine(t *testing.T) {
	for _, fixture := range []struct {
		line string
		want []string
	}{
		{"import { start } from \"./start\";", []string{"./start"}},
		{"import \"some-lib\";", []string{"some-lib"}},
		{"import type { Contract } from \"../contracts/generated.js\";", []string{"../contracts/generated.js"}},
		{"const local = 1;", nil},
	} {
		got := specifiersOf(fixture.line)
		if strings.Join(got, ",") != strings.Join(fixture.want, ",") {
			t.Errorf("specifiersOf(%q) = %v, want %v", fixture.line, got, fixture.want)
		}
	}
	if bannedMatch([]string{"react", "@vue/"}, "@vue/compiler-sfc") == "" {
		t.Error("o escopo banido não foi reconhecido: a proibição que se contorna escrevendo o escopo não é proibição")
	}
	if bannedMatch([]string{"react"}, "reactive-streams") != "" {
		t.Error("um nome que apenas começa com o banido foi recusado")
	}
}

// TestTheStatementRowsAreReadInBothDirections pins the reader of the catalogue:
// every row carries the label a human reads and the license it claims.
func TestTheStatementRowsAreReadInBothDirections(t *testing.T) {
	t.Chdir("../..")
	document, err := readRegister(".", registerPath)
	if err != nil {
		t.Fatalf("readRegister = %v", err)
	}
	rows, err := statementRows(".", document.Statement)
	if err != nil {
		t.Fatalf("statementRows = %v", err)
	}
	if len(rows) < len(document.Entries) {
		t.Errorf("o catálogo tem %d linha(s) e o registro %d entrada(s): cada aprovação aponta para a linha que a declara",
			len(rows), len(document.Entries))
	}
	for _, row := range rows {
		if row.Label == "" || row.License == "" {
			t.Errorf("a linha %d do catálogo não traz rótulo e licença: %q", row.Line, row.Text)
		}
	}
}

// TestTheBillOfMaterialsListsEachComponentOnce pins the shape of the derived
// document: a component used by six jobs is one component, and the files it was
// read from travel in the line.
func TestTheBillOfMaterialsListsEachComponentOnce(t *testing.T) {
	root, cleanup, err := materialize(fixture{
		Note: "a lista de materiais",
		Files: map[string]string{
			"go.mod":                  "module example.com/probe\n\ngo 1.27.1\n\nrequire example.com/direct v1.0.0\n",
			".github/workflows/a.yml": "jobs:\n  one:\n    steps:\n      - uses: example/action@1111111111111111111111111111111111111111\n",
			".github/workflows/b.yml": "jobs:\n  two:\n    steps:\n      - uses: example/action@1111111111111111111111111111111111111111\n",
			"web/package.json":        "{\"name\": \"probe\", \"private\": true, \"devDependencies\": {\"typescript\": \"7.0.2\"}}\n",
			"web/src/app.ts":          "import { start } from \"./start\";\n",
			"web/src/start.ts":        "export const start = () => 1;\n",
			"Dockerfile":              "FROM example.com/base@sha256:0000000000000000000000000000000000000000000000000000000000000001\n",
			"CATALOG.md":              "| D | C | O | F | L |\n| --- | --- | --- | --- | --- |\n| `example.com/direct` | runtime | app | prova | MIT |\n| `typescript` | build | web | prova | MIT |\n| `example/action` | ci | CI | prova | MIT |\n| `example.com/base` | infra | Dockerfile | prova | MIT |\n",
			"internal/app/app.go":     "package app\n\nimport _ \"example.com/direct\"\n",
		},
	})
	if err != nil {
		t.Fatalf("materialize = %v", err)
	}
	defer cleanup()
	document := register{
		Schema: registerSchemaVersion, Statement: "CATALOG.md",
		Paths: paths{
			GoMod: "go.mod", Manifests: []string{"*/package.json"}, BrowserManifests: []string{"web/package.json"},
			Images: []string{"Dockerfile"}, ProductionImages: []string{"Dockerfile"},
			Workflows: []string{".github/workflows/*.yml"}, Sources: []string{"web/src"}, SBOM: "sbom.json",
		},
	}
	built := buildSBOM(root, document)
	seen := map[string]bool{}
	files := map[string]bool{}
	for _, line := range built.Components {
		key := sbomKey(line)
		if seen[key] {
			t.Errorf("a lista de materiais repete o componente %q: ela é o conjunto do que se entrega e não a contagem das menções", key)
		}
		seen[key] = true
		if len(line.Files) == 0 {
			t.Errorf("a linha %q não diz de onde foi lida", key)
		}
		for _, file := range line.Files {
			files[file] = true
		}
	}
	for _, wanted := range []string{".github/workflows/a.yml", ".github/workflows/b.yml"} {
		if !files[wanted] {
			t.Errorf("a linha da action não registrou o arquivo %s: as menções viajam na linha", wanted)
		}
	}
	if len(built.Components) != 4 {
		t.Errorf("a lista tem %d componente(s), want 4: o módulo, o pacote, a action e a imagem", len(built.Components))
	}
}

// TestTheFixtureRefusesToDescribeNothing keeps the fixtures honest: a document
// that proves nothing is refused, and a path that escapes the tree is refused
// before it is written.
func TestTheFixtureRefusesToDescribeNothing(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "tree.json")
	if err := os.WriteFile(path, []byte(`{"note":"vazia","register":{"schema":1},"files":{}}`), 0o644); err != nil {
		t.Fatalf("write = %v", err)
	}
	if _, err := readFixture(path); err == nil {
		t.Error("a fixture sem arquivo foi aceita")
	}
	if err := os.WriteFile(path, []byte(`{"note":"escapa","register":{"schema":1},"files":{"../fora.txt":"x"}}`), 0o644); err != nil {
		t.Fatalf("write = %v", err)
	}
	document, err := readFixture(path)
	if err != nil {
		t.Fatalf("readFixture = %v", err)
	}
	if _, cleanup, err := materialize(document); err == nil {
		cleanup()
		t.Error("a fixture que escreve fora da própria árvore foi materializada")
	}
}

// mutateFirstClass applies a mutation to the first class of the decoded
// register.
func mutateFirstClass(document map[string]any, mutate func(map[string]any)) {
	classes := document["classes"].([]any)
	mutate(classes[0].(map[string]any))
}

// mutateFirstEntry applies a mutation to the first entry of the decoded
// register.
func mutateFirstEntry(document map[string]any, mutate func(map[string]any)) {
	entries := document["entries"].([]any)
	mutate(entries[0].(map[string]any))
}
