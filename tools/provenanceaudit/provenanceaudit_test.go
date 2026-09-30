package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18ngen"
	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// TestRuleFamiliesRefuseTheirFixtures drives the proof the gate runs before it
// judges anything: every rule, and the fixture it has to refuse.
func TestRuleFamiliesRefuseTheirFixtures(t *testing.T) {
	t.Chdir("../..")
	if err := proveFamilies(); err != nil {
		t.Fatalf("proveFamilies() = %v", err)
	}
}

// TestGateAcceptsTheDeliveredTree runs the delivered entry point over the
// delivered tree. It is the ratchet of this task: the repository generates the
// typed SQL, the locale catalogs, the TypeScript contracts and the fingerprinted
// assets, and every one of them is registered, is what it says it is and
// discloses that it is generated — the day one is not, this test and the gate
// fail together.
func TestGateAcceptsTheDeliveredTree(t *testing.T) {
	t.Chdir("../..")
	if err := run(".", registerPath); err != nil {
		t.Fatalf("run(.) = %v", err)
	}
}

// TestDeliveredTreeMeasuresWhatTheRulesJudge is the pin against a gate that
// refuses nothing because it looked at nothing. Every rule works from a count,
// and a run whose measurement says zero while the vocabulary claims otherwise is
// a report nobody should believe.
func TestDeliveredTreeMeasuresWhatTheRulesJudge(t *testing.T) {
	t.Chdir("../..")
	record := deliveredRegister(t)
	findings, result, err := judge(".", *record, phaseFamilies, registerPath)
	if err != nil {
		t.Fatalf("judge(.) = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a árvore entregue produziu %d achado(s): %s", len(findings), findings[0])
	}
	for _, entry := range []struct {
		name  string
		count int
	}{
		{"família(s) declarada(s)", result.Families},
		{"artefato(s) declarado(s)", result.Outputs},
		{"insumo(s) declarado(s)", result.Inputs},
		{"fonte(s) de gerador declarada(s)", result.Sources},
		{"arquivo(s) que se anunciam gerados", result.Disclosed},
		{"artefato(s) declarado(s) pelo censo", result.Claimed},
		{"produto(s) de build declarado(s)", result.BuildOutputs},
		{"comparação(ões) de digest", result.Compared},
		{"família(s) re-gerada(s) em processo", len(result.Rendered)},
	} {
		if entry.count == 0 {
			t.Errorf("a árvore mediu 0 %s: a regra que depende dessa contagem não julga nada", entry.name)
		}
	}
	if result.Disclosed != result.Claimed {
		t.Errorf("o censo viu %d arquivo(s) gerado(s) e o registro declara %d: a diferença é um artefato sem procedência ou um registro que descreve o que não existe",
			result.Disclosed, result.Claimed)
	}
	if len(result.Rendered) != 1 || result.Rendered[0] != "i18n" {
		t.Errorf("famílias re-geradas em processo = %v: só a de i18n tem renderizador que este portão executa", result.Rendered)
	}
	if len(phaseFamilies) != result.Families {
		t.Errorf("a fase nomeia %d pipeline(s) e a árvore tem %d família(s): uma família a mais é um artefato que a fase não pediu",
			len(phaseFamilies), result.Families)
	}
}

// TestCensusAndRegisterAgreeOnTheDeliveredTree states the property the gate
// exists for in both directions: what announces itself as generated is declared,
// and what is declared as an artifact announces itself.
func TestCensusAndRegisterAgreeOnTheDeliveredTree(t *testing.T) {
	t.Chdir("../..")
	record := deliveredRegister(t)
	disclosed, err := census(".", buildOutputs(*record))
	if err != nil {
		t.Fatalf("census(.) = %v", err)
	}
	claimed, err := claimedPaths(".", *record)
	if err != nil {
		t.Fatalf("claimedPaths(.) = %v", err)
	}
	for _, path := range disclosed {
		if !claimed[path] {
			t.Errorf("o arquivo %s se anuncia gerado e nenhuma família o declara", path)
		}
	}
	for path := range claimed {
		if !contains(disclosed, path) {
			t.Errorf("o registro declara %s como artefato e o arquivo não se anuncia gerado", path)
		}
	}
	if len(disclosed) == 0 {
		t.Fatal("o censo não viu arquivo nenhum: o vocabulário que este portão lê deixou de casar com a árvore")
	}
}

// TestRegisterIsExactlyTheRefreshedDigests is the property that makes
// `-print-register` safe to run twice: the document on disk is the recomputation,
// so refreshing it changes nothing. A digest edited by hand, an input that moved
// without a regeneration or a new file without a registration moves this test.
func TestRegisterIsExactlyTheRefreshedDigests(t *testing.T) {
	t.Chdir("../..")
	onDisk, err := os.ReadFile(registerPath)
	if err != nil {
		t.Fatalf("read %s = %v", registerPath, err)
	}
	record := deliveredRegister(t)
	if err := refreshRegister(".", record); err != nil {
		t.Fatalf("refreshRegister(.) = %v", err)
	}
	encoded, err := json.MarshalIndent(*record, "", "  ")
	if err != nil {
		t.Fatalf("encode the register = %v", err)
	}
	if refreshed := string(append(encoded, '\n')); refreshed != string(onDisk) {
		t.Fatalf("o registro em %s não é o que a árvore produz agora: rode `make audit-provenance` com `-print-register` e commite o diff de digests\n--- disco ---\n%s\n--- árvore ---\n%s",
			registerPath, onDisk, refreshed)
	}
}

// TestHandEditedArtifactIsRefused is the first half of the minimum validation of
// this task: editing an output without regenerating it fails. The bytes of a
// declared artifact stop matching the recorded digest, which is exactly what a
// hand edit looks like from the outside.
func TestHandEditedArtifactIsRefused(t *testing.T) {
	t.Chdir("../..")
	record := deliveredRegister(t)
	familyOf(t, record, "contracts").Outputs[0].Digest = "sha256:" + strings.Repeat("0", 64)
	findings, _, err := judge(".", *record, phaseFamilies, registerPath)
	if err != nil {
		t.Fatalf("judge(.) = %v", err)
	}
	refusal := requireRule(t, findings, RuleOutputDrift)
	if !strings.Contains(refusal.Detail, "web/src/contracts/generated.ts") {
		t.Errorf("o achado não nomeia o artefato editado: %s", refusal)
	}
}

// TestVersionEditedWithoutRegeneratingIsRefused is the other half: a version
// refreshed in the register that the artifact does not declare is a pin nobody
// checked, and it is refused where the register says the tree states it.
func TestVersionEditedWithoutRegeneratingIsRefused(t *testing.T) {
	t.Chdir("../..")
	record := deliveredRegister(t)
	pinned := familyOf(t, record, "sqlc")
	pinned.Version = "v9.9.9"
	findings, _, err := judge(".", *record, phaseFamilies, registerPath)
	if err != nil {
		t.Fatalf("judge(.) = %v", err)
	}
	refusal := requireRule(t, findings, RuleVersionDrift)
	if !strings.Contains(refusal.Detail, "v9.9.9") {
		t.Errorf("o achado não nomeia a versão inventada: %s", refusal)
	}
}

// TestRemovedArtifactIsRefused refuses the register that describes a tree which
// does not exist: the declaration is the only place the artifact was, so its
// absence is what a deleted output looks like.
func TestRemovedArtifactIsRefused(t *testing.T) {
	t.Chdir("../..")
	record := deliveredRegister(t)
	familyOf(t, record, "assets").Outputs = append(familyOf(t, record, "assets").Outputs,
		entry{Path: "web/dist/absent.js", Header: "Code generated by cmd/assetgen. DO NOT EDIT."})
	findings, _, err := judge(".", *record, phaseFamilies, registerPath)
	if err != nil {
		t.Fatalf("judge(.) = %v", err)
	}
	refusal := requireRule(t, findings, RuleMissingOutput)
	if !strings.Contains(refusal.Detail, "web/dist/absent.js") {
		t.Errorf("o achado não nomeia o artefato ausente: %s", refusal)
	}
}

// TestForgottenFamilyIsRefused refuses the register with a hole: the pipeline the
// phase names and nobody records is the artifact whose generator is unknown.
func TestForgottenFamilyIsRefused(t *testing.T) {
	t.Chdir("../..")
	record := deliveredRegister(t)
	kept := []pipeline{}
	for _, entry := range record.Families {
		if entry.Name != "assets" {
			kept = append(kept, entry)
		}
	}
	record.Families = kept
	findings, _, err := judge(".", *record, phaseFamilies, registerPath)
	if err != nil {
		t.Fatalf("judge(.) = %v", err)
	}
	refusal := requireRule(t, findings, RuleMissingFamily)
	if !strings.Contains(refusal.Detail, "assets") {
		t.Errorf("o achado não nomeia o pipeline esquecido: %s", refusal)
	}
}

// TestFamilyWithoutProofIsRefused refuses the family that answers none of the
// three ways a determinism is proven: no renderer this gate runs, no target that
// reruns the generator, no test that owns the property.
func TestFamilyWithoutProofIsRefused(t *testing.T) {
	t.Chdir("../..")
	record := deliveredRegister(t)
	bare := familyOf(t, record, "assets")
	bare.VerifyCommand = ""
	bare.OwnerTest = nil
	findings, _, err := judge(".", *record, phaseFamilies, registerPath)
	if err != nil {
		t.Fatalf("judge(.) = %v", err)
	}
	refusal := requireRule(t, findings, RuleUncoveredProof)
	if !strings.Contains(refusal.Detail, "reprodutibilidade") {
		t.Errorf("o achado não diz o que falta provar: %s", refusal)
	}
}

// TestOwningTestThatDoesNotExistIsRefused refuses the delegation to a proof that
// is not there: a family that names a test is a family whose determinism somebody
// answers for, and the answer has to be in the tree.
func TestOwningTestThatDoesNotExistIsRefused(t *testing.T) {
	t.Chdir("../..")
	record := deliveredRegister(t)
	bare := familyOf(t, record, "assets")
	bare.VerifyCommand = ""
	bare.OwnerTest = &ownerTest{Path: "cmd/assetgen/main_test.go", Function: "TestNobodyWroteThis"}
	findings, _, err := judge(".", *record, phaseFamilies, registerPath)
	if err != nil {
		t.Fatalf("judge(.) = %v", err)
	}
	refusal := requireRule(t, findings, RuleUncoveredProof)
	if !strings.Contains(refusal.Detail, "TestNobodyWroteThis") {
		t.Errorf("o achado não nomeia a prova ausente: %s", refusal)
	}
}

// TestUnknownGeneratedArtifactIsRefused is the third half of the minimum
// validation: a file that carries the disclosure of a generator and that no
// family declares was produced by something, and that something is not written
// down. The tree is built here instead of borrowed so that the rule is exercised
// on a file the register has never seen.
func TestUnknownGeneratedArtifactIsRefused(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "tree/orphan.ts", "// Code generated by something-else. DO NOT EDIT.\nexport const orphan = 1;\n")
	writeFixture(t, root, "tree/input.txt", "um insumo\n")
	record := register{Schema: 1, Families: []pipeline{{
		Name: "contracts", Generator: "contractgen", Command: "make generate",
		VerifyCommand: "make generate-check",
		Inputs:        []entry{{Path: "tree/input.txt"}},
	}}}
	findings, _, err := judge(root, record, []string{"contracts"}, root+"/provenance.json")
	if err != nil {
		t.Fatalf("judge(%s) = %v", root, err)
	}
	refusal := requireRule(t, findings, RuleUnknownArtifact)
	if !strings.Contains(refusal.Path, "orphan.ts") {
		t.Errorf("o achado não nomeia o artefato desconhecido: %s", refusal)
	}
	if refusal.Line != 1 {
		t.Errorf("o achado aponta a linha %d e a divulgação abre o arquivo na 1", refusal.Line)
	}
}

// TestDeclaredArtifactWithoutDisclosureIsRefused refuses the generated file that
// does not say so: it is the file a reader edits by hand, and the header the
// family recorded is the sentence the file owes.
func TestDeclaredArtifactWithoutDisclosureIsRefused(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "tree/silent.ts", "export const silent = 1;\n")
	writeFixture(t, root, "tree/input.txt", "um insumo\n")
	record := register{Schema: 1, Families: []pipeline{{
		Name: "contracts", Generator: "contractgen", Command: "make generate",
		VerifyCommand: "make generate-check",
		Inputs:        []entry{{Path: "tree/input.txt"}},
		Outputs: []entry{{
			Path:   "tree/silent.ts",
			Header: "Code generated by tools/contractgen from api/openapi.json. DO NOT EDIT.",
		}},
	}}}
	findings, _, err := judge(root, record, []string{"contracts"}, root+"/provenance.json")
	if err != nil {
		t.Fatalf("judge(%s) = %v", root, err)
	}
	refusal := requireRule(t, findings, RuleMissingHeader)
	if !strings.Contains(refusal.Path, "silent.ts") {
		t.Errorf("o achado não nomeia o artefato mudo: %s", refusal)
	}
}

// TestUnignoredBuildOutputIsRefused refuses the product of a build the repository
// would accept: dist belongs to a build and never to the tree.
func TestUnignoredBuildOutputIsRefused(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, ".gitignore", "*.log\n")
	record := register{Schema: 1, Families: []pipeline{{
		Name: "assets", Generator: "assetgen", Command: "make generate",
		VerifyCommand: "make generate-check",
		BuildOutputs:  []string{"web/dist"},
	}}}
	findings, _, err := judge(root, record, []string{"assets"}, root+"/provenance.json")
	if err != nil {
		t.Fatalf("judge(%s) = %v", root, err)
	}
	refusal := requireRule(t, findings, RuleUnignoredProduct)
	if !strings.Contains(refusal.Detail, "web/dist") {
		t.Errorf("o achado não nomeia o produto não ignorado: %s", refusal)
	}
	if _, err := os.Stat(filepath.Join(root, "provenance.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("julgar uma árvore escreveu no disco: %v", err)
	}
}

// TestI18nRenderIsDeterministicAndMatchesTheTree proves the one determinism this
// gate can prove itself: two renders in process answer the same bytes, and those
// bytes are the tree. `make generate-check` answers the same question with the
// real toolchain; this one answers it without one.
func TestI18nRenderIsDeterministicAndMatchesTheTree(t *testing.T) {
	t.Chdir("../..")
	record := deliveredRegister(t)
	pipe := familyOf(t, record, "i18n")
	outputs, err := i18nOutputs(".", *pipe)
	if err != nil {
		t.Fatalf("i18nOutputs(.) = %v", err)
	}
	firstTS, firstGo, err := i18ngen.RenderAll("locales", outputs)
	if err != nil {
		t.Fatalf("RenderAll(locales) = %v", err)
	}
	secondTS, secondGo, err := i18ngen.RenderAll("locales", outputs)
	if err != nil {
		t.Fatalf("RenderAll(locales) again = %v", err)
	}
	if !bytes.Equal(firstTS, secondTS) || !bytes.Equal(firstGo, secondGo) {
		t.Fatal("duas execuções do renderizador de i18n não produzem os mesmos bytes")
	}
	for _, target := range []struct {
		path     string
		produced []byte
	}{
		{"web/src/i18n/generated.ts", firstTS},
		{"internal/i18n/generated.go", firstGo},
	} {
		inTree, err := os.ReadFile(target.path)
		if err != nil {
			t.Fatalf("read %s = %v", target.path, err)
		}
		if !bytes.Equal(inTree, target.produced) {
			t.Errorf("%s não é o que o gerador produz agora: rode `make generate`", target.path)
		}
	}
}

// TestRegisterRefusesAnUnknownSchema keeps the document honest about the version
// of its own shape: a register this gate cannot read is refused instead of read
// as if it were current.
func TestRegisterRefusesAnUnknownSchema(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "provenance.json", `{"schema": 2, "families": [{"name": "i18n"}]}`)
	if _, err := readRegister(filepath.Join(root, "provenance.json")); err == nil {
		t.Fatal("readRegister aceitou um registro de schema 2")
	}
	writeFixture(t, root, "empty.json", `{"schema": 1}`)
	if _, err := readRegister(filepath.Join(root, "empty.json")); err == nil {
		t.Fatal("readRegister aceitou um registro sem família nenhuma")
	}
}

// TestDigestHoldsTheSetAndNotOnlyTheBytes keeps the digest from answering
// "unchanged" for a directory that lost a file and gained another: the name of
// every file is hashed next to its content.
func TestDigestHoldsTheSetAndNotOnlyTheBytes(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "set/a.txt", "mesmo conteúdo\n")
	writeFixture(t, root, "set/b.txt", "mesmo conteúdo\n")
	one, err := digestOf(root, []string{"set/a.txt"})
	if err != nil {
		t.Fatalf("digestOf(set/a.txt) = %v", err)
	}
	renamed, err := digestOf(root, []string{"set/b.txt"})
	if err != nil {
		t.Fatalf("digestOf(set/b.txt) = %v", err)
	}
	if one == renamed {
		t.Error("o digest não se moveu quando o arquivo mudou de nome")
	}
	writeFixture(t, root, "set/c.txt", "conteúdo diferente\n")
	changed, err := digestOf(root, []string{"set/c.txt"})
	if err != nil {
		t.Fatalf("digestOf(set/c.txt) = %v", err)
	}
	if changed == one {
		t.Error("o digest não se moveu quando o conteúdo mudou")
	}
	if !strings.HasPrefix(one, "sha256:") || len(one) != len("sha256:")+sha256.Size*2 {
		t.Errorf("o digest %q não é sha256 em hexadecimal", one)
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
		if entry.Reason == "" {
			t.Errorf("a família %s não diz por que a regra existe", entry.Name)
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

// TestTheGapsAreStated keeps the declared limitation printed: the register is a
// prose-free record, and the only place the gate says what it does not judge is
// the sentence every run prints.
func TestTheGapsAreStated(t *testing.T) {
	statement := unenforced()
	for _, named := range []string{"sqlc", "contractgen", "assetgen", ".gitignore", "três primeiras linhas"} {
		if !strings.Contains(statement, named) {
			t.Errorf("a lacuna declarada não nomeia %q: %q", named, statement)
		}
	}
}

// deliveredRegister reads the register the tree delivers, and refuses a run
// where it is not there: every test below judges the same document.
func deliveredRegister(t *testing.T) *register {
	t.Helper()
	record, err := readRegister(registerPath)
	if err != nil {
		t.Fatalf("readRegister(%s) = %v", registerPath, err)
	}
	return &record
}

// familyOf answers the family by name, as a pointer, because the tests below edit
// it to see the gate refuse.
func familyOf(t *testing.T, record *register, name string) *pipeline {
	t.Helper()
	for index := range record.Families {
		if record.Families[index].Name == name {
			return &record.Families[index]
		}
	}
	t.Fatalf("o registro não declara a família %q", name)
	return nil
}

func requireRule(t *testing.T, findings []auditkit.Finding, rule string) auditkit.Finding {
	t.Helper()
	for _, entry := range findings {
		if entry.Rule == rule {
			return entry
		}
	}
	t.Fatalf("a execução produziu %d achado(s) e nenhum é %q: %v", len(findings), rule, findings)
	return auditkit.Finding{}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// writeFixture builds the tree a fixture describes, including the directories.
func writeFixture(t *testing.T, root, relative, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s = %v", path, err)
	}
}
