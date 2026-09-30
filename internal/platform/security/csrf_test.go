package security_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

type deterministicRandom struct {
	sequence byte
}

func (d *deterministicRandom) Read(buf []byte) (int, error) {
	for i := range buf {
		buf[i] = d.sequence
		d.sequence++
	}
	return len(buf), nil
}

func newTestCSRFManager(t *testing.T, allowedOrigins []string, requireOrigin bool) *security.CSRFTokenManager {
	t.Helper()
	secret := bytes.Repeat([]byte("s"), 32)
	mgr, err := security.NewCSRFTokenManager(security.CSRFConfig{
		Secret:         secret,
		AllowedOrigins: allowedOrigins,
		RequireOrigin:  requireOrigin,
		Cookies:        security.NewCookieConfig(config.EnvTest),
		Random:         &deterministicRandom{sequence: 1},
	})
	if err != nil {
		t.Fatalf("new csrf manager: %v", err)
	}
	return mgr
}

func TestCSRFMiddleware_SafeMethodsNeverMutateState(t *testing.T) {
	mgr := newTestCSRFManager(t, []string{"http://localhost:8080"}, true)

	safeMethods := []string{http.MethodGet, http.MethodHead, http.MethodOptions, "TRACE"}

	for _, method := range safeMethods {
		t.Run(method, func(t *testing.T) {
			innerReached := false
			target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				innerReached = true
				w.WriteHeader(http.StatusOK)
			})

			handler := mgr.Middleware()(target)

			// Request with no cookies and no headers
			req := httptest.NewRequest(method, "/api/v1/public-feed", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected status 200 for safe method %s, got %d", method, w.Code)
			}
			if !innerReached {
				t.Fatalf("inner handler must be reached for safe method %s", method)
			}

			// Safe requests must issue an arena_csrf cookie if missing so subsequent mutations have one
			cookies := w.Result().Cookies()
			var issuedCSRF bool
			for _, c := range cookies {
				if c.Name == security.DefaultCSRFCookieName && c.Value != "" {
					issuedCSRF = true
				}
			}
			if !issuedCSRF {
				t.Fatalf("expected csrf cookie to be issued on safe method %s", method)
			}
		})
	}
}

func TestCSRFMiddleware_MutatingMethods_Enforcement(t *testing.T) {
	mgr := newTestCSRFManager(t, []string{"https://app.goyimarena.com"}, true)

	token, err := mgr.GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	mutatingMethods := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

	for _, method := range mutatingMethods {
		t.Run(method+"_Success", func(t *testing.T) {
			target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
			})
			handler := mgr.Middleware()(target)

			req := httptest.NewRequest(method, "/api/v1/action", nil)
			req.Header.Set("Origin", "https://app.goyimarena.com")
			req.Header.Set(security.DefaultCSRFHeaderName, token)
			req.AddCookie(&http.Cookie{
				Name:  security.DefaultCSRFCookieName,
				Value: token,
			})
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("expected 201 Created, got %d (body: %s)", w.Code, w.Body.String())
			}
		})

		t.Run(method+"_MissingCookie", func(t *testing.T) {
			target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("target should not be reached")
			})
			handler := mgr.Middleware()(target)

			req := httptest.NewRequest(method, "/api/v1/action", nil)
			req.Header.Set("Origin", "https://app.goyimarena.com")
			req.Header.Set(security.DefaultCSRFHeaderName, token)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403 Forbidden, got %d", w.Code)
			}
			assertProblemCode(t, w, "csrf_token_missing")
		})

		t.Run(method+"_MissingHeader", func(t *testing.T) {
			target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("target should not be reached")
			})
			handler := mgr.Middleware()(target)

			req := httptest.NewRequest(method, "/api/v1/action", nil)
			req.Header.Set("Origin", "https://app.goyimarena.com")
			req.AddCookie(&http.Cookie{
				Name:  security.DefaultCSRFCookieName,
				Value: token,
			})
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403 Forbidden, got %d", w.Code)
			}
			assertProblemCode(t, w, "csrf_token_missing")
		})

		t.Run(method+"_TamperedCookieSignature", func(t *testing.T) {
			target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("target should not be reached")
			})
			handler := mgr.Middleware()(target)

			// Tamper with signature segment
			parts := strings.Split(token, ".")
			tamperedToken := parts[0] + ".tampered_invalid_signature"

			req := httptest.NewRequest(method, "/api/v1/action", nil)
			req.Header.Set("Origin", "https://app.goyimarena.com")
			req.Header.Set(security.DefaultCSRFHeaderName, tamperedToken)
			req.AddCookie(&http.Cookie{
				Name:  security.DefaultCSRFCookieName,
				Value: tamperedToken,
			})
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403 Forbidden, got %d", w.Code)
			}
			assertProblemCode(t, w, "csrf_token_invalid")
		})

		t.Run(method+"_TokenMismatch", func(t *testing.T) {
			target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("target should not be reached")
			})
			handler := mgr.Middleware()(target)

			otherToken, _ := mgr.GenerateToken()

			req := httptest.NewRequest(method, "/api/v1/action", nil)
			req.Header.Set("Origin", "https://app.goyimarena.com")
			req.Header.Set(security.DefaultCSRFHeaderName, token) // Header has token A
			req.AddCookie(&http.Cookie{
				Name:  security.DefaultCSRFCookieName,
				Value: otherToken, // Cookie has token B
			})
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403 Forbidden, got %d", w.Code)
			}
			assertProblemCode(t, w, "csrf_token_mismatch")
		})
	}
}

func TestCSRFMiddleware_CrossOriginValidation(t *testing.T) {
	mgr := newTestCSRFManager(t, []string{"https://app.goyimarena.com"}, true)
	token, _ := mgr.GenerateToken()

	tests := []struct {
		name         string
		origin       string
		referer      string
		expectedCode int
		expectedErr  string
	}{
		{
			name:         "allowed Origin matches configured allowlist",
			origin:       "https://app.goyimarena.com",
			expectedCode: http.StatusOK,
		},
		{
			name:         "allowed Referer matches configured allowlist when Origin absent",
			referer:      "https://app.goyimarena.com/arenas/123",
			expectedCode: http.StatusOK,
		},
		{
			name:         "cross-origin attacker site rejected",
			origin:       "https://evil-attacker.com",
			expectedCode: http.StatusForbidden,
			expectedErr:  "csrf_origin_mismatch",
		},
		{
			name:         "cross-origin referer rejected",
			referer:      "https://evil-attacker.com/steal",
			expectedCode: http.StatusForbidden,
			expectedErr:  "csrf_origin_mismatch",
		},
		{
			name:         "missing both Origin and Referer rejected under RequireOrigin",
			expectedCode: http.StatusForbidden,
			expectedErr:  "csrf_origin_missing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			handler := mgr.Middleware()(target)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/arenas", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.referer != "" {
				req.Header.Set("Referer", tc.referer)
			}
			req.Header.Set(security.DefaultCSRFHeaderName, token)
			req.AddCookie(&http.Cookie{
				Name:  security.DefaultCSRFCookieName,
				Value: token,
			})

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tc.expectedCode {
				t.Fatalf("expected code %d, got %d", tc.expectedCode, w.Code)
			}
			if tc.expectedErr != "" {
				assertProblemCode(t, w, tc.expectedErr)
			}
		})
	}
}

// TestCSRFMiddleware_SameOriginSubmissionCarriesItsOrigin is the pair the
// browser policy rests on (P18-T07D): a submission from the host that served
// the page is accepted because it carries that host as its origin, and a
// submission whose origin was nullified — which is what a referrer policy of
// "no-referrer" produces for every form of the product — is refused. The
// second half is the reason the referrer policy has to keep the origin; the
// first is what makes the journey possible at all.
func TestCSRFMiddleware_SameOriginSubmissionCarriesItsOrigin(t *testing.T) {
	mgr := newTestCSRFManager(t, nil, false)
	token, err := mgr.GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	tests := []struct {
		name        string
		origin      string
		expected    int
		expectedErr string
	}{
		{
			name:     "the origin of the host that serves the page",
			origin:   "https://arena.example",
			expected: http.StatusNoContent,
		},
		{
			name:        "an origin nullified by the referrer policy",
			origin:      "null",
			expected:    http.StatusForbidden,
			expectedErr: "csrf_origin_mismatch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			handler := mgr.Middleware()(target)

			req := httptest.NewRequest(http.MethodPost, "https://arena.example/arenas/tema", nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set(security.DefaultCSRFHeaderName, token)
			req.AddCookie(&http.Cookie{
				Name:  security.DefaultCSRFCookieName,
				Value: token,
			})

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tc.expected {
				t.Fatalf("POST with Origin %q: status = %d, want %d (body: %s)", tc.origin, w.Code, tc.expected, w.Body.String())
			}
			if tc.expectedErr != "" {
				assertProblemCode(t, w, tc.expectedErr)
			}
		})
	}
}

func assertProblemCode(t *testing.T, w *httptest.ResponseRecorder, expectedCode string) {
	t.Helper()
	var problem struct {
		Code   string `json:"code"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem json: %v (body: %s)", err, w.Body.String())
	}
	if problem.Code != expectedCode {
		t.Fatalf("expected problem code %q, got %q", expectedCode, problem.Code)
	}
}
