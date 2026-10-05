package economy_test

import (
	"encoding/json"
	"fmt"
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

// bondMarks are substrings that must never appear in public routes, OpenAPI, catalogs, or checkouts.
var bondMarks = []string{
	"bond",
	"bonds",
	"titulo",
	"titulos",
	"título",
	"títulos",
	"rendimento",
	"yield",
}

// scanRoutesForBondTokens inspects route strings against forbidden bond tokens.
func scanRoutesForBondTokens(routes []httpserver.Route) []string {
	violations := []string{}
	for _, route := range routes {
		lowered := strings.ToLower(route.String())
		for _, mark := range bondMarks {
			if strings.Contains(lowered, mark) {
				violations = append(violations, fmt.Sprintf("route %q carries forbidden token %q", route.String(), mark))
			}
		}
	}
	return violations
}

// scanOpenAPIForBondTokens inspects the OpenAPI contract for bond references or published yields.
func scanOpenAPIForBondTokens(raw []byte) ([]string, error) {
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
		for _, mark := range bondMarks {
			if strings.Contains(lowered, mark) {
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

// catalogFileStructure mirrors billing products JSON schema for inspection.
type catalogFileStructure struct {
	Version  int `json:"version"`
	Products []struct {
		Market  string `json:"market"`
		Product string `json:"product"`
		Grant   string `json:"grant"`
	} `json:"products"`
}

// scanCatalogForBondTokens inspects catalog JSON entries for bond products or grants.
func scanCatalogForBondTokens(raw []byte) ([]string, error) {
	var file catalogFileStructure
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}

	violations := []string{}
	for _, entry := range file.Products {
		loweredProduct := strings.ToLower(entry.Product)
		loweredGrant := strings.ToLower(entry.Grant)
		for _, mark := range bondMarks {
			if strings.Contains(loweredProduct, mark) || strings.Contains(loweredGrant, mark) {
				violations = append(violations, fmt.Sprintf("catalog product %q (grant %q) carries %q", entry.Product, entry.Grant, mark))
			}
		}
	}
	return violations, nil
}

// repositoryRoot finds the root path for testing.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// TestRouteRegistryHoldsNoBondPath proves that no registered route offers crown bonds.
func TestRouteRegistryHoldsNoBondPath(t *testing.T) {
	t.Parallel()

	routes := httpserver.RegisteredRoutes()
	if len(routes) == 0 {
		t.Fatal("no routes registered: route check evaluated nothing")
	}

	violations := scanRoutesForBondTokens(routes)
	if len(violations) > 0 {
		t.Fatalf("route registry carries bond entrypoints:\n%s", strings.Join(violations, "\n"))
	}
}

// TestOpenAPIContractHoldsNoBondOrYield proves that openapi.json contains no bond routes, tags, or yields.
func TestOpenAPIContractHoldsNoBondOrYield(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "api", "openapi.json"))
	if err != nil {
		t.Fatalf("read openapi.json: %v", err)
	}

	violations, err := scanOpenAPIForBondTokens(raw)
	if err != nil {
		t.Fatalf("scan openapi: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("openapi contract carries bond/yield tokens:\n%s", strings.Join(violations, "\n"))
	}
}

// TestBillingCatalogHoldsNoBondProduct proves that the billing catalog contains no bond products.
func TestBillingCatalogHoldsNoBondProduct(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "billing", "adapters", "catalog", "products.json"))
	if err != nil {
		t.Fatalf("read products.json: %v", err)
	}

	violations, err := scanCatalogForBondTokens(raw)
	if err != nil {
		t.Fatalf("scan catalog: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("products.json carries bond products:\n%s", strings.Join(violations, "\n"))
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
		for _, mark := range bondMarks {
			if strings.Contains(idStr, mark) || strings.Contains(grantStr, mark) {
				t.Errorf("loaded product %q (%q) carries %q", product.ID(), product.Grant().Kind(), mark)
			}
		}
	}
}

// TestCheckoutSurfaceRejectsBondPurchases proves that attempting to check out any bond product fails.
func TestCheckoutSurfaceRejectsBondPurchases(t *testing.T) {
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

	probedBondProducts := []string{
		"bond_crown",
		"crown_bond",
		"titulo_coroa",
		"bond_90d",
		"yield_bond",
	}

	for _, probe := range probedBondProducts {
		productID, err := billingdomain.ParseProductID(probe)
		if err != nil {
			// Product ID syntax rejected: fail-closed.
			continue
		}
		// If valid identifier syntax, it must not exist in any commercial market.
		for _, market := range []billingdomain.Market{billingdomain.MarketBrazil, billingdomain.MarketInternational} {
			if loaded.Has(market, productID) {
				t.Fatalf("bond probe %q found in market %s", probe, market)
			}
		}
	}
}

// TestNoPublicYieldPublished proves that no yield rate or return schedule is exposed.
func TestNoPublicYieldPublished(t *testing.T) {
	t.Parallel()

	routes := httpserver.RegisteredRoutes()
	for _, route := range routes {
		lowered := strings.ToLower(route.String())
		if strings.Contains(lowered, "yield") || strings.Contains(lowered, "rendimento") {
			t.Errorf("public route %q advertises yield", route.String())
		}
	}
}

// TestScannerFalsification proves that the scanners fail when an unauthorized bond or yield is introduced.
func TestScannerFalsification(t *testing.T) {
	t.Parallel()

	// 1. Route scanner must flag bond route.
	badRoutes := []httpserver.Route{
		{Method: "POST", Path: "/api/v1/crown-bonds/checkout"},
	}
	if v := scanRoutesForBondTokens(badRoutes); len(v) == 0 {
		t.Fatal("scanRoutesForBondTokens failed to detect /api/v1/crown-bonds/checkout")
	}

	// 2. OpenAPI scanner must flag bond path and yield schema.
	badContract := []byte(`{"paths":{"/api/v1/bonds":{}},"tags":[],"components":{"schemas":{"BondYield":{}}}}`)
	vOpenAPI, err := scanOpenAPIForBondTokens(badContract)
	if err != nil || len(vOpenAPI) == 0 {
		t.Fatalf("scanOpenAPIForBondTokens failed to detect bad contract: %v, violations: %v", err, vOpenAPI)
	}

	// 3. Catalog scanner must flag bond product and grant.
	badCatalog := []byte(`{"version":1,"products":[{"market":"BR","product":"crown_bond_1","grant":"BOND"}]}`)
	vCatalog, err := scanCatalogForBondTokens(badCatalog)
	if err != nil || len(vCatalog) == 0 {
		t.Fatalf("scanCatalogForBondTokens failed to detect bad catalog: %v, violations: %v", err, vCatalog)
	}
}
