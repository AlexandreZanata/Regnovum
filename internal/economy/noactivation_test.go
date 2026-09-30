package economy_test

// P32-T10 — no unapproved monetary entrypoint, on real PostgreSQL and a
// live router.
//
// The economy exposes ports, never doors: no HTTP route, no CLI command,
// no flag, no decree and no bootstrap wiring may move custody or bypass
// the freeze. The tests prove it three ways, and every denial is logged
// as the registered attempt: the full process route registry holds no
// economic path, the published OpenAPI contract holds none either, and a
// live mux with the real money surfaces answers economy probes with 404
// while serving its own routes, leaving the journal empty.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	// Blank imports register every HTTP route provider through init, so
	// RegisteredRoutes below enumerates the whole process table.
	_ "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/html"
	_ "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/html"
	_ "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/search/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/http"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	wallethttp "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/http"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
)

// economicMarks are substrings no public entrypoint may carry: money
// movement vocabulary that would turn a route, command or flag into an
// activation bypass.
var economicMarks = []string{"economy", "genesis", "treasury", "milliink"}

// TestRouteRegistryHoldsNoEconomicPath enumerates every route the
// process registers and refuses any economic mark in method or path.
func TestRouteRegistryHoldsNoEconomicPath(t *testing.T) {
	t.Parallel()

	routes := httpserver.RegisteredRoutes()
	if len(routes) == 0 {
		t.Fatal("no registered routes: the registry judged nothing")
	}
	for _, route := range routes {
		lowered := strings.ToLower(route.String())
		for _, mark := range economicMarks {
			if strings.Contains(lowered, mark) {
				t.Errorf("registered route %q carries %q: an economic entrypoint", route.String(), mark)
			} else {
				t.Logf("registered and clean: %s", route.String())
			}
		}
	}
}

// TestOpenAPIContractHoldsNoEconomicPath parses the published contract
// and refuses any economic mark in paths, operation ids and tags.
func TestOpenAPIContractHoldsNoEconomicPath(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	raw, err := os.ReadFile(filepath.Join(root, "api", "openapi.json"))
	if err != nil {
		t.Fatalf("read openapi.json: %v", err)
	}
	var contract struct {
		Paths map[string]any `json:"paths"`
		Tags  []struct {
			Name string `json:"name"`
		} `json:"tags"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("decode openapi.json: %v", err)
	}
	if len(contract.Paths) == 0 {
		t.Fatal("no paths in the contract: the check judged nothing")
	}
	holdings := []string{}
	for path := range contract.Paths {
		holdings = append(holdings, "path "+path)
	}
	for _, tag := range contract.Tags {
		holdings = append(holdings, "tag "+tag.Name)
	}
	encoded, err := json.Marshal(contract.Paths)
	if err != nil {
		t.Fatalf("re-encode paths: %v", err)
	}
	holdings = append(holdings, "operations "+string(encoded))
	for _, holding := range holdings {
		lowered := strings.ToLower(holding)
		for _, mark := range economicMarks {
			if strings.Contains(lowered, mark) {
				t.Errorf("contract %q carries %q: an economic entrypoint", holding, mark)
			}
		}
	}
}

// scannedSourceDirs are the code trees that may wire an entrypoint: the
// binary, the composition root, the platform router and every HTTP/HTML
// adapter. Documentation may discuss the economy; code may not invoke it.
var scannedSourceDirs = []string{
	"cmd",
	"internal/bootstrap",
	"internal/platform/httpserver",
	"internal/arenas/adapters/http",
	"internal/arenas/adapters/html",
	"internal/arguments/adapters/http",
	"internal/billing/adapters/http",
	"internal/identity/adapters/http",
	"internal/identity/adapters/html",
	"internal/jobs/adapters/http",
	"internal/moderation/adapters/http",
	"internal/persuasion/adapters/http",
	"internal/positions/adapters/http",
	"internal/profiles/adapters/http",
	"internal/search/adapters/http",
	"internal/transparency/adapters/http",
	"internal/wallet/adapters/http",
	"web/src",
}

// TestSourceWiringMentionsNoEconomy scans every entrypoint-capable tree
// for economic vocabulary in delivered (non-test) Go sources and web
// sources. A match is a wire to review, never background noise.
func TestSourceWiringMentionsNoEconomy(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	checked := 0
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, entry := range entries {
			full := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				base := entry.Name()
				if base == "testdata" || base == "node_modules" || strings.HasPrefix(base, ".") {
					continue
				}
				walk(full)
				continue
			}
			name := entry.Name()
			isGo := strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
			isWeb := strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".css") || strings.HasSuffix(name, ".html")
			if !isGo && !isWeb {
				continue
			}
			raw, err := os.ReadFile(full)
			if err != nil {
				t.Fatalf("read file %s: %v", full, err)
			}
			lowered := strings.ToLower(string(raw))
			for _, mark := range economicMarks {
				if strings.Contains(lowered, mark) {
					rel, _ := filepath.Rel(root, full)
					t.Errorf("wiring %s carries %q: an economic entrypoint to review", rel, mark)
				}
			}
			checked++
		}
	}
	for _, dir := range scannedSourceDirs {
		walk(filepath.Join(root, filepath.FromSlash(dir)))
	}
	if checked == 0 {
		t.Fatal("no source file checked: the scan judged nothing")
	}
	t.Logf("wiring scan clean over %d sources", checked)
}

// TestLiveRouterDeniesEconomicProbes mounts the real money surfaces and
// proves economic probes die with 404 while the router serves: denial
// without effect, with the journal empty afterwards.
func TestLiveRouterDeniesEconomicProbes(t *testing.T) {
	database := dbtest.New(t)
	pool := database.Pool.Pool()

	secMgr, err := security.New(security.Options{
		Env:    config.EnvTest,
		Clock:  clockseed.NewClock(),
		Random: clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("security.New: %v", err)
	}
	repository := walletpg.NewRepository(pool)
	codec, err := walletapp.NewStatementCursorCodec(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewStatementCursorCodec: %v", err)
	}
	handler := wallethttp.NewHandler(wallethttp.HandlerConfig{
		GetWalletBalanceUseCase:   walletapp.NewGetWalletBalanceUseCase(repository),
		GetWalletStatementUseCase: walletapp.NewGetWalletStatementUseCase(repository, codec),
		SecurityManager:           secMgr,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	legit := []string{"/api/v1/me/wallet", "/api/v1/me/wallet/transactions"}
	for _, path := range legit {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode == http.StatusNotFound {
			t.Fatalf("GET %s = 404: the probe router serves nothing", path)
		}
		t.Logf("router live: GET %s = %d", path, response.StatusCode)
	}

	probes := []string{
		"/api/v1/economy", "/api/v1/economy/transfers", "/api/v1/economy/holds",
		"/api/v1/genesis", "/api/v1/treasury", "/api/v1/milliink",
		"/api/v1/admin/economy", "/api/v1/me/economy",
	}
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}
	for _, path := range probes {
		for _, method := range methods {
			request, err := http.NewRequest(method, server.URL+path, nil)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404: an economic entrypoint", method, path, response.StatusCode)
			} else {
				t.Logf("denied and registered: %s %s -> 404", method, path)
			}
		}
	}

	var legs int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM app.economy_entries`).Scan(&legs); err != nil {
		t.Fatalf("count journal: %v", err)
	}
	if legs != 0 {
		t.Fatalf("journal holds %d legs after denied probes: denial had effect", legs)
	}
}
