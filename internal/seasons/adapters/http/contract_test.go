package http_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/http"
)

// TestRoutesMatchFragment proves the staged route list and the
// OpenAPI fragment describe each other exactly in both directions:
// no mounted route without a contract path, no contract path
// without a route. The published contract stays untouched: this
// fragment ships beside the staged adapter until activation.
func TestRoutesMatchFragment(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "openapi.fragment.json"))
	if err != nil {
		t.Fatalf("read fragment: %v", err)
	}
	var fragment struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &fragment); err != nil {
		t.Fatalf("decode fragment: %v", err)
	}
	if fragment.OpenAPI != "3.1.0" {
		t.Fatalf("fragment openapi = %q, want 3.1.0", fragment.OpenAPI)
	}
	routes := adapterhttp.Routes()
	if len(routes) != 3 {
		t.Fatalf("routes = %d, want current, history and detail", len(routes))
	}
	fragmentRoutes := map[string]string{}
	for path, methods := range fragment.Paths {
		for method, operation := range methods {
			if operation.OperationID == "" {
				t.Fatalf("fragment %s %s has no operationId", method, path)
			}
			fragmentRoutes[strings.ToUpper(method)+" "+path] = operation.OperationID
		}
	}
	routeKeys := make([]string, 0, len(routes))
	for _, route := range routes {
		key := route.Method + " " + route.Path
		routeKeys = append(routeKeys, key)
		if _, ok := fragmentRoutes[key]; !ok {
			t.Errorf("route %q has no fragment path", key)
		}
	}
	sort.Strings(routeKeys)
	fragmentKeys := make([]string, 0, len(fragmentRoutes))
	for key := range fragmentRoutes {
		fragmentKeys = append(fragmentKeys, key)
	}
	sort.Strings(fragmentKeys)
	if len(routeKeys) != len(fragmentKeys) {
		t.Fatalf("routes %v vs fragment %v: the contract must match both ways", routeKeys, fragmentKeys)
	}
	for i := range routeKeys {
		if routeKeys[i] != fragmentKeys[i] {
			t.Fatalf("routes %v vs fragment %v: the contract must match both ways", routeKeys, fragmentKeys)
		}
	}
}
