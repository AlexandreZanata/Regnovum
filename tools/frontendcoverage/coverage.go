// Package main is the fail-closed frontend route coverage gate
// (P48-T02).
//
// It confronts quality/frontend-routes.json with the normative
// sources — api/openapi.json, the four staged fragments and every
// internal/*/adapters/{http,html}/routes.go declaration plus the
// platform health routes — and with the filesystem the inventory
// points at. It reads and judges; it never writes.
//
// Usage:
//
//	go run ./tools/frontendcoverage -root . [-mode planning|complete]
//
// The mode also comes from FRONTEND_COVERAGE_MODE, with planning as
// the default; an explicit -mode wins over the environment. Planning
// accepts declared pending work without certifying it; complete
// demands zero browser gap and zero mandatory contract gap. An
// exception carries a reason, an audience and a proof — a browser
// API is never excluded for lacking an implementation. Exit 0 holds
// every rule, 1 lists each violated rule, 2 is a usage error.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Rules are the closed vocabulary of findings, in stable order.
const (
	RuleInventoryUnreadable  = "inventory-unreadable"
	RuleSchemaUnknown        = "schema-unknown"
	RuleCountsMismatch       = "counts-mismatch"
	RuleDuplicateRoute       = "duplicate-route"
	RuleRouteMissing         = "route-missing-from-inventory"
	RuleUndeclaredRoute      = "inventory-without-declaration"
	RuleContractDrift        = "contract-drift"
	RuleContractGap          = "missing-contract-gap"
	RuleClientWithoutPage    = "client-without-page"
	RulePageWithoutClient    = "page-without-client"
	RuleEvidenceMissing      = "evidence-missing"
	RuleEvidenceUnknown      = "evidence-unknown"
	RuleTestOnlyAsProduction = "test-only-as-production"
	RuleStagedAsActive       = "staged-as-active"
	RuleExclusionUnjustified = "exclusion-unjustified"
	RuleCoverageGap          = "coverage-gap"
)

// AllRules answers every rule this gate can emit.
func AllRules() []string {
	return []string{
		RuleInventoryUnreadable,
		RuleSchemaUnknown,
		RuleCountsMismatch,
		RuleDuplicateRoute,
		RuleRouteMissing,
		RuleUndeclaredRoute,
		RuleContractDrift,
		RuleContractGap,
		RuleClientWithoutPage,
		RulePageWithoutClient,
		RuleEvidenceMissing,
		RuleEvidenceUnknown,
		RuleTestOnlyAsProduction,
		RuleStagedAsActive,
		RuleExclusionUnjustified,
		RuleCoverageGap,
	}
}

// Modes are the only coverage postures. Planning records pending
// work; complete certifies the browser.
const (
	ModePlanning = "planning"
	ModeComplete = "complete"
)

// Evidence states the inventory may carry. NOT_VERIFIED and
// MISSING_PUBLISHED_CONTRACT describe pending work; VERIFIED and
// DONE claim a proven browser binding and need a browser proof.
const (
	EvidenceNotVerified = "NOT_VERIFIED"
	EvidenceMissing     = "MISSING_PUBLISHED_CONTRACT"
	EvidenceVerified    = "VERIFIED"
	EvidenceDone        = "DONE"
)

// stagedHarnessBinding is the only binding a staged route may
// carry: consumption proven in the separate synthetic harness,
// never as an active production surface.
const stagedHarnessBinding = "planned-staged-harness"

// RouteEntry is one METHOD+path row of the inventory.
type RouteEntry struct {
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

// Inventory is the versioned route map P48-T01 reconciled.
type Inventory struct {
	Schema int `json:"schema"`
	Counts struct {
		Declared                 int `json:"declared"`
		Published                int `json:"published"`
		Staged                   int `json:"staged"`
		MissingPublishedContract int `json:"missingPublishedContract"`
	} `json:"counts"`
	Routes []RouteEntry `json:"routes"`
}

// StagedOperation is one fragment operation with its home.
type StagedOperation struct {
	Operation string
	Fragment  string
}

// Sources are the normative sets the inventory must mirror.
type Sources struct {
	Declarations map[string]string          // "METHOD path" -> routes.go path relative to root
	Published    map[string]string          // "METHOD path" -> operationId from api/openapi.json
	Staged       map[string]StagedOperation // "METHOD path" -> fragment operation
}

var routeDeclPattern = regexp.MustCompile(`\{Method:\s*http\.Method(\w+),\s*Path:\s*"([^"]+)"\}`)

// LoadInventory reads the versioned route map. An unreadable file
// or an unknown schema is a finding, never an empty inventory.
func LoadInventory(path string) (*Inventory, []string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{RuleInventoryUnreadable + ": " + path}
	}
	var inventory Inventory
	if err := json.Unmarshal(raw, &inventory); err != nil {
		return nil, []string{RuleInventoryUnreadable + ": " + path + " is not JSON"}
	}
	if inventory.Schema != 1 {
		return nil, []string{RuleSchemaUnknown + ": want 1"}
	}
	return &inventory, nil
}

// LoadSources rebuilds the normative sets from the tree. A source
// file that cannot be read aborts the load: a comparison against a
// half-read tree would make every missing-route finding a guess.
func LoadSources(root string) (*Sources, error) {
	sources := &Sources{
		Declarations: make(map[string]string),
		Published:    make(map[string]string),
		Staged:       make(map[string]StagedOperation),
	}
	files := []string{}
	for _, pattern := range []string{
		filepath.Join("internal", "*", "adapters", "http", "routes.go"),
		filepath.Join("internal", "*", "adapters", "html", "routes.go"),
	} {
		matched, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, err
		}
		files = append(files, matched...)
	}
	sort.Strings(files)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return nil, err
		}
		for _, match := range routeDeclPattern.FindAllStringSubmatch(string(raw), -1) {
			sources.Declarations[strings.ToUpper(match[1])+" "+match[2]] = rel
		}
	}
	// The two platform probes are normative health routes outside
	// the module adapters.
	for _, path := range []string{"/health/live", "/health/ready"} {
		sources.Declarations["GET "+path] = "internal/platform/httpserver/routes.go"
	}
	published, err := loadContractOperations(filepath.Join(root, "api", "openapi.json"))
	if err != nil {
		return nil, err
	}
	sources.Published = published
	for _, fragment := range []string{
		"internal/seasons/adapters/http/openapi.fragment.json",
		"internal/metering/adapters/http/openapi.fragment.json",
		"internal/commerce/adapters/http/openapi.fragment.json",
		"internal/disputes/adapters/http/openapi.fragment.json",
	} {
		operations, err := loadContractOperations(filepath.Join(root, fragment))
		if err != nil {
			return nil, err
		}
		for key, operation := range operations {
			sources.Staged[key] = StagedOperation{Operation: operation, Fragment: fragment}
		}
	}
	return sources, nil
}

func loadContractOperations(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	operations := make(map[string]string, len(document.Paths))
	for path, methods := range document.Paths {
		for method, operation := range methods {
			switch strings.ToLower(method) {
			case "get", "post", "put", "patch", "delete", "options", "head":
			default:
				continue
			}
			operations[strings.ToUpper(method)+" "+path] = operation.OperationID
		}
	}
	return operations, nil
}

// resolveEvidence answers whether one `path[::Test]` reference names
// a file — and a function — the tree really holds. A bare path is a
// document or browser proof; only Go references name a function.
func resolveEvidence(root, ref string) bool {
	path, function, _ := strings.Cut(ref, "::")
	if path == "" {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return false
	}
	if function == "" {
		return true
	}
	return strings.Contains(string(raw), "func "+function+"(")
}

// isBrowserProof answers whether a resolving reference proves a
// browser binding: a journey test under web/tests or an E2E spec
// under tools/e2e. A contract or unit test proves the declaration,
// never the browser.
func isBrowserProof(ref string) bool {
	path, _, _ := strings.Cut(ref, "::")
	return strings.HasPrefix(path, "web/tests/") || strings.HasPrefix(path, "tools/e2e/")
}

// hasBinding answers whether the entry names a planned consumer.
func hasBinding(entry RouteEntry) (client, page bool) {
	return entry.Client != nil && *entry.Client != "",
		entry.Page != nil && *entry.Page != ""
}

// validExclusion answers whether an exception carries its reason,
// a restricted audience and no production binding on the side.
// Probes and the restricted operator panel are the only audiences
// that may stay out of the browser; a browser API is never
// excluded for lacking an implementation.
func validExclusion(entry RouteEntry) bool {
	if entry.Exclusion == nil || *entry.Exclusion == "" {
		return false
	}
	if entry.Audience != "internal_probe" && entry.Audience != "operator" {
		return false
	}
	client, page := hasBinding(entry)
	return !client && !page
}

// browserVerified answers whether the entry claims a proven browser
// binding backed by at least one resolving browser proof.
func browserVerified(root string, entry RouteEntry) bool {
	switch entry.EvidenceState {
	case EvidenceVerified, EvidenceDone:
	default:
		return false
	}
	for _, ref := range entry.Tests {
		if isBrowserProof(ref) && resolveEvidence(root, ref) {
			return true
		}
	}
	return false
} // judgeTallies counts readiness states across one pass of the
// inventory, so the counts rule compares like with like.
type judgeTallies struct {
	published int
	staged    int
	missing   int
}

// Judge confronts the inventory with the normative sources under
// the given mode. A finding names exactly one broken rule with its
// route; an empty set is the only PASS. The per-entry work lives
// in the focused judges below so each one stays reviewable.
func Judge(root string, inventory *Inventory, sources *Sources, mode string) []string {
	findings := []string{}
	byKey := make(map[string]RouteEntry, len(inventory.Routes))
	var tallies judgeTallies
	for _, entry := range inventory.Routes {
		key := entry.Method + " " + entry.Path
		if _, dup := byKey[key]; dup {
			findings = append(findings, RuleDuplicateRoute+": "+key)
			continue
		}
		byKey[key] = entry
		findings = append(findings, judgeEntryDeclaration(key, entry, sources)...)
		findings = append(findings, judgeEntryContract(key, entry, sources, &tallies)...)
		findings = append(findings, judgeEntryBinding(key, entry)...)
		findings = append(findings, judgeEntryEvidence(root, key, entry)...)
	}
	findings = append(findings, judgeCounts(inventory, tallies)...)
	findings = append(findings, judgeMissingSources(byKey, sources)...)
	if mode == ModeComplete {
		findings = append(findings, judgeComplete(root, inventory)...)
	}
	sort.Strings(findings)
	return findings
}

// judgeEntryDeclaration checks the entry against the routes.go
// registry: every inventoried route is declared exactly there.
func judgeEntryDeclaration(key string, entry RouteEntry, sources *Sources) []string {
	wantDecl, declared := sources.Declarations[key]
	if !declared {
		return []string{RuleUndeclaredRoute + ": " + key + " has no routes.go declaration"}
	}
	if wantDecl != entry.Declaration {
		return []string{RuleUndeclaredRoute + ": " + key + " wants declaration " + wantDecl}
	}
	return nil
}

// judgeEntryContract checks the entry against its contract home
// and tallies its readiness. Unknown readiness is drift: the
// inventory only knows the three states the sources can prove.
func judgeEntryContract(key string, entry RouteEntry, sources *Sources, tallies *judgeTallies) []string {
	switch entry.Readiness {
	case "published":
		tallies.published++
		return judgePublishedContract(key, entry, sources)
	case "staged":
		tallies.staged++
		return judgeStagedContract(key, entry, sources)
	case "missing-contract":
		tallies.missing++
		return judgeContractGap(key, entry, sources)
	default:
		return []string{RuleContractDrift + ": " + key + " has unknown readiness " + entry.Readiness}
	}
}

// judgePublishedContract checks a published entry against
// api/openapi.json.
func judgePublishedContract(key string, entry RouteEntry, sources *Sources) []string {
	if entry.Contract == nil || *entry.Contract != "api/openapi.json" {
		return []string{RuleContractDrift + ": " + key + " leaves api/openapi.json"}
	}
	want, ok := sources.Published[key]
	if !ok {
		return []string{RuleContractDrift + ": " + key + " absent from api/openapi.json"}
	}
	findings := []string{}
	if entry.OperationID == nil || *entry.OperationID != want {
		findings = append(findings, RuleContractDrift+": "+key+" wants operation "+want)
	}
	if entry.EvidenceState == EvidenceMissing {
		findings = append(findings, RuleContractGap+": "+key+" hides a published contract behind a gap")
	}
	return findings
}

// judgeStagedContract checks a staged entry against its fragment
// and its harness-only binding.
func judgeStagedContract(key string, entry RouteEntry, sources *Sources) []string {
	want, ok := sources.Staged[key]
	if !ok {
		return []string{RuleContractDrift + ": " + key + " absent from the staged fragments"}
	}
	findings := []string{}
	if entry.Contract == nil || *entry.Contract != want.Fragment {
		findings = append(findings, RuleContractDrift+": "+key+" wants fragment "+want.Fragment)
	}
	if entry.OperationID == nil || *entry.OperationID != want.Operation {
		findings = append(findings, RuleContractDrift+": "+key+" wants operation "+want.Operation)
	}
	if _, published := sources.Published[key]; published {
		findings = append(findings, RuleContractDrift+": "+key+" staged but published in api/openapi.json")
	}
	findings = append(findings, judgeStagedBinding(key, entry)...)
	if entry.EvidenceState == EvidenceMissing {
		findings = append(findings, RuleContractGap+": "+key+" hides a staged contract behind a gap")
	}
	return findings
}

// judgeStagedBinding refuses a staged route dressed as an active
// surface: the harness binding is the only honest one, and an
// exception would hide the route instead of proving it elsewhere.
func judgeStagedBinding(key string, entry RouteEntry) []string {
	if entry.Exclusion != nil && *entry.Exclusion != "" {
		return []string{RuleStagedAsActive + ": " + key + " excepts a staged route instead of harness-binding it"}
	}
	client, page := hasBinding(entry)
	if !client || !page || *entry.Client != stagedHarnessBinding || *entry.Page != stagedHarnessBinding {
		return []string{RuleStagedAsActive + ": " + key + " treats a staged route as an active surface"}
	}
	return nil
}

// judgeContractGap checks an explicit contract gap: null operation,
// null contract, absent from every contract home, gap evidence.
func judgeContractGap(key string, entry RouteEntry, sources *Sources) []string {
	findings := []string{}
	if entry.OperationID != nil || entry.Contract != nil {
		findings = append(findings, RuleContractGap+": "+key+" must stay an explicit null-operation gap")
	}
	if _, ok := sources.Published[key]; ok {
		findings = append(findings, RuleContractGap+": "+key+" contracted in api/openapi.json")
	}
	if _, ok := sources.Staged[key]; ok {
		findings = append(findings, RuleContractGap+": "+key+" contracted in a staged fragment")
	}
	if entry.EvidenceState != EvidenceMissing {
		findings = append(findings, RuleContractGap+": "+key+" wants evidence "+EvidenceMissing)
	}
	return findings
}

// judgeEntryBinding checks the consumer side: a planned client and
// page, or a valid exception — never half a binding and never
// silence.
func judgeEntryBinding(key string, entry RouteEntry) []string {
	client, page := hasBinding(entry)
	_, excluded := excludedEntry(entry)
	switch {
	case client && !page:
		return []string{RuleClientWithoutPage + ": " + key}
	case page && !client:
		return []string{RulePageWithoutClient + ": " + key}
	case !client && !page && !excluded:
		return []string{RuleExclusionUnjustified + ": " + key + " has neither a consumer nor an exclusion"}
	case excluded && !validExclusion(entry):
		return []string{RuleExclusionUnjustified + ": " + key}
	default:
		return nil
	}
}

// judgeEntryEvidence checks that every named proof resolves and
// that a production claim carries a browser proof.
func judgeEntryEvidence(root, key string, entry RouteEntry) []string {
	findings := []string{}
	for _, ref := range entry.Tests {
		if !resolveEvidence(root, ref) {
			findings = append(findings, RuleEvidenceMissing+": "+key+" points at missing evidence "+ref)
		}
	}
	switch entry.EvidenceState {
	case EvidenceNotVerified, EvidenceMissing:
	case EvidenceVerified, EvidenceDone:
		if !browserVerified(root, entry) {
			findings = append(findings, RuleTestOnlyAsProduction+": "+key+" declares production without a browser proof")
		}
	default:
		findings = append(findings, RuleEvidenceUnknown+": "+key+" carries "+entry.EvidenceState)
	}
	return findings
}

// judgeCounts checks the header tallies against the rows: the
// counts describe the inventory, they never negotiate with it.
func judgeCounts(inventory *Inventory, tallies judgeTallies) []string {
	if inventory.Counts.Declared != len(inventory.Routes) ||
		inventory.Counts.Published != tallies.published ||
		inventory.Counts.Staged != tallies.staged ||
		inventory.Counts.MissingPublishedContract != tallies.missing {
		return []string{RuleCountsMismatch + ": inventory counts its rows"}
	}
	return nil
}

// judgeMissingSources accuses every normative route and operation
// the inventory omits: a new declaration and a dropped contract
// row fail alike.
func judgeMissingSources(byKey map[string]RouteEntry, sources *Sources) []string {
	findings := []string{}
	for key, decl := range sources.Declarations {
		if _, ok := byKey[key]; !ok {
			findings = append(findings, RuleRouteMissing+": "+key+" declared by "+decl)
		}
	}
	for key := range sources.Published {
		if _, ok := byKey[key]; !ok {
			findings = append(findings, RuleRouteMissing+": "+key+" contracted by api/openapi.json")
		}
	}
	for key, staged := range sources.Staged {
		if _, ok := byKey[key]; !ok {
			findings = append(findings, RuleRouteMissing+": "+key+" contracted by "+staged.Fragment)
		}
	}
	return findings
}

// judgeComplete demands zero browser gap: every route without a
// valid exception proves a browser-verified binding.
func judgeComplete(root string, inventory *Inventory) []string {
	findings := []string{}
	for _, entry := range inventory.Routes {
		key := entry.Method + " " + entry.Path
		if _, excluded := excludedEntry(entry); excluded && validExclusion(entry) {
			continue
		}
		if !browserVerified(root, entry) {
			findings = append(findings, RuleCoverageGap+": "+key+" has no browser-verified binding")
		}
	}
	return findings
}

// excludedEntry answers whether the entry carries any exclusion
// text. Validity is judged separately: an invalid exclusion is a
// finding, not a pass.
func excludedEntry(entry RouteEntry) (string, bool) {
	if entry.Exclusion == nil || *entry.Exclusion == "" {
		return "", false
	}
	return *entry.Exclusion, true
}
