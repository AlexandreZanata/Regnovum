package http_test

// P46-T11 — the staged seasons API serves the current season, one
// season and the allowlisted history on real PostgreSQL without
// mounting anything on the process router.
//
// The current ACTIVE book resolves with its dates and state; history
// lists allowlisted books in ordinal order without the explicitly
// inactive namespace. Missing sessions refuse with 401, the inactive
// namespace with 403, unknown books with 404 and suspended reads
// with 409; every answer is private no-store, and the same dates and
// ordinals read identically in pt and en. The route list and the
// OpenAPI fragment describe each other exactly. The suite runs on a
// disposable database and activates nothing: the package registers
// no route on import and the tests mount only a local mux.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/http"
	seasonpg "github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/postgres"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

const (
	seasonOwnerToken = "season-owner-session-token"
	seasonOtherToken = "season-other-session-token"
)

type seasonHarness struct {
	mux      http.Handler
	sec      *security.Manager
	ownerID  string
	otherID  string
	current  string
	archived string
}

func mustSeasonSecurity(t *testing.T) *security.Manager {
	t.Helper()
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
	return secMgr
}

func seasonAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('season-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return id
}

func seedLifecycleSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int, starts time.Time, stages [][2]string) {
	t.Helper()
	manifest, err := seasondomain.NewManifest(seasondomain.ManifestRequest{
		ID: key, Ordinal: ordinal, StartsAt: starts.UTC(),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	ends := manifest.StartsAt.Add(7776000 * time.Second)
	if _, err := db.Exec(ctx,
		`INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ($1, $2, $3, $4, 'v3', 'politica-inicial', 'fundadora', 'regente-tecnica', $5)`,
		key, ordinal, manifest.StartsAt, ends, manifest.Hash); err != nil {
		t.Fatalf("seed season %s: %v", key, err)
	}
	for _, stage := range stages {
		from := "NULL"
		if stage[0] != "" {
			from = "'" + stage[0] + "'"
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
			 VALUES ($1, `+from+`, $2, now())`, key, stage[1]); err != nil {
			t.Fatalf("stage %s of %s: %v", stage[1], key, err)
		}
	}
}

func newSeasonHarness(t *testing.T) (*seasonHarness, *dbtest.TestDB) {
	t.Helper()
	testDB := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	owner := seasonAccount(t, ctx, testDB)
	other := seasonAccount(t, ctx, testDB)
	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	seedLifecycleSeason(t, ctx, testDB, "temporada-contrato-a", 311, base,
		[][2]string{{"", "prepared"}, {"prepared", "active"}, {"active", "closing"}, {"closing", "sealed"}, {"sealed", "archived"}})
	seedLifecycleSeason(t, ctx, testDB, "temporada-contrato-b", 312, base.Add(7776000*time.Second),
		[][2]string{{"", "prepared"}, {"prepared", "active"}})

	reads, err := seasonpg.NewSeasonReader(testDB.Pool.Pool())
	if err != nil {
		t.Fatalf("NewSeasonReader: %v", err)
	}
	secMgr := mustSeasonSecurity(t)
	handler, err := adapterhttp.NewHandler(adapterhttp.HandlerConfig{Reads: reads, Security: secMgr})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if _, err := adapterhttp.NewHandler(adapterhttp.HandlerConfig{}); err == nil {
		t.Fatal("nil reads must refuse composition")
	}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case seasonOwnerToken:
			return security.AuthIdentity{AccountID: owner, SessionID: "session-owner"}, nil
		case seasonOtherToken:
			return security.AuthIdentity{AccountID: other, SessionID: "session-other"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})
	return &seasonHarness{
		mux: secMgr.AuthenticateMiddleware(validator)(mux), sec: secMgr,
		ownerID: owner, otherID: other,
		current: "temporada-contrato-b", archived: "temporada-contrato-a",
	}, testDB
}

func seasonRequest(t *testing.T, h *seasonHarness, method, path, token, locale string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	}
	if locale != "" {
		request.Header.Set("Accept-Language", locale)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func seasonDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return object
}

func assertSeasonNoStore(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
