package contract_test

// P26-T03 — corpus hostil e parsing contra HTTP real.
//
// SQL injection, JSON ambíguo/profundo/grande, confusão de path/query,
// Unicode confusável/bidi, template injection, header de email e CRLF,
// e gzip que o servidor nunca decodifica: nada vira panic ou 500 não
// classificado, nada concatena query, limites respondem cedo e o banco
// termina intacto. Achado vira regressão na mesma mudança (o mismatch
// alvo×ato da T02, que respondia 500, agora é 400 com célula própria).

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// hostilePayloads é o corpus de injeção: clássicos de SQLi, tentativas de
// template e controles. Cada um viaja como dado e deve voltar como dado
// recusado ou literal — nunca executado, ecoado em erro ou persistido
// como efeito colateral.
var hostilePayloads = []string{
	`' OR '1'='1`,
	`" OR "1"="1`,
	`'; DROP TABLE app.accounts; --`,
	`1; SELECT pg_sleep(5)`,
	`admin'--`,
	`{{.Password}}`,
	`{{7*7}}`,
	`${7*7}`,
	`<%= 7*7 %>`,
	"line1\nBcc: evil@arena.example.com",
	"subject\r\nInjected: 1",
	"ad\u043cmin@arena.example.com",
	"tok\u202eNE",
	"zero\u200bwidth",
	"\x00nullbyte",
}

// hostileStatus permite as famílias documentadas e proíbe 5xx: um input
// hostil pode ser aceito, recusado ou inexistente — nunca um estouro.
func hostileStatus(t *testing.T, context string, status int) {
	t.Helper()
	if status >= 500 {
		t.Fatalf("%s = %d, want no 5xx for hostile input", context, status)
	}
	if status != 200 && status != 201 && status != 204 && (status < 400 || status >= 500) {
		t.Fatalf("%s = %d, want a documented family", context, status)
	}
}

// hostileDBCounts fotografa as tabelas que injeção não pode tocar.
func hostileDBCounts(t *testing.T, world *journeyWorld) map[string]int64 {
	t.Helper()
	counts := map[string]int64{}
	for _, table := range []string{"app.accounts", "app.arenas", "app.arguments", "app.moderation_reports", "app.checkout_intents"} {
		counts[table] = journeyQueryInt(t, world.pool, `SELECT count(*) FROM `+table)
	}
	return counts
}

func hostileDBUnchanged(t *testing.T, before map[string]int64, world *journeyWorld) {
	t.Helper()
	after := hostileDBCounts(t, world)
	for table, count := range before {
		if after[table] != count {
			t.Fatalf("table %s changed %d -> %d under hostile input", table, count, after[table])
		}
	}
}

// TestHostileSQLInjection dispara o corpus contra cada entrada de texto e
// contra IDs e queries: sem 5xx, sem eco do payload na recusa e sem linha
// nova no banco.
func TestHostileSQLInjection(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	before := hostileDBCounts(t, world)

	targets := []struct {
		method string
		path   func(payload string) string
		body   func(payload string) string
	}{
		{method: "GET", path: func(payload string) string {
			return "/api/v1/search/arenas?q=" + url.QueryEscape(payload) + "&language=pt-BR"
		}},
		{method: "POST", path: func(string) string { return "/api/v1/auth/register" }, body: func(payload string) string {
			encoded, _ := json.Marshal(map[string]string{"email": payload, "password": "t03-correct-horse-1"})
			return string(encoded)
		}},
		{method: "POST", path: func(string) string { return "/api/v1/auth/login" }, body: func(payload string) string {
			encoded, _ := json.Marshal(map[string]string{"email": payload, "password": "t03-correct-horse-1"})
			return string(encoded)
		}},
		{method: "GET", path: func(payload string) string { return "/api/v1/arenas/" + url.PathEscape(payload) }},
		{method: "GET", path: func(payload string) string { return "/api/v1/arguments/" + url.PathEscape(payload) }},
		{method: "GET", path: func(payload string) string { return "/api/v1/arenas?limit=" + url.QueryEscape(payload) }},
	}
	for _, target := range targets {
		for _, payload := range hostilePayloads {
			var body string
			if target.body != nil {
				body = target.body(payload)
			}
			status, _, raw := journeyCall(t, server, target.method, target.path(payload), "", body, nil)
			hostileStatus(t, target.method+" "+target.path(payload), status)
			if status >= 400 && strings.Contains(string(raw), payload) && len(payload) > 8 {
				t.Fatalf("%s refusal echoes the payload", target.method)
			}
		}
	}
	hostileDBUnchanged(t, before, world)
}

// TestHostileJSON prova limites antecipados e uniformidade determinística:
// undecodable e grande caem no 4xx rápido; formas desconhecidas no registro
// caem na uniformidade 201 sem crash; endpoint estrito valida de verdade.
func TestHostileJSON(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	refused := []struct {
		name string
		body string
		code string
	}{
		{name: "truncated", body: `{"email":"a@arena.example.com",`, code: ""},
		{name: "array top", body: `[1,2,3]`, code: ""},
		{name: "oversized", body: `{"email":"` + strings.Repeat("a", 70*1024) + `@arena.example.com","password":"x"}`, code: ""},
		{name: "deep", body: `{"statement":` + strings.Repeat(`{"a":`, 300) + "1" + strings.Repeat(`}`, 300) + `,"category":"technology","language":"pt-BR"}`, code: "json_too_deep"},
	}
	for _, document := range refused {
		started := time.Now()
		status, _, raw := journeyCall(t, server, "POST", "/api/v1/auth/register", "", document.body, nil)
		if elapsed := time.Since(started); elapsed > 10*time.Second {
			t.Fatalf("%s took %s, want an early refusal", document.name, elapsed)
		}
		if status < 400 || status >= 500 {
			t.Fatalf("%s = %d, want 4xx", document.name, status)
		}
		if document.code != "" && !strings.Contains(string(raw), document.code) {
			t.Fatalf("%s refusal lacks stable code %q: %s", document.name, document.code, string(raw))
		}
	}

	// Formas que o registro ignora: uniformidade determinística, sem crash.
	for _, document := range []struct {
		name string
		body string
	}{
		{name: "type swap", body: `{"relation":["support"],"content":{"text":1}}`},
		{name: "duplicate keys", body: `{"email":"t03-dup@arena.example.com","email":"t03-dup2@arena.example.com","password":"x"}`},
	} {
		status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/register", "", document.body, nil)
		if status != 201 {
			t.Fatalf("%s = %d, want uniform 201", document.name, status)
		}
	}

	// Endpoint estrito com sessão: corpo vazio e tipo trocado são 400, e o
	// aninhado profundo não trava o parser.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := world.pool.Exec(ctx, `INSERT INTO app.accounts (email, status, email_verified_at) VALUES ('t03-strict@arena.example.com', 'active', now())`); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	journeySession(t, world.pool, "t03-strict@arena.example.com", "t03-strict-token", false)
	arenaID := journeyArena(t, world.pool, "t03-strict@arena.example.com", "Arena t03 estrita com clareza", "t03-strict-arena")
	strictDeep := `{"relation":"support","content":` + strings.Repeat(`{"a":`, 300) + "1" + strings.Repeat(`}`, 300) + `}`
	for _, document := range []struct {
		name string
		body string
		key  string
	}{
		{name: "empty", body: `{}`, key: "t03-strict-1"},
		{name: "type swap", body: `{"relation":["support"],"content":"x"}`, key: "t03-strict-2"},
		{name: "deep content", body: strictDeep, key: "t03-strict-3"},
	} {
		started := time.Now()
		status, _, _ := journeyCall(t, server, "POST", "/api/v1/me/arenas/"+arenaID+"/arguments", "t03-strict-token", document.body,
			map[string]string{"Idempotency-Key": document.key})
		if elapsed := time.Since(started); elapsed > 10*time.Second {
			t.Fatalf("strict %s took %s", document.name, elapsed)
		}
		if status != 400 {
			t.Fatalf("strict %s = %d, want 400", document.name, status)
		}
	}

	status, _, _ := journeyCall(t, server, "POST", "/api/v1/me/arena-drafts", "", "", nil)
	if status != 401 {
		t.Fatalf("empty body without session = %d, want 401 (not a parser crash)", status)
	}
}

// TestHostileGzip prova que o servidor nunca decodifica: gziprotulado
// chega opaco e cai no 4xx de JSON inválido, sem bomba de descompressão.
func TestHostileGzip(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(bytes.Repeat([]byte("A"), 1<<20)); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	request, err := http.NewRequest("POST", server.URL+"/api/v1/auth/register", &compressed)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	started := time.Now()
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer response.Body.Close()
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("gzip body took %s", elapsed)
	}
	if response.StatusCode < 400 || response.StatusCode >= 500 {
		t.Fatalf("gzip body = %d, want 4xx (never decoded)", response.StatusCode)
	}
}

// TestHostilePathQuery prova que confusão de rota não serve o handler
// errado: travessia, slash duplo e encoded respondem da família
// documentada, e método errado é 405.
func TestHostilePathQuery(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	for _, path := range []string{
		"/api/v1/arenas/../me/wallet",
		"/api/v1//arenas",
		"/api/v1/arenas/%2e%2e/me/wallet",
		"/api/v1/arenas/",
		"/api/v1/arenas/%FF",
		"/api/v1/no-such-resource",
	} {
		status, _, _ := journeyCall(t, server, "GET", path, "", "", nil)
		if status != 200 && status != 400 && status != 401 && status != 404 {
			t.Fatalf("GET %s = %d, want a documented family", path, status)
		}
	}
	status, _, _ := journeyCall(t, server, "DELETE", "/api/v1/arenas", "", "", nil)
	if status != 405 {
		t.Fatalf("DELETE /api/v1/arenas = %d, want 405", status)
	}
}

// TestHostileTemplateAndUnicode grava template injection e Unicode hostil
// e lê de volta: o byte entra e sai literal, sem avaliação nem duplicata
// confusável autenticável.
func TestHostileTemplateAndUnicode(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	const email = "t03-template@arena.example.com"
	const password = "t03-correct-horse-1"
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
	status, loginHeader, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if status != 200 {
		t.Fatalf("login = %d, want 200", status)
	}
	cookie := journeyCookie(t, loginHeader)

	for _, statement := range []string{
		`{{.Password}} misturado com tese {{7*7}}`,
		`${7*7} e <%= 7*7 %> na mesma tese`,
	} {
		body, _ := json.Marshal(map[string]string{"statement": statement, "category": "technology", "language": "pt-BR"})
		status, _, draftRaw := journeyCall(t, server, "POST", "/api/v1/me/arena-drafts", cookie, string(body), nil)
		if status != 201 {
			t.Fatalf("draft = %d, want 201", status)
		}
		var draft struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(draftRaw, &draft); err != nil || draft.ID == "" {
			t.Fatalf("draft carries no id: %s", string(draftRaw))
		}
		status, _, readRaw := journeyCall(t, server, "GET", "/api/v1/me/arena-drafts/"+draft.ID, cookie, "", nil)
		if status != 200 {
			t.Fatalf("draft read = %d, want 200", status)
		}
		var read struct {
			Statement string `json:"statement"`
		}
		if err := json.Unmarshal(readRaw, &read); err != nil {
			t.Fatalf("decode draft: %v", err)
		}
		if read.Statement != statement {
			t.Fatalf("statement round-trip changed %q into %q (evaluated?)", statement, read.Statement)
		}
	}

	// Bidi override é recusado na validação e nada persiste.
	bidiBody, _ := json.Marshal(map[string]string{"statement": "tok\u202eNE invertido com tese", "category": "technology", "language": "pt-BR"})
	bidiStatus, _, _ := journeyCall(t, server, "POST", "/api/v1/me/arena-drafts", cookie, string(bidiBody), nil)
	if bidiStatus != 400 {
		t.Fatalf("bidi statement = %d, want 400", bidiStatus)
	}

	// Email confusável não vira conta segunda: uniformidade 201 sem email,
	// e o login com ele cai no 401 comum.
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/auth/register", "", `{"email":"ad\u043cmin@arena.example.com","password":"`+password+`"}`, nil)
	if status != 201 {
		t.Fatalf("confusable register = %d, want uniform 201", status)
	}
	confusable, _ := identitydomain.ParseEmail("ad\u043cmin@arena.example.com")
	if _, found := world.sender.LastTokenForEmail(confusable); found {
		t.Fatal("confusable address received a verification email")
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/auth/login", "", `{"email":"ad\u043cmin@arena.example.com","password":"`+password+`"}`, nil)
	if status != 401 {
		t.Fatalf("confusable login = %d, want 401", status)
	}
}

// TestHostileEmailHeaders prova que CRLF não entra nem sai: endereço com
// quebra é 400 (ou 201 uniforme sem conta nem email), e a recusa nunca
// ecoa a carga.
func TestHostileEmailHeaders(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	for _, email := range []string{
		"a@arena.example.com\nBcc: evil@arena.example.com",
		"a@arena.example.com\r\nInjected: 1",
		"a@arena.example.com\x00",
	} {
		body, _ := json.Marshal(map[string]string{"email": email, "password": "t03-correct-horse-1"})
		status, _, raw := journeyCall(t, server, "POST", "/api/v1/auth/register", "", string(body), nil)
		hostileStatus(t, "register CRLF", status)
		if status == 201 {
			// Uniformidade anti-oráculo: 201 sem conta, sem email, sem login.
			if parsed, err := identitydomain.ParseEmail(email); err == nil {
				if _, found := world.sender.LastTokenForEmail(parsed); found {
					t.Fatalf("CRLF address %q received an email", email)
				}
			}
			status, _, _ := journeyCall(t, server, "POST", "/api/v1/auth/login", "", string(body), nil)
			if status != 401 {
				t.Fatalf("CRLF login = %d, want 401", status)
			}
		} else if strings.Contains(string(raw), "Bcc:") || strings.Contains(string(raw), "Injected:") {
			t.Fatalf("refusal echoes the injected header: %s", string(raw))
		}
	}
}

// TestHostileTargetMismatch é a regressão do achado da T02: ato que não
// sanciona o alvo respondia 500 não classificado; agora é 400 com código
// estável, sem conceder nada.
func TestHostileTargetMismatch(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	pool := world.pool

	for _, account := range []string{"t03-reporter@arena.example.com", "t03-moder@arena.example.com"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, `INSERT INTO app.accounts (email, status, email_verified_at) VALUES ($1, 'active', now())`, account); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	journeyGrantRole(t, pool, "t03-moder@arena.example.com", "admin")
	arenaID := journeyArena(t, pool, "t03-reporter@arena.example.com", "Arena t03 sob revisão hostil", "t03-hostile-arena")
	journeySession(t, pool, "t03-moder@arena.example.com", "t03-moder-token", true)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var caseID string
	if err := pool.QueryRow(ctx, `INSERT INTO app.moderation_cases (target_type, target_arena_id) VALUES ('arena', $1::uuid) RETURNING id::text`,
		arenaID).Scan(&caseID); err != nil {
		t.Fatalf("open case: %v", err)
	}
	status, _, _ := journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/claim", "t03-moder-token", "", nil)
	if status != 200 {
		t.Fatalf("claim = %d, want 200", status)
	}
	for _, action := range []string{
		`{"action":"argument_remove","rule":"MOD-5:argument_remove","justification":"Matriz hostil t03"}`,
		`{"action":"suspension","rule":"MOD-10:suspension","justification":"Matriz hostil t03","expires_at":"2026-10-18T12:00:00Z"}`,
	} {
		status, _, raw := journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+caseID+"/decisions", "t03-moder-token", action, nil)
		if status != 400 {
			t.Fatalf("mismatched decision = %d, want 400 (was 500 before the mapping fix)", status)
		}
		var document struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(raw, &document); err != nil || document.Code == "" {
			t.Fatalf("mismatch refusal carries no stable code: %s", string(raw))
		}
	}
	if got := journeyQueryInt(t, pool, `SELECT count(*) FROM app.moderation_actions WHERE case_id::text = $1`, caseID); got != 0 {
		t.Fatalf("mismatched decisions wrote %d actions, want 0", got)
	}
}
