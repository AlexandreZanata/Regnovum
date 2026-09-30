package security

import (
	"context"
	"errors"
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
)

// authContextKey is the unexported private type for context values to prevent collisions.
type authContextKey struct{}

var activeAuthContextKey = authContextKey{}

// AuthIdentity holds the authenticated identity values stored in request context.
type AuthIdentity struct {
	AccountID string
	SessionID string
}

// IsZero reports whether the identity carries no authenticated account.
func (id AuthIdentity) IsZero() bool {
	return id.AccountID == ""
}

// WithAuth attaches an authenticated identity to the context.
func WithAuth(ctx context.Context, identity AuthIdentity) context.Context {
	return context.WithValue(ctx, activeAuthContextKey, identity)
}

// FromContext extracts the authenticated identity from the context, if present.
func FromContext(ctx context.Context) (AuthIdentity, bool) {
	if ctx == nil {
		return AuthIdentity{}, false
	}
	identity, ok := ctx.Value(activeAuthContextKey).(AuthIdentity)
	if !ok || identity.IsZero() {
		return AuthIdentity{}, false
	}
	return identity, true
}

// SessionValidator validates a raw opaque session token against the underlying session store
// and returns the resolved authenticated identity.
type SessionValidator interface {
	ValidateSession(ctx context.Context, rawToken string) (AuthIdentity, error)
}

// SessionValidatorFunc adapts an ordinary function to the SessionValidator interface.
type SessionValidatorFunc func(ctx context.Context, rawToken string) (AuthIdentity, error)

// ValidateSession implements SessionValidator.
func (f SessionValidatorFunc) ValidateSession(ctx context.Context, rawToken string) (AuthIdentity, error) {
	return f(ctx, rawToken)
}

// Authenticate returns middleware that inspects the session cookie, resolves and validates
// the session via SessionValidator, and attaches the resulting AuthIdentity to the request context.
// If no session cookie is present, the request proceeds unauthenticated.
// If a session cookie is present but invalid/expired/revoked, the middleware responds with
// a 401 Unauthorized RFC 9457 Problem Details document and clears the invalid cookie.
func Authenticate(validator SessionValidator, cookies CookieConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := cookies.GetSessionToken(r)
			if err != nil {
				// No session cookie present; continue unauthenticated.
				next.ServeHTTP(w, r)
				return
			}

			if validator == nil {
				cookies.ClearSessionCookie(w)
				_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "invalid_session", "session validation unavailable"))
				return
			}

			identity, err := validator.ValidateSession(r.Context(), token)
			if err != nil || identity.IsZero() {
				// Clear the invalid or expired cookie so the client does not keep sending it.
				cookies.ClearSessionCookie(w)
				var appErr *apperr.Error
				if errors.As(err, &appErr) && appErr.Kind() == apperr.KindUnauthorized {
					_ = httperror.WriteProblem(w, r, appErr)
				} else {
					_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "invalid_session", "session is invalid, expired, or revoked").WithCause(err))
				}
				return
			}

			// Valid session: attach identity to request context.
			ctx := WithAuth(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAuth returns middleware that enforces an authenticated context.
// Requests lacking an authenticated AuthIdentity fail immediately with 401 Unauthorized Problem Details.
func RequireAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := FromContext(r.Context())
			if !ok || identity.IsZero() {
				_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
