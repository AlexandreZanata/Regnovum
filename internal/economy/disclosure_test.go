package economy_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// forbiddenPromiseTokens are phrases that must never appear in snippets, emails, or OpenAPI contracts.
var forbiddenPromiseTokens = []string{
	"lastro",
	"asset-backed",
	"backed by gold",
	"backed by fiat",
	"reserva fracionária",
	"rendimento garantido",
	"rendimento fixo",
	"retorno garantido",
	"lucro garantido",
	"guaranteed yield",
	"guaranteed return",
	"fixed return",
	"fixed yield",
	"promessa de retorno",
	"promise of return",
	"aposta ativa",
	"apostas ativas",
	"active bet",
	"active bets",
	"active wager",
	"active wagering",
	"apostar agora",
	"bet now",
	"place your bet",
	"faça sua aposta",
}

// scanTextForForbiddenPromises inspects a body of text for forbidden promises of return, backing, or active betting.
func scanTextForForbiddenPromises(source, text string) []string {
	lowered := strings.ToLower(text)
	violations := []string{}
	for _, token := range forbiddenPromiseTokens {
		if strings.Contains(lowered, token) {
			violations = append(violations, fmt.Sprintf("%s carries forbidden promise token %q", source, token))
		}
	}
	return violations
}

// scanOpenAPIForPromises inspects the OpenAPI contract paths, tags, descriptions, summaries, and schemas.
func scanOpenAPIForPromises(raw []byte) ([]string, error) {
	var contract struct {
		Info struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Summary     string `json:"summary"`
		} `json:"info"`
		Paths      map[string]any                       `json:"paths"`
		Tags       []struct{ Name, Description string } `json:"tags"`
		Components map[string]any                       `json:"components"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		return nil, err
	}

	violations := []string{}
	violations = append(violations, scanTextForForbiddenPromises("openapi info.description", contract.Info.Description)...)
	violations = append(violations, scanTextForForbiddenPromises("openapi info.summary", contract.Info.Summary)...)

	for _, tag := range contract.Tags {
		violations = append(violations, scanTextForForbiddenPromises("openapi tag "+tag.Name, tag.Name+" "+tag.Description)...)
	}

	encodedPaths, _ := json.Marshal(contract.Paths)
	violations = append(violations, scanTextForForbiddenPromises("openapi paths", string(encodedPaths))...)

	if contract.Components != nil {
		encodedComp, _ := json.Marshal(contract.Components)
		violations = append(violations, scanTextForForbiddenPromises("openapi components", string(encodedComp))...)
	}

	return violations, nil
}

// repoRootDisclosure locates the repository root.
func repoRootDisclosure(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// TestOpenAPIContractDisclosesNoBackingOrYieldOrWagering proves that openapi.json carries no backing, yield, or bet promises.
func TestOpenAPIContractDisclosesNoBackingOrYieldOrWagering(t *testing.T) {
	t.Parallel()

	root := repoRootDisclosure(t)
	raw, err := os.ReadFile(filepath.Join(root, "api", "openapi.json"))
	if err != nil {
		t.Fatalf("read openapi.json: %v", err)
	}

	violations, err := scanOpenAPIForPromises(raw)
	if err != nil {
		t.Fatalf("scan openapi: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("openapi.json suggests backing, guaranteed yield, or active bets:\n%s", strings.Join(violations, "\n"))
	}
}

// TestEmailSurfacesHoldNoFinancialPromiseOrActiveWager proves that email catalogs and templates make no return/bet promises.
func TestEmailSurfacesHoldNoFinancialPromiseOrActiveWager(t *testing.T) {
	t.Parallel()

	root := repoRootDisclosure(t)
	emailLocales := []string{
		filepath.Join(root, "locales", "pt-BR", "email.json"),
		filepath.Join(root, "locales", "en-US", "email.json"),
	}

	for _, path := range emailLocales {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read email locale %s: %v", path, err)
		}
		if violations := scanTextForForbiddenPromises(filepath.Base(path), string(raw)); len(violations) > 0 {
			t.Fatalf("%s carries forbidden promise:\n%s", path, strings.Join(violations, "\n"))
		}
	}

	goldenPattern := filepath.Join(root, "internal", "notifications", "adapters", "renderer", "testdata", "*.golden")
	goldenFiles, err := filepath.Glob(goldenPattern)
	if err != nil {
		t.Fatalf("glob golden files: %v", err)
	}
	if len(goldenFiles) == 0 {
		t.Fatal("no email golden files found for inspection")
	}

	for _, file := range goldenFiles {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read golden file %s: %v", file, err)
		}
		if violations := scanTextForForbiddenPromises(filepath.Base(file), string(raw)); len(violations) > 0 {
			t.Fatalf("email golden file %s carries forbidden promise:\n%s", file, strings.Join(violations, "\n"))
		}
	}
}

// TestPublicHTMLSnippetsHoldNoFinancialPromiseOrActiveWager proves public templates and snippets hold no return/bet promises.
func TestPublicHTMLSnippetsHoldNoFinancialPromiseOrActiveWager(t *testing.T) {
	t.Parallel()

	root := repoRootDisclosure(t)
	templateDirs := []string{
		filepath.Join(root, "internal", "arenas", "adapters", "html"),
		filepath.Join(root, "internal", "transparency", "adapters", "http"),
		filepath.Join(root, "internal", "identity", "adapters", "html"),
	}

	for _, dir := range templateDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read template file %s: %v", path, err)
			}
			if violations := scanTextForForbiddenPromises(filepath.Base(path), string(raw)); len(violations) > 0 {
				t.Fatalf("template file %s carries forbidden promise:\n%s", path, strings.Join(violations, "\n"))
			}
		}
	}
}

// requiredDisclosureKeys are honest disclosure catalog entries that must exist with parity in both locales.
var requiredDisclosureKeys = []struct {
	category string
	key      string
}{
	{"patent", "title"},
	{"patent", "honorary_notice"},
	{"patent", "no_power"},
	{"patent", "season_bounded"},
	{"bonds", "title"},
	{"bonds", "unavailable_notice"},
	{"bonds", "no_yield"},
	{"betting", "title"},
	{"betting", "unavailable_notice"},
	{"betting", "no_wagers"},
	{"disclosures", "honest_limits"},
	{"disclosures", "no_financial_return"},
	{"disclosures", "seo_summary"},
}

// kingdomCatalogStructure parses the kingdom JSON catalog.
type kingdomCatalogStructure struct {
	Kingdom map[string]map[string]string `json:"kingdom"`
}

// TestKingdomCatalogsDiscloseHonestLimitsInBothLocales asserts complete disclosure keys and wording in pt-BR and en-US.
func TestKingdomCatalogsDiscloseHonestLimitsInBothLocales(t *testing.T) {
	t.Parallel()

	root := repoRootDisclosure(t)
	locales := map[string]string{
		"pt-BR": filepath.Join(root, "locales", "pt-BR", "kingdom.json"),
		"en-US": filepath.Join(root, "locales", "en-US", "kingdom.json"),
	}

	parsedCatalogs := make(map[string]kingdomCatalogStructure)

	for loc, path := range locales {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read kingdom.json for %s: %v", loc, err)
		}
		var parsed kingdomCatalogStructure
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("unmarshal kingdom.json for %s: %v", loc, err)
		}
		parsedCatalogs[loc] = parsed

		// 1. Verify all required honest disclosure keys exist.
		for _, req := range requiredDisclosureKeys {
			catMap, ok := parsed.Kingdom[req.category]
			if !ok {
				t.Fatalf("locale %s missing kingdom.%s category", loc, req.category)
			}
			val, ok := catMap[req.key]
			if !ok || strings.TrimSpace(val) == "" {
				t.Fatalf("locale %s missing or empty kingdom.%s.%s", loc, req.category, req.key)
			}
		}
	}

	// 2. Specific assertions for pt-BR.
	pt := parsedCatalogs["pt-BR"].Kingdom
	ptPatentNotice := strings.ToLower(pt["patent"]["honorary_notice"])
	if !strings.Contains(ptPatentNotice, "honorífico") ||
		!strings.Contains(ptPatentNotice, "não concede poder") {
		t.Errorf("pt-BR patent honorary_notice missing honorary or power limits: %q", pt["patent"]["honorary_notice"])
	}
	if !strings.Contains(strings.ToLower(pt["patent"]["no_power"]), "não compram") {
		t.Errorf("pt-BR patent no_power missing explicit denial: %q", pt["patent"]["no_power"])
	}
	if !strings.Contains(strings.ToLower(pt["bonds"]["unavailable_notice"]), "não estão disponíveis") {
		t.Errorf("pt-BR bonds unavailable_notice missing unavailability notice: %q", pt["bonds"]["unavailable_notice"])
	}
	ptBondYield := strings.ToLower(pt["bonds"]["no_yield"])
	if !strings.Contains(ptBondYield, "rendimento garantido") ||
		!strings.Contains(ptBondYield, "promessa de retorno") {
		t.Errorf("pt-BR bonds no_yield missing negative disclosure: %q", pt["bonds"]["no_yield"])
	}
	if !strings.Contains(strings.ToLower(pt["betting"]["unavailable_notice"]), "estritamente indisponíveis") {
		t.Errorf("pt-BR betting unavailable_notice missing unavailability notice: %q", pt["betting"]["unavailable_notice"])
	}
	if !strings.Contains(strings.ToLower(pt["betting"]["no_wagers"]), "não oferece apostas ativas") {
		t.Errorf("pt-BR betting no_wagers missing denial: %q", pt["betting"]["no_wagers"])
	}
	if !strings.Contains(strings.ToLower(pt["disclosures"]["no_financial_return"]), "não promete valorização patrimonial") {
		t.Errorf("pt-BR disclosures missing no_financial_return wording: %q", pt["disclosures"]["no_financial_return"])
	}

	// 3. Specific assertions for en-US.
	en := parsedCatalogs["en-US"].Kingdom
	enPatentNotice := strings.ToLower(en["patent"]["honorary_notice"])
	if !strings.Contains(enPatentNotice, "honorary") ||
		!strings.Contains(enPatentNotice, "grants no power") {
		t.Errorf("en-US patent honorary_notice missing honorary or power limits: %q", en["patent"]["honorary_notice"])
	}
	if !strings.Contains(strings.ToLower(en["patent"]["no_power"]), "do not buy") {
		t.Errorf("en-US patent no_power missing explicit denial: %q", en["patent"]["no_power"])
	}
	if !strings.Contains(strings.ToLower(en["bonds"]["unavailable_notice"]), "unavailable") {
		t.Errorf("en-US bonds unavailable_notice missing unavailability notice: %q", en["bonds"]["unavailable_notice"])
	}
	enBondYield := strings.ToLower(en["bonds"]["no_yield"])
	if !strings.Contains(enBondYield, "guaranteed yield") ||
		!strings.Contains(enBondYield, "no promise of return") {
		t.Errorf("en-US bonds no_yield missing negative disclosure: %q", en["bonds"]["no_yield"])
	}
	if !strings.Contains(strings.ToLower(en["betting"]["unavailable_notice"]), "strictly unavailable") {
		t.Errorf("en-US betting unavailable_notice missing unavailability notice: %q", en["betting"]["unavailable_notice"])
	}
	if !strings.Contains(strings.ToLower(en["betting"]["no_wagers"]), "no active wagers") {
		t.Errorf("en-US betting no_wagers missing denial: %q", en["betting"]["no_wagers"])
	}
	if !strings.Contains(strings.ToLower(en["disclosures"]["no_financial_return"]), "does not promise") {
		t.Errorf("en-US disclosures missing no_financial_return wording: %q", en["disclosures"]["no_financial_return"])
	}
}

// TestHonestDisclosureScannerFalsification proves scanners reject injected promises and incomplete catalogs.
func TestHonestDisclosureScannerFalsification(t *testing.T) {
	t.Parallel()

	// 1. Text scanner flags forbidden tokens.
	fakeEmail := "Venha investir com rendimento garantido e faça sua aposta agora!"
	if v := scanTextForForbiddenPromises("fake_email", fakeEmail); len(v) < 2 {
		t.Fatalf("scanTextForForbiddenPromises failed to flag fake email: %v", v)
	}

	// 2. OpenAPI scanner flags injected return promise.
	fakeOpenAPI := []byte(`{
		"info": {"title": "Test", "description": "Platform with asset-backed bonds", "summary": "API"},
		"paths": {"/api/v1/invest": {"description": "Returns guaranteed return"}},
		"tags": [],
		"components": {}
	}`)
	vOpenAPI, err := scanOpenAPIForPromises(fakeOpenAPI)
	if err != nil || len(vOpenAPI) < 2 {
		t.Fatalf("scanOpenAPIForPromises failed to flag fake openapi: %v, violations: %v", err, vOpenAPI)
	}

	// 3. Catalog scanner fails on missing disclosure key.
	incompleteCatalog := kingdomCatalogStructure{
		Kingdom: map[string]map[string]string{
			"patent": {
				"title": "Patent",
				// Missing honorary_notice and no_power
			},
		},
	}
	missingKeyFound := false
	for _, req := range requiredDisclosureKeys {
		catMap, ok := incompleteCatalog.Kingdom[req.category]
		if !ok || catMap[req.key] == "" {
			missingKeyFound = true
			break
		}
	}
	if !missingKeyFound {
		t.Fatal("incomplete catalog check was expected to fail on missing keys")
	}
}
