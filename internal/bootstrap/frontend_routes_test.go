// Tests of the P48-T01 reconciled frontend route inventory: every
// METHOD+path declared by the normative sources must appear exactly once
// in quality/frontend-routes.json, and nothing else may appear there.
//
// Normative sources (never .local/frontend/ROUTE_BASELINE.json, which is
// only a photograph for cross-check):
//   - api/openapi.json (published contract, 82 operations),
//   - the four staged fragments (seasons 4, metering 4, commerce 2,
//     disputes 5 = 15 operations),
//   - every internal/*/adapters/{http,html}/routes.go declaration plus
//     the two platform health routes (100 declarations total).
//
// The inventory starts honest: every entry is NOT_VERIFIED, except the
// three operator jobs without a published contract, which are
// MISSING_PUBLISHED_CONTRACT. No entry may claim DONE by inference, and
// no browser usage is inferred by string search: client/page are planned
// bindings, and probes or contract gaps carry an exclusion instead.
package bootstrap_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type frontendRouteEntry struct {
	Method        string   `json:"method"`
	Path          string   `json:"path"`
	OperationID   *string  `json:"operation_id"`
	Contract      *string  `json:"contract"`
	Declaration   string   `json:"declaration"`
	Readiness     string   `json:"readiness"`
	Audience      string   `json:"audience"`
	Phase         string   `json:"phase"`
	Task          string   `json:"task"`
	Client        *string  `json:"client"`
	Page          *string  `json:"page"`
	Exclusion     *string  `json:"exclusion"`
	Tests         []string `json:"tests"`
	EvidenceState string   `json:"evidence_state"`
	Note          string   `json:"note"`
}

type frontendRoutesFile struct {
	Schema int `json:"schema"`
	Counts struct {
		Declared                 int `json:"declared"`
		Published                int `json:"published"`
		Staged                   int `json:"staged"`
		MissingPublishedContract int `json:"missingPublishedContract"`
	} `json:"counts"`
	Routes []frontendRouteEntry `json:"routes"`
}

var frontendRouteDeclPattern = regexp.MustCompile(`\{Method:\s*http\.Method(\w+),\s*Path:\s*"([^"]+)"\}`)

func frontendRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func loadFrontendRoutes(t *testing.T) (string, frontendRoutesFile) {
	t.Helper()
	root := frontendRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "quality", "frontend-routes.json"))
	if err != nil {
		t.Fatalf("read frontend routes inventory: %v", err)
	}
	var inventory frontendRoutesFile
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatalf("frontend routes inventory is not JSON: %v", err)
	}
	return root, inventory
}

func collectRouteDeclarations(t *testing.T, root string) map[string]string {
	t.Helper()
	declarations := make(map[string]string)
	files, err := filepath.Glob(filepath.Join(root, "internal", "*", "adapters", "http", "routes.go"))
	if err != nil {
		t.Fatalf("glob http routes: %v", err)
	}
	htmlFiles, err := filepath.Glob(filepath.Join(root, "internal", "*", "adapters", "html", "routes.go"))
	if err != nil {
		t.Fatalf("glob html routes: %v", err)
	}
	files = append(files, htmlFiles...)
	sort.Strings(files)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			t.Fatalf("rel %s: %v", file, err)
		}
		for _, match := range frontendRouteDeclPattern.FindAllStringSubmatch(string(raw), -1) {
			key := strings.ToUpper(match[1]) + " " + match[2]
			if prev, dup := declarations[key]; dup {
				t.Fatalf("duplicate declaration %q in %s and %s", key, prev, rel)
			}
			declarations[key] = rel
		}
	}
	// The two platform probes are normative health routes outside the
	// module adapters; they are declared by the platform registry.
	for _, path := range []string{"/health/live", "/health/ready"} {
		key := "GET " + path
		if _, dup := declarations[key]; dup {
			t.Fatalf("duplicate declaration %q", key)
		}
		declarations[key] = "internal/platform/httpserver/routes.go"
	}
	return declarations
}

func collectContractOperations(t *testing.T, root, name string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("%s is not JSON: %v", name, err)
	}
	operations := make(map[string]string, len(document.Paths))
	for path, methods := range document.Paths {
		for method, operation := range methods {
			switch strings.ToLower(method) {
			case "get", "post", "put", "patch", "delete", "options", "head":
			default:
				continue
			}
			key := strings.ToUpper(method) + " " + path
			operations[key] = operation.OperationID
		}
	}
	return operations
}

func resolveTestRef(t *testing.T, root, ref string) {
	t.Helper()
	path, function, _ := strings.Cut(ref, "::")
	if path == "" {
		t.Fatalf("test ref %q has no file", ref)
	}
	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatalf("test ref %q points at missing file: %v", ref, err)
	}
	if function == "" {
		return
	}
	if !strings.Contains(string(raw), "func "+function+"(") {
		t.Fatalf("test ref %q names unknown function", ref)
	}
}

func TestFrontendRoutesInventoryMatchesNormativeSources(t *testing.T) {
	t.Parallel()

	root, inventory := loadFrontendRoutes(t)

	if inventory.Counts.Declared != 100 {
		t.Errorf("declared = %d, want 100", inventory.Counts.Declared)
	}
	if inventory.Counts.Published != 82 {
		t.Errorf("published = %d, want 82", inventory.Counts.Published)
	}
	if inventory.Counts.Staged != 15 {
		t.Errorf("staged = %d, want 15", inventory.Counts.Staged)
	}
	if inventory.Counts.MissingPublishedContract != 3 {
		t.Errorf("missingPublishedContract = %d, want 3", inventory.Counts.MissingPublishedContract)
	}
	if len(inventory.Routes) != 100 {
		t.Fatalf("routes has %d entries, want 100", len(inventory.Routes))
	}

	declarations := collectRouteDeclarations(t, root)
	published := collectContractOperations(t, root, "api/openapi.json")
	staged := make(map[string]string)
	stagedContract := make(map[string]string)
	for _, fragment := range []string{
		"internal/seasons/adapters/http/openapi.fragment.json",
		"internal/metering/adapters/http/openapi.fragment.json",
		"internal/commerce/adapters/http/openapi.fragment.json",
		"internal/disputes/adapters/http/openapi.fragment.json",
	} {
		for key, operation := range collectContractOperations(t, root, fragment) {
			if prev, dup := staged[key]; dup {
				t.Fatalf("duplicate staged operation %q (%q vs %q)", key, prev, operation)
			}
			staged[key] = operation
			stagedContract[key] = fragment
		}
	}
	if len(published) != 82 {
		t.Errorf("published contract operations = %d, want 82", len(published))
	}
	if len(staged) != 15 {
		t.Errorf("staged fragment operations = %d, want 15 (4+4+2+5)", len(staged))
	}

	byKey := make(map[string]frontendRouteEntry, len(inventory.Routes))
	publishedCount, stagedCount, missingCount := 0, 0, 0
	for _, entry := range inventory.Routes {
		key := entry.Method + " " + entry.Path
		if _, dup := byKey[key]; dup {
			t.Errorf("duplicate inventory entry %q", key)
			continue
		}
		byKey[key] = entry

		if entry.EvidenceState == "DONE" || entry.EvidenceState == "VERIFIED" || entry.EvidenceState == "COMPLETE" {
			t.Errorf("%q claims %q by inference; T01 entries stay NOT_VERIFIED", key, entry.EvidenceState)
		}
		if entry.Task != "P48-T01" {
			t.Errorf("%q task = %q, want P48-T01", key, entry.Task)
		}
		if entry.Audience == "" || entry.Phase == "" || entry.Declaration == "" {
			t.Errorf("%q needs audience, phase and declaration", key)
		}
		if len(entry.Tests) == 0 {
			t.Errorf("%q lists no tests", key)
		}
		for _, ref := range entry.Tests {
			resolveTestRef(t, root, ref)
		}
		hasBinding := entry.Client != nil && *entry.Client != "" && entry.Page != nil && *entry.Page != ""
		hasExclusion := entry.Exclusion != nil && *entry.Exclusion != ""
		if hasBinding == hasExclusion {
			t.Errorf("%q needs either planned client+page or an exclusion justification, not both/neither", key)
		}

		switch entry.Readiness {
		case "published":
			publishedCount++
			if entry.Contract == nil || *entry.Contract != "api/openapi.json" {
				t.Errorf("%q readiness published needs contract api/openapi.json", key)
			}
			want, ok := published[key]
			if !ok {
				t.Errorf("%q marked published but absent from api/openapi.json", key)
			} else if entry.OperationID == nil || *entry.OperationID != want {
				t.Errorf("%q operation_id = %v, want %q from api/openapi.json", key, entry.OperationID, want)
			}
			if wantDecl, ok := declarations[key]; !ok {
				t.Errorf("%q marked published but absent from routes.go declarations", key)
			} else if wantDecl != entry.Declaration {
				t.Errorf("%q declaration = %q, want %q", key, entry.Declaration, wantDecl)
			}
			if entry.EvidenceState != "NOT_VERIFIED" {
				t.Errorf("%q evidence = %q, want NOT_VERIFIED", key, entry.EvidenceState)
			}
		case "staged":
			stagedCount++
			wantFragment, ok := stagedContract[key]
			if !ok {
				t.Errorf("%q marked staged but absent from the four staged fragments", key)
			} else if entry.Contract == nil || *entry.Contract != wantFragment {
				t.Errorf("%q contract = %v, want %q", key, entry.Contract, wantFragment)
			}
			if want, ok := staged[key]; ok && (entry.OperationID == nil || *entry.OperationID != want) {
				t.Errorf("%q operation_id = %v, want %q from %q", key, entry.OperationID, want, wantFragment)
			}
			if wantDecl, ok := declarations[key]; !ok {
				t.Errorf("%q marked staged but absent from routes.go declarations", key)
			} else if wantDecl != entry.Declaration {
				t.Errorf("%q declaration = %q, want %q", key, entry.Declaration, wantDecl)
			}
			if entry.EvidenceState != "NOT_VERIFIED" {
				t.Errorf("%q evidence = %q, want NOT_VERIFIED", key, entry.EvidenceState)
			}
		case "missing-contract":
			missingCount++
			if entry.OperationID != nil {
				t.Errorf("%q readiness missing-contract must carry a null operation_id gap", key)
			}
			if entry.Contract != nil {
				t.Errorf("%q readiness missing-contract must carry a null contract gap", key)
			}
			if _, ok := published[key]; ok {
				t.Errorf("%q marked missing-contract but present in api/openapi.json", key)
			}
			if _, ok := staged[key]; ok {
				t.Errorf("%q marked missing-contract but present in a staged fragment", key)
			}
			if entry.EvidenceState != "MISSING_PUBLISHED_CONTRACT" {
				t.Errorf("%q evidence = %q, want MISSING_PUBLISHED_CONTRACT", key, entry.EvidenceState)
			}
		default:
			t.Errorf("%q readiness = %q, want published|staged|missing-contract", key, entry.Readiness)
		}
	}

	if publishedCount != 82 {
		t.Errorf("inventory published = %d, want 82", publishedCount)
	}
	if stagedCount != 15 {
		t.Errorf("inventory staged = %d, want 15", stagedCount)
	}
	if missingCount != 3 {
		t.Errorf("inventory missing-contract = %d, want 3", missingCount)
	}

	// Bijection: every normative declaration and every contract
	// operation must appear in the inventory, with no extras.
	for key, decl := range declarations {
		entry, ok := byKey[key]
		if !ok {
			t.Errorf("routes.go declares %q (%s) but the inventory omits it", key, decl)
			continue
		}
		if entry.Declaration != decl {
			t.Errorf("%q declaration = %q, want %q", key, entry.Declaration, decl)
		}
	}
	for key := range published {
		if _, ok := byKey[key]; !ok {
			t.Errorf("api/openapi.json declares %q but the inventory omits it", key)
		}
	}
	for key := range staged {
		if _, ok := byKey[key]; !ok {
			t.Errorf("staged fragment declares %q but the inventory omits it", key)
		}
	}
	for key := range byKey {
		if _, ok := declarations[key]; !ok {
			t.Errorf("inventory lists %q which no routes.go file declares", key)
		}
	}

	// The three known operator jobs without a published contract are
	// explicit gaps, never silent omissions.
	for _, key := range []string{
		"GET /api/v1/admin/jobs/health",
		"GET /api/v1/admin/jobs/dead",
		"POST /api/v1/admin/jobs/{id}/retry",
	} {
		entry, ok := byKey[key]
		if !ok {
			t.Errorf("operator gap %q is missing from the inventory", key)
			continue
		}
		if entry.Readiness != "missing-contract" || entry.OperationID != nil {
			t.Errorf("operator gap %q must stay an explicit null-operation gap", key)
		}
	}
}
