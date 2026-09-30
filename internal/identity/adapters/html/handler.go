package html

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/requestid"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/websurface"
)

const (
	// passwordMinLength mirrors the rule the application layer enforces before
	// it hashes anything (a password shorter than eight characters is refused
	// with application.ErrWeakPassword). It exists here only to render the
	// hint, the `aria-required`-adjacent copy and nothing else: the decision
	// stays the application's, and the adapter tests prove that a shorter
	// password is refused with this same message.
	passwordMinLength = 8

	// sessionDuration is the session cookie max age, mirroring the JSON API
	// (14 days absolute ceiling per THR-AUTH-01).
	sessionDuration = 14 * 24 * time.Hour

	// csrfFormField is the hidden input the platform CSRF middleware reads
	// from a form body (security.CSRFTokenManager.Middleware). A browser form
	// cannot set a header, so the double submit travels in the body.
	csrfFormField = websurface.FormField

	// signInHref is the page a signed-out visitor belongs to.
	signInHref = "/login"
)

// The six operations of the journey, as ports oriented to this consumer. The
// application's use cases satisfy them; the surface declares what it calls and
// nothing else, so a test can exercise a journey without a persistence stack
// and composition cannot hand this adapter an operation it never performs.
type (
	// RegisterAccount creates the account (or re-issues the confirmation of an
	// account that already exists, answering identically).
	RegisterAccount interface {
		Execute(ctx context.Context, command application.RegisterAccountCommand) (*application.RegisterAccountResult, error)
	}
	// VerifyEmail consumes a single-use confirmation code.
	VerifyEmail interface {
		Execute(ctx context.Context, command application.VerifyEmailCommand) error
	}
	// Login verifies credentials and opens a session.
	Login interface {
		Execute(ctx context.Context, command application.LoginCommand) (*application.LoginResult, error)
	}
	// Logout revokes the session of an opaque token.
	Logout interface {
		Execute(ctx context.Context, command application.LogoutCommand) error
	}
	// RequestPasswordReset issues a recovery code, uniformly.
	RequestPasswordReset interface {
		Execute(ctx context.Context, command application.RequestPasswordResetCommand) error
	}
	// CompletePasswordReset consumes the recovery code and sets the new
	// password, ending every older session.
	CompletePasswordReset interface {
		Execute(ctx context.Context, command application.CompletePasswordResetCommand) error
	}
)

// RiskSignal observes the outcome of a credential attempt. The browser surface
// carries the same observation the JSON API carries, because the state machine
// that gates the next hard challenge must not go blind on a path a person can
// drive: a run of failed sign-ins through this page is exactly what the signal
// exists to notice. It is optional, and it is an observer *only* — presenting
// the challenge widget is not something this surface can do under the browser
// policy (`script-src 'self'` refuses the vendor's script), so the throttle is
// what bounds guessing here.
type RiskSignal interface {
	Observe(request *http.Request, failed bool)
}

// HandlerConfig aggregates the use cases and platform services of the browser
// journey.
type HandlerConfig struct {
	Register              RegisterAccount
	Verify                VerifyEmail
	Login                 Login
	Logout                Logout
	RequestPasswordReset  RequestPasswordReset
	CompletePasswordReset CompletePasswordReset
	Security              *security.Manager
	RateLimit             ratelimit.Protector
	Templates             *Templates
	RiskSignal            RiskSignal
	// Analytics receives the allowlisted product events of the journey. It
	// is optional: a test drives the journey without one, and a process
	// composed without a provider records nothing.
	Analytics observability.EventSink
}

// Handler serves the browser journey of the account.
type Handler struct {
	register      RegisterAccount
	verify        VerifyEmail
	login         Login
	logout        Logout
	requestReset  RequestPasswordReset
	completeReset CompletePasswordReset
	security      *security.Manager
	rateLimit     ratelimit.Protector
	templates     *Templates
	riskSignal    RiskSignal
	analytics     observability.EventSink
}

// NewHandler validates the composition and builds the handler. It fails closed:
// a journey served without a security manager would render forms whose CSRF
// token nobody checks, so that combination never reaches the mux.
func NewHandler(config HandlerConfig) (*Handler, error) {
	missing := make([]string, 0, 8)
	for name, dependency := range map[string]any{
		"register":               config.Register,
		"verify":                 config.Verify,
		"login":                  config.Login,
		"logout":                 config.Logout,
		"request password reset": config.RequestPasswordReset,
		"complete reset":         config.CompletePasswordReset,
		"security manager":       config.Security,
		"templates":              config.Templates,
	} {
		if unusable(dependency) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("identity html: missing dependencies: %s", strings.Join(missing, ", "))
	}

	return &Handler{
		register:      config.Register,
		verify:        config.Verify,
		login:         config.Login,
		logout:        config.Logout,
		requestReset:  config.RequestPasswordReset,
		completeReset: config.CompletePasswordReset,
		security:      config.Security,
		rateLimit:     config.RateLimit,
		templates:     config.Templates,
		riskSignal:    config.RiskSignal,
		analytics:     config.Analytics,
	}, nil
}

// capture records one allowlisted product event of the journey. The adapter
// supplies only the facts it owns — the account the flow resolved, the
// request correlation and the negotiated locale — and the observability
// package decides what may travel.
func (h *Handler) capture(request *http.Request, name, accountID string) {
	if h.analytics == nil {
		return
	}
	h.analytics.Capture(observability.Event{
		Name:      name,
		AccountID: accountID,
		RequestID: requestid.FromRequest(request),
		Properties: map[string]any{
			"locale": locale.FromContext(request.Context()).String(),
		},
	})
}

// unusable reports whether a dependency cannot be called, which covers both the
// nil interface and the typed nil pointer a composition passes by accident.
func unusable(dependency any) bool {
	return websurface.Missing(dependency)
}

// RegisterRoutes wires the journey into the provided ServeMux.
//
// Reads are plain handlers because they change nothing. Every mutation is a
// `submit`: the platform CSRF middleware proves the request came from a page of
// this origin, the throttle of the action bounds repetition, and the refusal
// translation turns whatever the middleware answers into a page instead of a
// problem document a browser would display raw.
//
// Three actions carry a policy, for the same reason their JSON siblings do:
// creating an account, verifying credentials and sending mail are the three
// things worth repeating from an attacker's side. Confirmation and sign-out
// carry none: they change nothing a stranger can profit from.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /register", h.ShowRegister)
	mux.Handle("POST /register", h.submit(ratelimit.ActionAuthRegister, h.SubmitRegister))

	mux.HandleFunc("GET /verify", h.ShowVerify)
	mux.Handle("POST /verify", h.submit("", h.SubmitVerify))

	mux.HandleFunc("GET /login", h.ShowLogin)
	mux.Handle("POST /login", h.submit(ratelimit.ActionAuthLogin, h.SubmitLogin))

	mux.HandleFunc("GET /logout", h.ShowLogout)
	mux.Handle("POST /logout", h.submit("", h.SubmitLogout))

	mux.HandleFunc("GET /reset", h.ShowResetRequest)
	mux.Handle("POST /reset", h.submit(ratelimit.ActionAuthPasswordResetRequest, h.SubmitResetRequest))

	mux.HandleFunc("GET /reset/confirm", h.ShowResetConfirm)
	mux.Handle("POST /reset/confirm", h.submit(ratelimit.ActionAuthPasswordResetConfirm, h.SubmitResetConfirm))
}

// submit composes the middleware of one mutating browser route.
func (h *Handler) submit(action ratelimit.Action, handler http.HandlerFunc) http.Handler {
	next := http.Handler(handler)
	if action != "" && h.rateLimit != nil {
		next = h.rateLimit.Protect(action, next)
	}
	next = h.security.CSRFMiddleware()(next)
	return h.htmlRefusals(next)
}

// ShowRegister renders the registration form.
func (h *Handler) ShowRegister(w http.ResponseWriter, r *http.Request) {
	page, err := h.registerPage(w, r, "", nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderForm(w, r, http.StatusOK, page)
}

// SubmitRegister creates the account. The answer is uniform: an address that
// already has an account and a fresh address both reach the same page, so the
// page cannot be used to ask whether an address is registered.
func (h *Handler) SubmitRegister(w http.ResponseWriter, r *http.Request) {
	form, ok := h.form(w, r)
	if !ok {
		return
	}
	email := strings.TrimSpace(form.Get("email"))
	password := form.Get("password")

	problems, err := h.requiredErrors(r, value{name: "email", content: email}, value{name: "password", content: password})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var registered *application.RegisterAccountResult
	if len(problems) == 0 {
		result, err := h.register.Execute(r.Context(), application.RegisterAccountCommand{Email: email, Password: password})
		if err != nil {
			classified, isInput, err := h.registrationErrors(r, err)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			if !isInput {
				h.fail(w, r, err)
				return
			}
			problems = classified
		} else {
			registered = result
		}
	}

	if len(problems) > 0 {
		page, err := h.registerPage(w, r, email, problems)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.renderForm(w, r, http.StatusBadRequest, page)
		return
	}

	// The answer is uniform, so the event records the accepted submission
	// and never claims whether the account was created or re-issued.
	if registered != nil && registered.AccountID != "" {
		h.capture(r, observability.EventAccountRegistrationSubmitted, registered.AccountID)
	}

	notice, err := h.notice(w, r, "auth.register.page_title", "auth.register.notice_heading", "auth.register.notice_detail", action{key: "auth.register.notice_action", href: "/verify"})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderNotice(w, r, http.StatusOK, notice)
}

// ShowVerify renders the confirmation-code form.
func (h *Handler) ShowVerify(w http.ResponseWriter, r *http.Request) {
	page, err := h.verifyPage(w, r, "", "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderForm(w, r, http.StatusOK, page)
}

// SubmitVerify consumes the confirmation code. Every rejection is answered with
// the same message: an expired code, a spent code and a code that never existed
// are one refusal, exactly as the JSON API answers.
func (h *Handler) SubmitVerify(w http.ResponseWriter, r *http.Request) {
	form, ok := h.form(w, r)
	if !ok {
		return
	}
	token := strings.TrimSpace(form.Get("token"))

	problems, err := h.requiredErrors(r, value{name: "token", content: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if len(problems) == 0 {
		if err := h.verify.Execute(r.Context(), application.VerifyEmailCommand{Token: token}); err != nil {
			message, err := localized(r, "auth.errors.invalid_code", nil)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			problems = map[string]string{"token": message}
		}
	}

	if len(problems) > 0 {
		page, err := h.verifyPage(w, r, "", problems["token"])
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.renderForm(w, r, http.StatusBadRequest, page)
		return
	}

	notice, err := h.notice(w, r, "auth.verify.page_title", "auth.verify.success_heading", "auth.verify.success_detail", action{key: "auth.verify.success_action", href: signInHref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderNotice(w, r, http.StatusOK, notice)
}

// ShowLogin renders the sign-in form.
func (h *Handler) ShowLogin(w http.ResponseWriter, r *http.Request) {
	page, err := h.loginPage(w, r, "", "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderForm(w, r, http.StatusOK, page)
}

// SubmitLogin verifies credentials and opens the session.
//
// A failure is one message for every cause — a wrong password, an unknown
// address, a suspended account — because the page must not become the oracle
// the JSON API refuses to be. The observation the risk signal needs is recorded
// before the answer is written, including when the answer is a refusal.
func (h *Handler) SubmitLogin(w http.ResponseWriter, r *http.Request) {
	form, ok := h.form(w, r)
	if !ok {
		return
	}
	email := strings.TrimSpace(form.Get("email"))
	password := form.Get("password")

	problems, err := h.requiredErrors(r, value{name: "email", content: email}, value{name: "password", content: password})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	var session string
	var signedIn *application.LoginResult
	if len(problems) == 0 {
		result, err := h.login.Execute(r.Context(), application.LoginCommand{
			Email:     email,
			Password:  password,
			IPAddress: r.RemoteAddr,
			UserAgent: r.UserAgent(),
		})
		if h.riskSignal != nil {
			h.riskSignal.Observe(r, err != nil)
		}
		if err != nil {
			message, translateErr := localized(r, "auth.errors.invalid_credentials", nil)
			if translateErr != nil {
				h.fail(w, r, translateErr)
				return
			}
			problems = map[string]string{"password": message}
		} else {
			session = result.RawToken
			signedIn = result
		}
	}

	if len(problems) > 0 {
		page, err := h.loginPage(w, r, email, problems["password"])
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.renderForm(w, r, http.StatusUnauthorized, page)
		return
	}

	if signedIn != nil && signedIn.Account != nil {
		h.capture(r, observability.EventAccountSignedIn, signedIn.Account.ID().String())
	}

	// Post-login rotation: the new session cookie and a fresh CSRF token, so a
	// token minted before the sign-in cannot be replayed after it.
	if _, err := h.security.RotateOnLogin(w, session, sessionDuration); err != nil {
		h.fail(w, r, err)
		return
	}
	httpcache.Private(w)
	// The authenticated landing page belongs to the next microtask of this
	// phase (the main journey); the root is already its canonical address.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ShowLogout renders the confirmation page whose form ends the session. The
// transition is a POST because it changes state, and the confirmation exists so
// that a plain navigation to /logout never ends a session.
func (h *Handler) ShowLogout(w http.ResponseWriter, r *http.Request) {
	document, err := h.document(r, "auth.logout.page_title")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	token, err := h.csrfToken(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	heading, err := localized(r, "auth.logout.heading", nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	intro, err := localized(r, "auth.logout.intro", nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	submit, err := localized(r, "auth.logout.submit", nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	busy, err := localized(r, "auth.logout.busy", nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	summary, err := localized(r, "auth.errors.summary_title", nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	h.renderForm(w, r, http.StatusOK, FormPageData{
		DocumentData: document,
		Heading:      heading,
		Intro:        intro,
		Action:       "/logout",
		Method:       http.MethodPost,
		CSRFName:     csrfFormField,
		CSRFToken:    token,
		SummaryTitle: summary,
		SubmitLabel:  submit,
		BusyLabel:    busy,
	})
}

// SubmitLogout ends the session of the browser and answers a redirect.
//
// The cookie is cleared even when revoking the row fails, which is what the JSON
// API does: a person who asked to end the session must not be left inside it by
// a transient storage fault. The failure is not silent in the logs, because the
// revocation is what makes the session unusable elsewhere.
func (h *Handler) SubmitLogout(w http.ResponseWriter, r *http.Request) {
	if token, err := h.security.Cookies().GetSessionToken(r); err == nil {
		_ = h.logout.Execute(r.Context(), application.LogoutCommand{RawToken: token})
	}
	if _, err := h.security.ClearOnLogout(w); err != nil {
		h.fail(w, r, err)
		return
	}
	httpcache.Private(w)
	http.Redirect(w, r, signInHref, http.StatusSeeOther)
}

// ShowResetRequest renders the recovery-code request form.
func (h *Handler) ShowResetRequest(w http.ResponseWriter, r *http.Request) {
	page, err := h.resetRequestPage(w, r, "", "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderForm(w, r, http.StatusOK, page)
}

// SubmitResetRequest asks for a recovery code and answers the uniform notice for
// every outcome, including a sending failure: the answer is where the existence
// of the account would leak, and a mailer that is down is not a reason to start
// answering questions about accounts.
func (h *Handler) SubmitResetRequest(w http.ResponseWriter, r *http.Request) {
	form, ok := h.form(w, r)
	if !ok {
		return
	}
	email := strings.TrimSpace(form.Get("email"))

	problems, err := h.requiredErrors(r, value{name: "email", content: email})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if len(problems) > 0 {
		page, err := h.resetRequestPage(w, r, email, problems["email"])
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.renderForm(w, r, http.StatusBadRequest, page)
		return
	}

	_ = h.requestReset.Execute(r.Context(), application.RequestPasswordResetCommand{Email: email})

	notice, err := h.notice(w, r, "auth.reset.page_title", "auth.reset.notice_heading", "auth.reset.notice_detail", action{key: "auth.reset.notice_action", href: "/reset/confirm"})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderNotice(w, r, http.StatusOK, notice)
}

// ShowResetConfirm renders the new-password form.
func (h *Handler) ShowResetConfirm(w http.ResponseWriter, r *http.Request) {
	page, err := h.resetConfirmPage(w, r, "", "", "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderForm(w, r, http.StatusOK, page)
}

// SubmitResetConfirm consumes the recovery code and sets the new password.
//
// Two failures are told apart on purpose, and only these two: a password below
// the minimum is about the person's own input and must be actionable, while a
// code that is wrong, expired or spent is one refusal. Nothing about the account
// is revealed either way.
func (h *Handler) SubmitResetConfirm(w http.ResponseWriter, r *http.Request) {
	form, ok := h.form(w, r)
	if !ok {
		return
	}
	token := strings.TrimSpace(form.Get("token"))
	password := form.Get("password")

	problems, err := h.requiredErrors(r, value{name: "token", content: token}, value{name: "password", content: password})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if len(problems) == 0 {
		if err := h.completeReset.Execute(r.Context(), application.CompletePasswordResetCommand{Token: token, NewPassword: password}); err != nil {
			if errors.Is(err, application.ErrWeakPassword) {
				message, err := localized(r, "auth.errors.weak_password", passwordValues())
				if err != nil {
					h.fail(w, r, err)
					return
				}
				problems = map[string]string{"password": message}
			} else {
				message, err := localized(r, "auth.errors.invalid_code", nil)
				if err != nil {
					h.fail(w, r, err)
					return
				}
				problems = map[string]string{"token": message}
			}
		}
	}

	if len(problems) > 0 {
		page, err := h.resetConfirmPage(w, r, "", problems["token"], problems["password"])
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.renderForm(w, r, http.StatusBadRequest, page)
		return
	}

	notice, err := h.notice(w, r, "auth.reset.confirm_page_title", "auth.reset.confirm_success_heading", "auth.reset.confirm_success_detail", action{key: "auth.reset.confirm_success_action", href: signInHref})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderNotice(w, r, http.StatusOK, notice)
}

// value is one submitted field, named after the control it came from.
type value struct {
	name    string
	content string
}

// action is one localized link of the page.
type action struct {
	key  string
	href string
}

// problems is the field name to message map of one failed submission.
type problems map[string]string

// form parses the submitted document inside the route budget. A body that
// cannot be read is refused with the validation page, which is what the platform
// would answer for a body it refuses.
func (h *Handler) form(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	// A body that cannot be read is refused by websurface.Form with a
	// problem document; the route is always wrapped in htmlRefusals, which
	// turns that document into the localized page. Writing the page here as
	// well would write the answer twice.
	return websurface.Form(w, r)
}

// requiredErrors translates the empty required fields of one submission, in the
// order the form declares them.
func (h *Handler) requiredErrors(r *http.Request, values ...value) (problems, error) {
	submitted := make(map[string]string, len(values))
	for _, field := range values {
		submitted[field.name] = field.content
	}
	found, err := websurface.RequiredErrors(r, "auth.errors.required", submitted)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return problems(found), nil
}

// registrationErrors classifies a registration failure: the two failures about
// the submitted document are answered on their field, everything else is a
// server fault.
func (h *Handler) registrationErrors(r *http.Request, failure error) (problems, bool, error) {
	var domainError domain.DomainError
	if errors.As(failure, &domainError) {
		message, err := localized(r, "auth.errors.invalid_email", nil)
		if err != nil {
			return nil, false, err
		}
		return problems{"email": message}, true, nil
	}
	if errors.Is(failure, application.ErrWeakPassword) {
		message, err := localized(r, "auth.errors.weak_password", passwordValues())
		if err != nil {
			return nil, false, err
		}
		return problems{"password": message}, true, nil
	}
	return nil, false, nil
}

// notice assembles one outcome document.
func (h *Handler) notice(w http.ResponseWriter, r *http.Request, titleKey, headingKey, detailKey string, links ...action) (NoticePageData, error) {
	document, err := h.document(r, titleKey)
	if err != nil {
		return NoticePageData{}, err
	}
	heading, err := localized(r, headingKey, nil)
	if err != nil {
		return NoticePageData{}, err
	}
	detail, err := localized(r, detailKey, nil)
	if err != nil {
		return NoticePageData{}, err
	}
	actions := make([]ActionLink, 0, len(links))
	for _, link := range links {
		label, err := localized(r, link.key, nil)
		if err != nil {
			return NoticePageData{}, err
		}
		actions = append(actions, ActionLink{Label: label, Href: link.href})
	}
	return NoticePageData{DocumentData: document, Heading: heading, Detail: detail, Actions: actions}, nil
}

// registerPage assembles the registration form.
func (h *Handler) registerPage(w http.ResponseWriter, r *http.Request, email string, errors problems) (FormPageData, error) {
	return h.formPage(w, r, formSpec{
		titleKey:      "auth.register.page_title",
		headingKey:    "auth.register.heading",
		introKey:      "auth.register.intro",
		submitKey:     "auth.register.submit",
		busyKey:       "auth.register.busy",
		action:        "/register",
		linkAction:    nil,
		emailValue:    email,
		emailError:    errors["email"],
		passwordError: errors["password"],
		passwordName:  "password",
		passwordKey:   "auth.field.password_label",
		autocomplete:  "new-password",
	})
}

// verifyPage assembles the confirmation-code form.
func (h *Handler) verifyPage(w http.ResponseWriter, r *http.Request, token, tokenError string) (FormPageData, error) {
	document, err := h.document(r, "auth.verify.page_title")
	if err != nil {
		return FormPageData{}, err
	}
	heading, err := localized(r, "auth.verify.heading", nil)
	if err != nil {
		return FormPageData{}, err
	}
	intro, err := localized(r, "auth.verify.intro", nil)
	if err != nil {
		return FormPageData{}, err
	}
	submit, err := localized(r, "auth.verify.submit", nil)
	if err != nil {
		return FormPageData{}, err
	}
	busy, err := localized(r, "auth.verify.busy", nil)
	if err != nil {
		return FormPageData{}, err
	}
	label, err := localized(r, "auth.field.code_label", nil)
	if err != nil {
		return FormPageData{}, err
	}
	hint, err := localized(r, "auth.field.code_hint", nil)
	if err != nil {
		return FormPageData{}, err
	}
	summary, err := localized(r, "auth.errors.summary_title", nil)
	if err != nil {
		return FormPageData{}, err
	}
	tokenValue, err := h.csrfToken(w, r)
	if err != nil {
		return FormPageData{}, err
	}

	fields := []FieldData{newField("token", "text", label, hint, token, "one-time-code", true, tokenError)}
	return FormPageData{
		DocumentData: document,
		Heading:      heading,
		Intro:        intro,
		Action:       "/verify",
		Method:       http.MethodPost,
		CSRFName:     csrfFormField,
		CSRFToken:    tokenValue,
		SummaryTitle: summary,
		Summary:      summaryOf(fields),
		Fields:       fields,
		SubmitLabel:  submit,
		BusyLabel:    busy,
	}, nil
}

// loginPage assembles the sign-in form.
func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request, email, passwordError string) (FormPageData, error) {
	page, err := h.formPage(w, r, formSpec{
		titleKey:      "auth.login.page_title",
		headingKey:    "auth.login.heading",
		introKey:      "auth.login.intro",
		submitKey:     "auth.login.submit",
		busyKey:       "auth.login.busy",
		action:        "/login",
		linkAction:    &action{key: "auth.login.reset_action", href: "/reset"},
		emailValue:    email,
		emailError:    "",
		passwordError: passwordError,
		passwordName:  "password",
		passwordKey:   "auth.field.password_label",
		autocomplete:  "current-password",
	})
	return page, err
}

// resetRequestPage assembles the recovery-code request form.
func (h *Handler) resetRequestPage(w http.ResponseWriter, r *http.Request, email, emailError string) (FormPageData, error) {
	return h.formPage(w, r, formSpec{
		titleKey:      "auth.reset.page_title",
		headingKey:    "auth.reset.heading",
		introKey:      "auth.reset.intro",
		submitKey:     "auth.reset.submit",
		busyKey:       "auth.reset.busy",
		action:        "/reset",
		linkAction:    &action{key: "auth.reset.notice_action", href: "/reset/confirm"},
		emailValue:    email,
		emailError:    emailError,
		passwordError: "",
		passwordName:  "",
		passwordKey:   "",
		autocomplete:  "",
	})
}

// resetConfirmPage assembles the new-password form: the recovery code and the
// new password in one submission.
func (h *Handler) resetConfirmPage(w http.ResponseWriter, r *http.Request, token, tokenError, passwordError string) (FormPageData, error) {
	document, err := h.document(r, "auth.reset.confirm_page_title")
	if err != nil {
		return FormPageData{}, err
	}
	heading, err := localized(r, "auth.reset.confirm_heading", nil)
	if err != nil {
		return FormPageData{}, err
	}
	intro, err := localized(r, "auth.reset.confirm_intro", nil)
	if err != nil {
		return FormPageData{}, err
	}
	submit, err := localized(r, "auth.reset.confirm_submit", nil)
	if err != nil {
		return FormPageData{}, err
	}
	busy, err := localized(r, "auth.reset.confirm_busy", nil)
	if err != nil {
		return FormPageData{}, err
	}
	codeLabel, err := localized(r, "auth.field.code_label", nil)
	if err != nil {
		return FormPageData{}, err
	}
	codeHint, err := localized(r, "auth.field.code_hint", nil)
	if err != nil {
		return FormPageData{}, err
	}
	passwordLabel, err := localized(r, "auth.field.new_password_label", nil)
	if err != nil {
		return FormPageData{}, err
	}
	passwordHint, err := localized(r, "auth.field.password_hint", passwordValues())
	if err != nil {
		return FormPageData{}, err
	}
	summary, err := localized(r, "auth.errors.summary_title", nil)
	if err != nil {
		return FormPageData{}, err
	}
	tokenValue, err := h.csrfToken(w, r)
	if err != nil {
		return FormPageData{}, err
	}

	fields := []FieldData{
		newField("token", "text", codeLabel, codeHint, token, "one-time-code", true, tokenError),
		newField("password", "password", passwordLabel, passwordHint, "", "new-password", true, passwordError),
	}
	return FormPageData{
		DocumentData: document,
		Heading:      heading,
		Intro:        intro,
		Action:       "/reset/confirm",
		Method:       http.MethodPost,
		CSRFName:     csrfFormField,
		CSRFToken:    tokenValue,
		SummaryTitle: summary,
		Summary:      summaryOf(fields),
		Fields:       fields,
		SubmitLabel:  submit,
		BusyLabel:    busy,
	}, nil
}

// formSpec is the shared shape of the email-and-password forms.
type formSpec struct {
	titleKey      string
	headingKey    string
	introKey      string
	submitKey     string
	busyKey       string
	action        string
	linkAction    *action
	emailValue    string
	emailError    string
	passwordError string
	passwordName  string
	passwordKey   string
	autocomplete  string
}

// formPage assembles an email-and-password form.
func (h *Handler) formPage(w http.ResponseWriter, r *http.Request, spec formSpec) (FormPageData, error) {
	document, err := h.document(r, spec.titleKey)
	if err != nil {
		return FormPageData{}, err
	}
	heading, err := localized(r, spec.headingKey, nil)
	if err != nil {
		return FormPageData{}, err
	}
	intro, err := localized(r, spec.introKey, nil)
	if err != nil {
		return FormPageData{}, err
	}
	submit, err := localized(r, spec.submitKey, nil)
	if err != nil {
		return FormPageData{}, err
	}
	busy, err := localized(r, spec.busyKey, nil)
	if err != nil {
		return FormPageData{}, err
	}
	emailLabel, err := localized(r, "auth.field.email_label", nil)
	if err != nil {
		return FormPageData{}, err
	}
	emailHint, err := localized(r, "auth.field.email_hint", nil)
	if err != nil {
		return FormPageData{}, err
	}
	summary, err := localized(r, "auth.errors.summary_title", nil)
	if err != nil {
		return FormPageData{}, err
	}
	token, err := h.csrfToken(w, r)
	if err != nil {
		return FormPageData{}, err
	}

	fields := []FieldData{newField("email", "email", emailLabel, emailHint, spec.emailValue, "email", true, spec.emailError)}
	if spec.passwordName != "" {
		label, err := localized(r, spec.passwordKey, nil)
		if err != nil {
			return FormPageData{}, err
		}
		hint, err := localized(r, "auth.field.password_hint", passwordValues())
		if err != nil {
			return FormPageData{}, err
		}
		fields = append(fields, newField(spec.passwordName, "password", label, hint, "", spec.autocomplete, true, spec.passwordError))
	}

	page := FormPageData{
		DocumentData: document,
		Heading:      heading,
		Intro:        intro,
		Action:       spec.action,
		Method:       http.MethodPost,
		CSRFName:     csrfFormField,
		CSRFToken:    token,
		SummaryTitle: summary,
		Summary:      summaryOf(fields),
		Fields:       fields,
		SubmitLabel:  submit,
		BusyLabel:    busy,
	}
	if spec.linkAction != nil {
		label, err := localized(r, spec.linkAction.key, nil)
		if err != nil {
			return FormPageData{}, err
		}
		page.After = &ActionLink{Label: label, Href: spec.linkAction.href}
	}
	return page, nil
}

// summaryOf lists the fields that carry an error, in form order, so the summary
// and the fields can never disagree about what is wrong.
func summaryOf(fields []FieldData) []SummaryItem {
	items := make([]SummaryItem, 0, len(fields))
	for _, field := range fields {
		if field.Error == "" {
			continue
		}
		items = append(items, SummaryItem{Target: field.ControlID, Message: field.Error})
	}
	return items
}

// document is the shared chrome of one page: the request locale, the page title
// of the journey and the account navigation.
func (h *Handler) document(r *http.Request, titleKey string) (DocumentData, error) {
	title, err := localized(r, titleKey, nil)
	if err != nil {
		return DocumentData{}, err
	}
	brand, err := localized(r, "auth.brand", nil)
	if err != nil {
		return DocumentData{}, err
	}
	navLabel, err := localized(r, "auth.nav.label", nil)
	if err != nil {
		return DocumentData{}, err
	}
	links := []struct {
		key  string
		href string
	}{
		{key: "auth.nav.login", href: signInHref},
		{key: "auth.nav.register", href: "/register"},
		{key: "auth.nav.verify", href: "/verify"},
		{key: "auth.nav.reset", href: "/reset"},
	}
	nav := make([]ActionLink, 0, len(links))
	for _, link := range links {
		label, err := localized(r, link.key, nil)
		if err != nil {
			return DocumentData{}, err
		}
		nav = append(nav, ActionLink{Label: label, Href: link.href})
	}
	return DocumentData{
		Lang:      requestLocale(r),
		PageTitle: title,
		Brand:     brand,
		NavLabel:  navLabel,
		Nav:       nav,
	}, nil
}

// csrfToken returns the double-submit token the form must carry.
//
// The page can only render the value the cookie holds, so a request without a
// valid signed cookie is issued a fresh one: a form rendered without a token
// would be refused by the middleware on the way back, which looks exactly like a
// broken page.
func (h *Handler) csrfToken(w http.ResponseWriter, r *http.Request) (string, error) {
	return websurface.CSRF(h.security, w, r)
}

// renderForm writes one form document. Every page of the journey is private and
// never stored: a document that carries a token or an address must not end up in
// a shared cache.
func (h *Handler) renderForm(w http.ResponseWriter, r *http.Request, status int, page FormPageData) {
	h.render(w, r, status, func(writer io.Writer) error {
		return h.templates.RenderForm(writer, page)
	})
}

// renderNotice writes one outcome document.
func (h *Handler) renderNotice(w http.ResponseWriter, r *http.Request, status int, page NoticePageData) {
	h.render(w, r, status, func(writer io.Writer) error {
		return h.templates.RenderNotice(writer, page)
	})
}

// render writes a document, or reports the failure as a server fault. A
// template that cannot render must not leave a half-written page: the body is
// buffered and only the finished document reaches the connection.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, execute func(io.Writer) error) {
	body, err := websurface.Document(execute)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	websurface.WritePrivate(w, status, body)
}

// fail answers an unexpected failure as a problem document. The refusal
// translation of the mutating routes turns it into the localized page, so a
// server fault reaches a person as a page and a program as RFC 9457.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, failure error) {
	_ = httperror.WriteProblem(w, r, failure)
}

// refuse writes a localized refusal page straight from the error vocabulary.
func (h *Handler) refuse(w http.ResponseWriter, r *http.Request, status int, kind apperr.Kind, csrf bool) {
	titleKey, detailKey := refusalKeys(kind, csrf)
	title, err := localized(r, titleKey, nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	detail, err := localized(r, detailKey, nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	document, err := h.document(r, titleKey)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	signIn, err := localized(r, "auth.nav.login", nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderNotice(w, r, status, NoticePageData{
		DocumentData: document,
		Heading:      title,
		Detail:       detail,
		Actions:      []ActionLink{{Label: signIn, Href: signInHref}},
	})
}

// refusalKeys resolves the catalog pair of one refusal: the shared error
// vocabulary by kind, except for the CSRF refusals, whose message has to tell
// the person what to do about it (reload and submit again).
func refusalKeys(kind apperr.Kind, csrf bool) (string, string) {
	if csrf {
		return "auth.errors.csrf_title", "auth.errors.csrf_detail"
	}
	return "errors." + string(kind) + ".title", "errors." + string(kind) + ".detail"
}

// htmlRefusals turns the problem documents the platform middleware produces
// around this surface into the pages the browser journey expects.
//
// Without it, a spent rate-limit budget would answer a form submission with
// `application/problem+json` — technically correct and useless to a person. The
// middleware's refusal is written into a buffer, and only a problem document is
// translated: a document this adapter rendered passes through untouched, so the
// translation can never rewrite a page or a redirect.
func (h *Handler) htmlRefusals(next http.Handler) http.Handler {
	return websurface.Refusals(h.presentRefusal, next)
}

// presentRefusal renders one platform refusal as a localized page.
func (h *Handler) presentRefusal(w http.ResponseWriter, r *http.Request, refusal websurface.Refusal) {
	h.refuse(w, r, refusal.Status, refusal.Kind, refusal.CSRF)
}

// localized resolves a catalog message in the request locale, falling back to
// the default locale so a partially translated catalog never renders an empty
// label (I18N_STANDARD §8).
func localized(r *http.Request, key string, values map[string]string) (string, error) {
	return websurface.Localized(r, key, values)
}

// requestLocale is the interface locale of the request, as resolved by the
// platform locale middleware.
func requestLocale(r *http.Request) string {
	return websurface.Locale(r)
}

// passwordValues are the values of every catalog message about the password
// minimum, so the number lives in one place.
func passwordValues() map[string]string {
	return map[string]string{"min": strconv.Itoa(passwordMinLength)}
}
