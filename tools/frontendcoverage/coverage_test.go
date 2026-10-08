// Tests of the frontend route coverage gate (P48-T02).
//
// Three properties hold, and each alone would be worthless:
//
//   - the inventory this repository commits passes in planning
//     mode without claiming complete coverage, judged against the
//     real contract, fragments and routes.go declarations;
//   - each rule refuses an inventory that breaks exactly it. The
//     edits below change one thing in the green inventory and
//     require the finding, with the precondition asserted, so a
//     rule that stopped working fails here instead of letting a
//     gap reach the browser. A new tree route, an omitted
//     contract operation, a client without a page, missing
//     evidence, test-only work declared as production, a staged
//     route treated as active and an unjustified exclusion each
//     fail;
//   - every rule the gate declares is named by at least one
//     refusal, recorded in exercised and closed by the last test.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exercised records every rule a refusal has named.
// TestAllRulesExercised closes the set.
var exercised = map[string]bool{}

func mark(rule string) {
	base := rule
	if index := strings.Index(rule, ":"); index >= 0 {
		base = rule[:index]
	}
	exercised[base] = true
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

func loadGreen(t *testing.T) *Inventory {
	t.Helper()
	inventory, findings := LoadInventory(filepath.Join(repoRoot(t), "quality", "frontend-routes.json"))
	if len(findings) != 0 {
		t.Fatalf("green inventory refused: %v", findings)
	}
	return inventory
}

func greenSources(t *testing.T) *Sources {
	t.Helper()
	sources, err := LoadSources(repoRoot(t))
	if err != nil {
		t.Fatalf("load normative sources: %v", err)
	}
	return sources
}

func findEntry(inventory *Inventory, method, path string) *RouteEntry {
	for i := range inventory.Routes {
		if inventory.Routes[i].Method == method && inventory.Routes[i].Path == path {
			return &inventory.Routes[i]
		}
	}
	return nil
}

func strptr(value string) *string { return &value }

// judgeGreen edits a copy of the committed inventory and judges
// the result against the real normative sources in planning mode.
func judgeGreen(t *testing.T, edit func(*Inventory)) []string {
	t.Helper()
	return Judge(repoRoot(t), editGreenInventory(t, edit), greenSources(t), ModePlanning)
}

func editGreenInventory(t *testing.T, edit func(*Inventory)) *Inventory {
	t.Helper()
	raw, err := json.Marshal(loadGreen(t))
	if err != nil {
		t.Fatalf("marshal green inventory: %v", err)
	}
	var inventory Inventory
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatalf("copy green inventory: %v", err)
	}
	edit(&inventory)
	return &inventory
}

func requireRule(t *testing.T, findings []string, rule string) {
	t.Helper()
	mark(rule)
	for _, finding := range findings {
		if finding == rule || strings.HasPrefix(finding, rule+":") {
			return
		}
	}
	t.Fatalf("rule %q did not fire: %v", rule, findings)
}

func requireClean(t *testing.T, findings []string) {
	t.Helper()
	if len(findings) != 0 {
		t.Fatalf("expected no findings: %v", findings)
	}
}

func TestPlanningPassesOnRealTree(t *testing.T) {
	// Planning accepts the declared pending work of P48-T01 without
	// certifying it: the committed inventory holds every rule in
	// planning mode.
	requireClean(t, Judge(repoRoot(t), loadGreen(t), greenSources(t), ModePlanning))
}

func TestCompletePassesOnRealTree(t *testing.T) {
	// P58-T01 earned the complete posture on the real tree: every
	// route carries a bound client, a page and a resolving
	// browser proof. This test guards the earned posture — a
	// broken binding or proof fails here. Refusals that prove
	// complete never fakes coverage live in
	// TestCompleteRefusesUnverifiedInventory and
	// TestEnvSelectsComplete.
	requireClean(t, Judge(repoRoot(t), loadGreen(t), greenSources(t), ModeComplete))
}

func TestDuplicateRouteFails(t *testing.T) {
	findings := judgeGreen(t, func(inventory *Inventory) {
		if len(inventory.Routes) == 0 {
			t.Fatal("green inventory is empty")
		}
		inventory.Routes = append(inventory.Routes, inventory.Routes[0])
	})
	requireRule(t, findings, RuleDuplicateRoute)
}

func TestNewTreeRouteFails(t *testing.T) {
	// A route the tree declares but the inventory omits — the new
	// route case — fails instead of shipping uncovered.
	sources := greenSources(t)
	key := "GET /api/v1/search/novelty"
	if _, dup := sources.Declarations[key]; dup {
		t.Fatalf("fixture route %q already declared", key)
	}
	sources.Declarations[key] = "internal/search/adapters/http/routes.go"
	findings := Judge(repoRoot(t), loadGreen(t), sources, ModePlanning)
	requireRule(t, findings, RuleRouteMissing)
}

func TestOmittedContractRouteFails(t *testing.T) {
	// Dropping a contracted operation from the inventory — the
	// omitted route case — fails on the contract side too.
	findings := judgeGreen(t, func(inventory *Inventory) {
		kept := inventory.Routes[:0]
		found := false
		for _, entry := range inventory.Routes {
			if entry.Method == "GET" && entry.Path == "/api/v1/arenas" {
				found = true
				continue
			}
			kept = append(kept, entry)
		}
		if !found {
			t.Fatal("fixture entry GET /api/v1/arenas is gone")
		}
		inventory.Routes = kept
	})
	requireRule(t, findings, RuleRouteMissing)
}

func TestInventoryWithoutDeclarationFails(t *testing.T) {
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "GET", "/api/v1/arenas")
		if entry == nil {
			t.Fatal("fixture entry GET /api/v1/arenas is gone")
		}
		entry.Path = "/api/v1/arenas-that-do-not-exist"
	})
	requireRule(t, findings, RuleUndeclaredRoute)
}

func TestContractDriftFails(t *testing.T) {
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "GET", "/api/v1/arenas")
		if entry == nil {
			t.Fatal("fixture entry GET /api/v1/arenas is gone")
		}
		if entry.OperationID == nil || *entry.OperationID != "getArenaFeed" {
			t.Fatalf("fixture operation drifted: %v", entry.OperationID)
		}
		entry.OperationID = strptr("getArenaFeedRenamed")
	})
	requireRule(t, findings, RuleContractDrift)
}

func TestContractGapFails(t *testing.T) {
	// P55 closed every contract gap, so the refusal resurrects
	// one: a login route flipped to missing-contract while
	// carrying its operationId is a contradiction, and the tallies
	// move with it so the refusal stays focused on the gap.
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "POST", "/api/v1/auth/login")
		if entry == nil {
			t.Fatal("fixture entry POST /api/v1/auth/login is gone")
		}
		if entry.Readiness != "published" || entry.OperationID == nil {
			t.Fatal("fixture entry is no published contracted route")
		}
		entry.Readiness = "missing-contract"
		inventory.Counts.Published--
		inventory.Counts.MissingPublishedContract++
	})
	requireRule(t, findings, RuleContractGap)
}

func TestClientWithoutPageFails(t *testing.T) {
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "POST", "/api/v1/auth/login")
		if entry == nil {
			t.Fatal("fixture entry POST /api/v1/auth/login is gone")
		}
		if entry.Client == nil || entry.Page == nil {
			t.Fatal("fixture entry has no planned binding")
		}
		entry.Page = nil
	})
	requireRule(t, findings, RuleClientWithoutPage)
}

func TestPageWithoutClientFails(t *testing.T) {
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "POST", "/api/v1/auth/login")
		if entry == nil {
			t.Fatal("fixture entry POST /api/v1/auth/login is gone")
		}
		if entry.Client == nil || entry.Page == nil {
			t.Fatal("fixture entry has no planned binding")
		}
		entry.Client = nil
	})
	requireRule(t, findings, RulePageWithoutClient)
}

func TestEvidenceMissingFails(t *testing.T) {
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "GET", "/health/live")
		if entry == nil {
			t.Fatal("fixture entry GET /health/live is gone")
		}
		if len(entry.Tests) == 0 {
			t.Fatal("fixture entry lists no tests")
		}
		entry.Tests[0] = "internal/platform/httpserver/no-such-file_test.go::TestHealthEndpoints"
	})
	requireRule(t, findings, RuleEvidenceMissing)
}

func TestEvidenceUnknownFails(t *testing.T) {
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "GET", "/api/v1/arenas")
		if entry == nil {
			t.Fatal("fixture entry GET /api/v1/arenas is gone")
		}
		entry.EvidenceState = "PENDING"
	})
	requireRule(t, findings, RuleEvidenceUnknown)
}

func TestTestOnlyAsProductionFails(t *testing.T) {
	// Declaring production on contract tests alone — without a
	// browser proof — fails in planning too: planning records
	// pending work, it never certifies it. P58-T01 earned browser
	// proofs for the arena feed, so the refusal strips them first.
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "GET", "/api/v1/arenas")
		if entry == nil {
			t.Fatal("fixture entry GET /api/v1/arenas is gone")
		}
		kept := entry.Tests[:0]
		proofs := 0
		for _, ref := range entry.Tests {
			if isBrowserProof(ref) {
				proofs++
				continue
			}
			kept = append(kept, ref)
		}
		if proofs == 0 {
			t.Fatal("fixture entry carries no browser proof to strip")
		}
		entry.Tests = kept
		entry.EvidenceState = EvidenceDone
	})
	requireRule(t, findings, RuleTestOnlyAsProduction)
}

func TestStagedAsActiveFails(t *testing.T) {
	// A staged route bound to a production client is an active
	// surface in disguise: staged routes live behind the harness
	// binding only.
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "GET", "/api/v1/me/seasons/current")
		if entry == nil {
			t.Fatal("fixture entry GET /api/v1/me/seasons/current is gone")
		}
		if entry.Client == nil || *entry.Client != stagedHarnessBinding {
			t.Fatalf("fixture entry lost its harness binding: %v", entry.Client)
		}
		entry.Client = strptr("arenas")
	})
	requireRule(t, findings, RuleStagedAsActive)
}

func TestExclusionWithoutReasonFails(t *testing.T) {
	// An exception without a reason is not an exception: the probe
	// exclusion emptied of its motive fails.
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "GET", "/health/live")
		if entry == nil {
			t.Fatal("fixture entry GET /health/live is gone")
		}
		if entry.Exclusion == nil || *entry.Exclusion == "" {
			t.Fatal("fixture entry carries no exclusion")
		}
		entry.Exclusion = strptr("")
	})
	requireRule(t, findings, RuleExclusionUnjustified)
}

func TestBrowserAPIExclusionFails(t *testing.T) {
	// A browser API is never excluded for lacking an
	// implementation: the public feed with an exclusion instead of
	// a consumer fails.
	findings := judgeGreen(t, func(inventory *Inventory) {
		entry := findEntry(inventory, "GET", "/api/v1/arenas")
		if entry == nil {
			t.Fatal("fixture entry GET /api/v1/arenas is gone")
		}
		entry.Client, entry.Page = nil, nil
		entry.Exclusion = strptr("not implemented yet")
	})
	requireRule(t, findings, RuleExclusionUnjustified)
}

func TestCountsMismatchFails(t *testing.T) {
	findings := judgeGreen(t, func(inventory *Inventory) {
		if inventory.Counts.Published != 85 {
			t.Fatalf("fixture counts drifted: %d", inventory.Counts.Published)
		}
		inventory.Counts.Published = 84
	})
	requireRule(t, findings, RuleCountsMismatch)
}

func TestSchemaUnknownFails(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "quality", "frontend-routes.json"))
	if err != nil {
		t.Fatalf("read green inventory: %v", err)
	}
	if !strings.Contains(string(raw), `"schema": 1`) {
		t.Fatal("fixture schema marker is gone")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "frontend-routes.json")
	if err := os.WriteFile(path, bytes.Replace(raw, []byte(`"schema": 1`), []byte(`"schema": 2`), 1), 0o644); err != nil {
		t.Fatalf("write mutated inventory: %v", err)
	}
	_, findings := LoadInventory(path)
	requireRule(t, findings, RuleSchemaUnknown)
}

func TestInventoryUnreadableFails(t *testing.T) {
	_, findings := LoadInventory(filepath.Join(t.TempDir(), "frontend-routes.json"))
	requireRule(t, findings, RuleInventoryUnreadable)
}

// verifiedInventory builds one browser-verified entry with a
// resolving browser proof and matching sources, the posture P58
// must earn for every route.
func verifiedInventory(t *testing.T) (*Inventory, *Sources) {
	t.Helper()
	root := repoRoot(t)
	proof := "web/tests/pages/participation.test.ts"
	if _, err := os.Stat(filepath.Join(root, proof)); err != nil {
		t.Fatalf("browser proof fixture is gone: %v", err)
	}
	operation := "getArenaFeed"
	contract := "api/openapi.json"
	declaration := "internal/arenas/adapters/http/routes.go"
	inventory := &Inventory{
		Schema: 1,
		Routes: []RouteEntry{
			{
				Method: "GET", Path: "/api/v1/arenas",
				OperationID: &operation, Contract: &contract, Declaration: declaration,
				Readiness: "published", Audience: "public", Phase: "P52", Task: "P58-T01",
				Client: strptr("arenas"), Page: strptr("planned-arenas"),
				Tests:         []string{declaration, contract, "internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes", proof},
				EvidenceState: EvidenceVerified,
			},
		},
	}
	inventory.Counts.Declared = 1
	inventory.Counts.Published = 1
	sources := &Sources{
		Declarations: map[string]string{"GET /api/v1/arenas": declaration},
		Published:    map[string]string{"GET /api/v1/arenas": operation},
		Staged:       map[string]StagedOperation{},
	}
	return inventory, sources
}

func TestCompletePassesOnVerifiedInventory(t *testing.T) {
	// Complete mode can pass: one browser-verified entry with a
	// resolving browser proof and matching sources holds every
	// rule, so the posture is enforceable when P58 earns it.
	inventory, sources := verifiedInventory(t)
	requireClean(t, Judge(repoRoot(t), inventory, sources, ModeComplete))
}

func TestCompleteRefusesUnverifiedInventory(t *testing.T) {
	// VERIFIED without a resolving browser proof is a claim, not
	// a proof: the entry collapses to an unbound client and
	// complete names the gap instead of passing quietly.
	inventory, sources := verifiedInventory(t)
	entry := findEntry(inventory, "GET", "/api/v1/arenas")
	if entry == nil {
		t.Fatal("verified fixture entry GET /api/v1/arenas is gone")
	}
	entry.Tests = entry.Tests[:len(entry.Tests)-1]
	entry.Page = nil
	findings := Judge(repoRoot(t), inventory, sources, ModeComplete)
	requireRule(t, findings, RuleCoverageGap)
}

func TestDefaultModeIsPlanning(t *testing.T) {
	t.Setenv(modeEnv, "")
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-root", repoRoot(t)}, &stdout, &stderr); got != exitOK {
		t.Fatalf("default mode exit = %d: %s %s", got, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "mode=planning") {
		t.Fatalf("default verdict does not name planning: %q", stdout.String())
	}
}

func TestEnvSelectsComplete(t *testing.T) {
	// FRONTEND_COVERAGE_MODE=complete judges the real tree as
	// complete: P58-T01 earned the posture, so the earned tree
	// holds. Refusals proving complete never fakes coverage live
	// in TestCompleteRefusesUnverifiedInventory.
	t.Setenv(modeEnv, ModeComplete)
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-root", repoRoot(t)}, &stdout, &stderr); got != exitOK {
		t.Fatalf("env complete exit = %d: %s %s", got, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "mode=complete") {
		t.Fatalf("env complete verdict does not name complete: %q", stdout.String())
	}
}

func TestModeFlagRejectsUnknown(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-mode", "bogus"}, &stdout, &stderr); got != exitUsage {
		t.Fatalf("bogus mode exit = %d", got)
	}
}

func TestAllRulesExercised(t *testing.T) {
	for _, rule := range AllRules() {
		if !exercised[rule] {
			t.Errorf("rule %q was never refused by a fixture", rule)
		}
	}
}
