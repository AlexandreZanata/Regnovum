// Package security provides HTTP authentication context, session cookie
// lifecycle management and robust CSRF protection for Regnovum (P04-T07).
//
// Invariants enforced by this package:
//   - Cookie attributes adapt strictly to the deployment environment:
//     production enforces Secure=true; development/test allow plain HTTP;
//   - Session cookies (DefaultSessionCookieName = "arena_session") are always
//     HttpOnly, SameSite=Lax and Path=/;
//   - CSRF cookies (DefaultCSRFCookieName = "arena_csrf") are SameSite=Lax,
//     Path=/, with HttpOnly=false so native client JavaScript can extract the
//     signed token and supply it via the X-CSRF-Token header;
//   - GET, HEAD, OPTIONS and TRACE never mutate state and never fail on CSRF;
//   - State-changing requests (POST, PUT, PATCH, DELETE) require strict Origin/Referer
//     verification and constant-time HMAC-signed CSRF token validation;
//   - Session rotation on login automatically rotates the CSRF token to prevent
//     both session and CSRF fixation attacks across authentication boundaries.
package security

import (
	"errors"
	"net/http"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
)

const (
	// DefaultSessionCookieName matches the OpenAPI securityScheme declared in api/openapi.json.
	DefaultSessionCookieName = "arena_session"

	// DefaultCSRFCookieName is the cookie carrying the double-submit HMAC token.
	DefaultCSRFCookieName = "arena_csrf"

	// DefaultCSRFHeaderName matches the OpenAPI CsrfHeader declared in api/openapi.json.
	DefaultCSRFHeaderName = "X-CSRF-Token"

	// DefaultCookiePath is the canonical cookie root path.
	DefaultCookiePath = "/"
)

var (
	// ErrNoSessionCookie indicates the session cookie is missing from the request.
	ErrNoSessionCookie = errors.New("security: session cookie missing")

	// ErrNoCSRFCookie indicates the CSRF cookie is missing from the request.
	ErrNoCSRFCookie = errors.New("security: csrf cookie missing")
)

// CookieConfig manages cookie formatting and environment-specific security flags.
type CookieConfig struct {
	Env               config.Env
	SessionCookieName string
	CSRFCookieName    string
	Path              string
	Domain            string
	SameSite          http.SameSite
}

// NewCookieConfig constructs a CookieConfig with defaults for any omitted fields.
func NewCookieConfig(env config.Env) CookieConfig {
	return CookieConfig{
		Env:               env,
		SessionCookieName: DefaultSessionCookieName,
		CSRFCookieName:    DefaultCSRFCookieName,
		Path:              DefaultCookiePath,
		SameSite:          http.SameSiteLaxMode,
	}
}

// isSecure returns true if the environment mandates the Secure attribute.
func (c CookieConfig) isSecure() bool {
	return c.Env == config.EnvProduction
}

// sessionName returns the configured session cookie name.
func (c CookieConfig) sessionName() string {
	if c.SessionCookieName != "" {
		return c.SessionCookieName
	}
	return DefaultSessionCookieName
}

// csrfName returns the configured CSRF cookie name.
func (c CookieConfig) csrfName() string {
	if c.CSRFCookieName != "" {
		return c.CSRFCookieName
	}
	return DefaultCSRFCookieName
}

// path returns the configured cookie path.
func (c CookieConfig) path() string {
	if c.Path != "" {
		return c.Path
	}
	return DefaultCookiePath
}

// sameSite returns the configured SameSite mode.
func (c CookieConfig) sameSite() http.SameSite {
	if c.SameSite != 0 {
		return c.SameSite
	}
	return http.SameSiteLaxMode
}

// BuildSessionCookie constructs an http.Cookie for an opaque session token.
func (c CookieConfig) BuildSessionCookie(token string, maxAge time.Duration) *http.Cookie {
	var maxAgeSeconds int
	if maxAge > 0 {
		maxAgeSeconds = int(maxAge.Seconds())
	}

	return &http.Cookie{
		Name:     c.sessionName(),
		Value:    token,
		Path:     c.path(),
		Domain:   c.Domain,
		MaxAge:   maxAgeSeconds,
		HttpOnly: true,
		Secure:   c.isSecure(),
		SameSite: c.sameSite(),
	}
}

// BuildClearSessionCookie constructs an http.Cookie that invalidates the session cookie in the client.
func (c CookieConfig) BuildClearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     c.sessionName(),
		Value:    "",
		Path:     c.path(),
		Domain:   c.Domain,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   c.isSecure(),
		SameSite: c.sameSite(),
	}
}

// BuildCSRFCookie constructs an http.Cookie for a signed CSRF token.
// HttpOnly is deliberately false so native browser scripts can read the token
// to populate the X-CSRF-Token header on mutating requests.
func (c CookieConfig) BuildCSRFCookie(token string, maxAge time.Duration) *http.Cookie {
	var maxAgeSeconds int
	if maxAge > 0 {
		maxAgeSeconds = int(maxAge.Seconds())
	}

	return &http.Cookie{
		Name:     c.csrfName(),
		Value:    token,
		Path:     c.path(),
		Domain:   c.Domain,
		MaxAge:   maxAgeSeconds,
		HttpOnly: false,
		Secure:   c.isSecure(),
		SameSite: c.sameSite(),
	}
}

// BuildClearCSRFCookie constructs an http.Cookie that clears the CSRF token in the client.
func (c CookieConfig) BuildClearCSRFCookie() *http.Cookie {
	return &http.Cookie{
		Name:     c.csrfName(),
		Value:    "",
		Path:     c.path(),
		Domain:   c.Domain,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: false,
		Secure:   c.isSecure(),
		SameSite: c.sameSite(),
	}
}

// SetSessionCookie writes the session cookie to the response writer.
func (c CookieConfig) SetSessionCookie(w http.ResponseWriter, token string, maxAge time.Duration) {
	http.SetCookie(w, c.BuildSessionCookie(token, maxAge))
}

// ClearSessionCookie writes an expired session cookie to clear the client's cookie.
func (c CookieConfig) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, c.BuildClearSessionCookie())
}

// SetCSRFCookie writes the CSRF cookie to the response writer.
func (c CookieConfig) SetCSRFCookie(w http.ResponseWriter, token string, maxAge time.Duration) {
	http.SetCookie(w, c.BuildCSRFCookie(token, maxAge))
}

// ClearCSRFCookie writes an expired CSRF cookie to clear the client's cookie.
func (c CookieConfig) ClearCSRFCookie(w http.ResponseWriter) {
	http.SetCookie(w, c.BuildClearCSRFCookie())
}

// GetSessionToken extracts the raw session token from the request's cookies.
func (c CookieConfig) GetSessionToken(r *http.Request) (string, error) {
	cookie, err := r.Cookie(c.sessionName())
	if err != nil {
		return "", ErrNoSessionCookie
	}
	if cookie.Value == "" {
		return "", ErrNoSessionCookie
	}
	return cookie.Value, nil
}

// GetCSRFToken extracts the raw signed CSRF token from the request's cookies.
func (c CookieConfig) GetCSRFToken(r *http.Request) (string, error) {
	cookie, err := r.Cookie(c.csrfName())
	if err != nil {
		return "", ErrNoCSRFCookie
	}
	if cookie.Value == "" {
		return "", ErrNoCSRFCookie
	}
	return cookie.Value, nil
}
