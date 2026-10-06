// Tests of the public transparency surface composed in the process
// (P49-T08): the JSON metrics document, the localized HTML document and
// the versioned public Arena export run on the platform mux over
// disposable PostgreSQL, driven by real HTTP with the same pool and clock
// the account journey wrote to.
//
// Everything here is public: no login exists, no session is opened and no
// cookie is verified. The proof is that the documents carry suppressed
// integer counts only (small samples report zero), the withdrawn content
// never serializes, the locale negotiates against the allowlist without
// reflecting unknown values, the public cache validators round-trip, and
// no seeded private fact (email, account identifier) leaks into any body.
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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// transparencyProbeEmail is the distinctive sender the leak assertions hunt
// for: it must appear in no public document.
const transparencyProbeEmail = "transparency-probe-creator@example.test"

// transparencyJourney is the transparency surface on a real listener, over
// disposable PostgreSQL like `arena server` mounts it.
type transparencyJourney struct {
	surface *bootstrap.TransparencySurface
	server  *httptest.Server
	pool    *pgxpool.Pool
}

// newTransparencyJourney composes the surface the way the process does: one
// pool, one clock, the shared cursor secret.
func newTransparencyJourney(t *testing.T) *transparencyJourney {
	t.Helper()

	database := dbtest.New(t)
	pool := database.Pool.Pool()
	options := bootstrap.Options{
		Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool: pool, Clock: clockseed.NewClock(), Random: clockseed.NewRandom(),
		CursorSecret: []byte(cursorSecret),
	}
	surface, err := bootstrap.ComposeTransparency(options)
	if err != nil {
		t.Fatalf("ComposeTransparency() error = %v", err)
	}
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clockseed.NewClock())
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{},
		[]httpserver.Surface{surface.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &transparencyJourney{surface: surface, server: server, pool: pool}
}

// transparencyGet issues one GET with the given headers and returns status,
// headers and body.
func transparencyGet(t *testing.T, client *http.Client, server *httptest.Server, path string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
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
	return response.StatusCode, response.Header, raw
}

// seedTransparencyWorld writes the small world the documents derive from:
// two verified accounts, one published Arena, two published arguments (one
// with a source), one withdrawn argument carrying a secret, and one
// position per account. Every cell stays below the suppression threshold,
// so every published count must read zero.
func seedTransparencyWorld(t *testing.T, pool *pgxpool.Pool) (creatorID, arenaID, publishedID, withdrawnID string) {
	t.Helper()

	ctx := context.Background()
	queries := platformpg.New(pool)
	creator := seedTransparencyAccount(t, ctx, queries, transparencyProbeEmail)
	participant := seedTransparencyAccount(t, ctx, queries, "transparency-probe-participant@example.test")

	creatorText := transparencyAccountText(t, ctx, pool, creator.ID)
	participantText := transparencyAccountText(t, ctx, pool, participant.ID)
	now := time.Now().UTC().Truncate(time.Second)
	var arenaText string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, context, category, language, status, slug, published_at)
		VALUES ($1, 'Transparency composition probe statement', 'Transparency probe context', 'technology', 'pt-BR', 'published', 'transparency-probe-arena', $2)
		RETURNING id::text`, creator.ID, now.Add(-time.Hour)).Scan(&arenaText); err != nil {
		t.Fatalf("seed arena: %v", err)
	}
	published := seedTransparencyArgument(t, ctx, pool, transparencyArgumentSeed{
		arenaID: arenaText, authorID: creatorText, relation: "support",
		content: "Transparency probe published content", status: "published", created: now.Add(-30 * time.Minute),
	})
	seedTransparencyArgument(t, ctx, pool, transparencyArgumentSeed{
		arenaID: arenaText, authorID: participantText, relation: "oppose",
		content: "Transparency probe second content", status: "published", created: now.Add(-20 * time.Minute),
	})
	withdrawn := seedTransparencyArgument(t, ctx, pool, transparencyArgumentSeed{
		arenaID: arenaText, authorID: creatorText, relation: "support",
		content: "withdrawn content that must never serialize", status: "withdrawn", created: now.Add(-10 * time.Minute),
	})
	seedTransparencySource(t, ctx, pool, published, "https://example.test/transparency-source", "Transparency probe source")
	seedTransparencyPosition(t, ctx, pool, arenaText, creatorText)
	seedTransparencyPosition(t, ctx, pool, arenaText, participantText)
	return creatorText, arenaText, published, withdrawn
}

// seedTransparencyAccount creates one verified active account.
func seedTransparencyAccount(t *testing.T, ctx context.Context, queries *platformpg.Queries, email string) platformpg.AppAccount {
	t.Helper()

	account, err := queries.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	if _, err := queries.SetEmailVerified(ctx, account.ID); err != nil {
		t.Fatalf("verify account %s: %v", email, err)
	}
	return account
}

// transparencyArgumentSeed is one argument row: the bundle keeps the seeder
// inside the parameter budget instead of widening it per column.
type transparencyArgumentSeed struct {
	arenaID  string
	authorID string
	relation string
	content  string
	status   string
	created  time.Time
}

// seedTransparencyArgument writes one argument row and returns its id.
func seedTransparencyArgument(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seed transparencyArgumentSeed) string {
	t.Helper()

	var withdrawn any
	if seed.status == "withdrawn" {
		withdrawn = seed.created.Add(time.Hour)
	}
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments
			(arena_id, author_id, relation, content, content_hash, grapheme_cost, status, created_at, updated_at, withdrawn_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'transparency-probe-hash', 10, $5, $6, $6, $7)
		RETURNING id::text`, seed.arenaID, seed.authorID, seed.relation, seed.content, seed.status, seed.created, withdrawn).Scan(&id); err != nil {
		t.Fatalf("seed argument %s: %v", seed.content, err)
	}
	return id
}

// seedTransparencySource attaches one public source to an argument.
func seedTransparencySource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, argumentID, url, description string) {
	t.Helper()

	if _, err := pool.Exec(ctx, `
		INSERT INTO app.argument_sources (argument_id, url, description, created_at)
		VALUES ($1::uuid, $2, $3, now())`, argumentID, url, description); err != nil {
		t.Fatalf("seed source %s: %v", url, err)
	}
}

// transparencyAccountText renders one account identifier in canonical
// text: the export leak assertion compares rendered text, not driver
// values.
func transparencyAccountText(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id any) string {
	t.Helper()

	var text string
	if err := pool.QueryRow(ctx, `SELECT $1::uuid::text`, id).Scan(&text); err != nil {
		t.Fatalf("read account id: %v", err)
	}
	return text
}

// seedTransparencyPosition records one confirmed position.
func seedTransparencyPosition(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, accountID string) {
	t.Helper()

	if _, err := pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position, version)
		VALUES ($1::uuid, $2::uuid, 'agree', 'agree', 1)`, arenaID, accountID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
}

// metricsDocument is the JSON shape the metrics route serves.
type metricsDocument struct {
	MethodologyVersion int              `json:"methodology_version"`
	PeriodStart        string           `json:"period_start"`
	PeriodEnd          string           `json:"period_end"`
	Timezone           string           `json:"timezone"`
	UpdatedAt          string           `json:"updated_at"`
	Metrics            map[string]int64 `json:"metrics"`
}

// decodeMetrics requires a 200 JSON metrics document.
func decodeMetrics(t *testing.T, status int, header http.Header, raw []byte) metricsDocument {
	t.Helper()

	if status != http.StatusOK {
		t.Fatalf("GET metrics status = %d, want 200 (body: %.300s)", status, raw)
	}
	if contentType := header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("GET metrics Content-Type = %q, want application/json", contentType)
	}
	var document metricsDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("metrics is not JSON: %v (body: %.300s)", err, raw)
	}
	return document
}

// requirePublicCache requires the restricted public cache contract: a
// public directive, a strong-or-weak validator and a 304 round-trip.
func requirePublicCache(t *testing.T, status int, header http.Header, raw []byte, client *http.Client, server *httptest.Server, path string) {
	t.Helper()

	if status != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200 (body: %.300s)", path, status, raw)
	}
	if cache := header.Get("Cache-Control"); !strings.Contains(cache, "public") {
		t.Errorf("GET %s Cache-Control = %q, want a public directive", path, cache)
	}
	etag := header.Get("ETag")
	if etag == "" {
		t.Fatalf("GET %s carries no ETag", path)
	}
	conditionalStatus, _, _ := transparencyGet(t, client, server, path, map[string]string{"If-None-Match": etag})
	if conditionalStatus != http.StatusNotModified {
		t.Errorf("GET %s with If-None-Match status = %d, want 304", path, conditionalStatus)
	}
}

// TestTransparencyMetricsServesSuppressedSnapshot proves the JSON document
// over real HTTP: the requested period echoes exactly, the methodology
// version is pinned, every small-sample count reads zero, an invalid
// window is a stable refusal, and no seeded email leaks into the body.
func TestTransparencyMetricsServesSuppressedSnapshot(t *testing.T) {
	journey := newTransparencyJourney(t)
	seedTransparencyWorld(t, journey.pool)
	client := browser(t)

	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-48 * time.Hour).Format(time.RFC3339)
	end := now.Format(time.RFC3339)
	path := "/api/v1/public/transparency?period_start=" + start + "&period_end=" + end
	status, header, raw := transparencyGet(t, client, journey.server, path, nil)
	document := decodeMetrics(t, status, header, raw)

	if document.MethodologyVersion != 1 {
		t.Errorf("methodology_version = %d, want 1", document.MethodologyVersion)
	}
	if document.PeriodStart != start {
		t.Errorf("period_start = %q, want %q", document.PeriodStart, start)
	}
	if document.PeriodEnd != end {
		t.Errorf("period_end = %q, want %q", document.PeriodEnd, end)
	}
	if document.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", document.Timezone)
	}
	if document.UpdatedAt == "" {
		t.Error("metrics document carries no updated_at")
	}
	for _, code := range []string{"eligible_accounts", "arenas_published", "arguments_published", "arguments_withdrawn", "position_changes"} {
		value, ok := document.Metrics[code]
		if !ok {
			t.Errorf("metrics document omits %q", code)
			continue
		}
		if value != 0 {
			t.Errorf("metric %q = %d, want 0: the sample is below the suppression threshold", code, value)
		}
	}
	for code, value := range document.Metrics {
		if value != 0 {
			t.Errorf("metric %q = %d, want 0: no exact small count may publish", code, value)
		}
	}
	if strings.Contains(string(raw), transparencyProbeEmail) {
		t.Error("metrics body leaks the seeded account email")
	}
	requirePublicCache(t, status, header, raw, client, journey.server, path)

	if status, _, raw := transparencyGet(t, client, journey.server, "/api/v1/public/transparency?period_start=not-a-date&period_end="+end, nil); status != http.StatusBadRequest || !strings.Contains(string(raw), "invalid_period") {
		t.Errorf("invalid period status = %d (body: %.200s), want 400 invalid_period", status, raw)
	}
}

// TestTransparencyMetricsDefaultWindow proves the undated call serves the
// trailing thirty days ending at the UTC day boundary, so the same report
// stays reproducible for the whole reporting day.
func TestTransparencyMetricsDefaultWindow(t *testing.T) {
	journey := newTransparencyJourney(t)
	client := browser(t)

	status, header, raw := transparencyGet(t, client, journey.server, "/api/v1/public/transparency", nil)
	document := decodeMetrics(t, status, header, raw)

	start, err := time.Parse(time.RFC3339, document.PeriodStart)
	if err != nil {
		t.Fatalf("period_start %q is not RFC3339: %v", document.PeriodStart, err)
	}
	end, err := time.Parse(time.RFC3339, document.PeriodEnd)
	if err != nil {
		t.Fatalf("period_end %q is not RFC3339: %v", document.PeriodEnd, err)
	}
	if end.Sub(start) != 30*24*time.Hour {
		t.Errorf("default window = %v, want exactly 720h", end.Sub(start))
	}
	if end.Hour() != 0 || end.Minute() != 0 || end.Second() != 0 {
		t.Errorf("default window ends at %v, want a UTC midnight boundary", end)
	}
}

// TestTransparencyDocumentServesLocalizedHTML proves the HTML document over
// real HTTP: the explicit locale wins, the header negotiates against the
// allowlist, unknown values fall back without being reflected, the cache
// stays public with a language variance, and no seeded email leaks.
func TestTransparencyDocumentServesLocalizedHTML(t *testing.T) {
	journey := newTransparencyJourney(t)
	seedTransparencyWorld(t, journey.pool)
	client := browser(t)

	status, header, raw := transparencyGet(t, client, journey.server, "/transparency?locale=pt-BR", nil)
	requirePublicCache(t, status, header, raw, client, journey.server, "/transparency?locale=pt-BR")
	if contentType := header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("GET /transparency Content-Type = %q, want text/html", contentType)
	}
	if !strings.Contains(string(raw), `<html lang="pt-BR"`) {
		t.Errorf("pt-BR document does not declare its language (body: %.300s)", raw)
	}
	if vary := header.Get("Vary"); !strings.Contains(vary, "Accept-Language") {
		t.Errorf("GET /transparency Vary = %q, want the language variance", vary)
	}

	status, _, raw = transparencyGet(t, client, journey.server, "/transparency", map[string]string{"Accept-Language": "en-US"})
	if status != http.StatusOK || !strings.Contains(string(raw), `<html lang="en-US"`) {
		t.Errorf("Accept-Language en-US status = %d without an en-US document (body: %.300s)", status, raw)
	}

	status, _, raw = transparencyGet(t, client, journey.server, "/transparency?locale=xx-unknown", map[string]string{"Accept-Language": "xx-unknown"})
	if status != http.StatusOK {
		t.Fatalf("unknown locale status = %d, want the 200 fallback", status)
	}
	if strings.Contains(string(raw), "xx-unknown") {
		t.Error("unknown locale value is reflected in the document")
	}
	if !strings.Contains(string(raw), `<html lang="pt-BR"`) {
		t.Errorf("unknown locale did not fall back to the product default (body: %.300s)", raw)
	}

	status, _, raw = transparencyGet(t, client, journey.server, "/transparency?locale=en-US", map[string]string{"Accept-Language": "pt-BR"})
	if status != http.StatusOK || !strings.Contains(string(raw), `<html lang="en-US"`) {
		t.Errorf("explicit locale lost to the header (body: %.300s)", raw)
	}

	if strings.Contains(string(raw), transparencyProbeEmail) {
		t.Error("HTML document leaks the seeded account email")
	}
}

// exportDocument is the JSON shape the export route serves.
type exportDocument struct {
	SchemaVersion int `json:"schema_version"`
	Arena         struct {
		Slug      string `json:"slug"`
		Statement string `json:"statement"`
		Status    string `json:"status"`
	} `json:"arena"`
	Positions struct {
		Suppressed bool `json:"suppressed"`
	} `json:"positions"`
	Arguments struct {
		Items []struct {
			ID      string  `json:"id"`
			Content *string `json:"content"`
			Status  string  `json:"status"`
			Sources []struct {
				URL string `json:"url"`
			} `json:"sources"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	} `json:"arguments"`
}

// TestTransparencyExportServesPublicArena proves the versioned export over
// real HTTP: the published Arena serves with suppressed aggregates, the
// withdrawn content stays null with no sources, unknown Arenas answer 404
// without reflection, forged cursors and bad limits are stable refusals,
// and neither the seeded email nor the creator identifier leaks.
func TestTransparencyExportServesPublicArena(t *testing.T) {
	journey := newTransparencyJourney(t)
	creatorID, arenaID, publishedID, withdrawnID := seedTransparencyWorld(t, journey.pool)
	client := browser(t)

	path := "/api/v1/arenas/" + arenaID + "/export"
	status, header, raw := transparencyGet(t, client, journey.server, path, nil)
	requirePublicCache(t, status, header, raw, client, journey.server, path)
	if contentType := header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("export Content-Type = %q, want application/json", contentType)
	}
	var document exportDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("export is not JSON: %v (body: %.300s)", err, raw)
	}
	if document.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", document.SchemaVersion)
	}
	if document.Arena.Slug != "transparency-probe-arena" || document.Arena.Status != "published" {
		t.Errorf("export arena = %+v, want the published probe arena", document.Arena)
	}
	if !document.Positions.Suppressed {
		t.Error("export positions are not suppressed for the two-person sample")
	}
	byID := make(map[string]struct {
		content *string
		sources int
		status  string
	})
	for _, item := range document.Arguments.Items {
		byID[item.ID] = struct {
			content *string
			sources int
			status  string
		}{content: item.Content, sources: len(item.Sources), status: item.Status}
	}
	published, ok := byID[publishedID]
	if !ok {
		t.Fatalf("export omits the published argument %s", publishedID)
	}
	if published.content == nil || *published.content != "Transparency probe published content" {
		t.Errorf("published argument content = %v, want the probe text", published.content)
	}
	if published.sources != 1 {
		t.Errorf("published argument carries %d sources, want 1", published.sources)
	}
	withdrawn, ok := byID[withdrawnID]
	if !ok {
		t.Fatalf("export omits the withdrawn argument %s", withdrawnID)
	}
	if withdrawn.content != nil {
		t.Errorf("withdrawn argument serializes content %q", *withdrawn.content)
	}
	if withdrawn.sources != 0 {
		t.Errorf("withdrawn argument carries %d sources, want none", withdrawn.sources)
	}
	if strings.Contains(string(raw), transparencyProbeEmail) {
		t.Error("export body leaks the seeded account email")
	}
	if strings.Contains(string(raw), creatorID) {
		t.Error("export body leaks the creator account identifier")
	}

	unknown := "00000000-0000-0000-0000-000000000000"
	if status, _, raw := transparencyGet(t, client, journey.server, "/api/v1/arenas/"+unknown+"/export", nil); status != http.StatusNotFound || !strings.Contains(string(raw), "arena_not_found") {
		t.Errorf("unknown arena status = %d (body: %.200s), want 404 arena_not_found", status, raw)
	}
	if status, _, raw := transparencyGet(t, client, journey.server, path+"?cursor=forged-cursor", nil); status != http.StatusBadRequest || !strings.Contains(string(raw), "invalid_cursor") {
		t.Errorf("forged cursor status = %d (body: %.200s), want 400 invalid_cursor", status, raw)
	}
	if status, _, raw := transparencyGet(t, client, journey.server, path+"?limit=nope", nil); status != http.StatusBadRequest || !strings.Contains(string(raw), "invalid_limit") {
		t.Errorf("bad limit status = %d (body: %.200s), want 400 invalid_limit", status, raw)
	}
}

// TestComposeTransparencyFailsClosed proves the composition refuses every
// incomplete edge at once, rejects a weak cursor secret, and never mounts
// twice: a public surface that served half its documents would look
// healthy while forging the other half.
func TestComposeTransparencyFailsClosed(t *testing.T) {
	complete := func() bootstrap.Options {
		return bootstrap.Options{
			Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
			Pool: dbtest.New(t).Pool.Pool(), Clock: clockseed.NewClock(), Random: clockseed.NewRandom(),
			CursorSecret: []byte(cursorSecret),
		}
	}
	cases := map[string]func(*bootstrap.Options){
		"logger":        func(options *bootstrap.Options) { options.Logger = nil },
		"clock":         func(options *bootstrap.Options) { options.Clock = nil },
		"pool":          func(options *bootstrap.Options) { options.Pool = nil },
		"cursor secret": func(options *bootstrap.Options) { options.CursorSecret = nil },
		"environment":   func(options *bootstrap.Options) { options.Env = "stage" },
	}
	for name, mutate := range cases {
		options := complete()
		mutate(&options)
		if _, err := bootstrap.ComposeTransparency(options); !errors.Is(err, bootstrap.ErrIncompleteComposition) {
			t.Errorf("ComposeTransparency without %s error = %v, want ErrIncompleteComposition", name, err)
		}
	}

	weak := complete()
	weak.CursorSecret = []byte("short")
	if _, err := bootstrap.ComposeTransparency(weak); !errors.Is(err, bootstrap.ErrIncompleteComposition) {
		t.Errorf("ComposeTransparency with a weak cursor secret error = %v, want ErrIncompleteComposition", err)
	}

	surface, err := bootstrap.ComposeTransparency(complete())
	if err != nil {
		t.Fatalf("ComposeTransparency() error = %v", err)
	}
	want := map[string]bool{
		"GET /api/v1/public/transparency": false,
		"GET /transparency":               false,
		"GET /api/v1/arenas/{id}/export":  false,
	}
	for _, route := range surface.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; !ok {
			t.Errorf("transparency mounts unexpected route %q", key)
			continue
		}
		want[key] = true
		if strings.Contains(route.Path, "metrics") || strings.Contains(route.Path, "pprof") || strings.Contains(route.Path, "debug") {
			t.Errorf("transparency mounts process internals at %q", key)
		}
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("transparency does not mount %q", key)
		}
	}
	if err := surface.Mount(nil); !errors.Is(err, bootstrap.ErrIncompleteComposition) {
		t.Errorf("Mount(nil) error = %v, want ErrIncompleteComposition", err)
	}
	mux := http.NewServeMux()
	if err := surface.Mount(mux); err != nil {
		t.Fatalf("first Mount() error = %v", err)
	}
	if err := surface.Mount(mux); err == nil {
		t.Error("second Mount() registered the surface again")
	}
}
