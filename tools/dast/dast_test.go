package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testClient is the scanner transport for fixtures: bounded and never
// following redirects, so a redirect target is evidence, not a fetch.
func testClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// parseTarget splits a fixture address for the engine.
func parseTarget(raw string) (*url.URL, error) {
	return url.Parse(raw)
}

// vulnerableMux is the controlled vulnerable fixture: every rule has a
// hose to drink from. If the scanner ever passes this fixture clean, the
// scanner is broken, not the fixture.
func vulnerableMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /echo", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"echo":` + jsonQuote(request.URL.Query().Get("q")) + `}`))
	})
	mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) {
		panic("controlled fixture panic")
	})
	mux.HandleFunc("GET /admin", func(writer http.ResponseWriter, request *http.Request) {
		_ = request
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"admin":true}`))
	})
	mux.HandleFunc("POST /admin", func(writer http.ResponseWriter, request *http.Request) {
		_ = request
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"admin":true}`))
	})
	mux.HandleFunc("GET /redirect", func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "https://evil.invalid/callback?q="+request.URL.Query().Get("q"), http.StatusFound)
	})
	mux.HandleFunc("GET /bare", func(writer http.ResponseWriter, request *http.Request) {
		_ = request
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /cookie", func(writer http.ResponseWriter, request *http.Request) {
		_ = request
		http.SetCookie(writer, &http.Cookie{Name: "arena_session", Value: "fixture", Path: "/"})
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	})
	return mux
}

// hardenedMux is the clean fixture: authenticated writes, refused
// anonymous, security headers everywhere, scoped cookies, no reflection.
func hardenedMux() *http.ServeMux {
	mux := http.NewServeMux()
	hardened := func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'")
		if request.Header.Get("Authorization") == "" {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"code":"unauthorized"}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}
	mux.HandleFunc("GET /items", hardened)
	mux.HandleFunc("POST /items", hardened)
	return mux
}

func jsonQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// scanFixture runs the engine against a fixture mux with the named
// operations, without touching the flag layer.
func scanFixture(t *testing.T, mux http.Handler, operations []Operation, profiles []Profile) *Report {
	t.Helper()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	config := &Config{
		Operations: operations,
		Profiles:   profiles,
		Timeout:    30 * time.Second,
		Client:     testClient(),
	}
	base, err := parseTarget(server.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	config.Base = base
	report, err := scan(config)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return report
}

func fixtureOperations() []Operation {
	return []Operation{
		{Method: "GET", Path: "/echo"},
		{Method: "GET", Path: "/panic"},
		{Method: "GET", Path: "/admin"},
		{Method: "POST", Path: "/admin"},
		{Method: "GET", Path: "/redirect"},
		{Method: "GET", Path: "/bare"},
		{Method: "GET", Path: "/cookie"},
	}
}

func fixtureProfiles() []Profile {
	return []Profile{{Name: "anonymous"}, {Name: "user", Cookie: "fixture-user-token"}}
}

// rulesFired answers which rules fired at least once.
func rulesFired(report *Report) map[string]bool {
	fired := map[string]bool{}
	for _, finding := range report.Findings {
		fired[finding.Rule] = true
	}
	return fired
}

// TestVulnerableFixtureIsDetected is the gate's own proof of life: the
// controlled vulnerable stack must light every family the tool claims.
func TestVulnerableFixtureIsDetected(t *testing.T) {
	report := scanFixture(t, vulnerableMux(), fixtureOperations(), fixtureProfiles())
	fired := rulesFired(report)
	for _, rule := range []string{RuleNo5xx, RuleNoReflection, RuleAuthBypass, RuleOpenRedirect, RuleSecurityHeaders, RuleCookieFlags} {
		if !fired[rule] {
			t.Errorf("rule %q never fired on the vulnerable fixture", rule)
		}
	}
	if !report.Blocking() {
		t.Error("vulnerable fixture does not block")
	}
}

// TestHardenedFixtureIsClean proves the rules do not fire on correct
// behavior: authenticated writes, refused anonymous, headers everywhere.
func TestHardenedFixtureIsClean(t *testing.T) {
	operations := []Operation{{Method: "GET", Path: "/items"}, {Method: "POST", Path: "/items"}}
	report := scanFixture(t, hardenedMux(), operations, fixtureProfiles())
	if len(report.Findings) != 0 {
		for _, finding := range report.Findings {
			t.Errorf("unexpected finding: %s %s %s: %s", finding.Severity, finding.Rule, finding.Path, finding.Detail)
		}
	}
	if report.Blocking() {
		t.Error("hardened fixture blocks")
	}
}

// TestWaiversSuppressAndExpire proves the triage path: a valid waiver
// clears the run, and expired, unknown-rule and stale waivers fail it.
func TestWaiversSuppressAndExpire(t *testing.T) {
	operations := []Operation{{Method: "GET", Path: "/echo"}}
	profiles := []Profile{{Name: "anonymous"}}

	runWith := func(t *testing.T, waivers []Waiver) *Report {
		t.Helper()
		server := httptest.NewServer(vulnerableMux())
		t.Cleanup(server.Close)
		base, err := parseTarget(server.URL)
		if err != nil {
			t.Fatalf("parse target: %v", err)
		}
		config := &Config{Base: base, Operations: operations, Profiles: profiles, Waivers: waivers, Timeout: 30 * time.Second, Client: testClient()}
		report, err := scan(config)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		return report
	}

	valid := []Waiver{
		{Rule: RuleNoReflection, Path: "/echo", Reason: "controlled fixture echoes by design", Owner: "security", Expires: "2099-01-01"},
		{Rule: RuleSecurityHeaders, Path: "/echo", Reason: "fixture has no middleware by design", Owner: "security", Expires: "2099-01-01"},
	}
	t.Run("valid suppresses", func(t *testing.T) {
		report := runWith(t, valid)
		for _, finding := range report.Findings {
			if !finding.Waived {
				t.Errorf("unwaived finding remains: %s %s: %s", finding.Rule, finding.Path, finding.Detail)
			}
		}
		if report.Blocking() {
			t.Error("valid waivers still block")
		}
	})

	t.Run("stale fails", func(t *testing.T) {
		waivers := append(append([]Waiver(nil), valid...), Waiver{
			Rule: RuleNo5xx, Path: "/echo", Reason: "nothing 500s here", Owner: "security", Expires: "2099-01-01",
		})
		report := runWith(t, waivers)
		found := false
		for _, finding := range report.Findings {
			if finding.Rule == "stale-waiver" {
				found = true
			}
		}
		if !found {
			t.Error("unused waiver not reported stale")
		}
	})

	t.Run("expired refused at load", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "waivers.json")
		encoded, _ := json.Marshal([]Waiver{{Rule: RuleNo5xx, Path: "/echo", Reason: "old", Owner: "security", Expires: "2020-01-01"}})
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatalf("write waivers: %v", err)
		}
		if _, err := loadWaivers(path); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("expired waiver loads: %v", err)
		}
	})

	t.Run("unknown rule refused at load", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "waivers.json")
		encoded, _ := json.Marshal([]Waiver{{Rule: "no-such-rule", Path: "/echo", Reason: "x", Owner: "security", Expires: "2099-01-01"}})
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatalf("write waivers: %v", err)
		}
		if _, err := loadWaivers(path); err == nil || !strings.Contains(err.Error(), "unknown rule") {
			t.Fatalf("unknown rule loads: %v", err)
		}
	})
}

// TestLoopbackGuardRefusesEgress proves the scanner cannot be pointed
// outward: names, public IPs and https-remote all fail to configure.
func TestLoopbackGuardRefusesEgress(t *testing.T) {
	for _, target := range []string{
		"https://example.com",
		"http://93.184.216.34",
		"https://attacker.invalid",
		"http://internal.corp",
		"ftp://127.0.0.1",
	} {
		if _, err := loadConfig(target, "testdata/openapi.json", "", "", "testdata/waivers.json", time.Minute); err == nil {
			t.Errorf("target %q configures", target)
		}
	}
	if _, err := loadConfig("http://127.0.0.1:9", "testdata/openapi.json", "", "", "testdata/waivers.json", time.Minute); err != nil {
		t.Fatalf("loopback target refused: %v", err)
	}
}

// TestOpenAPILoaderEnumerates proves the route table drives the scan.
func TestOpenAPILoaderEnumerates(t *testing.T) {
	operations, err := loadOperations("testdata/openapi.json")
	if err != nil {
		t.Fatalf("loadOperations: %v", err)
	}
	seen := map[string]bool{}
	for _, operation := range operations {
		seen[operation.Method+" "+operation.Path] = true
	}
	for _, want := range []string{"GET /items", "POST /items/{id}"} {
		if !seen[want] {
			t.Errorf("operation %q not enumerated", want)
		}
	}
}

// TestMainCommandContracts proves the exit-code contract: clean is 0,
// findings are 1, misuse is 2.
func TestMainCommandContracts(t *testing.T) {
	if got := run([]string{"-target", ""}, io.Discard, io.Discard); got != exitUsage {
		t.Errorf("missing target = %d, want %d", got, exitUsage)
	}
	if got := run([]string{"-target", "https://example.com"}, io.Discard, io.Discard); got != exitUsage {
		t.Errorf("remote target = %d, want %d", got, exitUsage)
	}
}
