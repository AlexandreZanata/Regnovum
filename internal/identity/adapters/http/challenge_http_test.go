// Wiring tests for the anti-bot challenge on the identity surface (P16-T04).
//
// The refusal shape itself (status, code, the absence of a body echo) is
// asserted once in internal/platform/turnstile; what is asserted here is that
// each route is actually behind the challenge, that a solved challenge lets the
// request through to the use case, and that the elevated-risk requirement is
// driven by the real login path rather than by a stub.
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

// solvedVerifier accepts every non-empty token and remembers what it was asked
// to verify, so the test can assert which action a route belongs to.
type solvedVerifier struct {
	mu   sync.Mutex
	seen []turnstile.Verification
}

func (verifier *solvedVerifier) Verify(_ context.Context, verification turnstile.Verification) error {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	verifier.seen = append(verifier.seen, verification)
	return nil
}

func (verifier *solvedVerifier) actions() []turnstile.Action {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	actions := make([]turnstile.Action, 0, len(verifier.seen))
	for _, verification := range verifier.seen {
		actions = append(actions, verification.Action)
	}
	return actions
}

// challengeRequest builds a request the way a browser would: from one address,
// carrying the challenge token when the test passes one.
func challengeRequest(method, path, body, token, address string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set(turnstile.DefaultChallengeHeaderName, token)
	}
	request.RemoteAddr = address
	return request
}

// problemCode decodes the stable code of a refusal.
func problemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("refusal is not a problem document: %v (%s)", err, recorder.Body.String())
	}
	return problem.Code
}

// TestRegisterAndResetRequestRequireAChallenge is the integration item for the
// two actions that create something: neither reaches its use case without a
// token, and both reach it with one.
func TestRegisterAndResetRequestRequireAChallenge(t *testing.T) {
	verifier := &solvedVerifier{}
	enforcer := turnstile.NewEnforcer(turnstile.Config{}, verifier, nil, clientip.New(nil))
	harness := setupHarness(t, nil, enforcer)

	const registerBody = `{"email":"challenged@example.com","password":"ValidSecretPassword123!"}`

	refused := httptest.NewRecorder()
	harness.mux.ServeHTTP(refused, challengeRequest(http.MethodPost, "/api/v1/auth/register", registerBody, "", "198.51.100.30:7000"))
	if refused.Code != http.StatusForbidden {
		t.Fatalf("register without a challenge: status = %d, want 403 (body %s)", refused.Code, refused.Body.String())
	}
	if code := problemCode(t, refused); code != turnstile.CodeChallengeRequired {
		t.Errorf("register without a challenge: code = %q, want %q", code, turnstile.CodeChallengeRequired)
	}
	// A refusal is a private response like every other answer of this surface.
	assertCacheControlPrivateNoStore(t, refused)
	assertProblemContentType(t, refused)

	accepted := httptest.NewRecorder()
	harness.mux.ServeHTTP(accepted, challengeRequest(http.MethodPost, "/api/v1/auth/register", registerBody, "solved-signup", "198.51.100.30:7000"))
	if accepted.Code != http.StatusCreated {
		t.Fatalf("register with a solved challenge: status = %d, want 201 (body %s)", accepted.Code, accepted.Body.String())
	}

	refusedReset := httptest.NewRecorder()
	harness.mux.ServeHTTP(refusedReset, challengeRequest(http.MethodPost, "/api/v1/auth/password-reset/request", `{"email":"challenged@example.com"}`, "", "198.51.100.30:7000"))
	if refusedReset.Code != http.StatusForbidden {
		t.Fatalf("reset request without a challenge: status = %d, want 403", refusedReset.Code)
	}
	if code := problemCode(t, refusedReset); code != turnstile.CodeChallengeRequired {
		t.Errorf("reset request without a challenge: code = %q, want %q", code, turnstile.CodeChallengeRequired)
	}

	acceptedReset := httptest.NewRecorder()
	harness.mux.ServeHTTP(acceptedReset, challengeRequest(http.MethodPost, "/api/v1/auth/password-reset/request", `{"email":"challenged@example.com"}`, "solved-reset", "198.51.100.30:7000"))
	if acceptedReset.Code != http.StatusOK {
		t.Fatalf("reset request with a solved challenge: status = %d, want 200 (body %s)", acceptedReset.Code, acceptedReset.Body.String())
	}

	// The routes are challenged for the actions the policy table names, so a
	// token minted for the signup widget cannot be spent on a reset.
	actions := verifier.actions()
	if len(actions) != 2 || actions[0] != turnstile.ActionSignup || actions[1] != turnstile.ActionPasswordReset {
		t.Errorf("the challenge saw actions %v, want [signup password_reset]", actions)
	}
}

// TestLoginIsChallengedOnlyAfterARunOfFailures is the elevated-risk item end to
// end: the requirement is driven by what the real login path recorded, not by a
// hand-set flag. The same caller is served freely, then gated after three
// failures, then served freely again after a success.
func TestLoginIsChallengedOnlyAfterARunOfFailures(t *testing.T) {
	verifier := &solvedVerifier{}
	tracker := turnstile.NewFailureTracker(turnstile.FailureTrackerOptions{
		Threshold: 3,
		Window:    10 * time.Minute,
	})
	enforcer := turnstile.NewEnforcer(turnstile.Config{}, verifier, tracker, clientip.New(nil))
	harness := setupHarness(t, nil, enforcer)

	const (
		email     = "elevated@example.com"
		password  = "ValidSecretPassword123!"
		fromThis  = "198.51.100.40:8000"
		fromOther = "198.51.100.41:8000"
	)

	// A verified account, with a solved challenge on the registration.
	register(t, harness, email, password, fromThis)
	verifyLatestEmail(t, harness, email, fromThis)

	login := func(caller, password, token string) *httptest.ResponseRecorder {
		body := `{"email":"` + email + `","password":"` + password + `"}`
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, challengeRequest(http.MethodPost, "/api/v1/auth/login", body, token, caller))
		return recorder
	}

	// Three consecutive failures. None of them is challenged, because the run
	// is below the threshold when each one starts; the third is what makes the
	// caller elevated for the next attempt.
	for attempt := 1; attempt <= 3; attempt++ {
		recorder := login(fromThis, "WrongPassword123!", "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status = %d, want 401 (body %s)", attempt, recorder.Code, recorder.Body.String())
		}
	}

	// Three consecutive failures: the next attempt must bring a challenge.
	refused := login(fromThis, password, "")
	if refused.Code != http.StatusForbidden {
		t.Fatalf("the elevated caller: status = %d, want 403 (body %s)", refused.Code, refused.Body.String())
	}
	if code := problemCode(t, refused); code != turnstile.CodeChallengeRequired {
		t.Errorf("the elevated caller: code = %q, want %q", code, turnstile.CodeChallengeRequired)
	}

	// The same credentials from another address are not elevated: the run
	// belongs to the caller that produced it.
	if recorder := login(fromOther, password, ""); recorder.Code != http.StatusOK {
		t.Fatalf("a caller with no failures: status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}

	// And a solved challenge lets the elevated caller through, which clears
	// the run: a person who gets it right is a person again.
	allowed := login(fromThis, password, "solved-login")
	if allowed.Code != http.StatusOK {
		t.Fatalf("the elevated caller with a solved challenge: status = %d, want 200 (body %s)", allowed.Code, allowed.Body.String())
	}
	if recorder := login(fromThis, "WrongPassword123!", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("after a success: status = %d, want 401 (the run should have been cleared)", recorder.Code)
	}

	// The registration carried the signup challenge, and the login was
	// challenged exactly once — for the elevated attempt, with the elevated
	// action, never for the caller that came from another address.
	counts := map[turnstile.Action]int{}
	for _, action := range verifier.actions() {
		counts[action]++
	}
	if counts[turnstile.ActionSignup] != 1 {
		t.Errorf("the signup challenge was spent %d time(s), want 1", counts[turnstile.ActionSignup])
	}
	if counts[turnstile.ActionLoginElevated] != 1 {
		t.Errorf("the elevated login challenge was spent %d time(s), want 1 (all actions: %v)", counts[turnstile.ActionLoginElevated], verifier.actions())
	}
	if len(counts) != 2 {
		t.Errorf("the challenge saw actions %v, want only the signup and the elevated login", verifier.actions())
	}
}

// register creates an account through the real route, with a challenge.
func register(t *testing.T, harness *testHarness, email, password, address string) {
	t.Helper()

	body := `{"email":"` + email + `","password":"` + password + `"}`
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, challengeRequest(http.MethodPost, "/api/v1/auth/register", body, "solved-register", address))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
	}
}

// verifyLatestEmail confirms the account through the token the sender received,
// which is what the login path requires.
func verifyLatestEmail(t *testing.T, harness *testHarness, email, address string) {
	t.Helper()

	token, ok := harness.sender.LastTokenForEmail(mustEmail(email))
	if !ok || token == "" {
		t.Fatalf("no verification token was dispatched for %s", email)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/verify?token="+token, nil)
	request.RemoteAddr = address
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("verify email: status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
}
