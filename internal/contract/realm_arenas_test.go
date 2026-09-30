package contract_test

// Tests of the realm vocabulary in the API contract (P39-T03):
// Regnovum is the single Kingdom and an Arena is one debate instance.
// The arena routes are pinned so presentation renames never move a
// URL, and no arena path or arena schema may name a winner, a verdict
// or an official truth: common debates keep no official outcome.

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// pinnedArenaPaths is the exact arena surface: API routes and page
// routes. A rename moves through an explicit task, never by drift.
var pinnedArenaPaths = []string{
	"/api/v1/arenas",
	"/api/v1/arenas/{id}/arguments",
	"/api/v1/arenas/{id}/export",
	"/api/v1/arenas/{slug}",
	"/api/v1/arenas/{id}/positions",
	"/api/v1/me/arena-drafts",
	"/api/v1/me/arena-drafts/{id}",
	"/api/v1/me/arena-drafts/{id}/publish",
	"/api/v1/me/arenas/{id}/arguments",
	"/api/v1/me/arenas/{id}/arguments/{argumentID}/replies",
	"/api/v1/me/arenas/{id}/close",
	"/api/v1/me/arenas/{id}/position",
	"/api/v1/me/arenas/{id}/position/changes",
	"/api/v1/search/arenas",
	"/arenas/{slug}",
	"/arenas/{slug}/arguments",
	"/arenas/{slug}/attributions",
	"/arenas/{slug}/position",
	"/arenas/{slug}/position/change",
}

var outcomePattern = regexp.MustCompile(`(?i)winner|verdict|truth|vencedor|verdade`)

func loadRealmContract(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.json")
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	return document
}

func remarshalRealm(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	return string(raw)
}

func TestArenaRoutesStayStable(t *testing.T) {
	document := loadRealmContract(t)
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatal("contract has no paths object")
	}
	var arena []string
	for path := range paths {
		if strings.Contains(strings.ToLower(path), "arena") {
			arena = append(arena, path)
		}
	}
	sort.Strings(arena)
	want := append([]string{}, pinnedArenaPaths...)
	sort.Strings(want)
	if len(arena) != len(want) {
		t.Fatalf("arena paths = %v, want %v", arena, want)
	}
	for i := range arena {
		if arena[i] != want[i] {
			t.Fatalf("arena paths = %v, want %v", arena, want)
		}
	}
}

func TestArenaSurfaceNamesNoOfficialOutcome(t *testing.T) {
	document := loadRealmContract(t)
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatal("contract has no paths object")
	}
	for path, operations := range paths {
		if !strings.Contains(strings.ToLower(path), "arena") {
			continue
		}
		if outcomePattern.MatchString(remarshalRealm(t, operations)) {
			t.Fatalf("path %s names a winner, verdict or truth: debates keep no official outcome", path)
		}
	}
	components, ok := document["components"].(map[string]any)
	if !ok {
		t.Fatal("contract has no components object")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("contract has no schemas object")
	}
	for name, schema := range schemas {
		if !strings.Contains(strings.ToLower(name), "rena") {
			continue
		}
		if outcomePattern.MatchString(remarshalRealm(t, schema)) {
			t.Fatalf("schema %s names a winner, verdict or truth: debates keep no official outcome", name)
		}
	}
}
