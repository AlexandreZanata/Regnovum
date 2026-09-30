package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureMatrix() Matrix {
	invs := []Invariant{
		{
			ID:               "INV-FIXTURE-IDENTITY",
			Module:           "identity",
			Packages:         []string{"internal/fixture/service"},
			Risk:             "Q0",
			Transition:       "absent -> pending: fixture registration persists",
			Precondition:     "Anyone may register with a strong password.",
			Postcondition:    "Repeating the registration does not duplicate the account.",
			GlobalInvariant:  "Concurrent inserts serialize on the email key. Security: Argon2id with unique salt.",
			ProhibitedEffect: "Must not reveal whether the email already exists.",
			Catalog:          "QUAL-FIXTURE-IDENTITY",
			Persisted:        true,
			Unreachable:      true,
			Tests: Tests{
				Valid:   "internal/fixture/service/rules_test.go::TestPlacementInsideIsAccepted",
				Invalid: "internal/fixture/service/rules_test.go::TestPlacementOutsideIsRefused",
				Limit:   "internal/fixture/service/rules_test.go::TestPlacementAtTheEdgeIsAccepted",
				Replay:  "internal/fixture/service/rules_test.go::TestReplayedPlacementDoesNotCountTwice",
			},
		},
	}
	for _, mod := range RequiredModules {
		if mod == "identity" {
			continue
		}
		id := "QUAL-FIXTURE-" + strings.ToUpper(mod)
		invs = append(invs, Invariant{
			ID:               "INV-FIXTURE-" + strings.ToUpper(mod),
			Module:           mod,
			Packages:         []string{"internal/fixture/service"},
			Risk:             "Q0",
			Transition:       "live -> dead is refused in " + mod + ": unreachable proof",
			Precondition:     "Only the owner may move the fixture in " + mod + ".",
			Postcondition:    "Repeating the refused move stays refused.",
			GlobalInvariant:  "The fixture is append-only in " + mod + ". Security: no secret leaves the tree.",
			ProhibitedEffect: "Must not move the fixture out of its terminal state in " + mod + ".",
			Catalog:          id,
			Persisted:        false,
			Unreachable:      true,
			Tests: Tests{
				Valid:   "internal/fixture/service/orders_test.go::TestConfirmationSucceeds",
				Invalid: "internal/fixture/service/orders_test.go::TestSecondConfirmationIsRefused",
				Limit:   "internal/fixture/service/rules_test.go::TestPlacementAtTheEdgeIsAccepted",
				Replay:  "internal/fixture/service/rules_test.go::TestReplayedPlacementDoesNotCountTwice",
			},
		})
	}
	return Matrix{
		SchemaVersion: 1,
		Generator:     "tools/qualityinvariants",
		Modules:       RequiredModules,
		Invariants:    invs,
	}
}

func fixtureCatalog() []byte {
	rules := []any{}
	for _, inv := range fixtureMatrix().Invariants {
		rules = append(rules, map[string]any{"id": inv.Catalog, "risk": "Q0"})
	}
	catalog := map[string]any{
		"schema_version": 1,
		"rules":          rules,
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		panic(err)
	}
	return raw
}

func writeFixtureTree(t *testing.T, matrix Matrix) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("the fixture tree cannot be built: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("the fixture tree cannot be built: %v", err)
		}
	}
	raw, err := json.Marshal(matrix)
	if err != nil {
		t.Fatalf("the fixture matrix cannot be encoded: %v", err)
	}
	write(InvariantsPath, string(raw))
	write(CatalogPath, string(fixtureCatalog()))
	write("internal/fixture/service/rules_test.go", "package service\n\nimport \"testing\"\n\nfunc TestPlacementInsideIsAccepted(t *testing.T) {}\nfunc TestPlacementOutsideIsRefused(t *testing.T) {}\nfunc TestPlacementAtTheEdgeIsAccepted(t *testing.T) {}\nfunc TestReplayedPlacementDoesNotCountTwice(t *testing.T) {}\n")
	write("internal/fixture/service/orders_test.go", "package service\n\nimport \"testing\"\n\nfunc TestConfirmationSucceeds(t *testing.T) {}\nfunc TestSecondConfirmationIsRefused(t *testing.T) {}\n")
	write("internal/fixture/service/doc.go", "package service\n")
	return root
}

func encodeMatrix(t *testing.T, matrix Matrix) []byte {
	t.Helper()
	raw, err := json.Marshal(matrix)
	if err != nil {
		t.Fatalf("the fixture matrix cannot be encoded: %v", err)
	}
	return raw
}

func TestTheCompleteFixtureIsAccepted(t *testing.T) {
	matrix := fixtureMatrix()
	root := writeFixtureTree(t, matrix)
	loaded, violations := ReadMatrix(root, InvariantsPath)
	if len(violations) > 0 {
		t.Fatalf("the complete fixture is refused: %v", violations)
	}
	if len(loaded.Invariants) != len(RequiredModules) {
		t.Fatalf("the accepted matrix holds %d invariants, want %d", len(loaded.Invariants), len(RequiredModules))
	}
	if violations := Check(root, loaded); len(violations) > 0 {
		t.Fatalf("the complete fixture fails the tree check: %v", violations)
	}
}

func TestEveryDefectIsRefused(t *testing.T) {
	cases := []struct {
		name string
		mut  func(Matrix) Matrix
		code string
	}{
		{"empty matrix", func(m Matrix) Matrix { m.Invariants = nil; return m }, "matrix-empty"},
		{"duplicate id", func(m Matrix) Matrix { m.Invariants[1].ID = m.Invariants[0].ID; return m }, "invariant-id-duplicate"},
		{"bad risk", func(m Matrix) Matrix { m.Invariants[0].Risk = "Q2"; return m }, "invariant-risk-invalid"},
		{"missing transition", func(m Matrix) Matrix { m.Invariants[0].Transition = "  "; return m }, "invariant-field-missing"},
		{"missing valid", func(m Matrix) Matrix { m.Invariants[0].Tests.Valid = ""; return m }, "invariant-test-missing"},
		{"missing invalid", func(m Matrix) Matrix { m.Invariants[0].Tests.Invalid = ""; return m }, "invariant-test-missing"},
		{"persisted without limit", func(m Matrix) Matrix { m.Invariants[0].Tests.Limit = ""; return m }, "invariant-persisted-incomplete"},
		{"persisted without replay", func(m Matrix) Matrix { m.Invariants[0].Tests.Replay = ""; return m }, "invariant-persisted-incomplete"},
		{"bad version", func(m Matrix) Matrix { m.SchemaVersion = 99; return m }, "matrix-version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := tc.mut(fixtureMatrix())
			_, violations := CheckDocument(encodeMatrix(t, mutated))
			if len(violations) == 0 {
				t.Fatalf("defect %s was accepted", tc.name)
			}
			found := false
			for _, v := range violations {
				if v.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("defect %s refused with %v, want code %s", tc.name, violations, tc.code)
			}
		})
	}
}

func TestUnresolvedReferenceIsRefused(t *testing.T) {
	matrix := fixtureMatrix()
	matrix.Invariants[0].Tests.Valid = "internal/fixture/service/rules_test.go::TestNobodyWrote"
	root := writeFixtureTree(t, matrix)
	loaded, violations := ReadMatrix(root, InvariantsPath)
	if len(violations) > 0 {
		t.Fatalf("the shape check refused the fixture before the tree check: %v", violations)
	}
	if violations := Check(root, loaded); len(violations) == 0 {
		t.Fatalf("an unresolved test reference was accepted")
	} else {
		found := false
		for _, v := range violations {
			if v.Code == "invariant-test-unresolved" {
				found = true
			}
		}
		if !found {
			t.Fatalf("unresolved reference refused with %v, want invariant-test-unresolved", violations)
		}
	}
}

func TestMissingCoverageFails(t *testing.T) {
	matrix := fixtureMatrix()
	matrix.Invariants = matrix.Invariants[:1]
	root := writeFixtureTree(t, matrix)
	loaded, violations := ReadMatrix(root, InvariantsPath)
	if len(violations) > 0 {
		t.Fatalf("the shape check refused the trimmed fixture: %v", violations)
	}
	violations = Check(root, loaded)
	if len(violations) == 0 {
		t.Fatalf("a matrix missing a catalog rule was accepted")
	}
	found := false
	for _, v := range violations {
		if v.Code == "invariants-coverage-missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing coverage refused with %v, want invariants-coverage-missing", violations)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, InvariantsPath)); err != nil {
		t.Fatalf("the delivered matrix is not above %s: %v", root, err)
	}
	return root
}

func TestDeliveredMatrixStands(t *testing.T) {
	root := repoRoot(t)
	matrix, violations := ReadMatrix(root, InvariantsPath)
	if len(violations) > 0 {
		t.Fatalf("the delivered matrix is refused at the shape gate: %v", violations)
	}
	if len(matrix.Invariants) == 0 {
		t.Fatalf("the delivered matrix holds zero invariants")
	}
	if violations := Check(root, matrix); len(violations) > 0 {
		t.Fatalf("the delivered matrix fails against the checkout: %v", violations)
	}
}

func TestRemovingATransitionFailsCoverage(t *testing.T) {
	root := repoRoot(t)
	matrix, violations := ReadMatrix(root, InvariantsPath)
	if len(violations) > 0 {
		t.Fatalf("the delivered matrix is refused before the mutation: %v", violations)
	}
	if len(matrix.Invariants) < 2 {
		t.Fatalf("the delivered matrix holds %d invariants, want at least 2 to prove removal fails", len(matrix.Invariants))
	}
	trimmed := matrix
	trimmed.Invariants = append([]Invariant(nil), matrix.Invariants[1:]...)
	if violations := Check(root, trimmed); len(violations) == 0 {
		t.Fatalf("removing the first transition was accepted: coverage does not fail when a row leaves")
	} else {
		found := false
		for _, v := range violations {
			if v.Code == "invariants-coverage-missing" || v.Code == "invariants-module-missing" || v.Code == "invariants-unreachable-missing" {
				found = true
			}
		}
		if !found {
			t.Fatalf("removal refused with %v, want a coverage code", violations)
		}
	}
}

func TestPersistedTransitionsCarryFourCases(t *testing.T) {
	root := repoRoot(t)
	matrix, violations := ReadMatrix(root, InvariantsPath)
	if len(violations) > 0 {
		t.Fatalf("the delivered matrix is refused: %v", violations)
	}
	persisted := 0
	for _, inv := range matrix.Invariants {
		if !inv.Persisted {
			continue
		}
		persisted++
		for _, slot := range []struct {
			name string
			ref  string
		}{
			{"valid", inv.Tests.Valid},
			{"invalid", inv.Tests.Invalid},
			{"limit", inv.Tests.Limit},
			{"replay", inv.Tests.Replay},
		} {
			if strings.TrimSpace(slot.ref) == "" {
				t.Fatalf("persisted row %s names no `%s` case", inv.ID, slot.name)
			}
		}
	}
	if persisted == 0 {
		t.Fatalf("the delivered matrix holds zero persisted transitions: a matrix without a persisted row proves no durable write")
	}
}

func TestUnreachableProofsCoverEveryRequiredModule(t *testing.T) {
	root := repoRoot(t)
	matrix, violations := ReadMatrix(root, InvariantsPath)
	if len(violations) > 0 {
		t.Fatalf("the delivered matrix is refused: %v", violations)
	}
	byModule := map[string]int{}
	for _, inv := range matrix.Invariants {
		if inv.Unreachable {
			byModule[inv.Module]++
		}
	}
	for _, mod := range RequiredModules {
		if byModule[mod] == 0 {
			t.Fatalf("module %s holds no unreachable proof", mod)
		}
	}
}

func TestTheCommandJudgesAMatrix(t *testing.T) {
	matrix := fixtureMatrix()
	root := writeFixtureTree(t, matrix)
	if code := run([]string{"-root", root}, &discard{}, &discard{}); code != exitOK {
		t.Fatalf("the command refused the complete fixture, want exit %d", exitOK)
	}
	broken := matrix
	broken.Invariants = broken.Invariants[:1]
	brokenRaw, err := json.Marshal(broken)
	if err != nil {
		t.Fatalf("the broken matrix cannot be encoded: %v", err)
	}
	path := filepath.Join(root, filepath.FromSlash(InvariantsPath))
	if err := os.WriteFile(path, brokenRaw, 0o644); err != nil {
		t.Fatalf("the broken matrix cannot be written: %v", err)
	}
	if code := run([]string{"-root", root}, &discard{}, &discard{}); code != exitViolation {
		t.Fatalf("the command accepted a matrix missing coverage, want exit %d", exitViolation)
	}
}

type discard struct{}

func (d *discard) Write(p []byte) (int, error) {
	return len(p), nil
}
