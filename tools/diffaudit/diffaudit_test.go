package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// TestGateAcceptsTheDeliveredRange runs the delivered entry point over the
// delivered change — the phase branch against the branch it targets. It is the
// ratchet of this task: the range brings new production files with the test of
// their area, no migration without the upgrade proof, no route without the
// contract, no generated artifact without its pair, every reference the catalog
// declares resolving and no bypass in any message.
func TestGateAcceptsTheDeliveredRange(t *testing.T) {
	t.Chdir("../..")
	if err := run(".", "origin/main", "HEAD", "", policyPath, false); err != nil {
		t.Fatalf("run(origin/main..HEAD) = %v", err)
	}
}

// TestTheDeliveredRangeMeasuresWhatTheRulesJudge is the antivacuity guard: a run
// whose corpus is empty answers green over nothing, and the classification is
// what makes the difference between judging a change and judging none.
func TestTheDeliveredRangeMeasuresWhatTheRulesJudge(t *testing.T) {
	t.Chdir("../..")
	pol, err := readPolicy(".", policyPath)
	if err != nil {
		t.Fatalf("readPolicy = %v", err)
	}
	register, err := readProvenance(".")
	if err != nil {
		t.Fatalf("readProvenance = %v", err)
	}
	document, err := buildChange(".", "origin/main", "HEAD")
	if err != nil {
		t.Fatalf("buildChange = %v", err)
	}
	st, err := newState(".", document, pol, register)
	if err != nil {
		t.Fatalf("newState = %v", err)
	}
	findings, result, err := judge(document, st)
	if err != nil {
		t.Fatalf("judge = %v", err)
	}
	for _, measured := range []struct {
		name  string
		count int
	}{
		{"commits", result.Commits},
		{"arquivos classificados", result.Classified},
		{"arquivos novos de produção", result.AddedProduction},
		{"referências declaradas no catálogo", result.DeclaredRefs},
		{"regras Q0 na tabela", result.Q0Rules},
		{"demandas avaliadas", result.Judgments},
	} {
		if measured.count == 0 {
			t.Errorf("a faixa entregue mediu 0 %s: a regra que depende dessa contagem não julga nada", measured.name)
		}
	}
	if result.Unclassified != 0 {
		t.Errorf("a faixa entregue tem %d arquivo(s) que a política não classifica, e a política entregue tem de cobrir o diff entregue", result.Unclassified)
	}
	if result.AddedProductionWithTest != result.AddedProduction {
		t.Errorf("arquivos novos de produção com teste na área = %d de %d: o trinco da árvore deixou de ser o trinco", result.AddedProductionWithTest, result.AddedProduction)
	}
	if len(findings) != 0 {
		t.Errorf("a faixa entregue tem %d achado(s): %v", len(findings), findings[0])
	}
	if base := document.Base; base == "" || base == document.Head {
		t.Errorf("a base da faixa é %q e a cabeça é %q: o construtor não resolveu a base", base, document.Head)
	}
}

// TestTheFormatExemptionReadsTheTextNotOnlyTheClass is the pin of the bug this
// gate found in itself: comparing the class of a token and not its text would
// call a `%v` to `%w` change — or a rename — formatting, and the exemption would
// hide exactly the change it was asked about.
func TestTheFormatExemptionReadsTheTextNotOnlyTheClass(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		before string
		after  string
		want   bool
	}{
		{
			"um comentário a mais",
			"package probe\n\nfunc run() error {\n\treturn nil\n}\n",
			"package probe\n\n// run does nothing yet.\nfunc run() error {\n\treturn nil\n}\n",
			true,
		},
		{
			"a indentação trocada",
			"package probe\n\nfunc run() error {\n\treturn nil\n}\n",
			"package probe\n\nfunc   run( )   error {\n\treturn nil\n}\n",
			true,
		},
		{
			"o verbo do formato trocado",
			"package probe\n\nfunc run(err error) error {\n\treturn fmt.Errorf(\"failed: %v\", err)\n}\n",
			"package probe\n\nfunc run(err error) error {\n\treturn fmt.Errorf(\"failed: %w\", err)\n}\n",
			false,
		},
		{
			"um identificador renomeado",
			"package probe\n\nfunc run() int {\n\tvalue := 1\n\treturn value\n}\n",
			"package probe\n\nfunc run() int {\n\tother := 1\n\treturn other\n}\n",
			false,
		},
		{
			"um número trocado",
			"package probe\n\nfunc run() int {\n\treturn 10\n}\n",
			"package probe\n\nfunc run() int {\n\treturn 20\n}\n",
			false,
		},
		{
			"a linha que sumiu",
			"package probe\n\nfunc run() error {\n\tcheck()\n\treturn nil\n}\n",
			"package probe\n\nfunc run() error {\n\treturn nil\n}\n",
			false,
		},
	} {
		if got := formatOnly(fixture.before, fixture.after); got != fixture.want {
			t.Errorf("%s: formatOnly = %v, want %v", fixture.name, got, fixture.want)
		}
	}
	if formatOnly("", "package probe\n") {
		t.Error("a ausência de um dos lados foi lida como formato: sem o lado anterior o arquivo é mudança de comportamento")
	}
}

// TestTheRouteSetIsComparedAndNotTheFile holds the boundary of the route demand:
// a route file whose body or comment changed without moving the route set is not
// a refusal, and a route added or removed is.
func TestTheRouteSetIsComparedAndNotTheFile(t *testing.T) {
	base := "package http\n\nfunc Routes() []Route {\n\treturn []Route{\n\t\t{Method: \"GET\", Path: \"/api/v1/arenas\"},\n\t}\n}\n"
	comment := strings.Replace(base, "package http\n", "package http\n\n// Routes is the route table.\n", 1)
	added := strings.Replace(base, "\t\t{Method: \"GET\", Path: \"/api/v1/arenas\"},\n",
		"\t\t{Method: \"GET\", Path: \"/api/v1/arenas\"},\n\t\t{Method: \"POST\", Path: \"/api/v1/arenas\"},\n", 1)
	removed := "package http\n\nfunc Routes() []Route {\n\treturn []Route{}\n}\n"

	for _, fixture := range []struct {
		name          string
		record        fileRecord
		wantAdded     int
		wantRemoved   int
		notFormatOnly bool
	}{
		{"o comentário a mais", fileRecord{Status: modifiedFile, Before: base, After: comment}, 0, 0, false},
		{"a rota acrescentada", fileRecord{Status: modifiedFile, Before: base, After: added}, 1, 0, true},
		{"a rota removida", fileRecord{Status: modifiedFile, Before: base, After: removed}, 0, 1, true},
		{"o arquivo novo com rota", fileRecord{Status: newFile, After: base}, 1, 0, true},
	} {
		gotAdded, gotRemoved, err := routeSetDiff(fixture.record)
		if err != nil {
			t.Fatalf("%s: routeSetDiff = %v", fixture.name, err)
		}
		if len(gotAdded) != fixture.wantAdded || len(gotRemoved) != fixture.wantRemoved {
			t.Errorf("%s: rotas = +%v -%v, want +%d -%d", fixture.name, gotAdded, gotRemoved, fixture.wantAdded, fixture.wantRemoved)
		}
		if fixture.record.Before != "" && fixture.record.After != "" {
			if got := !formatOnly(fixture.record.Before, fixture.record.After); got != fixture.notFormatOnly {
				t.Errorf("%s: a comparação de tokens discorda da expectativa", fixture.name)
			}
		}
	}
	if _, _, err := routesIn("package http\n\nfunc Routes() {\n"); err == nil {
		t.Error("uma fonte que não parseia foi aceita: a leitura das rotas tem de recusar o que não lê")
	}
}

// TestThePairIsDemandedInBothDirections drives the provenance demand: the
// artifact without its input, the input without the artifact, and the pair.
func TestThePairIsDemandedInBothDirections(t *testing.T) {
	t.Chdir("../..")
	pol, err := readPolicy(".", policyPath)
	if err != nil {
		t.Fatalf("readPolicy = %v", err)
	}
	register, err := readProvenance(".")
	if err != nil {
		t.Fatalf("readProvenance = %v", err)
	}
	for _, fixture := range []struct {
		name  string
		paths []string
		want  int
	}{
		{"o artefato sozinho", []string{"web/src/contracts/generated.ts"}, 1},
		{"o insumo sozinho", []string{"api/openapi.json"}, 1},
		{
			"o par",
			[]string{"api/openapi.json", "web/src/contracts/generated.ts"},
			0,
		},
	} {
		files := []fileRecord{}
		for _, path := range fixture.paths {
			files = append(files, fileRecord{Status: modifiedFile, Path: path})
		}
		document := changeDocument{Schema: changeSchemaVersion, Base: "base", Head: "head",
			Commits: []commitRecord{{SHA: "aaaaaaa1", Message: "chore(contracts): move the pair", Files: files}}}
		st, err := newState(".", document, pol, register)
		if err != nil {
			t.Fatalf("%s: newState = %v", fixture.name, err)
		}
		findings, _, err := judge(document, st)
		if err != nil {
			t.Fatalf("%s: judge = %v", fixture.name, err)
		}
		count := 0
		for _, finding := range findings {
			if finding.Rule == RuleProvenanceWithoutPair {
				count++
			}
		}
		if count != fixture.want {
			t.Errorf("%s: achados de %s = %d, want %d (%v)", fixture.name, RuleProvenanceWithoutPair, count, fixture.want, findings)
		}
	}
}

// TestTheDeclarationResolutionReadsTheChangeSide pins the tree-wide half: the
// evidence a rule declares disappears when the change removes the test, and the
// judgment reads the side the change brings and not the file on disk.
func TestTheDeclarationResolutionReadsTheChangeSide(t *testing.T) {
	t.Chdir("../..")
	pol, err := readPolicy(".", policyPath)
	if err != nil {
		t.Fatalf("readPolicy = %v", err)
	}
	register, err := readProvenance(".")
	if err != nil {
		t.Fatalf("readProvenance = %v", err)
	}
	const declared = "internal/wallet/adapters/postgres/repository_test.go"
	catalogText := `{"rules":[{"id":"QUAL-REQ-WAL-04","risk":"Q0","tests":{"positive":["` + declared + `::TestRepository_ApplyDebitIsIdempotent"]}}]}`
	document := changeDocument{Schema: changeSchemaVersion, Base: "base", Head: "head", CatalogHead: catalogText,
		Commits: []commitRecord{{SHA: "aaaaaaa1", Message: "test(wallet): rewrite the evidence", Files: []fileRecord{
			{Status: modifiedFile, Path: declared, Before: "func TestRepository_ApplyDebitIsIdempotent(t *testing.T) {}\n",
				After: "func TestRepository_ApplyDebitTwice(t *testing.T) {}\n"},
		}}}}
	st, err := newState(".", document, pol, register)
	if err != nil {
		t.Fatalf("newState = %v", err)
	}
	findings, _, err := judge(document, st)
	if err != nil {
		t.Fatalf("judge = %v", err)
	}
	found := false
	for _, finding := range findings {
		if finding.Rule == RuleDeclaredTestMissing {
			found = true
		}
	}
	if !found {
		t.Errorf("a referência declarada que o lado novo da mudança não declara não foi recusada: %v", findings)
	}
}

// TestARenamedFileIsJudgedByItsNewSide is the rename half of the task's minimum
// validation. A rename travels as one record with two paths, and the judgments
// read the new one: the renamed production file is an **edit** of its area and
// not a new file no one proved, and the renamed test keeps satisfying the demand
// of the area it moved into — including when it moves out of the area that owed
// it, which is the direction that proves the area is read from the new path and
// not from the origin.
func TestARenamedFileIsJudgedByItsNewSide(t *testing.T) {
	t.Chdir("../..")
	for _, fixture := range []struct {
		name      string
		files     []fileRecord
		wantAdded int
		wantFound int
	}{
		{
			"a renomeação do arquivo de produção",
			[]fileRecord{{Status: renamedFile, From: "internal/wallet/application/debit.go", Path: "internal/wallet/application/debitusecase.go"}},
			0, 0,
		},
		{
			"a renomeação do teste junto do arquivo novo",
			[]fileRecord{
				{Status: newFile, Path: "internal/wallet/application/newthing.go"},
				{Status: renamedFile, From: "internal/wallet/application/debit_test.go", Path: "internal/wallet/application/debitusecase_test.go"},
			},
			1, 0,
		},
		{
			"a renomeação do teste para fora da área",
			[]fileRecord{
				{Status: newFile, Path: "internal/wallet/application/newthing.go"},
				{Status: renamedFile, From: "internal/wallet/application/debit_test.go", Path: "internal/identity/application/debit_test.go"},
			},
			1, 1,
		},
	} {
		document := changeDocument{Schema: changeSchemaVersion, Base: "base", Head: "head",
			Commits: []commitRecord{{SHA: "aaaaaaa1", Message: "refactor(wallet): move the debit use case", Files: fixture.files}}}
		findings, result, err := judge(document, mustState(t, document))
		if err != nil {
			t.Fatalf("%s: judge = %v", fixture.name, err)
		}
		if result.AddedProduction != fixture.wantAdded {
			t.Errorf("%s: arquivos novos de produção = %d, want %d (a renomeação não é arquivo novo)", fixture.name, result.AddedProduction, fixture.wantAdded)
		}
		if len(findings) != fixture.wantFound {
			t.Errorf("%s: achados = %v, want %d", fixture.name, findings, fixture.wantFound)
		}
	}
}

// TestTheMergeCommitIsJudgedForItsMessageOnly states the boundary of the merge:
// the message is read, and the files of a merge are not, because the diff of a
// merge against its first parent is the content of the other side.
func TestTheMergeCommitIsJudgedForItsMessageOnly(t *testing.T) {
	document := changeDocument{Schema: changeSchemaVersion, Base: "base", Head: "head",
		Commits: []commitRecord{{
			SHA: "aaaaaaa1", Merge: true,
			Message: "merge(main): integrate the cadence [no-evidence]",
			Files:   []fileRecord{{Status: "A", Path: "internal/wallet/application/p23t08guard.go"}},
		}}}
	findings, result, err := judge(document, mustState(t, document))
	if err != nil {
		t.Fatalf("judge = %v", err)
	}
	if len(findings) != 1 || findings[0].Rule != RuleBypassMarker {
		t.Fatalf("achados = %v, want um achado de %s", findings, RuleBypassMarker)
	}
	if result.Merges != 1 || result.AddedProduction != 0 {
		t.Errorf("merges = %d e arquivos novos de produção = %d: o merge foi julgado pelos arquivos", result.Merges, result.AddedProduction)
	}
}

// TestTheChangeDocumentRefusesToJudgeNothing keeps the corpus honest: a document
// without a commit is refused by the reader, which is what stops a gate that lost
// its input from answering green over nothing.
func TestTheChangeDocumentRefusesToJudgeNothing(t *testing.T) {
	directory := t.TempDir()
	for name, body := range map[string]string{
		"empty.json":   `{"schema":1,"base":"b","head":"h","commits":[]}`,
		"version.json": `{"schema":9,"base":"b","head":"h","commits":[{"sha":"a","message":"m"}]}`,
		"unknown.json": `{"schema":1,"base":"b","head":"h","commits":[{"sha":"a","message":"m"}],"extra":true}`,
		"broken.json":  `{"schema":1,`,
	} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := readChange(path); err == nil {
			t.Errorf("%s: o documento foi aceito", name)
		}
	}
}

// TestThePolicyRefusesItsOwnDefects drives every refusal of the policy loader: a
// document that does not say what it means would make every judgment below it a
// guess, so the loader refuses the classes, the vocabulary and the matchers that
// promise less than they read.
func TestThePolicyRefusesItsOwnDefects(t *testing.T) {
	t.Chdir("../..")
	raw, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("read %s: %v", policyPath, err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode %s: %v", policyPath, err)
	}
	for _, fixture := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"a classe sem matcher", func(d map[string]any) {
			classes := d["classes"].([]any)
			classes = append(classes, map[string]any{"name": "vazia", "role": "exempt", "reason": "promete e não casa"})
			d["classes"] = classes
		}},
		{"o papel inventado", func(d map[string]any) { mutateFirstClass(d, func(c map[string]any) { c["role"] = "portao" }) }},
		{"a demanda inventada", func(d map[string]any) {
			mutateFirstClass(d, func(c map[string]any) { c["demands"] = []any{map[string]any{"kind": "confia"}} })
		}},
		{"a demanda sem estado válido", func(d map[string]any) {
			mutateFirstClass(d, func(c map[string]any) {
				c["demands"] = []any{map[string]any{"kind": demandSameAreaTest, "when": "carimbado"}}
			})
		}},
		{"a classe sem nome", func(d map[string]any) { mutateFirstClass(d, func(c map[string]any) { delete(c, "name") }) }},
		{"a classe sem razão", func(d map[string]any) { mutateFirstClass(d, func(c map[string]any) { delete(c, "reason") }) }},
		{"a classe duplicada", func(d map[string]any) {
			classes := d["classes"].([]any)
			d["classes"] = append(classes, classes[0])
		}},
		{"o vocabulário de bypass vazio", func(d map[string]any) { d["bypass"] = map[string]any{"note": "", "tokens": []any{}} }},
		{"a área sem prefixo", func(d map[string]any) {
			areas := d["areas"].(map[string]any)
			areas["prefixes"] = []any{}
		}},
		{"o par nominal e adversarial vazio", func(d map[string]any) {
			kinds := d["kinds"].(map[string]any)
			kinds["adversarial"] = []any{}
		}},
		{"a rota sem marcador", func(d map[string]any) {
			route := d["route"].(map[string]any)
			route["marker"] = ""
		}},
		{"o contrato sem família", func(d map[string]any) {
			contract := d["contract"].(map[string]any)
			contract["family"] = ""
		}},
		{"a versão do esquema", func(d map[string]any) { d["schema"] = float64(9) }},
		{"a chave desconhecida", func(d map[string]any) { d["extra"] = true }},
		{"nenhuma classe", func(d map[string]any) { d["classes"] = []any{} }},
	} {
		mutated := map[string]any{}
		for key, value := range document {
			mutated[key] = value
		}
		fixture.mutate(mutated)
		body, err := json.Marshal(mutated)
		if err != nil {
			t.Fatalf("%s: marshal: %v", fixture.name, err)
		}
		path := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("%s: write: %v", fixture.name, err)
		}
		if _, err := readPolicy(".", path); err == nil {
			t.Errorf("%s: a política foi aceita", fixture.name)
		}
	}
}

// TestTheDeliveredPolicyIsAccepted is the other direction of the loader: the
// document the tree ships stands, and every class it declares is reachable in the
// order it declares them.
func TestTheDeliveredPolicyIsAccepted(t *testing.T) {
	t.Chdir("../..")
	document, err := readPolicy(".", policyPath)
	if err != nil {
		t.Fatalf("readPolicy(%s) = %v", policyPath, err)
	}
	if len(document.Classes) < 8 {
		t.Errorf("a política entregue declara %d classe(s): a de produção, a de evidência, a de gerado, a de rota, a de governança, a de configuração, a de documento e a de formato", len(document.Classes))
	}
	seen := map[string]bool{}
	for _, entry := range document.Classes {
		if seen[entry.Name] {
			t.Errorf("a classe %s aparece duas vezes", entry.Name)
		}
		seen[entry.Name] = true
	}
	for _, wanted := range []string{"generated", "documentation", "governance", "configuration", "evidence", "migration", "contract", "route", "tooling", "production"} {
		if !seen[wanted] {
			t.Errorf("a política entregue não declara a classe %q", wanted)
		}
	}
	if !seen[document.Route.Registry] && document.Route.Registry == "" {
		t.Error("a política não declara o registrador de rotas")
	}
}

// TestTheAreasFollowThePolicyPrefixes pins the comparison the demand makes: two
// files are in the same area when the policy derives the same area for them.
func TestTheAreasFollowThePolicyPrefixes(t *testing.T) {
	t.Chdir("../..")
	document, err := readPolicy(".", policyPath)
	if err != nil {
		t.Fatalf("readPolicy = %v", err)
	}
	for _, fixture := range []struct {
		path string
		want string
	}{
		{"internal/wallet/application/debit.go", "internal/wallet"},
		{"internal/wallet/adapters/postgres/repository_test.go", "internal/wallet"},
		{"tools/testaudit/scan.go", "tools/testaudit"},
		{"cmd/arena/main.go", "cmd/arena"},
		{"web/src/i18n/generated.ts", "web/src/i18n"},
		{"README.md", "README.md"},
	} {
		if got := areaOf(fixture.path, document); got != fixture.want {
			t.Errorf("areaOf(%q) = %q, want %q", fixture.path, got, fixture.want)
		}
	}
}

// TestEveryRuleIsProvenInBothDirections keeps the vocabulary and the fixtures
// from drifting apart: a rule without a fixture that refuses and a fixture that
// accepts is a rule nobody would notice had stopped working.
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
	}
	for _, entry := range cleanFixtures {
		if !proved[entry.Name] {
			t.Errorf("a fixture limpa %s aceita a regra %q, que nenhuma fixture recusa", entry.Target, entry.Name)
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

// mustState builds the state of a document with the delivered policy and
// register, for the tests that drive one judgment directly.
func mustState(t *testing.T, document changeDocument) state {
	t.Helper()
	// The root is entered only when the policy is not already reachable: a test
	// that drives several judgments calls this more than once, and `t.Chdir` twice
	// in a row leaves the root for the parent of the root.
	if _, err := os.Stat(policyPath); err != nil {
		t.Chdir("../..")
	}
	pol, err := readPolicy(".", policyPath)
	if err != nil {
		t.Fatalf("readPolicy = %v", err)
	}
	register, err := readProvenance(".")
	if err != nil {
		t.Fatalf("readProvenance = %v", err)
	}
	built, err := newState(".", document, pol, register)
	if err != nil {
		t.Fatalf("newState = %v", err)
	}
	return built
}

// mutateFirstClass applies a mutation to the first class of the decoded policy.
func mutateFirstClass(document map[string]any, mutate func(map[string]any)) {
	classes := document["classes"].([]any)
	mutate(classes[0].(map[string]any))
}
