package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

const (
	// maxAuthBodyBytes bounds JSON bodies to 64 KiB (P04-T08).
	maxAuthBodyBytes = 64 << 10

	// sessionDuration is the standard cookie max age (14 days absolute ceiling per THR-AUTH-01).
	sessionDuration = 14 * 24 * time.Hour
)

// HandlerConfig aggregates the use cases and security manager required to serve the auth API.
type HandlerConfig struct {
	RegisterUseCase              *application.RegisterAccountUseCase
	VerifyEmailUseCase           *application.VerifyEmailUseCase
	LoginUseCase                 *application.LoginUseCase
	LogoutUseCase                *application.LogoutUseCase
	RequestPasswordResetUseCase  *application.RequestPasswordResetUseCase
	CompletePasswordResetUseCase *application.CompletePasswordResetUseCase
	AuthenticateSessionUseCase   *application.AuthenticateSessionUseCase
	SecurityManager              *security.Manager
	RateLimit                    ratelimit.Protector
	Challenge                    turnstile.Challenger
	BeginMFAEnrollmentUseCase    *application.BeginMFAEnrollmentUseCase
	ConfirmMFAEnrollmentUseCase  *application.ConfirmMFAEnrollmentUseCase
	StepUpMFAUseCase             *application.StepUpMFAUseCase
	RecoverMFAUseCase            *application.RecoverMFAUseCase
	ListSessionsUseCase          *application.ListSessionsUseCase
	RevokeSessionUseCase         *application.RevokeSessionUseCase
	RotateSessionUseCase         *application.RotateSessionUseCase
	Templates                    *HTMLTemplates
}

// Handler serves the versioned HTTP authentication API under /api/v1/auth/* (P04-T08).
type Handler struct {
	register             *application.RegisterAccountUseCase
	verifyEmail          *application.VerifyEmailUseCase
	login                *application.LoginUseCase
	logout               *application.LogoutUseCase
	requestPasswordReset *application.RequestPasswordResetUseCase
	completeReset        *application.CompletePasswordResetUseCase
	authenticateSession  *application.AuthenticateSessionUseCase
	security             *security.Manager
	rateLimit            ratelimit.Protector
	challenge            turnstile.Challenger
	beginMFA             *application.BeginMFAEnrollmentUseCase
	confirmMFA           *application.ConfirmMFAEnrollmentUseCase
	stepUpMFA            *application.StepUpMFAUseCase
	recoverMFA           *application.RecoverMFAUseCase
	listSessions         *application.ListSessionsUseCase
	revokeSession        *application.RevokeSessionUseCase
	rotateSession        *application.RotateSessionUseCase
	templates            *HTMLTemplates
}

// NewHandler constructs and validates an identity HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	templates := cfg.Templates
	if templates == nil {
		templates = NewHTMLTemplates()
	}

	return &Handler{
		register:             cfg.RegisterUseCase,
		verifyEmail:          cfg.VerifyEmailUseCase,
		login:                cfg.LoginUseCase,
		logout:               cfg.LogoutUseCase,
		requestPasswordReset: cfg.RequestPasswordResetUseCase,
		completeReset:        cfg.CompletePasswordResetUseCase,
		authenticateSession:  cfg.AuthenticateSessionUseCase,
		security:             cfg.SecurityManager,
		rateLimit:            cfg.RateLimit,
		challenge:            cfg.Challenge,
		beginMFA:             cfg.BeginMFAEnrollmentUseCase,
		confirmMFA:           cfg.ConfirmMFAEnrollmentUseCase,
		stepUpMFA:            cfg.StepUpMFAUseCase,
		recoverMFA:           cfg.RecoverMFAUseCase,
		listSessions:         cfg.ListSessionsUseCase,
		revokeSession:        cfg.RevokeSessionUseCase,
		rotateSession:        cfg.RotateSessionUseCase,
		templates:            templates,
	}
}

// setPrivateNoStoreHeaders enforces THR-CACHE-01 on all authentication endpoints.
func setPrivateNoStoreHeaders(w http.ResponseWriter) {
	httpcache.Private(w)
}

// protect applies the rate limit policy of one authentication action.
//
// The action's budget lives in the platform policy table, and the key is built
// by the platform from the request: the peer address (with forwarding headers
// honored only from trusted proxies) and, when the caller is authenticated,
// the account. The previous hook built its own key from RemoteAddr and the
// first X-Forwarded-For entry, which any client could set — a spoofed header
// bought an attacker an unlimited number of distinct keys.
func (h *Handler) protect(action ratelimit.Action, next http.Handler) http.Handler {
	if h.rateLimit == nil {
		return withPrivateNoStore(next)
	}
	// A refusal is a private response like the rest of the auth surface, so the
	// cache policy wraps the throttle instead of living inside the handlers it
	// may replace.
	return withPrivateNoStore(h.rateLimit.Protect(action, next))
}

// challenged applies the anti-bot requirement of one authentication action.
//
// It sits *inside* the rate limit on purpose: a caller who is already over its
// budget is refused before it is also made to spend a challenge, and the
// platform layer's refusal is the one that carries Retry-After. A nil
// challenger leaves the route as it was, which is the contract the platform
// package documents for a composition that has not installed one.
func (h *Handler) challenged(action turnstile.Action, next http.Handler) http.Handler {
	if h.challenge == nil {
		return next
	}
	return h.challenge.Challenge(action, next)
}

// withPrivateNoStore guarantees THR-CACHE-01 even for responses produced by
// middleware before the handler runs, such as a throttled request.
func withPrivateNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPrivateNoStoreHeaders(w)
		next.ServeHTTP(w, r)
	})
}

// Register handles POST /api/v1/auth/register.
// Responses strictly avoid account enumeration: whether the email is newly registered
// or already exists, the API returns a uniform 201 Created confirmation.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON"))
		return
	}

	if h.register != nil {
		_, err := h.register.Execute(r.Context(), application.RegisterAccountCommand{
			Email:    req.Email,
			Password: req.Password,
		})
		if err != nil {
			var appErr *apperr.Error
			if errors.As(err, &appErr) && appErr.Kind() == apperr.KindValidation {
				_ = httperror.WriteProblem(w, r, appErr)
				return
			}
			// Non-validation errors (e.g. duplicate account) are swallowed to prevent email enumeration.
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "pending_verification",
	})
}

// Verify handles GET /api/v1/auth/verify?token=...
// Landing page for email verification links: renders clean semantic HTML for browsers,
// or JSON if requested via Accept header.
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	token := r.URL.Query().Get("token")
	isHTML := strings.Contains(r.Header.Get("Accept"), "text/html")

	if token == "" {
		if isHTML {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = h.templates.RenderVerifyError(w, r)
			return
		}
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_token", "verification token is required"))
		return
	}

	if h.verifyEmail != nil {
		if err := h.verifyEmail.Execute(r.Context(), application.VerifyEmailCommand{Token: token}); err != nil {
			if isHTML {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusBadRequest)
				_ = h.templates.RenderVerifyError(w, r)
				return
			}
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_token", "token is invalid or expired").WithCause(err))
			return
		}
	}

	if isHTML {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = h.templates.RenderVerifySuccess(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "verified",
	})
}

// Login handles POST /api/v1/auth/login.
// Employs constant-time dummy hashing (THR-AUTH-02) on non-existent accounts
// to prevent timing enumeration, sets session cookie and rotates CSRF token.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON"))
		return
	}

	if h.login == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "login use case unavailable"))
		return
	}

	result, err := h.login.Execute(r.Context(), application.LoginCommand{
		Email:     req.Email,
		Password:  req.Password,
		IPAddress: r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	// The risk signal that gates the *next* login is maintained here, because
	// this is the only place that knows whether the attempt succeeded: the
	// outcome is reported before it is answered, so a run of failures counts
	// even when the answer is an enumeration-safe problem document. The
	// challenger keys it on the resolved peer address, never on the email, so
	// the signal cannot become an oracle for whether an account exists.
	if h.challenge != nil {
		h.challenge.Observe(r, err != nil)
	}
	if err != nil {
		var appErr *apperr.Error
		if errors.As(err, &appErr) && appErr.Kind() == apperr.KindUnauthorized {
			_ = httperror.WriteProblem(w, r, appErr)
			return
		}
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "invalid_credentials", "invalid email or password"))
		return
	}

	// Post-login credential rotation across HTTP boundary (P04-T07)
	if h.security != nil {
		_, _ = h.security.RotateOnLogin(w, result.RawToken, sessionDuration)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":     "authenticated",
		"account_id": result.Account.ID().String(),
	})
}

// Logout handles POST /api/v1/auth/logout.
// Revokes the session in the database, clears the session cookie and rotates the CSRF token.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	if h.security != nil {
		token, err := h.security.Cookies().GetSessionToken(r)
		if err == nil && h.logout != nil {
			_ = h.logout.Execute(r.Context(), application.LogoutCommand{RawToken: token})
		}
		_, _ = h.security.ClearOnLogout(w)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "logged_out",
	})
}

// RequestPasswordReset handles POST /api/v1/auth/password-reset/request.
// Strict anti-enumeration: always returns 200 OK regardless of account existence.
func (h *Handler) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON"))
		return
	}

	if h.requestPasswordReset != nil {
		_ = h.requestPasswordReset.Execute(r.Context(), application.RequestPasswordResetCommand{Email: req.Email})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "requested",
	})
}

// ViewPasswordReset handles GET /api/v1/auth/password-reset?token=...
// Landing page for password reset email links: renders minimal HTML form for the user to submit new password.
func (h *Handler) ViewPasswordReset(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	token := r.URL.Query().Get("token")
	var csrfToken string
	if h.security != nil {
		csrfToken, _ = h.security.Cookies().GetCSRFToken(r)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = h.templates.RenderPasswordResetForm(w, r, PasswordResetFormData{
		Token:     token,
		CSRFToken: csrfToken,
	})
}

// ConfirmPasswordReset handles POST /api/v1/auth/password-reset/confirm.
// Completes password reset, revokes all active account sessions, and invalidates tokens.
func (h *Handler) ConfirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	isHTML := strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")

	var token, password string
	if isHTML {
		if err := r.ParseForm(); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = h.templates.RenderPasswordResetError(w, r)
			return
		}
		token = r.PostFormValue("token")
		password = r.PostFormValue("password")
	} else {
		var req struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON"))
			return
		}
		token = req.Token
		password = req.Password
	}

	if h.completeReset != nil {
		if err := h.completeReset.Execute(r.Context(), application.CompletePasswordResetCommand{Token: token, NewPassword: password}); err != nil {
			if isHTML {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusBadRequest)
				_ = h.templates.RenderPasswordResetError(w, r)
				return
			}
			var appErr *apperr.Error
			if errors.As(err, &appErr) {
				_ = httperror.WriteProblem(w, r, appErr)
				return
			}
			_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_token", "token invalid, expired, or password too weak").WithCause(err))
			return
		}
	}

	if isHTML {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = h.templates.RenderPasswordResetSuccess(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "password_reset",
	})
}

// SessionValidatorAdapter adapts AuthenticateSessionUseCase to security.SessionValidator.
func (h *Handler) SessionValidatorAdapter() security.SessionValidatorFunc {
	return func(ctx context.Context, rawToken string) (security.AuthIdentity, error) {
		if h.authenticateSession == nil {
			return security.AuthIdentity{}, errors.New("authenticate session use case unavailable")
		}
		res, err := h.authenticateSession.Execute(ctx, application.AuthenticateSessionCommand{RawToken: rawToken})
		if err != nil {
			return security.AuthIdentity{}, err
		}
		return security.AuthIdentity{
			AccountID: res.Account.ID().String(),
			SessionID: res.Session.ID().String(),
		}, nil
	}
}

// RegisterRoutes wires the identity endpoints into the provided ServeMux with appropriate middleware.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// The four actions that create accounts, verify credentials or send mail
	// carry a policy; verification, logout and the reset form do not, because
	// they create nothing and their costs are bounded by the token they
	// require.
	//
	// Three of them also carry a challenge: registering creates an account,
	// requesting a reset sends mail, and logging in is challenged once the
	// caller has already failed repeatedly. The challenge is inside the
	// throttle, so a throttled caller never spends one.
	mux.Handle("POST /api/v1/auth/register", h.protect(ratelimit.ActionAuthRegister, h.challenged(turnstile.ActionSignup, http.HandlerFunc(h.Register))))
	mux.HandleFunc("GET /api/v1/auth/verify", h.Verify)
	mux.Handle("POST /api/v1/auth/login", h.protect(ratelimit.ActionAuthLogin, h.challenged(turnstile.ActionLoginElevated, http.HandlerFunc(h.Login))))
	mux.HandleFunc("POST /api/v1/auth/logout", h.Logout)
	mux.Handle("POST /api/v1/auth/password-reset/request", h.protect(ratelimit.ActionAuthPasswordResetRequest, h.challenged(turnstile.ActionPasswordReset, http.HandlerFunc(h.RequestPasswordReset))))
	mux.HandleFunc("GET /api/v1/auth/password-reset", h.ViewPasswordReset)
	mux.Handle("POST /api/v1/auth/password-reset/confirm", h.protect(ratelimit.ActionAuthPasswordResetConfirm, http.HandlerFunc(h.ConfirmPasswordReset)))

	// The second factor of the administrative surface (P16-T05). These four
	// routes are authenticated — the session is what is being elevated — and
	// the two that accept a code carry a throttle, because a code is a secret
	// a caller can guess: the step-up spends one code per attempt and the
	// recovery spends a hashed comparison per attempt, and both are bounded by
	// the account dimension rather than by the address.
	mux.Handle("POST /api/v1/me/mfa/enrollment", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.BeginMFAEnrollment))))
	mux.Handle("POST /api/v1/me/mfa/enrollment/confirm", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.ConfirmMFAEnrollment))))
	mux.Handle("POST /api/v1/me/mfa/step-up", withPrivateNoStore(h.protect(ratelimit.ActionMFAVerify, h.privateRoute(http.HandlerFunc(h.StepUpMFA)))))
	mux.Handle("POST /api/v1/me/mfa/recovery", withPrivateNoStore(h.protect(ratelimit.ActionMFAVerify, h.privateRoute(http.HandlerFunc(h.RecoverMFA)))))

	// The device list and the critical transition of sessions (P16-T06). The
	// list is a read of the caller's own rows; ending another session is the
	// one that carries a throttle, because it verifies a password and a guess
	// there is a guess at the credential itself.
	mux.Handle("GET /api/v1/me/sessions", withPrivateNoStore(h.privateRoute(http.HandlerFunc(h.ListSessions))))
	mux.Handle("POST /api/v1/me/sessions/revocation", withPrivateNoStore(h.protect(ratelimit.ActionSessionTransition, h.privateRoute(http.HandlerFunc(h.RevokeSession)))))
	mux.Handle("POST /api/v1/me/sessions/rotation", withPrivateNoStore(h.protect(ratelimit.ActionSessionTransition, h.privateRoute(http.HandlerFunc(h.RotateSession)))))
}

// privateRoute applies the session requirement when a security manager is
// configured, which is the same contract the other modules' private surfaces
// use: the composition installs the manager, and a handler built without one
// answers about the missing identity rather than panicking.
func (h *Handler) privateRoute(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}
