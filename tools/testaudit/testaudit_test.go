package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// TestRuleFamiliesRefuseTheirFixtures drives the proof the gate itself runs
// before it judges anything: every rule, and the fixture it must refuse.
func TestRuleFamiliesRefuseTheirFixtures(t *testing.T) {
	t.Chdir("../..")
	if err := proveFamilies(); err != nil {
		t.Fatalf("proveFamilies() = %v", err)
	}
}

// TestGateAcceptsTheDeliveredTree runs the delivered entry point over the
// delivered tree. It is the ratchet of this task: the 360 test files of the
// repository run, assert and wait for the conditions they affirm, and the five
// exceptions that remain are registered with an owner and an expiry — the day a
// rule finds something new, this test and the gate fail together.
func TestGateAcceptsTheDeliveredTree(t *testing.T) {
	t.Chdir("../..")
	if err := run(".", waiverPath); err != nil {
		t.Fatalf("run(.) = %v", err)
	}
}

// TestDeliveredTreeMeasuresWhatTheRulesJudge is the antivacuity guard: every rule
// works from a count, and a run whose measurement says zero while the vocabulary
// claims otherwise is a report nobody should believe. The corpus of this gate is
// the test files the other gates of the phase leave out, and a gate that judged
// none of them would answer green over nothing.
func TestDeliveredTreeMeasuresWhatTheRulesJudge(t *testing.T) {
	t.Chdir("../..")
	findings, result, _, err := scan(".", auditkit.SkippedDirectories)
	if err != nil {
		t.Fatalf("scan(.) = %v", err)
	}
	for _, entry := range []struct {
		name  string
		count int
	}{
		{"arquivo(s) de teste", result.Files},
		{"teste(s)", result.Tests},
		{"asserção(ões)", result.Assertions},
		{"fixture(s) sob testdata", result.Fixtures},
	} {
		if entry.count == 0 {
			t.Errorf("a árvore mediu 0 %s: a regra que depende dessa contagem não julga nada", entry.name)
		}
	}
	if result.Tests < result.Files {
		t.Errorf("testes = %d para %d arquivo(s): a leitura deixou de achar as funções de teste", result.Tests, result.Files)
	}
	// The boundary of the `vacuous-test` rule is printed, and it has to stay
	// small: a tree where most tests delegated their assertions would be a tree
	// whose subject is a harness.
	if result.NoAssertionOfItsOwn > result.Tests/10 {
		t.Errorf("teste(s) sem asserção própria = %d de %d: a fronteira declarada deixou de ser uma fronteira",
			result.NoAssertionOfItsOwn, result.Tests)
	}
	statement := unenforced()
	for _, named := range []string{"type-checker", "time.Sleep", "testdata"} {
		if !strings.Contains(statement, named) {
			t.Errorf("a lacuna declarada não nomeia %q: %q", named, statement)
		}
	}
	_ = findings
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
	for _, entry := range families {
		directory := fixtureRoot + "/" + entry.Target
		findings, _, _, err := scan(directory, nil)
		if err != nil {
			t.Fatalf("scan(%s) = %v", directory, err)
		}
		found := false
		for _, finding := range findings {
			if finding.Rule == entry.Name {
				found = true
			}
		}
		if !found {
			t.Errorf("a fixture %s não produz o achado %q: %v", directory, entry.Name, findings)
		}
	}
}

// TestTheTestNameRuleFollowsTheToolchain pins the vocabulary of the corpus: a
// function is a test when the `go test` tool would run it, which is the rule the
// gate has to agree with or it would judge the wrong functions.
func TestTheTestNameRuleFollowsTheToolchain(t *testing.T) {
	for name, wanted := range map[string]bool{
		"TestSomething":      true,
		"TestX":              true,
		"Test1":              false, // the second character has to be an upper-case letter
		"Testing":            false,
		"BenchmarkSomething": true,
		"FuzzSomething":      true,
		"ExampleSomething":   true,
		"helperSomething":    false,
		"Test":               false,
	} {
		if got := isTestName(name); got != wanted {
			t.Errorf("isTestName(%q) = %v, want %v", name, got, wanted)
		}
	}
}

// TestTheEffectRuleReadsWhatCanFail pins the three shapes the rule accepts and
// the one it refuses: a call of any kind is an effect (it can fail, block or
// panic), a compile-time assertion is an effect (the build fails), and a body
// that only declares variables is not.
func TestTheEffectRuleReadsWhatCanFail(t *testing.T) {
	for _, fixture := range []struct {
		name string
		body string
		want bool
	}{
		{"a body that only declares", "func TestX(t *testing.T) {\n\tvalue := 2 + 2\n\t_ = value\n}", false},
		{"a body that calls", "func TestX(t *testing.T) {\n\tdo()\n}", true},
		{"a body that asserts", "func TestX(t *testing.T) {\n\tif bad() {\n\t\tt.Errorf(\"bad\")\n\t}\n}", true},
		{"a body that proves at compile time", "func TestX(t *testing.T) {\n\tvar _ Port = (*impl)(nil)\n}", true},
		{"a body that hands the test over", "func TestX(t *testing.T) {\n\tcontract.Run(t, newHarness)\n}", true},
		{"a body that panics", "func TestX(t *testing.T) {\n\tpanic(\"no\")\n}", true},
	} {
		state := parseTest(t, fixture.body)
		tests := testFunctions(state.parsed)
		if len(tests) != 1 {
			t.Fatalf("a fixture %q não declara uma função de teste", fixture.name)
		}
		if got := effectOf(state, tests[0]).observable; got != fixture.want {
			t.Errorf("%s: observable = %v, want %v", fixture.name, got, fixture.want)
		}
	}
}

// TestTheErrorRuleLeavesClosuresAlone holds the boundary of the error rule: a
// guard inside a closure collects the failure for the test to assert, a guard
// whose `else` fails has answered, a guard that exits the process has answered,
// and a guard that only logs has not.
func TestTheErrorRuleLeavesClosuresAlone(t *testing.T) {
	for _, fixture := range []struct {
		name        string
		body        string
		wantFinding bool
	}{
		{"a guard that only logs", "func TestX(t *testing.T) {\n\tif err := do(); err != nil {\n\t\tt.Logf(\"failed: %v\", err)\n\t}\n}", true},
		{"a guard that fails", "func TestX(t *testing.T) {\n\tif err := do(); err != nil {\n\t\tt.Fatalf(\"failed: %v\", err)\n\t}\n}", false},
		{"a guard that declares the case away", "func TestX(t *testing.T) {\n\tfor _, row := range rows() {\n\t\tif _, err := prepare(row); err != nil {\n\t\t\tcontinue\n\t\t}\n\t}\n}", false},
		{"a guard whose else fails", "func TestX(t *testing.T) {\n\tif _, err := stat(); err == nil {\n\t\tuse()\n\t} else {\n\t\tt.Fatalf(\"failed: %v\", err)\n\t}\n}", false},
		{"a guard that exits the process", "func TestX(t *testing.T) {\n\tif err := do(); err != nil {\n\t\tfmt.Printf(\"failed: %v\\n\", err)\n\t\tos.Exit(4)\n\t}\n}", false},
		{"a guard inside a closure", "func TestX(t *testing.T) {\n\tgo func() {\n\t\tif _, err := do(); err != nil {\n\t\t\tcollected = append(collected, err)\n\t\t}\n\t}()\n}", false},
	} {
		state := parseTest(t, fixture.body)
		tests := testFunctions(state.parsed)
		findings := judgeIgnoredError(state, tests)
		if got := len(findings) > 0; got != fixture.wantFinding {
			t.Errorf("%s: achado = %v, want %v (%v)", fixture.name, got, fixture.wantFinding, findings)
		}
	}
}

// TestTheWaiverRulesRefuseWhatThePhaseForbids drives every refusal of the
// exception register: the phase asks for a non-critical exception with an
// expiry, and the register refuses everything else by name.
func TestTheWaiverRulesRefuseWhatThePhaseForbids(t *testing.T) {
	t.Chdir("../..")
	today := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	base := waiver{
		ID: "TST-001", Rule: RuleSkipped, Site: "tools/testaudit/testaudit_test.go::TestGateAcceptsTheDeliveredTree",
		Risk: riskHigh, Owner: "quality",
		Justification: "a razão com tamanho suficiente para ser julgada",
		Compensation:  "tools/testaudit/testaudit_test.go::TestRuleFamiliesRefuseTheirFixtures",
		Created:       "2026-09-24", Expires: "2026-12-31",
	}
	known := map[string]bool{"tools/testaudit/testaudit_test.go": true}
	for _, cenario := range []struct {
		name    string
		mutate  func(*waiver)
		refused bool
	}{
		{"a entrada legal", func(w *waiver) {}, false},
		{"a classe crítica", func(w *waiver) { w.Risk = forbiddenRisk }, true},
		{"uma classe fora do vocabulário", func(w *waiver) { w.Risk = "Q9" }, true},
		{"a entrada expirada", func(w *waiver) { w.Expires = "2026-09-23" }, true},
		{"a janela que corre para trás", func(w *waiver) { w.Expires = "2026-09-01" }, true},
		{"a data fora do formato", func(w *waiver) { w.Expires = "31/12/2026" }, true},
		{"o dono em branco", func(w *waiver) { w.Owner = " " }, true},
		{"o dono pessoal", func(w *waiver) { w.Owner = "alguem@example.com" }, true},
		{"a razão curta", func(w *waiver) { w.Justification = "porque sim" }, true},
		{"a regra que não existe", func(w *waiver) { w.Rule = "regra-inventada" }, true},
		{"o lugar que não é um arquivo de teste", func(w *waiver) { w.Site = "docs/README.md" }, true},
		{"o teste que não existe", func(w *waiver) { w.Site = "tools/testaudit/testaudit_test.go::TestNobodyWroteThis" }, true},
		{"a compensação que não resolve", func(w *waiver) { w.Compensation = "tools/testaudit/testaudit_test.go::TestNobodyWroteThis" }, true},
		{"o identificador fora da convenção", func(w *waiver) { w.ID = "WVR-1" }, true},
	} {
		entry := base
		cenario.mutate(&entry)
		trouble := judgeWaiver(entry, rules, known, today)
		if got := trouble != ""; got != cenario.refused {
			t.Errorf("%s: recusado = %v (%s)", cenario.name, got, trouble)
		}
	}
}

// TestTheRegisterRefusesAStaleException is the ratchet of the register: an
// exception whose site stopped carrying the finding is refused, because a
// permission nobody uses is a permission somebody forgot.
func TestTheRegisterRefusesAStaleException(t *testing.T) {
	t.Chdir("../..")
	path := filepath.Join(t.TempDir(), "waivers.json")
	body := `{"schema": 1, "note": "", "waivers": [{"id": "TST-001", "rule": "skipped-test",
	  "site": "tools/testaudit/testaudit_test.go::TestGateAcceptsTheDeliveredTree", "risk": "Q2", "owner": "quality",
	  "justification": "uma razão que cabe numa frase inteira", "compensation": "tools/testaudit/testaudit_test.go::TestRuleFamiliesRefuseTheirFixtures",
	  "created": "2026-09-24", "expires": "2099-12-31"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write the fixture register: %v", err)
	}
	if _, _, err := readWaivers(".", path, rules, nil, nil, time.Now()); err == nil {
		t.Fatal("o registro aceitou uma exceção cujo lugar não produz o achado")
	}
}

// TestTheRegisterRefusesAnUnknownKey keeps the document from growing a field
// nobody enforces: a key this loader does not know is a key that would be
// trusted for a reason that stopped being true.
func TestTheRegisterRefusesAnUnknownKey(t *testing.T) {
	t.Chdir("../..")
	directory := t.TempDir()
	for name, body := range map[string]string{
		"unknown.json": `{"schema": 1, "note": "", "waivers": [], "extra": true}`,
		"schema.json":  `{"schema": 2, "note": "", "waivers": []}`,
		"broken.json":  `{"schema": 1, "note": ""`,
	} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, _, err := readWaivers(".", path, rules, nil, nil, time.Now()); err == nil {
			t.Errorf("%s: o registro foi aceito", name)
		}
	}
}

// TestTheDeliveredRegisterIsAcceptedAndApplied is the other direction of the
// register: the document the tree ships is read, and every exception in it
// suspends exactly the finding it names.
func TestTheDeliveredRegisterIsAcceptedAndApplied(t *testing.T) {
	t.Chdir("../..")
	findings, _, sites, err := scan(".", auditkit.SkippedDirectories)
	if err != nil {
		t.Fatalf("scan(.) = %v", err)
	}
	register, accepted, err := readWaivers(".", waiverPath, rules, findings, sites, time.Now())
	if err != nil {
		t.Fatalf("readWaivers(%s) = %v", waiverPath, err)
	}
	if len(register.Waivers) == 0 {
		t.Fatal("o registro entregue não declara exceção nenhuma: a árvore não pode ficar sem nenhuma")
	}
	if len(accepted) != len(register.Waivers) {
		t.Fatalf("o registro declara %d exceção(ões) e %d foi(ram) aplicada(s): uma exceção sem achado é uma permissão que sobrou",
			len(register.Waivers), len(accepted))
	}
	for _, entry := range register.Waivers {
		if entry.Risk == forbiddenRisk {
			t.Errorf("a exceção %s declara a classe crítica", entry.ID)
		}
	}
}

// TestTheSleepRuleRefusesTheBarePauseAndMeasuresThePoll states the boundary of
// the sleep rule in one place: the pause that hopes is refused, the poll that
// re-reads its condition is measured.
func TestTheSleepRuleRefusesTheBarePauseAndMeasuresThePoll(t *testing.T) {
	bare := parseTest(t, "func TestX(t *testing.T) {\n\ttime.Sleep(50 * time.Millisecond)\n}")
	result := measured{}
	if findings := judgeSleep(bare, &result); len(findings) != 1 {
		t.Errorf("a pausa no corpo do teste = %v, want um achado", findings)
	}
	poll := parseTest(t, "func TestX(t *testing.T) {\n\tfor i := 0; i < 3; i++ {\n\t\ttime.Sleep(time.Millisecond)\n\t}\n}")
	result = measured{}
	if findings := judgeSleep(poll, &result); len(findings) != 0 {
		t.Errorf("o laço que espera = %v, want nenhum achado", findings)
	}
	if result.SleepsInLoop != 1 {
		t.Errorf("a pausa dentro de laço foi medida %d vez(es), want 1", result.SleepsInLoop)
	}
	// The second boundary is the subject's own timing: the pause the test hands
	// to a double is counted, and the pause the test runs in a goroutine of its
	// own is judged, because that one is the test waiting.
	staged := parseTest(t, "func TestX(t *testing.T) {\n\thandler := func() {\n\t\ttime.Sleep(time.Second)\n\t}\n\tregister(handler)\n}")
	result = measured{}
	if findings := judgeSleep(staged, &result); len(findings) != 0 {
		t.Errorf("a pausa do dublê = %v, want nenhum achado", findings)
	}
	if result.SleepsStaged != 1 || result.SleepsInLoop != 0 {
		t.Errorf("a pausa do dublê foi medida encenada=%d laço=%d, want 1 e 0", result.SleepsStaged, result.SleepsInLoop)
	}
	spawned := parseTest(t, "func TestX(t *testing.T) {\n\tgo func() {\n\t\ttime.Sleep(50 * time.Millisecond)\n\t\tcancel()\n\t}()\n\tserve()\n}")
	result = measured{}
	if findings := judgeSleep(spawned, &result); len(findings) != 1 {
		t.Errorf("a pausa que o teste mesmo corre = %v, want um achado", findings)
	}
	if result.SleepsStaged != 0 {
		t.Errorf("a goroutine do teste foi lida como dublê: encenada=%d", result.SleepsStaged)
	}
	// The import is what makes a call the sleep of the standard library: a method
	// called Sleep is not.
	if findings := judgeSleep(parseTest(t, "func TestX(t *testing.T) {\n\tpool.Sleep(50)\n}"), &measured{}); len(findings) != 0 {
		t.Errorf("um método chamado Sleep = %v, want nenhum achado", findings)
	}
}

// parseTest builds the state of one test file from its body. The fixture is
// parsed and never compiled, which is why it can state an anti-pattern.
func parseTest(t *testing.T, body string) *fileState {
	t.Helper()
	source := "package probe\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"testing\"\n\t\"time\"\n)\n\n" + body + "\n"
	positions := token.NewFileSet()
	parsed, err := parser.ParseFile(positions, "probe_test.go", source, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}
	return &fileState{relative: "probe_test.go", parsed: parsed, body: []byte(source), positions: positions, imports: importsOf(parsed)}
}

// TestTheFixtureCorpusIsTheCorpusTheTreeRefuses states the one difference
// between judging the tree and judging a fixture, because the whole proof of
// every rule depends on it.
func TestTheFixtureCorpusIsTheCorpusTheTreeRefuses(t *testing.T) {
	t.Chdir("../..")
	withTestdata := map[string]bool{}
	for name := range auditkit.SkippedDirectories {
		withTestdata[name] = name == "testdata"
	}
	if !withTestdata["testdata"] {
		t.Fatal("a caminhada da árvore deixou de recusar `testdata`")
	}
	fixtures, err := fixtureFiles(fixtureRoot, auditkit.SkippedDirectories)
	if err != nil {
		t.Fatalf("fixtureFiles(%s) = %v", fixtureRoot, err)
	}
	if len(fixtures) == 0 {
		t.Fatal("a caminhada da fixture não viu arquivo nenhum: a prova de cada regra ficou vazia")
	}
	tree, err := fixtureFiles(".", auditkit.SkippedDirectories)
	if err != nil {
		t.Fatalf("fixtureFiles(.) = %v", err)
	}
	if len(tree) < len(fixtures) {
		t.Errorf("a árvore tem %d fixture(s) e a fixture do portão tem %d: a leitura mudou de corpus", len(tree), len(fixtures))
	}
}
