package contract_test

// P26-T04 — fronteiras de confiança HTTP sobre o servidor real.
//
// Host/origin/proxy forjados, headers de segurança, partição de cache,
// redirects, ausência de fetch de URL de usuário e listener administrativo
// fora da superfície pública: spoofing não escala privilégio nem envenena
// cache, autenticado nunca é público e schemes/credenciais perigosos caem.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// trustCall faz uma chamada com Host forjado opcional e sem seguir
// redirects: o Location de um redirect é evidência, não detalhe.
func trustCall(t *testing.T, serverURL, method, path, host, cookie, body string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	client := &http.Client{
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	var request *http.Request
	var err error
	if body != "" {
		request, err = http.NewRequest(method, serverURL+path, reader)
	} else {
		request, err = http.NewRequest(method, serverURL+path, nil)
	}
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if host != "" {
		request.Host = host
	}
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: cookie})
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer response.Body.Close()
	responseBody := make([]byte, 0, 1<<20)
	buffer := make([]byte, 32*1024)
	for {
		count, readErr := response.Body.Read(buffer)
		responseBody = append(responseBody, buffer[:count]...)
		if readErr != nil {
			break
		}
	}
	assertNoSecrets(t, method+" "+path, responseBody)
	return response.StatusCode, response.Header, responseBody
}

// trustAccount semeia conta ativa verificada com sessão e devolve o token.
func trustAccount(t *testing.T, world *journeyWorld, email, token string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := world.pool.Exec(ctx, `INSERT INTO app.accounts (email, status, email_verified_at) VALUES ($1, 'active', now())`, email); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	journeySession(t, world.pool, email, token, false)
}

// TestTrustHostSpoof prova que Host e forwarding headers forjados não
// decidem nada: a resposta serve, nada reflete o host atacante em
// redirect, cookie ou corpo.
func TestTrustHostSpoof(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	for _, path := range []string{"/api/v1/arenas", "/api/v1/public/transparency"} {
		status, header, body := trustCall(t, server.URL, "GET", path, "evil.invalid", "", "", map[string]string{
			"X-Forwarded-Host":  "evil2.invalid",
			"X-Forwarded-Proto": "https",
			"X-Forwarded-For":   "1.2.3.4",
		})
		if status != 200 {
			t.Fatalf("GET %s with forged host = %d, want 200 (host is not a routing factor)", path, status)
		}
		if location := header.Get("Location"); strings.Contains(location, "evil") {
			t.Fatalf("GET %s redirects to the forged host: %q", path, location)
		}
		if strings.Contains(string(body), "evil.invalid") || strings.Contains(string(body), "evil2.invalid") {
			t.Fatalf("GET %s reflects the forged host in the body", path)
		}
	}

	status, header, _ := trustCall(t, server.URL, "POST", "/api/v1/auth/register", "evil.invalid", "",
		`{"email":"t04-sploof@arena.example.com","password":"t04-correct-horse-1"}`, nil)
	if status != 201 {
		t.Fatalf("register with forged host = %d, want 201", status)
	}
	if domain := header.Get("Set-Cookie"); strings.Contains(strings.ToLower(domain), "domain=evil") {
		t.Fatalf("cookie pins the forged host: %q", domain)
	}
}

// TestTrustOriginSpoof prova que Origin forjado não abre CORS nem muda a
// decisão: sem eco de Allow-Origin e sem bypass de autenticação.
func TestTrustOriginSpoof(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	status, header, _ := trustCall(t, server.URL, "POST", "/api/v1/auth/login", "", "",
		`{"email":"ghost@arena.example.com","password":"x"}`, map[string]string{"Origin": "https://evil.invalid"})
	if status != 401 {
		t.Fatalf("login with forged origin = %d, want 401", status)
	}
	if allow := header.Get("Access-Control-Allow-Origin"); allow == "https://evil.invalid" || allow == "*" {
		t.Fatalf("CORS echoes the forged origin: %q", allow)
	}

	status, _, _ = trustCall(t, server.URL, "GET", "/api/v1/me/wallet", "", "",
		"", map[string]string{"Origin": "https://evil.invalid"})
	if status != 401 {
		t.Fatalf("private read with forged origin = %d, want 401", status)
	}
}

// TestTrustSecurityHeaders prova a política em toda classe de resposta:
// nosniff, referrer contido, CSP e permissions sempre; HSTS só em
// produção (ausente aqui por desenho de ambiente).
func TestTrustSecurityHeaders(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	trustAccount(t, world, "t04-headers@arena.example.com", "t04-headers-token")

	targets := []struct {
		method string
		path   string
		cookie string
		body   string
	}{
		{"GET", "/api/v1/arenas", "", ""},
		{"GET", "/api/v1/me/wallet", "t04-headers-token", ""},
		{"GET", "/api/v1/no-such-resource", "", ""},
		{"GET", "/api/v1/me/wallet", "", ""},
		{"POST", "/api/v1/auth/register", "", `{"email":"not-an-email","password":"x"}`},
	}
	for _, target := range targets {
		_, header, _ := trustCall(t, server.URL, target.method, target.path, "", target.cookie, target.body, nil)
		if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s %s: X-Content-Type-Options = %q, want nosniff", target.method, target.path, got)
		}
		if got := header.Get("Referrer-Policy"); got == "" {
			t.Fatalf("%s %s: no Referrer-Policy", target.method, target.path)
		}
		if got := header.Get("Content-Security-Policy"); got == "" {
			t.Fatalf("%s %s: no Content-Security-Policy", target.method, target.path)
		}
		if got := header.Get("Permissions-Policy"); got == "" {
			t.Fatalf("%s %s: no Permissions-Policy", target.method, target.path)
		}
		if got := header.Get("Strict-Transport-Security"); got != "" {
			t.Fatalf("%s %s: HSTS in non-production = %q", target.method, target.path, got)
		}
	}
}

// TestTrustCachePartition prova que autenticado nunca é público e que o
// público carrega Vary: query-buster não promove privado, e Set-Cookie
// nunca viaja com cache público.
func TestTrustCachePartition(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	trustAccount(t, world, "t04-cache@arena.example.com", "t04-cache-token")

	status, header, _ := trustCall(t, server.URL, "GET", "/api/v1/public/transparency", "", "", "", nil)
	if status != 200 {
		t.Fatalf("transparency = %d, want 200", status)
	}
	if cache := header.Get("Cache-Control"); !strings.Contains(cache, "public") {
		t.Fatalf("transparency Cache-Control = %q, want public", cache)
	}
	if vary := header.Get("Vary"); vary == "" {
		t.Fatal("transparency carries no Vary on a negotiated public document")
	}

	for _, path := range []string{"/api/v1/me/wallet", "/api/v1/me/wallet?t=12345", "/api/v1/me/passes"} {
		status, header, _ := trustCall(t, server.URL, "GET", path, "", "t04-cache-token", "", nil)
		if status != 200 {
			t.Fatalf("GET %s = %d, want 200", path, status)
		}
		if cache := header.Get("Cache-Control"); strings.Contains(cache, "public") {
			t.Fatalf("GET %s Cache-Control = %q, want never public", path, cache)
		}
	}

	_, header, _ = trustCall(t, server.URL, "POST", "/api/v1/auth/login", "", "",
		`{"email":"t04-cache@arena.example.com","password":"wrong-password-9"}`, nil)
	if cache := header.Get("Cache-Control"); strings.Contains(cache, "public") {
		t.Fatalf("login refusal Cache-Control = %q, want never public", cache)
	}
	if setCookie := header.Get("Set-Cookie"); setCookie != "" {
		t.Fatalf("failed login sets a cookie: %q", setCookie)
	}

	status, header, _ = trustCall(t, server.URL, "POST", "/api/v1/me/arena-drafts", "", "t04-cache-token",
		`{"statement":"Rascunho t04 com clareza suficiente","category":"technology","language":"pt-BR"}`, nil)
	if status != 201 {
		t.Fatalf("draft = %d, want 201", status)
	}
	if cache := header.Get("Cache-Control"); strings.Contains(cache, "public") {
		t.Fatalf("draft write Cache-Control = %q, want never public", cache)
	}
}

// TestTrustNoUserURLFetch prova que URL de usuário é texto inerte: publica
// rápido mesmo com host inalcançável no conteúdo e lê de volta literal,
// sem expansão de fetch do servidor.
func TestTrustNoUserURLFetch(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	trustAccount(t, world, "t04-fetch@arena.example.com", "t04-fetch-token")
	journeyFund(t, world.pool, "t04-fetch@arena.example.com", "fetch", 100000)
	arenaID := journeyArena(t, world.pool, "t04-fetch@arena.example.com", "Arena t04 com clareza suficiente", "t04-fetch-arena")

	content := "Leia em http://127.0.0.1:9/unroutable e em https://evil.invalid/x com atenção suficiente"
	body, _ := json.Marshal(map[string]string{"relation": "support", "content": content})
	started := time.Now()
	status, _, raw := trustCall(t, server.URL, "POST", "/api/v1/me/arenas/"+arenaID+"/arguments", "", "t04-fetch-token", string(body),
		map[string]string{"Idempotency-Key": "t04-fetch-1"})
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("publish with URLs took %s (server-side fetch?)", elapsed)
	}
	if status != 200 && status != 201 {
		t.Fatalf("publish = %d, want 200 or 201 (%s)", status, string(raw))
	}
	var published struct {
		Argument struct {
			ID string `json:"id"`
		} `json:"argument"`
	}
	if err := json.Unmarshal(raw, &published); err != nil || published.Argument.ID == "" {
		t.Fatalf("publish carries no id: %s", string(raw))
	}
	status, _, readRaw := trustCall(t, server.URL, "GET", "/api/v1/arguments/"+published.Argument.ID, "", "", "", nil)
	if status != 200 {
		t.Fatalf("argument read = %d, want 200", status)
	}
	if !strings.Contains(string(readRaw), "http://127.0.0.1:9/unroutable") {
		t.Fatalf("stored content lost the literal URL: %s", string(readRaw))
	}
}

// TestTrustAdminNotPublic prova que a superfície operacional não está no
// mux público: métricas, pprof e prefixos administrativos são 404 aqui
// (o listener administrativo é loopback dedicado, fora deste mux).
func TestTrustAdminNotPublic(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	for _, path := range []string{"/metrics", "/debug/pprof/", "/debug/pprof/cmdline", "/admin", "/admin/metrics"} {
		status, _, _ := trustCall(t, server.URL, "GET", path, "", "", "", nil)
		if status == 200 {
			t.Fatalf("GET %s = 200 on the public mux", path)
		}
	}
}

// TestTrustClientIPUntrusted prova que forwarding forjado não muda a
// decisão: 401 continua 401 com XFF absurdo, e lixo no header não é 500.
func TestTrustClientIPUntrusted(t *testing.T) {
	t.Parallel()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)

	for _, xff := range []string{"1.2.3.4", "1.2.3.4, 5.6.7.8", "garbage", "999.999.999.999", ""} {
		status, _, _ := trustCall(t, server.URL, "GET", "/api/v1/me/wallet", "", "", "",
			map[string]string{"X-Forwarded-For": xff})
		if status != 401 {
			t.Fatalf("private read with XFF %q = %d, want 401", xff, status)
		}
		status, _, _ = trustCall(t, server.URL, "POST", "/api/v1/auth/login", "", "",
			`{"email":"ghost@arena.example.com","password":"x"}`, map[string]string{"X-Forwarded-For": xff})
		if status != 401 {
			t.Fatalf("login with XFF %q = %d, want 401", xff, status)
		}
	}
}
