// Tests of the P48-T03 declared-vs-mounted reach map: the process
// httptest server — not the constructor — decides what is mounted.
// A mounted route answers beyond the registry placeholder, so an
// applicable call never falls into a structural 404/501; a declared
// route no surface claims keeps the placeholder 404 of a module not
// composed yet. The map quality/frontend-surfaces.json records the
// outcome per environment, and P49 mounts the MVP families from it.
//
// No business machine changes here, no external provider is called
// and no real data is used: the journeys run over disposable
// dbtest PostgreSQL databases with the fakeemail sink, and the
// negative control builds a mux with the mounts removed.
package bootstrap_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// reachAnswer is one measured response of the process server.
type reachAnswer struct {
	status      int
	contentType string
	body        string
}

// reachGet issues one GET against the process server and returns
// the raw answer: status, content type and body. It never asserts,
// so the tests can tell a placeholder from a handler refusal.
func reachGet(t *testing.T, client *http.Client, server *httptest.Server, path string) reachAnswer {
	t.Helper()

	response, err := client.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	return reachAnswer{status: response.StatusCode, contentType: response.Header.Get("Content-Type"), body: string(body)}
}

// isPlaceholder404 answers whether the response is the registry
// placeholder of a route no surface claims: the stock Go 404 in
// text/plain. A handler 404 — a missing resource on a mounted
// route — renders the product document instead.
func isPlaceholder404(answer reachAnswer) bool {
	return answer.status == http.StatusNotFound &&
		strings.HasPrefix(answer.contentType, "text/plain") &&
		strings.Contains(answer.body, "404 page not found")
}

// TestMountedSurfacesAnswerBeyondPlaceholders drives the composed
// process the way a browser does and proves the mounted set: every
// applicable call answers beyond the placeholder, and the declared
// routes no surface claims keep it.
func TestMountedSurfacesAnswerBeyondPlaceholders(t *testing.T) {
	t.Parallel()

	journey := newParticipationJourney(t)
	client := browser(t)

	// The twelve account documents and the health probes answer
	// 200: they are mounted, not merely declared.
	for _, path := range []string{
		"/register", "/verify", "/login", "/logout", "/reset", "/reset/confirm",
		"/health/live", "/health/ready",
	} {
		answer := reachGet(t, client, journey.server, path)
		if isPlaceholder404(answer) {
			t.Errorf("GET %s answered the registry placeholder: the route is not mounted", path)
			continue
		}
		if answer.status == http.StatusNotImplemented {
			t.Errorf("GET %s answered 501: the route is not mounted", path)
			continue
		}
		if answer.status != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, answer.status)
		}
	}

	// An authorized call to an applicable operation never falls
	// into a structural 404/501: the signed-in person confirms a
	// position on a published Arena and the transition answers.
	const slug = "superficie-montada"
	participant := browser(t)
	participantID := signedIn(t, journey, participant, "surface-reach@example.test", "correct horse battery staple")
	publishedArena(t, journey, participantID, slug)
	if answer := reachGet(t, participant, journey.server, "/arenas/"+slug); answer.status != http.StatusOK {
		t.Fatalf("GET /arenas/%s status = %d, want 200", slug, answer.status)
	}
	confirmed := submit(t, participant, journey.server, "/arenas/"+slug+"/position", url.Values{
		"position":   {"undecided"},
		"csrf_token": {csrfToken(t, openPage(t, participant, journey.server, "/arenas/"+slug))},
	})
	if confirmed.StatusCode == http.StatusNotFound || confirmed.StatusCode == http.StatusNotImplemented {
		body, _ := io.ReadAll(confirmed.Body)
		t.Fatalf("POST /arenas/%s/position status = %d: the transition is not mounted (body: %.200s)", slug, confirmed.StatusCode, body)
	}
	if confirmed.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(confirmed.Body)
		t.Fatalf("POST /arenas/%s/position status = %d, want 303 (body: %.300s)", slug, confirmed.StatusCode, body)
	}

	// A 404 for a resource that does not exist is a handler
	// answer on a mounted route, not a missing route: the Arena
	// page renders its notice document instead of the placeholder.
	missing := reachGet(t, client, journey.server, "/arenas/this-arena-does-not-exist")
	if missing.status != http.StatusNotFound {
		t.Errorf("GET /arenas/this-arena-does-not-exist status = %d, want the handler 404", missing.status)
	} else if isPlaceholder404(missing) {
		t.Error("GET /arenas/this-arena-does-not-exist answered the placeholder: the page is not mounted")
	} else if !strings.HasPrefix(missing.contentType, "text/html") {
		t.Errorf("GET /arenas/this-arena-does-not-exist Content-Type = %q, want the handler notice document", missing.contentType)
	}

	// The declared routes no surface claims keep the placeholder:
	// JSON families P49 mounts, the unclaimed document and a staged
	// read are all answered by the module-not-composed-yet handler.
	for _, path := range []string{
		"/api/v1/arenas",
		"/api/v1/me/profile",
		"/api/v1/me/seasons/current",
		"/d/{slug}",
	} {
		if answer := reachGet(t, client, journey.server, path); !isPlaceholder404(answer) {
			t.Errorf("GET %s status = %d (%q): want the registry placeholder of an unmounted declaration", path, answer.status, answer.contentType)
		}
	}

	// An undeclared path answers the same placeholder status, and
	// that is why the status alone cannot tell them apart: the
	// declaration-vs-mounted distinction lives in the map, proven
	// by TestSurfacesMapMatchesComposition.
	if answer := reachGet(t, client, journey.server, "/api/v1/no-such-path"); !isPlaceholder404(answer) {
		t.Errorf("GET /api/v1/no-such-path status = %d: want the registry placeholder", answer.status)
	}
}

// TestRemovedMountAccusesTheGap is the negative control: the same
// probes that pass on the composed process accuse the gap once the
// mounts are removed, while the platform probes stay intact. A
// reach test that could not fail would prove nothing.
func TestRemovedMountAccusesTheGap(t *testing.T) {
	t.Parallel()

	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clockseed.NewClock())
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{}, nil)
	if err != nil {
		t.Fatalf("NewMuxWith() without surfaces: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := browser(t)

	for _, path := range []string{"/register", "/arenas/whatever-slug"} {
		if answer := reachGet(t, client, server, path); !isPlaceholder404(answer) {
			t.Errorf("GET %s on the mountless mux status = %d: want the placeholder accusing the removed mount", path, answer.status)
		}
	}
	if answer := reachGet(t, client, server, "/health/live"); answer.status != http.StatusOK {
		t.Errorf("GET /health/live on the mountless mux status = %d, want 200: the platform must survive the removed mounts", answer.status)
	}
}

// surfacesFile is the shape of quality/frontend-surfaces.json the
// composition test judges.
type surfacesFile struct {
	Schema int `json:"schema"`
	Counts struct {
		Declared        int `json:"declared"`
		Mounted         int `json:"mounted"`
		DeclarationOnly int `json:"declaration_only"`
		Validated       int `json:"validated"`
	} `json:"counts"`
	States   []string `json:"states"`
	Surfaces []struct {
		Name   string   `json:"name"`
		State  string   `json:"state"`
		Routes []string `json:"routes"`
	} `json:"surfaces"`
	P49Plan []string `json:"p49_plan"`
}

// surfacesContext carries the map with the live composition: the
// mounted routes the surfaces claim plus the platform probes,
// against the classified rows of the map.
type surfacesContext struct {
	surfaces      surfacesFile
	inventoryKeys map[string]bool
	composed      map[string]bool
	mapped        map[string]string
	stateBy       map[string]string
	mountedRows   int
	validatedRows int
}

func loadSurfacesContext(t *testing.T) *surfacesContext {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))

	raw, err := os.ReadFile(filepath.Join(root, "quality", "frontend-surfaces.json"))
	if err != nil {
		t.Fatalf("read surfaces map: %v", err)
	}
	var surfaces surfacesFile
	if err := json.Unmarshal(raw, &surfaces); err != nil {
		t.Fatalf("surfaces map is not JSON: %v", err)
	}
	if surfaces.Schema != 1 {
		t.Fatalf("surfaces schema = %d, want 1", surfaces.Schema)
	}

	rawRoutes, err := os.ReadFile(filepath.Join(root, "quality", "frontend-routes.json"))
	if err != nil {
		t.Fatalf("read routes inventory: %v", err)
	}
	var inventory struct {
		Routes []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(rawRoutes, &inventory); err != nil {
		t.Fatalf("routes inventory is not JSON: %v", err)
	}

	account, err := bootstrap.ComposeAccount(completeOptions(t))
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}
	participationOptions := completeOptions(t)
	participationOptions.CursorSecret = []byte(cursorSecret)
	participation, err := bootstrap.ComposeParticipation(participationOptions)
	if err != nil {
		t.Fatalf("ComposeParticipation() error = %v", err)
	}
	privacy, err := bootstrap.ComposeAccountPrivacy(participationOptions)
	if err != nil {
		t.Fatalf("ComposeAccountPrivacy() error = %v", err)
	}
	lifecycle, err := bootstrap.ComposeArenaLifecycle(participationOptions)
	if err != nil {
		t.Fatalf("ComposeArenaLifecycle() error = %v", err)
	}
	context := &surfacesContext{
		surfaces:      surfaces,
		inventoryKeys: make(map[string]bool, len(inventory.Routes)),
		composed:      make(map[string]bool),
		mapped:        make(map[string]string),
		stateBy:       make(map[string]string, len(surfaces.Surfaces)),
	}
	for _, route := range inventory.Routes {
		context.inventoryKeys[route.Method+" "+route.Path] = true
	}
	for _, route := range account.Routes() {
		context.composed[route.String()] = true
	}
	for _, route := range participation.Routes() {
		context.composed[route.String()] = true
	}
	for _, route := range privacy.Routes() {
		context.composed[route.String()] = true
	}
	for _, route := range lifecycle.Routes() {
		context.composed[route.String()] = true
	}
	debate, err := bootstrap.ComposeDebateAttribution(participationOptions)
	if err != nil {
		t.Fatalf("ComposeDebateAttribution() error = %v", err)
	}
	for _, route := range debate.Routes() {
		context.composed[route.String()] = true
	}
	entitlements, err := bootstrap.ComposeEntitlementReads(participationOptions)
	if err != nil {
		t.Fatalf("ComposeEntitlementReads() error = %v", err)
	}
	for _, route := range entitlements.Routes() {
		context.composed[route.String()] = true
	}
	// The billing writes compose over the synthetic gateway and secret in
	// this map like the process composes them over Stripe: the proof is the
	// guarded composition, and production refuses the incomplete one.
	writes, err := bootstrap.ComposeBillingWrites(participationOptions, billingWritesTestConfig(t))
	if err != nil {
		t.Fatalf("ComposeBillingWrites() error = %v", err)
	}
	for _, route := range writes.Routes() {
		context.composed[route.String()] = true
	}
	for _, route := range httpserver.HealthRoutes() {
		context.composed[route.String()] = true
	}
	for _, surface := range surfaces.Surfaces {
		context.stateBy[surface.Name] = surface.State
		for _, route := range surface.Routes {
			if prev, dup := context.mapped[route]; dup {
				t.Errorf("route %q mapped by %q and %q", route, prev, surface.Name)
				continue
			}
			context.mapped[route] = surface.Name
		}
		switch surface.State {
		case "mounted":
			context.mountedRows += len(surface.Routes)
		case "declaration":
		case "validated":
			context.validatedRows += len(surface.Routes)
			t.Errorf("surface %q claims validated routes: browser validation lands in P49+", surface.Name)
		default:
			t.Errorf("surface %q has unknown state %q", surface.Name, surface.State)
		}
	}
	return context
}

// TestSurfacesMapMatchesComposition ties the map to the code: every
// composed route is mapped by a mounted surface, and every other
// mapped route sits in a declaration.
func TestSurfacesMapMatchesComposition(t *testing.T) {
	t.Parallel()

	context := loadSurfacesContext(t)
	for route := range context.composed {
		owner, ok := context.mapped[route]
		if !ok {
			t.Errorf("composed route %q is missing from the map", route)
			continue
		}
		if context.stateBy[owner] != "mounted" {
			t.Errorf("composed route %q is mapped by %q, which is not mounted", route, owner)
		}
	}
	for route, owner := range context.mapped {
		if context.composed[route] {
			continue
		}
		if context.stateBy[owner] != "declaration" {
			t.Errorf("route %q is mapped by %q, which is neither the composition nor a declaration", route, owner)
		}
	}
}

// TestSurfacesMapCoversInventory proves the map classifies every
// inventory route exactly once with honest counts: 100 declared,
// 19 mounted, zero validated and a P49 plan naming what mounts
// next.
func TestSurfacesMapCoversInventory(t *testing.T) {
	t.Parallel()

	context := loadSurfacesContext(t)
	if len(context.mapped) != len(context.inventoryKeys) {
		t.Errorf("map classifies %d routes, inventory declares %d", len(context.mapped), len(context.inventoryKeys))
	}
	for key := range context.inventoryKeys {
		if _, ok := context.mapped[key]; !ok {
			t.Errorf("inventory route %s is missing from the map", key)
		}
	}
	if context.surfaces.Counts.Declared != len(context.inventoryKeys) || context.surfaces.Counts.Declared != 100 {
		t.Errorf("map declared = %d, want 100", context.surfaces.Counts.Declared)
	}
	if context.surfaces.Counts.Mounted != len(context.composed) || context.surfaces.Counts.Mounted != context.mountedRows {
		t.Errorf("map mounted = %d, composition serves %d, mounted rows hold %d", context.surfaces.Counts.Mounted, len(context.composed), context.mountedRows)
	}
	if context.surfaces.Counts.DeclarationOnly != len(context.inventoryKeys)-len(context.composed) {
		t.Errorf("map declaration_only = %d, want %d", context.surfaces.Counts.DeclarationOnly, len(context.inventoryKeys)-len(context.composed))
	}
	if context.surfaces.Counts.Validated != 0 || context.validatedRows != 0 {
		t.Errorf("map validated = %d, want 0: browser validation lands in P49+", context.surfaces.Counts.Validated)
	}
	if len(context.surfaces.P49Plan) == 0 {
		t.Error("map names no P49 mount plan")
	}
}
