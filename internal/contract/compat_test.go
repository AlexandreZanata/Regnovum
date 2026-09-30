package contract_test

// Tests of the API compatibility gate (P25-T07): the supported baseline
// only moves inside an explicit task, every breaking-change category is
// refused by a fixture that breaks exactly it, compatible evolution passes,
// and breaking changes without a version bump are refused as unversioned.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/contract"
)

func loadBaselineBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/openapi.baseline.json")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	return raw
}

func loadCurrentBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.json")
	if err != nil {
		t.Fatalf("read current contract: %v", err)
	}
	return raw
}

func decodeBaseline(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	return document
}

func remmarshal(t *testing.T, document map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("re-encode mutated baseline: %v", err)
	}
	return raw
}

func findingCodes(findings []contract.BreakingChange) []string {
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	return codes
}

func requireCode(t *testing.T, findings []contract.BreakingChange, code string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("findings %q do not include %q", findingCodes(findings), code)
}

// TestBaselinePinAndMatchesCurrent locks the supported snapshot: the file
// matches its pin, and the current contract introduces no breaking change
// at the same version.
func TestBaselinePinAndMatchesCurrent(t *testing.T) {
	t.Parallel()

	rawBaseline := loadBaselineBytes(t)
	digest := sha256.Sum256(rawBaseline)
	if hex.EncodeToString(digest[:]) != contract.BaselineSHA256 {
		t.Fatal("baseline drifted from its pin: regenerate it only inside an explicit task")
	}
	if err := contract.CheckBaselinePin(".", contract.BaselinePath); err != nil {
		t.Fatalf("baseline pin: %v", err)
	}
	rawCurrent := loadCurrentBytes(t)
	findings := contract.DiffRawBaseline(rawBaseline, rawCurrent)
	if len(findings) != 0 {
		for _, finding := range findings {
			t.Errorf("finding: %s", finding)
		}
		t.Fatal("current contract breaks its supported baseline")
	}
	if violations := contract.RequireVersionBump(rawBaseline, rawCurrent, findings); len(violations) != 0 {
		t.Fatalf("version gate fired without breakings: %v", violations)
	}
}

// breakFixture is one way compatibility can be lost: a mutation of a
// baseline copy plus the exact rule it must break (naming it in full keeps
// a mutation that reaches further than its intent from passing as if it
// reached exactly its intent).
type breakFixture struct {
	name  string
	rules []string
	mute  func(t *testing.T, document map[string]any)
}

func breakFixtures() []breakFixture {
	at := func(document map[string]any, path ...string) map[string]any {
		current := document
		for _, segment := range path {
			next, ok := current[segment].(map[string]any)
			if !ok {
				return nil
			}
			current = next
		}
		return current
	}
	return []breakFixture{
		{
			name:  "a removed path",
			rules: []string{contract.CodeRemovedPath},
			mute: func(t *testing.T, document map[string]any) {
				t.Helper()
				delete(at(document, "paths"), "/api/v1/arenas")
			},
		},
		{
			name:  "a removed method",
			rules: []string{contract.CodeRemovedMethod},
			mute: func(t *testing.T, document map[string]any) {
				t.Helper()
				delete(at(document, "paths", "/api/v1/arenas/{slug}"), "get")
			},
		},
		{
			name:  "a removed response status",
			rules: []string{contract.CodeRemovedStatus},
			mute: func(t *testing.T, document map[string]any) {
				t.Helper()
				delete(at(document, "paths", "/api/v1/arenas", "get", "responses"), "200")
			},
		},
		{
			name:  "a newly required field",
			rules: []string{contract.CodeAddedRequired, contract.CodeAddedRequired},
			mute: func(t *testing.T, document map[string]any) {
				t.Helper()
				schema := at(document, "components", "schemas", "ArenaDraftRequest")
				required, _ := schema["required"].([]any)
				schema["required"] = append(required, "context")
			},
		},
		{
			name:  "a removed property",
			rules: []string{contract.CodeRemovedProperty, contract.CodeRemovedProperty},
			mute: func(t *testing.T, document map[string]any) {
				t.Helper()
				delete(at(document, "components", "schemas", "ArenaDraftRequest", "properties"), "context")
			},
		},
		{
			name:  "a changed property type",
			rules: []string{contract.CodeChangedType, contract.CodeChangedType},
			mute: func(t *testing.T, document map[string]any) {
				t.Helper()
				at(document, "components", "schemas", "WalletBalance", "properties", "balance_free")["type"] = "string"
			},
		},
		{
			name:  "a narrowed enum",
			rules: []string{contract.CodeNarrowedEnum, contract.CodeNarrowedEnum, contract.CodeNarrowedEnum},
			mute: func(t *testing.T, document map[string]any) {
				t.Helper()
				at(document, "components", "schemas", "PositionRequest", "properties", "position")["enum"] = []any{"agree", "disagree"}
			},
		},
		{
			name:  "a removed operation security",
			rules: []string{contract.CodeChangedSecurity},
			mute: func(t *testing.T, document map[string]any) {
				t.Helper()
				delete(at(document, "paths", "/api/v1/me/arena-drafts", "post"), "security")
			},
		},
		{
			name:  "a removed security scheme",
			rules: []string{contract.CodeRemovedScheme},
			mute: func(t *testing.T, document map[string]any) {
				t.Helper()
				delete(at(document, "components", "securitySchemes"), "SessionCookie")
			},
		},
	}
}

// TestEachBreakingChangeRefusesItsFixture is the falsification of every
// breaking category: one mutation, the exact expected rules, and a
// restored tree afterwards (mutations run on copies).
func TestEachBreakingChangeRefusesItsFixture(t *testing.T) {
	t.Parallel()

	rawBaseline := loadBaselineBytes(t)
	for _, fixture := range breakFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			document := decodeBaseline(t, rawBaseline)
			fixture.mute(t, document)
			findings := contract.DiffRawBaseline(rawBaseline, remmarshal(t, document))
			if len(findings) == 0 {
				t.Fatalf("nothing was refused, want %v", fixture.rules)
			}
			got := findingCodes(findings)
			sort.Strings(got)
			want := append([]string{}, fixture.rules...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				for _, finding := range findings {
					t.Errorf("finding: %s", finding)
				}
				t.Fatalf("the mutation broke %v; it should have broken %v", got, want)
			}
		})
	}
}

// TestCompatibleAdditionsPass proves the gate is not a freeze: compatible
// evolution (new paths, methods, statuses, optional fields, wider enums and
// relaxed requirements) introduces no breaking change.
func TestCompatibleAdditionsPass(t *testing.T) {
	t.Parallel()

	rawBaseline := loadBaselineBytes(t)
	document := decodeBaseline(t, rawBaseline)

	paths := document["paths"].(map[string]any)
	paths["/api/v1/t07-probe"] = map[string]any{
		"get": map[string]any{"responses": map[string]any{"200": map[string]any{"description": "probe"}}},
	}
	arenas := paths["/api/v1/arenas"].(map[string]any)
	arenas["delete"] = map[string]any{"responses": map[string]any{"200": map[string]any{"description": "probe"}}}
	feedGet := arenas["get"].(map[string]any)
	feedGet["responses"].(map[string]any)["202"] = map[string]any{"description": "probe"}
	draft := document["components"].(map[string]any)["schemas"].(map[string]any)["ArenaDraftRequest"].(map[string]any)
	draft["properties"].(map[string]any)["nickname"] = map[string]any{"type": "string"}
	position := document["components"].(map[string]any)["schemas"].(map[string]any)["PositionRequest"].(map[string]any)["properties"].(map[string]any)["position"].(map[string]any)
	position["enum"] = []any{"agree", "disagree", "undecided", "observing"}
	var required []any
	for _, field := range draft["required"].([]any) {
		if field != "language" {
			required = append(required, field)
		}
	}
	draft["required"] = required

	if findings := contract.DiffRawBaseline(rawBaseline, remmarshal(t, document)); len(findings) != 0 {
		for _, finding := range findings {
			t.Errorf("finding: %s", finding)
		}
		t.Fatal("compatible additions were refused")
	}
}

// TestUnversionedBreakingRequiresBump proves the release rule: breaking
// changes at the same version are refused as unversioned, while the same
// breakings under a new version pass the version gate (the breakings
// themselves stay listed).
func TestUnversionedBreakingRequiresBump(t *testing.T) {
	t.Parallel()

	rawBaseline := loadBaselineBytes(t)
	document := decodeBaseline(t, rawBaseline)
	delete(document["paths"].(map[string]any), "/api/v1/arenas")
	rawBreaking := remmarshal(t, document)

	findings := contract.DiffRawBaseline(rawBaseline, rawBreaking)
	requireCode(t, findings, contract.CodeRemovedPath)
	if violations := contract.RequireVersionBump(rawBaseline, rawBreaking, findings); len(violations) != 1 {
		t.Fatalf("violations = %v, want exactly the unversioned-breaking-change refusal", violations)
	} else if violations[0].Code != contract.CodeUnversionedBreaking {
		t.Fatalf("violation = %v, want %s", violations[0], contract.CodeUnversionedBreaking)
	}

	bumped := decodeBaseline(t, rawBreaking)
	bumped["info"].(map[string]any)["version"] = "0.2.0"
	rawBumped := remmarshal(t, bumped)
	if violations := contract.RequireVersionBump(rawBaseline, rawBumped, contract.DiffRawBaseline(rawBaseline, rawBumped)); len(violations) != 0 {
		t.Fatalf("a versioned breaking change must pass the version gate: %v", violations)
	}
}
