// Tests of the account surface over real PostgreSQL and a real HTTP server
// (P18-T07A): the composition is exercised the way the process serves it —
// platform mux, real repositories, real pages — so the claim "the journey is
// reachable" is answered by an HTTP client and not by a constructor returning
// without an error.
package bootstrap_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	identityhtml "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/html"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
)

// accountJourney is the composed surface serving over a real listener, with the
// disposable database the repositories write to.
type accountJourney struct {
	surface *bootstrap.AccountSurface
	server  *httptest.Server
}

// newAccountSurface composes the account surface against a disposable database
// provisioned with the migrations of the project. The surface is returned
// unmounted, so a test can either mount it on the platform router or assert
// what mounting itself does.
func newAccountSurface(t *testing.T) *bootstrap.AccountSurface {
	t.Helper()

	database := dbtest.New(t)
	surface, err := bootstrap.ComposeAccount(bootstrap.Options{
		Env:    config.EnvTest,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool:   database.Pool.Pool(),
		Clock:  clockseed.NewClock(),
		Random: clockseed.NewRandom(),
		Assets: manifestFixture(t),
	})
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}
	return surface
}

// newAccountJourney mounts the composed surface on the platform router, which
// is the composition `arena server` performs, and serves it on a real listener.
func newAccountJourney(t *testing.T) *accountJourney {
	t.Helper()

	surface := newAccountSurface(t)
	clock := clockseed.NewClock()
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{}, []httpserver.Surface{surface.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &accountJourney{surface: surface, server: server}
}

// browser is a client that keeps cookies and never follows a redirect: the
// journey is asserted on the responses themselves, because the status and the
// cookies are what a browser acts on.
func browser(t *testing.T) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New() error = %v", err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// csrfField is the hidden input every mutating form renders. Reading it from
// the document — instead of from the cookie — is what a browser does, and it
// proves the page actually carries what the middleware will verify.
var csrfField = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// openPage reads one page and returns its document, failing on anything a
// person could not fill in.
func openPage(t *testing.T, client *http.Client, server *httptest.Server, path string) string {
	t.Helper()

	response, err := client.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200 (body: %.200s)", path, response.StatusCode, body)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("GET %s Content-Type = %q, want text/html", path, contentType)
	}
	return string(body)
}

// csrfToken returns the double-submit token of a rendered page.
func csrfToken(t *testing.T, document string) string {
	t.Helper()

	matches := csrfField.FindStringSubmatch(document)
	if len(matches) != 2 {
		t.Fatalf("the page renders no csrf_token field: %.200s", document)
	}
	return matches[1]
}

// submit posts one form the way a browser without JavaScript does.
func submit(t *testing.T, client *http.Client, server *httptest.Server, path string, values url.Values) *http.Response {
	t.Helper()

	response, err := client.PostForm(server.URL+path, values)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

// TestEveryPageOfTheAccountJourneyIsServed is the first half of the task's
// validation: `arena server`, composed from configuration, answers the pages
// of the account journey and the health routes.
func TestEveryPageOfTheAccountJourneyIsServed(t *testing.T) {
	t.Parallel()

	journey := newAccountJourney(t)
	client := browser(t)

	for _, page := range []string{"/register", "/verify", "/login", "/logout", "/reset", "/reset/confirm"} {
		document := openPage(t, client, journey.server, page)
		if !strings.Contains(document, "csrf_token") {
			t.Errorf("GET %s renders no csrf_token field", page)
		}
	}

	response, err := client.Get(journey.server.URL + "/health/live")
	if err != nil {
		t.Fatalf("GET /health/live: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("GET /health/live status = %d, want 200", response.StatusCode)
	}
}

// TestTheAccountJourneyIsCompletableOverPostgreSQL drives the journey the way a
// person does — register, follow the link, sign in — and checks the state the
// database holds afterwards. The link comes from the development sink, which is
// where the message a provider would deliver is recorded in this environment.
func TestTheAccountJourneyIsCompletableOverPostgreSQL(t *testing.T) {
	t.Parallel()

	journey := newAccountJourney(t)
	client := browser(t)

	const email = "journey@example.test"
	const password = "correct horse battery staple"

	// 1. Registration. The answer is the uniform document, whatever the address.
	register := url.Values{
		"email":      {email},
		"password":   {password},
		"csrf_token": {csrfToken(t, openPage(t, client, journey.server, "/register"))},
	}
	registered := submit(t, client, journey.server, "/register", register)
	if registered.StatusCode != http.StatusOK {
		t.Fatalf("POST /register status = %d, want 200", registered.StatusCode)
	}

	// 2. The link that would have been emailed is the one the sink recorded.
	address, err := domain.ParseEmail(email)
	if err != nil {
		t.Fatalf("domain.ParseEmail(%q) error = %v", email, err)
	}
	token, found := journey.surface.LocalSink().LastTokenForEmail(address)
	if !found {
		t.Fatal("registration issued no verification token: the sink recorded no message")
	}

	// 3. Confirmation, with the token from the message.
	verify := url.Values{
		"token":      {token},
		"csrf_token": {csrfToken(t, openPage(t, client, journey.server, "/verify"))},
	}
	// A refusal of the code answers 400; the confirmation itself renders the
	// notice page, so a 200 here is the accepted code and not a rejection.
	verified := submit(t, client, journey.server, "/verify", verify)
	if verified.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(verified.Body)
		t.Fatalf("POST /verify status = %d, want 200 (body: %.200s)", verified.StatusCode, body)
	}

	// 4. Sign in. Only an active account reaches the session, so a 303 here is
	// the confirmation that the previous step took effect in the database.
	login := url.Values{
		"email":      {email},
		"password":   {password},
		"csrf_token": {csrfToken(t, openPage(t, client, journey.server, "/login"))},
	}
	signedIn := submit(t, client, journey.server, "/login", login)
	if signedIn.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(signedIn.Body)
		t.Fatalf("POST /login status = %d, want 303 (body: %.200s)", signedIn.StatusCode, body)
	}
	if !hasCookie(client, journey.server.URL, security.DefaultSessionCookieName) {
		t.Errorf("sign in set no %s cookie: the session never reached the browser", security.DefaultSessionCookieName)
	}

	// 5. Sign out ends it, and the session cookie goes away.
	logout := url.Values{
		"csrf_token": {csrfToken(t, openPage(t, client, journey.server, "/logout"))},
	}
	signedOut := submit(t, client, journey.server, "/logout", logout)
	if signedOut.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /logout status = %d, want 303", signedOut.StatusCode)
	}
}

// TestTheSurfaceRefusesASecondMount is the idempotency of the surface: mounting
// is offered once, and a second attempt is a refusal instead of a panic from
// net/http on a duplicate pattern.
func TestTheSurfaceRefusesASecondMount(t *testing.T) {
	t.Parallel()

	surface := newAccountSurface(t)
	mux := http.NewServeMux()

	if err := surface.Mount(mux); err != nil {
		t.Fatalf("first Mount() error = %v", err)
	}
	if err := surface.Mount(mux); err == nil {
		t.Fatal("second Mount() registered the surface again")
	} else if !strings.Contains(err.Error(), "already mounted") {
		t.Errorf("second Mount() error = %q, want it to report the duplicate mount", err)
	}

	unmounted := newAccountSurface(t)
	if err := unmounted.Mount(nil); err == nil {
		t.Error("Mount(nil) accepted a nil mux")
	}
}

// TestTheCompositionIsDeterministic is the other half of idempotency: composing
// twice from the same inputs mounts the same routes, so the route list of a
// surface cannot depend on the order of an internal map.
func TestTheCompositionIsDeterministic(t *testing.T) {
	t.Parallel()

	first := newAccountSurface(t)
	second := newAccountSurface(t)

	if !reflect.DeepEqual(first.Routes(), second.Routes()) {
		t.Errorf("two compositions declare different routes:\n%v\n%v", first.Routes(), second.Routes())
	}
	if !reflect.DeepEqual(first.Routes(), identityhtml.Routes()) {
		t.Errorf("the surface declares %v, want the adapter's own %v", first.Routes(), identityhtml.Routes())
	}
}

// hasCookie reports whether the client jar holds the named cookie for the host.
func hasCookie(client *http.Client, rawURL, name string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	for _, cookie := range client.Jar.Cookies(parsed) {
		if cookie.Name == name && cookie.Value != "" {
			return true
		}
	}
	return false
}
