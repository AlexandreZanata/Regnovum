package http_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	identityhttp "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/mfamechanism"
	identitypg "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/mfa"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// The second factor of the administrative surface (P16-T05), over the real
// transport and the real storage. What this file proves is what the routes
// promise and the unit tests cannot: that a caller without a session is refused
// before anything is generated, that the secret and the recovery codes are
// shown exactly once and appear in no other response, and that a code which was
// already spent is refused with the same shape whether it is replayed at
// confirmation, at the step-up or at the recovery.

const mfaHTTPToken = "mfa-http-session-token"

// steppingClock is a clock the test moves, so a code from the next time step
// can be produced without waiting thirty seconds.
type steppingClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *steppingClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *steppingClock) Advance(step time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(step)
}

// recordingMFAAudit counts the administrative facts the surface records.
type recordingMFAAudit struct {
	mu       sync.Mutex
	enrolled int
	recovery int
}

func (audit *recordingMFAAudit) RecordMFAEnrolled(context.Context, string, time.Time) error {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.enrolled++
	return nil
}

func (audit *recordingMFAAudit) RecordMFABackupCodeUsed(context.Context, string, time.Time) error {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.recovery++
	return nil
}

func (audit *recordingMFAAudit) counts() (int, int) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	return audit.enrolled, audit.recovery
}

type mfaHTTPHarness struct {
	mux http.Handler

	clock   *steppingClock
	audit   *recordingMFAAudit
	pool    *pgxpool.Pool
	account domain.AccountID
	session domain.SessionID
}

func newMFAHTTPHarness(t *testing.T) *mfaHTTPHarness {
	t.Helper()
	ctx := context.Background()

	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := identitypg.NewRepository(pool)

	email, err := domain.ParseEmail("mfa-http@arena.example.com")
	if err != nil {
		t.Fatalf("parse email: %v", err)
	}
	account, err := repo.CreateAccountWithPassword(ctx, email, "$argon2id$v=19$m=8192,t=1,p=1$fakeSalt$fakeHash")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	tokenHash := sha256.Sum256([]byte(mfaHTTPToken))
	session, err := repo.CreateSession(ctx, account.ID(), tokenHash[:], time.Now().UTC().Add(time.Hour), "198.51.100.9", "ArenaClient/1.0")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	clock := &steppingClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	sealer, err := mfa.NewSealer(make([]byte, mfa.KeySize), clockseed.NewRandom())
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	mechanism, err := mfamechanism.New(mfa.Config{}, sealer, clockseed.NewRandom())
	if err != nil {
		t.Fatalf("mfamechanism.New: %v", err)
	}
	hasher, err := argon2id.New(argon2id.FastParams(), rand.Reader)
	if err != nil {
		t.Fatalf("argon2id.New: %v", err)
	}
	audit := &recordingMFAAudit{}

	handler := identityhttp.NewHandler(identityhttp.HandlerConfig{
		SecurityManager:             mustSecurityManager(t),
		BeginMFAEnrollmentUseCase:   application.NewBeginMFAEnrollmentUseCase(repo, mechanism),
		ConfirmMFAEnrollmentUseCase: application.NewConfirmMFAEnrollmentUseCase(repo, mechanism, hasher, clock, audit),
		StepUpMFAUseCase:            application.NewStepUpMFAUseCase(repo, mechanism, clock),
		RecoverMFAUseCase:           application.NewRecoverMFAUseCase(repo, mechanism, hasher, clock, audit),
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		if rawToken != mfaHTTPToken {
			return security.AuthIdentity{}, errors.New("unknown session")
		}
		return security.AuthIdentity{
			AccountID: account.ID().String(),
			SessionID: session.ID().String(),
		}, nil
	})

	return &mfaHTTPHarness{
		mux:     mustSecurityManager(t).AuthenticateMiddleware(validator)(mux),
		clock:   clock,
		audit:   audit,
		pool:    pool,
		account: account.ID(),
		session: session.ID(),
	}
}

func mustSecurityManager(t *testing.T) *security.Manager {
	t.Helper()
	manager, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clockseed.NewClock(),
		Random:         clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("security manager: %v", err)
	}
	return manager
}

func mfaHTTPRequest(method, path, body string, authenticated bool) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authenticated {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: mfaHTTPToken})
	}
	return request
}

func mfaHTTPDo(t *testing.T, harness *mfaHTTPHarness, method, path, body string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, mfaHTTPRequest(method, path, body, authenticated))
	return recorder
}

func assertMFAPrivateCacheHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	cacheControl := recorder.Header().Get("Cache-Control")
	for _, directive := range []string{"private", "no-store", "no-cache", "must-revalidate"} {
		if !strings.Contains(cacheControl, directive) {
			t.Fatalf("Cache-Control = %q, want %q", cacheControl, directive)
		}
	}
}

func mfaHTTPObject(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &object); err != nil {
		t.Fatalf("decode body: %v (body: %s)", err, recorder.Body.String())
	}
	return object
}

func TestMFAEndpointsRequireASession(t *testing.T) {
	harness := newMFAHTTPHarness(t)

	targets := []struct{ path, body string }{
		{"/api/v1/me/mfa/enrollment", ""},
		{"/api/v1/me/mfa/enrollment/confirm", `{"code":"123456"}`},
		{"/api/v1/me/mfa/step-up", `{"code":"123456"}`},
		{"/api/v1/me/mfa/recovery", `{"code":"234567234567"}`},
	}
	for _, target := range targets {
		recorder := mfaHTTPDo(t, harness, http.MethodPost, target.path, target.body, false)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("POST %s without a session status = %d, want 401 (body: %s)", target.path, recorder.Code, recorder.Body.String())
		}
		if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
			t.Fatalf("POST %s Content-Type = %q, want application/problem+json", target.path, contentType)
		}
		assertMFAPrivateCacheHeaders(t, recorder)
	}

	// No session means no enrollment row was touched, either.
	var count int
	if err := harness.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app.account_mfa WHERE account_id = $1`, harness.account.String()).Scan(&count); err != nil {
		t.Fatalf("count enrollments: %v", err)
	}
	if count != 0 {
		t.Fatalf("enrollments after unauthenticated requests = %d, want 0", count)
	}
}

func TestMFAEnrollmentShowsTheSecretAndRotatesWhilePending(t *testing.T) {
	harness := newMFAHTTPHarness(t)

	recorder := mfaHTTPDo(t, harness, http.MethodPost, "/api/v1/me/mfa/enrollment", "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("begin enrollment status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertMFAPrivateCacheHeaders(t, recorder)
	document := mfaHTTPObject(t, recorder)
	if len(document) != 2 {
		t.Fatalf("enrollment keys = %v, want exactly secret and uri", document)
	}
	secretText, _ := document["secret"].(string)
	uri, _ := document["uri"].(string)
	if secretText == "" || !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("enrollment document = %v, want a secret and an otpauth URI", document)
	}
	secret, err := mfa.DecodeSecret(secretText)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	if len(secret) != mfa.DefaultSecretBytes {
		t.Fatalf("secret length = %d, want %d", len(secret), mfa.DefaultSecretBytes)
	}

	// Restarting while pending is allowed, and the second response carries the
	// new secret and nothing else either.
	restart := mfaHTTPDo(t, harness, http.MethodPost, "/api/v1/me/mfa/enrollment", "", true)
	if restart.Code != http.StatusOK {
		t.Fatalf("restart status = %d, want 200 (body: %s)", restart.Code, restart.Body.String())
	}
	restarted := mfaHTTPObject(t, restart)
	if _, ok := restarted["secret"]; !ok {
		t.Fatalf("restart document = %v, want a secret", restarted)
	}
	if restarted["secret"] == secretText {
		t.Fatalf("restart returned the same secret %q; a restart must issue a new one", secretText)
	}
}

func TestMFAConfirmationSpendsTheCodeAndReturnsRecoveryCodesOnce(t *testing.T) {
	harness := newMFAHTTPHarness(t)
	secret, enrollment := beginEnrollment(t, harness)

	// A code that is not the current one is refused, and the refusal says
	// nothing about the enrollment.
	wrong := mfaHTTPDo(t, harness, http.MethodPost, "/api/v1/me/mfa/enrollment/confirm", `{"code":"000000"}`, true)
	if wrong.Code != http.StatusForbidden {
		t.Fatalf("wrong code status = %d, want 403 (body: %s)", wrong.Code, wrong.Body.String())
	}
	if strings.Contains(wrong.Body.String(), "backup") {
		t.Fatalf("a failed confirmation returned recovery material: %s", wrong.Body.String())
	}

	code := codeAt(t, secret, harness.clock.Now())
	recorder := mfaHTTPDo(t, harness, http.MethodPost, "/api/v1/me/mfa/enrollment/confirm", `{"code":"`+code+`"}`, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("confirmation status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertMFAPrivateCacheHeaders(t, recorder)
	document := mfaHTTPObject(t, recorder)
	codes, _ := document["backup_codes"].([]any)
	if len(codes) != mfa.DefaultBackupCodes {
		t.Fatalf("backup codes = %d, want %d (document: %v)", len(codes), mfa.DefaultBackupCodes, document)
	}
	if secretText, _ := enrollment["secret"].(string); strings.Contains(recorder.Body.String(), secretText) {
		t.Fatalf("the confirmation response repeated the shared secret: %s", recorder.Body.String())
	}

	// The code that confirmed the enrollment is spent: replaying it at the
	// step-up is a replay, not a wrong guess, and the session stays
	// un-elevated.
	replay := mfaHTTPDo(t, harness, http.MethodPost, "/api/v1/me/mfa/step-up", `{"code":"`+code+`"}`, true)
	if replay.Code != http.StatusForbidden {
		t.Fatalf("replayed step-up status = %d, want 403 (body: %s)", replay.Code, replay.Body.String())
	}
	if replayDocument := mfaHTTPObject(t, replay); replayDocument["code"] != "mfa_code_replayed" {
		t.Fatalf("replayed step-up code = %v, want mfa_code_replayed", replayDocument["code"])
	}
	if elevated := sessionElevated(t, harness); elevated {
		t.Fatal("a replayed step elevated the session")
	}

	// One time step later the authenticator shows a new code, and that one
	// elevates the session.
	harness.clock.Advance(mfa.DefaultPeriod)
	stepUp := mfaHTTPDo(t, harness, http.MethodPost, "/api/v1/me/mfa/step-up", `{"code":"`+codeAt(t, secret, harness.clock.Now())+`"}`, true)
	if stepUp.Code != http.StatusOK {
		t.Fatalf("step-up status = %d, want 200 (body: %s)", stepUp.Code, stepUp.Body.String())
	}
	assertMFAPrivateCacheHeaders(t, stepUp)
	if !sessionElevated(t, harness) {
		t.Fatal("the step-up did not elevate the session")
	}
	if enrolled, _ := harness.audit.counts(); enrolled != 1 {
		t.Fatalf("recorded enrollments = %d, want 1", enrolled)
	}
}

func TestMFARecoverySpendsTheCodeOnceAndRecordsIt(t *testing.T) {
	harness := newMFAHTTPHarness(t)
	secret, _ := beginEnrollment(t, harness)
	recorder := mfaHTTPDo(t, harness, http.MethodPost, "/api/v1/me/mfa/enrollment/confirm", `{"code":"`+codeAt(t, secret, harness.clock.Now())+`"}`, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("confirmation status = %d (body: %s)", recorder.Code, recorder.Body.String())
	}
	document := mfaHTTPObject(t, recorder)
	rawCodes, _ := document["backup_codes"].([]any)
	if len(rawCodes) == 0 {
		t.Fatal("no recovery codes were issued")
	}
	recoveryCode, _ := rawCodes[0].(string)

	recovery := mfaHTTPDo(t, harness, http.MethodPost, "/api/v1/me/mfa/recovery", `{"code":"`+recoveryCode+`"}`, true)
	if recovery.Code != http.StatusOK {
		t.Fatalf("recovery status = %d, want 200 (body: %s)", recovery.Code, recovery.Body.String())
	}
	assertMFAPrivateCacheHeaders(t, recovery)
	if !sessionElevated(t, harness) {
		t.Fatal("the recovery did not elevate the session")
	}
	if _, recorded := harness.audit.counts(); recorded != 1 {
		t.Fatalf("recorded recoveries = %d, want 1", recorded)
	}

	// The same code, presented again, is refused like an unknown one.
	reuse := mfaHTTPDo(t, harness, http.MethodPost, "/api/v1/me/mfa/recovery", `{"code":"`+recoveryCode+`"}`, true)
	if reuse.Code != http.StatusForbidden {
		t.Fatalf("reused recovery code status = %d, want 403 (body: %s)", reuse.Code, reuse.Body.String())
	}
	if reuseDocument := mfaHTTPObject(t, reuse); reuseDocument["code"] != "mfa_code_invalid" {
		t.Fatalf("reused recovery code problem = %v, want mfa_code_invalid", reuseDocument["code"])
	}
	if _, recorded := harness.audit.counts(); recorded != 1 {
		t.Fatalf("recorded recoveries after a reuse = %d, want 1", recorded)
	}
}

func TestMFAEndpointsRejectBodiesTheyCannotRead(t *testing.T) {
	harness := newMFAHTTPHarness(t)

	for _, path := range []string{"/api/v1/me/mfa/enrollment/confirm", "/api/v1/me/mfa/step-up", "/api/v1/me/mfa/recovery"} {
		recorder := mfaHTTPDo(t, harness, http.MethodPost, path, "not-json", true)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("POST %s with a malformed body status = %d, want 400 (body: %s)", path, recorder.Code, recorder.Body.String())
		}
		if problem := mfaHTTPObject(t, recorder); problem["code"] != "invalid_json" {
			t.Fatalf("POST %s problem = %v, want invalid_json", path, problem["code"])
		}
		assertMFAPrivateCacheHeaders(t, recorder)
	}
}

// beginEnrollment runs the enrollment route and returns the decoded secret and
// the response document.
func beginEnrollment(t *testing.T, harness *mfaHTTPHarness) ([]byte, map[string]any) {
	t.Helper()
	recorder := mfaHTTPDo(t, harness, http.MethodPost, "/api/v1/me/mfa/enrollment", "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("begin enrollment status = %d (body: %s)", recorder.Code, recorder.Body.String())
	}
	document := mfaHTTPObject(t, recorder)
	secretText, _ := document["secret"].(string)
	secret, err := mfa.DecodeSecret(secretText)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	return secret, document
}

// codeAt is what an authenticator shows at an instant, computed with the same
// parameters the mechanism verifies with.
func codeAt(t *testing.T, secret []byte, instant time.Time) string {
	t.Helper()
	code, err := mfa.Config{}.Code(secret, instant)
	if err != nil {
		t.Fatalf("compute code: %v", err)
	}
	return code
}

// sessionElevated reports whether the harness session crossed the step-up, read
// from the row rather than from the response, so a 200 without the fact would
// be caught.
func sessionElevated(t *testing.T, harness *mfaHTTPHarness) bool {
	t.Helper()
	var verified *time.Time
	if err := harness.pool.QueryRow(context.Background(),
		`SELECT mfa_verified_at FROM app.sessions WHERE id = $1`, harness.session.String()).Scan(&verified); err != nil {
		t.Fatalf("read session elevation: %v", err)
	}
	return verified != nil
}
