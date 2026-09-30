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

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

const (
	ownerSessionToken = "pass-owner-session"
	otherSessionToken = "pass-other-session"
)

type testHarness struct {
	mux        http.Handler
	ownerEmail string
	otherEmail string
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) platformpg.AppAccount {
	t.Helper()
	acc, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return acc
}

func mustGrant(t *testing.T, ctx context.Context, repo *postgres.Repository, accountID domain.AccountID, origin domain.PassOrigin, quantity int32, reference string, expiresAt *time.Time) {
	t.Helper()
	parsedQuantity, err := domain.NewQuantity(quantity)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	parsedReference, err := domain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if _, err := repo.GrantPassLot(ctx, application.GrantPassLotRequest{
		AccountID: accountID,
		Origin:    origin,
		Quantity:  parsedQuantity,
		Reference: parsedReference,
		ExpiresAt: expiresAt,
		GrantedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("grant %s: %v", reference, err)
	}
}

func mustArenaID(t *testing.T, index int) domain.ArenaID {
	t.Helper()
	arenaID, err := domain.ParseArenaID(fmt.Sprintf("00000000-0000-7000-8000-%012d", index))
	if err != nil {
		t.Fatalf("ParseArenaID(%d): %v", index, err)
	}
	return arenaID
}

func mustConsume(t *testing.T, ctx context.Context, repo *postgres.Repository, accountID domain.AccountID, arena domain.ArenaID) {
	t.Helper()
	if _, err := repo.ConsumeArenaPass(ctx, application.ConsumePassRequest{
		AccountID:  accountID,
		ArenaID:    arena,
		ConsumedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("consume %s: %v", arena, err)
	}
}

func setupPassHarness(t *testing.T) *testHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustAccount(t, ctx, q, "pass-http-owner@arena.example.com")
	other := mustAccount(t, ctx, q, "pass-http-other@arena.example.com")
	ownerID := domain.AccountID(uuidString(owner.ID))
	otherID := domain.AccountID(uuidString(other.ID))

	now := time.Now().UTC()

	// Owner lots: a purchase with one consumed pass, a valid Member lot and
	// an expired Member lot that stays in the breakdown.
	mustGrant(t, ctx, repo, ownerID, domain.OriginPurchase, 3, "stripe:evt_http_purchase", nil)
	mustConsume(t, ctx, repo, ownerID, mustArenaID(t, 1))
	soonEnd := now.Add(24 * time.Hour)
	mustGrant(t, ctx, repo, ownerID, domain.OriginMember, 1, "member:2026-09", &soonEnd)
	expiredEnd := now.Add(-time.Hour)
	mustGrant(t, ctx, repo, ownerID, domain.OriginMember, 2, "member:2026-08", &expiredEnd)

	// Two more consumptions so history has three entries.
	mustGrant(t, ctx, repo, ownerID, domain.OriginPurchase, 2, "stripe:evt_http_purchase_2", nil)
	mustConsume(t, ctx, repo, ownerID, mustArenaID(t, 2))
	mustConsume(t, ctx, repo, ownerID, mustArenaID(t, 3))

	// The other account must never leak into owner responses.
	mustGrant(t, ctx, repo, otherID, domain.OriginPurchase, 1, "stripe:evt_http_other", nil)

	codec, err := application.NewHistoryCursorCodec([]byte("pass-http-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("build history codec: %v", err)
	}

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
		GetArenaPassSummaryUseCase: application.NewGetArenaPassSummaryUseCase(repo, clockseed.NewClock()),
		GetArenaPassHistoryUseCase: application.NewGetArenaPassHistoryUseCase(repo, codec),
		SecurityManager:            secMgr,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case ownerSessionToken:
			return security.AuthIdentity{AccountID: ownerID.String(), SessionID: "session-owner"}, nil
		case otherSessionToken:
			return security.AuthIdentity{AccountID: otherID.String(), SessionID: "session-other"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})

	return &testHarness{
		mux:        secMgr.AuthenticateMiddleware(validator)(mux),
		ownerEmail: "pass-http-owner@arena.example.com",
		otherEmail: "pass-http-other@arena.example.com",
	}
}

func authenticatedRequest(method, path, token string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	return request
}

func decodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return object
}

func assertExactKeys(t *testing.T, object map[string]any, want ...string) {
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

func assertPrivateCacheHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
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

func TestPassAPIRequiresAuthentication(t *testing.T) {
	harness := setupPassHarness(t)

	for _, path := range []string{"/api/v1/me/passes", "/api/v1/me/passes/history"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous status = %d, want 401 (body: %s)", recorder.Code, recorder.Body.String())
			}
			if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
			}
			assertPrivateCacheHeaders(t, recorder)
		})
	}

	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/me/passes", "revoked-token"))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid session status = %d, want 401", recorder.Code)
	}
}

func TestPassSummaryHTTP(t *testing.T) {
	harness := setupPassHarness(t)

	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/me/passes", ownerSessionToken))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertPrivateCacheHeaders(t, recorder)

	body := recorder.Body.String()
	if strings.Contains(body, harness.ownerEmail) || strings.Contains(body, "account_id") || strings.Contains(body, "stripe:evt") {
		t.Fatalf("SECURITY VIOLATION: summary leaked internal data: %s", body)
	}

	summary := decodeObject(t, recorder.Body.Bytes())
	assertExactKeys(t, summary, "available_total", "checked_at", "lots")
	// Consumption follows the nearest-expiration priority: the second and
	// third publications consumed the Member lot and one bought pass, so the
	// owner is left with 2 + 0 + 1 = 3 available passes.
	if summary["available_total"] != float64(3) {
		t.Fatalf("available_total = %v, want 3", summary["available_total"])
	}

	lots, ok := summary["lots"].([]any)
	if !ok || len(lots) != 4 {
		t.Fatalf("lots = %v, want 4 breakdown entries", summary["lots"])
	}
	expiredFlagged := 0
	for _, rawLot := range lots {
		lot, ok := rawLot.(map[string]any)
		if !ok {
			t.Fatalf("lot = %v, want an object", rawLot)
		}
		assertExactKeys(t, lot, "origin", "quantity", "remaining", "expires_at", "expired", "created_at")
		if lot["expired"] == true {
			expiredFlagged++
		}
		if _, err := time.Parse(time.RFC3339, lot["created_at"].(string)); err != nil {
			t.Errorf("created_at is not RFC 3339: %v", lot["created_at"])
		}
	}
	if expiredFlagged != 1 {
		t.Fatalf("expired lots flagged = %d, want 1", expiredFlagged)
	}

	// Ownership: the other account sees only its own single lot.
	otherRecorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(otherRecorder, authenticatedRequest(http.MethodGet, "/api/v1/me/passes", otherSessionToken))
	if otherRecorder.Code != http.StatusOK {
		t.Fatalf("other status = %d, want 200", otherRecorder.Code)
	}
	otherSummary := decodeObject(t, otherRecorder.Body.Bytes())
	if otherSummary["available_total"] != float64(1) {
		t.Fatalf("other available_total = %v, want 1", otherSummary["available_total"])
	}
}

func TestPassHistoryHTTPPagination(t *testing.T) {
	harness := setupPassHarness(t)

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/api/v1/me/passes/history?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, authenticatedRequest(http.MethodGet, path, ownerSessionToken))
		if recorder.Code != http.StatusOK {
			t.Fatalf("page %d status = %d (body: %s)", pages, recorder.Code, recorder.Body.String())
		}
		assertPrivateCacheHeaders(t, recorder)
		pages++

		body := recorder.Body.String()
		if strings.Contains(body, harness.otherEmail) || strings.Contains(body, "stripe:evt_http_other") {
			t.Fatalf("SECURITY VIOLATION: history leaked another account: %s", body)
		}

		page := decodeObject(t, recorder.Body.Bytes())
		assertExactKeys(t, page, "items", "next_cursor")
		items, ok := page["items"].([]any)
		if !ok {
			t.Fatalf("items = %v, want an array", page["items"])
		}
		for _, rawItem := range items {
			item, ok := rawItem.(map[string]any)
			if !ok {
				t.Fatalf("item = %v, want an object", rawItem)
			}
			assertExactKeys(t, item, "consumption_id", "arena_id", "origin", "reference", "consumed_at")
			consumptionID, _ := item["consumption_id"].(string)
			if seen[consumptionID] {
				t.Fatalf("duplicate entry %q", consumptionID)
			}
			seen[consumptionID] = true
			if _, err := time.Parse(time.RFC3339, item["consumed_at"].(string)); err != nil {
				t.Errorf("consumed_at is not RFC 3339: %v", item["consumed_at"])
			}
		}

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

	if len(seen) != 3 {
		t.Fatalf("entries delivered = %d, want 3", len(seen))
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3 for limit 1 and 3 entries", pages)
	}
}

func TestPassHistoryHTTPInvalidInput(t *testing.T) {
	harness := setupPassHarness(t)

	probes := []struct {
		name     string
		path     string
		wantCode string
	}{
		{name: "malformed cursor", path: "/api/v1/me/passes/history?cursor=not-a-cursor", wantCode: "invalid_cursor"},
		{name: "unsigned cursor", path: "/api/v1/me/passes/history?cursor=" + "djF8MjAyNi0wOS0xN1QxMjowMDowMFp8ZW50cnktYQ", wantCode: "invalid_cursor"},
		{name: "non numeric limit", path: "/api/v1/me/passes/history?limit=abc", wantCode: "invalid_limit"},
		{name: "negative limit", path: "/api/v1/me/passes/history?limit=-1", wantCode: "invalid_limit"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, authenticatedRequest(http.MethodGet, probe.path, ownerSessionToken))

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", recorder.Code, recorder.Body.String())
			}
			if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
			}
			assertPrivateCacheHeaders(t, recorder)

			problem := decodeObject(t, recorder.Body.Bytes())
			if problem["code"] != probe.wantCode {
				t.Fatalf("problem code = %v, want %q", problem["code"], probe.wantCode)
			}
		})
	}
}

// TestPassAPIHasNoMutationEndpoints proves the phase rule "sem endpoint
// público de grant/consume": only the two GET routes exist.
func TestPassAPIHasNoMutationEndpoints(t *testing.T) {
	harness := setupPassHarness(t)

	for _, path := range []string{"/api/v1/me/passes", "/api/v1/me/passes/history"} {
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, authenticatedRequest(http.MethodPost, path, ownerSessionToken))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want 405", path, recorder.Code)
		}
	}

	for _, path := range []string{"/api/v1/me/passes/grant", "/api/v1/me/passes/consume"} {
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, authenticatedRequest(http.MethodPost, path, ownerSessionToken))
		if recorder.Code != http.StatusNotFound && recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want 404/405", path, recorder.Code)
		}
	}
}
