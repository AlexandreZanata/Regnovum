package http_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	identityhttp "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	identitypg "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// sessionHTTPHarness is the identity surface with the real session validator:
// the middleware resolves the cookie through the same use case production uses,
// so a revoked, expired or rotated token is refused by the platform and not by
// a fake the test wrote.
type sessionHTTPHarness struct {
	mux      http.Handler
	pool     *pgxpool.Pool
	repo     *identitypg.Repository
	account  *domain.Account
	password string
	first    string
	second   string
}

const (
	sessionHTTPFirst  = "session-http-first-token"
	sessionHTTPSecond = "session-http-second-token"
)

func newSessionHTTPHarness(t *testing.T) *sessionHTTPHarness {
	t.Helper()
	ctx := context.Background()

	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := identitypg.NewRepository(pool)

	hasher, err := argon2id.New(argon2id.FastParams(), rand.Reader)
	if err != nil {
		t.Fatalf("argon2id.New: %v", err)
	}
	password := "CorrectHorse123!"
	hash, err := hasher.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	email, err := domain.ParseEmail("sessions-http@arena.example.com")
	if err != nil {
		t.Fatalf("parse email: %v", err)
	}
	account, err := repo.CreateAccountWithPassword(ctx, email, hash)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := repo.SetEmailVerified(ctx, account.ID(), time.Now().UTC()); err != nil {
		t.Fatalf("verify email: %v", err)
	}

	policy := domain.DefaultSessionPolicy()
	clock := &steppingClock{now: time.Now().UTC()}
	for _, token := range []string{sessionHTTPFirst, sessionHTTPSecond} {
		hash := sha256Sum(token)
		if _, err := repo.CreateSession(ctx, account.ID(), hash, time.Now().UTC().Add(24*time.Hour), "198.51.100.20", "ArenaClient/1.0"); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}

	authenticate := application.NewAuthenticateSessionUseCase(repo, repo, clock, policy, 0)
	securityManager := mustSecurityManager(t)
	handler := identityhttp.NewHandler(identityhttp.HandlerConfig{
		AuthenticateSessionUseCase: authenticate,
		LoginUseCase:               application.NewLoginUseCase(repo, repo, repo, hasher, clock, clockseed.NewRandom(), policy),
		ListSessionsUseCase:        application.NewListSessionsUseCase(repo, clock, policy),
		RevokeSessionUseCase:       application.NewRevokeSessionUseCase(repo, repo, hasher),
		RotateSessionUseCase:       application.NewRotateSessionUseCase(repo, repo, clock, clockseed.NewRandom(), policy),
		SecurityManager:            securityManager,
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	return &sessionHTTPHarness{
		mux:      securityManager.AuthenticateMiddleware(handler.SessionValidatorAdapter())(mux),
		pool:     pool,
		repo:     repo,
		account:  account,
		password: password,
		first:    sessionHTTPFirst,
		second:   sessionHTTPSecond,
	}
}

// sha256Sum is the stored form of a session token: the column holds the digest,
// never the value a browser presents.
func sha256Sum(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func sessionHTTPDo(harness *sessionHTTPHarness, method, path, body string, tokens ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for _, token := range tokens {
		request.AddCookie(&http.Cookie{Name: security.DefaultSessionCookieName, Value: token})
	}
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, request)
	return recorder
}

func sessionHTTPObject(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &object); err != nil {
		t.Fatalf("decode body: %v (body: %s)", err, recorder.Body.String())
	}
	return object
}

func sessionProblemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body: %s)", contentType, recorder.Body.String())
	}
	code, _ := sessionHTTPObject(t, recorder)["code"].(string)
	return code
}

// sessionIDOf reads the identifier the database assigned to a token.
func sessionIDOf(t *testing.T, harness *sessionHTTPHarness, token string) string {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(context.Background(),
		`SELECT id FROM app.sessions WHERE token_hash = $1`, sha256Sum(token)).Scan(&id); err != nil {
		t.Fatalf("read the session of %q: %v", token, err)
	}
	return id
}

func assertSessionPrivateHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	cacheControl := recorder.Header().Get("Cache-Control")
	for _, directive := range []string{"private", "no-store"} {
		if !strings.Contains(cacheControl, directive) {
			t.Fatalf("Cache-Control = %q, want %q", cacheControl, directive)
		}
	}
}

func TestSessionEndpointsRequireASession(t *testing.T) {
	harness := newSessionHTTPHarness(t)

	targets := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/me/sessions", ""},
		{http.MethodPost, "/api/v1/me/sessions/revocation", `{"session_id":"x","password":"y"}`},
		{http.MethodPost, "/api/v1/me/sessions/rotation", ""},
	}
	for _, target := range targets {
		recorder := sessionHTTPDo(harness, target.method, target.path, target.body)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a session status = %d, want 401 (body: %s)", target.method, target.path, recorder.Code, recorder.Body.String())
		}
		assertSessionPrivateHeaders(t, recorder)
	}
}

func TestSessionListReturnsTheOwnersSessionsAndMarksTheCaller(t *testing.T) {
	harness := newSessionHTTPHarness(t)

	recorder := sessionHTTPDo(harness, http.MethodGet, "/api/v1/me/sessions", "", harness.first)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list sessions status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertSessionPrivateHeaders(t, recorder)

	document := sessionHTTPObject(t, recorder)
	sessions, ok := document["sessions"].([]any)
	if !ok || len(sessions) != 2 {
		t.Fatalf("sessions = %v, want the two of the account", document["sessions"])
	}
	current := 0
	callerID := sessionIDOf(t, harness, harness.first)
	for _, entry := range sessions {
		session, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("session entry = %v, want an object", entry)
		}
		if isCurrent, _ := session["current"].(bool); isCurrent {
			current++
			if session["id"] != callerID {
				t.Fatalf("the current entry is %v, want the calling session %s", session["id"], callerID)
			}
		}
	}

	if current != 1 {
		t.Fatalf("entries marked current = %d, want exactly one", current)
	}
	// No credential travels: neither the token nor its hash can appear.
	if strings.Contains(recorder.Body.String(), harness.first) || strings.Contains(recorder.Body.String(), fmt.Sprintf("%x", sha256Sum(harness.first))) {
		t.Fatal("the session listing carries session credential material")
	}
	for _, other := range []string{harness.second} {
		if strings.Contains(recorder.Body.String(), other) {
			t.Fatal("the session listing carries another session token")
		}
	}
}

func TestSessionRevocationRequiresThePasswordAndChangesNothingWithoutIt(t *testing.T) {
	harness := newSessionHTTPHarness(t)
	target := sessionIDOf(t, harness, harness.second)

	recorder := sessionHTTPDo(harness, http.MethodPost, "/api/v1/me/sessions/revocation",
		fmt.Sprintf(`{"session_id":%q,"password":"WrongPassword!"}`, target), harness.first)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("revocation with a wrong password status = %d, want 403 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if code := sessionProblemCode(t, recorder); code != "reauth_failed" {
		t.Fatalf("problem code = %q, want reauth_failed", code)
	}

	// The refused call changed nothing: the addressed session still works.
	if after := sessionHTTPDo(harness, http.MethodGet, "/api/v1/me/sessions", "", harness.second); after.Code != http.StatusOK {
		t.Fatalf("the addressed session after a refused revocation status = %d, want 200", after.Code)
	}
	// And the unknown identifier answers exactly like the succeeded-looking
	// one, so the route cannot be asked which sessions exist.
	recorder = sessionHTTPDo(harness, http.MethodPost, "/api/v1/me/sessions/revocation",
		fmt.Sprintf(`{"session_id":%q,"password":%q}`, "00000000-0000-0000-0000-000000000000", harness.password), harness.first)
	if code := sessionProblemCode(t, recorder); recorder.Code != http.StatusNotFound || code != "session_not_found" {
		t.Fatalf("revocation of an unknown session = (%d, %q), want (404, session_not_found)", recorder.Code, code)
	}
}

func TestSessionRevocationEndsOnlyTheAddressedSession(t *testing.T) {
	harness := newSessionHTTPHarness(t)
	caller := sessionIDOf(t, harness, harness.first)
	target := sessionIDOf(t, harness, harness.second)

	// The calling session is ended by logging out, not here.
	recorder := sessionHTTPDo(harness, http.MethodPost, "/api/v1/me/sessions/revocation",
		fmt.Sprintf(`{"session_id":%q,"password":%q}`, caller, harness.password), harness.first)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("revoking the calling session status = %d, want 409 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if code := sessionProblemCode(t, recorder); code != "session_is_current" {
		t.Fatalf("problem code = %q, want session_is_current", code)
	}

	recorder = sessionHTTPDo(harness, http.MethodPost, "/api/v1/me/sessions/revocation",
		fmt.Sprintf(`{"session_id":%q,"password":%q}`, target, harness.password), harness.first)
	if recorder.Code != http.StatusOK {
		t.Fatalf("revocation status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertSessionPrivateHeaders(t, recorder)

	// The ended session is refused by the platform from now on; the caller's
	// own session is untouched.
	if after := sessionHTTPDo(harness, http.MethodGet, "/api/v1/me/sessions", "", harness.second); after.Code != http.StatusUnauthorized {
		t.Fatalf("the ended session status = %d, want 401", after.Code)
	}
	if callerAfter := sessionHTTPDo(harness, http.MethodGet, "/api/v1/me/sessions", "", harness.first); callerAfter.Code != http.StatusOK {
		t.Fatalf("the calling session status = %d, want 200", callerAfter.Code)
	}
}

func TestSessionRevocationIsAtomicUnderConcurrency(t *testing.T) {
	harness := newSessionHTTPHarness(t)
	target := sessionIDOf(t, harness, harness.second)

	const workers = 4
	var wg sync.WaitGroup
	codes := make([]int, workers)
	errs := make([]string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			recorder := sessionHTTPDo(harness, http.MethodPost, "/api/v1/me/sessions/revocation",
				fmt.Sprintf(`{"session_id":%q,"password":%q}`, target, harness.password), harness.first)
			codes[worker] = recorder.Code
			errs[worker] = recorder.Body.String()
		}(i)
	}
	wg.Wait()

	ok, notFound := 0, 0
	for i := 0; i < workers; i++ {
		switch codes[i] {
		case http.StatusOK:
			ok++
		case http.StatusNotFound:
			notFound++
		default:
			t.Fatalf("concurrent revocation %d status = %d, want 200 or 404 (body: %s)", i, codes[i], errs[i])
		}
	}
	if ok != 1 || notFound != workers-1 {
		t.Fatalf("concurrent revocations = %d ok and %d not found, want exactly one winner", ok, notFound)
	}
	if after := sessionHTTPDo(harness, http.MethodGet, "/api/v1/me/sessions", "", harness.second); after.Code != http.StatusUnauthorized {
		t.Fatalf("the ended session status = %d, want 401", after.Code)
	}
}

func TestSessionRotationEndsTheOldTokenAndIssuesANewOne(t *testing.T) {
	harness := newSessionHTTPHarness(t)
	oldID := sessionIDOf(t, harness, harness.first)

	recorder := sessionHTTPDo(harness, http.MethodPost, "/api/v1/me/sessions/rotation", "", harness.first)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rotation status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertSessionPrivateHeaders(t, recorder)

	cookie := sessionCookieFrom(t, recorder)
	if cookie == "" || cookie == harness.first {
		t.Fatalf("rotated cookie = %q, want a fresh server-generated token", cookie)
	}
	document := sessionHTTPObject(t, recorder)
	if document["status"] != "rotated" {
		t.Fatalf("rotation body = %v, want status rotated", document)
	}

	// The old token is worthless from the moment the new one exists.
	if after := sessionHTTPDo(harness, http.MethodGet, "/api/v1/me/sessions", "", harness.first); after.Code != http.StatusUnauthorized {
		t.Fatalf("the token taken before the rotation status = %d, want 401", after.Code)
	}
	if after := sessionHTTPDo(harness, http.MethodGet, "/api/v1/me/sessions", "", cookie); after.Code != http.StatusOK {
		t.Fatalf("the rotated token status = %d, want 200 (body: %s)", after.Code, after.Body.String())
	}

	// The listing shows the new session as the current one, and the old row is
	// revoked rather than removed.
	document = sessionHTTPObject(t, sessionHTTPDo(harness, http.MethodGet, "/api/v1/me/sessions", "", cookie))
	sessions := document["sessions"].([]any)
	current := 0
	for _, entry := range sessions {
		if isCurrent, _ := entry.(map[string]any)["current"].(bool); isCurrent {
			current++
		}
	}
	if current != 1 {
		t.Fatalf("entries marked current after the rotation = %d, want exactly one", current)
	}
	var revoked bool
	if err := harness.pool.QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM app.sessions WHERE id = $1`, oldID).Scan(&revoked); err != nil {
		t.Fatalf("read the old session: %v", err)
	}
	if !revoked {
		t.Fatal("the rotated-from session is still active")
	}
}

func TestSessionRotationDoesNotCarryTheSecondFactorElevation(t *testing.T) {
	harness := newSessionHTTPHarness(t)
	ctx := context.Background()

	if _, err := harness.pool.Exec(ctx,
		`UPDATE app.sessions SET mfa_verified_at = now() WHERE token_hash = $1`, sha256Sum(harness.first)); err != nil {
		t.Fatalf("elevate the fixture session: %v", err)
	}

	recorder := sessionHTTPDo(harness, http.MethodPost, "/api/v1/me/sessions/rotation", "", harness.first)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rotation status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	cookie := sessionCookieFrom(t, recorder)
	if cookie == "" {
		t.Fatal("the rotation did not return a new cookie")
	}

	// A privilege that a session proved does not survive the transition: the
	// new session may act, but it must present the factor again before an
	// administrative action is served.
	var verifiedAt *time.Time
	if err := harness.pool.QueryRow(ctx,
		`SELECT mfa_verified_at FROM app.sessions WHERE token_hash = $1`, sha256Sum(cookie)).Scan(&verifiedAt); err != nil {
		t.Fatalf("read the rotated session: %v", err)
	}
	if verifiedAt != nil {
		t.Fatalf("the rotated session inherited the second factor (%v), want a session that must present it again", verifiedAt)
	}
}

func TestLoginNeverAdoptsAClientSuppliedSessionToken(t *testing.T) {
	harness := newSessionHTTPHarness(t)
	const chosen = "attacker-chosen-token"

	// A cookie the client chose is a credential the platform refuses: it is
	// validated like any other and answered as invalid, never adopted.
	recorder := sessionHTTPDo(harness, http.MethodPost, "/api/v1/auth/login",
		fmt.Sprintf(`{"email":%q,"password":%q}`, harness.account.Email().String(), harness.password), chosen)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("login carrying a client-chosen session status = %d, want 401 (body: %s)", recorder.Code, recorder.Body.String())
	}

	// With no cookie at all, the token the platform issues is its own, and two
	// logins are two sessions rather than one reused token.
	seen := make(map[string]bool)
	for attempt := 0; attempt < 2; attempt++ {
		recorder = sessionHTTPDo(harness, http.MethodPost, "/api/v1/auth/login",
			fmt.Sprintf(`{"email":%q,"password":%q}`, harness.account.Email().String(), harness.password))
		if recorder.Code != http.StatusOK {
			t.Fatalf("login attempt %d status = %d, want 200 (body: %s)", attempt, recorder.Code, recorder.Body.String())
		}
		token := sessionCookieFrom(t, recorder)
		if token == "" || token == chosen {
			t.Fatalf("login attempt %d issued %q, want a server-generated token", attempt, token)
		}
		if seen[token] {
			t.Fatalf("login attempt %d reused a token the platform already issued", attempt)
		}
		seen[token] = true
		if after := sessionHTTPDo(harness, http.MethodGet, "/api/v1/me/sessions", "", token); after.Code != http.StatusOK {
			t.Fatalf("the issued token of attempt %d status = %d, want 200", attempt, after.Code)
		}
	}
}

// sessionCookieFrom reads the session cookie a response set.
func sessionCookieFrom(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == security.DefaultSessionCookieName && cookie.Value != "" {
			return cookie.Value
		}
	}
	return ""
}
