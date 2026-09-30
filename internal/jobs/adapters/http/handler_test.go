package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	auditrepo "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/auditbridge"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/operatorbridge"
	jobsrepo "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	moderationrepo "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// The session tokens the validator recognises. Only the operator holds an
// administrative assignment, and only one of the two operator sessions is
// fresh.
const (
	sessionOperator      = "jobs-operator-session"
	sessionStaleOperator = "jobs-stale-operator-session"
	sessionModerator     = "jobs-moderator-session"
	sessionStranger      = "jobs-stranger-session"
)

// payloadSecret is the value seeded inside one dead job's payload. It must
// never appear in a response body or in an audit row.
const payloadSecret = "K7QP-2M4Z-9RTX"

type jobsHarness struct {
	mux          http.Handler
	pool         *pgxpool.Pool
	operatorID   string
	deadJobID    string
	schedulerKey string
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

func mustAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) platformpg.AppAccount {
	t.Helper()
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return account
}

func mustSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account pgtype.UUID, age time.Duration) string {
	t.Helper()
	var id pgtype.UUID
	now := time.Now().UTC()
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.sessions (account_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id`,
		account, []byte("jobs-hash-"+uuidString(account)), now.Add(-age), now.Add(time.Hour)).Scan(&id); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return uuidString(id)
}

func mustDeadJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, raw map[string]any) string {
	t.Helper()
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.jobs (type, version, parameters, state, available_at, attempts, max_attempts,
		                      last_error_code, last_error_detail, created_at, updated_at)
		VALUES ('email_delivery', 1, $1::jsonb, 'dead', now() - interval '2 hours', 5, 5,
		        'JOB_HANDLER_ERROR', 'provider unavailable',
		        now() - interval '2 hours', now() - interval '2 hours')
		RETURNING id`, string(body)).Scan(&id); err != nil {
		t.Fatalf("seed dead job: %v", err)
	}
	return uuidString(id)
}

func setupJobsHarness(t *testing.T) *jobsHarness {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)
	repo := jobsrepo.NewRepository(pool)

	operator := mustAccount(t, ctx, q, "jobs-http-operator@arena.example.com")
	stale := mustAccount(t, ctx, q, "jobs-http-stale@arena.example.com")
	moderator := mustAccount(t, ctx, q, "jobs-http-moderator@arena.example.com")
	stranger := mustAccount(t, ctx, q, "jobs-http-stranger@arena.example.com")

	for _, holder := range []platformpg.AppAccount{operator, stale, moderator} {
		role := "moderator"
		if holder.ID == operator.ID || holder.ID == stale.ID {
			role = "admin"
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1, $2, $3)`,
			holder.ID, role, operator.ID); err != nil {
			t.Fatalf("grant role: %v", err)
		}
	}

	operatorSession := mustSession(t, ctx, pool, operator.ID, 0)
	staleSession := mustSession(t, ctx, pool, stale.ID, time.Hour)
	moderatorSession := mustSession(t, ctx, pool, moderator.ID, 0)
	strangerSession := mustSession(t, ctx, pool, stranger.ID, 0)

	// One dead job whose payload carries a secret, and one that is queued so
	// the retry can be refused for the right reason.
	deadJobID := mustDeadJob(t, ctx, pool, map[string]any{
		"template": "verification", "locale": "pt-BR",
		"recipient": "ana.silva@example.com", "code": payloadSecret,
	})
	queuedID := mustDeadJob(t, ctx, pool, map[string]any{"period": "2026-09"})
	if _, err := pool.Exec(ctx, `UPDATE app.jobs SET state = 'queued', attempts = 0 WHERE id = $1::uuid`, queuedID); err != nil {
		t.Fatalf("requeue the second job: %v", err)
	}
	schedulerKey := "schedule:email_delivery:2026-09"

	clock := clockseed.NewClock()
	health, err := jobsapp.NewGetQueueHealthUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewGetQueueHealthUseCase: %v", err)
	}
	list, err := jobsapp.NewListDeadJobsUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewListDeadJobsUseCase: %v", err)
	}
	operators, err := operatorbridge.NewDirectory(moderationrepo.NewRepository(pool))
	if err != nil {
		t.Fatalf("operator bridge: %v", err)
	}
	audit, err := auditbridge.NewRecorder(auditrepo.NewRepository(pool))
	if err != nil {
		t.Fatalf("audit bridge: %v", err)
	}
	retry, err := jobsapp.NewRetryJobUseCase(repo, repo, audit, platformpg.NewTxManager(pool), clock)
	if err != nil {
		t.Fatalf("NewRetryJobUseCase: %v", err)
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
		Health:    health,
		ListDead:  list,
		Retry:     retry,
		Operators: operators,
		Sessions:  repo,
		Security:  secMgr,
		Clock:     clock,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case sessionOperator:
			return security.AuthIdentity{AccountID: uuidString(operator.ID), SessionID: operatorSession}, nil
		case sessionStaleOperator:
			return security.AuthIdentity{AccountID: uuidString(stale.ID), SessionID: staleSession}, nil
		case sessionModerator:
			return security.AuthIdentity{AccountID: uuidString(moderator.ID), SessionID: moderatorSession}, nil
		case sessionStranger:
			return security.AuthIdentity{AccountID: uuidString(stranger.ID), SessionID: strangerSession}, nil
		default:
			return security.AuthIdentity{}, fmt.Errorf("security: unknown session")
		}
	})
	// The platform middleware resolves the session cookie into the identity
	// the handler reads, so the test exercises the same path production does.
	wrapped := secMgr.AuthenticateMiddleware(validator)(mux)
	return &jobsHarness{
		mux:          wrapped,
		pool:         pool,
		operatorID:   uuidString(operator.ID),
		deadJobID:    deadJobID,
		schedulerKey: schedulerKey,
	}
}

// request issues one call with the given session token (empty means anonymous).
func (h *jobsHarness) request(t *testing.T, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func (h *jobsHarness) auditCount(t *testing.T, action string) int {
	t.Helper()
	var total int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app.audit_events WHERE action = $1`, action).Scan(&total); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return total
}

// TestOperationalSurfaceDeniesAnonymousCallers covers the first gate: nothing
// on this surface is readable or actionable without authentication.
func TestOperationalSurfaceDeniesAnonymousCallers(t *testing.T) {
	built := setupJobsHarness(t)
	for _, testCase := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/admin/jobs/health", ""},
		{http.MethodGet, "/api/v1/admin/jobs/dead", ""},
		{http.MethodPost, "/api/v1/admin/jobs/" + built.deadJobID + "/retry", `{"reason":"testing"}`},
	} {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			recorder := built.request(t, testCase.method, testCase.path, testCase.body, "")
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", recorder.Code)
			}
			assertNoStore(t, recorder)
			assertProblemCode(t, recorder, "unauthorized")
		})
	}
	if built.auditCount(t, domain.ActionRetryDeadJob) != 0 {
		t.Error("an anonymous call left an audit row")
	}
}

// TestOperationalSurfaceRequiresTheAdministrativeRole: an authenticated account
// without the assignment is refused, and a moderator is refused too — triaging
// content is not operating the queue.
func TestOperationalSurfaceRequiresTheAdministrativeRole(t *testing.T) {
	built := setupJobsHarness(t)
	for _, token := range []string{sessionStranger, sessionModerator} {
		recorder := built.request(t, http.MethodGet, "/api/v1/admin/jobs/health", "", token)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 for %s", recorder.Code, token)
		}
		assertNoStore(t, recorder)
		assertProblemCode(t, recorder, "forbidden")
	}
	recorder := built.request(t, http.MethodPost, "/api/v1/admin/jobs/"+built.deadJobID+"/retry", `{"reason":"testing"}`, sessionStranger)
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", recorder.Code)
	}
	if built.auditCount(t, domain.ActionRetryDeadJob) != 0 {
		t.Error("a denied caller left an audit row")
	}
}

// TestRetryRequiresRecentAuthentication is the step-up requirement measured end
// to end: the same operator, the same assignment, an aged session.
func TestRetryRequiresRecentAuthentication(t *testing.T) {
	built := setupJobsHarness(t)
	recorder := built.request(t, http.MethodPost, "/api/v1/admin/jobs/"+built.deadJobID+"/retry",
		`{"reason":"provider outage resolved"}`, sessionStaleOperator)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	assertNoStore(t, recorder)
	assertProblemCode(t, recorder, "step_up_required")

	// Reading the queue is not a sensitive action: the same stale session may
	// look, and only acting requires freshness.
	read := built.request(t, http.MethodGet, "/api/v1/admin/jobs/health", "", sessionStaleOperator)
	if read.Code != http.StatusOK {
		t.Errorf("health status = %d, want 200 for a stale session", read.Code)
	}
	if built.auditCount(t, domain.ActionRetryDeadJob) != 0 {
		t.Error("a refused retry left an audit row")
	}
	if state := built.jobState(t, built.deadJobID); state != "dead" {
		t.Errorf("job state = %q, want it untouched", state)
	}
}

// TestHealthAnswersCountsAndLag proves the read surface, and that it carries no
// job content.
func TestHealthAnswersCountsAndLag(t *testing.T) {
	built := setupJobsHarness(t)
	recorder := built.request(t, http.MethodGet, "/api/v1/admin/jobs/health", "", sessionOperator)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	assertNoStore(t, recorder)
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("content type = %q", contentType)
	}
	var document struct {
		GeneratedAt string `json:"generated_at"`
		Queue       struct {
			Queued            int64 `json:"queued"`
			Dead              int64 `json:"dead"`
			DueNow            int64 `json:"due_now"`
			LagSeconds        int64 `json:"lag_seconds"`
			OldestDeadSeconds int64 `json:"oldest_dead_seconds"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if document.Queue.Dead != 1 || document.Queue.Queued != 1 || document.Queue.DueNow != 1 {
		t.Errorf("queue = %+v, want the seeded rows", document.Queue)
	}
	if document.Queue.OldestDeadSeconds <= 0 {
		t.Errorf("oldest dead = %d, want the age of the dead row", document.Queue.OldestDeadSeconds)
	}
	if strings.Contains(recorder.Body.String(), payloadSecret) {
		t.Error("the health answer carries a job payload")
	}
}

// TestDeadListingNeverCarriesAPayload is the privacy rule of the surface.
func TestDeadListingNeverCarriesAPayload(t *testing.T) {
	built := setupJobsHarness(t)
	recorder := built.request(t, http.MethodGet, "/api/v1/admin/jobs/dead", "", sessionOperator)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	assertNoStore(t, recorder)
	body := recorder.Body.String()
	for _, secret := range []string{payloadSecret, "ana.silva@example.com", "parameters"} {
		if strings.Contains(body, secret) {
			t.Errorf("the listing carries %q: %s", secret, body)
		}
	}
	var page struct {
		Total int64 `json:"total"`
		Items []struct {
			JobID         string `json:"job_id"`
			Type          string `json:"type"`
			Attempts      int    `json:"attempts"`
			LastErrorCode string `json:"last_error_code"`
			AgeSeconds    int64  `json:"age_seconds"`
			Retryable     bool   `json:"retryable"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page = %+v, want the single dead job", page)
	}
	item := page.Items[0]
	if item.JobID != built.deadJobID || item.Type != string(domain.TypeEmailDelivery) {
		t.Errorf("item = %+v, want the seeded dead job", item)
	}
	if item.LastErrorCode != string(domain.FailureHandlerError) || !item.Retryable || item.AgeSeconds <= 0 {
		t.Errorf("item = %+v, want the stable code, the allowlist flag and the age", item)
	}

	// The limit is bounded, and an unusable value is a validation problem.
	recorder = built.request(t, http.MethodGet, "/api/v1/admin/jobs/dead?limit=0", "", sessionOperator)
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for limit 0", recorder.Code)
	}
	recorder = built.request(t, http.MethodGet, "/api/v1/admin/jobs/dead?limit=abc", "", sessionOperator)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unusable limit", recorder.Code)
	}
	assertNoStore(t, recorder)
}

// TestRetryRequeuesAndAudits is the positive path, measured against the
// database: the row moves, the trail records who did it and why, and neither
// carries the payload.
func TestRetryRequeuesAndAudits(t *testing.T) {
	built := setupJobsHarness(t)
	recorder := built.request(t, http.MethodPost, "/api/v1/admin/jobs/"+built.deadJobID+"/retry",
		`{"reason":"provider outage resolved"}`, sessionOperator)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	assertNoStore(t, recorder)
	var document struct {
		JobID string `json:"job_id"`
		Type  string `json:"type"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if document.JobID != built.deadJobID || document.State != string(domain.StateQueued) {
		t.Errorf("retry = %+v, want the requeued job", document)
	}
	if state := built.jobState(t, built.deadJobID); state != "queued" {
		t.Errorf("job state = %q, want queued", state)
	}
	var attempts, maxAttempts int
	if err := built.pool.QueryRow(context.Background(),
		`SELECT attempts, max_attempts FROM app.jobs WHERE id = $1::uuid`, built.deadJobID).Scan(&attempts, &maxAttempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 0 {
		t.Errorf("attempts = %d, want a renewed budget", attempts)
	}

	if built.auditCount(t, domain.ActionRetryDeadJob) != 1 {
		t.Fatalf("audit rows = %d, want one", built.auditCount(t, domain.ActionRetryDeadJob))
	}
	var actor, targetType, targetID, reason string
	var metadata []byte
	if err := built.pool.QueryRow(context.Background(),
		`SELECT actor_id::text, target_type, target_id, reason_code, metadata::text
		 FROM app.audit_events WHERE action = $1`, domain.ActionRetryDeadJob).
		Scan(&actor, &targetType, &targetID, &reason, &metadata); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if actor != built.operatorID {
		t.Errorf("actor = %q, want the authenticated operator %q", actor, built.operatorID)
	}
	if targetType != "operation" || targetID != built.deadJobID {
		t.Errorf("target = %s/%s, want the operation and the job", targetType, targetID)
	}
	// The trail's reason column is a stable code and never the operator's
	// prose; the sentence is metadata, which is what keeps the column
	// greppable and the claim attributable.
	if reason != domain.ReasonCodeDeadJobRetry {
		t.Errorf("reason code = %q, want the module's stable code", reason)
	}
	if !strings.Contains(string(metadata), "provider outage resolved") {
		t.Errorf("audit metadata = %s, want the operator's stated reason", metadata)
	}
	if strings.Contains(string(metadata), payloadSecret) || strings.Contains(string(metadata), "ana.silva@example.com") {
		t.Errorf("audit metadata carries job content: %s", metadata)
	}
	if !strings.Contains(string(metadata), `"new_status": "queued"`) && !strings.Contains(string(metadata), `"new_status":"queued"`) {
		t.Errorf("audit metadata = %s, want the recorded transition", metadata)
	}
}

// TestRetryRefusesWhatItMust keeps the refusals honest and non-destructive.
func TestRetryRefusesWhatItMust(t *testing.T) {
	built := setupJobsHarness(t)
	queued := built.queuedJobID(t)
	for _, testCase := range []struct {
		name       string
		jobID      string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"missing reason", built.deadJobID, `{"reason":"  "}`, http.StatusBadRequest, "reason_required"},
		{"malformed body", built.deadJobID, `{`, http.StatusBadRequest, "invalid_body"},
		{"job that is not dead", queued, `{"reason":"testing"}`, http.StatusConflict, "job_not_dead"},
		{"unknown job", "0191f0e0-0000-7000-8000-0000000000ff", `{"reason":"testing"}`, http.StatusNotFound, "job_not_found"},
		{"identifier that cannot be a job", "not-a-uuid", `{"reason":"testing"}`, http.StatusNotFound, "job_not_found"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			before := built.auditCount(t, domain.ActionRetryDeadJob)
			recorder := built.request(t, http.MethodPost, "/api/v1/admin/jobs/"+testCase.jobID+"/retry",
				testCase.body, sessionOperator)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			assertNoStore(t, recorder)
			assertProblemCode(t, recorder, testCase.wantCode)
			if recorder.Header().Get("Content-Type") != "application/problem+json" {
				t.Errorf("content type = %q, want a problem document", recorder.Header().Get("Content-Type"))
			}
			if after := built.auditCount(t, domain.ActionRetryDeadJob); after != before {
				t.Errorf("audit rows = %d, want the refused retry unrecorded (%d)", after, before)
			}
			if strings.Contains(recorder.Body.String(), payloadSecret) {
				t.Error("a problem document carries the job payload")
			}
		})
	}
	if state := built.jobState(t, built.deadJobID); state != "dead" {
		t.Errorf("job state = %q, want it untouched by the refusals", state)
	}
}

func (h *jobsHarness) jobState(t *testing.T, jobID string) string {
	t.Helper()
	var state string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT state FROM app.jobs WHERE id = $1::uuid`, jobID).Scan(&state); err != nil {
		t.Fatalf("read job state: %v", err)
	}
	return state
}

func (h *jobsHarness) queuedJobID(t *testing.T) string {
	t.Helper()
	var id pgtype.UUID
	if err := h.pool.QueryRow(context.Background(),
		`SELECT id FROM app.jobs WHERE state = 'queued' LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read queued job: %v", err)
	}
	return uuidString(id)
}

func assertNoStore(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	cacheControl := recorder.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store on every answer", cacheControl)
	}
	if recorder.Header().Get("Pragma") != "no-cache" {
		t.Errorf("Pragma = %q, want no-cache", recorder.Header().Get("Pragma"))
	}
}

func assertProblemCode(t *testing.T, recorder *httptest.ResponseRecorder, want string) {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, recorder.Body.String())
	}
	if problem.Code != want {
		t.Errorf("problem code = %q, want %q", problem.Code, want)
	}
}
