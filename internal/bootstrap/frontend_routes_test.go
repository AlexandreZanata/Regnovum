// Tests of the P48-T01 reconciled frontend route inventory: every
// METHOD+path declared by the normative sources must appear exactly once
// in quality/frontend-routes.json, and nothing else may appear there.
//
// Normative sources (never .local/frontend/ROUTE_BASELINE.json, which is
// only a photograph for cross-check):
//   - api/openapi.json (published contract, 86 operations),
//   - the four staged fragments (seasons 4, metering 4, commerce 2,
//     disputes 5 = 15 operations),
//   - every internal/*/adapters/{http,html}/routes.go declaration plus
//     the two platform health routes (101 declarations total).
//
// The inventory starts honest: every entry is NOT_VERIFIED. P49-T09 closed
// the three operator jobs gap, so no MISSING_PUBLISHED_CONTRACT row remains.
// No entry may claim DONE by inference, and
// no browser usage is inferred by string search: client/page are planned
// bindings, and probes or contract gaps carry an exclusion instead.
//
// The verification is split into one focused test per facet so each
// stays reviewable: counts, contracts, bindings and bijection.
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

// frontendInventoryContext carries the reconciled inventory with
// the normative sets, loaded once per test.
type frontendInventoryContext struct {
	root           string
	inventory      frontendRoutesFile
	declarations   map[string]string
	published      map[string]string
	staged         map[string]string
	stagedContract map[string]string
	byKey          map[string]frontendRouteEntry
}

func loadFrontendInventoryContext(t *testing.T) *frontendInventoryContext {
	t.Helper()

	root, inventory := loadFrontendRoutes(t)
	if len(inventory.Routes) != 101 {
		t.Fatalf("routes has %d entries, want 101", len(inventory.Routes))
	}
	context := &frontendInventoryContext{
		root:           root,
		inventory:      inventory,
		declarations:   collectRouteDeclarations(t, root),
		published:      collectContractOperations(t, root, "api/openapi.json"),
		staged:         make(map[string]string),
		stagedContract: make(map[string]string),
		byKey:          make(map[string]frontendRouteEntry, len(inventory.Routes)),
	}
	for _, fragment := range []string{
		"internal/seasons/adapters/http/openapi.fragment.json",
		"internal/metering/adapters/http/openapi.fragment.json",
		"internal/commerce/adapters/http/openapi.fragment.json",
		"internal/disputes/adapters/http/openapi.fragment.json",
	} {
		for key, operation := range collectContractOperations(t, root, fragment) {
			if prev, dup := context.staged[key]; dup {
				t.Fatalf("duplicate staged operation %q (%q vs %q)", key, prev, operation)
			}
			context.staged[key] = operation
			context.stagedContract[key] = fragment
		}
	}
	for _, entry := range inventory.Routes {
		key := entry.Method + " " + entry.Path
		if _, dup := context.byKey[key]; dup {
			t.Errorf("duplicate inventory entry %q", key)
			continue
		}
		context.byKey[key] = entry
	}
	return context
}

func TestFrontendRoutesInventoryCounts(t *testing.T) {
	t.Parallel()

	context := loadFrontendInventoryContext(t)
	if context.inventory.Counts.Declared != 101 {
		t.Errorf("declared = %d, want 101", context.inventory.Counts.Declared)
	}
	if context.inventory.Counts.Published != 86 {
		t.Errorf("published = %d, want 86", context.inventory.Counts.Published)
	}
	if context.inventory.Counts.Staged != 15 {
		t.Errorf("staged = %d, want 15", context.inventory.Counts.Staged)
	}
	if context.inventory.Counts.MissingPublishedContract != 0 {
		t.Errorf("missingPublishedContract = %d, want 0", context.inventory.Counts.MissingPublishedContract)
	}
	if len(context.published) != 86 {
		t.Errorf("published contract operations = %d, want 86", len(context.published))
	}
	if len(context.staged) != 15 {
		t.Errorf("staged fragment operations = %d, want 15 (4+4+2+5)", len(context.staged))
	}
	published, staged, missing := 0, 0, 0
	for _, entry := range context.inventory.Routes {
		switch entry.Readiness {
		case "published":
			published++
		case "staged":
			staged++
		case "missing-contract":
			missing++
		default:
			t.Errorf("readiness = %q, want published|staged|missing-contract", entry.Readiness)
		}
	}
	if published != 86 || staged != 15 || missing != 0 {
		t.Errorf("readiness rows = %d/%d/%d, want 86/15/0", published, staged, missing)
	}
}

// checkPublishedEntry verifies one published row against
// api/openapi.json and the routes.go registry.
func checkPublishedEntry(t *testing.T, context *frontendInventoryContext, key string, entry frontendRouteEntry) {
	t.Helper()

	if entry.Contract == nil || *entry.Contract != "api/openapi.json" {
		t.Errorf("%q readiness published needs contract api/openapi.json", key)
	}
	want, ok := context.published[key]
	if !ok {
		t.Errorf("%q marked published but absent from api/openapi.json", key)
	} else if entry.OperationID == nil || *entry.OperationID != want {
		t.Errorf("%q operation_id = %v, want %q from api/openapi.json", key, entry.OperationID, want)
	}
	wantDecl, ok := context.declarations[key]
	if !ok {
		t.Errorf("%q marked published but absent from routes.go declarations", key)
	} else if wantDecl != entry.Declaration {
		t.Errorf("%q declaration = %q, want %q", key, entry.Declaration, wantDecl)
	}
	if entry.EvidenceState != "NOT_VERIFIED" {
		t.Errorf("%q evidence = %q, want NOT_VERIFIED", key, entry.EvidenceState)
	}
}

// checkStagedEntry verifies one staged row against its fragment
// and the routes.go registry.
func checkStagedEntry(t *testing.T, context *frontendInventoryContext, key string, entry frontendRouteEntry) {
	t.Helper()

	wantFragment, ok := context.stagedContract[key]
	if !ok {
		t.Errorf("%q marked staged but absent from the four staged fragments", key)
	} else if entry.Contract == nil || *entry.Contract != wantFragment {
		t.Errorf("%q contract = %v, want %q", key, entry.Contract, wantFragment)
	}
	if want, ok := context.staged[key]; ok && (entry.OperationID == nil || *entry.OperationID != want) {
		t.Errorf("%q operation_id = %v, want %q from %q", key, entry.OperationID, want, wantFragment)
	}
	wantDecl, ok := context.declarations[key]
	if !ok {
		t.Errorf("%q marked staged but absent from routes.go declarations", key)
	} else if wantDecl != entry.Declaration {
		t.Errorf("%q declaration = %q, want %q", key, entry.Declaration, wantDecl)
	}
	if entry.EvidenceState != "NOT_VERIFIED" {
		t.Errorf("%q evidence = %q, want NOT_VERIFIED", key, entry.EvidenceState)
	}
}

// checkGapEntry verifies one explicit contract gap: null operation,
// null contract, absent from every contract home, gap evidence.
func checkGapEntry(t *testing.T, context *frontendInventoryContext, key string, entry frontendRouteEntry) {
	t.Helper()

	if entry.OperationID != nil {
		t.Errorf("%q readiness missing-contract must carry a null operation_id gap", key)
	}
	if entry.Contract != nil {
		t.Errorf("%q readiness missing-contract must carry a null contract gap", key)
	}
	if _, ok := context.published[key]; ok {
		t.Errorf("%q marked missing-contract but present in api/openapi.json", key)
	}
	if _, ok := context.staged[key]; ok {
		t.Errorf("%q marked missing-contract but present in a staged fragment", key)
	}
	if entry.EvidenceState != "MISSING_PUBLISHED_CONTRACT" {
		t.Errorf("%q evidence = %q, want MISSING_PUBLISHED_CONTRACT", key, entry.EvidenceState)
	}
}

func TestFrontendRoutesInventoryContracts(t *testing.T) {
	t.Parallel()

	context := loadFrontendInventoryContext(t)
	for _, entry := range context.inventory.Routes {
		key := entry.Method + " " + entry.Path
		switch entry.Readiness {
		case "published":
			checkPublishedEntry(t, context, key, entry)
		case "staged":
			checkStagedEntry(t, context, key, entry)
		case "missing-contract":
			checkGapEntry(t, context, key, entry)
		}
	}
}

func TestFrontendRoutesInventoryBindings(t *testing.T) {
	t.Parallel()

	context := loadFrontendInventoryContext(t)
	for _, entry := range context.inventory.Routes {
		key := entry.Method + " " + entry.Path
		if entry.EvidenceState == "DONE" || entry.EvidenceState == "VERIFIED" || entry.EvidenceState == "COMPLETE" {
			t.Errorf("%q claims %q by inference; T01 entries stay NOT_VERIFIED", key, entry.EvidenceState)
		}
		if entry.Task != "P48-T01" && !(entry.Path == "/" && entry.Task == "P60-T01") {
			t.Errorf("%q task = %q, want P48-T01", key, entry.Task)
		}
		if entry.Audience == "" || entry.Phase == "" || entry.Declaration == "" {
			t.Errorf("%q needs audience, phase and declaration", key)
		}
		if len(entry.Tests) == 0 {
			t.Errorf("%q lists no tests", key)
		}
		for _, ref := range entry.Tests {
			resolveTestRef(t, context.root, ref)
		}
		hasBinding := entry.Client != nil && *entry.Client != "" && entry.Page != nil && *entry.Page != ""
		hasExclusion := entry.Exclusion != nil && *entry.Exclusion != ""
		if hasBinding == hasExclusion {
			t.Errorf("%q needs either planned client+page or an exclusion justification, not both/neither", key)
		}
	}
}

func TestFrontendRoutesInventoryBijection(t *testing.T) {
	t.Parallel()

	context := loadFrontendInventoryContext(t)
	for key, decl := range context.declarations {
		entry, ok := context.byKey[key]
		if !ok {
			t.Errorf("routes.go declares %q (%s) but the inventory omits it", key, decl)
			continue
		}
		if entry.Declaration != decl {
			t.Errorf("%q declaration = %q, want %q", key, entry.Declaration, decl)
		}
	}
	for key := range context.published {
		if _, ok := context.byKey[key]; !ok {
			t.Errorf("api/openapi.json declares %q but the inventory omits it", key)
		}
	}
	for key := range context.staged {
		if _, ok := context.byKey[key]; !ok {
			t.Errorf("staged fragment declares %q but the inventory omits it", key)
		}
	}
	for key := range context.byKey {
		if _, ok := context.declarations[key]; !ok {
			t.Errorf("inventory lists %q which no routes.go file declares", key)
		}
	}
	// P49-T09 closed the three operator jobs gap: they are published
	// operations with a contract, never silent omissions.
	for key, operation := range map[string]string{
		"GET /api/v1/admin/jobs/health":      "getJobsHealth",
		"GET /api/v1/admin/jobs/dead":        "listDeadJobs",
		"POST /api/v1/admin/jobs/{id}/retry": "retryDeadJob",
	} {
		entry, ok := context.byKey[key]
		if !ok {
			t.Errorf("operator job %q is missing from the inventory", key)
			continue
		}
		if entry.Readiness != "published" || entry.OperationID == nil || *entry.OperationID != operation {
			t.Errorf("operator job %q must stay a published operation %q", key, operation)
		}
	}
}
