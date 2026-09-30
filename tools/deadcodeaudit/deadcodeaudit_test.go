package main

import (
	"sort"
	"strings"
	"testing"
)

// TestRuleFamiliesRefuseTheirFixtures drives the proof the gate itself runs
// before it judges anything: every rule, and the fixture it must refuse. The
// clean fixtures are the other direction, and they are the reason a rule cannot
// pass this test by refusing everything.
func TestRuleFamiliesRefuseTheirFixtures(t *testing.T) {
	t.Chdir("../..")
	if err := proveFamilies(fixtureRoot); err != nil {
		t.Fatalf("proveFamilies(%s) = %v", fixtureRoot, err)
	}
}

// TestGateAcceptsTheDeliveredTree runs the delivered entry point over the
// delivered tree. It is the ratchet of this task: the tree has no placeholder,
// no untracked deferral, no statement nobody runs, no branch decided by a literal
// and no configuration that lies, and the day it has one, this test and the gate
// fail together.
func TestGateAcceptsTheDeliveredTree(t *testing.T) {
	t.Chdir("../..")
	if err := run("."); err != nil {
		t.Fatalf("run(.) = %v", err)
	}
}

// TestDeliveredTreeMeasuresTheDeclaredGap pins the measurement behind the gap
// this gate prints: the exports no other package of this tree mentions have to be
// counted, have to be a part of the surface and not all of it, and the number the
// report carries has to name them. A gap that stops being measured is a gap
// nobody reviews.
func TestDeliveredTreeMeasuresTheDeclaredGap(t *testing.T) {
	t.Chdir("../..")
	tree, err := scanTree()
	if err != nil {
		t.Fatalf("scanTree() = %v", err)
	}
	if tree.Files == 0 || tree.Functions == 0 {
		t.Fatalf("the walk judged %d file(s) and %d function(s)", tree.Files, tree.Functions)
	}
	if len(tree.Generated) == 0 {
		t.Fatal("no file was excluded by provenance")
	}
	if len(tree.Unreadable) != 0 {
		t.Fatalf("unreadable files = %v", tree.Unreadable)
	}
	if len(tree.Findings) != 0 {
		t.Fatalf("the delivered tree produced %d finding(s): %s", len(tree.Findings), tree.Findings[0])
	}
	if tree.Exported == 0 || len(tree.Unconsumed) == 0 || len(tree.Unconsumed) >= tree.Exported {
		t.Fatalf("unconsumed exports = %d of %d exported declaration(s)", len(tree.Unconsumed), tree.Exported)
	}
	statement := unenforced(tree.Exported, tree.Unconsumed)
	if !strings.Contains(statement, "export sem consumo") || !strings.Contains(statement, tree.Unconsumed[0]) {
		t.Fatalf("the declared gap does not name the measurement: %q", statement)
	}
}

// TestDeliveredConfigurationSurfaceIsClean holds the three sets of the
// configuration rule to each other on the delivered tree. The counts are not
// pinned to a number — a key added tomorrow has to change all three — but they do
// have to agree, which is what "no finding" means here.
func TestDeliveredConfigurationSurfaceIsClean(t *testing.T) {
	t.Chdir("../..")
	configuration, err := configurationFindings(configurationDirectory, configurationTemplate, readRoots)
	if err != nil {
		t.Fatalf("configurationFindings = %v", err)
	}
	if len(configuration.comparison) != 0 {
		t.Fatalf("the configuration surface produced %d finding(s): %s", len(configuration.comparison), configuration.comparison[0])
	}
	if configuration.accepted == 0 {
		t.Fatal("the accepted set is empty: the rule would compare nothing")
	}
	if configuration.accepted != configuration.read || configuration.accepted != configuration.documented {
		t.Fatalf("accepted = %d, read = %d, documented = %d",
			configuration.accepted, configuration.read, configuration.documented)
	}
}

// TestDeferralMarkerOpensTheComment pins the decision the postponement rule
// makes: the marker has to open the comment, and only the upper case opens it.
func TestDeferralMarkerOpensTheComment(t *testing.T) {
	cases := []struct {
		name    string
		comment string
		marker  string
	}{
		{"o marcador abre o comentário", "// TODO(P23-T04): close the port", "TODO"},
		{"o marcador abre e não traz referência", "// TODO: fix later", "TODO"},
		{"o marcador abre depois de um espaço", "//   FIXME(#85): the loop", "FIXME"},
		{"o marcador abre um comentário de bloco", "/* HACK(P23-T07): the fake sleeps */", "HACK"},
		{"a frase depois do marcador continua sendo adiamento", "// TODO: fix this later.", "TODO"},
		{"o marcador no meio da frase é prosa", "// it matches whole words, so TODO is prose", ""},
		{"o marcador com maiúsculas é outra palavra", "// XXXX is a token of another vocabulary", ""},
		{"o minúsculo é a palavra comum", "// todo requisito tem teste", ""},
		{"a continuação de um parágrafo que discute o marcador", "// and \"TBD\" are conventions of source code", ""},
	}
	for _, entry := range cases {
		if marker := deferralMarker(entry.comment); marker != entry.marker {
			t.Errorf("%s: deferralMarker(%q) = %q, want %q", entry.name, entry.comment, marker, entry.marker)
		}
	}
}

// TestDeferredFixtureSeparatesTrackedFromUntracked separates the two answers the
// rule gives: the deferral that names who resolves it is printed, and the one
// that names nobody is refused.
func TestDeferredFixtureSeparatesTrackedFromUntracked(t *testing.T) {
	measured := scanFixture(t, "deferred")
	assertRules(t, measured.Findings, RuleDeferred, 3)
	assertRules(t, measured.Accepted, RuleDeferred, 2)
	for _, accepted := range measured.Accepted {
		if !strings.Contains(accepted.Detail, "P23-T04") && !strings.Contains(accepted.Detail, "#85") {
			t.Errorf("accepted deferral without a plan reference or an issue: %s", accepted)
		}
	}
	for _, refusal := range measured.Findings {
		if !strings.Contains(refusal.Detail, "sem referência") && !strings.Contains(refusal.Detail, "não é uma tarefa") {
			t.Errorf("refusal that does not say what a reference looks like: %s", refusal)
		}
	}
}

// TestFalseSuccessFixtureRequiresUnfinishedAndSuccessful holds the rule to both
// of its terms: the announcement and the success. The fixture names the deferral
// honestly in one of the three refusals, and the refusal stands — the gate prints
// the deferral and refuses the success in the same run.
func TestFalseSuccessFixtureRequiresUnfinishedAndSuccessful(t *testing.T) {
	measured := scanFixture(t, "falsesuccess")
	assertRules(t, measured.Findings, RuleFalseSuccess, 3)
	assertRules(t, measured.Accepted, RuleDeferred, 1)
}

func TestUnreachableFixtureRefusesEveryTerminator(t *testing.T) {
	measured := scanFixture(t, "unreachable")
	assertRules(t, measured.Findings, RuleUnreachable, 4)
	for _, refusal := range measured.Findings {
		if !strings.Contains(refusal.Detail, "nada mais roda") {
			t.Errorf("refusal that does not name the terminator: %s", refusal)
		}
	}
}

func TestConstantBranchFixtureRefusesLiterals(t *testing.T) {
	measured := scanFixture(t, "constantbranch")
	assertRules(t, measured.Findings, RuleConstantBranch, 2)
}

func TestPlaceholderFixtureRefusesTheVocabulary(t *testing.T) {
	measured := scanFixture(t, "placeholder")
	assertRules(t, measured.Findings, RulePlaceholderPanic, 2)
}

// TestCleanFixturesProduceNothing is the negative direction of every family that
// has a legal shape next to the refused one, and the deferrals the clean fixtures
// carry have to be accepted: the gate reads them, it does not skip them.
func TestCleanFixturesProduceNothing(t *testing.T) {
	for _, entry := range cleanFixtures {
		measured := scanFixture(t, entry.Target)
		if len(measured.Findings) != 0 {
			t.Errorf("the clean fixture %s produced %d finding(s): %s", entry.Target, len(measured.Findings), measured.Findings[0])
		}
		if measured.Files == 0 {
			t.Errorf("the clean fixture %s judged no file: a green run over nothing proves nothing", entry.Target)
		}
	}
	accepted := scanFixture(t, "clean/deferred")
	if len(accepted.Accepted) != 0 {
		t.Errorf("the prose fixture produced %d accepted deferral(s): %s", len(accepted.Accepted), accepted.Accepted[0])
	}
	honest := scanFixture(t, "clean/falsesuccess")
	assertRules(t, honest.Accepted, RuleDeferred, 1)
}

// TestGeneratedCodeIsExcludedByProvenance proves the exclusion in both
// directions: the marked file is left out, and the file that only carries the
// marker inside a string is judged.
func TestGeneratedCodeIsExcludedByProvenance(t *testing.T) {
	generated := scanFixture(t, "generated")
	if len(generated.Generated) != 1 || len(generated.Findings) != 0 {
		t.Fatalf("the generated fixture excluded %d file(s) and produced %d finding(s)", len(generated.Generated), len(generated.Findings))
	}
	decoy := scanFixture(t, "decoy")
	if len(decoy.Generated) != 0 {
		t.Fatalf("the decoy fixture was excluded by provenance: %v", decoy.Generated)
	}
	names := map[string]bool{}
	for _, refusal := range decoy.Findings {
		names[refusal.Rule] = true
	}
	for _, want := range []string{RulePlaceholderPanic, RuleDeferred} {
		if !names[want] {
			t.Errorf("the decoy fixture produced no %s finding: %v", want, decoy.Findings)
		}
	}
}

func TestDocumentedKeys(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"a atribuição e a prosa contam, cada chave uma vez",
			"ARENA_ENV=development\n# see ARENA_ENV for the default\nARENA_DB_MAX_CONNS=10\n",
			[]string{"ARENA_DB_MAX_CONNS", "ARENA_ENV"}},
		{"a variável de outro sistema não é configuração do processo",
			"POSTGRES_PASSWORD=secret\nARENA_ADDR=127.0.0.1:8080\n",
			[]string{"ARENA_ADDR"}},
		{"um nome minúsculo não é uma chave", "arena_env=development\n", nil},
		{"um prefixo que continua não é a chave inteira", "ARENA_ENV_SUFFIX=x\n", []string{"ARENA_ENV_SUFFIX"}},
	}
	for _, entry := range cases {
		got := documentedKeys(entry.text)
		if len(got) != len(entry.want) {
			t.Errorf("%s: documentedKeys = %v, want %v", entry.name, got, entry.want)
			continue
		}
		for index := range got {
			if got[index] != entry.want[index] {
				t.Errorf("%s: documentedKeys = %v, want %v", entry.name, got, entry.want)
				break
			}
		}
	}
}

// TestCompareConfigurationDirections drives the five directions with a table
// instead of a checkout: the registry is a value, so every direction can be asked
// for by itself.
func TestCompareConfigurationDirections(t *testing.T) {
	// One key per direction, so that the count below is the number of directions
	// and not a coincidence: a key that fails twice would count twice.
	registry := registry{
		directory: "fixture/config",
		accepted:  map[string]int{"ARENA_WIRED": 4, "ARENA_ORPHAN": 5, "ARENA_UNDOCUMENTED": 6},
		read:      map[string]int{"ARENA_WIRED": 8, "ARENA_UNDOCUMENTED": 9, "ARENA_GHOST": 10},
		order:     []string{"ARENA_GHOST", "ARENA_ORPHAN", "ARENA_UNDOCUMENTED", "ARENA_WIRED"},
	}
	findings := compareConfiguration(registry, []string{"ARENA_WIRED", "ARENA_ORPHAN", "ARENA_TEMPLATE_ONLY"}, "fixture/env.example")
	windows := []string{"nunca é lida", "não é documentada", "não está no conjunto aceito", "documentada em"}
	for _, window := range windows {
		found := false
		for _, refusal := range findings {
			if strings.Contains(refusal.Detail, window) {
				found = true
			}
		}
		if !found {
			t.Errorf("no finding says %q: %v", window, findings)
		}
	}
	if len(findings) != 4 {
		t.Fatalf("the table produced %d finding(s), want 4: %v", len(findings), findings)
	}
	for _, refusal := range findings {
		if refusal.Rule != RuleConfigurationKey {
			t.Errorf("finding of another rule: %s", refusal)
		}
	}
}

// TestConfigurationFixtureExercisesEveryDirection goes through the same entry
// point the delivered tree goes through, with the fixture's registry, template and
// read roots: one key of each direction is refused, and the registered key read
// directly is left alone.
func TestConfigurationFixtureExercisesEveryDirection(t *testing.T) {
	root := "testdata/configuration"
	configuration, err := configurationFindings(root+"/registry", root+"/env.example", []string{root + "/outside", root + "/inside"})
	if err != nil {
		t.Fatalf("configurationFindings = %v", err)
	}
	expected := map[string]string{
		"ARENA_ORPHAN":          "nunca é lida",
		"ARENA_SILENT":          "não é documentada",
		"ARENA_UNACCEPTED":      "não está no conjunto aceito",
		"ARENA_DOCUMENTED_ONLY": "documentada em",
		"ARENA_LEGACY_FLAG":     "direto do ambiente",
	}
	if len(configuration.comparison) != len(expected) {
		t.Fatalf("the fixture produced %d finding(s), want %d: %v", len(configuration.comparison), len(expected), configuration.comparison)
	}
	for key, window := range expected {
		found := false
		for _, refusal := range configuration.comparison {
			if strings.Contains(refusal.Detail, key) && strings.Contains(refusal.Detail, window) {
				found = true
			}
		}
		if !found {
			t.Errorf("no finding refuses %s with %q: %v", key, window, configuration.comparison)
		}
	}
	for _, refusal := range configuration.comparison {
		if strings.Contains(refusal.Path, "inside/registered.go") {
			t.Errorf("the registered key read directly was refused: %s", refusal)
		}
	}
	if configuration.accepted != 4 || configuration.read != 4 || configuration.documented != 4 {
		t.Errorf("accepted = %d, read = %d, documented = %d",
			configuration.accepted, configuration.read, configuration.documented)
	}
}

func TestRegistryResolvesConstantsAndReads(t *testing.T) {
	registry, err := readRegistry("testdata/configuration/registry")
	if err != nil {
		t.Fatalf("readRegistry = %v", err)
	}
	for _, key := range []string{"ARENA_ENV", "ARENA_SINK_DIR", "ARENA_SILENT", "ARENA_ORPHAN"} {
		if _, present := registry.accepted[key]; !present {
			t.Errorf("%s is not in the accepted set", key)
		}
	}
	for _, key := range []string{"ARENA_ENV", "ARENA_SINK_DIR", "ARENA_SILENT", "ARENA_UNACCEPTED"} {
		if _, present := registry.read[key]; !present {
			t.Errorf("%s is not read by Load", key)
		}
	}
	if _, present := registry.read["ARENA_ORPHAN"]; present {
		t.Error("ARENA_ORPHAN is read: the fixture no longer exercises the orphan flag")
	}
	if !sort.StringsAreSorted(registry.order) {
		t.Errorf("the registry order is not sorted: %v", registry.order)
	}
}

// TestReadRegistryRefusesWithoutALoader is the hole the rule refuses to skip: a
// directory that declares no Load has no accepted set to compare, and a gate that
// answered "no finding" there would be green over nothing.
func TestReadRegistryRefusesWithoutALoader(t *testing.T) {
	if _, err := readRegistry("testdata/deferred"); err == nil {
		t.Fatal("readRegistry answered without a Load declaration")
	} else if !strings.Contains(err.Error(), "Load") {
		t.Fatalf("the refusal does not name what is missing: %v", err)
	}
}

func scanFixture(t *testing.T, name string) measured {
	t.Helper()
	measured, err := scanDirectory("testdata/" + name)
	if err != nil {
		t.Fatalf("scanDirectory(testdata/%s) = %v", name, err)
	}
	return measured
}

// assertRules holds a list of findings to one rule and one count: a fixture that
// started refusing another rule, or refusing a different number of shapes, has to
// fail here instead of passing as "some finding".
func assertRules(t *testing.T, findings []finding, rule string, count int) {
	t.Helper()
	if len(findings) != count {
		t.Fatalf("%d finding(s) of %s, want %d: %v", len(findings), rule, count, findings)
	}
	for _, entry := range findings {
		if entry.Rule != rule {
			t.Errorf("finding of %s where %s was expected: %s", entry.Rule, rule, entry)
		}
	}
}
