// Tests of the Arena lifecycle surface composed in the process (P49-T03):
// drafts, publication spending one Arena Pass, closing, the public feed
// and document, and the public search run on the platform mux over
// disposable PostgreSQL, driven by real HTTP with the same pool and
// security boundary as the account journey — the session the JSON login
// opened is the session the draft routes require, and no second
// authentication exists.
package bootstrap_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	billingpg "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// arenaJourney is the account plus lifecycle surfaces on a real listener,
// sharing one pool and one security boundary like `arena server` does.
type arenaJourney struct {
	account *bootstrap.AccountSurface
	arena   *bootstrap.ArenaSurface
	server  *httptest.Server
	pool    *pgxpool.Pool
}

// newArenaJourney composes both surfaces the way the process does.
func newArenaJourney(t *testing.T) *arenaJourney {
	t.Helper()

	database := dbtest.New(t)
	pool := database.Pool.Pool()
	clock := clockseed.NewClock()
	random := clockseed.NewRandom()
	manager, err := security.New(security.Options{Env: config.EnvTest, Clock: clock, Random: random})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}
	account, err := bootstrap.ComposeAccount(bootstrap.Options{
		Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool: pool, Clock: clock, Random: random, Assets: manifestFixture(t), Security: manager,
	})
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}
	arena, err := bootstrap.ComposeArenaLifecycle(bootstrap.Options{
		Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool: pool, Clock: clock, Random: random, Assets: manifestFixture(t), Security: manager,
		CursorSecret: []byte(cursorSecret),
	})
	if err != nil {
		t.Fatalf("ComposeArenaLifecycle() error = %v", err)
	}
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{},
		[]httpserver.Surface{account.Surface(), arena.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &arenaJourney{account: account, arena: arena, server: server, pool: database.Pool.Pool()}
}

// arenaLogin registers, verifies and signs in one account over the JSON API,
// returning the client holding its session plus the account identifier.
func arenaLogin(t *testing.T, journey *arenaJourney, email, password string) (*http.Client, string) {
	t.Helper()

	client := browser(t)
	status, _, _ := jsonPost(t, client, journey.server, "/api/v1/auth/register", `{"email":`+quoteJSON(email)+`,"password":`+quoteJSON(password)+`}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /api/v1/auth/register status = %d, want 201", status)
	}
	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	token, found := journey.account.LocalSink().LastTokenForEmail(address)
	if !found {
		t.Fatalf("registration of %s issued no token", email)
	}
	if status, _ := jsonGet(t, client, journey.server, "/api/v1/auth/verify?token="+token); status != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/verify status = %d, want 200", status)
	}
	status, raw, _ := jsonPost(t, client, journey.server, "/api/v1/auth/login", `{"email":`+quoteJSON(email)+`,"password":`+quoteJSON(password)+`}`)
	if status != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/login status = %d, want 200 (body: %.200s)", status, raw)
	}
	var document map[string]string
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("login is not JSON: %v", err)
	}
	return client, document["account_id"]
}

// jsonPatch sends one JSON PATCH document without following redirects.
func jsonPatch(t *testing.T, client *http.Client, server *httptest.Server, path, body string) (int, []byte) {
	t.Helper()

	request, err := http.NewRequest(http.MethodPatch, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read PATCH %s: %v", path, err)
	}
	return response.StatusCode, raw
}

// probeMethod issues one bodiless request of any method and returns its status.
func probeMethod(t *testing.T, client *http.Client, server *httptest.Server, method, path string) int {
	t.Helper()

	request, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.ReadAll(response.Body)
	return response.StatusCode
}

// grantPass funds one publication through the billing repository, the same
// path a purchase takes.
func grantPass(t *testing.T, journey *arenaJourney, accountID, reference string) {
	t.Helper()

	quantity, err := billingdomain.NewQuantity(1)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	parsed, err := billingdomain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if _, err := billingpg.NewRepository(journey.pool).GrantPassLot(context.Background(), billingapp.GrantPassLotRequest{
		AccountID: billingdomain.AccountID(accountID),
		Origin:    billingdomain.OriginPurchase,
		Quantity:  quantity,
		Reference: parsed,
		GrantedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("GrantPassLot: %v", err)
	}
}

// createDraft persists one draft over HTTP and returns its opaque identifier.
func createDraft(t *testing.T, client *http.Client, journey *arenaJourney, statement string) string {
	t.Helper()

	status, raw, _ := jsonPost(t, client, journey.server, "/api/v1/me/arena-drafts",
		`{"statement":`+quoteJSON(statement)+`,"category":"technology","language":"pt-BR"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /api/v1/me/arena-drafts status = %d, want 201 (body: %.300s)", status, raw)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("draft is not JSON: %v", err)
	}
	identifier, _ := document["id"].(string)
	if identifier == "" {
		t.Fatalf("draft answers no id: %.200s", raw)
	}
	return identifier
}

// TestArenaDraftPublishFeedDocument proves the lifecycle the process serves:
// draft → publication spending one pass → public feed, JSON document and
// HTML document; a second publication without a pass is refused and leaves
// the draft untouched.
func TestArenaDraftPublishFeedDocument(t *testing.T) {
	t.Parallel()

	journey := newArenaJourney(t)
	owner, ownerID := arenaLogin(t, journey, "arena-lifecycle@example.test", "correct horse battery staple")
	grantPass(t, journey, ownerID, "stripe:lifecycle_first")

	identifier := createDraft(t, owner, journey, "O debate público melhora quando os argumentos podem ser verificados.")

	// The owner reads the draft version, updates the statement and lists
	// their drafts: update and list are served, not just declared.
	status, raw := jsonGet(t, owner, journey.server, "/api/v1/me/arena-drafts/"+identifier)
	if status != http.StatusOK {
		t.Fatalf("GET draft status = %d, want 200 (body: %.300s)", status, raw)
	}
	var draft map[string]any
	if err := json.Unmarshal(raw, &draft); err != nil {
		t.Fatalf("draft is not JSON: %v", err)
	}
	version, _ := draft["version"].(float64)
	versionRaw, err := json.Marshal(int(version))
	if err != nil {
		t.Fatalf("marshal draft version: %v", err)
	}
	status, raw = jsonPatch(t, owner, journey.server, "/api/v1/me/arena-drafts/"+identifier,
		`{"statement":"O debate público melhora quando cada argumento pode ser verificado.","category":"technology","language":"pt-BR","expected_version":`+string(versionRaw)+`}`)
	if status != http.StatusOK {
		t.Fatalf("PATCH draft status = %d, want 200 (body: %.300s)", status, raw)
	}
	if !strings.Contains(string(raw), "cada argumento") {
		t.Errorf("updated draft body = %.300s, want the new statement", raw)
	}
	status, raw = jsonGet(t, owner, journey.server, "/api/v1/me/arena-drafts")
	if status != http.StatusOK {
		t.Fatalf("GET drafts status = %d, want 200", status)
	}
	if !strings.Contains(string(raw), identifier) {
		t.Errorf("draft list body = %.300s, want the draft identifier", raw)
	}

	status, raw, _ = jsonPost(t, owner, journey.server, "/api/v1/me/arena-drafts/"+identifier+"/publish", `{}`)
	if status != http.StatusOK {
		t.Fatalf("POST publish status = %d, want 200 (body: %.300s)", status, raw)
	}
	var published map[string]any
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("published arena is not JSON: %v", err)
	}
	slug, _ := published["slug"].(string)
	if slug == "" {
		t.Fatalf("published arena carries no slug: %.200s", raw)
	}

	status, raw = jsonGet(t, owner, journey.server, "/api/v1/arenas/"+slug)
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/arenas/%s status = %d, want 200", slug, status)
	}
	if !strings.Contains(string(raw), slug) {
		t.Errorf("arena document body = %.300s, want the published slug", raw)
	}
	status, raw = jsonGet(t, owner, journey.server, "/api/v1/arenas?limit=10")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/arenas status = %d, want 200", status)
	}
	if !strings.Contains(string(raw), slug) {
		t.Errorf("feed body = %.300s, want the published slug", raw)
	}
	status, raw = jsonGet(t, owner, journey.server, "/d/"+slug)
	if status != http.StatusOK {
		t.Fatalf("GET /d/%s status = %d, want 200", slug, status)
	}
	if len(raw) == 0 {
		t.Error("GET /d/{slug} answered an empty document: the public read must render the published Arena")
	}

	second := createDraft(t, owner, journey, "Um segundo rascunho que não tem passe para nascer.")
	status, raw, _ = jsonPost(t, owner, journey.server, "/api/v1/me/arena-drafts/"+second+"/publish", `{}`)
	if status != http.StatusConflict {
		t.Fatalf("POST publish without pass status = %d, want 409 (body: %.300s)", status, raw)
	}
	if !strings.Contains(string(raw), "no_pass_available") {
		t.Errorf("publish refusal body = %.200s, want no_pass_available", raw)
	}
	status, raw = jsonGet(t, owner, journey.server, "/api/v1/me/arena-drafts/"+second)
	if status != http.StatusOK {
		t.Fatalf("GET draft after refused publish status = %d, want 200: the refusal must change nothing", status)
	}
	if !strings.Contains(string(raw), "draft") {
		t.Errorf("draft body after refused publish = %.200s, want the draft untouched", raw)
	}
}

// TestArenaDraftOwnerStranger proves ownership: a stranger answers
// not-found on every draft address of another account, anonymous calls
// fail closed, and the owner deletes their own draft.
func TestArenaDraftOwnerStranger(t *testing.T) {
	t.Parallel()

	journey := newArenaJourney(t)
	owner, _ := arenaLogin(t, journey, "arena-owner@example.test", "correct horse battery staple")
	stranger, _ := arenaLogin(t, journey, "arena-stranger@example.test", "correct horse battery staple")
	anonymous := browser(t)

	identifier := createDraft(t, owner, journey, "Um rascunho privado que estranhos não podem ver.")

	for _, probe := range []struct{ method, path string }{
		{"GET", "/api/v1/me/arena-drafts/" + identifier},
		{"POST", "/api/v1/me/arena-drafts/" + identifier + "/publish"},
	} {
		status := probeMethod(t, stranger, journey.server, probe.method, probe.path)
		if status != http.StatusNotFound {
			t.Errorf("%s %s stranger status = %d, want 404", probe.method, probe.path, status)
		}
	}
	if status := probeMethod(t, stranger, journey.server, "DELETE", "/api/v1/me/arena-drafts/"+identifier); status != http.StatusNotFound {
		t.Errorf("DELETE draft stranger status = %d, want 404", status)
	}
	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/me/arena-drafts"); status != http.StatusUnauthorized {
		t.Errorf("GET drafts anonymous status = %d, want 401", status)
	}
	if status, _, _ := jsonPost(t, anonymous, journey.server, "/api/v1/me/arena-drafts", `{}`); status != http.StatusUnauthorized {
		t.Errorf("POST drafts anonymous status = %d, want 401", status)
	}

	if status := probeMethod(t, owner, journey.server, "DELETE", "/api/v1/me/arena-drafts/"+identifier); status != http.StatusNoContent {
		t.Errorf("DELETE draft owner status = %d, want 204", status)
	}
	if status, _ := jsonGet(t, owner, journey.server, "/api/v1/me/arena-drafts/"+identifier); status != http.StatusNotFound {
		t.Errorf("GET deleted draft status = %d, want 404", status)
	}
}

// TestArenaFeedSearchCursor proves the public reads: the feed and both
// search APIs serve anonymously, find the published Arena, and refuse a
// forged cursor without touching the database.
func TestArenaFeedSearchCursor(t *testing.T) {
	t.Parallel()

	journey := newArenaJourney(t)
	owner, ownerID := arenaLogin(t, journey, "arena-search@example.test", "correct horse battery staple")
	grantPass(t, journey, ownerID, "stripe:search_first")
	identifier := createDraft(t, owner, journey, "A busca encontra arenas publicadas pelo texto.")
	if status, _, _ := jsonPost(t, owner, journey.server, "/api/v1/me/arena-drafts/"+identifier+"/publish", `{}`); status != http.StatusOK {
		t.Fatalf("POST publish status = %d, want 200", status)
	}
	anonymous := browser(t)

	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/arenas?cursor=forged"); status != http.StatusBadRequest {
		t.Errorf("GET feed with forged cursor status = %d, want 400", status)
	}
	status, raw := jsonGet(t, anonymous, journey.server, "/api/v1/search/arenas?q=busca")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/search/arenas status = %d, want 200 (body: %.300s)", status, raw)
	}
	if !strings.Contains(string(raw), "busca") {
		t.Errorf("search body = %.300s, want the published statement", raw)
	}
	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/search/arguments?q=busca"); status != http.StatusOK {
		t.Errorf("GET /api/v1/search/arguments status = %d, want 200", status)
	}
	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/search/arenas?cursor=forged&q=busca"); status != http.StatusBadRequest {
		t.Errorf("GET search with forged cursor status = %d, want 400", status)
	}
	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/arenas/no-such-arena"); status != http.StatusNotFound {
		t.Errorf("GET unknown arena document status = %d, want 404", status)
	}
}

// TestArenaClose proves the closing transition: the creator closes,
// closing twice replays without a second transition, a stranger answers
// not-found, and anonymous calls fail closed.
func TestArenaClose(t *testing.T) {
	t.Parallel()

	journey := newArenaJourney(t)
	owner, ownerID := arenaLogin(t, journey, "arena-close@example.test", "correct horse battery staple")
	stranger, _ := arenaLogin(t, journey, "arena-close-stranger@example.test", "correct horse battery staple")
	anonymous := browser(t)
	grantPass(t, journey, ownerID, "stripe:close_first")

	identifier := createDraft(t, owner, journey, "Uma arena que será encerrada pelo criador.")
	if status, _, _ := jsonPost(t, owner, journey.server, "/api/v1/me/arena-drafts/"+identifier+"/publish", `{}`); status != http.StatusOK {
		t.Fatalf("POST publish status = %d, want 200", status)
	}

	status, raw, _ := jsonPost(t, owner, journey.server, "/api/v1/me/arenas/"+identifier+"/close", `{}`)
	if status != http.StatusOK {
		t.Fatalf("POST close status = %d, want 200 (body: %.300s)", status, raw)
	}
	status, raw, _ = jsonPost(t, owner, journey.server, "/api/v1/me/arenas/"+identifier+"/close", `{}`)
	if status != http.StatusOK {
		t.Errorf("POST close twice status = %d, want 200: closing replays without a second transition (body: %.200s)", status, raw)
	}
	if !strings.Contains(string(raw), "closed") {
		t.Errorf("close replay body = %.200s, want the closed arena", raw)
	}
	if status, _, _ := jsonPost(t, stranger, journey.server, "/api/v1/me/arenas/"+identifier+"/close", `{}`); status != http.StatusNotFound {
		t.Errorf("POST close stranger status = %d, want 404", status)
	}
	if status, _, _ := jsonPost(t, anonymous, journey.server, "/api/v1/me/arenas/"+identifier+"/close", `{}`); status != http.StatusUnauthorized {
		t.Errorf("POST close anonymous status = %d, want 401", status)
	}
}
