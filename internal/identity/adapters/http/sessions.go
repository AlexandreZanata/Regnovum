package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// maxSessionBodyBytes bounds the revocation body: it is a session identifier
// and a password, and nothing else fits.
const maxSessionBodyBytes = 4 << 10

// rfc3339 is the instant shape the API states (I18N_STANDARD section 5).
const rfc3339 = time.RFC3339

// RotateSession handles POST /api/v1/me/sessions/rotation.
//
// It ends the calling session and issues a fresh one, which is the primitive a
// privilege change or a suspicion of theft needs: the token the browser holds
// stops working the moment the new one exists, so a copy taken before the
// rotation is worthless after it. The raw token is read from the request cookie
// the authentication middleware already validated — it is not carried in the
// request context, because a credential that travels further than the layer
// that consumed it is a credential that can be logged by the layer that did
// not need it.
func (h *Handler) RotateSession(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok || identity.AccountID == "" {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.rotateSession == nil || h.security == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "session rotation unavailable"))
		return
	}

	current, err := h.security.Cookies().GetSessionToken(r)
	if err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required").WithCause(err))
		return
	}

	result, err := h.rotateSession.Execute(r.Context(), application.RotateSessionCommand{
		CurrentRawToken: current,
		IPAddress:       r.RemoteAddr,
		UserAgent:       r.UserAgent(),
	})
	if err != nil {
		writeSessionProblem(w, r, err)
		return
	}

	// The browser credential is replaced in the same response that ends the
	// old session: the client never holds a session the server has already
	// revoked, which is what makes the rotation atomic from its point of view.
	if _, err := h.security.RotateOnLogin(w, result.RawToken, sessionDuration); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "session rotation could not be completed").WithCause(err))
		return
	}

	writeJSONBody(w, http.StatusOK, map[string]string{
		"status":     "rotated",
		"session_id": result.Session.ID().String(),
	})
}

// sessionBody is the JSON shape of the revocation request. The password is
// read from the body and never from a header or a query string: a credential
// does not belong in a URL, which is logged, and the transport is HTTPS.
type sessionBody struct {
	SessionID string `json:"session_id"`
	Password  string `json:"password"`
}

// sessionJSON is one entry of the session list.
//
// The instant fields are RFC 3339 in UTC (I18N_STANDARD section 5): the
// presentation layer decides how a date reads, and the API states the instant.
// The user agent travels as recorded, bounded by the use case — the list shows
// what the session presented, not a device profile derived from it.
type sessionJSON struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
	IPAddress  string `json:"ip_address,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
	Current    bool   `json:"current"`
}

// ListSessions handles GET /api/v1/me/sessions.
//
// The account is taken from the authenticated context, so the route cannot be
// asked about another account: the only list it can return is the caller's.
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok || identity.AccountID == "" {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.listSessions == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "session listing unavailable"))
		return
	}

	summaries, err := h.listSessions.Execute(r.Context(), application.ListSessionsCommand{
		AccountID:        domain.AccountID(identity.AccountID),
		CurrentSessionID: domain.SessionID(identity.SessionID),
	})
	if err != nil {
		writeSessionProblem(w, r, err)
		return
	}

	body := make([]sessionJSON, 0, len(summaries))
	for _, summary := range summaries {
		body = append(body, sessionJSON{
			ID:         summary.ID.String(),
			CreatedAt:  summary.CreatedAt.UTC().Format(rfc3339),
			LastSeenAt: summary.LastSeenAt.UTC().Format(rfc3339),
			ExpiresAt:  summary.ExpiresAt.UTC().Format(rfc3339),
			IPAddress:  summary.IPAddress,
			UserAgent:  summary.UserAgent,
			Current:    summary.Current,
		})
	}
	writeJSONBody(w, http.StatusOK, map[string]any{"sessions": body})
}

// RevokeSession handles POST /api/v1/me/sessions/revocation.
//
// Ending another session is a critical action, so it demands the account
// password even though the caller already holds a valid session: the session
// proves access, and the password proves the person. A refusal changes
// nothing.
func (h *Handler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok || identity.AccountID == "" {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	var body sessionBody
	if !decodeSessionBody(w, r, &body) {
		return
	}
	if h.revokeSession == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "session revocation unavailable"))
		return
	}

	err := h.revokeSession.Execute(r.Context(), application.RevokeSessionCommand{
		AccountID:        domain.AccountID(identity.AccountID),
		SessionID:        domain.SessionID(body.SessionID),
		CurrentSessionID: domain.SessionID(identity.SessionID),
		Password:         body.Password,
	})
	if err != nil {
		writeSessionProblem(w, r, err)
		return
	}

	writeJSONBody(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// decodeSessionBody reads the small JSON body, answering the caller itself
// when it cannot.
func decodeSessionBody(w http.ResponseWriter, r *http.Request, target *sessionBody) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxSessionBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON"))
		return false
	}
	return true
}

// writeSessionProblem maps the module's sentinels to stable problem codes.
//
// The mapping is here, in the inbound adapter, because a wire code is a fact of
// the interface and not of the domain: the application layer returns what
// happened, and this is where it is said to a client. The refusals of the
// re-authentication path are one code on purpose — a wrong password and a
// session identifier that is not the caller's are indistinguishable, so the
// route cannot be used to enumerate session identifiers.
func writeSessionProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrReauthFailed):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "reauth_failed", "password confirmation failed").WithCause(err))
	case errors.Is(err, application.ErrReauthUnavailable):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "reauth_unavailable", "password confirmation is unavailable").WithCause(err))
	case errors.Is(err, application.ErrSessionNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "session_not_found", "session not found").WithCause(err))
	case errors.Is(err, application.ErrCannotRevokeCurrentSession):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "session_is_current", "the current session is ended by logging out").WithCause(err))
	case errors.Is(err, domain.ErrEmptyAccountID):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_account", "account is required").WithCause(err))
	default:
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "session operation failed").WithCause(err))
	}
}
