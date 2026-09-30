package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/fakeemail"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

type testHarness struct {
	handler  *adapterhttp.Handler
	mux      http.Handler
	sender   *fakeemail.Sender
	security *security.Manager
}

func mustEmail(s string) domain.Email {
	e, err := domain.ParseEmail(s)
	if err != nil {
		panic(err)
	}
	return e
}

func setupTestHarness(t *testing.T, rateLimit ratelimit.Protector) *testHarness {
	t.Helper()
	return setupHarness(t, rateLimit, nil)
}

// setupHarness builds the identity surface with the security layers the caller
// asks for. The throttle and the challenge are parameters because the tests
// that are *about* one of them must install it while the tests that are about
// something else must not be forced through it.
func setupHarness(t *testing.T, rateLimit ratelimit.Protector, challenge turnstile.Challenger) *testHarness {
	t.Helper()
	db := dbtest.New(t)
	repo := postgres.NewRepository(db.Pool.Pool())
	random := clockseed.NewRandom()
	hasher, err := argon2id.New(argon2id.FastParams(), random)
	if err != nil {
		t.Fatalf("setup hasher: %v", err)
	}
	sender := fakeemail.NewSender()
	clock := clockseed.NewClock()

	vPolicy := domain.DefaultVerificationPolicy()
	sPolicy := domain.DefaultSessionPolicy()
	rPolicy := domain.DefaultPasswordResetPolicy()

	regUC := application.NewRegisterAccountUseCase(repo, repo, hasher, sender, clock, random, vPolicy)
	verUC := application.NewVerifyEmailUseCase(repo, repo, clock)
	logUC := application.NewLoginUseCase(repo, repo, repo, hasher, clock, random, sPolicy)
	loutUC := application.NewLogoutUseCase(repo)
	reqResetUC := application.NewRequestPasswordResetUseCase(repo, repo, sender, clock, random, rPolicy)
	compResetUC := application.NewCompletePasswordResetUseCase(repo, repo, repo, repo, repo, hasher, sender, clock)
	authSessUC := application.NewAuthenticateSessionUseCase(repo, repo, clock, sPolicy, 5*time.Minute)

	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false, // permissive in test harness unless explicitly tested
		Clock:          clock,
		Random:         random,
	})
	if err != nil {
		t.Fatalf("setup security manager: %v", err)
	}

	h := adapterhttp.NewHandler(adapterhttp.HandlerConfig{
		RegisterUseCase:              regUC,
		VerifyEmailUseCase:           verUC,
		LoginUseCase:                 logUC,
		LogoutUseCase:                loutUC,
		RequestPasswordResetUseCase:  reqResetUC,
		CompletePasswordResetUseCase: compResetUC,
		AuthenticateSessionUseCase:   authSessUC,
		SecurityManager:              secMgr,
		RateLimit:                    rateLimit,
		Challenge:                    challenge,
	})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Wrap entire mux with Authenticate middleware
	wrappedMux := secMgr.AuthenticateMiddleware(h.SessionValidatorAdapter())(mux)

	return &testHarness{
		handler:  h,
		mux:      wrappedMux,
		sender:   sender,
		security: secMgr,
	}
}

func TestFullAuthJourney_HTTP(t *testing.T) {
	harness := setupTestHarness(t, nil)

	// 1. REGISTER
	regBody := `{"email":"alice@example.com","password":"ValidSecretPassword123!"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	harness.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d (body: %s)", w.Code, w.Body.String())
	}
	assertCacheControlPrivateNoStore(t, w)

	var regResp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &regResp); err != nil {
		t.Fatalf("unmarshal register response: %v", err)
	}
	if regResp["status"] != "pending_verification" {
		t.Fatalf("expected pending_verification, got %s", regResp["status"])
	}

	verifyToken, ok := harness.sender.LastTokenForEmail(mustEmail("alice@example.com"))
	if !ok || verifyToken == "" {
		t.Fatal("expected email verification token to be dispatched")
	}

	// 2. VERIFY EMAIL (HTML browser link landing page)
	verifyReqHTML := httptest.NewRequest(http.MethodGet, "/api/v1/auth/verify?token="+verifyToken, nil)
	verifyReqHTML.Header.Set("Accept", "text/html,application/xhtml+xml")
	wVerifyHTML := httptest.NewRecorder()
	harness.mux.ServeHTTP(wVerifyHTML, verifyReqHTML)

	if wVerifyHTML.Code != http.StatusOK {
		t.Fatalf("verify email html: expected 200, got %d", wVerifyHTML.Code)
	}
	assertCacheControlPrivateNoStore(t, wVerifyHTML)
	if !strings.Contains(wVerifyHTML.Body.String(), "Email verificado com sucesso") {
		t.Fatalf("expected HTML verification success document, got: %s", wVerifyHTML.Body.String())
	}

	// Replay of email verification token must fail
	wReplay := httptest.NewRecorder()
	harness.mux.ServeHTTP(wReplay, verifyReqHTML)
	if wReplay.Code != http.StatusBadRequest {
		t.Fatalf("replay token: expected 400 Bad Request, got %d", wReplay.Code)
	}

	// 3. LOGIN
	loginBody := `{"email":"alice@example.com","password":"ValidSecretPassword123!"}`
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	wLogin := httptest.NewRecorder()
	harness.mux.ServeHTTP(wLogin, loginReq)

	if wLogin.Code != http.StatusOK {
		t.Fatalf("login: expected 200, got %d (body: %s)", wLogin.Code, wLogin.Body.String())
	}
	assertCacheControlPrivateNoStore(t, wLogin)

	var loginResp map[string]string
	if err := json.Unmarshal(wLogin.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("unmarshal login: %v", err)
	}
	if loginResp["status"] != "authenticated" || loginResp["account_id"] == "" {
		t.Fatalf("unexpected login response: %+v", loginResp)
	}

	// Extract session and CSRF cookies
	loginCookies := wLogin.Result().Cookies()
	var sessionCookie, csrfCookie *http.Cookie
	for _, c := range loginCookies {
		if c.Name == security.DefaultSessionCookieName {
			sessionCookie = c
		}
		if c.Name == security.DefaultCSRFCookieName {
			csrfCookie = c
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("session cookie arena_session not set on login")
	}
	if csrfCookie == nil || csrfCookie.Value == "" {
		t.Fatal("csrf cookie arena_csrf not set on login")
	}

	// 4. LOGOUT
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutReq.AddCookie(sessionCookie)
	logoutReq.AddCookie(csrfCookie)
	logoutReq.Header.Set(security.DefaultCSRFHeaderName, csrfCookie.Value)
	wLogout := httptest.NewRecorder()
	harness.mux.ServeHTTP(wLogout, logoutReq)

	if wLogout.Code != http.StatusOK {
		t.Fatalf("logout: expected 200, got %d", wLogout.Code)
	}
	assertCacheControlPrivateNoStore(t, wLogout)

	// Check that session cookie was cleared in logout response
	var sessionCleared bool
	for _, c := range wLogout.Result().Cookies() {
		if c.Name == security.DefaultSessionCookieName && c.MaxAge == -1 {
			sessionCleared = true
		}
	}
	if !sessionCleared {
		t.Fatal("expected arena_session cookie to be cleared with MaxAge=-1 on logout")
	}

	// 5. REQUEST PASSWORD RESET
	resetReqBody := `{"email":"alice@example.com"}`
	resetReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password-reset/request", strings.NewReader(resetReqBody))
	resetReq.Header.Set("Content-Type", "application/json")
	wResetReq := httptest.NewRecorder()
	harness.mux.ServeHTTP(wResetReq, resetReq)

	if wResetReq.Code != http.StatusOK {
		t.Fatalf("password reset request: expected 200, got %d", wResetReq.Code)
	}
	assertCacheControlPrivateNoStore(t, wResetReq)

	resetToken, ok := harness.sender.LastResetTokenForEmail(mustEmail("alice@example.com"))
	if !ok || resetToken == "" {
		t.Fatal("expected reset token to be dispatched to email")
	}

	// 6. VIEW PASSWORD RESET FORM (HTML landing page)
	viewResetReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/password-reset?token="+resetToken, nil)
	wViewReset := httptest.NewRecorder()
	harness.mux.ServeHTTP(wViewReset, viewResetReq)

	if wViewReset.Code != http.StatusOK {
		t.Fatalf("view reset form: expected 200, got %d", wViewReset.Code)
	}
	assertCacheControlPrivateNoStore(t, wViewReset)
	if !strings.Contains(wViewReset.Body.String(), "<form") || !strings.Contains(wViewReset.Body.String(), resetToken) {
		t.Fatalf("expected reset form containing token, got: %s", wViewReset.Body.String())
	}

	// 7. CONFIRM PASSWORD RESET (form post or JSON)
	confirmForm := url.Values{}
	confirmForm.Set("token", resetToken)
	confirmForm.Set("password", "BrandNewPassword2026!")
	confirmReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password-reset/confirm", strings.NewReader(confirmForm.Encode()))
	confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wConfirm := httptest.NewRecorder()
	harness.mux.ServeHTTP(wConfirm, confirmReq)

	if wConfirm.Code != http.StatusOK {
		t.Fatalf("confirm password reset: expected 200, got %d", wConfirm.Code)
	}
	assertCacheControlPrivateNoStore(t, wConfirm)
	if !strings.Contains(wConfirm.Body.String(), "Senha redefinida com sucesso") {
		t.Fatalf("expected reset success HTML, got: %s", wConfirm.Body.String())
	}

	// 8. LOGIN WITH NEW PASSWORD
	newLoginBody := `{"email":"alice@example.com","password":"BrandNewPassword2026!"}`
	newLoginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(newLoginBody))
	newLoginReq.Header.Set("Content-Type", "application/json")
	wNewLogin := httptest.NewRecorder()
	harness.mux.ServeHTTP(wNewLogin, newLoginReq)

	if wNewLogin.Code != http.StatusOK {
		t.Fatalf("login with new password: expected 200, got %d", wNewLogin.Code)
	}

	// Old password must now fail
	oldLoginBody := `{"email":"alice@example.com","password":"ValidSecretPassword123!"}`
	oldLoginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(oldLoginBody))
	oldLoginReq.Header.Set("Content-Type", "application/json")
	wOldLogin := httptest.NewRecorder()
	harness.mux.ServeHTTP(wOldLogin, oldLoginReq)

	if wOldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("login with old password: expected 401 Unauthorized, got %d", wOldLogin.Code)
	}
}

func TestAntiEnumeration(t *testing.T) {
	harness := setupTestHarness(t, nil)

	// 1. Duplicate email registration returns uniform 201 Created
	regBody := `{"email":"dup@example.com","password":"InitialPassword123!"}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(regBody))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	harness.mux.ServeHTTP(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w1.Code)
	}

	// Second registration of same email: must ALSO return 201 Created
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(regBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	harness.mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusCreated {
		t.Fatalf("duplicate register: expected 201 Created (anti-enumeration), got %d", w2.Code)
	}

	// 2. Login with non-existent email returns 401 with standard Problem Details
	badLogin := `{"email":"ghost@example.com","password":"WrongPassword123!"}`
	reqLogin := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(badLogin))
	reqLogin.Header.Set("Content-Type", "application/json")
	wBadLogin := httptest.NewRecorder()
	harness.mux.ServeHTTP(wBadLogin, reqLogin)

	if wBadLogin.Code != http.StatusUnauthorized {
		t.Fatalf("non-existent email login: expected 401, got %d", wBadLogin.Code)
	}
	assertProblemContentType(t, wBadLogin)

	// 3. Password reset request for non-existent email returns 200 OK uniform
	badReset := `{"email":"nonexistent@example.com"}`
	reqReset := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password-reset/request", strings.NewReader(badReset))
	reqReset.Header.Set("Content-Type", "application/json")
	wBadReset := httptest.NewRecorder()
	harness.mux.ServeHTTP(wBadReset, reqReset)

	if wBadReset.Code != http.StatusOK {
		t.Fatalf("non-existent reset request: expected 200 OK (anti-enumeration), got %d", wBadReset.Code)
	}
}

func TestBodyLimitEnforcement(t *testing.T) {
	harness := setupTestHarness(t, nil)

	// Construct body > 64 KiB
	oversized := strings.Repeat("A", 70*1024)
	payload := `{"email":"huge@example.com","password":"` + oversized + `"}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	harness.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: expected 400 Bad Request, got %d", w.Code)
	}
	assertProblemContentType(t, w)
}

// refusingGuard refuses every action, recording which actions it was asked
// about, so the test can prove the throttle runs before the use case.
type refusingGuard struct {
	mu      sync.Mutex
	actions []ratelimit.Action
}

func (guard *refusingGuard) Allow(_ context.Context, action ratelimit.Action, _ ...ratelimit.Subject) (ratelimit.Decision, error) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.actions = append(guard.actions, action)
	return ratelimit.Decision{Allowed: false, Refused: ratelimit.SubjectAddress, RetryAfter: 30 * time.Second, Limit: 10}, nil
}

func (guard *refusingGuard) seen() []ratelimit.Action {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	return append([]ratelimit.Action(nil), guard.actions...)
}

func TestThrottledLoginIsRefusedBeforeTheUseCase(t *testing.T) {
	guard := &refusingGuard{}
	harness := setupTestHarness(t, ratelimit.New(guard, clientip.New(nil)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"rate@example.com","password":"Pass"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	harness.mux.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", w.Code)
	}
	assertProblemContentType(t, w)
	assertCacheControlPrivateNoStore(t, w)

	var problem struct {
		Code   string `json:"code"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("unmarshal problem: %v", err)
	}
	if problem.Code != "rate_limited" {
		t.Fatalf("expected code 'rate_limited', got %s", problem.Code)
	}
	if retryAfter := w.Header().Get("Retry-After"); retryAfter != "30" {
		t.Errorf("Retry-After = %q, want the wait the guard reported", retryAfter)
	}
	// The use case never ran: a throttled login cannot authenticate anybody,
	// so it cannot hand out a session.
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("a refused login set %d cookies, want none", len(cookies))
	}
	if actions := guard.seen(); len(actions) != 1 || actions[0] != ratelimit.ActionAuthLogin {
		t.Errorf("guard saw %v, want just %q", actions, ratelimit.ActionAuthLogin)
	}
}

// TestSpoofedForwardedHeaderDoesNotMultiplyTheLoginBudget is the regression
// test for the defect this task replaced: the previous hook keyed its bucket on
// the first X-Forwarded-For entry, which the client writes, so rotating the
// header bought unlimited login attempts. With the platform policy and no
// trusted proxies configured, every one of those attempts is the same client.
func TestSpoofedForwardedHeaderDoesNotMultiplyTheLoginBudget(t *testing.T) {
	limiter := ratelimit.NewLimiter(ratelimit.Options{})
	harness := setupTestHarness(t, ratelimit.New(limiter, clientip.New(nil)))

	budget := 0
	for attempt := 0; attempt < 64; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"spoof@example.com","password":"Pass"}`))
		req.Header.Set("Content-Type", "application/json")
		// A different address every time, and a different peer port too.
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", attempt+1))
		req.RemoteAddr = fmt.Sprintf("198.51.100.7:%d", 40000+attempt)

		w := httptest.NewRecorder()
		harness.mux.ServeHTTP(w, req)

		if w.Code == http.StatusTooManyRequests {
			break
		}
		budget++
	}

	policy, _ := ratelimit.PolicyFor(ratelimit.ActionAuthLogin)
	if budget != policy.Address.Burst {
		t.Errorf("the spoofed header bought %d attempts, want the policy burst %d", budget, policy.Address.Burst)
	}
}

func assertCacheControlPrivateNoStore(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	cc := w.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") || !strings.Contains(cc, "private") {
		t.Fatalf("expected Cache-Control private, no-store, got: %q", cc)
	}
	if w.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("expected Pragma: no-cache, got: %q", w.Header().Get("Pragma"))
	}
}

func assertProblemContentType(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	ct := w.Header().Get("Content-Type")
	if ct != "application/problem+json" {
		t.Fatalf("expected Content-Type application/problem+json, got: %q", ct)
	}
}
