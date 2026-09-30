package security_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

type mockSessionValidator struct {
	validTokens map[string]security.AuthIdentity
	errToReturn error
}

func (m *mockSessionValidator) ValidateSession(ctx context.Context, rawToken string) (security.AuthIdentity, error) {
	if m.errToReturn != nil {
		return security.AuthIdentity{}, m.errToReturn
	}
	identity, ok := m.validTokens[rawToken]
	if !ok {
		return security.AuthIdentity{}, apperr.New(apperr.KindUnauthorized, "invalid_session", "session not found or expired")
	}
	return identity, nil
}

func TestAuthMiddleware_ValidSession(t *testing.T) {
	cookies := security.NewCookieConfig(config.EnvTest)
	validator := &mockSessionValidator{
		validTokens: map[string]security.AuthIdentity{
			"valid-session-123": {
				AccountID: "acc_456",
				SessionID: "sess_789",
			},
		},
	}

	var capturedIdentity security.AuthIdentity
	var capturedOk bool

	targetHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedIdentity, capturedOk = security.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := security.Authenticate(validator, cookies)(targetHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
	req.AddCookie(&http.Cookie{
		Name:  security.DefaultSessionCookieName,
		Value: "valid-session-123",
	})
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if !capturedOk {
		t.Fatal("expected identity to be present in context")
	}
	if capturedIdentity.AccountID != "acc_456" || capturedIdentity.SessionID != "sess_789" {
		t.Fatalf("unexpected identity: %+v", capturedIdentity)
	}
}

func TestAuthMiddleware_MissingSession(t *testing.T) {
	cookies := security.NewCookieConfig(config.EnvTest)
	validator := &mockSessionValidator{
		validTokens: map[string]security.AuthIdentity{},
	}

	var capturedOk bool

	targetHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, capturedOk = security.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := security.Authenticate(validator, cookies)(targetHandler)

	// No session cookie provided; public endpoint scenario
	req := httptest.NewRequest(http.MethodGet, "/api/v1/public", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if capturedOk {
		t.Fatal("expected identity to NOT be present in context for unauthenticated request")
	}
}

func TestAuthMiddleware_InvalidOrExpiredSession(t *testing.T) {
	cookies := security.NewCookieConfig(config.EnvTest)
	validator := &mockSessionValidator{
		validTokens: map[string]security.AuthIdentity{},
		errToReturn: errors.New("session expired or revoked"),
	}

	targetHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("target handler should not be reached when session is invalid")
	})

	handler := security.Authenticate(validator, cookies)(targetHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
	req.AddCookie(&http.Cookie{
		Name:  security.DefaultSessionCookieName,
		Value: "expired-or-revoked-token",
	})
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/problem+json" {
		t.Fatalf("expected media type application/problem+json, got %q", contentType)
	}

	var problem struct {
		Code   string `json:"code"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("failed to decode problem details json: %v", err)
	}
	if problem.Status != http.StatusUnauthorized {
		t.Fatalf("expected problem status 401, got %d", problem.Status)
	}
	if problem.Code != "invalid_session" {
		t.Fatalf("expected code 'invalid_session', got %q", problem.Code)
	}

	// Verify that the invalid cookie is explicitly cleared in the response
	setCookies := w.Result().Cookies()
	var clearedSession bool
	for _, c := range setCookies {
		if c.Name == security.DefaultSessionCookieName && c.MaxAge == -1 {
			clearedSession = true
		}
	}
	if !clearedSession {
		t.Fatal("expected session cookie to be cleared with MaxAge=-1 on invalid session")
	}
}

func TestRequireAuthMiddleware(t *testing.T) {
	requireAuth := security.RequireAuth()

	innerReached := false
	target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerReached = true
		w.WriteHeader(http.StatusOK)
	})

	chained := requireAuth(target)

	// 1. Unauthenticated request must be rejected with 401
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	unauthW := httptest.NewRecorder()

	chained.ServeHTTP(unauthW, unauthReq)

	if unauthW.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", unauthW.Code)
	}
	if innerReached {
		t.Fatal("inner handler must not be reached for unauthenticated caller")
	}

	var problem struct {
		Code   string `json:"code"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(unauthW.Body.Bytes(), &problem); err != nil {
		t.Fatalf("failed to unmarshal problem: %v", err)
	}
	if problem.Code != "unauthorized" {
		t.Fatalf("expected code 'unauthorized', got %q", problem.Code)
	}

	// 2. Authenticated context must pass through
	authReq := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	ctx := security.WithAuth(authReq.Context(), security.AuthIdentity{
		AccountID: "acc_active",
		SessionID: "sess_active",
	})
	authW := httptest.NewRecorder()

	chained.ServeHTTP(authW, authReq.WithContext(ctx))

	if authW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", authW.Code)
	}
	if !innerReached {
		t.Fatal("inner handler must be reached for authenticated caller")
	}
}
