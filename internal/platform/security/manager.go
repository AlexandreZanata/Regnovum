package security

import (
	"errors"
	"net/http"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Options specifies configuration for constructing the unified security Manager.
type Options struct {
	Env               config.Env
	CSRFSecret        []byte
	AllowedOrigins    []string
	RequireOrigin     bool
	Clock             ports.Clock
	Random            ports.Random
	SessionCookieName string
	CSRFCookieName    string
	CookieDomain      string
	CookiePath        string
	CookieSameSite    http.SameSite
}

// Manager aggregates cookie policy, authentication context and CSRF protection into
// a coherent security boundary.
type Manager struct {
	cookies CookieConfig
	csrf    *CSRFTokenManager
	clock   ports.Clock
	random  ports.Random
}

// New constructs and validates a new security Manager.
func New(opts Options) (*Manager, error) {
	if opts.Random == nil {
		return nil, errors.New("security: random source is required")
	}

	secret := opts.CSRFSecret
	if len(secret) == 0 {
		// Generate an ephemeral 32-byte secret when none is explicitly configured.
		generated := make([]byte, 32)
		if _, err := opts.Random.Read(generated); err != nil {
			return nil, err
		}
		secret = generated
	} else if len(secret) < 32 {
		return nil, ErrInvalidCSRFSecret
	}

	cookieCfg := CookieConfig{
		Env:               opts.Env,
		SessionCookieName: opts.SessionCookieName,
		CSRFCookieName:    opts.CSRFCookieName,
		Path:              opts.CookiePath,
		Domain:            opts.CookieDomain,
		SameSite:          opts.CookieSameSite,
	}

	csrfMgr, err := NewCSRFTokenManager(CSRFConfig{
		Secret:         secret,
		AllowedOrigins: opts.AllowedOrigins,
		RequireOrigin:  opts.RequireOrigin,
		Cookies:        cookieCfg,
		Random:         opts.Random,
		Clock:          opts.Clock,
	})
	if err != nil {
		return nil, err
	}

	return &Manager{
		cookies: cookieCfg,
		csrf:    csrfMgr,
		clock:   opts.Clock,
		random:  opts.Random,
	}, nil
}

// Cookies returns the underlying CookieConfig.
func (m *Manager) Cookies() CookieConfig {
	return m.cookies
}

// CSRF returns the underlying CSRFTokenManager.
func (m *Manager) CSRF() *CSRFTokenManager {
	return m.csrf
}

// AuthenticateMiddleware returns middleware that resolves and verifies the session token,
// attaching the resulting identity to the request context.
func (m *Manager) AuthenticateMiddleware(validator SessionValidator) func(http.Handler) http.Handler {
	return Authenticate(validator, m.cookies)
}

// RequireAuthMiddleware returns middleware that enforces an active authenticated identity in the context.
func (m *Manager) RequireAuthMiddleware() func(http.Handler) http.Handler {
	return RequireAuth()
}

// CSRFMiddleware returns middleware that validates CSRF tokens on mutating requests.
func (m *Manager) CSRFMiddleware() func(http.Handler) http.Handler {
	return m.csrf.Middleware()
}

// IssueCSRFToken generates a fresh signed CSRF token, writes the CSRF cookie to the response,
// and returns the token string.
func (m *Manager) IssueCSRFToken(w http.ResponseWriter) (string, error) {
	token, err := m.csrf.GenerateToken()
	if err != nil {
		return "", err
	}
	m.cookies.SetCSRFCookie(w, token, 0)
	return token, nil
}

// RotateOnLogin performs post-login credential rotation across the HTTP boundary:
//  1. Emits a new session cookie carrying the newly authenticated opaque session token.
//  2. Rotates the CSRF token and issues a fresh CSRF cookie, eliminating CSRF fixation
//     and binding the new session to an uncompromised token.
func (m *Manager) RotateOnLogin(w http.ResponseWriter, sessionToken string, maxAge time.Duration) (string, error) {
	m.cookies.SetSessionCookie(w, sessionToken, maxAge)
	newCSRFToken, err := m.csrf.GenerateToken()
	if err != nil {
		return "", err
	}
	m.cookies.SetCSRFCookie(w, newCSRFToken, 0)
	return newCSRFToken, nil
}

// ClearOnLogout performs clean logout across the HTTP boundary:
//  1. Clears the session cookie in the client by setting MaxAge=-1.
//  2. Rotates the CSRF cookie to prevent post-logout CSRF token reuse.
func (m *Manager) ClearOnLogout(w http.ResponseWriter) (string, error) {
	m.cookies.ClearSessionCookie(w)
	newCSRFToken, err := m.csrf.GenerateToken()
	if err != nil {
		return "", err
	}
	m.cookies.SetCSRFCookie(w, newCSRFToken, 0)
	return newCSRFToken, nil
}
