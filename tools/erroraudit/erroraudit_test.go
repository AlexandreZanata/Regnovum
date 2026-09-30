package main

import (
	"go/ast"
	"go/parser"
	"os"
	"strings"
	"testing"
)

// TestRuleFamiliesRefuseTheirFixtures drives the proof the gate itself runs
// before it judges anything: every rule, and the fixture it must refuse.
func TestRuleFamiliesRefuseTheirFixtures(t *testing.T) {
	t.Chdir("../..")
	if err := proveFamilies(fixtureRoot); err != nil {
		t.Fatalf("proveFamilies(%s) = %v", fixtureRoot, err)
	}
}

// TestGateAcceptsTheDeliveredTree runs the delivered entry point over the
// delivered tree. It is the ratchet of this task: the tree acquires resources,
// opens transactions, derives contexts and starts goroutines, and every one of
// them is released, used and owned — the day one is not, this test and the gate
// fail together.
func TestGateAcceptsTheDeliveredTree(t *testing.T) {
	t.Chdir("../..")
	if err := run("."); err != nil {
		t.Fatalf("run(.) = %v", err)
	}
}

// TestDeliveredTreeMeasuresWhatTheRulesJudge is the pin against a gate that
// refuses nothing because it looked at nothing. Each rule of this gate needs a
// count it works with, and the count is printed: a run whose measurement says
// zero while the vocabulary claims otherwise is a report nobody should believe —
// this test exists because it once said zero transactions over a tree that opens
// them in fifteen places.
func TestDeliveredTreeMeasuresWhatTheRulesJudge(t *testing.T) {
	t.Chdir("../..")
	tree, err := scanTree()
	if err != nil {
		t.Fatalf("scanTree() = %v", err)
	}
	for _, entry := range []struct {
		name  string
		count int
	}{
		{"recurso(s) adquirido(s)", tree.Acquired},
		{"transação(ões)", tree.Transactions},
		{"cliente(s) HTTP", tree.Clients},
		{"contexto(s) de parâmetro", tree.ContextParams},
		{"goroutine(s)", tree.Goroutines},
		{"mensagem(ns) pública(s)", tree.Messages},
		{"fmt.Errorf", tree.FormatCalls},
		{"chamada(s) com resultado descartado", tree.Discarded},
	} {
		if entry.count == 0 {
			t.Errorf("a árvore mediu 0 %s: a regra que depende dessa contagem não julga nada", entry.name)
		}
	}
	if tree.Resolved == 0 || tree.Resolved >= tree.Discarded {
		t.Errorf("chamada(s) resolvida(s) = %d de %d descartada(s): a fronteira declarada deixou de ser medida",
			tree.Resolved, tree.Discarded)
	}
	if len(tree.Generated) == 0 {
		t.Error("nenhum arquivo excluído por proveniência: o marcador deixou de casar")
	}
	if len(tree.Unreadable) != 0 {
		t.Errorf("arquivos ilegíveis = %v", tree.Unreadable)
	}
	if len(tree.Findings) != 0 {
		t.Fatalf("a árvore entregue produziu %d achado(s): %s", len(tree.Findings), tree.Findings[0])
	}
	statement := unenforced(tree)
	if !strings.Contains(statement, "descartado") || !strings.Contains(statement, "goroutine") {
		t.Fatalf("a lacuna declarada não nomeia o que o portão deixa de julgar: %q", statement)
	}
}

// TestEveryRefusedFixtureNamesItsOwnRule separates the family proof per fixture:
// the fixture has to be refused by the rule it is the proof of, and every finding
// it produces has to be classified — a finding the vocabulary cannot name is the
// "other" bucket this gate refuses to have.
func TestEveryRefusedFixtureNamesItsOwnRule(t *testing.T) {
	enterRoot(t)
	for _, entry := range families {
		measured := scanFixture(t, entry.Target)
		named := false
		for _, refusal := range measured.Findings {
			if !ruleKnown(refusal.Rule) {
				t.Errorf("a fixture %s produziu um achado que o vocabulário não nomeia: %s", entry.Target, refusal)
			}
			if refusal.Rule == entry.Name {
				named = true
			}
		}
		if !named {
			t.Errorf("a família %q não recusou a própria fixture %s: %v", entry.Name, entry.Target, measured.Findings)
		}
		if measured.Files == 0 {
			t.Errorf("a fixture %s não julgou arquivo nenhum", entry.Target)
		}
	}
}

// TestCleanFixturesProduceNothing is the other direction of every family: the
// legal shape next to the refused one. A rule proved only where it refuses could
// be refusing everything while looking strict.
func TestCleanFixturesProduceNothing(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range cleanFixtures {
		if seen[entry.Target] {
			continue
		}
		seen[entry.Target] = true
		measured := scanFixture(t, entry.Target)
		if measured.Files == 0 {
			t.Errorf("a fixture limpa %s não julgou arquivo nenhum: um verde sobre nada não prova nada", entry.Target)
		}
		if len(measured.Findings) != 0 {
			t.Errorf("a fixture limpa %s produziu %d achado(s): %s", entry.Target, len(measured.Findings), measured.Findings[0])
		}
	}
}

// TestGeneratedCodeIsExcludedByProvenance proves the exclusion in both directions:
// the marked file is left out, and the file that only carries the marker inside a
// string is judged by every rule it breaks.
func TestGeneratedCodeIsExcludedByProvenance(t *testing.T) {
	enterRoot(t)
	generated := scanFixture(t, "generated")
	if len(generated.Generated) != 1 || len(generated.Findings) != 0 {
		t.Fatalf("a fixture gerada excluiu %d arquivo(s) e produziu %d achado(s)",
			len(generated.Generated), len(generated.Findings))
	}
	decoy := scanFixture(t, "decoy")
	if len(decoy.Generated) != 0 {
		t.Fatalf("a fixture chamariz foi excluída por proveniência: %v", decoy.Generated)
	}
	if len(decoy.Findings) == 0 {
		t.Fatal("a fixture chamariz não produziu achado: ela não prova nada sobre arquivo julgado")
	}
}

// TestCuratedFieldsArePublicText holds the narrowing of the leak rule to a table:
// the two parts of a domain error that are public by contract are published, and
// everything else an error carries — the error itself, its text, its cause, a
// credential — is refused. Without this table the rule would read a domain error
// being surfaced as a leak of the domain error.
func TestCuratedFieldsArePublicText(t *testing.T) {
	enterRoot(t)
	cases := []struct {
		name       string
		source     string
		wantLeaked bool
	}{
		{"o campo curado é texto público", "domainErr.Message", false},
		{"o código é texto público", "domainErr.Code", false},
		{"o campo curado dentro de uma conversão", `strings.ToLower(string(domainErr.Code))`, false},
		{"o erro entregue inteiro é vazamento", "errProvider", true},
		{"a mensagem do erro é vazamento", "errProvider.Error()", true},
		{"um campo público desconhecido é vazamento", "domainErr.Cause", true},
		{"a credencial é vazamento pelo sufixo", "webhookSigningSecret", true},
		{"a chave de API é vazamento pelo sufixo", "postHogAPIKey", true},
		{"o identificador comum não é vazamento", "requestID", false},
		{"um texto formatado com o erro é vazamento", `fmt.Sprintf("%v", errProvider)`, true},
		{"um ponteiro para erro é vazamento", "&errProvider", true},
	}
	for _, entry := range cases {
		expression, err := parser.ParseExpr(entry.source)
		if err != nil {
			t.Fatalf("%s: ParseExpr(%q) = %v", entry.name, entry.source, err)
		}
		leaked := leaksInto(expression) != ""
		if leaked != entry.wantLeaked {
			t.Errorf("%s: leaksInto(%q) = %q, want leaked = %v", entry.name, entry.source, leaksInto(expression), entry.wantLeaked)
		}
	}
}

// TestAcquisitionVocabulary drives the five acquisition shapes and the calls that
// are not one: the rule is only honest if it names what it watched, and a `Query`
// of a URL is not a row set.
func TestAcquisitionVocabulary(t *testing.T) {
	enterRoot(t)
	cases := []struct {
		name   string
		source string
		kind   string
	}{
		{"o arquivo aberto", "os.Open(path)", "arquivo"},
		{"o arquivo criado", "os.Create(path)", "arquivo"},
		{"a transação aberta", "pool.Begin(ctx)", "transação"},
		{"a transação aberta com opções", "db.BeginTx(ctx, nil)", "transação"},
		{"a resposta HTTP", "client.Do(request)", "resposta HTTP"},
		{"a conexão discada", `net.Dial("tcp", addr)`, "conexão"},
		{"a consulta com contexto", "pool.Query(ctx, query)", "consulta"},
		{"a consulta sem contexto não é julgada", "url.Query()", ""},
		{"a formatação não é aquisição", `fmt.Sprintf("%s", name)`, ""},
		{"o contexto derivado não é aquisição", "context.WithTimeout(ctx, time.Second)", ""},
	}
	for _, entry := range cases {
		expression, err := parser.ParseExpr(entry.source)
		if err != nil {
			t.Fatalf("%s: ParseExpr(%q) = %v", entry.name, entry.source, err)
		}
		call, ok := expression.(*ast.CallExpr)
		if !ok {
			t.Fatalf("%s: %q não é uma chamada", entry.name, entry.source)
		}
		if kind := judgesAcquisition(call); kind != entry.kind {
			t.Errorf("%s: judgesAcquisition(%q) = %q, want %q", entry.name, entry.source, kind, entry.kind)
		}
	}
}

// TestCleanupChainSeparatesTheTwoOwes keeps the transaction's cleanup apart from
// the resource's: a transaction is rolled back on the error path, and asking it
// for a Close would accept a value nobody ever releases.
func TestCleanupChainSeparatesTheTwoOwes(t *testing.T) {
	enterRoot(t)
	if chain := cleanupChain("transação"); chain != ".Rollback" {
		t.Errorf("cleanupChain(transação) = %q, want .Rollback", chain)
	}
	for _, kind := range []string{"arquivo", "resposta HTTP", "conexão", "consulta"} {
		if chain := cleanupChain(kind); chain != ".Close" {
			t.Errorf("cleanupChain(%s) = %q, want .Close", kind, chain)
		}
	}
}

// enterRoot puts the test where the gate runs from: the delivered entry point
// walks the tree, and the fixtures are asked for by a path relative to it. It is
// asked for more than once per test — a fixture helper enters too —, so it reads
// the go.mod of the root to tell "not yet there" from "already there".
func enterRoot(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("go.mod"); err == nil {
		return
	}
	t.Chdir("../..")
}

func scanFixture(t *testing.T, name string) measured {
	t.Helper()
	enterRoot(t)
	measured, err := scanDirectory(fixtureRoot + "/" + name)
	if err != nil {
		t.Fatalf("scanDirectory(%s/%s) = %v", fixtureRoot, name, err)
	}
	return measured
}
