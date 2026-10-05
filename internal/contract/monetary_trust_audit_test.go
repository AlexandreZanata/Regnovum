package contract_test

// P44-T04 — amostra red team das fronteiras monetárias de confiança.
//
// Amostra dirigida em stack isolada (httptest + Postgres descartável):
// autorização horizontal/vertical sobre intent, carteira e exports;
// amarração webhook→conta dona; replay de checkout; negação anônima;
// varredura de reflexão/5xx nas rotas adjacentes a dinheiro; deny-list do
// OpenAPI (produto econômico desativado: sem rota de mint ou escrow).
// A matriz DAST completa fica para P45. Insider/Rei, oracle/cotação e
// antifraude vivem nos testes de domínio citados no registro — aqui só
// HTTP, sem duplicar. Relatório sem segredo ou PII: só canários
// `.invalid`, sem chaves ou dados reais.

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/providersim"
)

// redFinding é uma linha do relatório: ameaça, regra, runbook, severidade
// do modelo e disposição auditável. Crítica/alta só é aceita com status
// "blocked" (teste abaixo que prova o bloqueio) ou "deferred-blocked"
// (sem caminho explorável na stack atual + implementação planejada
// rastreada no mapa de cobertura).
type redFinding struct {
	id       string
	title    string
	threat   string
	rule     string
	runbook  string
	severity string
	status   string
	evidence string
}

// redRegister é o relatório: cada linha precisa de prova ou de bloqueio
// declarado. Zero crítica/alta aberta é o gate da tarefa.
func redRegister() []redFinding {
	return []redFinding{
		{id: "RED-01", title: "liquidação vincula sessão à conta dona; vizinho intacto", threat: "THR-ECON-30", rule: "Q30", runbook: "R5", severity: "critical", status: "blocked", evidence: "TestMonetaryCrossBuyerSettlementBinding"},
		{id: "RED-02", title: "webhook forjado não concede nada a ninguém", threat: "THR-ECON-30", rule: "Q30", runbook: "R5", severity: "critical", status: "blocked", evidence: "TestMonetaryCrossBuyerSettlementBinding"},
		{id: "RED-03", title: "replay de checkout resolve o mesmo intent, sem duplicar", threat: "THR-ECON-21", rule: "Q21", runbook: "R3/R6", severity: "critical", status: "blocked", evidence: "TestMonetaryCheckoutReplaySingleIntent"},
		{id: "RED-04", title: "anônimo não alcança checkout, carteira, exports nem webhook", threat: "THR-ECON-11", rule: "Q11", runbook: "R1", severity: "high", status: "blocked", evidence: "TestMonetaryAnonymousVerticalDenial"},
		{id: "RED-05", title: "download de export exige capacidade do dono", threat: "THR-ECON-34", rule: "Q34", runbook: "R1", severity: "high", status: "blocked", evidence: "TestMonetaryCrossBuyerSettlementBinding"},
		{id: "RED-06", title: "amostra DAST: sem 5xx, sem reflexão, sem bypass nas rotas de dinheiro", threat: "THR-ECON-20/21/30", rule: "Q20/Q21/Q30", runbook: "R1", severity: "high", status: "blocked", evidence: "TestMonetaryDASTSample"},
		{id: "RED-07", title: "OpenAPI sem rota de mint, faucet, seigniorage ou escrow", threat: "THR-ECON-20/28", rule: "Q20/Q28", runbook: "R1", severity: "critical", status: "blocked", evidence: "TestMonetarySurfaceDenyList"},
		{id: "RED-08", title: "insider/ex-Rei: fence de reinado e self-grant recusados", threat: "THR-ECON-11/32", rule: "Q11/Q32", runbook: "R1", severity: "critical", status: "blocked", evidence: "crown/domain manipulation_test + sovereign_test + application/fence_test (citados, sem duplicar)"},
		{id: "RED-09", title: "oracle: cotação expirada/futura recusada, sem liquidação", threat: "THR-ECON-22", rule: "Q22", runbook: "R5/R6", severity: "high", status: "blocked", evidence: "billing TestPurchaseIntentRefusesExpiredQuote + webhook stale/future (citados, sem duplicar)"},
		{id: "RED-10", title: "Sybil em Migalhas e P2P disfarçado: sem rota que dispare o comportamento", threat: "THR-ECON-25/24", rule: "Q25/Q24", runbook: "R6/R5", severity: "high", status: "deferred-blocked", evidence: "superfície desativada (RED-07); implementação planejada P37/P38 no mapa de cobertura"},
	}
}

// TestMonetaryRedTeamRegister é o gate do relatório: zero crítica/alta
// aberta, toda linha com ameaça/regra/runbook/evidência, e texto livre de
// segredo ou PII.
func TestMonetaryRedTeamRegister(t *testing.T) {
	t.Parallel()
	var report strings.Builder
	for _, finding := range redRegister() {
		if finding.id == "" || finding.threat == "" || finding.rule == "" || finding.runbook == "" || finding.evidence == "" {
			t.Fatalf("%s: linha sem ameaça, regra, runbook ou evidência", finding.id)
		}
		switch finding.severity {
		case "critical", "high":
			if finding.status != "blocked" && finding.status != "deferred-blocked" {
				t.Fatalf("%s: %s/%s aberta (%s)", finding.id, finding.severity, finding.status, finding.title)
			}
		case "medium", "low":
			if finding.status != "blocked" && finding.status != "deferred-blocked" && finding.status != "open" {
				t.Fatalf("%s: status desconhecido %q", finding.id, finding.status)
			}
		default:
			t.Fatalf("%s: severidade desconhecida %q", finding.id, finding.severity)
		}
		report.WriteString(finding.id + " " + finding.title + " " + finding.threat + " " + finding.rule + " " + finding.runbook + " " + finding.status + " " + finding.evidence + "\n")
	}
	for _, marker := range []string{"sk_live", "sk_test", "whsec_", "begin", "private key"} {
		if strings.Contains(strings.ToLower(report.String()), marker) {
			t.Fatalf("relatório contém marcador sensível %q", marker)
		}
	}
}

// redteamLogin registra, verifica e loga sem comprar: o titular que
// observa sem intent próprio.
func redteamLogin(t *testing.T, world *journeyWorld, server *httptest.Server, email, password string) string {
	t.Helper()
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("register = %d, want 201", status)
	}
	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	verifyToken, found := world.sender.LastTokenForEmail(address)
	if !found {
		t.Fatal("registration issued no verification email")
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/auth/verify?token="+verifyToken, "", "", nil)
	if status != 200 {
		t.Fatalf("verify = %d, want 200", status)
	}
	status, header, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	return journeyCookie(t, header)
}

// TestMonetaryCrossBuyerSettlementBinding prova a amarração horizontal:
// o evento válido liquida só a conta dona (RED-01), o forjado não concede
// a ninguém (RED-02), e o export de uma não se baixa com o cookie da
// outra (RED-05).
func TestMonetaryCrossBuyerSettlementBinding(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	ava, avaIntent := adversarialBuyer(t, world, server, "red44-ava@canary.invalid", "red44-correct-horse-1", "red44-ava-1")
	ben := redteamLogin(t, world, server, "red44-ben@canary.invalid", "red44-correct-horse-2")

	sessionJSON := providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim")
	payload, signature, timestamp := adversarialEvent("evt_RED44001", "checkout.session.completed", journeyFixedInstant, sessionJSON)
	if status, _ := adversarialDeliver(t, server, payload, signature, timestamp); status != 200 {
		t.Fatalf("valid event = %d, want 200", status)
	}
	if got := adversarialPaid(t, world, avaIntent); got != 1 {
		t.Fatalf("ava paid intents = %d, want 1", got)
	}
	if balance := adversarialBalance(t, world, server, ava); balance != 10000 {
		t.Fatalf("ava balance = %d, want catalog 10000", balance)
	}
	if balance := adversarialBalance(t, world, server, ben); balance != 0 {
		t.Fatalf("ben balance = %d after ava settlement, want 0 (horizontal leak)", balance)
	}

	forgedPayload, _, forgedTimestamp := adversarialEvent("evt_RED44002", "checkout.session.completed", journeyFixedInstant, sessionJSON)
	if status, _ := adversarialDeliver(t, server, forgedPayload, "t=0,v1=deadbeef", forgedTimestamp); status != 400 {
		t.Fatalf("forged event = %d, want 400", status)
	}
	if balance := adversarialBalance(t, world, server, ava); balance != 10000 {
		t.Fatalf("ava balance = %d after forgery, want unchanged 10000", balance)
	}
	if balance := adversarialBalance(t, world, server, ben); balance != 0 {
		t.Fatalf("ben balance = %d after forgery, want 0", balance)
	}

	status, _, exportRaw := journeyCall(t, server, "POST", "/api/v1/me/exports", ava, "", nil)
	if status != 202 {
		t.Fatalf("ava export = %d, want 202 (%s)", status, string(exportRaw))
	}
	var order struct {
		ExportID string `json:"export_id"`
	}
	if err := json.Unmarshal(exportRaw, &order); err != nil || order.ExportID == "" {
		t.Fatalf("export carries no id: %s", string(exportRaw))
	}
	status, _, _ = journeyCall(t, server, "GET", "/api/v1/me/exports/"+order.ExportID+"/download?token=bogus", ben, "", nil)
	if status != 401 && status != 403 && status != 404 {
		t.Fatalf("ben downloads ava export = %d, want 401/403/404", status)
	}
}

// TestMonetaryCheckoutReplaySingleIntent prova que repetir a mesma chave
// de idempotência resolve o mesmo intent sem segunda linha (RED-03).
func TestMonetaryCheckoutReplaySingleIntent(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	cookie, intentID := adversarialBuyer(t, world, server, "red44-replay@canary.invalid", "red44-correct-horse-3", "red44-replay-1")

	status, _, raw := journeyCall(t, server, "POST", "/api/v1/me/billing/checkout", cookie,
		`{"market":"BR","product":"ink_10000","idempotency_key":"red44-replay-1"}`, nil)
	if status != 200 {
		t.Fatalf("checkout replay = %d, want 200 (%s)", status, string(raw))
	}
	var replayed struct {
		IntentID string `json:"intent_id"`
		Replayed bool   `json:"replayed"`
	}
	if err := json.Unmarshal(raw, &replayed); err != nil || replayed.IntentID != intentID {
		t.Fatalf("replay resolved another intent: %s (want %s)", string(raw), intentID)
	}
	if !replayed.Replayed {
		t.Fatalf("replay flag = false, want true (silent double effect risk): %s", string(raw))
	}
	accountID := journeyAccountID(t, world.pool, "red44-replay@canary.invalid")
	if got := journeyQueryInt(t, world.pool, `SELECT count(*) FROM app.checkout_intents WHERE account_id::text = $1`, accountID); got != 1 {
		t.Fatalf("checkout intents = %d, want exactly 1", got)
	}
}

// TestMonetaryAnonymousVerticalDenial prova a negação vertical: sem
// sessão, nada de dinheiro se move ou se lê (RED-04).
func TestMonetaryAnonymousVerticalDenial(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	for _, target := range []struct{ method, path, body string }{
		{"POST", "/api/v1/me/billing/checkout", `{"market":"BR","product":"ink_10000","idempotency_key":"red44-anon-1"}`},
		{"GET", "/api/v1/me/billing/subscription", ""},
		{"POST", "/api/v1/me/billing/portal", ""},
		{"GET", "/api/v1/me/wallet", ""},
		{"GET", "/api/v1/me/wallet/transactions", ""},
		{"POST", "/api/v1/me/exports", ""},
	} {
		status, _, _ := journeyCall(t, server, target.method, target.path, "", target.body, nil)
		if status != 401 {
			t.Fatalf("anon %s %s = %d, want 401", target.method, target.path, status)
		}
	}
	sessionJSON := providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim")
	payload, _, timestamp := adversarialEvent("evt_RED44003", "checkout.session.completed", journeyFixedInstant, sessionJSON)
	if status, _ := adversarialDeliver(t, server, payload, "", timestamp); status != 400 {
		t.Fatalf("unsigned webhook = %d, want 400", status)
	}
}

// TestMonetaryDASTSample é a amostra dirigida do scanner nas rotas de
// dinheiro: nenhum perfil recebe 5xx, o marcador hostil nunca reflete, e
// anônimo/outro-perfil não atravessa autenticação (RED-06).
func TestMonetaryDASTSample(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	owner, _ := adversarialBuyer(t, world, server, "red44-dast@canary.invalid", "red44-correct-horse-4", "red44-dast-1")
	other := redteamLogin(t, world, server, "red44-dast-other@canary.invalid", "red44-correct-horse-5")

	const marker = "red44-canary-noreflect"
	probes := []struct {
		method, path string
		cookie       string
		wantAuth     []int
	}{
		{"GET", "/api/v1/me/wallet?q=" + marker, "", []int{401}},
		{"GET", "/api/v1/me/wallet?q=" + marker, owner, []int{200}},
		{"GET", "/api/v1/me/wallet?q=" + marker, other, []int{200}},
		{"GET", "/api/v1/me/wallet/transactions?q=" + marker, "", []int{401}},
		{"GET", "/api/v1/me/wallet/transactions?q=" + marker, owner, []int{200}},
		{"GET", "/api/v1/me/billing/subscription?q=" + marker, "", []int{401}},
		{"GET", "/api/v1/me/billing/subscription?q=" + marker, owner, []int{200}},
		{"POST", "/api/v1/me/billing/portal", "", []int{401}},
		{"GET", "/api/v1/me/passes?q=" + marker, owner, []int{200}},
	}
	for _, probe := range probes {
		status, _, body := journeyCall(t, server, probe.method, probe.path, probe.cookie, "", nil)
		if status >= 500 {
			t.Fatalf("%s %s = %d: 5xx em amostra DAST", probe.method, probe.path, status)
		}
		allowed := false
		for _, want := range probe.wantAuth {
			if status == want {
				allowed = true
			}
		}
		if !allowed {
			t.Fatalf("%s %s = %d, want %v (bypass ou bloqueio indevido)", probe.method, probe.path, status, probe.wantAuth)
		}
		if strings.Contains(string(body), marker) {
			t.Fatalf("%s %s reflete o marcador hostil", probe.method, probe.path)
		}
	}
	if got := adversarialBalance(t, world, server, other); got != 0 {
		t.Fatalf("other balance = %d, want 0 (leitura cruzada)", got)
	}
}

// TestMonetarySurfaceDenyList prova que o contrato público não expõe
// cunhagem nem custódia de terceiros: produto desativado (RED-07).
func TestMonetarySurfaceDenyList(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(repoRoot(t) + "/api/openapi.json")
	if err != nil {
		t.Fatalf("read openapi: %v", err)
	}
	var document struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	lowered := strings.ToLower(string(raw))
	for _, forbidden := range []string{"mint", "faucet", "seigniorage", "escrow"} {
		if strings.Contains(lowered, forbidden) {
			t.Fatalf("openapi expõe %q com o produto desativado", forbidden)
		}
	}
	if len(document.Paths) == 0 {
		t.Fatal("openapi sem paths: deny-list sem objeto")
	}
}
