// Tests of the account JSON API composed in the process (P49-T01): the
// handlers run on the platform mux over disposable PostgreSQL, driven by
// real HTTP with the same security boundary as the pages — one pool, one
// cookie/CSRF manager, no second authentication.
package bootstrap_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// accountJSONJourney is the composed account surface on a real listener.
type accountJSONJourney struct {
	surface *bootstrap.AccountSurface
	server  *httptest.Server
}

// newAccountJSONJourney composes exactly like `arena server`: one pool, one
// shared security boundary, mounted on the platform router.
func newAccountJSONJourney(t *testing.T) *accountJSONJourney {
	t.Helper()

	database := dbtest.New(t)
	manager, err := security.New(security.Options{Env: config.EnvTest, Clock: clockseed.NewClock(), Random: clockseed.NewRandom()})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}
	surface, err := bootstrap.ComposeAccount(bootstrap.Options{
		Env:      config.EnvTest,
		Logger:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool:     database.Pool.Pool(),
		Clock:    clockseed.NewClock(),
		Random:   clockseed.NewRandom(),
		Assets:   manifestFixture(t),
		Security: manager,
	})
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clockseed.NewClock())
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{}, []httpserver.Surface{surface.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &accountJSONJourney{surface: surface, server: server}
}

// jsonPost sends one JSON document without following redirects.
func jsonPost(t *testing.T, client *http.Client, server *httptest.Server, path, body string) (int, []byte, http.Header) {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read POST %s: %v", path, err)
	}
	return response.StatusCode, raw, response.Header
}

// jsonGet sends one GET without following redirects.
func jsonGet(t *testing.T, client *http.Client, server *httptest.Server, path string) (int, []byte) {
	t.Helper()

	response, err := client.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	return response.StatusCode, raw
}

// registerVerifyLogin drives register → verify → login over JSON and returns
// the login document. It proves the same sink the pages use feeds the API.
func registerVerifyLogin(t *testing.T, journey *accountJSONJourney, client *http.Client, email, password string) map[string]string {
	t.Helper()

	status, _, _ := jsonPost(t, client, journey.server, "/api/v1/auth/register", `{"email":`+strconv(email)+`,"password":`+strconv(password)+`}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /api/v1/auth/register status = %d, want 201", status)
	}
	address, err := domain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	token, found := journey.surface.LocalSink().LastTokenForEmail(address)
	if !found {
		t.Fatalf("registration of %s issued no token", email)
	}
	if status, _ := jsonGet(t, client, journey.server, "/api/v1/auth/verify?token="+token); status != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/verify status = %d, want 200", status)
	}
	status, raw, _ := jsonPost(t, client, journey.server, "/api/v1/auth/login", `{"email":`+strconv(email)+`,"password":`+strconv(password)+`}`)
	if status != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/login status = %d, want 200 (body: %.200s)", status, raw)
	}
	var document map[string]string
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("login is not JSON: %v", err)
	}
	if document["status"] != "authenticated" {
		t.Fatalf("login status = %q, want authenticated", document["status"])
	}
	return document
}

// sessionList is one decoded GET /api/v1/me/sessions document.
type sessionList struct {
	Sessions []struct {
		ID      string `json:"id"`
		Current bool   `json:"current"`
	} `json:"sessions"`
}

// listSessions decodes the owner's session list, failing on anything but 200.
func listSessions(t *testing.T, client *http.Client, journey *accountJSONJourney) sessionList {
	t.Helper()

	status, raw := jsonGet(t, client, journey.server, "/api/v1/me/sessions")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/sessions status = %d, want 200 (body: %.200s)", status, raw)
	}
	var decoded sessionList
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("sessions list is not JSON: %v", err)
	}
	return decoded
}

// strconv quotes one JSON string without importing a second encoder.
func strconv(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// assertPrivateNoStore proves THR-CACHE-01 on a private JSON answer.
func assertPrivateNoStore(t *testing.T, header http.Header) {
	t.Helper()

	if got := header.Get("Cache-Control"); !strings.Contains(got, "private") || !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control = %q, want private, no-store", got)
	}
}

// TestAccountJSONRegisterLoginSessionsLogout proves the journey the process
// serves: register → verify → login (same session/CSRF cookies as the pages)
// → own sessions → logout clears the session and the token stops working.
func TestAccountJSONRegisterLoginSessionsLogout(t *testing.T) {
	t.Parallel()

	journey := newAccountJSONJourney(t)
	client := browser(t)
	const email = "json-journey@example.test"
	const password = "correct horse battery staple"

	registerVerifyLogin(t, journey, client, email, password)

	if !hasCookie(client, journey.server.URL, security.DefaultSessionCookieName) {
		t.Error("login set no session cookie: the JSON API does not share the pages' cookie")
	}
	if !hasCookie(client, journey.server.URL, security.DefaultCSRFCookieName) {
		t.Error("login set no CSRF cookie: the JSON API does not share the pages' CSRF")
	}

	listed := listSessions(t, client, journey)
	if len(listed.Sessions) != 1 || !listed.Sessions[0].Current {
		t.Fatalf("sessions = %+v, want one current session", listed.Sessions)
	}

	status, raw, header := jsonPost(t, client, journey.server, "/api/v1/auth/logout", `{}`)
	if status != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/logout status = %d, want 200 (body: %.200s)", status, raw)
	}
	assertPrivateNoStore(t, header)

	if status, _ := jsonGet(t, client, journey.server, "/api/v1/me/sessions"); status != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/me/sessions after logout status = %d, want 401", status)
	}
}

// TestAccountJSONRequiresAuth proves the private family fails closed: no
// session cookie means 401 before any secret is touched.
func TestAccountJSONRequiresAuth(t *testing.T) {
	t.Parallel()

	journey := newAccountJSONJourney(t)
	anonymous := browser(t)

	if status, _ := jsonGet(t, anonymous, journey.server, "/api/v1/me/sessions"); status != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/me/sessions anonymous status = %d, want 401", status)
	}
	for _, path := range []string{
		"/api/v1/me/mfa/enrollment",
		"/api/v1/me/mfa/enrollment/confirm",
		"/api/v1/me/mfa/step-up",
		"/api/v1/me/mfa/recovery",
		"/api/v1/me/sessions/revocation",
		"/api/v1/me/sessions/rotation",
	} {
		if status, _, _ := jsonPost(t, anonymous, journey.server, path, `{}`); status != http.StatusUnauthorized {
			t.Errorf("POST %s anonymous status = %d, want 401", path, status)
		}
	}
}

// TestAccountJSONRotation proves the critical transition: rotating ends the
// calling session and issues a fresh one atomically from the client's view.
func TestAccountJSONRotation(t *testing.T) {
	t.Parallel()

	journey := newAccountJSONJourney(t)
	first := browser(t)
	const email = "json-rotation@example.test"
	const password = "correct horse battery staple"

	registerVerifyLogin(t, journey, first, email, password)

	status, raw, _ := jsonPost(t, first, journey.server, "/api/v1/me/sessions/rotation", `{}`)
	if status != http.StatusOK {
		t.Fatalf("POST /api/v1/me/sessions/rotation status = %d, want 200 (body: %.200s)", status, raw)
	}
	var rotated map[string]string
	if err := json.Unmarshal(raw, &rotated); err != nil {
		t.Fatalf("rotation is not JSON: %v", err)
	}
	if rotated["status"] != "rotated" {
		t.Fatalf("rotation status = %q, want rotated", rotated["status"])
	}

	if got := listSessions(t, first, journey); len(got.Sessions) != 1 {
		t.Fatalf("sessions after rotation = %d, want 1", len(got.Sessions))
	}
}

// TestAccountJSONRevocation proves ownership with re-authentication: the
// wrong password changes nothing, the right one ends the other session, and
// the revoked token is refused while the current one survives.
func TestAccountJSONRevocation(t *testing.T) {
	t.Parallel()

	journey := newAccountJSONJourney(t)
	current := browser(t)
	other := browser(t)
	const email = "json-revocation@example.test"
	const password = "correct horse battery staple"

	registerVerifyLogin(t, journey, current, email, password)
	status, _, _ := jsonPost(t, other, journey.server, "/api/v1/auth/login", `{"email":`+strconv(email)+`,"password":`+strconv(password)+`}`)
	if status != http.StatusOK {
		t.Fatalf("second login status = %d, want 200", status)
	}

	mine := listSessions(t, current, journey)
	if len(mine.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(mine.Sessions))
	}
	var target string
	for _, entry := range mine.Sessions {
		if !entry.Current {
			target = entry.ID
		}
	}
	if target == "" {
		t.Fatal("no non-current session to revoke")
	}

	wrong := `{"session_id":` + strconv(target) + `,"password":"wrong password"}`
	if status, _, _ := jsonPost(t, current, journey.server, "/api/v1/me/sessions/revocation", wrong); status != http.StatusForbidden {
		t.Errorf("revocation with wrong password status = %d, want 403", status)
	}
	if got := listSessions(t, current, journey); len(got.Sessions) != 2 {
		t.Fatalf("sessions after refused revocation = %d, want 2 (refusal must change nothing)", len(got.Sessions))
	}

	right := `{"session_id":` + strconv(target) + `,"password":` + strconv(password) + `}`
	if status, raw, _ := jsonPost(t, current, journey.server, "/api/v1/me/sessions/revocation", right); status != http.StatusOK {
		t.Fatalf("revocation status = %d, want 200 (body: %.200s)", status, raw)
	}
	if got := listSessions(t, current, journey); len(got.Sessions) != 1 {
		t.Fatalf("sessions after revocation = %d, want 1", len(got.Sessions))
	}
	if status, _ := jsonGet(t, other, journey.server, "/api/v1/me/sessions"); status != http.StatusUnauthorized {
		t.Errorf("revoked session status = %d, want 401", status)
	}
}
