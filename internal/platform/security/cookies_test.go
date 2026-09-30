package security_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

func TestCookieAttributesByEnvironment(t *testing.T) {
	tests := []struct {
		name           string
		env            config.Env
		expectedSecure bool
	}{
		{
			name:           "production enforces Secure=true",
			env:            config.EnvProduction,
			expectedSecure: true,
		},
		{
			name:           "development allows Secure=false",
			env:            config.EnvDevelopment,
			expectedSecure: false,
		},
		{
			name:           "test allows Secure=false",
			env:            config.EnvTest,
			expectedSecure: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := security.NewCookieConfig(tc.env)

			// 1. Session cookie verification
			sessionCookie := cfg.BuildSessionCookie("test-session-token", 24*time.Hour)
			if sessionCookie.Name != security.DefaultSessionCookieName {
				t.Fatalf("expected session cookie name %q, got %q", security.DefaultSessionCookieName, sessionCookie.Name)
			}
			if sessionCookie.Value != "test-session-token" {
				t.Fatalf("expected session cookie value 'test-session-token', got %q", sessionCookie.Value)
			}
			if sessionCookie.Path != "/" {
				t.Fatalf("expected session cookie path '/', got %q", sessionCookie.Path)
			}
			if !sessionCookie.HttpOnly {
				t.Fatal("session cookie must always be HttpOnly")
			}
			if sessionCookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("expected SameSite=Lax, got %v", sessionCookie.SameSite)
			}
			if sessionCookie.Secure != tc.expectedSecure {
				t.Fatalf("expected Secure=%v in %v, got %v", tc.expectedSecure, tc.env, sessionCookie.Secure)
			}
			if sessionCookie.MaxAge != int((24 * time.Hour).Seconds()) {
				t.Fatalf("expected MaxAge=%d, got %d", int((24 * time.Hour).Seconds()), sessionCookie.MaxAge)
			}

			// 2. CSRF cookie verification
			csrfCookie := cfg.BuildCSRFCookie("test-csrf-token", 24*time.Hour)
			if csrfCookie.Name != security.DefaultCSRFCookieName {
				t.Fatalf("expected csrf cookie name %q, got %q", security.DefaultCSRFCookieName, csrfCookie.Name)
			}
			if csrfCookie.Value != "test-csrf-token" {
				t.Fatalf("expected csrf cookie value 'test-csrf-token', got %q", csrfCookie.Value)
			}
			if csrfCookie.Path != "/" {
				t.Fatalf("expected csrf cookie path '/', got %q", csrfCookie.Path)
			}
			if csrfCookie.HttpOnly {
				t.Fatal("csrf cookie must NOT be HttpOnly so browser scripts can read and submit in X-CSRF-Token")
			}
			if csrfCookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("expected SameSite=Lax, got %v", csrfCookie.SameSite)
			}
			if csrfCookie.Secure != tc.expectedSecure {
				t.Fatalf("expected Secure=%v in %v, got %v", tc.expectedSecure, tc.env, csrfCookie.Secure)
			}

			// 3. Clear cookie verification
			clearSession := cfg.BuildClearSessionCookie()
			if clearSession.MaxAge != -1 {
				t.Fatalf("expected clear session cookie MaxAge=-1, got %d", clearSession.MaxAge)
			}
			if clearSession.Value != "" {
				t.Fatalf("expected empty value for clear session cookie, got %q", clearSession.Value)
			}

			clearCSRF := cfg.BuildClearCSRFCookie()
			if clearCSRF.MaxAge != -1 {
				t.Fatalf("expected clear csrf cookie MaxAge=-1, got %d", clearCSRF.MaxAge)
			}
		})
	}
}

func TestCookieReadingAndWriting(t *testing.T) {
	cfg := security.NewCookieConfig(config.EnvProduction)

	w := httptest.NewRecorder()
	cfg.SetSessionCookie(w, "active-session", 1*time.Hour)
	cfg.SetCSRFCookie(w, "active-csrf", 1*time.Hour)

	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected 2 cookies set, got %d", len(cookies))
	}

	rawSetCookie := w.Header().Values("Set-Cookie")
	var hasSessionSecure, hasCSRFNotHttpOnly bool
	for _, sc := range rawSetCookie {
		if strings.Contains(sc, "arena_session=") {
			if strings.Contains(sc, "HttpOnly") && strings.Contains(sc, "Secure") && strings.Contains(sc, "SameSite=Lax") {
				hasSessionSecure = true
			}
		}
		if strings.Contains(sc, "arena_csrf=") {
			if !strings.Contains(sc, "HttpOnly") && strings.Contains(sc, "Secure") && strings.Contains(sc, "SameSite=Lax") {
				hasCSRFNotHttpOnly = true
			}
		}
	}

	if !hasSessionSecure {
		t.Fatal("Set-Cookie for session does not have HttpOnly, Secure, SameSite=Lax")
	}
	if !hasCSRFNotHttpOnly {
		t.Fatal("Set-Cookie for csrf must not have HttpOnly, but must have Secure, SameSite=Lax")
	}

	// Read cookies from incoming request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.AddCookie(&http.Cookie{Name: security.DefaultSessionCookieName, Value: "extracted-session"})
	req.AddCookie(&http.Cookie{Name: security.DefaultCSRFCookieName, Value: "extracted-csrf"})

	sessionTok, err := cfg.GetSessionToken(req)
	if err != nil || sessionTok != "extracted-session" {
		t.Fatalf("expected 'extracted-session', got %q (err: %v)", sessionTok, err)
	}

	csrfTok, err := cfg.GetCSRFToken(req)
	if err != nil || csrfTok != "extracted-csrf" {
		t.Fatalf("expected 'extracted-csrf', got %q (err: %v)", csrfTok, err)
	}

	// Missing cookies return typed errors
	emptyReq := httptest.NewRequest(http.MethodGet, "/test", nil)
	if _, err := cfg.GetSessionToken(emptyReq); err != security.ErrNoSessionCookie {
		t.Fatalf("expected ErrNoSessionCookie, got %v", err)
	}
	if _, err := cfg.GetCSRFToken(emptyReq); err != security.ErrNoCSRFCookie {
		t.Fatalf("expected ErrNoCSRFCookie, got %v", err)
	}
}
