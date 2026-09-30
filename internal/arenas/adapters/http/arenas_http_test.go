package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/billingpass"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/http"
	arenaspg "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	billingpg "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

const (
	arenaOwnerSessionToken = "arena-owner-session-token"
	arenaOtherSessionToken = "arena-other-session-token"
)

const (
	statementOne   = "A AGI existirá até 2040"
	statementTwo   = "O Brasil sediará a próxima Copa do Mundo"
	statementThree = "A energia de fusão será comercial em 2035"
)

type arenaHarness struct {
	mux         http.Handler
	pool        *pgxpool.Pool
	arenasRepo  *arenaspg.Repository
	billingRepo *billingpg.Repository
	create      *arenasapp.CreateArenaDraftUseCase
	publish     *arenasapp.PublishArenaUseCase
	ownerID     string
	otherID     string
	ownerEmail  string
	otherEmail  string
}

func arenaUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustArenaAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) string {
	t.Helper()
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return arenaUUID(account.ID)
}

func grantArenaPass(t *testing.T, ctx context.Context, repo *billingpg.Repository, accountID, reference string) {
	t.Helper()
	quantity, err := billingdomain.NewQuantity(1)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	parsed, err := billingdomain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", reference, err)
	}
	if _, err := repo.GrantPassLot(ctx, billingapp.GrantPassLotRequest{
		AccountID: billingdomain.AccountID(accountID),
		Origin:    billingdomain.OriginPurchase,
		Quantity:  quantity,
		Reference: parsed,
		GrantedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("GrantPassLot(%q): %v", reference, err)
	}
}

func setupArenaHarness(t *testing.T) *arenaHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	ownerEmail := "arenas-http-owner@arena.example.com"
	otherEmail := "arenas-http-other@arena.example.com"
	ownerID := mustArenaAccount(t, ctx, q, ownerEmail)
	otherID := mustArenaAccount(t, ctx, q, otherEmail)

	arenasRepo := arenaspg.NewRepository(pool)
	billingRepo := billingpg.NewRepository(pool)
	clock := clockseed.NewClock()

	codec, err := arenasapp.NewFeedCursorCodec([]byte("arenas-http-feed-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("build feed cursor codec: %v", err)
	}

	publish := arenasapp.NewPublishArenaUseCase(arenasRepo, billingpass.New(billingRepo, clock), platformpg.NewTxManager(pool), clock)
	create := arenasapp.NewCreateArenaDraftUseCase(arenasRepo, domain.DefaultStatementPolicy())

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

	handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{
		CreateDraftUseCase: create,
		GetDraftUseCase:    arenasapp.NewGetArenaDraftUseCase(arenasRepo),
		ListDraftsUseCase:  arenasapp.NewListArenaDraftsUseCase(arenasRepo),
		UpdateDraftUseCase: arenasapp.NewUpdateArenaDraftUseCase(arenasRepo, domain.DefaultStatementPolicy()),
		DeleteDraftUseCase: arenasapp.NewDeleteArenaDraftUseCase(arenasRepo),
		PublishUseCase:     publish,
		CloseUseCase:       arenasapp.NewCloseArenaUseCase(arenasRepo),
		FeedUseCase:        arenasapp.NewGetArenaFeedUseCase(arenasRepo, codec),
		GetPublicUseCase:   arenasapp.NewGetPublicArenaUseCase(arenasRepo),
		SecurityManager:    secMgr,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case arenaOwnerSessionToken:
			return security.AuthIdentity{AccountID: ownerID, SessionID: "session-owner"}, nil
		case arenaOtherSessionToken:
			return security.AuthIdentity{AccountID: otherID, SessionID: "session-other"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})

	return &arenaHarness{
		mux:         secMgr.AuthenticateMiddleware(validator)(mux),
		pool:        pool,
		arenasRepo:  arenasRepo,
		billingRepo: billingRepo,
		create:      create,
		publish:     publish,
		ownerID:     ownerID,
		otherID:     otherID,
		ownerEmail:  ownerEmail,
		otherEmail:  otherEmail,
	}
}

func arenaAuthenticatedRequest(method, path, token string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	return request
}

func arenaJSONRequest(method, path, token, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	return request
}

func arenaDecodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return object
}

func arenaAssertExactKeys(t *testing.T, object map[string]any, want ...string) {
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

func arenaAssertPrivateHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
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

func arenaAssertPublicHeaders(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	cacheControl := recorder.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "public") || !strings.Contains(cacheControl, "max-age=60") {
		t.Fatalf("Cache-Control = %q, want public max-age=60", cacheControl)
	}
	if vary := recorder.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
	etag := recorder.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag = %q, want a strong quoted validator", etag)
	}
	return etag
}

func TestArenaAPIRequiresAuthentication(t *testing.T) {
	harness := setupArenaHarness(t)

	probes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/me/arena-drafts"},
		{http.MethodGet, "/api/v1/me/arena-drafts"},
		{http.MethodGet, "/api/v1/me/arena-drafts/00000000-0000-0000-0000-000000000000"},
		{http.MethodPatch, "/api/v1/me/arena-drafts/00000000-0000-0000-0000-000000000000"},
		{http.MethodDelete, "/api/v1/me/arena-drafts/00000000-0000-0000-0000-000000000000"},
		{http.MethodPost, "/api/v1/me/arena-drafts/00000000-0000-0000-0000-000000000000/publish"},
		{http.MethodPost, "/api/v1/me/arenas/00000000-0000-0000-0000-000000000000/close"},
	}

	for _, probe := range probes {
		t.Run(probe.method+" "+probe.path, func(t *testing.T) {
			request := httptest.NewRequest(probe.method, probe.path, nil)
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous status = %d, want 401 (body: %s)", recorder.Code, recorder.Body.String())
			}
			if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
			}
			arenaAssertPrivateHeaders(t, recorder)
		})
	}

	invalid := arenaAuthenticatedRequest(http.MethodGet, "/api/v1/me/arena-drafts", "revoked-token")
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, invalid)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid session status = %d, want 401", recorder.Code)
	}
}

func TestArenaDraftLifecycleHTTP(t *testing.T) {
	harness := setupArenaHarness(t)

	createBody := `{"statement":"` + statementOne + `","context":"Contexto opcional do debate","category":"technology","language":"pt-BR"}`
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaJSONRequest(http.MethodPost, "/api/v1/me/arena-drafts", arenaOwnerSessionToken, createBody))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)

	created := arenaDecodeObject(t, recorder.Body.Bytes())
	arenaAssertExactKeys(t, created,
		"id", "slug", "statement", "context", "category", "language", "status", "version", "created_at", "published_at", "closes_at")
	if created["status"] != "draft" || created["version"] != float64(1) || created["slug"] != nil || created["published_at"] != nil {
		t.Fatalf("created draft = %v, want draft v1 without slug", created)
	}
	draftID, _ := created["id"].(string)
	if draftID == "" {
		t.Fatal("created draft has no id")
	}
	if body := recorder.Body.String(); strings.Contains(body, harness.ownerEmail) || strings.Contains(body, "creator_id") {
		t.Fatalf("SECURITY VIOLATION: draft leaked account data: %s", body)
	}

	// Owner list returns only the owned draft.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodGet, "/api/v1/me/arena-drafts", arenaOwnerSessionToken))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)
	list := arenaDecodeObject(t, recorder.Body.Bytes())
	arenaAssertExactKeys(t, list, "items")
	items, ok := list["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("list items = %v, want exactly one draft", list["items"])
	}

	// Read one draft.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodGet, "/api/v1/me/arena-drafts/"+draftID, arenaOwnerSessionToken))
	if recorder.Code != http.StatusOK {
		t.Fatalf("read status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)
	read := arenaDecodeObject(t, recorder.Body.Bytes())
	if read["statement"] != statementOne || read["version"] != float64(1) {
		t.Fatalf("read draft = %v, want v1 with the created statement", read)
	}

	// Update under the optimistic check.
	updateBody := `{"statement":"` + statementTwo + `","context":"","category":"science","language":"pt-BR","expected_version":1}`
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaJSONRequest(http.MethodPatch, "/api/v1/me/arena-drafts/"+draftID, arenaOwnerSessionToken, updateBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)
	updated := arenaDecodeObject(t, recorder.Body.Bytes())
	if updated["statement"] != statementTwo || updated["category"] != "science" || updated["version"] != float64(2) {
		t.Fatalf("updated draft = %v, want v2 science with the new statement", updated)
	}

	// A stale version must not write.
	staleBody := `{"statement":"` + statementThree + `","context":"","category":"science","language":"pt-BR","expected_version":1}`
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaJSONRequest(http.MethodPatch, "/api/v1/me/arena-drafts/"+draftID, arenaOwnerSessionToken, staleBody))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d, want 409 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)
	problem := arenaDecodeObject(t, recorder.Body.Bytes())
	if problem["code"] != "version_conflict" {
		t.Fatalf("stale update code = %v, want version_conflict", problem["code"])
	}

	// Closing a draft is an invalid transition with a stable conflict code.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodPost, "/api/v1/me/arenas/"+draftID+"/close", arenaOwnerSessionToken))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("close draft status = %d, want 409 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)
	problem = arenaDecodeObject(t, recorder.Body.Bytes())
	if problem["code"] != "arena_invalid_status_transition" || problem["status"] != float64(409) {
		t.Fatalf("close draft problem = %v, want arena_invalid_status_transition/409", problem)
	}

	// Delete the draft: 204 without body, then the draft is gone.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodDelete, "/api/v1/me/arena-drafts/"+draftID, arenaOwnerSessionToken))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)
	if recorder.Body.Len() != 0 {
		t.Fatalf("delete body = %q, want empty", recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodGet, "/api/v1/me/arena-drafts/"+draftID, arenaOwnerSessionToken))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("read after delete status = %d, want 404", recorder.Code)
	}
}

func TestArenaPublishAndCloseHTTP(t *testing.T) {
	ctx := context.Background()
	harness := setupArenaHarness(t)

	draft, err := harness.create.Execute(ctx, arenasapp.CreateArenaDraftCommand{
		AccountID: harness.ownerID,
		Statement: statementOne,
		Context:   "Contexto da publicação via API",
		Category:  "technology",
		Language:  "pt-BR",
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	// Publishing without an available pass keeps the draft untouched.
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodPost, "/api/v1/me/arena-drafts/"+draft.ID().String()+"/publish", arenaOwnerSessionToken))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("publish without pass status = %d, want 409 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)
	problem := arenaDecodeObject(t, recorder.Body.Bytes())
	if problem["code"] != "no_pass_available" {
		t.Fatalf("publish without pass code = %v, want no_pass_available", problem["code"])
	}

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodGet, "/api/v1/me/arena-drafts/"+draft.ID().String(), arenaOwnerSessionToken))
	stillDraft := arenaDecodeObject(t, recorder.Body.Bytes())
	if stillDraft["status"] != "draft" {
		t.Fatalf("draft after failed publish = %v, want draft", stillDraft["status"])
	}

	grantArenaPass(t, ctx, harness.billingRepo, harness.ownerID, "stripe:evt_http_publish")

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodPost, "/api/v1/me/arena-drafts/"+draft.ID().String()+"/publish", arenaOwnerSessionToken))
	if recorder.Code != http.StatusOK {
		t.Fatalf("publish status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)
	published := arenaDecodeObject(t, recorder.Body.Bytes())
	slug, _ := published["slug"].(string)
	if published["status"] != "published" || slug == "" || published["published_at"] == nil {
		t.Fatalf("published arena = %v, want published with slug and published_at", published)
	}

	// Editing a published Arena is an invalid transition with a stable
	// conflict code; nothing is written.
	publishedVersion := int(published["version"].(float64))
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaJSONRequest(http.MethodPatch, "/api/v1/me/arena-drafts/"+draft.ID().String(), arenaOwnerSessionToken,
		fmt.Sprintf(`{"statement":%q,"category":"science","language":"pt-BR","expected_version":%d}`, statementThree, publishedVersion)))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("edit published status = %d, want 409 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)
	editProblem := arenaDecodeObject(t, recorder.Body.Bytes())
	if editProblem["code"] != "arena_not_draft" {
		t.Fatalf("edit published code = %v, want arena_not_draft", editProblem["code"])
	}

	// The public document is addressable by slug and carries the public
	// cache policy plus a strong ETag.
	publicPath := "/api/v1/arenas/" + slug
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, publicPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("public read status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	etag := arenaAssertPublicHeaders(t, recorder)
	publicBody := recorder.Body.String()
	if strings.Contains(publicBody, "creator_id") || strings.Contains(publicBody, `"version"`) || strings.Contains(publicBody, harness.ownerEmail) {
		t.Fatalf("SECURITY VIOLATION: public arena leaked internal fields: %s", publicBody)
	}
	publicArena := arenaDecodeObject(t, recorder.Body.Bytes())
	arenaAssertExactKeys(t, publicArena, "id", "slug", "statement", "context", "category", "language", "status", "published_at", "closes_at")
	if publicArena["id"] != draft.ID().String() {
		t.Fatalf("public id = %v, want the stable arena id %s", publicArena["id"], draft.ID())
	}

	// A matching If-None-Match answers 304 without a body.
	conditional := httptest.NewRequest(http.MethodGet, publicPath, nil)
	conditional.Header.Set("If-None-Match", etag)
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, conditional)
	if recorder.Code != http.StatusNotModified {
		t.Fatalf("conditional read status = %d, want 304 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("conditional body = %q, want empty", recorder.Body.String())
	}
	if returned := recorder.Header().Get("ETag"); returned != etag {
		t.Fatalf("conditional ETag = %q, want %q", returned, etag)
	}

	// The public feed carries the same ETag discipline and includes the
	// published Arena.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/arenas", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("feed status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPublicHeaders(t, recorder)
	feed := arenaDecodeObject(t, recorder.Body.Bytes())
	arenaAssertExactKeys(t, feed, "items", "next_cursor")
	feedItems, _ := feed["items"].([]any)
	if len(feedItems) != 1 {
		t.Fatalf("feed items = %v, want one published arena", feed["items"])
	}
	entry, _ := feedItems[0].(map[string]any)
	arenaAssertExactKeys(t, entry, "id", "slug", "statement", "category", "language", "status", "published_at", "closes_at")
	if entry["slug"] != slug {
		t.Fatalf("feed slug = %v, want %q", entry["slug"], slug)
	}

	// Closing is creator-only and replay-safe; the closed Arena stays
	// publicly readable.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodPost, "/api/v1/me/arenas/"+draft.ID().String()+"/close", arenaOwnerSessionToken))
	if recorder.Code != http.StatusOK {
		t.Fatalf("close status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	closed := arenaDecodeObject(t, recorder.Body.Bytes())
	if closed["status"] != "closed" {
		t.Fatalf("closed status = %v, want closed", closed["status"])
	}

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodPost, "/api/v1/me/arenas/"+draft.ID().String()+"/close", arenaOwnerSessionToken))
	if recorder.Code != http.StatusOK {
		t.Fatalf("close replay status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	replayed := arenaDecodeObject(t, recorder.Body.Bytes())
	if replayed["status"] != "closed" {
		t.Fatalf("close replay status = %v, want closed", replayed["status"])
	}

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, publicPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("public read after close status = %d, want 200", recorder.Code)
	}
	if afterClose := arenaDecodeObject(t, recorder.Body.Bytes()); afterClose["status"] != "closed" {
		t.Fatalf("public status after close = %v, want closed", afterClose["status"])
	}
}

func TestArenaDraftOwnerScoped(t *testing.T) {
	ctx := context.Background()
	harness := setupArenaHarness(t)

	draft, err := harness.create.Execute(ctx, arenasapp.CreateArenaDraftCommand{
		AccountID: harness.ownerID,
		Statement: statementOne,
		Category:  "technology",
		Language:  "pt-BR",
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	draftPath := "/api/v1/me/arena-drafts/" + draft.ID().String()

	// The other account sees an empty list and never the owner draft.
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodGet, "/api/v1/me/arena-drafts", arenaOtherSessionToken))
	if recorder.Code != http.StatusOK {
		t.Fatalf("other list status = %d, want 200", recorder.Code)
	}
	otherList := arenaDecodeObject(t, recorder.Body.Bytes())
	if items, _ := otherList["items"].([]any); len(items) != 0 {
		t.Fatalf("other list items = %v, want empty", otherList["items"])
	}

	probes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, draftPath, ""},
		{http.MethodPatch, draftPath, `{"statement":"` + statementTwo + `","category":"science","language":"pt-BR","expected_version":1}`},
		{http.MethodDelete, draftPath, ""},
		{http.MethodPost, draftPath + "/publish", ""},
		{http.MethodPost, "/api/v1/me/arenas/" + draft.ID().String() + "/close", ""},
	}
	for _, probe := range probes {
		t.Run(probe.method, func(t *testing.T) {
			var request *http.Request
			if probe.body != "" {
				request = arenaJSONRequest(probe.method, probe.path, arenaOtherSessionToken, probe.body)
			} else {
				request = arenaAuthenticatedRequest(probe.method, probe.path, arenaOtherSessionToken)
			}
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("intruder status = %d, want 404 (body: %s)", recorder.Code, recorder.Body.String())
			}
		})
	}

	// The owner draft still exists untouched and is not publicly visible.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaAuthenticatedRequest(http.MethodGet, draftPath, arenaOwnerSessionToken))
	if recorder.Code != http.StatusOK {
		t.Fatalf("owner read after intruder status = %d, want 200", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, statementOne) {
		t.Fatalf("owner draft changed after intruder probes: %s", body)
	}

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/arenas", nil))
	if body := recorder.Body.String(); strings.Contains(body, statementOne) {
		t.Fatalf("SECURITY VIOLATION: draft statement appears in the public feed: %s", body)
	}
}

func TestArenaFeedHTTPPaginationAndFilters(t *testing.T) {
	ctx := context.Background()
	harness := setupArenaHarness(t)

	statements := []string{statementOne, statementTwo, statementThree}
	for index, statement := range statements {
		grantArenaPass(t, ctx, harness.billingRepo, harness.ownerID, fmt.Sprintf("stripe:evt_http_feed_%d", index))
		draft, err := harness.create.Execute(ctx, arenasapp.CreateArenaDraftCommand{
			AccountID: harness.ownerID,
			Statement: statement,
			Category:  "technology",
			Language:  "pt-BR",
		})
		if err != nil {
			t.Fatalf("create draft %d: %v", index, err)
		}
		if _, err := harness.publish.Execute(ctx, arenasapp.PublishArenaCommand{
			AccountID: harness.ownerID,
			ArenaID:   draft.ID().String(),
		}); err != nil {
			t.Fatalf("publish draft %d: %v", index, err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/api/v1/arenas?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("page %d status = %d (body: %s)", pages, recorder.Code, recorder.Body.String())
		}
		arenaAssertPublicHeaders(t, recorder)
		pages++

		page := arenaDecodeObject(t, recorder.Body.Bytes())
		arenaAssertExactKeys(t, page, "items", "next_cursor")
		items, ok := page["items"].([]any)
		if !ok || len(items) != 1 {
			t.Fatalf("page %d items = %v, want exactly one", pages, page["items"])
		}
		item, _ := items[0].(map[string]any)
		id, _ := item["id"].(string)
		if id == "" || seen[id] {
			t.Fatalf("page %d delivered duplicate or empty id %q", pages, id)
		}
		seen[id] = true

		nextCursor, _ := page["next_cursor"].(string)
		if nextCursor == "" {
			if page["next_cursor"] != nil {
				t.Fatalf("next_cursor = %v, want null on the last page", page["next_cursor"])
			}
			break
		}
		cursor = nextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != len(statements) || pages != len(statements) {
		t.Fatalf("delivered %d arenas in %d pages, want %d", len(seen), pages, len(statements))
	}

	// Filters and invalid inputs.
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/arenas?language=en-US", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("language filter status = %d, want 200", recorder.Code)
	}
	if filtered := arenaDecodeObject(t, recorder.Body.Bytes()); len(filtered["items"].([]any)) != 0 {
		t.Fatalf("en-US feed = %v, want empty", filtered["items"])
	}

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/arenas?status=closed", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status filter status = %d, want 200", recorder.Code)
	}
	if filtered := arenaDecodeObject(t, recorder.Body.Bytes()); len(filtered["items"].([]any)) != 0 {
		t.Fatalf("closed feed = %v, want empty", filtered["items"])
	}

	// A well-formed but unseeded category is not an error: it filters to an
	// empty page (existence is enforced at write time by the foreign key).
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/arenas?category=unseeded", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unseeded category status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if filtered := arenaDecodeObject(t, recorder.Body.Bytes()); len(filtered["items"].([]any)) != 0 {
		t.Fatalf("unseeded category feed = %v, want empty", filtered["items"])
	}

	probes := []struct {
		name     string
		path     string
		wantCode string
	}{
		{name: "draft status", path: "/api/v1/arenas?status=draft", wantCode: "invalid_filter"},
		{name: "removed status", path: "/api/v1/arenas?status=removed", wantCode: "invalid_filter"},
		{name: "invalid category format", path: "/api/v1/arenas?category=x", wantCode: "arena_invalid_category"},
		{name: "invalid language", path: "/api/v1/arenas?language=xx-XX", wantCode: "arena_unsupported_language"},
		{name: "malformed cursor", path: "/api/v1/arenas?cursor=not-a-cursor", wantCode: "invalid_cursor"},
		{name: "non numeric limit", path: "/api/v1/arenas?limit=abc", wantCode: "invalid_limit"},
		{name: "negative limit", path: "/api/v1/arenas?limit=-1", wantCode: "invalid_limit"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, probe.path, nil))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", recorder.Code, recorder.Body.String())
			}
			problem := arenaDecodeObject(t, recorder.Body.Bytes())
			if problem["code"] != probe.wantCode {
				t.Fatalf("code = %v, want %q", problem["code"], probe.wantCode)
			}
		})
	}
}

func TestArenaPublicReadsNeverLeakPrivateFields(t *testing.T) {
	ctx := context.Background()
	harness := setupArenaHarness(t)

	grantArenaPass(t, ctx, harness.billingRepo, harness.ownerID, "stripe:evt_http_leak")
	draft, err := harness.create.Execute(ctx, arenasapp.CreateArenaDraftCommand{
		AccountID: harness.ownerID,
		Statement: statementOne,
		Context:   "Detalhes internos do contexto publicado",
		Category:  "technology",
		Language:  "pt-BR",
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	result, err := harness.publish.Execute(ctx, arenasapp.PublishArenaCommand{
		AccountID: harness.ownerID,
		ArenaID:   draft.ID().String(),
	})
	if err != nil {
		t.Fatalf("publish draft: %v", err)
	}

	responses := map[string]string{}
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/arenas/"+result.Arena.Slug().String(), nil))
	responses["public arena"] = recorder.Body.String()

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/arenas", nil))
	responses["public feed"] = recorder.Body.String()

	forbidden := []string{
		harness.ownerEmail,
		harness.otherEmail,
		harness.ownerID,
		"creator_id",
		"account_id",
		"stripe",
		"payment",
		"fraud",
		"version",
		"reason",
		"actor",
		"moderation",
		"admin",
	}
	for name, body := range responses {
		for _, marker := range forbidden {
			if strings.Contains(body, marker) {
				t.Fatalf("SECURITY VIOLATION: %s leaked %q: %s", name, marker, body)
			}
		}
	}

	// Nullable public fields are always present, never omitted.
	publicArena := arenaDecodeObject(t, []byte(responses["public arena"]))
	for _, key := range []string{"context", "closes_at"} {
		if _, ok := publicArena[key]; !ok {
			t.Fatalf("public arena is missing nullable key %q", key)
		}
	}
}

func TestArenaDraftBodyLimit(t *testing.T) {
	harness := setupArenaHarness(t)

	huge := strings.Repeat("a", 70<<10)
	body := `{"statement":"` + huge + `","category":"technology","language":"pt-BR"}`
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, arenaJSONRequest(http.MethodPost, "/api/v1/me/arena-drafts", arenaOwnerSessionToken, body))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status = %d, want 400 (body: %s)", recorder.Code, recorder.Body.String())
	}
	arenaAssertPrivateHeaders(t, recorder)
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
	}
	if recorder.Body.Len() > 1024 {
		t.Fatalf("problem body echoes too much of the rejected payload: %d bytes", recorder.Body.Len())
	}
}
