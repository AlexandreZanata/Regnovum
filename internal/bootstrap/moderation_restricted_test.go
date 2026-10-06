// Tests of the restricted moderation surface composed in the process
// (P49-T07): report/appeal filing plus the case queue, claim and decision
// routes run on the platform mux over disposable PostgreSQL, driven by real
// HTTP with the same pool and security boundary as the account journey —
// the session the JSON login opened is the session the moderation routes
// require, and no second authentication exists.
//
// Competence is read at request time, never granted here: the triage routes
// require an active assignment from the role store plus a recently
// stepped-up session, and no promotion route exists on this surface — the
// first-administrator bootstrap stays a host-only command.
package bootstrap_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// moderationJourney is the account plus moderation surfaces on a real
// listener, sharing one pool and one security boundary like `arena server`
// does.
type moderationJourney struct {
	account    *bootstrap.AccountSurface
	moderation *bootstrap.ModerationSurface
	server     *httptest.Server
	pool       *pgxpool.Pool
}

// newModerationJourney composes both surfaces the way the process does.
func newModerationJourney(t *testing.T) *moderationJourney {
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
			CursorSecret: []byte(cursorSecret),
		}
	}
	account, err := bootstrap.ComposeAccount(base())
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}
	moderation, err := bootstrap.ComposeModeration(base())
	if err != nil {
		t.Fatalf("ComposeModeration() error = %v", err)
	}
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{},
		[]httpserver.Surface{account.Surface(), moderation.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &moderationJourney{account: account, moderation: moderation, server: server, pool: pool}
}

// moderationLogin registers, verifies and signs in one account over the
// JSON API, returning the client holding its session plus the account
// identifier.
func moderationLogin(t *testing.T, journey *moderationJourney, email, password string) (*http.Client, string) {
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

// moderationPost issues one authenticated JSON POST and returns status, body
// and headers.
func moderationPost(t *testing.T, client *http.Client, server *httptest.Server, path, body string) (int, []byte, http.Header) {
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

// grantModerationRole records an active assignment for the account. Role
// assignment is seeded here the way the host-only bootstrap would write it:
// no HTTP route can grant it, which the no-promotion test proves.
func grantModerationRole(t *testing.T, pool *pgxpool.Pool, accountID string) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1::uuid, 'admin', $1::uuid)`,
		accountID); err != nil {
		t.Fatalf("grant moderator: %v", err)
	}
}

// stepUpModerationSession marks the account's latest session as having
// presented a second factor factorAge ago: the factor ceremony itself belongs
// to the identity module, and what is proven here is that the triage gate
// reads it.
func stepUpModerationSession(t *testing.T, pool *pgxpool.Pool, accountID string, factorAge time.Duration) {
	t.Helper()

	var sessionID string
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM app.sessions WHERE account_id = $1::uuid ORDER BY created_at DESC LIMIT 1`,
		accountID).Scan(&sessionID); err != nil {
		t.Fatalf("find session: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE app.sessions SET mfa_verified_at = now() - ($1::bigint * interval '1 second') WHERE id = $2::uuid`,
		int64(factorAge/time.Second), sessionID); err != nil {
		t.Fatalf("step up session: %v", err)
	}
}

// stageModerationArena inserts one draft arena the reports and cases target.
func stageModerationArena(t *testing.T, pool *pgxpool.Pool, creatorID string) string {
	t.Helper()

	var arenaID string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1::uuid, 'Moderation composition probe statement', 'technology', 'pt-BR', 'draft')
		RETURNING id`, creatorID).Scan(&arenaID); err != nil {
		t.Fatalf("stage arena: %v", err)
	}
	return arenaID
}

// stageModerationCase opens one triage case, the way triage would after a
// report: the composition proof stages the row directly because case intake
// is not a browser route.
func stageModerationCase(t *testing.T, pool *pgxpool.Pool, targetType, targetID string) string {
	t.Helper()

	column := map[string]string{"arena": "target_arena_id", "profile": "target_account_id"}[targetType]
	if column == "" {
		t.Fatalf("unknown case target %q", targetType)
	}
	var caseID string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO app.moderation_cases (target_type, `+column+`) VALUES ($1, $2::uuid) RETURNING id`,
		targetType, targetID).Scan(&caseID); err != nil {
		t.Fatalf("stage case: %v", err)
	}
	return caseID
}

// expireModerationLease lapses the claim lease, so the next decision faces
// the expired-lease refusal instead of settling. Both instants move to the
// past together because the schema requires the lease to exceed the claim;
// the lease still lapsed relative to now.
func expireModerationLease(t *testing.T, pool *pgxpool.Pool, caseID string) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`UPDATE app.moderation_cases SET claimed_at = now() - interval '2 hours', lease_expires_at = now() - interval '1 hour' WHERE id = $1::uuid`,
		caseID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
}

// countModerationActions reads the audit rows of one case: the sanction
// effect and the recorded action commit together, so the row is the proof
// the case was audited.
func countModerationActions(t *testing.T, pool *pgxpool.Pool, caseID string) int {
	t.Helper()

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app.moderation_actions WHERE case_id = $1::uuid`, caseID).Scan(&count); err != nil {
		t.Fatalf("count moderation actions: %v", err)
	}
	return count
}

// assertModerationPrivate requires the private cache policy on every
// moderation answer: no intermediary keeps triage state.
func assertModerationPrivate(t *testing.T, header http.Header) {
	t.Helper()

	if !strings.Contains(header.Get("Cache-Control"), "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", header.Get("Cache-Control"))
	}
}

// TestModerationQueueRequiresRoleAndStepUp proves the restricted audience of
// triage: strangers without an assignment are refused, moderators whose
// session never presented a factor are refused, a stale factor is refused,
// and a stepped-up moderator is served.
func TestModerationQueueRequiresRoleAndStepUp(t *testing.T) {
	t.Parallel()

	journey := newModerationJourney(t)
	stranger := func() *http.Client {
		client, _ := moderationLogin(t, journey, "mod-stranger-t07@arena.example.com", "Correct Horse 11!")
		return client
	}()
	moderator, moderatorID := moderationLogin(t, journey, "mod-t07@arena.example.com", "Correct Horse 11!")
	grantModerationRole(t, journey.pool, moderatorID)

	status, _, header := entitlementGet(t, stranger, journey.server, "/api/v1/moderation/cases")
	if status != http.StatusForbidden {
		t.Fatalf("GET queue without a role status = %d, want 403", status)
	}
	assertModerationPrivate(t, header)

	status, raw, _ := entitlementGet(t, moderator, journey.server, "/api/v1/moderation/cases")
	if status != http.StatusForbidden {
		t.Fatalf("GET queue without a factor status = %d, want 403 (body: %.300s)", status, raw)
	}
	if !strings.Contains(string(raw), "mfa_step_up_required") {
		t.Fatalf("GET queue without a factor body = %.300s, want the step-up code", raw)
	}

	stepUpModerationSession(t, journey.pool, moderatorID, 0)
	status, raw, header = entitlementGet(t, moderator, journey.server, "/api/v1/moderation/cases")
	if status != http.StatusOK {
		t.Fatalf("GET queue stepped up status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertModerationPrivate(t, header)
	var queue struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(raw, &queue); err != nil {
		t.Fatalf("queue is not JSON: %v", err)
	}
	if len(queue.Items) != 0 {
		t.Fatalf("queue items = %d, want 0 before triage stages a case", len(queue.Items))
	}

	stale, staleID := moderationLogin(t, journey, "mod-stale-t07@arena.example.com", "Correct Horse 11!")
	grantModerationRole(t, journey.pool, staleID)
	stepUpModerationSession(t, journey.pool, staleID, time.Hour)
	status, _, _ = entitlementGet(t, stale, journey.server, "/api/v1/moderation/cases")
	if status != http.StatusForbidden {
		t.Fatalf("GET queue with a stale factor status = %d, want 403", status)
	}

	anonymous := browser(t)
	status, _, header = entitlementGet(t, anonymous, journey.server, "/api/v1/moderation/cases")
	if status != http.StatusUnauthorized {
		t.Fatalf("GET queue anonymous status = %d, want 401", status)
	}
	assertModerationPrivate(t, header)
}

// TestModerationReportFiling proves any authenticated account can file a
// structured report, and nobody can without a session.
func TestModerationReportFiling(t *testing.T) {
	t.Parallel()

	journey := newModerationJourney(t)
	reporter, reporterID := moderationLogin(t, journey, "mod-reporter-t07@arena.example.com", "Correct Horse 11!")
	arenaID := stageModerationArena(t, journey.pool, reporterID)

	status, raw, header := moderationPost(t, reporter, journey.server, "/api/v1/me/moderation/reports",
		`{"target_type":"arena","target_id":`+quoteJSON(arenaID)+`,"reason":"spam","context":"Composition probe"}`)
	if status != http.StatusOK {
		t.Fatalf("POST reports status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertModerationPrivate(t, header)
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("report is not JSON: %v", err)
	}
	for _, key := range []string{"report_id", "replayed", "rate_limited", "reports_in_window"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("report is missing %q: %.300s", key, raw)
		}
	}
	if reportID, _ := document["report_id"].(string); reportID == "" {
		t.Fatalf("report_id is empty: %.300s", raw)
	}

	anonymous := browser(t)
	status, _, _ = moderationPost(t, anonymous, journey.server, "/api/v1/me/moderation/reports",
		`{"target_type":"arena","target_id":`+quoteJSON(arenaID)+`,"reason":"spam"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("POST reports anonymous status = %d, want 401", status)
	}
}

// TestModerationClaimDecideAuditJourney proves the auditable decision path:
// a stepped-up moderator claims, a second moderator loses the live lease, a
// warning settles with its audit row, a second decision conflicts, and a
// lapsed lease refuses the decision.
func TestModerationClaimDecideAuditJourney(t *testing.T) {
	t.Parallel()

	journey := newModerationJourney(t)
	_, reporterID := moderationLogin(t, journey, "mod-owner-t07@arena.example.com", "Correct Horse 11!")
	moderator, moderatorID := moderationLogin(t, journey, "mod-decider-t07@arena.example.com", "Correct Horse 11!")
	second, secondID := moderationLogin(t, journey, "mod-second-t07@arena.example.com", "Correct Horse 11!")
	grantModerationRole(t, journey.pool, moderatorID)
	grantModerationRole(t, journey.pool, secondID)
	stepUpModerationSession(t, journey.pool, moderatorID, 0)
	stepUpModerationSession(t, journey.pool, secondID, 0)
	arenaID := stageModerationArena(t, journey.pool, reporterID)
	caseID := stageModerationCase(t, journey.pool, "arena", arenaID)
	casePath := "/api/v1/moderation/cases/" + caseID

	status, raw, _ := moderationPost(t, moderator, journey.server, casePath+"/claim", "")
	if status != http.StatusOK {
		t.Fatalf("POST claim status = %d, want 200 (body: %.300s)", status, raw)
	}
	var claim struct {
		CaseID    string `json:"case_id"`
		Status    string `json:"status"`
		ClaimedBy string `json:"claimed_by"`
	}
	if err := json.Unmarshal(raw, &claim); err != nil {
		t.Fatalf("claim is not JSON: %v", err)
	}
	if claim.Status != "under_review" || claim.ClaimedBy == "" {
		t.Fatalf("claim = %.300s, want an under_review routing", raw)
	}

	status, raw, _ = moderationPost(t, second, journey.server, casePath+"/claim", "")
	if status != http.StatusConflict {
		t.Fatalf("POST claim on a live lease status = %d, want 409 (body: %.300s)", status, raw)
	}

	status, raw, header := moderationPost(t, moderator, journey.server, casePath+"/decisions",
		`{"action":"warning","rule":"MOD-2:warning","justification":"Measured warning with scope"}`)
	if status != http.StatusOK {
		t.Fatalf("POST decisions status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertModerationPrivate(t, header)
	var decision struct {
		ActionID string `json:"action_id"`
		CaseID   string `json:"case_id"`
		Action   string `json:"action"`
	}
	if err := json.Unmarshal(raw, &decision); err != nil {
		t.Fatalf("decision is not JSON: %v", err)
	}
	if decision.ActionID == "" || decision.Action != "warning" {
		t.Fatalf("decision = %.300s, want the recorded warning", raw)
	}
	for _, leaked := range []string{"Measured warning", "justification", "moderation_actions"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("decision response leaks restricted evidence %q: %.300s", leaked, raw)
		}
	}
	if got := countModerationActions(t, journey.pool, caseID); got != 1 {
		t.Fatalf("moderation actions = %d, want 1: the sanction effect and the audit row commit together", got)
	}

	status, raw, _ = moderationPost(t, moderator, journey.server, casePath+"/decisions",
		`{"action":"warning","rule":"MOD-2:warning","justification":"Second measure on a decided case"}`)
	if status != http.StatusConflict {
		t.Fatalf("POST second decision status = %d, want 409 (body: %.300s)", status, raw)
	}

	lapsing := stageModerationCase(t, journey.pool, "arena", arenaID)
	status, _, _ = moderationPost(t, moderator, journey.server, "/api/v1/moderation/cases/"+lapsing+"/claim", "")
	if status != http.StatusOK {
		t.Fatalf("POST claim on the lapsing case status = %d, want 200", status)
	}
	expireModerationLease(t, journey.pool, lapsing)
	status, raw, _ = moderationPost(t, moderator, journey.server, "/api/v1/moderation/cases/"+lapsing+"/decisions",
		`{"action":"warning","rule":"MOD-2:warning","justification":"Lapsed lease attempt"}`)
	if status != http.StatusConflict {
		t.Fatalf("POST decision on a lapsed lease status = %d, want 409 (body: %.300s)", status, raw)
	}

	status, raw, _ = entitlementGet(t, moderator, journey.server, "/api/v1/moderation/cases?status=decided")
	if status != http.StatusOK {
		t.Fatalf("GET decided queue status = %d, want 200", status)
	}
	var decided struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(raw, &decided); err != nil {
		t.Fatalf("decided queue is not JSON: %v", err)
	}
	if len(decided.Items) != 1 {
		t.Fatalf("decided items = %d, want 1", len(decided.Items))
	}
}

// TestModerationAppealAfterWarning proves the sanctioned owner can contest
// the measure over HTTP: a warning on their profile is appealable inside
// its window, and the warning leaves the account active so the owner keeps
// a session to file with. (A suspension moves the account to suspended,
// whose sessions the identity module refuses — appealing that measure over
// HTTP is unreachable by product rule, not by composition; recorded here so
// no later task mistakes the absence of that proof for an untested path.)
func TestModerationAppealAfterWarning(t *testing.T) {
	t.Parallel()

	journey := newModerationJourney(t)
	reporter, reporterID := moderationLogin(t, journey, "mod-appellant-t07@arena.example.com", "Correct Horse 11!")
	moderator, moderatorID := moderationLogin(t, journey, "mod-warner-t07@arena.example.com", "Correct Horse 11!")
	grantModerationRole(t, journey.pool, moderatorID)
	stepUpModerationSession(t, journey.pool, moderatorID, 0)
	caseID := stageModerationCase(t, journey.pool, "profile", reporterID)
	casePath := "/api/v1/moderation/cases/" + caseID

	status, _, _ := moderationPost(t, moderator, journey.server, casePath+"/claim", "")
	if status != http.StatusOK {
		t.Fatalf("POST claim status = %d, want 200", status)
	}
	status, raw, _ := moderationPost(t, moderator, journey.server, casePath+"/decisions",
		`{"action":"warning","rule":"MOD-2:warning","justification":"Appealable warning with scope"}`)
	if status != http.StatusOK {
		t.Fatalf("POST warning status = %d, want 200 (body: %.300s)", status, raw)
	}
	var decision struct {
		ActionID string `json:"action_id"`
	}
	if err := json.Unmarshal(raw, &decision); err != nil {
		t.Fatalf("decision is not JSON: %v", err)
	}

	status, raw, header := moderationPost(t, reporter, journey.server, "/api/v1/me/moderation/appeals",
		`{"action_id":`+quoteJSON(decision.ActionID)+`,"context":"Contesting the warning with fresh context"}`)
	if status != http.StatusOK {
		t.Fatalf("POST appeals status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertModerationPrivate(t, header)
	var appeal struct {
		AppealID string `json:"appeal_id"`
		ActionID string `json:"action_id"`
		Replayed bool   `json:"replayed"`
	}
	if err := json.Unmarshal(raw, &appeal); err != nil {
		t.Fatalf("appeal is not JSON: %v", err)
	}
	if appeal.AppealID == "" || appeal.Replayed {
		t.Fatalf("appeal = %.300s, want a fresh appeal identity", raw)
	}
}

// TestModerationHasNoSelfPromotionRoute proves the browser cannot promote:
// the surface answers exactly the five moderation routes, and plausible
// administration paths fall into the registry placeholder instead of a
// handler refusal.
func TestModerationHasNoSelfPromotionRoute(t *testing.T) {
	t.Parallel()

	journey := newModerationJourney(t)
	want := map[string]bool{
		"POST /api/v1/me/moderation/reports":           false,
		"POST /api/v1/me/moderation/appeals":           false,
		"GET /api/v1/moderation/cases":                 false,
		"POST /api/v1/moderation/cases/{id}/claim":     false,
		"POST /api/v1/moderation/cases/{id}/decisions": false,
	}
	for _, route := range journey.moderation.Routes() {
		key := route.String()
		if _, ok := want[key]; !ok {
			t.Fatalf("moderation surface answers %q: only the five moderation routes may mount", key)
		}
		want[key] = true
	}
	for key, seen := range want {
		if !seen {
			t.Fatalf("moderation surface is missing %q", key)
		}
	}

	moderator, moderatorID := moderationLogin(t, journey, "mod-nopromo-t07@arena.example.com", "Correct Horse 11!")
	grantModerationRole(t, journey.pool, moderatorID)
	stepUpModerationSession(t, journey.pool, moderatorID, 0)
	for _, path := range []string{"/api/v1/admin/grant", "/api/v1/moderation/roles", "/api/v1/me/moderation/role"} {
		status, raw, _ := moderationPost(t, moderator, journey.server, path, "{}")
		if status != http.StatusNotFound {
			t.Fatalf("POST %s status = %d, want 404: no promotion route may exist", path, status)
		}
		if !strings.Contains(string(raw), "404 page not found") {
			t.Fatalf("POST %s body = %.200s, want the registry placeholder, not a handler refusal", path, raw)
		}
	}
}
