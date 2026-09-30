package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/http"
	positionspg "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/positions/application"
	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

const (
	positionsOwnerToken = "positions-owner-session-token"
	positionsOtherToken = "positions-other-session-token"
)

// arenaGate is a mutable stub of the Arena eligibility port: tests flip the
// answer to simulate a closed Arena.
type arenaGate struct {
	mu  sync.Mutex
	err error
}

func (g *arenaGate) EnsureAcceptsPositions(context.Context, domain.ArenaID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

func (g *arenaGate) set(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err = err
}

// eligibleAccounts is a stub of the account eligibility port.
type eligibleAccounts struct{ err error }

func (e eligibleAccounts) EnsureEligible(context.Context, domain.AccountID) error { return e.err }

type positionsHarness struct {
	mux        http.Handler
	defaultMux http.Handler
	pool       *pgxpool.Pool
	gate       *arenaGate
	ownerID    string
	otherID    string
	arenaID    string
	ownerEmail string
	otherEmail string
}

func positionsUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustPositionsAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) string {
	t.Helper()
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return positionsUUID(account.ID)
}

func mustPositionsArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator string) string {
	t.Helper()
	var creatorUUID pgtype.UUID
	if err := creatorUUID.Scan(creator); err != nil {
		t.Fatalf("parse creator id: %v", err)
	}
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Afirmação para a API de posições', 'technology', 'pt-BR', 'draft')
		RETURNING id`, creatorUUID).Scan(&id); err != nil {
		t.Fatalf("insert arena: %v", err)
	}
	return positionsUUID(id)
}

func setupPositionsHarness(t *testing.T) *positionsHarness {
	t.Helper()
	return setupPositionsHarnessWithClock(t, clockseed.NewClock())
}

// harnessClock is what the harness needs from a clock: the use cases accept any
// implementation of it.
type harnessClock interface{ Now() time.Time }

// setupPositionsHarnessWithClock wires the harness over a given clock, which
// lets a test move time between two reads of the same counts.
func setupPositionsHarnessWithClock(t *testing.T, clock harnessClock) *positionsHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	ownerEmail := "positions-http-owner@arena.example.com"
	otherEmail := "positions-http-other@arena.example.com"
	ownerID := mustPositionsAccount(t, ctx, q, ownerEmail)
	otherID := mustPositionsAccount(t, ctx, q, otherEmail)
	arenaID := mustPositionsArena(t, ctx, pool, ownerID)

	repo := positionspg.NewRepository(pool)
	gate := &arenaGate{}
	accounts := eligibleAccounts{}

	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clockseed.NewClock(),
		Random:         clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("setup security manager: %v", err)
	}

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case positionsOwnerToken:
			return security.AuthIdentity{AccountID: ownerID, SessionID: "session-owner"}, nil
		case positionsOtherToken:
			return security.AuthIdentity{AccountID: otherID, SessionID: "session-other"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})

	buildMux := func(policy domain.AggregatePolicy) http.Handler {
		handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{
			ConfirmUseCase:   application.NewConfirmInitialPositionUseCase(repo, accounts, gate, clock),
			ChangeUseCase:    application.NewChangePositionUseCase(repo, gate, platformpg.NewTxManager(pool), clock),
			GetMineUseCase:   application.NewGetMyPositionUseCase(repo),
			ListMineUseCase:  application.NewListPositionChangesUseCase(repo),
			AggregateUseCase: application.NewGetPositionAggregateUseCase(repo, policy, clock),
			SecurityManager:  secMgr,
		})
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		return secMgr.AuthenticateMiddleware(validator)(mux)
	}

	return &positionsHarness{
		mux:        buildMux(domain.AggregatePolicy{Version: "test", MinParticipants: 1}),
		defaultMux: buildMux(domain.DefaultAggregatePolicy()),
		pool:       pool,
		gate:       gate,
		ownerID:    ownerID,
		otherID:    otherID,
		arenaID:    arenaID,
		ownerEmail: ownerEmail,
		otherEmail: otherEmail,
	}
}

func positionsRequest(method, path, token, body string) *http.Request {
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	}
	return request
}

func positionsDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return object
}

func positionsAssertExactKeys(t *testing.T, object map[string]any, want ...string) {
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

func positionsAssertPrivateHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
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

func (h *positionsHarness) confirm(t *testing.T, token, position string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, positionsRequest(http.MethodPost, "/api/v1/me/arenas/"+h.arenaID+"/position", token,
		fmt.Sprintf(`{"position":%q}`, position)))
	return recorder
}

func TestPositionsAPIRequiresAuthentication(t *testing.T) {
	harness := setupPositionsHarness(t)

	probes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/me/arenas/" + harness.arenaID + "/position"},
		{http.MethodGet, "/api/v1/me/arenas/" + harness.arenaID + "/position"},
		{http.MethodPost, "/api/v1/me/arenas/" + harness.arenaID + "/position/changes"},
		{http.MethodGet, "/api/v1/me/arenas/" + harness.arenaID + "/position/changes"},
	}
	for _, probe := range probes {
		t.Run(probe.method+" "+probe.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, positionsRequest(probe.method, probe.path, "", ""))

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous status = %d, want 401 (body: %s)", recorder.Code, recorder.Body.String())
			}
			if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
			}
			positionsAssertPrivateHeaders(t, recorder)
		})
	}

	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, positionsRequest(http.MethodGet, "/api/v1/me/arenas/"+harness.arenaID+"/position", "revoked", ""))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid session status = %d, want 401", recorder.Code)
	}
}

func TestConfirmPositionHTTPIsIdempotent(t *testing.T) {
	harness := setupPositionsHarness(t)

	recorder := harness.confirm(t, positionsOwnerToken, domain.PositionAgree)
	if recorder.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	positionsAssertPrivateHeaders(t, recorder)

	body := recorder.Body.String()
	if strings.Contains(body, harness.ownerEmail) || strings.Contains(body, harness.ownerID) || strings.Contains(body, "account_id") {
		t.Fatalf("SECURITY VIOLATION: private position leaked account data: %s", body)
	}
	confirmation := positionsDecode(t, recorder.Body.Bytes())
	positionsAssertExactKeys(t, confirmation, "position", "replayed")
	if confirmation["replayed"] != false {
		t.Fatalf("first confirmation replayed = %v, want false", confirmation["replayed"])
	}
	position, _ := confirmation["position"].(map[string]any)
	positionsAssertExactKeys(t, position, "arena_id", "initial_position", "current_position", "version", "created_at", "updated_at")
	if position["initial_position"] != domain.PositionAgree || position["current_position"] != domain.PositionAgree || position["version"] != float64(1) {
		t.Fatalf("position = %v, want agree v1", position)
	}

	retry := harness.confirm(t, positionsOwnerToken, domain.PositionAgree)
	if retry.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", retry.Code)
	}
	if retried := positionsDecode(t, retry.Body.Bytes()); retried["replayed"] != true {
		t.Fatalf("retry replayed = %v, want true", retried["replayed"])
	}

	different := harness.confirm(t, positionsOwnerToken, domain.PositionDisagree)
	if different.Code != http.StatusConflict {
		t.Fatalf("different value status = %d, want 409 (body: %s)", different.Code, different.Body.String())
	}
	if problem := positionsDecode(t, different.Body.Bytes()); problem["code"] != "initial_position_already_set" {
		t.Fatalf("different value code = %v, want initial_position_already_set", problem["code"])
	}

	// The owner reads the projection back.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, positionsRequest(http.MethodGet, "/api/v1/me/arenas/"+harness.arenaID+"/position", positionsOwnerToken, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("read status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	positionsAssertPrivateHeaders(t, recorder)
	read := positionsDecode(t, recorder.Body.Bytes())
	positionsAssertExactKeys(t, read, "arena_id", "initial_position", "current_position", "version", "created_at", "updated_at")
	if read["arena_id"] != harness.arenaID || read["current_position"] != domain.PositionAgree {
		t.Fatalf("read position = %v, want the stored projection", read)
	}
}

func TestChangePositionHTTPRecordsAndListsHistory(t *testing.T) {
	harness := setupPositionsHarness(t)

	if recorder := harness.confirm(t, positionsOwnerToken, domain.PositionAgree); recorder.Code != http.StatusOK {
		t.Fatalf("confirm status = %d", recorder.Code)
	}

	change := func(position string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, positionsRequest(http.MethodPost, "/api/v1/me/arenas/"+harness.arenaID+"/position/changes", positionsOwnerToken,
			fmt.Sprintf(`{"position":%q}`, position)))
		return recorder
	}

	recorder := change(domain.PositionDisagree)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("change status = %d, want 201 (body: %s)", recorder.Code, recorder.Body.String())
	}
	positionsAssertPrivateHeaders(t, recorder)
	recorded := positionsDecode(t, recorder.Body.Bytes())
	positionsAssertExactKeys(t, recorded, "change_id", "position")
	changeID, _ := recorded["change_id"].(string)
	if changeID == "" {
		t.Fatal("change must return its identifier for later attribution")
	}
	position, _ := recorded["position"].(map[string]any)
	if position["current_position"] != domain.PositionDisagree || position["version"] != float64(2) || position["initial_position"] != domain.PositionAgree {
		t.Fatalf("changed position = %v, want agree -> disagree v2", position)
	}

	// A retried change to the current position is a conflict.
	same := change(domain.PositionDisagree)
	if same.Code != http.StatusConflict {
		t.Fatalf("same position status = %d, want 409 (body: %s)", same.Code, same.Body.String())
	}
	if problem := positionsDecode(t, same.Body.Bytes()); problem["code"] != "position_same" {
		t.Fatalf("same position code = %v, want position_same", problem["code"])
	}

	// A closed Arena refuses new changes.
	harness.gate.set(application.ErrArenaNotOpen)
	closed := change(domain.PositionUndecided)
	if closed.Code != http.StatusConflict {
		t.Fatalf("closed arena status = %d, want 409 (body: %s)", closed.Code, closed.Body.String())
	}
	if problem := positionsDecode(t, closed.Body.Bytes()); problem["code"] != "arena_not_open" {
		t.Fatalf("closed arena code = %v, want arena_not_open", problem["code"])
	}
	harness.gate.set(nil)

	// The owner history lists the accepted change, newest first.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, positionsRequest(http.MethodGet, "/api/v1/me/arenas/"+harness.arenaID+"/position/changes", positionsOwnerToken, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("history status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	positionsAssertPrivateHeaders(t, recorder)
	history := positionsDecode(t, recorder.Body.Bytes())
	positionsAssertExactKeys(t, history, "items")
	items, _ := history["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("history items = %v, want the single accepted change", history["items"])
	}
	entry, _ := items[0].(map[string]any)
	positionsAssertExactKeys(t, entry, "change_id", "from_position", "to_position", "version", "changed_at")
	if entry["change_id"] != changeID || entry["from_position"] != domain.PositionAgree || entry["to_position"] != domain.PositionDisagree || entry["version"] != float64(2) {
		t.Fatalf("history entry = %v, want the recorded change", entry)
	}
}

func TestPositionsAPIIsOwnerScoped(t *testing.T) {
	harness := setupPositionsHarness(t)

	if recorder := harness.confirm(t, positionsOwnerToken, domain.PositionAgree); recorder.Code != http.StatusOK {
		t.Fatalf("owner confirm status = %d", recorder.Code)
	}

	// The other account cannot read the owner projection or history.
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, positionsRequest(http.MethodGet, "/api/v1/me/arenas/"+harness.arenaID+"/position", positionsOtherToken, ""))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("other read status = %d, want 404 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if problem := positionsDecode(t, recorder.Body.Bytes()); problem["code"] != "position_not_found" {
		t.Fatalf("other read code = %v, want position_not_found", problem["code"])
	}

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, positionsRequest(http.MethodGet, "/api/v1/me/arenas/"+harness.arenaID+"/position/changes", positionsOtherToken, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("other history status = %d, want 200", recorder.Code)
	}
	otherHistory := positionsDecode(t, recorder.Body.Bytes())
	if items, _ := otherHistory["items"].([]any); len(items) != 0 {
		t.Fatalf("other history items = %v, want empty", otherHistory["items"])
	}

	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, positionsRequest(http.MethodPost, "/api/v1/me/arenas/"+harness.arenaID+"/position/changes", positionsOtherToken,
		fmt.Sprintf(`{"position":%q}`, domain.PositionDisagree)))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("other change status = %d, want 404 (body: %s)", recorder.Code, recorder.Body.String())
	}

	// The owner projection is untouched.
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, positionsRequest(http.MethodGet, "/api/v1/me/arenas/"+harness.arenaID+"/position", positionsOwnerToken, ""))
	if owner := positionsDecode(t, recorder.Body.Bytes()); owner["version"] != float64(1) || owner["current_position"] != domain.PositionAgree {
		t.Fatalf("owner position after intruder probes = %v", owner)
	}

	// The other account confirms its own independent position.
	if recorder := harness.confirm(t, positionsOtherToken, domain.PositionDisagree); recorder.Code != http.StatusOK {
		t.Fatalf("other confirm status = %d (body: %s)", recorder.Code, recorder.Body.String())
	}
}

// steppingClock moves the instant on every read, which is exactly the boundary
// the cache defect could not survive: the derivation instant of two consecutive
// reads never lands on the same second.
type steppingClock struct {
	mu  sync.Mutex
	now time.Time
}

func newSteppingClock(at time.Time) *steppingClock { return &steppingClock{now: at} }

func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(2 * time.Second)
	return c.now
}

// TestAggregateValidatorSurvivesAMovingInstant is the regression test of the
// cache defect: the aggregate states the instant of its own derivation, the
// clock moves between the reads here, and the counts did not move — so a
// revalidation must still answer 304 without a body. A validator computed over
// the annotation instead of the counts answers 200, which is what used to
// happen under load and what made the response uncacheable in production.
func TestAggregateValidatorSurvivesAMovingInstant(t *testing.T) {
	harness := setupPositionsHarnessWithClock(t, newSteppingClock(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)))
	if recorder := harness.confirm(t, positionsOwnerToken, domain.PositionAgree); recorder.Code != http.StatusOK {
		t.Fatalf("owner confirm status = %d", recorder.Code)
	}
	if recorder := harness.confirm(t, positionsOtherToken, domain.PositionDisagree); recorder.Code != http.StatusOK {
		t.Fatalf("other confirm status = %d", recorder.Code)
	}

	aggregatePath := "/api/v1/arenas/" + harness.arenaID + "/positions"
	first := httptest.NewRecorder()
	harness.mux.ServeHTTP(first, positionsRequest(http.MethodGet, aggregatePath, "", ""))
	if first.Code != http.StatusOK {
		t.Fatalf("aggregate status = %d, want 200 (body: %s)", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag is missing on the public aggregate read")
	}

	second := httptest.NewRecorder()
	harness.mux.ServeHTTP(second, positionsRequest(http.MethodGet, aggregatePath, "", ""))
	firstInstant, _ := positionsDecode(t, first.Body.Bytes())["checked_at"].(string)
	secondInstant, _ := positionsDecode(t, second.Body.Bytes())["checked_at"].(string)
	if firstInstant == "" || firstInstant == secondInstant {
		t.Fatalf("checked_at = %q and %q, want an instant that moved between the reads", firstInstant, secondInstant)
	}
	if second.Header().Get("ETag") != etag {
		t.Fatalf("ETag moved with the annotation: %q then %q", etag, second.Header().Get("ETag"))
	}

	revalidation := positionsRequest(http.MethodGet, aggregatePath, "", "")
	revalidation.Header.Set("If-None-Match", etag)
	notModified := httptest.NewRecorder()
	harness.mux.ServeHTTP(notModified, revalidation)
	if notModified.Code != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want 304 with the counts unchanged (body: %s)", notModified.Code, notModified.Body.String())
	}
	if notModified.Body.Len() != 0 {
		t.Fatalf("304 body has %d bytes, want empty", notModified.Body.Len())
	}
}

func TestPositionAggregateHTTPIsPublicAndCacheable(t *testing.T) {
	harness := setupPositionsHarness(t)

	if recorder := harness.confirm(t, positionsOwnerToken, domain.PositionAgree); recorder.Code != http.StatusOK {
		t.Fatalf("owner confirm status = %d", recorder.Code)
	}
	if recorder := harness.confirm(t, positionsOtherToken, domain.PositionDisagree); recorder.Code != http.StatusOK {
		t.Fatalf("other confirm status = %d", recorder.Code)
	}

	aggregatePath := "/api/v1/arenas/" + harness.arenaID + "/positions"
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, positionsRequest(http.MethodGet, aggregatePath, "", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("aggregate status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	cacheControl := recorder.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "public") || !strings.Contains(cacheControl, "max-age=60") {
		t.Fatalf("Cache-Control = %q, want public max-age=60", cacheControl)
	}
	if vary := recorder.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
	// The validator is weak on purpose: the aggregate states the instant of its
	// own derivation, and an annotation that moves on every request cannot be
	// part of a validator whose job is to confirm that the counts did not move
	// (RFC 9110 section 8.8.2).
	etag := recorder.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag = %q, want a weak quoted validator", etag)
	}

	body := recorder.Body.String()
	if strings.Contains(body, harness.ownerEmail) || strings.Contains(body, harness.otherEmail) || strings.Contains(body, harness.ownerID) || strings.Contains(body, harness.otherID) {
		t.Fatalf("SECURITY VIOLATION: aggregate leaked account data: %s", body)
	}
	aggregate := positionsDecode(t, recorder.Body.Bytes())
	positionsAssertExactKeys(t, aggregate, "participants_total", "suppressed", "initial", "current", "checked_at")
	if aggregate["participants_total"] != float64(2) || aggregate["suppressed"] != false {
		t.Fatalf("aggregate = %v, want 2 participants published", aggregate)
	}
	initial, _ := aggregate["initial"].(map[string]any)
	positionsAssertExactKeys(t, initial, "agree", "disagree", "undecided")
	if initial["agree"] != float64(1) || initial["disagree"] != float64(1) {
		t.Fatalf("initial distribution = %v, want one agree and one disagree", initial)
	}

	// Conditional revalidation answers 304 without a body.
	conditional := positionsRequest(http.MethodGet, aggregatePath, "", "")
	conditional.Header.Set("If-None-Match", etag)
	recorder = httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, conditional)
	if recorder.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("conditional body = %q, want empty", recorder.Body.String())
	}

	// The default policy withholds the same small sample.
	recorder = httptest.NewRecorder()
	harness.defaultMux.ServeHTTP(recorder, positionsRequest(http.MethodGet, aggregatePath, "", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("default policy status = %d, want 200", recorder.Code)
	}
	suppressed := positionsDecode(t, recorder.Body.Bytes())
	if suppressed["suppressed"] != true || suppressed["participants_total"] != float64(0) {
		t.Fatalf("default policy aggregate = %v, want every count withheld", suppressed)
	}
	suppressedInitial, _ := suppressed["initial"].(map[string]any)
	if suppressedInitial["agree"] != float64(0) || suppressedInitial["disagree"] != float64(0) || suppressedInitial["undecided"] != float64(0) {
		t.Fatalf("suppressed distribution leaked counts: %v", suppressedInitial)
	}
}

func TestPositionsBodyLimit(t *testing.T) {
	harness := setupPositionsHarness(t)

	huge := strings.Repeat("a", 70<<10)
	recorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(recorder, positionsRequest(http.MethodPost, "/api/v1/me/arenas/"+harness.arenaID+"/position", positionsOwnerToken,
		`{"position":"`+huge+`"}`))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status = %d, want 400 (body: %s)", recorder.Code, recorder.Body.String())
	}
	positionsAssertPrivateHeaders(t, recorder)
	if recorder.Body.Len() > 1024 {
		t.Fatalf("problem body echoes too much of the rejected payload: %d bytes", recorder.Body.Len())
	}
}
