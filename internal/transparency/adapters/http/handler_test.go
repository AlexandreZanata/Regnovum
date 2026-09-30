package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/http"
	transparencypg "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/application"
)

func setupTransparencyHarness(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	return setupTransparencyHarnessWithClock(t, clockseed.NewClock())
}

// harnessClock is what the harness needs from a clock: the handler accepts any
// implementation of it.
type harnessClock interface{ Now() time.Time }

// setupTransparencyHarnessWithClock wires the harness over a given clock, which
// lets a test move time between two reads of the same metrics.
func setupTransparencyHarnessWithClock(t *testing.T, clock harnessClock) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := transparencypg.NewRepository(pool)

	derive, err := application.NewDeriveMetricsUseCase(repo)
	if err != nil {
		t.Fatalf("NewDeriveMetricsUseCase: %v", err)
	}
	templates, err := adapterhttp.NewTemplates()
	if err != nil {
		t.Fatalf("NewTemplates: %v", err)
	}
	handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{
		Derive:    derive,
		Templates: templates,
		Clock:     clock,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux, pool
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

// TestTransparencyValidatorsSurviveAMovingInstant is the regression test of the
// cache defect, on both public documents: the page and the JSON state the
// instant they were derived at, the clock moves between the reads here, and the
// metrics did not move — so a revalidation must still answer 304 without a
// body. A validator computed over the annotation instead of the metrics answers
// 200, which is what used to happen under load and what made these documents
// uncacheable in production.
func TestTransparencyValidatorsSurviveAMovingInstant(t *testing.T) {
	mux, _ := setupTransparencyHarnessWithClock(t, newSteppingClock(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)))
	for _, route := range []string{"/api/v1/public/transparency", "/transparency"} {
		t.Run(route, func(t *testing.T) {
			first := httptest.NewRecorder()
			mux.ServeHTTP(first, httptest.NewRequest(http.MethodGet, route, nil))
			if first.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", first.Code, first.Body.String())
			}
			etag := first.Header().Get("ETag")
			if etag == "" {
				t.Fatal("validator is missing on a public document")
			}
			if !strings.HasPrefix(etag, "W/\"") {
				t.Fatalf("ETag = %q, want the weak validator of an annotated representation", etag)
			}

			second := httptest.NewRecorder()
			mux.ServeHTTP(second, httptest.NewRequest(http.MethodGet, route, nil))
			if first.Body.String() == second.Body.String() {
				t.Fatal("the document did not move between the reads: the test would pass without proving anything")
			}
			if second.Header().Get("ETag") != etag {
				t.Fatalf("ETag moved with the annotation: %q then %q", etag, second.Header().Get("ETag"))
			}

			revalidation := httptest.NewRequest(http.MethodGet, route, nil)
			revalidation.Header.Set("If-None-Match", etag)
			notModified := httptest.NewRecorder()
			mux.ServeHTTP(notModified, revalidation)
			if notModified.Code != http.StatusNotModified {
				t.Fatalf("revalidation status = %d, want 304 with the metrics unchanged (body: %s)", notModified.Code, notModified.Body.String())
			}
			if notModified.Body.Len() != 0 {
				t.Fatalf("304 body has %d bytes, want empty", notModified.Body.Len())
			}
		})
	}
}

func decodeTransparencyJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return document
}

func assertPublicCache(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	cacheControl := recorder.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "public") || !strings.Contains(cacheControl, "max-age=3600") {
		t.Fatalf("Cache-Control = %q, want public, max-age=3600", cacheControl)
	}
	if strings.Contains(cacheControl, "no-store") {
		t.Fatalf("Cache-Control = %q, must not be no-store on public documents", cacheControl)
	}
	// Public documents carry a validator; the ones whose body states the
	// instant of its own derivation carry a weak one, because that annotation
	// moves on every request and cannot be part of the representation identity
	// (RFC 9110 section 8.8.2).
	if etag := recorder.Header().Get("ETag"); etag == "" {
		t.Fatal("public document must carry a validator")
	}
}

var transparencyMetricKeys = []string{
	"eligible_accounts",
	"arenas_published", "arenas_closed", "arenas_restricted", "arenas_removed",
	"arguments_published", "arguments_withdrawn",
	"position_changes",
	"attributions_valid", "attributions_invalidated", "influenced_authors",
	"ink_free_granted", "ink_free_expired", "ink_free_consumed",
	"ink_purchased_granted", "ink_purchased_consumed", "ink_refunded", "ink_admin_adjusted",
	"passes_purchase_granted", "passes_member_granted", "passes_consumed",
	"reports_filed", "actions_recorded", "appeals_filed", "appeals_reversed",
}

func TestTransparencyJSONContractCacheAndPrivacy(t *testing.T) {
	mux, _ := setupTransparencyHarness(t)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/public/transparency", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertPublicCache(t, recorder)

	document := decodeTransparencyJSON(t, recorder.Body.Bytes())
	for _, key := range []string{"methodology_version", "period_start", "period_end", "timezone", "updated_at", "metrics"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("document is missing key %q (keys: %v)", key, document)
		}
	}
	if document["methodology_version"] != float64(1) || document["timezone"] != "UTC" {
		t.Fatalf("envelope = %v, want version 1 in UTC", document)
	}
	for _, key := range []string{"period_start", "period_end", "updated_at"} {
		value, _ := document[key].(string)
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			t.Fatalf("%s = %q, want RFC 3339 UTC", key, value)
		}
	}
	metrics, _ := document["metrics"].(map[string]any)
	if len(metrics) != len(transparencyMetricKeys) {
		t.Fatalf("metrics has %d entries, want exactly %d", len(metrics), len(transparencyMetricKeys))
	}
	for _, key := range transparencyMetricKeys {
		if _, ok := metrics[key]; !ok {
			t.Fatalf("metrics is missing %q", key)
		}
	}

	serialized := recorder.Body.String()
	for _, marker := range []string{"@", "cus_", "cs_test", "cs_live", "sub_", "pi_", "192.168", "position\":", "attributor"} {
		if strings.Contains(serialized, marker) {
			t.Fatalf("public document leaks private marker %q", marker)
		}
	}

	// Revalidation is cheap: the ETag answers 304 without a body.
	etag := recorder.Header().Get("ETag")
	revalidated := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/public/transparency", nil)
	request.Header.Set("If-None-Match", etag)
	mux.ServeHTTP(revalidated, request)
	if revalidated.Code != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want 304", revalidated.Code)
	}
	if revalidated.Body.Len() != 0 {
		t.Fatalf("304 body has %d bytes, want empty", revalidated.Body.Len())
	}
}

func TestTransparencyJSONLowCountSuppresses(t *testing.T) {
	mux, _ := setupTransparencyHarness(t)

	// Explicit window over an empty database: every family stays below the
	// low-count threshold, so the document reports zeros.
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/public/transparency?period_start=2026-09-10T00:00:00Z&period_end=2026-09-11T00:00:00Z&timezone=UTC", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", recorder.Code, recorder.Body.String())
	}
	document := decodeTransparencyJSON(t, recorder.Body.Bytes())
	metrics, _ := document["metrics"].(map[string]any)
	for key, value := range metrics {
		if value != float64(0) {
			t.Fatalf("metric %s = %v, want suppressed zero on empty sources", key, value)
		}
	}
	if document["timezone"] != "UTC" {
		t.Fatalf("timezone = %v, want echoed UTC", document["timezone"])
	}
}

func TestTransparencyJSONRejectsIncoherentWindows(t *testing.T) {
	mux, _ := setupTransparencyHarness(t)

	for _, target := range []string{
		"/api/v1/public/transparency?period_start=2026-09-11T00:00:00Z&period_end=2026-09-10T00:00:00Z",
		"/api/v1/public/transparency?period_start=not-a-date&period_end=2026-09-11T00:00:00Z",
		"/api/v1/public/transparency?period_start=2026-09-10T00:00:00Z&period_end=2026-09-11T00:00:00Z&timezone=Mars/Olympus",
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, want 400 (body: %s)", target, recorder.Code, recorder.Body.String())
		}
		if contentType := recorder.Header().Get("Content-Type"); contentType != "application/problem+json" {
			t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
		}
	}
}

func TestTransparencyHTMLLocalizesAndCaches(t *testing.T) {
	mux, _ := setupTransparencyHarness(t)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/transparency", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertPublicCache(t, recorder)
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `<html lang="pt-BR"`) || !strings.Contains(body, "Transparência da plataforma") {
		t.Fatalf("default document must render pt-BR without reflection: %.200s", body)
	}

	english := httptest.NewRecorder()
	mux.ServeHTTP(english, httptest.NewRequest(http.MethodGet, "/transparency?locale=en-US", nil))
	if english.Code != http.StatusOK {
		t.Fatalf("english status = %d", english.Code)
	}
	if !strings.Contains(english.Body.String(), `<html lang="en-US"`) || !strings.Contains(english.Body.String(), "Platform transparency") {
		t.Fatalf("english document must localize: %.200s", english.Body.String())
	}

	// Unknown locales fall back without reflection.
	injected := httptest.NewRecorder()
	mux.ServeHTTP(injected, httptest.NewRequest(http.MethodGet, "/transparency?locale=<script>alert(1)</script>", nil))
	if injected.Code != http.StatusOK {
		t.Fatalf("injected locale status = %d", injected.Code)
	}
	if strings.Contains(injected.Body.String(), "<script>alert(1)</script>") {
		t.Fatal("unknown locale must never reflect")
	}
	if !strings.Contains(injected.Body.String(), `<html lang="pt-BR"`) {
		t.Fatal("unknown locale must fall back to the default")
	}

	for _, marker := range []string{"@", "cus_", "cs_test", "sub_", "192.168"} {
		if strings.Contains(body, marker) {
			t.Fatalf("HTML document leaks private marker %q", marker)
		}
	}
}

func TestTransparencyHTMLSeededCounts(t *testing.T) {
	mux, pool := setupTransparencyHarness(t)
	ctx := context.Background()

	creator, err := platformpg.New(pool).CreateAccount(ctx, platformpg.CreateAccountParams{Email: "transparency-html@arena.example.com", Status: "active"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
			VALUES ($1, 'HTML probe statement', 'technology', 'pt-BR', 'published', $2, now())`,
			creator.ID, fmt.Sprintf("transparency-html-%02d", i)); err != nil {
			t.Fatalf("seed arena: %v", err)
		}
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/transparency", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "<td>arenas_published</td><td>6</td>") {
		t.Fatalf("document must publish the reconstructed count: %.500s", body)
	}
}
