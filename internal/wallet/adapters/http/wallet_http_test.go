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

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/http"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

const (
	ownerSessionToken = "owner-session-token"
	otherSessionToken = "other-session-token"
)

const (
	adminReasonText = "concessão manual aprovada no ticket 99"
	adminReference  = "admin:ticket-99"
)

type testHarness struct {
	mux          http.Handler
	ownerEmail   string
	otherEmail   string
	ownerID      domain.AccountID
	otherID      domain.AccountID
	operatorID   domain.AccountID
	ownerEntries int
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

func seedCredit(t *testing.T, ctx context.Context, repo *walletpg.Repository, accountID domain.AccountID, bucket domain.Bucket, operationType domain.OperationType, amount int64, reference, key string, reason domain.Reason, actor domain.AccountID) {
	t.Helper()
	ink, err := domain.NewInk(amount)
	if err != nil {
		t.Fatalf("NewInk(%d): %v", amount, err)
	}
	ref, err := domain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", reference, err)
	}
	idempotencyKey, err := domain.ParseIdempotencyKey(key)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey(%q): %v", key, err)
	}
	direction, err := operationType.Direction()
	if err != nil {
		t.Fatalf("Direction(%q): %v", operationType, err)
	}
	delta, err := direction.Apply(ink)
	if err != nil {
		t.Fatalf("Apply(%d): %v", amount, err)
	}
	if _, err := repo.ApplyCredit(ctx, application.CreditRequest{
		AccountID:      accountID,
		Bucket:         bucket,
		OperationType:  operationType,
		IdempotencyKey: idempotencyKey,
		Reference:      ref,
		Reason:         reason,
		ActorAccountID: actor,
		Delta:          delta,
		ChangedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed credit %s: %v", key, err)
	}
}

func seedDebit(t *testing.T, ctx context.Context, repo *walletpg.Repository, accountID domain.AccountID, amount int64, reference, key string) {
	t.Helper()
	ink, err := domain.NewInk(amount)
	if err != nil {
		t.Fatalf("NewInk(%d): %v", amount, err)
	}
	ref, err := domain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", reference, err)
	}
	idempotencyKey, err := domain.ParseIdempotencyKey(key)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey(%q): %v", key, err)
	}
	if _, err := repo.ApplyDebit(ctx, application.DebitRequest{
		AccountID:      accountID,
		OperationType:  domain.OperationDebitArgument,
		IdempotencyKey: idempotencyKey,
		Reference:      ref,
		Amount:         ink,
		ChangedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed debit %s: %v", key, err)
	}
}

func setupWalletHarness(t *testing.T) *testHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustAccount(t, ctx, q, "wallet-http-owner@arena.example.com")
	other := mustAccount(t, ctx, q, "wallet-http-other@arena.example.com")
	operator := mustAccount(t, ctx, q, "wallet-http-operator@arena.example.com")

	ownerID := domain.AccountID(uuidString(owner.ID))
	otherID := domain.AccountID(uuidString(other.ID))
	operatorID := domain.AccountID(uuidString(operator.ID))

	// Owner history: franchise grant, a publication debit and one audited
	// administrative adjustment.
	seedCredit(t, ctx, repo, ownerID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:2026-09", "http-owner:free", domain.Reason{}, "")
	seedDebit(t, ctx, repo, ownerID, 1200, "argument:http-1", "http-owner:debit")
	adminReason, err := domain.ParseReason(adminReasonText)
	if err != nil {
		t.Fatalf("parse admin reason: %v", err)
	}
	seedCredit(t, ctx, repo, ownerID, domain.BucketPurchased, domain.OperationCreditAdmin, 300, adminReference, "http-owner:admin", adminReason, operatorID)

	// The other account must never leak into the owner responses.
	seedCredit(t, ctx, repo, otherID, domain.BucketPurchased, domain.OperationCreditPurchase, 700, "stripe:other-purchase", "http-other:purchase", domain.Reason{}, "")

	codec, err := application.NewStatementCursorCodec([]byte("wallet-http-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("build cursor codec: %v", err)
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
		GetWalletBalanceUseCase:   application.NewGetWalletBalanceUseCase(repo),
		GetWalletStatementUseCase: application.NewGetWalletStatementUseCase(repo, codec),
		SecurityManager:           secMgr,
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
		mux:          secMgr.AuthenticateMiddleware(validator)(mux),
		ownerEmail:   "wallet-http-owner@arena.example.com",
		otherEmail:   "wallet-http-other@arena.example.com",
		ownerID:      ownerID,
		otherID:      otherID,
		operatorID:   operatorID,
		ownerEntries: 3,
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

func TestWalletAPIRequiresAuthentication(t *testing.T) {
	harness := setupWalletHarness(t)

	for _, path := range []string{"/api/v1/me/wallet", "/api/v1/me/wallet/transactions"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous status = %d, want 401 (body: %s)", recorder.Code, recorder.Body.String())
			}
			if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
			}
			assertPrivateCacheHeaders(t, recorder)
		})
	}

	invalid := authenticatedRequest(http.MethodGet, "/api/v1/me/wallet", "revoked-token")
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, invalid)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid session status = %d, want 401", recorder.Code)
	}
}

func TestWalletBalanceHTTP(t *testing.T) {
	harness := setupWalletHarness(t)

	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/me/wallet", ownerSessionToken))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertPrivateCacheHeaders(t, recorder)

	body := recorder.Body.String()
	if strings.Contains(body, harness.ownerEmail) || strings.Contains(body, "account_id") {
		t.Fatalf("SECURITY VIOLATION: balance leaked account data: %s", body)
	}

	balance := decodeObject(t, recorder.Body.Bytes())
	assertExactKeys(t, balance, "balance_free", "balance_purchased")
	if balance["balance_free"] != float64(3800) || balance["balance_purchased"] != float64(300) {
		t.Fatalf("balance = %v, want 3800 free / 300 purchased", balance)
	}

	otherRecorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(otherRecorder, authenticatedRequest(http.MethodGet, "/api/v1/me/wallet", otherSessionToken))
	if otherRecorder.Code != http.StatusOK {
		t.Fatalf("other status = %d, want 200", otherRecorder.Code)
	}
	otherBalance := decodeObject(t, otherRecorder.Body.Bytes())
	if otherBalance["balance_free"] != float64(0) || otherBalance["balance_purchased"] != float64(700) {
		t.Fatalf("other balance = %v, want 0/700", otherBalance)
	}
}

func TestWalletStatementHTTPPagination(t *testing.T) {
	harness := setupWalletHarness(t)

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/api/v1/me/wallet/transactions?limit=2"
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
		if strings.Contains(body, harness.otherEmail) || strings.Contains(body, "other-purchase") {
			t.Fatalf("SECURITY VIOLATION: owner statement leaked another account: %s", body)
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
			assertExactKeys(t, item, "transaction_id", "operation_id", "operation_type", "bucket", "amount", "reference", "created_at")

			transactionID, _ := item["transaction_id"].(string)
			if transactionID == "" {
				t.Fatal("entry without transaction_id")
			}
			if seen[transactionID] {
				t.Fatalf("duplicate entry %q across pages", transactionID)
			}
			seen[transactionID] = true

			createdAt, _ := item["created_at"].(string)
			if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
				t.Errorf("created_at %q is not RFC 3339: %v", createdAt, err)
			}
			if _, ok := item["amount"].(float64); !ok {
				t.Errorf("amount = %v, want an integer", item["amount"])
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

	if len(seen) != harness.ownerEntries {
		t.Fatalf("entries delivered = %d, want %d", len(seen), harness.ownerEntries)
	}
	if pages < 2 {
		t.Fatalf("pages = %d, want multiple pages for limit 2", pages)
	}
}

// TestWalletStatementHTTPNeverLeaksRestrictedFields covers the phase rule
// "ausência de detalhes antifraude": administrative justifications, actors,
// account emails and payment markers never serialize.
func TestWalletStatementHTTPNeverLeaksRestrictedFields(t *testing.T) {
	harness := setupWalletHarness(t)

	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/me/wallet/transactions?limit=100", ownerSessionToken))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	forbidden := []string{
		adminReasonText,
		harness.operatorID.String(),
		harness.ownerEmail,
		harness.otherEmail,
		"stripe",
		"fraud",
		"reason",
		"actor",
	}
	for _, marker := range forbidden {
		if strings.Contains(body, marker) {
			t.Fatalf("SECURITY VIOLATION: statement leaked %q: %s", marker, body)
		}
	}

	// The administrative adjustment appears only through its stable type.
	if !strings.Contains(body, `"operation_type":"credit_admin"`) {
		t.Fatalf("statement is missing the administrative operation type: %s", body)
	}
	if !strings.Contains(body, `"reference":"`+adminReference+`"`) {
		t.Fatalf("statement is missing the administrative reference: %s", body)
	}
}

func TestWalletStatementHTTPInvalidInput(t *testing.T) {
	harness := setupWalletHarness(t)

	probes := []struct {
		name     string
		path     string
		wantCode string
	}{
		{name: "malformed cursor", path: "/api/v1/me/wallet/transactions?cursor=not-a-cursor", wantCode: "invalid_cursor"},
		{name: "unsigned cursor", path: "/api/v1/me/wallet/transactions?cursor=" + "djF8MjAyNi0wOS0xN1QxMjowMDowMFp8dHJhbnNhY3Rpb24tYQ", wantCode: "invalid_cursor"},
		{name: "non numeric limit", path: "/api/v1/me/wallet/transactions?limit=abc", wantCode: "invalid_limit"},
		{name: "negative limit", path: "/api/v1/me/wallet/transactions?limit=-1", wantCode: "invalid_limit"},
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

func TestWalletStatementOwnerScoped(t *testing.T) {
	harness := setupWalletHarness(t)

	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/me/wallet/transactions?limit=100", otherSessionToken))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	body := recorder.Body.String()
	if strings.Contains(body, "free:2026-09") || strings.Contains(body, "argument:http-1") || strings.Contains(body, adminReference) {
		t.Fatalf("SECURITY VIOLATION: other account saw owner entries: %s", body)
	}
	if !strings.Contains(body, "stripe:other-purchase") {
		t.Fatalf("other statement is missing its own entry: %s", body)
	}
}
