package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/http"
	argumentspg "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/walletdebit"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

const (
	argumentsOwnerToken = "arguments-owner-session-token"
	argumentsOtherToken = "arguments-other-session-token"
)

// arenaGate is a mutable stub of the Arena eligibility port.
type arenaGate struct {
	mu  sync.Mutex
	err error
}

func (g *arenaGate) EnsureAcceptsArguments(context.Context, domain.ArenaID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

func (g *arenaGate) set(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err = err
}

// eligibleAccounts is a stub of the account eligibility port.
type eligibleAccounts struct{ err error }

func (e eligibleAccounts) EnsureEligible(context.Context, domain.AccountID) error { return e.err }

type argumentsHarness struct {
	mux        http.Handler
	pool       *pgxpool.Pool
	gate       *arenaGate
	ownerID    string
	otherID    string
	arenaID    string
	ownerEmail string
	otherEmail string
}

func argumentsUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustArgumentsAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) string {
	t.Helper()
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return argumentsUUID(account.ID)
}

func mustArgumentsArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator string) string {
	t.Helper()
	var creatorUUID pgtype.UUID
	if err := creatorUUID.Scan(creator); err != nil {
		t.Fatalf("parse creator id: %v", err)
	}
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Afirmação para a API de argumentos', 'technology', 'pt-BR', 'draft')
		RETURNING id`, creatorUUID).Scan(&id); err != nil {
		t.Fatalf("insert arena: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE app.arenas SET status = 'published', slug = $2, published_at = now(), version = version + 1
		WHERE id = $1`, id, "arguments-http-arena"); err != nil {
		t.Fatalf("publish arena: %v", err)
	}
	return argumentsUUID(id)
}

// seedArgumentsCredit creates the wallet and credits INK through the real
// wallet repository.
func seedArgumentsCredit(t *testing.T, ctx context.Context, repo *walletpg.Repository, accountID string, amount int64, key string) {
	t.Helper()
	var accountUUID pgtype.UUID
	if err := accountUUID.Scan(accountID); err != nil {
		t.Fatalf("parse account id: %v", err)
	}
	ink, err := walletdomain.NewInk(amount)
	if err != nil {
		t.Fatalf("NewInk: %v", err)
	}
	reference, err := walletdomain.ParseReference("free:2026-09")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	idempotencyKey, err := walletdomain.ParseIdempotencyKey(key)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey: %v", err)
	}
	direction, err := walletdomain.OperationCreditFree.Direction()
	if err != nil {
		t.Fatalf("Direction: %v", err)
	}
	delta, err := direction.Apply(ink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := repo.ApplyCredit(ctx, walletapp.CreditRequest{
		AccountID:      walletdomain.AccountID(accountID),
		Bucket:         walletdomain.BucketFree,
		OperationType:  walletdomain.OperationCreditFree,
		IdempotencyKey: idempotencyKey,
		Reference:      reference,
		Delta:          delta,
		ChangedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}
}

func argumentsBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID string) int64 {
	t.Helper()
	var free int64
	if err := pool.QueryRow(ctx, `
		SELECT balance_free FROM app.wallet_accounts WHERE account_id = $1`, accountID).Scan(&free); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return free
}

func setupArgumentsHarness(t *testing.T) *argumentsHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	ownerEmail := "arguments-http-owner@arena.example.com"
	otherEmail := "arguments-http-other@arena.example.com"
	ownerID := mustArgumentsAccount(t, ctx, q, ownerEmail)
	otherID := mustArgumentsAccount(t, ctx, q, otherEmail)
	arenaID := mustArgumentsArena(t, ctx, pool, ownerID)

	argumentsRepo := argumentspg.NewRepository(pool)
	walletRepo := walletpg.NewRepository(pool)
	clock := clockseed.NewClock()
	bridge := walletdebit.New(walletapp.NewDebitInkUseCase(walletRepo, clock))
	gate := &arenaGate{}

	codec, err := application.NewArgumentCursorCodec([]byte("arguments-http-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("NewArgumentCursorCodec: %v", err)
	}

	handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{
		PublishUseCase: application.NewPublishArgumentUseCase(
			argumentsRepo,
			eligibleAccounts{},
			gate,
			bridge,
			platformpg.NewTxManager(pool),
			text.GraphemeCount,
			domain.DefaultReplyPolicy(),
			clock,
		),
		WithdrawUseCase:  application.NewWithdrawArgumentUseCase(argumentsRepo, clock),
		ListArenaUseCase: application.NewListArenaArgumentsUseCase(argumentsRepo, codec),
		ListRepliesCase:  application.NewListRepliesUseCase(argumentsRepo, codec),
		GetPublicUseCase: application.NewGetPublicArgumentUseCase(argumentsRepo),
		SecurityManager:  mustSecurityManager(t),
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	secMgr := mustSecurityManager(t)
	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case argumentsOwnerToken:
			return security.AuthIdentity{AccountID: ownerID, SessionID: "session-owner"}, nil
		case argumentsOtherToken:
			return security.AuthIdentity{AccountID: otherID, SessionID: "session-other"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})

	return &argumentsHarness{
		mux:        secMgr.AuthenticateMiddleware(validator)(mux),
		pool:       pool,
		gate:       gate,
		ownerID:    ownerID,
		otherID:    otherID,
		arenaID:    arenaID,
		ownerEmail: ownerEmail,
		otherEmail: otherEmail,
	}
}

func mustSecurityManager(t *testing.T) *security.Manager {
	t.Helper()
	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clockseed.NewClock(),
		Random:         clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("setup security manager: %v", err)
	}
	return secMgr
}

func argumentsRequest(method, path, token, body string) *http.Request {
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	}
	return request
}

func argumentsDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return object
}

func argumentsAssertExactKeys(t *testing.T, object map[string]any, want ...string) {
	t.Helper()
	if len(object) != len(want) {
		t.Fatalf("response keys = %v, want exactly %v", object, want)
	}
	for _, key := range want {
		if _, ok := object[key]; !ok {
			t.Fatalf("response is missing key %q (keys: %v)", key, object)
		}
	}
}

func argumentsAssertPrivateHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	cacheControl := recorder.Header().Get("Cache-Control")
	for _, directive := range []string{"private", "no-store", "no-cache", "must-revalidate"} {
		if !strings.Contains(cacheControl, directive) {
			t.Fatalf("Cache-Control = %q, want %q (THR-CACHE-01)", cacheControl, directive)
		}
	}
	if pragma := recorder.Header().Get("Pragma"); pragma != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", pragma)
	}
}

func (h *argumentsHarness) publish(t *testing.T, token, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := argumentsRequest(http.MethodPost, "/api/v1/me/arenas/"+h.arenaID+"/arguments", token, body)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func TestArgumentsAPIRequiresAuthentication(t *testing.T) {
	harness := setupArgumentsHarness(t)

	probes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/me/arenas/" + harness.arenaID + "/arguments"},
		{http.MethodPost, "/api/v1/me/arenas/018f6b2a-0000-7000-8000-000000000002/arguments/018f6b2a-0000-7000-8000-000000000001/replies"},
		{http.MethodPost, "/api/v1/me/arguments/018f6b2a-0000-7000-8000-000000000001/withdraw"},
	}
	for _, probe := range probes {
		t.Run(probe.method+" "+probe.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, argumentsRequest(probe.method, probe.path, "", ""))

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous status = %d, want 401 (body: %s)", recorder.Code, recorder.Body.String())
			}
			if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
			}
			argumentsAssertPrivateHeaders(t, recorder)
		})
	}
}

func TestPublishArgumentJourneyWithWallet(t *testing.T) {
	ctx := context.Background()
	harness := setupArgumentsHarness(t)
	walletRepo := walletpg.NewRepository(harness.pool)
	seedArgumentsCredit(t, ctx, walletRepo, harness.ownerID, 1000, "http:seed:owner")
	seedArgumentsCredit(t, ctx, walletRepo, harness.otherID, 1000, "http:seed:other")

	// The idempotency header is mandatory on state-changing posts.
	missingKey := harness.publish(t, argumentsOwnerToken, "", `{"relation":"support","content":"A AGI existirá até 2040"}`)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing key status = %d, want 400 (body: %s)", missingKey.Code, missingKey.Body.String())
	}
	if problem := argumentsDecode(t, missingKey.Body.Bytes()); problem["code"] != "missing_idempotency_key" {
		t.Fatalf("missing key code = %v, want missing_idempotency_key", problem["code"])
	}

	recorder := harness.publish(t, argumentsOwnerToken, "http-attempt-1",
		`{"relation":"support","content":"A AGI existirá até 2040","sources":[{"url":"https://example.com/estudo","description":"Estudo revisado"}]}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("publish status = %d, want 201 (body: %s)", recorder.Code, recorder.Body.String())
	}
	argumentsAssertPrivateHeaders(t, recorder)

	body := recorder.Body.String()
	if strings.Contains(body, harness.ownerEmail) || strings.Contains(body, harness.ownerID) || strings.Contains(body, "author") {
		t.Fatalf("SECURITY VIOLATION: publish response leaked account data: %s", body)
	}
	published := argumentsDecode(t, recorder.Body.Bytes())
	argumentsAssertExactKeys(t, published, "argument", "replayed")
	if published["replayed"] != false {
		t.Fatalf("fresh publish replayed = %v, want false", published["replayed"])
	}
	argument, _ := published["argument"].(map[string]any)
	argumentsAssertExactKeys(t, argument, "id", "arena_id", "parent_id", "relation", "content", "status", "created_at")
	if argument["relation"] != domain.RelationSupport || argument["content"] != "A AGI existirá até 2040" || argument["status"] != "published" || argument["parent_id"] != nil {
		t.Fatalf("argument = %v, want the published top-level argument", argument)
	}
	argumentID, _ := argument["id"].(string)

	if free := argumentsBalance(t, ctx, harness.pool, harness.ownerID); free != 977 {
		t.Fatalf("balance = %d, want the 23 INK publication cost debited", free)
	}

	// The retry replays without charging again and marks the response.
	retry := harness.publish(t, argumentsOwnerToken, "http-attempt-1", `{"relation":"support","content":"A AGI existirá até 2040"}`)
	if retry.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200 (body: %s)", retry.Code, retry.Body.String())
	}
	if retry.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("Idempotency-Replayed = %q, want true", retry.Header().Get("Idempotency-Replayed"))
	}
	replayed := argumentsDecode(t, retry.Body.Bytes())
	if replayed["replayed"] != true {
		t.Fatalf("retry replayed = %v, want true", replayed["replayed"])
	}
	retriedArgument, _ := replayed["argument"].(map[string]any)
	if retriedArgument["id"] != argumentID {
		t.Fatalf("retry id = %v, want %q", retriedArgument["id"], argumentID)
	}
	if free := argumentsBalance(t, ctx, harness.pool, harness.ownerID); free != 977 {
		t.Fatalf("balance after retry = %d, want a single charge", free)
	}

	// A reply reuses the same use case through the reply route.
	reply := argumentsRequest(http.MethodPost, "/api/v1/me/arenas/"+harness.arenaID+"/arguments/"+argumentID+"/replies", argumentsOtherToken,
		`{"relation":"oppose","content":"Resposta direta ao argumento"}`)
	reply.Header.Set("Idempotency-Key", "http-reply-1")
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, reply)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("reply status = %d, want 201 (body: %s)", recorder.Code, recorder.Body.String())
	}
	replyBody := argumentsDecode(t, recorder.Body.Bytes())
	replyArgument, _ := replyBody["argument"].(map[string]any)
	if replyArgument["parent_id"] != argumentID {
		t.Fatalf("reply parent = %v, want %q", replyArgument["parent_id"], argumentID)
	}
	replyID, _ := replyArgument["id"].(string)

	// A reply to a reply exceeds the single recursion level.
	deep := argumentsRequest(http.MethodPost, "/api/v1/me/arenas/"+harness.arenaID+"/arguments/"+replyID+"/replies", argumentsOwnerToken,
		`{"relation":"context","content":"Resposta a uma resposta"}`)
	deep.Header.Set("Idempotency-Key", "http-reply-2")
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, deep)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("deep reply status = %d, want 409 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if problem := argumentsDecode(t, recorder.Body.Bytes()); problem["code"] != "reply_depth_exceeded" {
		t.Fatalf("deep reply code = %v, want reply_depth_exceeded", problem["code"])
	}
}

func TestArgumentPublicReadsAreCacheable(t *testing.T) {
	ctx := context.Background()
	harness := setupArgumentsHarness(t)
	seedArgumentsCredit(t, ctx, walletpg.NewRepository(harness.pool), harness.ownerID, 1000, "http:seed:owner")

	published := harness.publish(t, argumentsOwnerToken, "http-public-1", `{"relation":"support","content":"A AGI existirá até 2040"}`)
	argument, _ := argumentsDecode(t, published.Body.Bytes())["argument"].(map[string]any)
	argumentID, _ := argument["id"].(string)

	// The per-relation list is public, cacheable and revalidatable.
	listPath := "/api/v1/arenas/" + harness.arenaID + "/arguments?relation=support"
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, argumentsRequest(http.MethodGet, listPath, "", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	cacheControl := recorder.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "public") || !strings.Contains(cacheControl, "max-age=60") {
		t.Fatalf("Cache-Control = %q, want public max-age=60", cacheControl)
	}
	etag := recorder.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag = %q, want a strong quoted validator", etag)
	}
	page := argumentsDecode(t, recorder.Body.Bytes())
	argumentsAssertExactKeys(t, page, "items", "next_cursor")
	items, _ := page["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want the single published argument", page["items"])
	}
	item, _ := items[0].(map[string]any)
	argumentsAssertExactKeys(t, item, "id", "arena_id", "parent_id", "relation", "content", "status", "created_at", "reply_count")
	if item["reply_count"] != float64(0) {
		t.Fatalf("reply_count = %v, want 0", item["reply_count"])
	}

	conditional := argumentsRequest(http.MethodGet, listPath, "", "")
	conditional.Header.Set("If-None-Match", etag)
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, conditional)
	if recorder.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("conditional body = %q, want empty", recorder.Body.String())
	}

	// The public get and replies list resolve without a session.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, argumentsRequest(http.MethodGet, "/api/v1/arguments/"+argumentID, "", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	fetched := argumentsDecode(t, recorder.Body.Bytes())
	argumentsAssertExactKeys(t, fetched, "id", "arena_id", "parent_id", "relation", "content", "status", "created_at")
	if fetched["content"] != "A AGI existirá até 2040" {
		t.Fatalf("get content = %v, want the published content", fetched["content"])
	}

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, argumentsRequest(http.MethodGet, "/api/v1/arguments/"+argumentID+"/replies", "", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("replies status = %d, want 200", recorder.Code)
	}
	argumentsAssertExactKeys(t, argumentsDecode(t, recorder.Body.Bytes()), "items", "next_cursor")

	// Invalid public inputs are client errors.
	probes := []struct {
		path     string
		wantCode string
	}{
		{path: "/api/v1/arenas/" + harness.arenaID + "/arguments", wantCode: "argument_empty_relation"},
		{path: "/api/v1/arenas/" + harness.arenaID + "/arguments?relation=maybe", wantCode: "argument_invalid_relation"},
		{path: "/api/v1/arenas/" + harness.arenaID + "/arguments?relation=support&cursor=bad", wantCode: "invalid_cursor"},
		{path: "/api/v1/arenas/" + harness.arenaID + "/arguments?relation=support&limit=abc", wantCode: "invalid_limit"},
	}
	for _, probe := range probes {
		t.Run(probe.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, argumentsRequest(http.MethodGet, probe.path, "", ""))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", recorder.Code, recorder.Body.String())
			}
			if problem := argumentsDecode(t, recorder.Body.Bytes()); problem["code"] != probe.wantCode {
				t.Fatalf("code = %v, want %q", problem["code"], probe.wantCode)
			}
		})
	}
}

func TestWithdrawArgumentHTTP(t *testing.T) {
	ctx := context.Background()
	harness := setupArgumentsHarness(t)
	seedArgumentsCredit(t, ctx, walletpg.NewRepository(harness.pool), harness.ownerID, 1000, "http:seed:owner")

	published := harness.publish(t, argumentsOwnerToken, "http-withdraw-1", `{"relation":"support","content":"A AGI existirá até 2040"}`)
	argument, _ := argumentsDecode(t, published.Body.Bytes())["argument"].(map[string]any)
	argumentID, _ := argument["id"].(string)

	// A foreign author never reaches the argument.
	foreign := httptest.NewRecorder()
	harness.mux.ServeHTTP(foreign, argumentsRequest(http.MethodPost, "/api/v1/me/arguments/"+argumentID+"/withdraw", argumentsOtherToken, ""))
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign withdraw status = %d, want 404 (body: %s)", foreign.Code, foreign.Body.String())
	}

	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, argumentsRequest(http.MethodPost, "/api/v1/me/arguments/"+argumentID+"/withdraw", argumentsOwnerToken, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("withdraw status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	argumentsAssertPrivateHeaders(t, recorder)
	withdrawn := argumentsDecode(t, recorder.Body.Bytes())
	argumentsAssertExactKeys(t, withdrawn, "argument", "replayed")
	retracted, _ := withdrawn["argument"].(map[string]any)
	if retracted["status"] != "withdrawn" || retracted["content"] != nil {
		t.Fatalf("withdrawn argument = %v, want the retracted placeholder", retracted)
	}

	// The repeat resolves the recorded withdrawal.
	repeat := httptest.NewRecorder()
	harness.mux.ServeHTTP(repeat, argumentsRequest(http.MethodPost, "/api/v1/me/arguments/"+argumentID+"/withdraw", argumentsOwnerToken, ""))
	if repeat.Code != http.StatusOK {
		t.Fatalf("repeat status = %d, want 200", repeat.Code)
	}
	if repeat.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("repeat Idempotency-Replayed = %q, want true", repeat.Header().Get("Idempotency-Replayed"))
	}

	// The public surface shows the placeholder and drops the argument from
	// the list.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, argumentsRequest(http.MethodGet, "/api/v1/arguments/"+argumentID, "", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("public get status = %d, want 200", recorder.Code)
	}
	if fetched := argumentsDecode(t, recorder.Body.Bytes()); fetched["content"] != nil || fetched["status"] != "withdrawn" {
		t.Fatalf("public get = %v, want the retracted placeholder", fetched)
	}
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, argumentsRequest(http.MethodGet, "/api/v1/arenas/"+harness.arenaID+"/arguments?relation=support", "", ""))
	if page := argumentsDecode(t, recorder.Body.Bytes()); len(page["items"].([]any)) != 0 {
		t.Fatalf("list items = %v, want the withdrawn argument excluded", page["items"])
	}
}

func TestArgumentPublishErrors(t *testing.T) {
	ctx := context.Background()
	harness := setupArgumentsHarness(t)

	t.Run("insufficient ink", func(t *testing.T) {
		// No credit seeded: the wallet cannot pay the publication cost.
		recorder := harness.publish(t, argumentsOwnerToken, "http-poor-1", `{"relation":"support","content":"A AGI existirá até 2040"}`)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body: %s)", recorder.Code, recorder.Body.String())
		}
		if problem := argumentsDecode(t, recorder.Body.Bytes()); problem["code"] != "insufficient_ink" {
			t.Fatalf("code = %v, want insufficient_ink", problem["code"])
		}
	})

	t.Run("closed arena", func(t *testing.T) {
		seedArgumentsCredit(t, ctx, walletpg.NewRepository(harness.pool), harness.ownerID, 1000, "http:seed:closed")
		harness.gate.set(application.ErrArenaNotOpen)
		defer harness.gate.set(nil)

		recorder := harness.publish(t, argumentsOwnerToken, "http-closed-1", `{"relation":"support","content":"A AGI existirá até 2040"}`)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body: %s)", recorder.Code, recorder.Body.String())
		}
		if problem := argumentsDecode(t, recorder.Body.Bytes()); problem["code"] != "arena_not_open" {
			t.Fatalf("code = %v, want arena_not_open", problem["code"])
		}
	})

	t.Run("validation", func(t *testing.T) {
		recorder := harness.publish(t, argumentsOwnerToken, "http-invalid-1", `{"relation":"maybe","content":"A AGI existirá até 2040"}`)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", recorder.Code, recorder.Body.String())
		}
		if problem := argumentsDecode(t, recorder.Body.Bytes()); problem["code"] != "argument_invalid_relation" {
			t.Fatalf("code = %v, want argument_invalid_relation", problem["code"])
		}
	})

	t.Run("body limit", func(t *testing.T) {
		huge := strings.Repeat("a", 70<<10)
		recorder := harness.publish(t, argumentsOwnerToken, "http-huge-1", `{"relation":"support","content":"`+huge+`"}`)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", recorder.Code, recorder.Body.String())
		}
		argumentsAssertPrivateHeaders(t, recorder)
		if recorder.Body.Len() > 1024 {
			t.Fatalf("problem body echoes too much of the rejected payload: %d bytes", recorder.Body.Len())
		}
	})
}
