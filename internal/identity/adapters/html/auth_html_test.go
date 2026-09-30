// Tests of the browser account journey (P18-T05).
//
// Every test here drives the surface the way a browser without JavaScript does:
// a read that mints the CSRF cookie, then a form POST that carries the token in
// its body. Nothing in this file executes the client module, which is the point
// — the journey has to be complete before the module runs, and the module only
// adds the busy state to a submission that was already going to happen.
package html

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// manifestFixture resolves the six assets the pages load, with the shape
// cmd/assetgen emits.
func manifestFixture() assets.Manifest {
	names := []string{
		"pages/auth.js",
		"styles/reset.css",
		"styles/tokens.css",
		"styles/base.css",
		"styles/primitives.css",
		"styles/auth.css",
	}
	records := make(map[string]assets.Record, len(names))
	for _, name := range names {
		records[name] = assets.Record{Path: "/assets/" + strings.ReplaceAll(name, "/", "-"), SHA256: strings.Repeat("a", 64)}
	}
	return assets.Manifest{Version: 1, Assets: records}
}

// The six operations as programmable fakes. They stand in for the application
// layer, and the two that validate input re-use the real domain function so the
// adapter is exercised against the same rule the application applies.
type (
	fakeRegister struct {
		calls []application.RegisterAccountCommand
		fail  error
	}
	fakeVerify struct {
		calls []application.VerifyEmailCommand
		// failures are consumed in order, one per submission; the queue is what
		// lets a test answer the same document with two different causes.
		failures []error
	}
	fakeLogin struct {
		calls    []application.LoginCommand
		failures []error
		token    string
	}
	fakeLogout struct {
		calls []application.LogoutCommand
		fail  error
	}
	fakeRequestReset struct {
		calls []application.RequestPasswordResetCommand
		fail  error
	}
	fakeCompleteReset struct {
		calls    []application.CompletePasswordResetCommand
		failures []error
	}
	fakeSignal struct {
		observed []bool
	}
)

func (f *fakeRegister) Execute(_ context.Context, command application.RegisterAccountCommand) (*application.RegisterAccountResult, error) {
	f.calls = append(f.calls, command)
	if _, err := domain.ParseEmail(command.Email); err != nil {
		return nil, err
	}
	if len(command.Password) < 8 {
		return nil, application.ErrWeakPassword
	}
	if f.fail != nil {
		return nil, f.fail
	}
	return &application.RegisterAccountResult{AccountID: "acc-1", Email: command.Email}, nil
}

func (f *fakeVerify) Execute(_ context.Context, command application.VerifyEmailCommand) error {
	f.calls = append(f.calls, command)
	return f.next()
}

func (f *fakeVerify) next() error {
	if len(f.failures) == 0 {
		return nil
	}
	failure := f.failures[0]
	f.failures = f.failures[1:]
	return failure
}

func (f *fakeLogin) Execute(_ context.Context, command application.LoginCommand) (*application.LoginResult, error) {
	f.calls = append(f.calls, command)
	if len(f.failures) > 0 {
		failure := f.failures[0]
		f.failures = f.failures[1:]
		return nil, failure
	}
	token := f.token
	if token == "" {
		token = "opaque-session-token"
	}
	return &application.LoginResult{RawToken: token}, nil
}

func (f *fakeLogout) Execute(_ context.Context, command application.LogoutCommand) error {
	f.calls = append(f.calls, command)
	return f.fail
}

func (f *fakeRequestReset) Execute(_ context.Context, command application.RequestPasswordResetCommand) error {
	f.calls = append(f.calls, command)
	return f.fail
}

func (f *fakeCompleteReset) Execute(_ context.Context, command application.CompletePasswordResetCommand) error {
	f.calls = append(f.calls, command)
	if len(command.NewPassword) < 8 {
		return application.ErrWeakPassword
	}
	if len(f.failures) > 0 {
		failure := f.failures[0]
		f.failures = f.failures[1:]
		return failure
	}
	return nil
}

func (f *fakeSignal) Observe(_ *http.Request, failed bool) {
	f.observed = append(f.observed, failed)
}

// journey is the composed surface under test.
type journey struct {
	handler       *Handler
	mux           *http.ServeMux
	register      *fakeRegister
	verify        *fakeVerify
	login         *fakeLogin
	logout        *fakeLogout
	requestReset  *fakeRequestReset
	completeReset *fakeCompleteReset
	signal        *fakeSignal
	security      *security.Manager
}

// newJourney composes the adapter with the real security manager, the real
// templates and programmable use cases.
func newJourney(t *testing.T) *journey {
	t.Helper()
	return newJourneyWith(t, nil)
}

// newJourneyWith composes the same surface with one part substituted, so a test
// can exercise a platform refusal without a limiter.
func newJourneyWith(t *testing.T, mutate func(*HandlerConfig)) *journey {
	t.Helper()

	templates, err := NewTemplates(manifestFixture())
	if err != nil {
		t.Fatalf("NewTemplates() error = %v", err)
	}
	manager, err := security.New(security.Options{
		Env:    config.EnvTest,
		Clock:  clockseed.NewClock(),
		Random: clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}

	built := &journey{
		register:      &fakeRegister{},
		verify:        &fakeVerify{},
		login:         &fakeLogin{},
		logout:        &fakeLogout{},
		requestReset:  &fakeRequestReset{},
		completeReset: &fakeCompleteReset{},
		signal:        &fakeSignal{},
		security:      manager,
	}

	config := HandlerConfig{
		Register:              built.register,
		Verify:                built.verify,
		Login:                 built.login,
		Logout:                built.logout,
		RequestPasswordReset:  built.requestReset,
		CompletePasswordReset: built.completeReset,
		Security:              manager,
		Templates:             templates,
		RiskSignal:            built.signal,
	}
	if mutate != nil {
		mutate(&config)
	}

	handler, err := NewHandler(config)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	built.handler = handler

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	built.mux = mux
	return built
}

// get performs one read.
func (j *journey) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Accept", "text/html")
	recorder := httptest.NewRecorder()
	j.mux.ServeHTTP(recorder, request)
	return recorder
}

// form describes one submission the way the page rendered it.
type form struct {
	page    string
	action  string
	fields  map[string]string
	without string // "csrf_token" drops the field, "cookie" drops the cookie
}

// pageSession is one open document: the token it rendered and the cookies it
// set, which is what a browser holds while a person submits — and resubmits —
// the form in front of them.
type pageSession struct {
	token   string
	cookies []*http.Cookie
}

// open performs the read that mints the CSRF cookie.
func (j *journey) open(t *testing.T, page string) pageSession {
	t.Helper()

	recorder := j.get(t, page)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", page, recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	token := csrfFieldOf(t, recorder.Body.String())
	for _, cookie := range cookies {
		if cookie.Name == "arena_csrf" {
			return pageSession{token: token, cookies: cookies}
		}
	}
	t.Fatalf("GET %s set no CSRF cookie, so the form it rendered could never be submitted", page)
	return pageSession{}
}

// post submits fields the way the rendered form does: url-encoded, in the body,
// with the token and the cookies of the open page.
func (j *journey) post(t *testing.T, session pageSession, action string, fields map[string]string, without string, extra ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	values := url.Values{}
	for name, value := range fields {
		values.Set(name, value)
	}
	if without != "csrf_token" {
		values.Set("csrf_token", session.token)
	}

	request := httptest.NewRequest(http.MethodPost, action, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", browserUserAgent)
	if without != "cookie" {
		for _, cookie := range session.cookies {
			request.AddCookie(cookie)
		}
	}
	for _, cookie := range extra {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	j.mux.ServeHTTP(recorder, request)
	return recorder
}

// submit performs the two requests a browser performs: the read that mints the
// CSRF cookie, then the POST that carries the token in the body.
func (j *journey) submit(t *testing.T, submission form) *httptest.ResponseRecorder {
	t.Helper()

	session := j.open(t, submission.page)
	return j.post(t, session, submission.action, submission.fields, submission.without)
}

// submitTwice posts the same fields twice from one open page. The token and the
// cookies do not change between the two requests, so the only thing that can
// make the two answers differ is the cause the use case reported — which is
// exactly what a refusal must not reveal.
func (j *journey) submitTwice(t *testing.T, submission form) (*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
	t.Helper()

	session := j.open(t, submission.page)
	first := j.post(t, session, submission.action, submission.fields, submission.without)
	second := j.post(t, session, submission.action, submission.fields, submission.without)
	return first, second
}

// browserUserAgent is the device context a real submission carries.
const browserUserAgent = "browser-test/1.0"

// csrfFieldOf reads the hidden token the page rendered.
func csrfFieldOf(t *testing.T, document string) string {
	t.Helper()
	pattern := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	match := pattern.FindStringSubmatch(document)
	if len(match) != 2 {
		t.Fatalf("the rendered page carries no CSRF field:\n%s", document)
	}
	return match[1]
}

// cookieValue reads one cookie of an open page.
func cookieValue(t *testing.T, session pageSession, name string) string {
	t.Helper()
	for _, cookie := range session.cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	t.Fatalf("the open page carries no %s cookie", name)
	return ""
}

// cookieOf returns one Set-Cookie value of a response.
func cookieOf(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response carries no %s cookie: %v", name, recorder.Header().Values("Set-Cookie"))
	return nil
}

func TestEveryRouteOfTheJourneyIsMounted(t *testing.T) {
	t.Parallel()

	registered := Routes()
	if len(registered) != 12 {
		t.Fatalf("Routes() returned %d routes, want the 12 of the journey", len(registered))
	}

	built := newJourney(t)
	for _, route := range registered {
		request := httptest.NewRequest(route.Method, route.Path, nil)
		recorder := httptest.NewRecorder()
		built.mux.ServeHTTP(recorder, request)

		if recorder.Code == http.StatusNotFound || recorder.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s is not mounted: %d", route.Method, route.Path, recorder.Code)
		}
	}
}

func TestReadPagesArePrivateAndCarryTheNoJavaScriptJourney(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	for _, page := range []string{"/register", "/verify", "/login", "/logout", "/reset", "/reset/confirm"} {
		recorder := built.get(t, page)

		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", page, recorder.Code)
		}
		if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("GET %s content type = %q, want text/html", page, contentType)
		}
		if cache := recorder.Header().Get("Cache-Control"); !strings.Contains(cache, "no-store") {
			t.Errorf("GET %s cache control = %q, want a private, no-store policy", page, cache)
		}

		document := recorder.Body.String()
		for _, marker := range []string{
			`<html lang="pt-BR" dir="ltr">`,
			`<form class="ga-auth__form" method="POST"`,
			`name="csrf_token"`,
			`type="hidden"`,
			`<ga-busy label="`,
			`<ga-error-summary`,
			`/assets/pages-auth.js`,
			`/assets/styles-tokens.css`,
		} {
			if !strings.Contains(document, marker) {
				t.Errorf("GET %s does not render %q", page, marker)
			}
		}
		// Nothing inline: the browser policy of this binary allows no inline
		// style, no inline script and no event handler attribute.
		for _, forbidden := range []string{"<style", " onclick=", "<script>", "javascript:"} {
			if strings.Contains(document, forbidden) {
				t.Errorf("GET %s renders %q, which the browser policy blocks", page, forbidden)
			}
		}
	}
}

func TestLoginFormCarriesTheAccessibleFieldWiringThePrimitivesAdopt(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	document := built.get(t, "/login").Body.String()

	for _, marker := range []string{
		`<ga-field name="email" label="Email" hint="`,
		`<label for="email-control">Email</label>`,
		`id="email-control"`,
		`name="email"`,
		`autocomplete="email"`,
		`aria-describedby="email-hint"`,
		`id="email-hint"`,
		`<ga-field name="password" label="Senha" hint="`,
		`id="password-control"`,
		`autocomplete="current-password"`,
		`required="required"`,
		`<button type="submit">Entrar</button>`,
		`href="/reset"`,
	} {
		if !strings.Contains(document, marker) {
			t.Errorf("the sign-in page does not render %q:\n%s", marker, document)
		}
	}
	if strings.Contains(document, `value=""`) {
		t.Error("the sign-in page renders an empty value attribute, which the primitives would adopt as state")
	}
}

func TestRegisterAnswersTheSameDocumentForANewAndAnExistingAddress(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	// The use case answers identically for both cases — that is its contract —
	// so the adapter's job is to not add a difference of its own.
	first := built.submit(t, form{
		page:   "/register",
		action: "/register",
		fields: map[string]string{"email": "nova@exemplo.test", "password": "senha-longa-1"},
	})
	second := built.submit(t, form{
		page:   "/register",
		action: "/register",
		fields: map[string]string{"email": "existente@exemplo.test", "password": "senha-longa-1"},
	})

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("registration answered %d and %d, want 200 for both", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Error("the two registrations rendered different documents, which is exactly the oracle to avoid")
	}
	if !strings.Contains(first.Body.String(), "Confira seu email") {
		t.Errorf("the answer is not the confirmation page:\n%s", first.Body.String())
	}
	if len(built.register.calls) != 2 {
		t.Fatalf("the use case received %d submissions, want 2", len(built.register.calls))
	}
	if got := built.register.calls[0].Email; got != "nova@exemplo.test" {
		t.Errorf("the use case received email %q, want the trimmed submission", got)
	}
}

func TestRegisterRefusesAShortPasswordWithTheMinimumItDeclares(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	recorder := built.submit(t, form{
		page:   "/register",
		action: "/register",
		fields: map[string]string{"email": "pessoa@exemplo.test", "password": strings.Repeat("a", passwordMinLength-1)},
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("a short password answered %d, want 400", recorder.Code)
	}
	document := recorder.Body.String()
	if !strings.Contains(document, "pelo menos 8 caracteres") {
		t.Errorf("the page does not explain the minimum the application enforces:\n%s", document)
	}
	if !strings.Contains(document, `href="#password-control"`) {
		t.Error("the summary does not link the field that has to be fixed")
	}
	if !strings.Contains(document, `id="password-error" role="alert"`) {
		t.Error("the field does not carry its own error paragraph for the primitives to adopt")
	}
	if !strings.Contains(document, `aria-invalid="true"`) {
		t.Error("the refused control is not marked invalid")
	}
	// The address the person typed is kept; the password never is.
	if !strings.Contains(document, `value="pessoa@exemplo.test"`) {
		t.Error("the refused submission lost the address")
	}
	if strings.Contains(document, strings.Repeat("a", passwordMinLength-1)) {
		t.Error("the page echoed the password back")
	}
}

func TestRegisterRefusesAnEmailTheDomainRefuses(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	recorder := built.submit(t, form{
		page:   "/register",
		action: "/register",
		fields: map[string]string{"email": "não-é-email", "password": "senha-longa-1"},
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("an invalid address answered %d, want 400", recorder.Code)
	}
	document := recorder.Body.String()
	if !strings.Contains(document, "endereço de email válido") {
		t.Errorf("the page does not explain the address rule:\n%s", document)
	}
	if !strings.Contains(document, `aria-describedby="email-hint email-error"`) {
		t.Error("the described-by list must keep the hint first and the error second, as the primitives do")
	}
}

func TestRegisterRefusesAnEmptyFieldBeforeCallingTheUseCase(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	recorder := built.submit(t, form{
		page:   "/register",
		action: "/register",
		fields: map[string]string{"email": "  ", "password": ""},
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("an empty submission answered %d, want 400", recorder.Code)
	}
	if len(built.register.calls) != 0 {
		t.Error("the use case was called for a submission that had not been filled in")
	}
	document := recorder.Body.String()
	if count := strings.Count(document, "Preencha este campo.</p>"); count != 2 {
		t.Errorf("the page carries %d field messages, want one per refused field", count)
	}
	for _, link := range []string{`href="#email-control"`, `href="#password-control"`} {
		if !strings.Contains(document, link) {
			t.Errorf("the summary does not link %s:\n%s", link, document)
		}
	}
}

func TestLoginRotatesTheSessionAndTheCSRFCookieAndRedirects(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	built.login.token = "fresh-opaque-token"

	session := built.open(t, "/login")
	before := cookieValue(t, session, "arena_csrf")

	recorder := built.post(t, session, "/login", map[string]string{
		"email":    "pessoa@exemplo.test",
		"password": "senha-longa-1",
	}, "")

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("a successful sign-in answered %d, want 303", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "/" {
		t.Errorf("the redirect points at %q, want the authenticated root", location)
	}

	issued := cookieOf(t, recorder, "arena_session")
	if issued.Value != "fresh-opaque-token" {
		t.Errorf("session cookie = %q, want the token the use case returned", issued.Value)
	}
	if !issued.HttpOnly {
		t.Error("the session cookie must stay HttpOnly")
	}
	rotated := cookieOf(t, recorder, "arena_csrf")
	if rotated.Value == before {
		t.Error("the CSRF token was not rotated on sign-in, so a pre-login token still works")
	}
	if !built.security.CSRF().VerifyToken(rotated.Value) {
		t.Error("the rotated CSRF cookie is not a token this server accepts")
	}
	if len(built.login.calls) != 1 {
		t.Fatalf("the use case received %d attempts, want 1", len(built.login.calls))
	}
	attempt := built.login.calls[0]
	if attempt.IPAddress == "" {
		t.Error("the attempt reached the use case without the peer address every session records")
	}
	if attempt.UserAgent != browserUserAgent {
		t.Errorf("the attempt reached the use case with user agent %q, want the submitted device context", attempt.UserAgent)
	}
}

func TestLoginRefusalIsUniformAndReportsTheRiskSignal(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	// Every refusal is answered by the adapter with one message, whatever the use
	// case said: a suspended account and a wrong password are one answer. The two
	// submissions are byte-identical requests, so any difference in the answer
	// could only come from the cause.
	built.login.failures = []error{domain.ErrAccountSuspended, application.ErrInvalidCredentials}

	suspended, wrongPassword := built.submitTwice(t, form{
		page:   "/login",
		action: "/login",
		fields: map[string]string{"email": "pessoa@exemplo.test", "password": "errada-mas-longa"},
	})

	if suspended.Code != http.StatusUnauthorized || wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("refusals answered %d and %d, want 401 for both", suspended.Code, wrongPassword.Code)
	}
	if suspended.Body.String() != wrongPassword.Body.String() {
		t.Error("the refusal differs by cause, which turns the page into an oracle")
	}
	document := suspended.Body.String()
	if !strings.Contains(document, "Email ou senha inválidos.") {
		t.Errorf("the page does not carry the uniform message:\n%s", document)
	}
	if !strings.Contains(document, `value="pessoa@exemplo.test"`) {
		t.Error("the refusal lost the typed address, which the person has to fix")
	}
	if strings.Contains(document, "errada-mas-longa") {
		t.Error("the refusal echoed the submitted password")
	}

	if len(built.signal.observed) != 2 {
		t.Fatalf("the risk signal saw %d attempts, want 2", len(built.signal.observed))
	}
	if !built.signal.observed[0] || !built.signal.observed[1] {
		t.Error("both attempts failed, so both must be reported as failures")
	}

	// And a successful attempt reports success, so the signal can decay.
	success := built.submit(t, form{
		page:   "/login",
		action: "/login",
		fields: map[string]string{"email": "pessoa@exemplo.test", "password": "senha-longa-1"},
	})
	if success.Code != http.StatusSeeOther {
		t.Fatalf("the successful attempt answered %d, want 303", success.Code)
	}
	if failed := built.signal.observed[len(built.signal.observed)-1]; failed {
		t.Error("a successful attempt was reported as a failure")
	}
}

func TestVerifyAnswersOneRefusalForEveryCauseOfFailure(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	// The same code, refused for two different reasons: the answers must be the
	// same document, because telling them apart tells the reader something about
	// the token they hold.
	built.verify.failures = []error{application.ErrTokenExpired, application.ErrTokenAlreadyUsed}

	expired, spent := built.submitTwice(t, form{
		page:   "/verify",
		action: "/verify",
		fields: map[string]string{"token": "codigo-em-maos"},
	})

	if expired.Code != http.StatusBadRequest || spent.Code != http.StatusBadRequest {
		t.Fatalf("refusals answered %d and %d, want 400 for both", expired.Code, spent.Code)
	}
	if expired.Body.String() != spent.Body.String() {
		t.Error("an expired code and a spent code render different answers")
	}
	if !strings.Contains(expired.Body.String(), "inválido, expirou ou já foi usado") {
		t.Errorf("the refusal is not the uniform one:\n%s", expired.Body.String())
	}

	confirmed := built.submit(t, form{
		page:   "/verify",
		action: "/verify",
		fields: map[string]string{"token": "valido"},
	})
	if confirmed.Code != http.StatusOK {
		t.Fatalf("a valid code answered %d, want 200", confirmed.Code)
	}
	if !strings.Contains(confirmed.Body.String(), "Email confirmado") {
		t.Errorf("the confirmation page is missing:\n%s", confirmed.Body.String())
	}
	if len(built.verify.calls) != 3 {
		t.Fatalf("the use case received %d codes, want 3", len(built.verify.calls))
	}
}

func TestRecoveryRequestAnswersTheSameNoticeEvenWhenSendingFails(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	known := built.submit(t, form{
		page:   "/reset",
		action: "/reset",
		fields: map[string]string{"email": "existente@exemplo.test"},
	})

	built.requestReset.fail = errors.New("the mailer is down")
	unknown := built.submit(t, form{
		page:   "/reset",
		action: "/reset",
		fields: map[string]string{"email": "desconhecido@exemplo.test"},
	})

	if known.Code != http.StatusOK || unknown.Code != http.StatusOK {
		t.Fatalf("the recovery request answered %d and %d, want 200 for both", known.Code, unknown.Code)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Error("a sending failure produced a different page, which reveals that the account exists")
	}
	if !strings.Contains(known.Body.String(), "Se existir uma conta para este endereço") {
		t.Errorf("the notice is not the uniform one:\n%s", known.Body.String())
	}
}

func TestResetConfirmTellsApartOnlyThePasswordMinimum(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	// A wrong code and an expired code are one refusal: the same submission,
	// refused for two reasons, must render the same document.
	built.completeReset.failures = []error{application.ErrInvalidToken, application.ErrTokenExpired}

	wrong, expired := built.submitTwice(t, form{
		page:   "/reset/confirm",
		action: "/reset/confirm",
		fields: map[string]string{"token": "codigo-em-maos", "password": "senha-longa-1"},
	})
	if wrong.Body.String() != expired.Body.String() {
		t.Error("a wrong code and an expired code render different answers")
	}

	short := built.submit(t, form{
		page:   "/reset/confirm",
		action: "/reset/confirm",
		fields: map[string]string{"token": "codigo-em-maos", "password": "curta"},
	})
	if short.Code != http.StatusBadRequest {
		t.Fatalf("a short password answered %d, want 400", short.Code)
	}
	document := short.Body.String()
	if !strings.Contains(document, "pelo menos 8 caracteres") {
		t.Errorf("the password minimum is not explained:\n%s", document)
	}
	if !strings.Contains(document, `href="#password-control"`) {
		t.Error("the summary does not point at the password field")
	}

	changed := built.submit(t, form{
		page:   "/reset/confirm",
		action: "/reset/confirm",
		fields: map[string]string{"token": "valido", "password": "senha-nova-1"},
	})
	if changed.Code != http.StatusOK {
		t.Fatalf("a valid recovery answered %d, want 200", changed.Code)
	}
	if !strings.Contains(changed.Body.String(), "Senha alterada") {
		t.Errorf("the confirmation page is missing:\n%s", changed.Body.String())
	}
	if strings.Contains(changed.Body.String(), "senha-nova-1") {
		t.Error("the confirmation page echoed the new password")
	}
}

func TestLogoutEndsTheSessionAndClearsBothCookies(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	session := built.open(t, "/logout")
	recorder := built.post(t, session, "/logout", nil, "", &http.Cookie{Name: "arena_session", Value: "the-session-to-end"})

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("sign-out answered %d, want 303", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != signInHref {
		t.Errorf("sign-out redirects to %q, want %q", location, signInHref)
	}
	if len(built.logout.calls) != 1 || built.logout.calls[0].RawToken != "the-session-to-end" {
		t.Fatalf("the use case received %v, want the presented session", built.logout.calls)
	}
	if cleared := cookieOf(t, recorder, "arena_session"); cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Errorf("the session cookie was not cleared: %+v", cleared)
	}
	cookieOf(t, recorder, "arena_csrf")
}

func TestLogoutClearsTheCookiesEvenWhenRevocationFails(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	built.logout.fail = errors.New("storage unavailable")

	session := built.open(t, "/logout")
	recorder := built.post(t, session, "/logout", nil, "", &http.Cookie{Name: "arena_session", Value: "still-here"})

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("a failed revocation answered %d, want the redirect: a person who asked to leave must not be trapped", recorder.Code)
	}
	if cleared := cookieOf(t, recorder, "arena_session"); cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Errorf("the session cookie survived a failed revocation: %+v", cleared)
	}
}

func TestAFormWithoutTheCSRFTokenIsRefusedAsAPage(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	for _, without := range []string{"csrf_token", "cookie"} {
		recorder := built.submit(t, form{
			page:    "/login",
			action:  "/login",
			fields:  map[string]string{"email": "pessoa@exemplo.test", "password": "senha-longa-1"},
			without: without,
		})

		if recorder.Code != http.StatusForbidden {
			t.Fatalf("a submission without the %s answered %d, want 403", without, recorder.Code)
		}
		if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("the refusal answered %q, want a localized page instead of a problem document", contentType)
		}
		if !strings.Contains(recorder.Body.String(), "A proteção do formulário expirou") {
			t.Errorf("the refusal does not tell the person what to do:\n%s", recorder.Body.String())
		}
		if len(built.login.calls) != 0 {
			t.Error("a request without a valid CSRF double submit reached the use case")
		}
	}
}

// refusingThrottle answers every submission with the refusal the platform
// enforcer composes, so the translation of a throttled form is proven without a
// limiter.
type refusingThrottle struct{}

func (refusingThrottle) Protect(_ ratelimit.Action, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = next
		writeRateLimited(w, r)
	})
}

func TestAThrottledFormIsAnsweredWithAPage(t *testing.T) {
	t.Parallel()

	built := newJourneyWith(t, func(config *HandlerConfig) {
		config.RateLimit = refusingThrottle{}
	})
	session := built.open(t, "/login")
	recorder := built.post(t, session, "/login", map[string]string{
		"email":    "pessoa@exemplo.test",
		"password": "senha-longa-1",
	}, "")

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("a throttled submission answered %d, want 429", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("the throttle answered %q, want a localized page", contentType)
	}
	if !strings.Contains(recorder.Body.String(), "Muitas requisições") {
		t.Errorf("the throttle page does not carry the localized refusal:\n%s", recorder.Body.String())
	}
	if len(built.login.calls) != 0 {
		t.Error("a throttled submission reached the use case")
	}
}

func TestPagesRenderInTheRequestLocale(t *testing.T) {
	t.Parallel()

	built := newJourney(t)
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	request = request.WithContext(locale.WithLocale(request.Context(), locale.Tag("en-US")))
	recorder := httptest.NewRecorder()
	built.mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", recorder.Code)
	}
	document := recorder.Body.String()
	for _, marker := range []string{`<html lang="en-US" dir="ltr">`, ">Sign in<", "At least 8 characters."} {
		if !strings.Contains(document, marker) {
			t.Errorf("the en-US page does not render %q:\n%s", marker, document)
		}
	}
	if strings.Contains(document, "Entrar") {
		t.Error("the en-US page renders Portuguese copy")
	}
}

func TestNewHandlerFailsClosedOnMissingDependencies(t *testing.T) {
	t.Parallel()

	templates, err := NewTemplates(manifestFixture())
	if err != nil {
		t.Fatalf("NewTemplates() error = %v", err)
	}
	manager, err := security.New(security.Options{Env: config.EnvTest, Clock: clockseed.NewClock(), Random: clockseed.NewRandom()})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}
	var typedNil *fakeRegister

	_, err = NewHandler(HandlerConfig{Templates: templates, Security: manager})
	if err == nil {
		t.Fatal("NewHandler() accepted a composition without the use cases")
	}
	if !strings.Contains(err.Error(), "security manager") && !strings.Contains(err.Error(), "login") {
		t.Errorf("the report does not name what is missing: %v", err)
	}

	_, err = NewHandler(HandlerConfig{
		Register:              typedNil,
		Verify:                &fakeVerify{},
		Login:                 &fakeLogin{},
		Logout:                &fakeLogout{},
		RequestPasswordReset:  &fakeRequestReset{},
		CompletePasswordReset: &fakeCompleteReset{},
		Security:              manager,
		Templates:             templates,
	})
	if err == nil {
		t.Fatal("NewHandler() accepted a typed nil use case, which would panic inside a request")
	}
}

func TestNewTemplatesRefusesAManifestWithoutTheAssets(t *testing.T) {
	t.Parallel()

	if _, err := NewTemplates(assets.Manifest{Version: 1, Assets: map[string]assets.Record{}}); err == nil {
		t.Fatal("NewTemplates() accepted a manifest that cannot resolve the page assets")
	}
}

// writeRateLimited composes the platform refusal the enforcer writes.
func writeRateLimited(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "https://goyim-arena.dev/problems/rate_limited",
		"title":  "Muitas requisições",
		"status": http.StatusTooManyRequests,
		"code":   apperr.New(apperr.KindRateLimited, "rate_limited", "too many attempts").Code(),
		"detail": "too many attempts",
	})
	_ = r
}
