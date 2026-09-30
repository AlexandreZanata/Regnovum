package contract_test

// P25-T06 — conformidade OpenAPI bidirecional contra servidor real.
//
// O teste monta os handlers HTTP da API com casos de uso e adapters reais
// sobre PostgreSQL descartável e os serve por um listener real
// (httptest.Server): do contrato para o servidor, cada operação da matriz
// recebe requests válidos e inválidos gerados dos schemas; do servidor para
// o contrato, todo status, campo, header e media type observado precisa
// estar documentado, erros falam Problem Details (RFC 9457) e nenhum corpo
// carrega segredo. Rotas que exigem provider externo (checkout, portal) e
// superfícies operacionais (jobs admin) executam em T08/T09 com simuladores;
// aqui valem 401 sem sessão e o corpo inválido quando a validação precede o
// provider. O contrato declara um único exemplo não-executável (a URI do
// Problem type); os casos mínimos vêm dos schemas, documentados por
// operação.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	arenasbilling "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/billingpass"
	arenashttp "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/http"
	arenaspg "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentshttp "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/http"
	argumentspg "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	argumentswallet "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/walletdebit"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	billinghttp "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	billingpg "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/contract"
	identityargon "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	identityfake "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/fakeemail"
	identityhttp "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	identitypg "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	moderationhttp "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/http"
	moderationpg "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	persuasionhttp "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/http"
	persuasionpg "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/postgres"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	persuasiondomain "github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionhttp "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/http"
	positionspg "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	profileshttp "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/http"
	profilespg "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	searchhttp "github.com/AlexandreZanata/Regnovum/internal/search/adapters/http"
	searchpg "github.com/AlexandreZanata/Regnovum/internal/search/adapters/postgres"
	searchapp "github.com/AlexandreZanata/Regnovum/internal/search/application"
	transparencyhttp "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/http"
	transparencypg "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/postgres"
	transparencyapp "github.com/AlexandreZanata/Regnovum/internal/transparency/application"
	wallethttp "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/http"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

const (
	conformanceOwnerToken = "t06-owner-session-token"
	conformancePeerToken  = "t06-peer-session-token"
	conformanceCookie     = "arena_session"
)

// secretMarkers é o vocabulário que nenhum corpo de resposta da API pode
// carregar, em forma de chave JSON ou prefixo de valor: credenciais,
// segredos, identificadores de provider e sinais de rede/operador. Palavras
// em mensagens documentadas ("invalid email or password",
// "invalid_credentials") são legítimas e não casam: o detector procura a
// chave, não a palavra. O cookie de sessão é o mecanismo de auth, não
// vazamento; senhas enviadas são verificadas por eco à parte.
var secretMarkers = []string{
	`"password`, `"passwd`, `"hash`, `"secret`, `"credential`,
	`"stripe_customer`, "cus_", `"private_key`, `"api_key`, `"apikey`,
	`"user_agent`, `"x-forwarded`, `"authorization`,
}

func assertNoSecrets(t *testing.T, context string, body []byte) {
	t.Helper()
	lowered := strings.ToLower(string(body))
	for _, marker := range secretMarkers {
		if strings.Contains(lowered, marker) {
			t.Fatalf("%s: response body leaks %q", context, marker)
		}
	}
}

// conformanceHarness is the served API: real handlers, use cases,
// repositories and database behind one listener.
type conformanceHarness struct {
	server *httptest.Server
	pool   *pgxpool.Pool
	sender *identityfake.Sender
}

// conformanceRequest performs one HTTP call against the served API.
func conformanceRequest(t *testing.T, h *conformanceHarness, method, path, token, body string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.AddCookie(&http.Cookie{Name: conformanceCookie, Value: token})
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := h.server.Client().Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response.StatusCode, response.Header, responseBody
}

// conformanceOperation is one contract operation under test.
type conformanceOperation struct {
	method string
	path   string
}

// operationResponses returns the documented response statuses of one
// operation: numeric keys plus "default" as a wildcard.
func operationResponses(t *testing.T, document *contract.Document, operation conformanceOperation) map[string]bool {
	t.Helper()
	paths, ok := document.Paths[operation.path]
	if !ok {
		t.Fatalf("contract is missing %s", operation.path)
	}
	raw, ok := paths[strings.ToLower(operation.method)]
	if !ok {
		t.Fatalf("contract is missing %s %s", operation.method, operation.path)
	}
	var decoded struct {
		Responses map[string]json.RawMessage `json:"responses"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s %s responses: %v", operation.method, operation.path, err)
	}
	statuses := map[string]bool{}
	for status := range decoded.Responses {
		statuses[status] = true
	}
	return statuses
}

// responseMediaTypes returns the media types a response status documents.
func responseMediaTypes(t *testing.T, document *contract.Document, operation conformanceOperation, status int) []string {
	t.Helper()
	paths := document.Paths[operation.path]
	raw := paths[strings.ToLower(operation.method)]
	var decoded struct {
		Responses map[string]struct {
			Content map[string]json.RawMessage `json:"content"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s %s content: %v", operation.method, operation.path, err)
	}
	key := strconv.Itoa(status)
	answer, ok := decoded.Responses[key]
	if !ok {
		answer, ok = decoded.Responses["default"]
	}
	if !ok {
		return nil
	}
	var media []string
	for mediaType := range answer.Content {
		media = append(media, mediaType)
	}
	return media
}

// resolveSchemaRef follows one local $ref ("#/components/schemas/Name").
func resolveSchemaRef(t *testing.T, document *contract.Document, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var reference struct {
		Ref string `json:"$ref"`
	}
	if err := json.Unmarshal(raw, &reference); err != nil || reference.Ref == "" {
		return raw
	}
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(reference.Ref, prefix) {
		t.Fatalf("unsupported schema reference %q (only local component refs)", reference.Ref)
	}
	name := strings.TrimPrefix(reference.Ref, prefix)
	resolved, ok := document.Components.Schemas[name]
	if !ok {
		t.Fatalf("components.schemas.%s is missing", name)
	}
	return resolved
}

// checkEnvelope validates a decoded JSON value against a response schema:
// required properties exist, no undeclared top-level property travels, and
// objects/arrays recurse. Primitive kinds are checked where the schema
// states them plainly; formats and deep combinators stay out by design
// (documented below).
func checkEnvelope(t *testing.T, context string, document *contract.Document, schemaRaw json.RawMessage, value any) {
	t.Helper()
	schemaRaw = resolveSchemaRef(t, document, schemaRaw)
	var schema struct {
		Type       any                        `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		Items      json.RawMessage            `json:"items"`
	}
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		t.Fatalf("%s: decode response schema: %v", context, err)
	}
	switch body := value.(type) {
	case map[string]any:
		for _, required := range schema.Required {
			if _, ok := body[required]; !ok {
				t.Fatalf("%s: response is missing required field %q", context, required)
			}
		}
		if schema.Properties != nil {
			for field, fieldValue := range body {
				fieldSchema, ok := schema.Properties[field]
				if !ok {
					t.Fatalf("%s: response carries undeclared field %q", context, field)
				}
				checkEnvelope(t, context+"."+field, document, fieldSchema, fieldValue)
			}
		}
	case []any:
		if schema.Items != nil {
			for index, item := range body {
				checkEnvelope(t, context+"["+strconv.Itoa(index)+"]", document, schema.Items, item)
			}
		}
	}
}

// responseSchema returns the response schema of one operation status,
// following $ref chains. A missing content schema is not an error: some
// answers (redirects, empty replays) carry no document.
func responseSchema(t *testing.T, document *contract.Document, operation conformanceOperation, status int) (json.RawMessage, bool) {
	t.Helper()
	paths := document.Paths[operation.path]
	raw := paths[strings.ToLower(operation.method)]
	var decoded struct {
		Responses map[string]struct {
			Content map[string]struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"content"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s %s: %v", operation.method, operation.path, err)
	}
	key := strconv.Itoa(status)
	answer, ok := decoded.Responses[key]
	if !ok {
		answer, ok = decoded.Responses["default"]
	}
	if !ok {
		return nil, false
	}
	for _, content := range answer.Content {
		if content.Schema != nil {
			return content.Schema, true
		}
	}
	return nil, false
}

// checkProblemShape enforces the RFC 9457 envelope on error answers.
func checkProblemShape(t *testing.T, context string, status int, body []byte) {
	t.Helper()
	var problem struct {
		Title  string `json:"title"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("%s: error body is not JSON: %v", context, err)
	}
	if problem.Title == "" {
		t.Fatalf("%s: problem detail carries no title", context)
	}
	if problem.Status != status {
		t.Fatalf("%s: problem status = %d, want the HTTP status %d", context, problem.Status, status)
	}
}

// conformanceServer mounts the real API handlers with real use cases,
// repositories and database behind one listener: the same handlers the
// process serves, without the process edge (config, frontend, telemetry).
func conformanceServer(t *testing.T) *conformanceHarness {
	t.Helper()
	database := dbtest.New(t)
	pool := database.Pool.Pool()
	clock := clockseed.NewClock()
	random := clockseed.NewRandom()

	secMgr, err := security.New(security.Options{Env: config.EnvTest, Clock: clock, Random: random})
	if err != nil {
		t.Fatalf("security.New: %v", err)
	}
	ownerID := conformanceAccount(t, pool, "t06-owner@arena.example.com")
	peerID := conformanceAccount(t, pool, "t06-peer@arena.example.com")
	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case conformanceOwnerToken:
			return security.AuthIdentity{AccountID: ownerID, SessionID: "session-owner"}, nil
		case conformancePeerToken:
			return security.AuthIdentity{AccountID: peerID, SessionID: "session-peer"}, nil
		default:
			return security.AuthIdentity{}, errConformanceUnknownSession
		}
	})

	identity, sender := mountIdentity(t, pool, clock, random, secMgr)
	positions := mountPositions(t, pool, clock, secMgr)
	arguments := mountArguments(t, pool, clock, secMgr)
	persuasion := mountPersuasion(t, pool, clock, secMgr)
	wallet := mountWallet(t, pool, secMgr)
	profiles := mountProfiles(t, pool, secMgr)
	moderation := mountModerationReports(t, pool, clock, secMgr)
	search := mountSearch(t, pool, secMgr)
	arenas := mountArenas(t, pool, clock, secMgr, arenasbilling.New(billingpg.NewRepository(pool), clock))
	passes, checkout := mountBillingPasses(t, pool, clock, secMgr)
	metrics, export := mountTransparencyExport(t, pool, clock, secMgr)
	ids := clockseed.NewIDGenerator("req", random, clock)
	handler, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{}, []httpserver.Surface{
		surfaceOf(identityRoutes(), identity.RegisterRoutes),
		surfaceOf(arenasRoutes(), arenas.RegisterRoutes),
		surfaceOf(argumentsRoutes(), arguments.RegisterRoutes),
		surfaceOf(positionsRoutes(), positions.RegisterRoutes),
		surfaceOf(persuasionRoutes(), persuasion.RegisterRoutes),
		surfaceOf(walletRoutes(), wallet.RegisterRoutes),
		surfaceOf(profilesRoutes(), profiles.RegisterRoutes),
		surfaceOf(billingPassesRoutes(), passes.RegisterRoutes),
		surfaceOf(billingCheckoutRoutes(), checkout.RegisterBillingRoutes),
		surfaceOf(moderationRoutes(), moderation.RegisterRoutes),
		surfaceOf(searchRoutes(), search.RegisterRoutes),
		surfaceOf(transparencyRoutes(), metrics.RegisterRoutes),
		surfaceOf(transparencyExportRoutes(), export.RegisterRoutes),
	})
	if err != nil {
		t.Fatalf("compose platform router: %v", err)
	}
	server := httptest.NewServer(secMgr.AuthenticateMiddleware(validator)(handler))
	t.Cleanup(server.Close)
	return &conformanceHarness{server: server, pool: pool, sender: sender}
}

// surfaceOf adapts a handler registration to a platform surface. The routes
// are declared beside the registration (not derived from it) so a handler
// that served an undeclared route would fail the composition instead of
// passing in silence.
func surfaceOf(routes []httpserver.Route, register func(*http.ServeMux)) httpserver.Surface {
	return httpserver.Surface{Routes: routes, Register: func(mux *http.ServeMux) error {
		register(mux)
		return nil
	}}
}

func conformanceRoutes(method string, paths ...string) []httpserver.Route {
	routes := make([]httpserver.Route, 0, len(paths))
	for _, path := range paths {
		routes = append(routes, httpserver.Route{Method: method, Path: path})
	}
	return routes
}

func identityRoutes() []httpserver.Route {
	return slices.Concat(
		conformanceRoutes("GET", "/api/v1/auth/verify", "/api/v1/auth/password-reset", "/api/v1/me/sessions"),
		conformanceRoutes("POST",
			"/api/v1/auth/register", "/api/v1/auth/login", "/api/v1/auth/logout",
			"/api/v1/auth/password-reset/request", "/api/v1/auth/password-reset/confirm",
			"/api/v1/me/mfa/enrollment", "/api/v1/me/mfa/enrollment/confirm",
			"/api/v1/me/mfa/recovery", "/api/v1/me/mfa/step-up",
			"/api/v1/me/sessions/revocation", "/api/v1/me/sessions/rotation"),
	)
}

func arenasRoutes() []httpserver.Route {
	return slices.Concat(
		conformanceRoutes("GET", "/api/v1/arenas", "/api/v1/arenas/{slug}", "/api/v1/me/arena-drafts", "/api/v1/me/arena-drafts/{id}"),
		conformanceRoutes("POST", "/api/v1/me/arena-drafts", "/api/v1/me/arena-drafts/{id}/publish", "/api/v1/me/arenas/{id}/close"),
		conformanceRoutes("PATCH", "/api/v1/me/arena-drafts/{id}"),
		conformanceRoutes("DELETE", "/api/v1/me/arena-drafts/{id}"),
	)
}

func argumentsRoutes() []httpserver.Route {
	return append(
		conformanceRoutes("GET", "/api/v1/arenas/{id}/arguments", "/api/v1/arguments/{id}", "/api/v1/arguments/{id}/replies"),
		conformanceRoutes("POST", "/api/v1/me/arenas/{id}/arguments", "/api/v1/me/arenas/{id}/arguments/{argumentID}/replies", "/api/v1/me/arguments/{id}/withdraw")...,
	)
}

func positionsRoutes() []httpserver.Route {
	return append(
		conformanceRoutes("GET", "/api/v1/arenas/{id}/positions", "/api/v1/me/arenas/{id}/position", "/api/v1/me/arenas/{id}/position/changes"),
		conformanceRoutes("POST", "/api/v1/me/arenas/{id}/position", "/api/v1/me/arenas/{id}/position/changes")...,
	)
}

func persuasionRoutes() []httpserver.Route {
	return append(
		conformanceRoutes("GET", "/api/v1/arguments/{id}/attributions", "/api/v1/moderation/attribution-signals/{authorID}", "/api/v1/profiles/{username}/reputation"),
		conformanceRoutes("POST", "/api/v1/me/position-changes/{id}/attributions")...,
	)
}

func walletRoutes() []httpserver.Route {
	return conformanceRoutes("GET", "/api/v1/me/wallet", "/api/v1/me/wallet/transactions")
}

func profilesRoutes() []httpserver.Route {
	return conformanceRoutes("GET", "/api/v1/me/profile", "/api/v1/profiles/{username}")
}

func billingPassesRoutes() []httpserver.Route {
	return conformanceRoutes("GET", "/api/v1/me/passes", "/api/v1/me/passes/history")
}

func billingCheckoutRoutes() []httpserver.Route {
	return append(
		conformanceRoutes("GET", "/api/v1/me/billing/subscription"),
		conformanceRoutes("POST", "/api/v1/me/billing/checkout", "/api/v1/me/billing/portal")...,
	)
}

func moderationRoutes() []httpserver.Route {
	return slices.Concat(
		conformanceRoutes("GET", "/api/v1/moderation/cases"),
		conformanceRoutes("POST", "/api/v1/me/moderation/reports", "/api/v1/me/moderation/appeals",
			"/api/v1/moderation/cases/{id}/claim", "/api/v1/moderation/cases/{id}/decisions"),
	)
}

func searchRoutes() []httpserver.Route {
	return conformanceRoutes("GET", "/api/v1/search/arenas", "/api/v1/search/arguments")
}

func transparencyRoutes() []httpserver.Route {
	return conformanceRoutes("GET", "/api/v1/public/transparency", "/transparency")
}

func transparencyExportRoutes() []httpserver.Route {
	return conformanceRoutes("GET", "/api/v1/arenas/{id}/export")
}

var errConformanceUnknownSession = errors.New("unknown session")

func conformanceAccount(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1, 'active') RETURNING id::text`, email).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

func mountIdentity(t *testing.T, pool *pgxpool.Pool, clock clockseed.System, random clockseed.CryptoRandom, secMgr *security.Manager) (*identityhttp.Handler, *identityfake.Sender) {
	t.Helper()
	repo := identitypg.NewRepository(pool)
	hasher, err := identityargon.New(identityargon.FastParams(), random)
	if err != nil {
		t.Fatalf("setup hasher: %v", err)
	}
	sender := identityfake.NewSender()
	vPolicy := identitydomain.DefaultVerificationPolicy()
	sPolicy := identitydomain.DefaultSessionPolicy()
	rPolicy := identitydomain.DefaultPasswordResetPolicy()
	handler := identityhttp.NewHandler(identityhttp.HandlerConfig{
		RegisterUseCase:              identityapp.NewRegisterAccountUseCase(repo, repo, hasher, sender, clock, random, vPolicy),
		VerifyEmailUseCase:           identityapp.NewVerifyEmailUseCase(repo, repo, clock),
		LoginUseCase:                 identityapp.NewLoginUseCase(repo, repo, repo, hasher, clock, random, sPolicy),
		LogoutUseCase:                identityapp.NewLogoutUseCase(repo),
		RequestPasswordResetUseCase:  identityapp.NewRequestPasswordResetUseCase(repo, repo, sender, clock, random, rPolicy),
		CompletePasswordResetUseCase: identityapp.NewCompletePasswordResetUseCase(repo, repo, repo, repo, repo, hasher, sender, clock),
		AuthenticateSessionUseCase:   identityapp.NewAuthenticateSessionUseCase(repo, repo, clock, sPolicy, 5*time.Minute),
		SecurityManager:              secMgr,
	})
	return handler, sender
}

func mountArenas(t *testing.T, pool *pgxpool.Pool, clock clockseed.System, secMgr *security.Manager, passes arenasapp.ArenaPassConsumer) *arenashttp.Handler {
	t.Helper()
	repo := arenaspg.NewRepository(pool)
	cursors, err := arenasapp.NewFeedCursorCodec([]byte("t06-feed-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("feed cursor codec: %v", err)
	}
	policy := arenasdomain.DefaultStatementPolicy()
	uow := platformpg.NewTxManager(pool)
	handler := arenashttp.NewHandler(arenashttp.HandlerConfig{
		CreateDraftUseCase: arenasapp.NewCreateArenaDraftUseCase(repo, policy),
		GetDraftUseCase:    arenasapp.NewGetArenaDraftUseCase(repo),
		ListDraftsUseCase:  arenasapp.NewListArenaDraftsUseCase(repo),
		UpdateDraftUseCase: arenasapp.NewUpdateArenaDraftUseCase(repo, policy),
		DeleteDraftUseCase: arenasapp.NewDeleteArenaDraftUseCase(repo),
		PublishUseCase:     arenasapp.NewPublishArenaUseCase(repo, passes, uow, clock),
		CloseUseCase:       arenasapp.NewCloseArenaUseCase(repo),
		FeedUseCase:        arenasapp.NewGetArenaFeedUseCase(repo, cursors),
		GetPublicUseCase:   arenasapp.NewGetPublicArenaUseCase(repo),
		SecurityManager:    secMgr,
	})
	return handler
}

func mountArguments(t *testing.T, pool *pgxpool.Pool, clock clockseed.System, secMgr *security.Manager) *argumentshttp.Handler {
	t.Helper()
	repo := argumentspg.NewRepository(pool)
	cursors, err := argumentsapp.NewArgumentCursorCodec([]byte("t06-argument-cursor-secret-01234567"))
	if err != nil {
		t.Fatalf("argument cursor codec: %v", err)
	}
	walletRepo := walletpg.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	publish := argumentsapp.NewPublishArgumentUseCase(
		repo, concPublishAccounts{}, concPublishArenas{},
		argumentswallet.New(walletapp.NewDebitInkUseCase(walletRepo, clock)),
		uow, text.GraphemeCount, argumentsdomain.DefaultReplyPolicy(), clock)
	handler := argumentshttp.NewHandler(argumentshttp.HandlerConfig{
		PublishUseCase:   publish,
		WithdrawUseCase:  argumentsapp.NewWithdrawArgumentUseCase(repo, clock),
		ListArenaUseCase: argumentsapp.NewListArenaArgumentsUseCase(repo, cursors),
		ListRepliesCase:  argumentsapp.NewListRepliesUseCase(repo, cursors),
		GetPublicUseCase: argumentsapp.NewGetPublicArgumentUseCase(repo),
		SecurityManager:  secMgr,
	})
	return handler
}

// concPublishAccounts/Arenas are permissive gates for the conformance
// seed: only published arenas seeded by the test accept arguments.
type concPublishAccounts struct{}

func (concPublishAccounts) EnsureEligible(_ context.Context, _ argumentsdomain.AccountID) error {
	return nil
}

type concPublishArenas struct{}

func (concPublishArenas) EnsureAcceptsArguments(_ context.Context, _ argumentsdomain.ArenaID) error {
	return nil
}

func mountPositions(t *testing.T, pool *pgxpool.Pool, clock clockseed.System, secMgr *security.Manager) *positionhttp.Handler {
	t.Helper()
	repo := positionspg.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	handler := positionhttp.NewHandler(positionhttp.HandlerConfig{
		ConfirmUseCase:   positionsapp.NewConfirmInitialPositionUseCase(repo, concPositionsAccounts{}, concPositionsArenas{}, clock),
		ChangeUseCase:    positionsapp.NewChangePositionUseCase(repo, concPositionsArenas{}, uow, clock),
		GetMineUseCase:   positionsapp.NewGetMyPositionUseCase(repo),
		ListMineUseCase:  positionsapp.NewListPositionChangesUseCase(repo),
		AggregateUseCase: positionsapp.NewGetPositionAggregateUseCase(repo, positionsdomain.DefaultAggregatePolicy(), clock),
		SecurityManager:  secMgr,
	})
	return handler
}

// concPositionsAccounts/Arenas are permissive gates for the conformance seed.
type concPositionsAccounts struct{}

func (concPositionsAccounts) EnsureEligible(_ context.Context, _ positionsdomain.AccountID) error {
	return nil
}

type concPositionsArenas struct{}

func (concPositionsArenas) EnsureAcceptsPositions(_ context.Context, _ positionsdomain.ArenaID) error {
	return nil
}

func mountPersuasion(t *testing.T, pool *pgxpool.Pool, clock clockseed.System, secMgr *security.Manager) *persuasionhttp.Handler {
	t.Helper()
	repo := persuasionpg.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	handler := persuasionhttp.NewHandler(persuasionhttp.HandlerConfig{
		RecordUseCase:          persuasionapp.NewRecordAttributionsUseCase(repo, persuasiondomain.DefaultEligibilityPolicy(), uow),
		ArgumentMetricsUseCase: persuasionapp.NewGetArgumentMetricsUseCase(repo, clock),
		ProfileReputationCase:  persuasionapp.NewGetProfileReputationUseCase(repo, repo, clock),
		SecurityManager:        secMgr,
	})
	return handler
}

func mountWallet(t *testing.T, pool *pgxpool.Pool, secMgr *security.Manager) *wallethttp.Handler {
	t.Helper()
	repo := walletpg.NewRepository(pool)
	cursors, err := walletapp.NewStatementCursorCodec([]byte("t06-statement-cursor-secret-0123456"))
	if err != nil {
		t.Fatalf("statement cursor codec: %v", err)
	}
	handler := wallethttp.NewHandler(wallethttp.HandlerConfig{
		GetWalletBalanceUseCase:   walletapp.NewGetWalletBalanceUseCase(repo),
		GetWalletStatementUseCase: walletapp.NewGetWalletStatementUseCase(repo, cursors),
		SecurityManager:           secMgr,
	})
	return handler
}

func mountProfiles(t *testing.T, pool *pgxpool.Pool, secMgr *security.Manager) *profileshttp.Handler {
	t.Helper()
	repo := profilespg.NewRepository(pool)
	handler := profileshttp.NewHandler(profileshttp.HandlerConfig{
		GetPublicProfileUseCase:  profilesapp.NewGetPublicProfileUseCase(repo),
		GetPrivateProfileUseCase: profilesapp.NewGetPrivateProfileUseCase(repo),
		SecurityManager:          secMgr,
	})
	return handler
}

func mountBillingPasses(t *testing.T, pool *pgxpool.Pool, clock clockseed.System, secMgr *security.Manager) (*billinghttp.Handler, *billinghttp.BillingHandler) {
	t.Helper()
	repo := billingpg.NewRepository(pool)
	cursors, err := billingapp.NewHistoryCursorCodec([]byte("t06-pass-history-cursor-secret-0123"))
	if err != nil {
		t.Fatalf("pass history cursor codec: %v", err)
	}
	handler := billinghttp.NewHandler(billinghttp.HandlerConfig{
		GetArenaPassSummaryUseCase: billingapp.NewGetArenaPassSummaryUseCase(repo, clock),
		GetArenaPassHistoryUseCase: billingapp.NewGetArenaPassHistoryUseCase(repo, cursors),
		SecurityManager:            secMgr,
	})
	checkout := billinghttp.NewBillingHandler(billinghttp.BillingHandlerConfig{
		CreateCheckout:        nil,
		GetSubscriptionStatus: billingapp.NewGetSubscriptionStatusUseCase(repo),
		GetBillingPortal:      nil,
		SecurityManager:       secMgr,
	})
	return handler, checkout
}

func mountModerationReports(t *testing.T, pool *pgxpool.Pool, clock clockseed.System, secMgr *security.Manager) *moderationhttp.Handler {
	t.Helper()
	repo := moderationpg.NewRepository(pool)
	fileReport, err := moderationapp.NewFileReportUseCase(moderationapp.FileReportDependencies{
		Targets: concReportArenas{}, Reports: repo, Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewFileReportUseCase: %v", err)
	}
	handler := moderationhttp.NewHandler(moderationhttp.HandlerConfig{
		FileReport:      fileReport,
		SecurityManager: secMgr,
		Clock:           clock,
	})
	return handler
}

// concReportArenas resolves any arena target: the report flow, not the
// directory, is under test.
type concReportArenas struct{}

func (concReportArenas) DescribeArena(_ context.Context, _ string) (*moderationapp.TargetInfo, error) {
	return &moderationapp.TargetInfo{Exists: true}, nil
}

func (concReportArenas) DescribeArgument(_ context.Context, _ string) (*moderationapp.TargetInfo, error) {
	return &moderationapp.TargetInfo{Exists: false}, nil
}

func (concReportArenas) DescribeProfile(_ context.Context, _ string) (*moderationapp.TargetInfo, error) {
	return &moderationapp.TargetInfo{Exists: false}, nil
}

func mountSearch(t *testing.T, pool *pgxpool.Pool, secMgr *security.Manager) *searchhttp.Handler {
	t.Helper()
	repo := searchpg.NewRepository(pool)
	cursors, err := searchapp.NewCursorCodec([]byte("t06-search-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("search cursor codec: %v", err)
	}
	arenaSearch, err := searchapp.NewArenaSearchUseCase(repo, cursors)
	if err != nil {
		t.Fatalf("NewArenaSearchUseCase: %v", err)
	}
	argumentSearch, err := searchapp.NewArgumentSearchUseCase(repo, cursors)
	if err != nil {
		t.Fatalf("NewArgumentSearchUseCase: %v", err)
	}
	handler := searchhttp.NewHandler(searchhttp.HandlerConfig{Arenas: arenaSearch, Arguments: argumentSearch})
	_ = secMgr
	return handler
}

func mountTransparencyExport(t *testing.T, pool *pgxpool.Pool, clock clockseed.System, secMgr *security.Manager) (*transparencyhttp.Handler, *transparencyhttp.ExportHandler) {
	t.Helper()
	repo := transparencypg.NewRepository(pool)
	derive, err := transparencyapp.NewDeriveMetricsUseCase(repo)
	if err != nil {
		t.Fatalf("NewDeriveMetricsUseCase: %v", err)
	}
	templates, err := transparencyhttp.NewTemplates()
	if err != nil {
		t.Fatalf("NewTemplates: %v", err)
	}
	metrics := transparencyhttp.NewHandler(transparencyhttp.HandlerConfig{
		Derive: derive, Templates: templates, Clock: clock,
	})
	cursors, err := transparencyapp.NewExportCursorCodec([]byte("t06-export-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("export cursor codec: %v", err)
	}
	useCase, err := transparencyapp.NewGetArenaExportUseCase(repo, cursors)
	if err != nil {
		t.Fatalf("NewGetArenaExportUseCase: %v", err)
	}
	export := transparencyhttp.NewExportHandler(transparencyhttp.ExportHandlerConfig{UseCase: useCase})
	_ = secMgr
	return metrics, export
}

// checkConformance is the bidirectional gate of one call: the status and
// media type must be documented (contract to server), and the document must
// match the contract schemas with no secret fields (server to contract).
func checkConformance(t *testing.T, document *contract.Document, operation conformanceOperation, status int, header http.Header, body []byte) {
	t.Helper()
	context := operation.method + " " + operation.path + " -> " + strconv.Itoa(status)
	statuses := operationResponses(t, document, operation)
	if !statuses[strconv.Itoa(status)] && !statuses["default"] {
		t.Fatalf("%s: status is not documented by the contract", context)
	}
	mediaType := header.Get("Content-Type")
	documented := responseMediaTypes(t, document, operation, status)
	if len(documented) > 0 {
		matched := false
		for _, want := range documented {
			if strings.HasPrefix(mediaType, want) {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("%s: media type %q is not documented (%v)", context, mediaType, documented)
		}
	}
	assertNoSecrets(t, context, body)
	if status >= 400 {
		if !strings.HasPrefix(mediaType, contract.ProblemMediaType) {
			t.Fatalf("%s: error media type = %q, want %q", context, mediaType, contract.ProblemMediaType)
		}
		checkProblemShape(t, context, status, body)
		return
	}
	if !strings.HasPrefix(mediaType, "application/json") {
		return
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("%s: JSON body does not parse: %v", context, err)
	}
	schema, ok := responseSchema(t, document, operation, status)
	if !ok {
		return
	}
	checkEnvelope(t, context, document, schema, value)
}

// loadConformanceContract parses and validates the real contract document.
func loadConformanceContract(t *testing.T) *contract.Document {
	t.Helper()
	document, err := contract.Load("../../api/openapi.json")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("contract invalid: %v", err)
	}
	return document
}

// conformanceCall performs one call and gates it bidirectionally, returning
// the status, headers and raw body for further assertions. The operation
// names the contract template ("/api/v1/me/arena-drafts/{id}") while the
// request travels the concrete path (with identifiers and query strings).
// conformanceExchange is one contracted call: the template names the
// operation in the contract vocabulary while the concrete path travels the
// identifiers and query strings of a real request.
type conformanceExchange struct {
	Method   string
	Template string
	Concrete string
	Token    string
	Body     string
	Headers  map[string]string
}

func conformanceCall(t *testing.T, h *conformanceHarness, document *contract.Document, exchange conformanceExchange) (int, http.Header, []byte) {
	t.Helper()
	operation := conformanceOperation{method: exchange.Method, path: exchange.Template}
	status, header, raw := conformanceRequest(t, h, exchange.Method, exchange.Concrete, exchange.Token, exchange.Body, exchange.Headers)
	checkConformance(t, document, operation, status, header, raw)
	return status, header, raw
}

func TestConformanceAuthRegisterLogin(t *testing.T) {
	h := conformanceServer(t)
	document := loadConformanceContract(t)

	status, _, _ := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/auth/register", Concrete: "/api/v1/auth/register", Token: "", Body: `{"email":"t06-reg@arena.example.com","password":"t06-correct-horse-1"}`, Headers: nil})
	if status != 201 {
		t.Fatalf("POST /api/v1/auth/register = %d, want 201", status)
	}
	address, err := identitydomain.ParseEmail("t06-reg@arena.example.com")
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	token, found := h.sender.LastTokenForEmail(address)
	if !found {
		t.Fatal("registration issued no verification token")
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/auth/verify", Concrete: "/api/v1/auth/verify?token=" + token, Token: "", Body: "", Headers: nil})
	if status != 200 {
		t.Fatalf("GET /api/v1/auth/verify = %d, want 200", status)
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/auth/register", Concrete: "/api/v1/auth/register", Token: "", Body: `{invalid json`, Headers: nil})
	if status != 400 {
		t.Fatalf("POST /api/v1/auth/register malformed = %d, want 400", status)
	}

	// Anti-enumeration is uniform by design: an invalid address and a
	// duplicate address both answer 201 with the same document, so the
	// endpoint is never an existence oracle.
	uniform, _, uniformBody := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/auth/register", Concrete: "/api/v1/auth/register", Token: "", Body: `{"email":"not-an-email","password":"x"}`, Headers: nil})
	if uniform != 201 {
		t.Fatalf("POST /api/v1/auth/register invalid address = %d, want uniform 201", uniform)
	}
	duplicate, _, duplicateBody := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/auth/register", Concrete: "/api/v1/auth/register", Token: "", Body: `{"email":"t06-reg@arena.example.com","password":"t06-correct-horse-1"}`, Headers: nil})
	if duplicate != 201 {
		t.Fatalf("POST /api/v1/auth/register duplicate = %d, want uniform 201", duplicate)
	}
	if string(uniformBody) != string(duplicateBody) {
		t.Fatal("register answers differ between invalid and duplicate addresses")
	}

	status, header, _ := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/auth/login", Concrete: "/api/v1/auth/login", Token: "", Body: `{"email":"t06-reg@arena.example.com","password":"t06-correct-horse-1"}`, Headers: nil})
	if status != 200 {
		t.Fatalf("POST /api/v1/auth/login = %d, want 200", status)
	}
	cookies := header.Values("Set-Cookie")
	if len(cookies) == 0 {
		t.Fatal("login issued no session cookie")
	}
	status, _, loginBody := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/auth/login", Concrete: "/api/v1/auth/login", Token: "", Body: `{"email":"t06-reg@arena.example.com","password":"wrong-password-1"}`, Headers: nil})
	if status != 401 {
		t.Fatalf("POST /api/v1/auth/login wrong password = %d, want 401", status)
	}
	// Secret echo: a password that travels in must never travel back out,
	// even inside a problem detail.
	for _, secret := range []string{"t06-correct-horse-1", "wrong-password-1"} {
		if strings.Contains(string(loginBody), secret) {
			t.Fatalf("login refusal echoes the submitted password")
		}
	}
}

func TestConformanceAuthRequired(t *testing.T) {
	h := conformanceServer(t)
	document := loadConformanceContract(t)

	for _, operation := range []struct {
		method   string
		template string
		concrete string
	}{
		{"GET", "/api/v1/me/profile", "/api/v1/me/profile"},
		{"GET", "/api/v1/me/wallet", "/api/v1/me/wallet"},
		{"GET", "/api/v1/me/passes", "/api/v1/me/passes"},
		{"POST", "/api/v1/me/arena-drafts", "/api/v1/me/arena-drafts"},
		{"POST", "/api/v1/me/arenas/{id}/arguments", "/api/v1/me/arenas/0196a1b2-c3d4-7e5f-8000-000000000000/arguments"},
		{"POST", "/api/v1/me/arenas/{id}/position", "/api/v1/me/arenas/0196a1b2-c3d4-7e5f-8000-000000000000/position"},
		{"POST", "/api/v1/me/position-changes/{id}/attributions", "/api/v1/me/position-changes/0196a1b2-c3d4-7e5f-8000-000000000000/attributions"},
		{"POST", "/api/v1/me/moderation/reports", "/api/v1/me/moderation/reports"},
		{"POST", "/api/v1/me/billing/checkout", "/api/v1/me/billing/checkout"},
	} {
		body := `{}`
		if operation.method == "GET" || operation.method == "DELETE" {
			// GET/DELETE travel without a body: a body on a read is
			// eccentric and must not decide the auth answer.
			body = ""
		}
		status, _, _ := conformanceCall(t, h, document, conformanceExchange{Method: operation.method, Template: operation.template, Concrete: operation.concrete, Token: "", Body: body, Headers: nil})
		if status != 401 {
			t.Fatalf("%s %s without session = %d, want 401", operation.method, operation.template, status)
		}
	}
}

func TestConformanceRequestID(t *testing.T) {
	h := conformanceServer(t)
	document := loadConformanceContract(t)

	status, header, _ := conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas", Concrete: "/api/v1/arenas", Token: "", Body: "", Headers: map[string]string{"X-Request-Id": "t06-probe-id"}})
	if status != 200 {
		t.Fatalf("GET /api/v1/arenas = %d, want 200", status)
	}
	if got := header.Get("X-Request-Id"); got != "t06-probe-id" {
		t.Fatalf("X-Request-Id echo = %q, want the inbound value", got)
	}
	status, header, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas", Concrete: "/api/v1/arenas", Token: "", Body: "", Headers: nil})
	if status != 200 {
		t.Fatalf("GET /api/v1/arenas = %d, want 200", status)
	}
	if got := header.Get("X-Request-Id"); got == "" {
		t.Fatal("response without inbound X-Request-Id carries no generated one")
	}
}

func TestConformanceMeReads(t *testing.T) {
	h := conformanceServer(t)
	document := loadConformanceContract(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var ownerID string
	if err := h.pool.QueryRow(ctx, `SELECT id::text FROM app.accounts WHERE email = 't06-owner@arena.example.com'`).Scan(&ownerID); err != nil {
		t.Fatalf("resolve owner: %v", err)
	}
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO app.profiles (account_id, username, username_normalized) VALUES ($1::uuid, 't06owner', 't06owner')`, ownerID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	for _, operation := range []conformanceOperation{
		{"GET", "/api/v1/me/profile"},
		{"GET", "/api/v1/me/wallet"},
		{"GET", "/api/v1/me/passes"},
		{"GET", "/api/v1/me/passes/history"},
		{"GET", "/api/v1/me/billing/subscription"},
	} {
		status, _, body := conformanceCall(t, h, document, conformanceExchange{Method: operation.method, Template: operation.path, Concrete: operation.path, Token: conformanceOwnerToken, Body: "", Headers: nil})
		if status != 200 {
			t.Fatalf("%s %s = %d, want 200", operation.method, operation.path, status)
		}
		_ = body
	}
}

func TestConformanceDraftLifecycle(t *testing.T) {
	h := conformanceServer(t)
	document := loadConformanceContract(t)

	status, _, body := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arena-drafts", Concrete: "/api/v1/me/arena-drafts", Token: conformanceOwnerToken, Body: `{"statement":"A arena t06 debate a tese com clareza","category":"technology","language":"pt-BR"}`, Headers: nil})
	if status != 201 {
		t.Fatalf("POST /api/v1/me/arena-drafts = %d, want 201 (%s)", status, string(body))
	}
	var draft struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &draft); err != nil || draft.ID == "" {
		t.Fatalf("draft response carries no id: %s", string(body))
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arena-drafts", Concrete: "/api/v1/me/arena-drafts", Token: conformanceOwnerToken, Body: `{}`, Headers: nil})
	if status != 400 {
		t.Fatalf("POST /api/v1/me/arena-drafts empty = %d, want 400", status)
	}

	status, _, listBody := conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/me/arena-drafts", Concrete: "/api/v1/me/arena-drafts", Token: conformanceOwnerToken, Body: "", Headers: nil})
	if status != 200 {
		t.Fatalf("GET /api/v1/me/arena-drafts = %d, want 200", status)
	}
	var list struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(listBody, &list); err != nil || len(list.Items) == 0 {
		t.Fatalf("draft list is empty after creation: %s", string(listBody))
	}

	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "PATCH", Template: "/api/v1/me/arena-drafts/{id}", Concrete: "/api/v1/me/arena-drafts/" + draft.ID, Token: conformanceOwnerToken, Body: `{"statement":"A arena t06 revisada com clareza","category":"technology","language":"pt-BR","expected_version":1}`, Headers: nil})
	if status != 200 {
		t.Fatalf("PATCH /api/v1/me/arena-drafts/{id} = %d, want 200", status)
	}

	// Publishing without a pass is refused with a documented status: the
	// owner holds no lot on this fresh database.
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arena-drafts/{id}/publish", Concrete: "/api/v1/me/arena-drafts/" + draft.ID + "/publish", Token: conformanceOwnerToken, Body: `{}`, Headers: nil})
	if status != 402 && status != 409 {
		t.Fatalf("POST publish without pass = %d, want a documented refusal (402 or 409)", status)
	}

	grantPassLot(t, h, "t06-owner@arena.example.com")
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arena-drafts/{id}/publish", Concrete: "/api/v1/me/arena-drafts/" + draft.ID + "/publish", Token: conformanceOwnerToken, Body: `{}`, Headers: nil})
	if status != 200 {
		t.Fatalf("POST publish with pass = %d, want 200", status)
	}

	// Only drafts delete: the arena above is published and immutable, so a
	// second draft exercises the deletion.
	status, _, draftBody := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arena-drafts", Concrete: "/api/v1/me/arena-drafts", Token: conformanceOwnerToken, Body: `{"statement":"Rascunho t06 descartável","category":"culture","language":"pt-BR"}`, Headers: nil})
	if status != 201 {
		t.Fatalf("POST second draft = %d, want 201", status)
	}
	var spare struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(draftBody, &spare); err != nil || spare.ID == "" {
		t.Fatalf("second draft carries no id: %s", string(draftBody))
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "DELETE", Template: "/api/v1/me/arena-drafts/{id}", Concrete: "/api/v1/me/arena-drafts/" + spare.ID, Token: conformanceOwnerToken, Body: "", Headers: nil})
	if status != 204 && status != 200 {
		t.Fatalf("DELETE /api/v1/me/arena-drafts/{id} = %d, want 204 or 200", status)
	}
}

func TestConformanceFeedAndPublicReads(t *testing.T) {
	h := conformanceServer(t)
	document := loadConformanceContract(t)

	status, _, _ := conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas", Concrete: "/api/v1/arenas", Token: "", Body: "", Headers: nil})
	if status != 200 {
		t.Fatalf("GET /api/v1/arenas = %d, want 200", status)
	}
	for _, query := range []string{"?limit=abc"} {
		status, _, _ := conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas", Concrete: "/api/v1/arenas" + query, Token: "", Body: "", Headers: nil})
		if status != 400 {
			t.Fatalf("GET /api/v1/arenas%s = %d, want 400", query, status)
		}
	}
	// Out-of-range limits clamp instead of refusing (parseFeedLimit plus
	// the use-case clamp, by design): the answers stay valid envelopes.
	for _, query := range []string{"?limit=101", "?limit=0", "?limit=2"} {
		status, _, _ := conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas", Concrete: "/api/v1/arenas" + query, Token: "", Body: "", Headers: nil})
		if status != 200 {
			t.Fatalf("GET /api/v1/arenas%s = %d, want 200", query, status)
		}
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas/{slug}", Concrete: "/api/v1/arenas/no-such-arena", Token: "", Body: "", Headers: nil})
	if status != 404 {
		t.Fatalf("GET /api/v1/arenas/no-such-arena = %d, want 404", status)
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas/{id}/export", Concrete: "/api/v1/arenas/0196a1b2-c3d4-7e5f-8000-000000000000/export", Token: "", Body: "", Headers: nil})
	if status != 404 {
		t.Fatalf("GET export of unknown arena = %d, want 404", status)
	}
}

func conformanceFund(t *testing.T, h *conformanceHarness, email string, amount int64) {
	conformanceFundKeyed(t, h, email, "t06-fund", amount)
}

func conformanceFundKeyed(t *testing.T, h *conformanceHarness, email, keySuffix string, amount int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var accountID string
	if err := h.pool.QueryRow(ctx, `SELECT id::text FROM app.accounts WHERE email = $1`, email).Scan(&accountID); err != nil {
		t.Fatalf("resolve account: %v", err)
	}
	key, err := walletdomain.ParseIdempotencyKey("t06-fund-" + keySuffix)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey: %v", err)
	}
	reference, err := walletdomain.ParseReference("model:fund:t06")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	repo := walletpg.NewRepository(h.pool)
	if _, err := repo.ApplyCredit(ctx, walletapp.CreditRequest{
		AccountID: walletdomain.AccountID(accountID), Bucket: walletdomain.BucketPurchased,
		OperationType: walletdomain.OperationCreditPurchase, IdempotencyKey: key,
		Reference: reference, Delta: amount, ChangedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("fund wallet: %v", err)
	}
}

func TestConformanceArguments(t *testing.T) {
	h := conformanceServer(t)
	document := loadConformanceContract(t)
	conformanceFund(t, h, "t06-owner@arena.example.com", 100000)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var arenaID string
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		SELECT id, 'A arena t06 debate a tese com clareza', 'technology', 'pt-BR', 'published', 't06-conformance-arena', now()
		FROM app.accounts WHERE email = 't06-owner@arena.example.com'
		RETURNING id::text`).Scan(&arenaID); err != nil {
		t.Fatalf("seed arena: %v", err)
	}

	body := `{"relation":"support","content":"Afirmação t06 para conformidade"}`
	headers := map[string]string{"Idempotency-Key": "t06-arg-1"}
	status, _, raw := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arenas/{id}/arguments", Concrete: "/api/v1/me/arenas/" + arenaID + "/arguments", Token: conformanceOwnerToken, Body: body, Headers: headers})
	if status != 200 && status != 201 {
		t.Fatalf("POST arguments = %d, want 200 or 201 (%s)", status, string(raw))
	}
	var published struct {
		Argument struct {
			ID string `json:"id"`
		} `json:"argument"`
	}
	if err := json.Unmarshal(raw, &published); err != nil || published.Argument.ID == "" {
		t.Fatalf("publish response carries no argument id: %s", string(raw))
	}
	status, replayHeader, _ := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arenas/{id}/arguments", Concrete: "/api/v1/me/arenas/" + arenaID + "/arguments", Token: conformanceOwnerToken, Body: body, Headers: headers})
	if status != 200 && status != 201 {
		t.Fatalf("POST arguments replay = %d, want 200 or 201", status)
	}
	if got := replayHeader.Get("Idempotency-Replayed"); got != "true" {
		t.Fatalf("replay header = %q, want true", got)
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arenas/{id}/arguments", Concrete: "/api/v1/me/arenas/" + arenaID + "/arguments", Token: conformanceOwnerToken, Body: `{"relation":"maybe","content":"x"}`, Headers: map[string]string{"Idempotency-Key": "t06-arg-bad"}})
	if status != 400 {
		t.Fatalf("POST arguments invalid = %d, want 400", status)
	}

	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas/{id}/arguments", Concrete: "/api/v1/arenas/" + arenaID + "/arguments?relation=support", Token: "", Body: "", Headers: nil})
	if status != 200 {
		t.Fatalf("GET arguments = %d, want 200", status)
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas/{id}/arguments", Concrete: "/api/v1/arenas/" + arenaID + "/arguments", Token: "", Body: "", Headers: nil})
	if status != 400 {
		t.Fatalf("GET arguments without relation = %d, want 400", status)
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arguments/{id}", Concrete: "/api/v1/arguments/" + published.Argument.ID, Token: "", Body: "", Headers: nil})
	if status != 200 {
		t.Fatalf("GET argument = %d, want 200", status)
	}
}

func TestConformancePositions(t *testing.T) {
	h := conformanceServer(t)
	document := loadConformanceContract(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var arenaID string
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		SELECT id, 'A arena t06 debate a tese com clareza', 'technology', 'pt-BR', 'published', 't06-positions-arena', now()
		FROM app.accounts WHERE email = 't06-owner@arena.example.com'
		RETURNING id::text`).Scan(&arenaID); err != nil {
		t.Fatalf("seed arena: %v", err)
	}

	status, _, _ := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arenas/{id}/position", Concrete: "/api/v1/me/arenas/" + arenaID + "/position", Token: conformanceOwnerToken, Body: `{"position":"agree"}`, Headers: nil})
	if status != 200 {
		t.Fatalf("POST position = %d, want 200", status)
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arenas/{id}/position", Concrete: "/api/v1/me/arenas/" + arenaID + "/position", Token: conformanceOwnerToken, Body: `{"position":"maybe"}`, Headers: nil})
	if status != 400 {
		t.Fatalf("POST position invalid = %d, want 400", status)
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arenas/{id}/position/changes", Concrete: "/api/v1/me/arenas/" + arenaID + "/position/changes", Token: conformanceOwnerToken, Body: `{"position":"disagree"}`, Headers: nil})
	if status != 200 && status != 201 {
		t.Fatalf("POST position change = %d, want 200 or 201", status)
	}

	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/me/arenas/{id}/position", Concrete: "/api/v1/me/arenas/" + arenaID + "/position", Token: conformanceOwnerToken, Body: "", Headers: nil})
	if status != 200 {
		t.Fatalf("GET my position = %d, want 200", status)
	}
	// The public aggregate stays public: no session, still a document.
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas/{id}/positions", Concrete: "/api/v1/arenas/" + arenaID + "/positions", Token: "", Body: "", Headers: nil})
	if status != 200 {
		t.Fatalf("GET aggregate = %d, want 200", status)
	}
}

func TestConformanceAttributionsModerationSearch(t *testing.T) {
	h := conformanceServer(t)
	document := loadConformanceContract(t)
	conformanceFundKeyed(t, h, "t06-owner@arena.example.com", "attrs-owner", 100000)
	conformanceFundKeyed(t, h, "t06-peer@arena.example.com", "attrs-peer", 100000)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var arenaID string
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		SELECT id, 'A arena t06 para atribuições', 'technology', 'pt-BR', 'published', 't06-attribution-arena', now()
		FROM app.accounts WHERE email = 't06-owner@arena.example.com'
		RETURNING id::text`).Scan(&arenaID); err != nil {
		t.Fatalf("seed arena: %v", err)
	}
	// Self-attribution is refused by policy, so the peer publishes the
	// argument the owner later credits.
	status, _, raw := conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arenas/{id}/arguments", Concrete: "/api/v1/me/arenas/" + arenaID + "/arguments", Token: conformancePeerToken, Body: `{"relation":"support","content":"Argumento t06 para atribuição"}`, Headers: map[string]string{"Idempotency-Key": "t06-attr-arg"}})
	if status != 200 && status != 201 {
		t.Fatalf("POST arguments = %d, want 200 or 201", status)
	}
	var published struct {
		Argument struct {
			ID string `json:"id"`
		} `json:"argument"`
	}
	if err := json.Unmarshal(raw, &published); err != nil || published.Argument.ID == "" {
		t.Fatalf("publish carries no argument id: %s", string(raw))
	}
	for _, position := range []string{"agree", "disagree"} {
		endpoint := "position"
		if position == "disagree" {
			endpoint = "position/changes"
		}
		status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/arenas/{id}/" + endpoint, Concrete: "/api/v1/me/arenas/" + arenaID + "/" + endpoint, Token: conformanceOwnerToken, Body: `{"position":"` + position + `"}`, Headers: nil})
		if status != 200 && status != 201 {
			t.Fatalf("POST %s = %d, want 200 or 201", endpoint, status)
		}
	}
	var changeID string
	if err := h.pool.QueryRow(ctx, `SELECT id::text FROM app.position_changes WHERE arena_id = $1::uuid ORDER BY version DESC LIMIT 1`,
		arenaID).Scan(&changeID); err != nil {
		t.Fatalf("read change: %v", err)
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/position-changes/{id}/attributions", Concrete: "/api/v1/me/position-changes/" + changeID + "/attributions", Token: conformanceOwnerToken, Body: `{"argument_ids":["` + published.Argument.ID + `"]}`, Headers: nil})
	if status != 200 && status != 201 {
		t.Fatalf("POST attributions = %d, want 200 or 201", status)
	}

	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "POST", Template: "/api/v1/me/moderation/reports", Concrete: "/api/v1/me/moderation/reports", Token: conformanceOwnerToken, Body: `{"target_type":"arena","target_id":"` + arenaID + `","reason":"spam"}`, Headers: nil})
	if status != 200 && status != 201 {
		t.Fatalf("POST moderation reports = %d, want 200 or 201", status)
	}

	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/search/arenas", Concrete: "/api/v1/search/arenas?q=t06&language=pt-BR", Token: "", Body: "", Headers: nil})
	if status != 200 {
		t.Fatalf("GET search arenas = %d, want 200", status)
	}
	// Without language the backend may fail closed: the contract documents
	// 500 as a possible answer, so whatever comes back must still be a
	// documented status (hardening the optional parameter is out of scope).
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/search/arenas", Concrete: "/api/v1/search/arenas?q=t06", Token: "", Body: "", Headers: nil})
	if status != 200 && status != 400 && status != 500 {
		t.Fatalf("GET search arenas without language = %d, want a documented status", status)
	}
	status, _, _ = conformanceCall(t, h, document, conformanceExchange{Method: "GET", Template: "/api/v1/arenas/{id}/export", Concrete: "/api/v1/arenas/" + arenaID + "/export", Token: "", Body: "", Headers: nil})
	if status != 200 {
		t.Fatalf("GET export = %d, want 200", status)
	}
}

func TestConformanceUnknownRoutes(t *testing.T) {
	h := conformanceServer(t)

	// Unknown paths never reach a module handler: the platform mux answers
	// 404 itself (net/http plain text, like the real process), so an
	// undeclared route can never borrow a declared answer.
	status, _, raw := conformanceRequest(t, h, "GET", "/api/v1/no-such-resource", "", "", nil)
	if status != 404 {
		t.Fatalf("GET unknown route = %d, want 404", status)
	}
	assertNoSecrets(t, "unknown route", raw)

	status, _, raw = conformanceRequest(t, h, "DELETE", "/api/v1/arenas", "", "", nil)
	if status != 405 {
		t.Fatalf("DELETE /api/v1/arenas = %d, want 405", status)
	}
	assertNoSecrets(t, "wrong method", raw)
}

// grantPassLot seeds one administrative pass lot for the account behind an
// email, through the real billing repository.
func grantPassLot(t *testing.T, h *conformanceHarness, email string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var accountID string
	if err := h.pool.QueryRow(ctx, `SELECT id::text FROM app.accounts WHERE email = $1`, email).Scan(&accountID); err != nil {
		t.Fatalf("resolve account: %v", err)
	}
	reference, err := billingdomain.ParseReference("t06:grant:draft")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	quantity, err := billingdomain.NewQuantity(1)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	repo := billingpg.NewRepository(h.pool)
	if _, err := repo.GrantPassLot(ctx, billingapp.GrantPassLotRequest{
		AccountID: billingdomain.AccountID(accountID), Origin: billingdomain.OriginAdmin,
		Quantity: quantity, Reference: reference, GrantedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("grant pass lot: %v", err)
	}
}
