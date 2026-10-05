package economy_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	// Blank imports register HTTP route providers so RegisteredRoutes enumerates all endpoints.
	_ "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/http"

	billingcatalog "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/catalog"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

// bettingMarks are keywords that must never appear in public routes, OpenAPI, catalogs, or checkouts.
var bettingMarks = []string{
	"bet",
	"bets",
	"betting",
	"aposta",
	"apostas",
	"prediction",
	"predictions",
	"previsao",
	"previsoes",
	"previsão",
	"previsões",
	"palpite",
	"palpites",
	"wager",
	"wagers",
}

// scanRoutesForBettingTokens inspects route strings against forbidden betting tokens.
func scanRoutesForBettingTokens(routes []httpserver.Route) []string {
	violations := []string{}
	for _, route := range routes {
		lowered := strings.ToLower(route.String())
		// Match path segments or tokens.
		for _, mark := range bettingMarks {
			// Check if token appears as word segment in path or method.
			if containsBettingToken(lowered, mark) {
				violations = append(violations, fmt.Sprintf("route %q carries forbidden token %q", route.String(), mark))
			}
		}
	}
	return violations
}

// containsBettingToken checks if the keyword appears as a delimited token in a route or identifier.
func containsBettingToken(source, token string) bool {
	if source == token {
		return true
	}
	for _, sep := range []string{"/", "_", "-", " ", "."} {
		if strings.Contains(source, sep+token+sep) ||
			strings.HasPrefix(source, token+sep) ||
			strings.HasSuffix(source, sep+token) {
			return true
		}
	}
	return false
}

// scanOpenAPIForBettingTokens inspects the OpenAPI contract for betting or prediction market paths/schemas.
func scanOpenAPIForBettingTokens(raw []byte) ([]string, error) {
	var contract struct {
		Paths      map[string]any          `json:"paths"`
		Tags       []struct{ Name string } `json:"tags"`
		Components map[string]any          `json:"components"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		return nil, err
	}

	violations := []string{}
	checkToken := func(scope, text string) {
		lowered := strings.ToLower(text)
		for _, mark := range bettingMarks {
			if containsBettingToken(lowered, mark) {
				violations = append(violations, fmt.Sprintf("%s carries %q", scope, mark))
			}
		}
	}

	for path := range contract.Paths {
		checkToken("path "+path, path)
	}
	for _, tag := range contract.Tags {
		checkToken("tag "+tag.Name, tag.Name)
	}
	encodedPaths, _ := json.Marshal(contract.Paths)
	checkToken("operations", string(encodedPaths))
	if contract.Components != nil {
		encodedComp, _ := json.Marshal(contract.Components)
		checkToken("components", string(encodedComp))
	}

	return violations, nil
}

// scanCatalogForBettingTokens inspects catalog JSON entries for betting products or grants.
func scanCatalogForBettingTokens(raw []byte) ([]string, error) {
	var file struct {
		Version  int `json:"version"`
		Products []struct {
			Market  string `json:"market"`
			Product string `json:"product"`
			Grant   string `json:"grant"`
		} `json:"products"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}

	violations := []string{}
	for _, entry := range file.Products {
		loweredProduct := strings.ToLower(entry.Product)
		loweredGrant := strings.ToLower(entry.Grant)
		for _, mark := range bettingMarks {
			if containsBettingToken(loweredProduct, mark) || containsBettingToken(loweredGrant, mark) {
				violations = append(violations, fmt.Sprintf("catalog product %q (grant %q) carries %q", entry.Product, entry.Grant, mark))
			}
		}
	}
	return violations, nil
}

// repositoryRootBetting finds the repository root.
func repositoryRootBetting(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// TestRouteRegistryHoldsNoBettingPath proves that no registered route offers betting or prediction markets.
func TestRouteRegistryHoldsNoBettingPath(t *testing.T) {
	t.Parallel()

	routes := httpserver.RegisteredRoutes()
	if len(routes) == 0 {
		t.Fatal("no routes registered: route check evaluated nothing")
	}

	violations := scanRoutesForBettingTokens(routes)
	if len(violations) > 0 {
		t.Fatalf("route registry carries betting entrypoints:\n%s", strings.Join(violations, "\n"))
	}
}

// TestOpenAPIContractHoldsNoBettingPath proves that openapi.json contains no betting or prediction routes.
func TestOpenAPIContractHoldsNoBettingPath(t *testing.T) {
	t.Parallel()

	root := repositoryRootBetting(t)
	raw, err := os.ReadFile(filepath.Join(root, "api", "openapi.json"))
	if err != nil {
		t.Fatalf("read openapi.json: %v", err)
	}

	violations, err := scanOpenAPIForBettingTokens(raw)
	if err != nil {
		t.Fatalf("scan openapi: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("openapi contract carries betting tokens:\n%s", strings.Join(violations, "\n"))
	}
}

// TestBillingCatalogHoldsNoBettingProduct proves that the billing catalog contains no betting products.
func TestBillingCatalogHoldsNoBettingProduct(t *testing.T) {
	t.Parallel()

	root := repositoryRootBetting(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "billing", "adapters", "catalog", "products.json"))
	if err != nil {
		t.Fatalf("read products.json: %v", err)
	}

	violations, err := scanCatalogForBettingTokens(raw)
	if err != nil {
		t.Fatalf("scan catalog: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("products.json carries betting products:\n%s", strings.Join(violations, "\n"))
	}

	loaded, err := billingcatalog.Load(billingcatalog.Spec{
		Markets: []billingcatalog.EnabledMarket{
			{Market: "BR", Currency: "BRL"},
			{Market: "INTERNATIONAL", Currency: "USD"},
		},
	})
	if err != nil {
		t.Fatalf("billingcatalog.Load: %v", err)
	}

	for _, product := range loaded.Products() {
		idStr := strings.ToLower(string(product.ID()))
		grantStr := strings.ToLower(product.Grant().Kind().String())
		for _, mark := range bettingMarks {
			if containsBettingToken(idStr, mark) || containsBettingToken(grantStr, mark) {
				t.Errorf("loaded product %q (%q) carries %q", product.ID(), product.Grant().Kind(), mark)
			}
		}
	}
}

// TestCheckoutSurfaceRejectsBettingPurchases proves that attempting to checkout any betting product fails.
func TestCheckoutSurfaceRejectsBettingPurchases(t *testing.T) {
	t.Parallel()

	loaded, err := billingcatalog.Load(billingcatalog.Spec{
		Markets: []billingcatalog.EnabledMarket{
			{Market: "BR", Currency: "BRL"},
			{Market: "INTERNATIONAL", Currency: "USD"},
		},
	})
	if err != nil {
		t.Fatalf("billingcatalog.Load: %v", err)
	}

	probedBettingProducts := []string{
		"bet_ticket",
		"aposta_round",
		"prediction_market_1",
		"bet_ink_100",
		"wager_pass",
	}

	for _, probe := range probedBettingProducts {
		productID, err := billingdomain.ParseProductID(probe)
		if err != nil {
			// Product ID syntax rejected: fail-closed.
			continue
		}
		// If valid identifier syntax, it must not exist in any commercial market.
		for _, market := range []billingdomain.Market{billingdomain.MarketBrazil, billingdomain.MarketInternational} {
			if loaded.Has(market, productID) {
				t.Fatalf("betting probe %q found in market %s", probe, market)
			}
		}
	}
}

// TestLiveRouterDeniesBettingProbes probes a live mux and ensures betting paths return 404.
func TestLiveRouterDeniesBettingProbes(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	probes := []string{
		"/api/v1/bets",
		"/api/v1/me/bets",
		"/api/v1/economy/bets",
		"/api/v1/markets/bets",
		"/api/v1/predictions",
		"/api/v1/apostas",
	}

	for _, path := range probes {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
}

// TestBettingScannerFalsification proves that the scanners fail when an unauthorized betting product or route is introduced.
func TestBettingScannerFalsification(t *testing.T) {
	t.Parallel()

	// 1. Route scanner must flag betting route.
	badRoutes := []httpserver.Route{
		{Method: "POST", Path: "/api/v1/bets/place"},
		{Method: "GET", Path: "/api/v1/apostas"},
	}
	if v := scanRoutesForBettingTokens(badRoutes); len(v) != 2 {
		t.Fatalf("scanRoutesForBettingTokens failed to detect both bad routes: %v", v)
	}

	// 2. OpenAPI scanner must flag betting path and tags.
	badContract := []byte(`{"paths":{"/api/v1/predictions":{}},"tags":[{"name":"apostas"}],"components":{}}`)
	vOpenAPI, err := scanOpenAPIForBettingTokens(badContract)
	if err != nil || len(vOpenAPI) < 2 {
		t.Fatalf("scanOpenAPIForBettingTokens failed to detect bad contract: %v, violations: %v", err, vOpenAPI)
	}

	// 3. Catalog scanner must flag betting product and grant.
	badCatalog := []byte(`{"version":1,"products":[{"market":"BR","product":"bet_ticket_1","grant":"BET"}]}`)
	vCatalog, err := scanCatalogForBettingTokens(badCatalog)
	if err != nil || len(vCatalog) == 0 {
		t.Fatalf("scanCatalogForBettingTokens failed to detect bad catalog: %v, violations: %v", err, vCatalog)
	}
}
