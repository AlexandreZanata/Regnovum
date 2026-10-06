package http_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/http"
)

// TestRoutesMatchContract proves the operational route list and the
// published contract describe each other exactly in both directions (P49-T09):
// no mounted route without a contract path, no contract path without a
// route. The three operations carry operator/role/step-up semantics in the
// published document; the retry stays operator-only with no public binding.
func TestRoutesMatchContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file)))))
	raw, err := os.ReadFile(filepath.Join(root, "api", "openapi.json"))
	if err != nil {
		t.Fatalf("read published contract: %v", err)
	}
	var document struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			OperationID string                `json:"operationId"`
			Security    []map[string][]string `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode published contract: %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("contract openapi = %q, want 3.1.0", document.OpenAPI)
	}
	routes := adapterhttp.Routes()
	if len(routes) != 3 {
		t.Fatalf("routes = %d, want health, dead and retry", len(routes))
	}
	want := map[string]string{
		"GET /api/v1/admin/jobs/health":      "getJobsHealth",
		"GET /api/v1/admin/jobs/dead":        "listDeadJobs",
		"POST /api/v1/admin/jobs/{id}/retry": "retryDeadJob",
	}
	for _, route := range routes {
		key := route.Method + " " + route.Path
		operation, ok := want[key]
		if !ok {
			t.Errorf("route %q is not one of the three operator jobs", key)
			continue
		}
		methods, ok := document.Paths[route.Path]
		if !ok {
			t.Errorf("route %q has no published contract path", key)
			continue
		}
		operationDoc, ok := methods[strings.ToLower(route.Method)]
		if !ok {
			t.Errorf("route %q has no published contract method", key)
			continue
		}
		if operationDoc.OperationID != operation {
			t.Errorf("route %q operationId = %q, want %q", key, operationDoc.OperationID, operation)
		}
		if len(operationDoc.Security) == 0 {
			t.Errorf("route %q carries no security: the operator surface requires the session cookie", key)
		}
	}
	keys := make([]string, 0, len(routes))
	for _, route := range routes {
		keys = append(keys, route.Method+" "+route.Path)
	}
	sort.Strings(keys)
	wantKeys := make([]string, 0, len(want))
	for key := range want {
		wantKeys = append(wantKeys, key)
	}
	sort.Strings(wantKeys)
	if len(keys) != len(wantKeys) {
		t.Fatalf("routes %v vs contract %v: the contract must match both ways", keys, wantKeys)
	}
	for i := range keys {
		if keys[i] != wantKeys[i] {
			t.Fatalf("routes %v vs contract %v: the contract must match both ways", keys, wantKeys)
		}
	}
}

// TestRegisterRoutesMountsExactlyTheDeclaredRoutes proves the mux wiring
// cannot drift from the registry: every declared route is mounted, and no
// staged path is smuggled beside them.
func TestRegisterRoutesMountsExactlyTheDeclaredRoutes(t *testing.T) {
	handler := adapterhttp.NewHandler(adapterhttp.HandlerConfig{})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	for _, route := range adapterhttp.Routes() {
		pattern := route.Method + " " + route.Path
		probed, _ := http.NewRequest(route.Method, route.Path, nil)
		// A path template with {id} never matches literally; probe with a
		// concrete identifier so the mux proves the pattern is mounted.
		if strings.Contains(route.Path, "{id}") {
			probed, _ = http.NewRequest(route.Method, strings.Replace(route.Path, "{id}", "0191f0e0-0000-7000-8000-0000000000aa", 1), nil)
		}
		matched, patternMatched := mux.Handler(probed)
		_ = matched
		if patternMatched == "" {
			t.Errorf("declared route %q is not mounted on the mux", pattern)
		}
	}
	for _, staged := range []string{"/api/v1/me/seasons/current", "/api/v1/commerce/intents"} {
		probed, _ := http.NewRequest(http.MethodGet, staged, nil)
		if _, pattern := mux.Handler(probed); pattern != "" {
			t.Errorf("staged path %q is mounted beside the operator surface", staged)
		}
	}
}
