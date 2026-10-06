// Tests of the debate surface composed in the process (P49-T04):
// positions, arguments and persuasion run on the platform mux over
// disposable PostgreSQL, driven by real HTTP with the same pool and
// security boundary as the account journey — the session the JSON login
// opened is the session the debate routes require, and no second
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
	moderationpg "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
	profilespostgres "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	profilesdomain "github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
	walletpostgres "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// debateJourney is the account plus lifecycle plus debate surfaces on a
// real listener, sharing one pool and one security boundary like `arena
// server` does.
type debateJourney struct {
	account  *bootstrap.AccountSurface
	lifetime *bootstrap.ArenaSurface
	debate   *bootstrap.DebateSurface
	server   *httptest.Server
	pool     *pgxpool.Pool
}

// newDebateJourney composes the three surfaces the way the process does.
func newDebateJourney(t *testing.T) *debateJourney {
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
	lifetime, err := bootstrap.ComposeArenaLifecycle(bootstrap.Options{
		Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool: pool, Clock: clock, Random: random, Assets: manifestFixture(t), Security: manager,
		CursorSecret: []byte(cursorSecret),
	})
	if err != nil {
		t.Fatalf("ComposeArenaLifecycle() error = %v", err)
	}
	debate, err := bootstrap.ComposeDebateAttribution(bootstrap.Options{
		Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool: pool, Clock: clock, Random: random, Assets: manifestFixture(t), Security: manager,
		CursorSecret: []byte(cursorSecret),
	})
	if err != nil {
		t.Fatalf("ComposeDebateAttribution() error = %v", err)
	}
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{},
		[]httpserver.Surface{account.Surface(), lifetime.Surface(), debate.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &debateJourney{account: account, lifetime: lifetime, debate: debate, server: server, pool: database.Pool.Pool()}
}

// debateLogin registers, verifies and signs in one account over the JSON
// API, returning the client holding its session plus the account
// identifier.
func debateLogin(t *testing.T, journey *debateJourney, email, password string) (*http.Client, string) {
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

// debatePostKey sends one JSON POST carrying an Idempotency-Key, which the
// state-changing debate writes require.
func debatePostKey(t *testing.T, client *http.Client, server *httptest.Server, path, body, key string) (int, []byte, http.Header) {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read POST %s: %v", path, err)
	}
	return response.StatusCode, raw, response.Header
}

// debateGrantPass funds one Arena publication through the billing
// repository, the same path a purchase takes.
func debateGrantPass(t *testing.T, journey *debateJourney, accountID, reference string) {
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

// debateCreditINK funds one account through the free credit path, so its
// arguments can pay the publication cost.
func debateCreditINK(t *testing.T, journey *debateJourney, accountID string, amount int64, key string) {
	t.Helper()

	_, err := walletapp.NewCreditInkUseCase(
		walletpostgres.NewRepository(journey.pool), clockseed.NewClock(),
	).Execute(context.Background(), walletapp.CreditInkCommand{
		AccountID:      accountID,
		Bucket:         string(walletdomain.BucketFree),
		OperationType:  walletdomain.OperationCreditFree.String(),
		Amount:         amount,
		Reference:      "debate-journey-credit",
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("CreditInk(%s) error = %v", accountID, err)
	}
}

// debateCreateProfile persists the verified account's public handle, which
// the reputation read addresses.
func debateCreateProfile(t *testing.T, journey *debateJourney, accountID, username string) {
	t.Helper()

	created, err := profilesapp.NewCreateProfileUseCase(
		profilespostgres.NewRepository(journey.pool), profilespostgres.NewRepository(journey.pool),
		profilesdomain.DefaultUsernamePolicy(), clockseed.NewClock(),
	).Execute(context.Background(), profilesapp.CreateProfileCommand{
		AccountID: accountID, Username: username, Locale: "pt-BR",
	})
	if err != nil {
		t.Fatalf("CreateProfile(%q) error = %v", username, err)
	}
	if created == nil {
		t.Fatalf("CreateProfile(%q) answered nothing", username)
	}
}

// debateGrantModerator assigns the moderator role the signals read
// requires, attributed to the account itself like the local command does.
func debateGrantModerator(t *testing.T, journey *debateJourney, accountID string) {
	t.Helper()

	if _, err := moderationpg.NewRepository(journey.pool).Grant(context.Background(), moderationapp.RoleGrantRequest{
		AccountID: moderationdomain.AccountID(accountID),
		Role:      moderationdomain.RoleModerator,
		GrantedBy: moderationdomain.AccountID(accountID),
		GrantedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Grant(moderator) error = %v", err)
	}
}

// debatePublishArena drafts and publishes one Arena over HTTP, returning
// the numeric identifier the debate routes address plus the public slug.
func debatePublishArena(t *testing.T, client *http.Client, journey *debateJourney, ownerID, statement string) (string, string) {
	t.Helper()

	status, raw, _ := jsonPost(t, client, journey.server, "/api/v1/me/arena-drafts",
		`{"statement":`+quoteJSON(statement)+`,"category":"technology","language":"pt-BR"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /api/v1/me/arena-drafts status = %d, want 201 (body: %.300s)", status, raw)
	}
	var draft map[string]any
	if err := json.Unmarshal(raw, &draft); err != nil {
		t.Fatalf("draft is not JSON: %v", err)
	}
	identifier, _ := draft["id"].(string)
	if identifier == "" {
		t.Fatalf("draft answers no id: %.200s", raw)
	}
	status, raw, _ = jsonPost(t, client, journey.server, "/api/v1/me/arena-drafts/"+identifier+"/publish", `{}`)
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
	return identifier, slug
}

// TestDebatePositionConfirmChangeAggregate proves the position writes the
// process serves: confirm, idempotent reconfirm, change with a new version,
// owner reads and history, the public aggregate, and the closed doors for
// strangers and anonymous callers.
func TestDebatePositionConfirmChangeAggregate(t *testing.T) {
	t.Parallel()

	journey := newDebateJourney(t)
	owner, ownerID := debateLogin(t, journey, "debate-position@example.test", "correct horse battery staple")
	stranger, _ := debateLogin(t, journey, "debate-position-stranger@example.test", "correct horse battery staple")
	anonymous := browser(t)
	debateGrantPass(t, journey, ownerID, "stripe:debate_position_first")
	arenaID, _ := debatePublishArena(t, owner, journey, ownerID, "A deliberação melhora quando cada posição fica registrada.")

	status, raw, _ := jsonPost(t, owner, journey.server, "/api/v1/me/arenas/"+arenaID+"/position", `{"position":"agree"}`)
	if status != http.StatusOK {
		t.Fatalf("POST position status = %d, want 200 (body: %.300s)", status, raw)
	}
	status, raw, _ = jsonPost(t, owner, journey.server, "/api/v1/me/arenas/"+arenaID+"/position", `{"position":"agree"}`)
	if status != http.StatusOK {
		t.Fatalf("POST position twice status = %d, want 200 (body: %.300s)", status, raw)
	}
	if !strings.Contains(string(raw), `"replayed":true`) {
		t.Errorf("reconfirm body = %.300s, want the idempotent replay", raw)
	}
	status, raw, _ = jsonPost(t, owner, journey.server, "/api/v1/me/arenas/"+arenaID+"/position/changes", `{"position":"disagree"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST position change status = %d, want 201 (body: %.300s)", status, raw)
	}
	var change map[string]any
	if err := json.Unmarshal(raw, &change); err != nil {
		t.Fatalf("change is not JSON: %v", err)
	}
	if _, ok := change["change_id"].(string); !ok {
		t.Errorf("change body = %.300s, want the change identifier", raw)
	}
	status, raw = jsonGet(t, owner, journey.server, "/api/v1/me/arenas/"+arenaID+"/position")
	if status != http.StatusOK {
		t.Fatalf("GET my position status = %d, want 200", status)
	}
	if !strings.Contains(string(raw), `"current_position":"disagree"`) {
		t.Errorf("my position body = %.300s, want the changed position", raw)
	}
	status, raw = jsonGet(t, owner, journey.server, "/api/v1/me/arenas/"+arenaID+"/position/changes")
	if status != http.StatusOK {
		t.Fatalf("GET my changes status = %d, want 200", status)
	}
	if !strings.Contains(string(raw), "agree") || !strings.Contains(string(raw), "disagree") {
		t.Errorf("changes body = %.300s, want both sides of the move", raw)
	}
	status, raw = jsonGet(t, anonymous, journey.server, "/api/v1/arenas/"+arenaID+"/positions")
	if status != http.StatusOK {
		t.Fatalf("GET aggregate status = %d, want 200", status)
	}
	if !strings.Contains(string(raw), "participants_total") {
		t.Errorf("aggregate body = %.300s, want the public counts", raw)
	}
	if status, _ := jsonGet(t, stranger, journey.server, "/api/v1/me/arenas/"+arenaID+"/position"); status != http.StatusNotFound {
		t.Errorf("GET stranger position status = %d, want 404: positions never leak across accounts", status)
	}
	if status, _, _ := jsonPost(t, anonymous, journey.server, "/api/v1/me/arenas/"+arenaID+"/position", `{"position":"agree"}`); status != http.StatusUnauthorized {
		t.Errorf("POST position anonymous status = %d, want 401", status)
	}
	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/me/arenas/"+arenaID+"/position"); status != http.StatusUnauthorized {
		t.Errorf("GET position anonymous status = %d, want 401", status)
	}
}

// TestDebateArgumentPublishReplyWithdraw proves the argument lifecycle the
// process serves: publication spending INK, replies, public lists with an
// honest cursor, withdrawal retracting the content, and the refusal without
// funds changing nothing.
func TestDebateArgumentPublishReplyWithdraw(t *testing.T) {
	t.Parallel()

	journey := newDebateJourney(t)
	author, authorID := debateLogin(t, journey, "debate-author@example.test", "correct horse battery staple")
	replier, replierID := debateLogin(t, journey, "debate-replier@example.test", "correct horse battery staple")
	broke, _ := debateLogin(t, journey, "debate-broke@example.test", "correct horse battery staple")
	anonymous := browser(t)
	debateGrantPass(t, journey, authorID, "stripe:debate_argument_first")
	arenaID, _ := debatePublishArena(t, author, journey, authorID, "O futuro do debate público passa por argumentos verificáveis.")
	debateCreditINK(t, journey, authorID, 1000, "debate-author-ink")
	debateCreditINK(t, journey, replierID, 1000, "debate-replier-ink")

	status, raw, _ := debatePostKey(t, author, journey.server, "/api/v1/me/arenas/"+arenaID+"/arguments",
		`{"relation":"support","content":"A verificação pública eleva a qualidade do debate coletivo."}`, "debate-publish-1")
	if status != http.StatusCreated {
		t.Fatalf("POST argument status = %d, want 201 (body: %.300s)", status, raw)
	}
	var published map[string]any
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("published argument is not JSON: %v", err)
	}
	argument, _ := published["argument"].(map[string]any)
	argumentID, _ := argument["id"].(string)
	if argumentID == "" {
		t.Fatalf("published argument carries no id: %.300s", raw)
	}
	status, raw, headers := debatePostKey(t, author, journey.server, "/api/v1/me/arenas/"+arenaID+"/arguments",
		`{"relation":"support","content":"A verificação pública eleva a qualidade do debate coletivo."}`, "debate-publish-1")
	if status != http.StatusOK {
		t.Fatalf("POST argument replay status = %d, want 200 (body: %.300s)", status, raw)
	}
	if headers.Get("Idempotency-Replayed") != "true" {
		t.Error("argument replay carries no Idempotency-Replayed header")
	}
	status, raw, _ = debatePostKey(t, replier, journey.server, "/api/v1/me/arenas/"+arenaID+"/arguments/"+argumentID+"/replies",
		`{"relation":"context","content":"O contexto histórico mostra que a verificação sempre ajudou."}`, "debate-reply-1")
	if status != http.StatusCreated {
		t.Fatalf("POST reply status = %d, want 201 (body: %.300s)", status, raw)
	}
	var reply map[string]any
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("reply is not JSON: %v", err)
	}
	replyArgument, _ := reply["argument"].(map[string]any)
	replyID, _ := replyArgument["id"].(string)
	if replyID == "" {
		t.Fatalf("reply carries no id: %.300s", raw)
	}
	status, raw = jsonGet(t, anonymous, journey.server, "/api/v1/arenas/"+arenaID+"/arguments?relation=support")
	if status != http.StatusOK {
		t.Fatalf("GET arguments status = %d, want 200", status)
	}
	if !strings.Contains(string(raw), "verificação pública") {
		t.Errorf("arguments body = %.300s, want the published content", raw)
	}
	status, raw = jsonGet(t, anonymous, journey.server, "/api/v1/arguments/"+argumentID+"/replies")
	if status != http.StatusOK {
		t.Fatalf("GET replies status = %d, want 200", status)
	}
	if !strings.Contains(string(raw), "contexto histórico") {
		t.Errorf("replies body = %.300s, want the reply content", raw)
	}
	status, raw = jsonGet(t, anonymous, journey.server, "/api/v1/arguments/"+argumentID)
	if status != http.StatusOK {
		t.Fatalf("GET argument status = %d, want 200", status)
	}
	if !strings.Contains(string(raw), "verificação pública") {
		t.Errorf("argument body = %.300s, want the published content", raw)
	}
	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/arenas/"+arenaID+"/arguments?cursor=forged"); status != http.StatusBadRequest {
		t.Errorf("GET arguments with forged cursor status = %d, want 400", status)
	}
	status, raw, _ = jsonPost(t, author, journey.server, "/api/v1/me/arguments/"+argumentID+"/withdraw", `{}`)
	if status != http.StatusOK {
		t.Fatalf("POST withdraw status = %d, want 200 (body: %.300s)", status, raw)
	}
	if !strings.Contains(string(raw), `"content":null`) {
		t.Errorf("withdrawn body = %.300s, want the retracted content", raw)
	}
	status, raw, _ = debatePostKey(t, broke, journey.server, "/api/v1/me/arenas/"+arenaID+"/arguments",
		`{"relation":"support","content":"Sem saldo esta publicação não pode nascer."}`, "debate-broke-1")
	if status != http.StatusConflict {
		t.Fatalf("POST argument without INK status = %d, want 409 (body: %.300s)", status, raw)
	}
	if !strings.Contains(string(raw), "insufficient_ink") {
		t.Errorf("refusal body = %.200s, want insufficient_ink", raw)
	}
	status, raw = jsonGet(t, anonymous, journey.server, "/api/v1/arenas/"+arenaID+"/arguments?relation=support")
	if status != http.StatusOK {
		t.Fatalf("GET arguments status = %d, want 200", status)
	}
	if strings.Contains(string(raw), "Sem saldo") {
		t.Error("the refused publication leaked into the list: the debit and the write must roll back together")
	}
	if status, _, _ := jsonPost(t, replier, journey.server, "/api/v1/me/arguments/"+argumentID+"/withdraw", `{}`); status != http.StatusNotFound {
		t.Errorf("POST withdraw stranger status = %d, want 404", status)
	}
	if status, _, _ := jsonPost(t, anonymous, journey.server, "/api/v1/me/arguments/"+argumentID+"/withdraw", `{}`); status != http.StatusUnauthorized {
		t.Errorf("POST withdraw anonymous status = %d, want 401", status)
	}
}

// TestDebateAttributionRecordMetricsReputation proves influence
// attribution over the process: a change credits arguments, the replay
// resolves without a second write, the public metrics and the author
// reputation reflect the credit, foreign changes stay indistinguishable
// from missing ones, and anonymous recording fails closed.
func TestDebateAttributionRecordMetricsReputation(t *testing.T) {
	t.Parallel()

	journey := newDebateJourney(t)
	author, authorID := debateLogin(t, journey, "debate-attributor-author@example.test", "correct horse battery staple")
	attributor, attributorID := debateLogin(t, journey, "debate-attributor@example.test", "correct horse battery staple")
	stranger, _ := debateLogin(t, journey, "debate-attribution-stranger@example.test", "correct horse battery staple")
	anonymous := browser(t)
	debateGrantPass(t, journey, authorID, "stripe:debate_attribution_first")
	arenaID, _ := debatePublishArena(t, author, journey, authorID, "A influência no debate deve ser atribuída a quem convenceu.")
	debateCreditINK(t, journey, authorID, 1000, "debate-attribution-author-ink")
	debateCreditINK(t, journey, attributorID, 1000, "debate-attribution-attributor-ink")
	debateCreateProfile(t, journey, authorID, "debateautora")

	status, raw, _ := debatePostKey(t, author, journey.server, "/api/v1/me/arenas/"+arenaID+"/arguments",
		`{"relation":"support","content":"Um argumento que muda posições merece o crédito público."}`, "debate-attribution-argument")
	if status != http.StatusCreated {
		t.Fatalf("POST argument status = %d, want 201 (body: %.300s)", status, raw)
	}
	var published map[string]any
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("published argument is not JSON: %v", err)
	}
	argument, _ := published["argument"].(map[string]any)
	argumentID, _ := argument["id"].(string)
	if argumentID == "" {
		t.Fatalf("published argument carries no id: %.300s", raw)
	}
	if status, _, _ := jsonPost(t, attributor, journey.server, "/api/v1/me/arenas/"+arenaID+"/position", `{"position":"undecided"}`); status != http.StatusOK {
		t.Fatalf("POST position status = %d, want 200", status)
	}
	status, raw, _ = jsonPost(t, attributor, journey.server, "/api/v1/me/arenas/"+arenaID+"/position/changes", `{"position":"agree"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST position change status = %d, want 201 (body: %.300s)", status, raw)
	}
	var change map[string]any
	if err := json.Unmarshal(raw, &change); err != nil {
		t.Fatalf("change is not JSON: %v", err)
	}
	changeID, _ := change["change_id"].(string)
	if changeID == "" {
		t.Fatalf("change carries no change_id: %.300s", raw)
	}
	status, raw, _ = jsonPost(t, attributor, journey.server, "/api/v1/me/position-changes/"+changeID+"/attributions",
		`{"argument_ids":[`+quoteJSON(argumentID)+`]}`)
	if status != http.StatusCreated {
		t.Fatalf("POST attributions status = %d, want 201 (body: %.300s)", status, raw)
	}
	status, raw, headers := jsonPost(t, attributor, journey.server, "/api/v1/me/position-changes/"+changeID+"/attributions",
		`{"argument_ids":[`+quoteJSON(argumentID)+`]}`)
	if status != http.StatusOK {
		t.Fatalf("POST attributions replay status = %d, want 200 (body: %.300s)", status, raw)
	}
	if headers.Get("Idempotency-Replayed") != "true" {
		t.Error("attribution replay carries no Idempotency-Replayed header")
	}
	status, raw = jsonGet(t, anonymous, journey.server, "/api/v1/arguments/"+argumentID+"/attributions")
	if status != http.StatusOK {
		t.Fatalf("GET argument metrics status = %d, want 200", status)
	}
	if !strings.Contains(string(raw), `"valid_attributions":1`) {
		t.Errorf("metrics body = %.300s, want the single valid credit", raw)
	}
	status, raw = jsonGet(t, anonymous, journey.server, "/api/v1/profiles/debateautora/reputation")
	if status != http.StatusOK {
		t.Fatalf("GET reputation status = %d, want 200 (body: %.300s)", status, raw)
	}
	if !strings.Contains(string(raw), "debateautora") {
		t.Errorf("reputation body = %.300s, want the author handle", raw)
	}
	if status, _, _ := jsonPost(t, stranger, journey.server, "/api/v1/me/position-changes/"+changeID+"/attributions",
		`{"argument_ids":[`+quoteJSON(argumentID)+`]}`); status != http.StatusNotFound {
		t.Errorf("POST attributions stranger status = %d, want 404: foreign changes stay indistinguishable", status)
	}
	if status, _, _ := jsonPost(t, anonymous, journey.server, "/api/v1/me/position-changes/"+changeID+"/attributions",
		`{"argument_ids":[`+quoteJSON(argumentID)+`]}`); status != http.StatusUnauthorized {
		t.Errorf("POST attributions anonymous status = %d, want 401", status)
	}
}

// TestDebateSignalsModeratorOnly proves the restricted read: the moderator
// sees the assessment, a plain account is refused without learning whether
// signals exist, and anonymous calls fail closed.
func TestDebateSignalsModeratorOnly(t *testing.T) {
	t.Parallel()

	journey := newDebateJourney(t)
	moderator, moderatorID := debateLogin(t, journey, "debate-moderator@example.test", "correct horse battery staple")
	plain, plainID := debateLogin(t, journey, "debate-plain@example.test", "correct horse battery staple")
	anonymous := browser(t)
	debateGrantModerator(t, journey, moderatorID)

	status, raw := jsonGet(t, moderator, journey.server, "/api/v1/moderation/attribution-signals/"+plainID)
	if status != http.StatusOK {
		t.Fatalf("GET signals moderator status = %d, want 200 (body: %.300s)", status, raw)
	}
	if !strings.Contains(string(raw), `"signals"`) {
		t.Errorf("signals body = %.300s, want the assessment document", raw)
	}
	status, raw = jsonGet(t, plain, journey.server, "/api/v1/moderation/attribution-signals/"+moderatorID)
	if status != http.StatusForbidden {
		t.Fatalf("GET signals plain status = %d, want 403 (body: %.300s)", status, raw)
	}
	if !strings.Contains(string(raw), "not_authorized") {
		t.Errorf("signals refusal body = %.200s, want not_authorized", raw)
	}
	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/moderation/attribution-signals/"+moderatorID); status != http.StatusUnauthorized {
		t.Errorf("GET signals anonymous status = %d, want 401", status)
	}
}
