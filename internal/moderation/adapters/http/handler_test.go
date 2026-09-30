package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

const (
	modOwnerSession       = "mod-owner-session"
	modModeratorSession   = "mod-moderator-session"
	modStrangerSession    = "mod-stranger-session"
	modStaleSession       = "mod-stale-session"
	modNoFactorSession    = "mod-no-factor-session"
	modStaleFactorSession = "mod-stale-factor-session"
)

type moderationHarness struct {
	mux         http.Handler
	pool        *pgxpool.Pool
	ownerID     string
	moderatorID string
	staleModID  string
	arenaID     string
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustModAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) platformpg.AppAccount {
	t.Helper()
	acc, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return acc
}

func mustSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account pgtype.UUID, age time.Duration) string {
	t.Helper()
	var id pgtype.UUID
	now := time.Now().UTC()
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.sessions (account_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id`,
		account, []byte("hash-"+uuidString(account)), now.Add(-age), now.Add(time.Hour)).Scan(&id); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return uuidString(id)
}

// mustSessionWithSecondFactor seeds a session that presented a second factor
// factorAge ago. The factor age is deliberately independent from the session
// age: the administrative gate reads the factor, the high-impact rule reads the
// session, and the two are different facts (P16-T05).
func mustSessionWithSecondFactor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account pgtype.UUID, sessionAge, factorAge time.Duration, label string) string {
	t.Helper()
	var id pgtype.UUID
	now := time.Now().UTC()
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.sessions (account_id, token_hash, created_at, expires_at, mfa_verified_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		account, []byte("hash-mfa-"+label+"-"+uuidString(account)), now.Add(-sessionAge), now.Add(time.Hour), now.Add(-factorAge)).Scan(&id); err != nil {
		t.Fatalf("seed session with second factor: %v", err)
	}
	return uuidString(id)
}

func setupModerationHarness(t *testing.T) *moderationHarness {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustModAccount(t, ctx, q, "mod-http-owner@arena.example.com")
	moderator := mustModAccount(t, ctx, q, "mod-http-moderator@arena.example.com")
	stranger := mustModAccount(t, ctx, q, "mod-http-stranger@arena.example.com")
	staleMod := mustModAccount(t, ctx, q, "mod-http-stale@arena.example.com")

	if _, err := pool.Exec(ctx, `INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1, 'admin', $2)`, moderator.ID, moderator.ID); err != nil {
		t.Fatalf("grant moderator: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1, 'admin', $2)`, staleMod.ID, staleMod.ID); err != nil {
		t.Fatalf("grant stale moderator: %v", err)
	}

	var arenaID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Moderation HTTP probe statement', 'technology', 'pt-BR', 'draft')
		RETURNING id`, owner.ID).Scan(&arenaID); err != nil {
		t.Fatalf("seed arena: %v", err)
	}

	ownerSession := mustSession(t, ctx, pool, owner.ID, 0)
	strangerSession := mustSession(t, ctx, pool, stranger.ID, 0)

	// The moderator holds three sessions, because the two rules of the
	// administrative gate are about different facts. One has stepped up and is
	// fresh; one never presented a factor; one presented it outside the
	// window. The first is served, the other two are refused.
	moderatorSession := mustSessionWithSecondFactor(t, ctx, pool, moderator.ID, 0, 0, "moderator")
	noFactorSession := mustSession(t, ctx, pool, moderator.ID, 0)
	staleFactorSession := mustSessionWithSecondFactor(t, ctx, pool, moderator.ID, 0, time.Hour, "moderator-stale-factor")

	// The stale moderator stepped up just now inside an old session: the factor
	// is current, the session is not, which is what separates a low-impact
	// claim from a high-impact decision.
	staleSession := mustSessionWithSecondFactor(t, ctx, pool, staleMod.ID, time.Hour, 0, "stale")

	clock := clockseed.NewClock()
	authorizer, err := application.NewAuthorizer(repo, clock)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	fileReport, err := application.NewFileReportUseCase(application.FileReportDependencies{
		Targets: repo, Reports: repo, Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewFileReportUseCase: %v", err)
	}
	fileAppeal, err := application.NewFileAppealUseCase(application.AppealDependencies{
		Appeals: repo, Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewFileAppealUseCase: %v", err)
	}
	getQueue, err := application.NewGetCaseQueueUseCase(repo, repo)
	if err != nil {
		t.Fatalf("NewGetCaseQueueUseCase: %v", err)
	}
	claimCase, err := application.NewClaimCaseUseCase(application.ReviewDependencies{
		Cases: repo, Authorizer: authorizer, Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewClaimCaseUseCase: %v", err)
	}
	decideCase, err := application.NewDecideCaseUseCase(application.ReviewDependencies{
		Cases: repo, Authorizer: authorizer, Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewDecideCaseUseCase: %v", err)
	}
	codec, err := application.NewQueueCursorCodec([]byte("mod-http-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("build queue codec: %v", err)
	}

	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clockseed.NewClock(),
		Random:         clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("security manager: %v", err)
	}

	handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{
		FileReport:      fileReport,
		FileAppeal:      fileAppeal,
		GetQueue:        getQueue,
		ClaimCase:       claimCase,
		DecideCase:      decideCase,
		Roles:           repo,
		Sessions:        repo,
		QueueCodec:      codec,
		SecurityManager: secMgr,
		Clock:           clock,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case modOwnerSession:
			return security.AuthIdentity{AccountID: uuidString(owner.ID), SessionID: ownerSession}, nil
		case modModeratorSession:
			return security.AuthIdentity{AccountID: uuidString(moderator.ID), SessionID: moderatorSession}, nil
		case modStrangerSession:
			return security.AuthIdentity{AccountID: uuidString(stranger.ID), SessionID: strangerSession}, nil
		case modStaleSession:
			return security.AuthIdentity{AccountID: uuidString(staleMod.ID), SessionID: staleSession}, nil
		case modNoFactorSession:
			return security.AuthIdentity{AccountID: uuidString(moderator.ID), SessionID: noFactorSession}, nil
		case modStaleFactorSession:
			return security.AuthIdentity{AccountID: uuidString(moderator.ID), SessionID: staleFactorSession}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})

	return &moderationHarness{
		mux:         secMgr.AuthenticateMiddleware(validator)(mux),
		pool:        pool,
		ownerID:     uuidString(owner.ID),
		moderatorID: uuidString(moderator.ID),
		staleModID:  uuidString(staleMod.ID),
		arenaID:     uuidString(arenaID),
	}
}

func modAuthenticatedRequest(method, path, token, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	return request
}

func modDecodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return object
}

func assertModPrivateCacheHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	cacheControl := recorder.Header().Get("Cache-Control")
	for _, directive := range []string{"private", "no-store", "no-cache", "must-revalidate"} {
		if !strings.Contains(cacheControl, directive) {
			t.Fatalf("Cache-Control = %q, want %q (THR-CACHE-01)", cacheControl, directive)
		}
	}
	if pragma := recorder.Header().Get("Pragma"); pragma != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", pragma)
	}
}

func assertModExactKeys(t *testing.T, object map[string]any, want ...string) {
	t.Helper()
	if len(object) != len(want) {
		t.Fatalf("response keys = %v, want exactly %v", object, want)
	}
	for _, key := range want {
		if _, ok := object[key]; !ok {
			t.Fatalf("response is missing key %q (keys: %v)", key, object)
		}
	}
}

func TestModerationAPIRequiresAuthentication(t *testing.T) {
	harness := setupModerationHarness(t)

	targets := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/me/moderation/reports", `{"target_type":"arena","target_id":"x","reason":"spam"}`},
		{http.MethodPost, "/api/v1/me/moderation/appeals", `{"action_id":"x","context":"y"}`},
		{http.MethodGet, "/api/v1/moderation/cases", ""},
		{http.MethodPost, "/api/v1/moderation/cases/x/claim", ""},
		{http.MethodPost, "/api/v1/moderation/cases/x/decisions", `{"action":"warning","rule":"MOD-2:warning","justification":"z"}`},
	}
	for _, target := range targets {
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, httptest.NewRequest(target.method, target.path, strings.NewReader(target.body)))

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401 (body: %s)", target.method, target.path, recorder.Code, recorder.Body.String())
		}
		if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
			t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
		}
		assertModPrivateCacheHeaders(t, recorder)
	}
}

func TestModerationAdminRoutesDenyStrangers(t *testing.T) {
	harness := setupModerationHarness(t)

	targets := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/moderation/cases", ""},
		{http.MethodPost, "/api/v1/moderation/cases/x/claim", ""},
		{http.MethodPost, "/api/v1/moderation/cases/x/decisions", `{"action":"warning","rule":"MOD-2:warning","justification":"z"}`},
	}
	for _, target := range targets {
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(target.method, target.path, modStrangerSession, target.body))

		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s stranger status = %d, want 403 (body: %s)", target.method, target.path, recorder.Code, recorder.Body.String())
		}
		assertModPrivateCacheHeaders(t, recorder)
	}
}

// TestModerationAdminGateRequiresSecondFactor is the adapter-level proof of
// P16-T05: holding the capability is not enough, the calling session must have
// presented a second factor inside the step-up window. All three administrative
// routes are exercised, because a gate that covers only the queue would still
// let a non-elevated session decide a case.
// TestModerationGateFollowsTheRoleChangeImmediately is the privilege-change
// rule of P16-T06: the capability is read from the assignment on every request,
// so revoking it stops the operator's existing session at once. A gate that
// cached the role in the session would keep serving a person who no longer has
// the capability until that session expired, which is the window a revocation
// exists to close.
func TestModerationGateFollowsTheRoleChangeImmediately(t *testing.T) {
	harness := setupModerationHarness(t)

	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodGet, "/api/v1/moderation/cases", modModeratorSession, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("elevated moderator status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}

	if _, err := harness.pool.Exec(context.Background(),
		`UPDATE app.admin_roles SET revoked_at = now() WHERE account_id = $1`, harness.moderatorID); err != nil {
		t.Fatalf("revoke the assignment: %v", err)
	}

	// The same session, the same fresh factor: only the capability changed,
	// and that is enough.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodGet, "/api/v1/moderation/cases", modModeratorSession, ""))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("revoked moderator status = %d, want 403 (body: %s)", recorder.Code, recorder.Body.String())
	}
}

func TestModerationAdminGateRequiresSecondFactor(t *testing.T) {
	harness := setupModerationHarness(t)

	casePath := "/api/v1/moderation/cases/00000000-0000-0000-0000-000000000001"
	adminRoutes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/moderation/cases", ""},
		{http.MethodPost, casePath + "/claim", ""},
		{http.MethodPost, casePath + "/decisions", `{"action":"warning","rule":"MOD-2:warning","justification":"z"}`},
	}

	for _, target := range adminRoutes {
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(target.method, target.path, modNoFactorSession, target.body))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s without a second factor status = %d, want 403 (body: %s)", target.method, target.path, recorder.Code, recorder.Body.String())
		}
		assertModPrivateCacheHeaders(t, recorder)
		document := modDecodeObject(t, recorder.Body.Bytes())
		if document["code"] != "mfa_step_up_required" {
			t.Fatalf("%s %s refusal code = %v, want mfa_step_up_required (body: %s)", target.method, target.path, document["code"], recorder.Body.String())
		}
	}

	// A factor presented outside the window is refused like one that was never
	// presented: the difference is what the operator has to do, and neither
	// state is administrative access.
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodGet, "/api/v1/moderation/cases", modStaleFactorSession, ""))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("stale factor status = %d, want 403 (body: %s)", recorder.Code, recorder.Body.String())
	}
	staleDocument := modDecodeObject(t, recorder.Body.Bytes())
	if staleDocument["code"] != "mfa_step_up_required" {
		t.Fatalf("stale factor refusal code = %v, want mfa_step_up_required", staleDocument["code"])
	}

	// None of the refusals may describe the factor itself: the gate reports
	// that a step-up is needed, never the enrollment state of the account.
	for _, marker := range []string{"secret", "totp", "backup", "enrolled", "verified_at"} {
		if strings.Contains(strings.ToLower(recorder.Body.String()), marker) {
			t.Fatalf("refusal leaks factor state %q: %s", marker, recorder.Body.String())
		}
	}

	// The moderator whose session stepped up inside the window is served.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodGet, "/api/v1/moderation/cases", modModeratorSession, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("elevated moderator status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
}

func TestModerationReportFlowHidesEvidence(t *testing.T) {
	harness := setupModerationHarness(t)

	reportBody := `{"target_type":"arena","target_id":"` + harness.arenaID + `","reason":"spam","context":"Probe report context"}`
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodPost, "/api/v1/me/moderation/reports", modOwnerSession, reportBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("report status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertModPrivateCacheHeaders(t, recorder)

	document := modDecodeObject(t, recorder.Body.Bytes())
	assertModExactKeys(t, document, "report_id", "replayed", "rate_limited", "reports_in_window")
	serialized := recorder.Body.String()
	for _, marker := range []string{"Probe report context", "reporter", "justification", "moderation_reports"} {
		if strings.Contains(serialized, marker) {
			t.Fatalf("report response leaks restricted evidence %q: %s", marker, serialized)
		}
	}
}

func TestModerationQueueClaimDecideFlow(t *testing.T) {
	harness := setupModerationHarness(t)

	// File a report first (exercises the user route), then stage the case.
	reportBody := `{"target_type":"arena","target_id":"` + harness.arenaID + `","reason":"spam"}`
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodPost, "/api/v1/me/moderation/reports", modOwnerSession, reportBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("seed report status = %d (body: %s)", recorder.Code, recorder.Body.String())
	}

	// The queue starts empty: reports never auto-create cases.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodGet, "/api/v1/moderation/cases", modModeratorSession, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("queue status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertModPrivateCacheHeaders(t, recorder)
	document := modDecodeObject(t, recorder.Body.Bytes())
	assertModExactKeys(t, document, "items", "next_cursor")
	if items, _ := document["items"].([]any); len(items) != 0 {
		t.Fatalf("queue items = %d, want 0 before triage stages a case", len(items))
	}

	// Stage one open case for the reported arena.
	ctx := context.Background()
	var caseID pgtype.UUID
	if err := harness.pool.QueryRow(ctx, `
		INSERT INTO app.moderation_cases (target_type, target_arena_id)
		VALUES ('arena', $1)
		RETURNING id`, mustUUID(harness.arenaID)).Scan(&caseID); err != nil {
		t.Fatalf("stage case: %v", err)
	}
	casePath := "/api/v1/moderation/cases/" + uuidString(caseID)

	// Claim the case.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodPost, casePath+"/claim", modModeratorSession, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("claim status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertModPrivateCacheHeaders(t, recorder)
	claimDocument := modDecodeObject(t, recorder.Body.Bytes())
	assertModExactKeys(t, claimDocument, "case_id", "status", "claimed_by")

	// Decide with the least restrictive measure for the risk.
	decideBody := `{"action":"warning","rule":"MOD-2:warning","justification":"Measured warning with scope"}`
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodPost, casePath+"/decisions", modModeratorSession, decideBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("decide status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertModPrivateCacheHeaders(t, recorder)
	decisionDocument := modDecodeObject(t, recorder.Body.Bytes())
	assertModExactKeys(t, decisionDocument, "action_id", "case_id", "action")
	serialized := recorder.Body.String()
	for _, marker := range []string{"Measured warning", "justification", "moderation_actions"} {
		if strings.Contains(serialized, marker) {
			t.Fatalf("decision response leaks restricted evidence %q: %s", marker, serialized)
		}
	}

	// The queue now shows the decided case under its filter.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodGet, "/api/v1/moderation/cases?status=decided", modModeratorSession, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("filtered queue status = %d (body: %s)", recorder.Code, recorder.Body.String())
	}
	filtered := modDecodeObject(t, recorder.Body.Bytes())
	if items, _ := filtered["items"].([]any); len(items) != 1 {
		t.Fatalf("decided filter items = %d, want 1", len(items))
	}
}

func TestModerationStepUpDeniesStaleHighImpact(t *testing.T) {
	harness := setupModerationHarness(t)
	ctx := context.Background()

	// Stage a profile case owned by the reporter and claim it with the stale
	// session: the factor was presented just now, and claiming is low-impact,
	// so the aged session passes.
	var caseID pgtype.UUID
	if err := harness.pool.QueryRow(ctx, `
		INSERT INTO app.moderation_cases (target_type, target_account_id)
		VALUES ('profile', $1)
		RETURNING id`, mustUUID(harness.ownerID)).Scan(&caseID); err != nil {
		t.Fatalf("stage profile case: %v", err)
	}
	casePath := "/api/v1/moderation/cases/" + uuidString(caseID)

	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodPost, casePath+"/claim", modStaleSession, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("stale claim status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}

	// Deciding a suspension on the aged session denies: high-impact measures
	// require recent authentication, and a current second factor inside an old
	// session is not a substitute for it.
	decideBody := `{"action":"suspension","rule":"MOD-10:suspension","justification":"Stale high-impact attempt","expires_at":"2026-10-18T12:00:00Z"}`
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodPost, casePath+"/decisions", modStaleSession, decideBody))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("stale decide status = %d, want 401 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertModPrivateCacheHeaders(t, recorder)
}

func mustUUID(raw string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		panic(err)
	}
	return id
}

func TestModerationBodyLimitRejectsOversizedPayloads(t *testing.T) {
	harness := setupModerationHarness(t)

	oversized := `{"target_type":"arena","target_id":"` + harness.arenaID + `","reason":"spam","context":"` + strings.Repeat("x", 1<<20) + `"}`
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, modAuthenticatedRequest(http.MethodPost, "/api/v1/me/moderation/reports", modOwnerSession, oversized))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized status = %d, want 400 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
	}
	assertModPrivateCacheHeaders(t, recorder)
}
