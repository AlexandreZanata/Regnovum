package security_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

type staticClock struct {
	now time.Time
}

func (s staticClock) Now() time.Time {
	return s.now
}

func TestPostLoginRotation(t *testing.T) {
	clock := staticClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &deterministicRandom{sequence: 10}

	mgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"https://app.goyimarena.com"},
		RequireOrigin:  true,
		Clock:          clock,
		Random:         rnd,
	})
	if err != nil {
		t.Fatalf("new security manager: %v", err)
	}

	// 1. Client loads a page; receives initial pre-login CSRF token (ga_csrf_pre)
	wInit := httptest.NewRecorder()
	initialCSRF, err := mgr.IssueCSRFToken(wInit)
	if err != nil {
		t.Fatalf("issue initial csrf: %v", err)
	}

	// 2. Client logs in. Handler calls RotateOnLogin
	wLogin := httptest.NewRecorder()
	newSessionToken := "opaque_session_xyz_789"
	rotatedCSRF, err := mgr.RotateOnLogin(wLogin, newSessionToken, 24*time.Hour)
	if err != nil {
		t.Fatalf("rotate on login: %v", err)
	}

	if rotatedCSRF == initialCSRF {
		t.Fatal("rotated CSRF token must differ from initial pre-login token to prevent CSRF fixation")
	}

	// Verify response cookies from login
	loginCookies := wLogin.Result().Cookies()
	var foundSessionCookie, foundCSRFCookie bool
	for _, c := range loginCookies {
		if c.Name == security.DefaultSessionCookieName && c.Value == newSessionToken {
			foundSessionCookie = true
		}
		if c.Name == security.DefaultCSRFCookieName && c.Value == rotatedCSRF {
			foundCSRFCookie = true
		}
	}
	if !foundSessionCookie {
		t.Fatal("session cookie not set on login response")
	}
	if !foundCSRFCookie {
		t.Fatal("rotated CSRF cookie not set on login response")
	}

	// 3. Build a protected mutating endpoint requiring both CSRF and Authentication
	validator := &mockSessionValidator{
		validTokens: map[string]security.AuthIdentity{
			newSessionToken: {
				AccountID: "acc_user_1",
				SessionID: "sess_user_1",
			},
		},
	}

	target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := security.FromContext(r.Context())
		if !ok || identity.AccountID != "acc_user_1" {
			http.Error(w, "bad identity", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	pipeline := mgr.CSRFMiddleware()(
		mgr.AuthenticateMiddleware(validator)(
			mgr.RequireAuthMiddleware()(target),
		),
	)

	// 4. Request with old (pre-login) CSRF token header must be REJECTED with 403
	reqOldCSRF := httptest.NewRequest(http.MethodPost, "/api/v1/wallet/spend", nil)
	reqOldCSRF.Header.Set("Origin", "https://app.goyimarena.com")
	reqOldCSRF.Header.Set(security.DefaultCSRFHeaderName, initialCSRF) // old token
	reqOldCSRF.AddCookie(&http.Cookie{
		Name:  security.DefaultSessionCookieName,
		Value: newSessionToken,
	})
	reqOldCSRF.AddCookie(&http.Cookie{
		Name:  security.DefaultCSRFCookieName,
		Value: rotatedCSRF, // cookie has new token
	})

	wOld := httptest.NewRecorder()
	pipeline.ServeHTTP(wOld, reqOldCSRF)
	if wOld.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden with old CSRF token, got %d", wOld.Code)
	}

	// 5. Request with rotated CSRF token must SUCCEED with 200 OK
	reqNewCSRF := httptest.NewRequest(http.MethodPost, "/api/v1/wallet/spend", nil)
	reqNewCSRF.Header.Set("Origin", "https://app.goyimarena.com")
	reqNewCSRF.Header.Set(security.DefaultCSRFHeaderName, rotatedCSRF) // new token
	reqNewCSRF.AddCookie(&http.Cookie{
		Name:  security.DefaultSessionCookieName,
		Value: newSessionToken,
	})
	reqNewCSRF.AddCookie(&http.Cookie{
		Name:  security.DefaultCSRFCookieName,
		Value: rotatedCSRF,
	})

	wNew := httptest.NewRecorder()
	pipeline.ServeHTTP(wNew, reqNewCSRF)
	if wNew.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with rotated CSRF token, got %d (body: %s)", wNew.Code, wNew.Body.String())
	}
}

func TestPostLogoutLifecycle(t *testing.T) {
	clock := staticClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &deterministicRandom{sequence: 50}

	mgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"https://app.goyimarena.com"},
		RequireOrigin:  true,
		Clock:          clock,
		Random:         rnd,
	})
	if err != nil {
		t.Fatalf("new security manager: %v", err)
	}

	// 1. Client logs out. Handler calls ClearOnLogout
	wLogout := httptest.NewRecorder()
	newCSRF, err := mgr.ClearOnLogout(wLogout)
	if err != nil {
		t.Fatalf("clear on logout: %v", err)
	}
	if newCSRF == "" {
		t.Fatal("expected a fresh CSRF token upon logout")
	}

	// Verify cookies on logout response
	logoutCookies := wLogout.Result().Cookies()
	var clearedSession, issuedNewCSRF bool
	for _, c := range logoutCookies {
		if c.Name == security.DefaultSessionCookieName && c.MaxAge == -1 {
			clearedSession = true
		}
		if c.Name == security.DefaultCSRFCookieName && c.Value == newCSRF {
			issuedNewCSRF = true
		}
	}
	if !clearedSession {
		t.Fatal("expected session cookie to be cleared (MaxAge=-1)")
	}
	if !issuedNewCSRF {
		t.Fatal("expected fresh CSRF cookie to be issued")
	}

	// 2. Subsequent request without session cookie to protected route fails with 401
	validator := &mockSessionValidator{
		validTokens: map[string]security.AuthIdentity{},
	}
	pipeline := mgr.AuthenticateMiddleware(validator)(
		mgr.RequireAuthMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	wReq := httptest.NewRecorder()
	pipeline.ServeHTTP(wReq, req)

	if wReq.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", wReq.Code)
	}
}

func TestManagerChaining_FullPipeline(t *testing.T) {
	clock := staticClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := &deterministicRandom{sequence: 1}

	mgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"https://app.goyimarena.com"},
		RequireOrigin:  true,
		Clock:          clock,
		Random:         rnd,
	})
	if err != nil {
		t.Fatalf("new security manager: %v", err)
	}

	sessionToken := "session_token_ok"
	validator := &mockSessionValidator{
		validTokens: map[string]security.AuthIdentity{
			sessionToken: {
				AccountID: "acc_42",
				SessionID: "sess_42",
			},
		},
	}

	csrfToken, err := mgr.IssueCSRFToken(httptest.NewRecorder())
	if err != nil {
		t.Fatalf("issue csrf: %v", err)
	}

	// Wire full pipeline: CSRF -> Authenticate -> RequireAuth -> Handler
	handler := mgr.CSRFMiddleware()(
		mgr.AuthenticateMiddleware(validator)(
			mgr.RequireAuthMiddleware()(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					id, ok := security.FromContext(r.Context())
					if !ok {
						t.Fatal("expected authenticated context")
					}
					if id.AccountID != "acc_42" {
						t.Fatalf("expected account acc_42, got %s", id.AccountID)
					}
					w.WriteHeader(http.StatusOK)
				}),
			),
		),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/arenas", nil)
	req.Header.Set("Origin", "https://app.goyimarena.com")
	req.Header.Set(security.DefaultCSRFHeaderName, csrfToken)
	req.AddCookie(&http.Cookie{Name: security.DefaultSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: security.DefaultCSRFCookieName, Value: csrfToken})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d (body: %s)", w.Code, w.Body.String())
	}
}
