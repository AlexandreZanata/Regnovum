// Tests of the guarded job operation surface composed in the process
// (P49-T09): queue health, dead listing and single retry run on the platform
// mux over disposable PostgreSQL, driven by real HTTP with the same pool and
// security boundary as the account journey — the session the JSON login
// opened is the session the operator routes require, and no second
// authentication exists.
//
// Reading requires an active administrative assignment; retrying additionally
// requires a recently stepped-up session resolved from the server-observed
// session age, and the retry and its audit record commit in one transaction.
// No payload ever serializes, no DSN travels, and staged modules stay
// unmounted.
package bootstrap_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// jobsPayloadSecret is seeded inside one dead job's payload. It must never
// appear in a response body or in an audit row.
const jobsPayloadSecret = "K7QP-2M4Z-9RTX"

// jobsJourney is the account plus job operation surfaces on a real listener,
// sharing one pool and one security boundary like `arena server` does.
type jobsJourney struct {
	account *bootstrap.AccountSurface
	jobs    *bootstrap.JobsSurface
	server  *httptest.Server
	pool    *pgxpool.Pool
}

// newJobsJourney composes both surfaces the way the process does.
func newJobsJourney(t *testing.T) *jobsJourney {
	t.Helper()

	database := dbtest.New(t)
	pool := database.Pool.Pool()
	clock := clockseed.NewClock()
	random := clockseed.NewRandom()
	manager, err := security.New(security.Options{Env: config.EnvTest, Clock: clock, Random: random})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}
	base := func() bootstrap.Options {
		return bootstrap.Options{
			Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
			Pool: pool, Clock: clock, Random: random, Assets: manifestFixture(t), Security: manager,
		}
	}
	account, err := bootstrap.ComposeAccount(base())
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}
	jobs, err := bootstrap.ComposeJobs(base())
	if err != nil {
		t.Fatalf("ComposeJobs() error = %v", err)
	}
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{},
		[]httpserver.Surface{account.Surface(), jobs.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &jobsJourney{account: account, jobs: jobs, server: server, pool: pool}
}

// jobsLogin registers, verifies and signs in one account over the JSON API,
// returning the client holding its session plus the account identifier.
func jobsLogin(t *testing.T, journey *jobsJourney, email, password string) (*http.Client, string) {
	t.Helper()

	client := browser(t)
	status, _, _ := jsonPost(t, client, journey.server, "/api/v1/auth/register", `{"email":`+quoteJSON(email)+`,"password":`+quoteJSON(password)+`}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /api/v1/auth/register status = %d, want 201", status)
	}
	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	token, found := journey.account.LocalSink().LastTokenForEmail(address)
	if !found {
		t.Fatalf("registration of %s issued no token", email)
	}
	if status, _ := jsonGet(t, client, journey.server, "/api/v1/auth/verify?token="+token); status != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/verify status = %d, want 200", status)
	}
	status, raw, _ := jsonPost(t, client, journey.server, "/api/v1/auth/login", `{"email":`+quoteJSON(email)+`,"password":`+quoteJSON(password)+`}`)
	if status != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/login status = %d, want 200 (body: %.200s)", status, raw)
	}
	var document map[string]string
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("login is not JSON: %v", err)
	}
	return client, document["account_id"]
}

// jobsGet issues one authenticated GET and returns status, body and headers.
func jobsGet(t *testing.T, client *http.Client, server *httptest.Server, path string) (int, []byte, http.Header) {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	return response.StatusCode, raw, response.Header
}

// jobsPost issues one authenticated JSON POST and returns status, body and
// headers.
func jobsPost(t *testing.T, client *http.Client, server *httptest.Server, path, body string) (int, []byte, http.Header) {
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
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read POST %s: %v", path, err)
	}
	return response.StatusCode, raw, response.Header
}

// grantJobsAdmin records an active administrative assignment for the account.
// The assignment is seeded here the way the host-only bootstrap would write
// it: no HTTP route can grant it.
func grantJobsAdmin(t *testing.T, pool *pgxpool.Pool, accountID string) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1::uuid, 'admin', $1::uuid)`,
		accountID); err != nil {
		t.Fatalf("grant admin: %v", err)
	}
}

// grantJobsRole records an active assignment of the given role.
func grantJobsRole(t *testing.T, pool *pgxpool.Pool, accountID, role string) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1::uuid, $2, $1::uuid)`,
		accountID, role); err != nil {
		t.Fatalf("grant role %s: %v", role, err)
	}
}

// ageJobsSession moves the account's latest session creation back by the
// given interval text, so the server-observed session age proves the step-up
// rule without touching the factor ceremony.
func ageJobsSession(t *testing.T, pool *pgxpool.Pool, accountID, interval string) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`UPDATE app.sessions SET created_at = now() - ($1::interval), expires_at = now() + interval '1 hour'
		 WHERE id = (SELECT id FROM app.sessions WHERE account_id = $2::uuid ORDER BY created_at DESC LIMIT 1)`,
		interval, accountID); err != nil {
		t.Fatalf("age session: %v", err)
	}
}

// seedJobsDead inserts one dead email-delivery job carrying a secret payload
// and returns its identifier.
func seedJobsDead(t *testing.T, pool *pgxpool.Pool, payload string) string {
	t.Helper()

	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO app.jobs (type, version, parameters, state, available_at, attempts, max_attempts,
		                      last_error_code, last_error_detail, created_at, updated_at)
		VALUES ('email_delivery', 1, $1::jsonb, 'dead', now() - interval '2 hours', 5, 5,
		        'JOB_HANDLER_ERROR', 'provider unavailable',
		        now() - interval '2 hours', now() - interval '2 hours')
		RETURNING id::text`, payload).Scan(&id); err != nil {
		t.Fatalf("seed dead job: %v", err)
	}
	return id
}

// jobsState reads the lifecycle state of one job row.
func jobsState(t *testing.T, pool *pgxpool.Pool, jobID string) string {
	t.Helper()

	var state string
	if err := pool.QueryRow(context.Background(),
		`SELECT state FROM app.jobs WHERE id = $1::uuid`, jobID).Scan(&state); err != nil {
		t.Fatalf("read job state: %v", err)
	}
	return state
}

// jobsAuditCount counts the recorded operator retries.
func jobsAuditCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var total int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app.audit_events WHERE action = $1`, jobsdomain.ActionRetryDeadJob).Scan(&total); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return total
}

// assertJobsPrivate requires the restricted cache policy on every answer.
func assertJobsPrivate(t *testing.T, header http.Header) {
	t.Helper()

	if !strings.Contains(header.Get("Cache-Control"), "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", header.Get("Cache-Control"))
	}
}

// assertJobsProblem requires the stable RFC 9457 code.
func assertJobsProblem(t *testing.T, raw []byte, want string) {
	t.Helper()

	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem: %v (%.300s)", err, raw)
	}
	if problem.Code != want {
		t.Fatalf("problem code = %q, want %q (%.300s)", problem.Code, want, raw)
	}
}

// TestJobsHealthAndDeadServeForOperator proves the read surface: an operator
// with a fresh session reads counts and the dead page, the listing carries
// lifecycle columns only, and an unusable limit is a validation problem.
func TestJobsHealthAndDeadServeForOperator(t *testing.T) {
	t.Parallel()

	journey := newJobsJourney(t)
	operator, operatorID := jobsLogin(t, journey, "jobs-operator-t09@example.test", "Correct Horse 11!")
	grantJobsAdmin(t, journey.pool, operatorID)
	jobID := seedJobsDead(t, journey.pool, `{"template":"verification","locale":"pt-BR","recipient":"ana.silva@example.com","code":"`+jobsPayloadSecret+`"}`)

	status, raw, header := jobsGet(t, operator, journey.server, "/api/v1/admin/jobs/health")
	if status != http.StatusOK {
		t.Fatalf("GET health status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertJobsPrivate(t, header)
	var health struct {
		Queue struct {
			Dead int64 `json:"dead"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(raw, &health); err != nil {
		t.Fatalf("health is not JSON: %v", err)
	}
	if health.Queue.Dead < 1 {
		t.Fatalf("health dead = %d, want the seeded row", health.Queue.Dead)
	}
	if strings.Contains(string(raw), jobsPayloadSecret) || strings.Contains(string(raw), "ana.silva@example.com") {
		t.Fatal("health carries job content")
	}

	status, raw, header = jobsGet(t, operator, journey.server, "/api/v1/admin/jobs/dead")
	if status != http.StatusOK {
		t.Fatalf("GET dead status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertJobsPrivate(t, header)
	var page struct {
		Total int64 `json:"total"`
		Items []struct {
			JobID string `json:"job_id"`
			Type  string `json:"type"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("dead page is not JSON: %v", err)
	}
	if page.Total < 1 || len(page.Items) < 1 || page.Items[0].JobID != jobID {
		t.Fatalf("dead page = %.300s, want the seeded job %s", raw, jobID)
	}
	for _, secret := range []string{jobsPayloadSecret, "ana.silva@example.com", "parameters"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("dead page carries %q", secret)
		}
	}

	status, _, _ = jobsGet(t, operator, journey.server, "/api/v1/admin/jobs/dead?limit=abc")
	if status != http.StatusBadRequest {
		t.Fatalf("GET dead with an unusable limit status = %d, want 400", status)
	}
}

// TestJobsSurfacesDeniesWithoutRoleOrStepUp proves the three gates: no
// session, no assignment (including a moderator assignment, which triages
// content and does not operate the queue), and a stale session that may
// still read but may not act. No denial leaves an audit row.
func TestJobsSurfacesDeniesWithoutRoleOrStepUp(t *testing.T) {
	t.Parallel()

	journey := newJobsJourney(t)
	operator, operatorID := jobsLogin(t, journey, "jobs-gate-op-t09@example.test", "Correct Horse 11!")
	grantJobsAdmin(t, journey.pool, operatorID)
	jobID := seedJobsDead(t, journey.pool, `{"period":"2026-09"}`)
	retryPath := "/api/v1/admin/jobs/" + jobID + "/retry"

	anonymous := browser(t)
	for _, path := range []string{"/api/v1/admin/jobs/health", "/api/v1/admin/jobs/dead"} {
		status, raw, header := jobsGet(t, anonymous, journey.server, path)
		if status != http.StatusUnauthorized {
			t.Fatalf("GET %s anonymous status = %d, want 401", path, status)
		}
		assertJobsPrivate(t, header)
		assertJobsProblem(t, raw, "unauthorized")
	}
	if status, raw, _ := jobsPost(t, anonymous, journey.server, retryPath, `{"reason":"testing"}`); status != http.StatusUnauthorized {
		t.Fatalf("POST retry anonymous status = %d, want 401 (%.200s)", status, raw)
	}

	stranger, _ := jobsLogin(t, journey, "jobs-stranger-t09@example.test", "Correct Horse 11!")
	if status, raw, _ := jobsGet(t, stranger, journey.server, "/api/v1/admin/jobs/health"); status != http.StatusForbidden {
		t.Fatalf("GET health without a role status = %d, want 403 (%.200s)", status, raw)
	}
	moderator, moderatorID := jobsLogin(t, journey, "jobs-moderator-t09@example.test", "Correct Horse 11!")
	grantJobsRole(t, journey.pool, moderatorID, "moderator")
	if status, raw, _ := jobsGet(t, moderator, journey.server, "/api/v1/admin/jobs/health"); status != http.StatusForbidden {
		t.Fatalf("GET health with a moderator assignment status = %d, want 403 (%.200s)", status, raw)
	}

	ageJobsSession(t, journey.pool, operatorID, "1 hour")
	if status, raw, _ := jobsGet(t, operator, journey.server, "/api/v1/admin/jobs/health"); status != http.StatusOK {
		t.Fatalf("GET health with a stale session status = %d, want 200: reading is not the sensitive action (%.200s)", status, raw)
	}
	status, raw, header := jobsPost(t, operator, journey.server, retryPath, `{"reason":"provider outage resolved"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("POST retry with a stale session status = %d, want 401 (%.200s)", status, raw)
	}
	assertJobsPrivate(t, header)
	assertJobsProblem(t, raw, "step_up_required")
	if jobsState(t, journey.pool, jobID) != "dead" {
		t.Fatal("a refused retry moved the job")
	}
	if jobsAuditCount(t, journey.pool) != 0 {
		t.Fatal("a denied call left an audit row")
	}
}

// TestJobsRetryRequeuesAndAudits proves the positive path against the
// database: the row moves with a renewed budget, the trail records actor,
// code, reason and transition without job content, and a second retry
// conflicts while malformed calls refuse without a trace.
func TestJobsRetryRequeuesAndAudits(t *testing.T) {
	t.Parallel()

	journey := newJobsJourney(t)
	operator, operatorID := jobsLogin(t, journey, "jobs-retry-t09@example.test", "Correct Horse 11!")
	grantJobsAdmin(t, journey.pool, operatorID)
	jobID := seedJobsDead(t, journey.pool, `{"template":"verification","locale":"pt-BR","recipient":"ana.silva@example.com","code":"`+jobsPayloadSecret+`"}`)
	retryPath := "/api/v1/admin/jobs/" + jobID + "/retry"

	status, raw, header := jobsPost(t, operator, journey.server, retryPath, `{"reason":"provider outage resolved"}`)
	if status != http.StatusOK {
		t.Fatalf("POST retry status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertJobsPrivate(t, header)
	var retried struct {
		JobID string `json:"job_id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &retried); err != nil {
		t.Fatalf("retry is not JSON: %v", err)
	}
	if retried.JobID != jobID || retried.State != "queued" {
		t.Fatalf("retry = %.200s, want the requeued job", raw)
	}
	if strings.Contains(string(raw), jobsPayloadSecret) {
		t.Fatal("retry carries the job payload")
	}
	if jobsState(t, journey.pool, jobID) != "queued" {
		t.Fatal("job row did not move to queued")
	}
	var attempts int
	if err := journey.pool.QueryRow(context.Background(),
		`SELECT attempts FROM app.jobs WHERE id = $1::uuid`, jobID).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("attempts = %d, want a renewed budget", attempts)
	}

	if jobsAuditCount(t, journey.pool) != 1 {
		t.Fatalf("audit rows = %d, want one", jobsAuditCount(t, journey.pool))
	}
	var actor, targetType, targetID, reason string
	var metadata []byte
	if err := journey.pool.QueryRow(context.Background(),
		`SELECT actor_id::text, target_type, target_id, reason_code, metadata::text
		 FROM app.audit_events WHERE action = $1`, jobsdomain.ActionRetryDeadJob).
		Scan(&actor, &targetType, &targetID, &reason, &metadata); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if actor != operatorID || targetType != "operation" || targetID != jobID {
		t.Fatalf("audit target = %s/%s/%s, want the operator and the job", actor, targetType, targetID)
	}
	if reason != jobsdomain.ReasonCodeDeadJobRetry {
		t.Fatalf("audit reason = %q, want the stable code", reason)
	}
	if !strings.Contains(string(metadata), "provider outage resolved") {
		t.Fatalf("audit metadata = %s, want the stated reason", metadata)
	}
	for _, secret := range []string{jobsPayloadSecret, "ana.silva@example.com"} {
		if strings.Contains(string(metadata), secret) {
			t.Fatalf("audit metadata carries job content: %s", metadata)
		}
	}

	before := jobsAuditCount(t, journey.pool)
	duplicate, raw, _ := jobsPost(t, operator, journey.server, retryPath, `{"reason":"testing"}`)
	if duplicate != http.StatusConflict {
		t.Fatalf("POST retry twice status = %d, want 409 (%.200s)", duplicate, raw)
	}
	assertJobsProblem(t, raw, "job_not_dead")
	for _, refusal := range []struct {
		name string
		path string
		body string
		code string
		want int
	}{
		{"missing reason", retryPath, `{"reason":"  "}`, "reason_required", http.StatusBadRequest},
		{"unknown job", "/api/v1/admin/jobs/0191f0e0-0000-7000-8000-0000000000ff/retry", `{"reason":"testing"}`, "job_not_found", http.StatusNotFound},
	} {
		status, raw, _ := jobsPost(t, operator, journey.server, refusal.path, refusal.body)
		if status != refusal.want {
			t.Fatalf("%s status = %d, want %d (%.200s)", refusal.name, status, refusal.want, raw)
		}
		assertJobsProblem(t, raw, refusal.code)
	}
	if jobsAuditCount(t, journey.pool) != before {
		t.Fatal("a refused retry left an audit row")
	}
}

// TestComposeJobsFailsClosed proves the composer refuses an incomplete
// composition instead of serving a partial surface, and that the surface
// mounts exactly its three operator routes — no staged path beside them.
func TestComposeJobsFailsClosed(t *testing.T) {
	t.Parallel()

	options := completeOptions(t)
	options.Pool = nil
	options.Clock = nil
	if _, err := bootstrap.ComposeJobs(options); err == nil {
		t.Fatal("ComposeJobs() built a surface without pool and clock")
	} else if !errors.Is(err, bootstrap.ErrIncompleteComposition) {
		t.Fatalf("ComposeJobs() error = %v, want it to wrap ErrIncompleteComposition", err)
	} else if !strings.Contains(err.Error(), "postgres pool") || !strings.Contains(err.Error(), "clock") {
		t.Fatalf("ComposeJobs() error = %q, want it to name every missing edge", err)
	}

	unknown := completeOptions(t)
	unknown.Env = config.Env("staging")
	if _, err := bootstrap.ComposeJobs(unknown); err == nil {
		t.Fatal("ComposeJobs() accepted an environment outside development, test and production")
	} else if !errors.Is(err, bootstrap.ErrIncompleteComposition) {
		t.Fatalf("ComposeJobs() error = %v, want it to wrap ErrIncompleteComposition", err)
	}

	database := dbtest.New(t)
	pool := database.Pool.Pool()
	clock := clockseed.NewClock()
	random := clockseed.NewRandom()
	manager, err := security.New(security.Options{Env: config.EnvTest, Clock: clock, Random: random})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}
	surface, err := bootstrap.ComposeJobs(bootstrap.Options{
		Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool: pool, Clock: clock, Random: random, Assets: manifestFixture(t), Security: manager,
	})
	if err != nil {
		t.Fatalf("ComposeJobs() error = %v", err)
	}
	routes := surface.Routes()
	if len(routes) != 3 {
		t.Fatalf("routes = %d, want health, dead and retry", len(routes))
	}
	for _, route := range routes {
		if !strings.HasPrefix(route.Path, "/api/v1/admin/jobs/") {
			t.Fatalf("route %q escapes the operator surface", route.String())
		}
	}
	mux := http.NewServeMux()
	if err := surface.Mount(mux); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}
	if err := surface.Mount(http.NewServeMux()); err == nil {
		t.Fatal("Mount() mounted twice without refusing")
	}
	if err := surface.Mount(nil); err == nil {
		t.Fatal("Mount(nil) mounted without refusing")
	} else if !errors.Is(err, bootstrap.ErrIncompleteComposition) {
		t.Fatalf("Mount(nil) error = %v, want it to wrap ErrIncompleteComposition", err)
	}
}
