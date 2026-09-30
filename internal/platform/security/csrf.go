package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

var (
	// ErrInvalidCSRFSecret indicates the HMAC secret is empty or too short.
	ErrInvalidCSRFSecret = errors.New("security: csrf secret must be at least 32 bytes")
)

// CSRFConfig configures robust double-submit CSRF protection.
type CSRFConfig struct {
	Secret         []byte
	AllowedOrigins []string
	RequireOrigin  bool
	Cookies        CookieConfig
	Random         ports.Random
	Clock          ports.Clock
}

// CSRFTokenManager handles cryptographically signed CSRF tokens.
type CSRFTokenManager struct {
	secret         []byte
	allowedOrigins map[string]bool
	requireOrigin  bool
	cookies        CookieConfig
	random         ports.Random
}

// NewCSRFTokenManager constructs a token manager after validating parameters.
func NewCSRFTokenManager(cfg CSRFConfig) (*CSRFTokenManager, error) {
	if len(cfg.Secret) < 32 {
		return nil, ErrInvalidCSRFSecret
	}
	if cfg.Random == nil {
		return nil, errors.New("security: random source is required")
	}

	origins := make(map[string]bool, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		normalized := normalizeOrigin(o)
		if normalized != "" {
			origins[normalized] = true
		}
	}

	return &CSRFTokenManager{
		secret:         cfg.Secret,
		allowedOrigins: origins,
		requireOrigin:  cfg.RequireOrigin,
		cookies:        cfg.Cookies,
		random:         cfg.randomOrDefault(),
	}, nil
}

func (c CSRFConfig) randomOrDefault() ports.Random {
	return c.Random
}

// GenerateToken produces a URL-safe random token paired with an HMAC-SHA256 signature:
// <rawToken>.<signature>
func (m *CSRFTokenManager) GenerateToken() (string, error) {
	rawBytes := make([]byte, 32)
	if _, err := m.random.Read(rawBytes); err != nil {
		return "", err
	}
	rawToken := base64.RawURLEncoding.EncodeToString(rawBytes)
	sig := m.computeSignature(rawToken)
	sigString := base64.RawURLEncoding.EncodeToString(sig)
	return rawToken + "." + sigString, nil
}

// VerifyToken validates that the token was signed with the manager's secret key.
func (m *CSRFTokenManager) VerifyToken(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	rawToken := parts[0]
	sigString := parts[1]

	providedSig, err := base64.RawURLEncoding.DecodeString(sigString)
	if err != nil {
		return false
	}

	expectedSig := m.computeSignature(rawToken)
	return subtle.ConstantTimeCompare(providedSig, expectedSig) == 1
}

func (m *CSRFTokenManager) computeSignature(rawToken string) []byte {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(rawToken))
	return mac.Sum(nil)
}

// isSafeMethod checks if the request method is defined as safe per RFC 7231 / RFC 9110.
// Safe methods MUST NEVER mutate state and are exempt from CSRF token validation.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, "TRACE":
		return true
	default:
		return false
	}
}

// normalizeOrigin normalizes a scheme://host string to lowercase without trailing slashes.
func normalizeOrigin(origin string) string {
	origin = strings.TrimSpace(strings.ToLower(origin))
	origin = strings.TrimSuffix(origin, "/")
	return origin
}

// validateOriginOrReferer verifies that the request origin or referer matches
// configured allowed origins or the request's own Host header.
//
// An origin that is present and carries no host — "null", the value the HTML
// standard produces for a form submission under a referrer policy of
// "no-referrer" — is refused here like any other mismatch. That is deliberate
// and it is why the product's referrer policy has to be one that keeps the
// origin (P18-T07D): tolerating "null" would leave the double submit as the
// only browser protection and contradict the strict-origin control declared
// in docs/THREAT_MODEL.md (THR-AUTH-03).
func (m *CSRFTokenManager) validateOriginOrReferer(r *http.Request) error {
	originHeader := r.Header.Get("Origin")
	if originHeader != "" {
		parsed, err := url.Parse(originHeader)
		if err != nil || parsed.Host == "" {
			return apperr.New(apperr.KindForbidden, "csrf_origin_mismatch", "invalid origin header format")
		}
		target := normalizeOrigin(parsed.Scheme + "://" + parsed.Host)
		if m.allowedOrigins[target] || parsed.Host == r.Host {
			return nil
		}
		return apperr.New(apperr.KindForbidden, "csrf_origin_mismatch", "request origin is not allowed")
	}

	// Fallback to Referer header when Origin is absent.
	refererHeader := r.Header.Get("Referer")
	if refererHeader != "" {
		parsed, err := url.Parse(refererHeader)
		if err != nil || parsed.Host == "" {
			return apperr.New(apperr.KindForbidden, "csrf_origin_mismatch", "invalid referer header format")
		}
		target := normalizeOrigin(parsed.Scheme + "://" + parsed.Host)
		if m.allowedOrigins[target] || parsed.Host == r.Host {
			return nil
		}
		return apperr.New(apperr.KindForbidden, "csrf_origin_mismatch", "request referer is not allowed")
	}

	// Neither Origin nor Referer provided.
	if m.requireOrigin {
		return apperr.New(apperr.KindForbidden, "csrf_origin_missing", "mutating request requires an origin or referer header")
	}
	return nil
}

// Middleware returns HTTP middleware that enforces CSRF validation for mutating requests
// and ensures safe requests receive a valid CSRF cookie.
func (m *CSRFTokenManager) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Safe methods: never mutate state, exempt from CSRF token validation.
			if isSafeMethod(r.Method) {
				// If client lacks a CSRF cookie, issue one automatically on safe reads.
				if _, err := m.cookies.GetCSRFToken(r); err != nil {
					if token, err := m.GenerateToken(); err == nil {
						m.cookies.SetCSRFCookie(w, token, 0)
					}
				}
				next.ServeHTTP(w, r)
				return
			}

			// 1. Validate Origin / Referer against cross-origin attacks.
			if err := m.validateOriginOrReferer(r); err != nil {
				var appErr *apperr.Error
				if errors.As(err, &appErr) {
					_ = httperror.WriteProblem(w, r, appErr)
				} else {
					_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "csrf_origin_mismatch", "origin validation failed"))
				}
				return
			}

			// 2. Extract and verify the CSRF cookie token.
			cookieToken, err := m.cookies.GetCSRFToken(r)
			if err != nil {
				_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "csrf_token_missing", "csrf cookie is missing"))
				return
			}

			if !m.VerifyToken(cookieToken) {
				_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "csrf_token_invalid", "csrf cookie signature is invalid or tampered"))
				return
			}

			// 3. Extract the CSRF header (X-CSRF-Token).
			headerToken := r.Header.Get(DefaultCSRFHeaderName)
			if headerToken == "" {
				// Also check multipart/form-data or form-urlencoded field as fallback.
				headerToken = r.PostFormValue("csrf_token")
			}
			if headerToken == "" {
				_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "csrf_token_missing", "csrf header X-CSRF-Token is missing"))
				return
			}

			// 4. Double-submit constant-time match verification.
			if subtle.ConstantTimeCompare([]byte(headerToken), []byte(cookieToken)) != 1 {
				_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "csrf_token_mismatch", "csrf token header does not match cookie"))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
