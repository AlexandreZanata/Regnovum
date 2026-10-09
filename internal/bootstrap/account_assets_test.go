// Tests of the served shell and authentication assets (P50-T04): the
// process serves the account documents and the fingerprinted build they
// reference — the same composition `arena server` performs — so a page
// whose stylesheet or module answers 404 is caught here, not by a person.
//
// Every HTML operation of the account journey is a real form or a real
// navigation: no JSON fetch is invented for a document a browser fills in,
// and a refusal (missing CSRF) answers the localized page, never a problem
// document a browser would display raw.
package bootstrap_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// assetJourney is the account surface plus the fingerprinted build it
// references, mounted on the platform router like `arena server` mounts
// them: the static build first, the journey beside it, inside the request
// id, locale and security layers.
type assetJourney struct {
	server *httptest.Server
}

// newAssetJourney composes exactly like `arena server`: one pool, one
// shared security boundary, the manifest read back from the build
// directory, and the static build served from that same directory.
func newAssetJourney(t *testing.T) *assetJourney {
	t.Helper()

	manifest := manifestFixture(t)
	frontend, err := assets.NewServer(assets.Config{
		Directory: "testdata/assets",
		Manifest:  manifest,
	})
	if err != nil {
		t.Fatalf("assets.NewServer(testdata/assets) error = %v", err)
	}

	database := dbtest.New(t)
	manager, err := security.New(security.Options{Env: config.EnvTest, Clock: clockseed.NewClock(), Random: clockseed.NewRandom()})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}
	surface, err := bootstrap.ComposeAccount(bootstrap.Options{
		Env:      config.EnvTest,
		Logger:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool:     database.Pool.Pool(),
		Clock:    clockseed.NewClock(),
		Random:   clockseed.NewRandom(),
		Assets:   manifest,
		Security: manager,
	})
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clockseed.NewClock())
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{}, []httpserver.Surface{
		{Static: true, Register: frontend.Mount},
		surface.Surface(),
	})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &assetJourney{server: server}
}

// assetReference matches one address a document loads: a stylesheet link
// or the entry module. The allowlist serves exactly these and nothing
// else, so collecting them from the rendered page is what proves no page
// references a missing asset.
var assetReference = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)

// formAction matches the one state-changing form of a journey page.
var formAction = regexp.MustCompile(`<form[^>]*method="POST"[^>]*action="([^"]+)"`)

// TestAccountShellAndAuthAssetsAreServed drives every HTML operation of
// the account journey as a deep link — a fresh visitor opening the
// address directly — and proves the shell, the fingerprinted assets and
// the real forms arrive together: 200 text/html documents with private
// no-store caching, a CSP without inline execution, the skip link, the
// current-page mark and the footer of the shell, one entry module and six
// stylesheets, every referenced address answering 200 with its media type
// and cache policy, and every page carrying the POST form the middleware
// will verify.
func TestAccountShellAndAuthAssetsAreServed(t *testing.T) {
	t.Parallel()

	journey := newAssetJourney(t)

	pages := []string{"/register", "/verify", "/login", "/logout", "/reset", "/reset/confirm"}
	referenced := map[string]bool{}
	for _, page := range pages {
		// A fresh client per page: a deep link carries no session, and the
		// document must stand on its own before any script runs.
		client := browser(t)
		document, header := openAssetPage(t, client, journey.server, page)

		assertShellChrome(t, page, document)
		assertContentSecurity(t, page, document, header)
		for _, reference := range assetReferences(t, page, document) {
			referenced[reference] = true
		}
		assertRealForm(t, page, document)
	}

	// Every address a page loads is served fingerprinted: the hashed name
	// is immutable for a year, and nothing a page references is missing.
	if len(referenced) != 8 {
		t.Fatalf("pages reference %d asset addresses, want the 8 required ones (entry module plus six sheets plus the realm crest)", len(referenced))
	}
	for _, reference := range []string{
		"/assets/pages/auth-9bde709de9d0.js",
		"/assets/styles/reset-9ce670ee3f4c.css",
		"/assets/styles/tokens-38e920b12e7e.css",
		"/assets/styles/base-3173c8db69b3.css",
		"/assets/styles/primitives-d73c96be7ed6.css",
		"/assets/styles/shell-3c68fa47d345.css",
		"/assets/styles/auth-53f96aba637c.css",
		"/assets/realm/brand/crest-e7d49c223c75.svg",
	} {
		if !referenced[reference] {
			t.Errorf("no page references %s: the entrypoint asset is registered but never loaded", reference)
		}
	}
	for reference := range referenced {
		openHashedAsset(t, browser(t), journey.server, reference)
	}

	// The stable names of the module graph resolve too, revalidated every
	// time instead of immutable: the entry module is what the document
	// loads, the stable names are what its imports resolve by.
	for _, stable := range []string{
		"/assets/pages/auth.js",
		"/assets/styles/reset.css",
		"/assets/styles/tokens.css",
		"/assets/styles/base.css",
		"/assets/styles/primitives.css",
		"/assets/styles/shell.css",
		"/assets/styles/auth.css",
		"/assets/realm/brand/crest.svg",
	} {
		openStableAsset(t, browser(t), journey.server, stable)
	}
}

// TestAccountHTMLRefusalIsAPageNotJSON posts a form without its CSRF
// token and proves the refusal is the localized page, never a problem
// document: the HTML operations stay forms end to end, including when
// they fail.
func TestAccountHTMLRefusalIsAPageNotJSON(t *testing.T) {
	t.Parallel()

	journey := newAssetJourney(t)
	client := browser(t)

	response, err := client.PostForm(journey.server.URL+"/register", map[string][]string{
		"email":    {"refusal@example.test"},
		"password": {"correct horse battery staple"},
	})
	if err != nil {
		t.Fatalf("POST /register: %v", err)
	}
	defer func() { _ = response.Body.Close }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read POST /register: %v", err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("POST /register without CSRF status = %d, want 403", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("POST /register without CSRF Content-Type = %q, want the refusal page, never JSON", contentType)
	}
	if strings.Contains(string(body), "application/problem+json") {
		t.Error("the refusal answers a problem document a browser would display raw")
	}
	assertContentSecurity(t, "POST /register", string(body), response.Header)
}

// openAssetPage reads one document as a deep link and proves the envelope
// a person needs before any script runs: 200, text/html, private
// no-store.
func openAssetPage(t *testing.T, client *http.Client, server *httptest.Server, path string) (string, http.Header) {
	t.Helper()

	response, err := client.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200 (body: %.200s)", path, response.StatusCode, body)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("GET %s Content-Type = %q, want text/html", path, contentType)
	}
	if cache := response.Header.Get("Cache-Control"); !strings.Contains(cache, "private") || !strings.Contains(cache, "no-store") {
		t.Errorf("GET %s Cache-Control = %q, want private, no-store", path, cache)
	}
	return string(body), response.Header
}

// assertShellChrome proves the chrome every document shares: the skip
// link to the main landmark, the header navigation with the current page
// marked for this deep link, the footer, and the assets of the module
// and the sheets — exactly one entry script, six stylesheets in cascade
// order, no inline code the policy blocks.
func assertShellChrome(t *testing.T, page, document string) {
	t.Helper()

	for _, marker := range []string{
		`href="#main"`,
		`<main id="main"`,
		`class="ga-shell__footer"`,
	} {
		if !strings.Contains(document, marker) {
			t.Errorf("GET %s renders no %s: the shell chrome is missing", page, marker)
		}
	}
	// The current mark belongs to the deep link only when the navigation
	// offers its address: /logout is offered to a signed-in browser alone
	// and /reset/confirm has no link of its own (the shell never marks
	// /reset/confirm as /reset), so a visitor reads those two documents
	// with no link marked — and never with another page's mark.
	if page == "/logout" || page == "/reset/confirm" {
		if strings.Contains(document, `aria-current="page"`) {
			t.Errorf("GET %s marks a navigation link it does not offer", page)
		}
		return
	}
	if got := strings.Count(document, "<script"); got != 1 {
		t.Errorf("GET %s renders %d <script elements, want exactly the entry module", page, got)
	}
	if !strings.Contains(document, `<script type="module" src="/assets/pages/auth-`) {
		t.Errorf("GET %s loads no fingerprinted entry module pages/auth.js", page)
	}
	for _, sheet := range []string{"reset", "tokens", "base", "primitives", "shell", "auth"} {
		if !strings.Contains(document, `/assets/styles/`+sheet+`-`) {
			t.Errorf("GET %s loads no fingerprinted styles/%s.css", page, sheet)
		}
	}
	// The current mark belongs to this deep link, not to another page.
	if !strings.Contains(document, `<a href="`+page+`" aria-current="page"`) {
		t.Errorf("GET %s marks no navigation link for its own address", page)
	}
	for _, forbidden := range []string{"<style", " onclick=", "<script>", "javascript:"} {
		if strings.Contains(document, forbidden) {
			t.Errorf("GET %s renders %q, which the browser policy blocks", page, forbidden)
		}
	}
}

// assertContentSecurity proves the policy travels on the response and
// forbids inline execution: no 'unsafe-inline', no 'unsafe-eval'.
func assertContentSecurity(t *testing.T, page, document string, header http.Header) {
	t.Helper()

	policy := header.Get("Content-Security-Policy")
	if policy == "" {
		t.Errorf("%s answers no Content-Security-Policy", page)
		return
	}
	for _, forbidden := range []string{"'unsafe-inline'", "'unsafe-eval'"} {
		if strings.Contains(policy, forbidden) {
			t.Errorf("%s Content-Security-Policy = %q, which allows inline execution", page, policy)
		}
	}
	_ = document
}

// assetReferences collects the distinct asset addresses of one document.
func assetReferences(t *testing.T, page, document string) []string {
	t.Helper()

	seen := map[string]bool{}
	var references []string
	for _, match := range assetReference.FindAllStringSubmatch(document, -1) {
		if seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		references = append(references, match[1])
	}
	if len(references) == 0 {
		t.Fatalf("GET %s references no asset address", page)
	}
	return references
}

// assertRealForm proves the operation is a form the browser submits, not
// a JSON fetch: one POST form whose action names the page's own address,
// carrying the double-submit token the middleware verifies.
func assertRealForm(t *testing.T, page, document string) {
	t.Helper()

	match := formAction.FindStringSubmatch(document)
	if match == nil {
		t.Fatalf("GET %s renders no POST form: the operation is not a real form", page)
	}
	if match[1] != page {
		t.Errorf("GET %s form action = %q, want the page's own address", page, match[1])
	}
	if !strings.Contains(document, `name="csrf_token"`) {
		t.Errorf("GET %s form carries no csrf_token field", page)
	}
	if strings.Contains(document, "application/json") {
		t.Errorf("GET %s mentions a JSON body: an HTML operation never fetches JSON", page)
	}
}

// openHashedAsset proves one fingerprinted address is served with its
// media type and the immutable cache policy of an address that can never
// mean different bytes.
func openHashedAsset(t *testing.T, client *http.Client, server *httptest.Server, reference string) {
	t.Helper()

	response, err := client.Get(server.URL + reference)
	if err != nil {
		t.Fatalf("GET %s: %v", reference, err)
	}
	defer func() { _ = response.Body.Close }()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read GET %s: %v", reference, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: a page references an asset nobody serves", reference, response.StatusCode)
	}
	wantType := "text/javascript; charset=utf-8"
	if strings.HasSuffix(reference, ".css") {
		wantType = "text/css; charset=utf-8"
	}
	if strings.HasSuffix(reference, ".svg") {
		wantType = "image/svg+xml"
	}
	if strings.HasSuffix(reference, ".webp") {
		wantType = "image/webp"
	}
	if contentType := response.Header.Get("Content-Type"); contentType != wantType {
		t.Errorf("GET %s Content-Type = %q, want %q", reference, contentType, wantType)
	}
	if cache := response.Header.Get("Cache-Control"); !strings.Contains(cache, "immutable") {
		t.Errorf("GET %s Cache-Control = %q, want the immutable policy of a fingerprinted address", reference, cache)
	}
	if etag := response.Header.Get("ETag"); etag == "" {
		t.Errorf("GET %s answers no ETag", reference)
	}
}

// openStableAsset proves one stable address of the module graph resolves
// with revalidation instead of immutability.
func openStableAsset(t *testing.T, client *http.Client, server *httptest.Server, stable string) {
	t.Helper()

	response, err := client.Get(server.URL + stable)
	if err != nil {
		t.Fatalf("GET %s: %v", stable, err)
	}
	defer func() { _ = response.Body.Close }()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read GET %s: %v", stable, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", stable, response.StatusCode)
	}
	if cache := response.Header.Get("Cache-Control"); !strings.Contains(cache, "no-cache") {
		t.Errorf("GET %s Cache-Control = %q, want revalidation of the stable address", stable, cache)
	}
}
