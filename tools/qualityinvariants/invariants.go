package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Tests are the four cases every persisted transition must carry. Valid and
// invalid are demanded of every row; limit and replay are additionally
// demanded when persisted is true.
type Tests struct {
	Valid   string `json:"valid"`
	Invalid string `json:"invalid"`
	Limit   string `json:"limit"`
	Replay  string `json:"replay"`
}

// Invariant is one row of the matrix: the transition, what it assumes, what
// it guarantees, what always holds, and what must never happen, with the
// catalog rule it proves and the tests that prove it.
type Invariant struct {
	ID               string   `json:"id"`
	Module           string   `json:"module"`
	Packages         []string `json:"packages"`
	Risk             string   `json:"risk"`
	Transition       string   `json:"transition"`
	Precondition     string   `json:"precondition"`
	Postcondition    string   `json:"postcondition"`
	GlobalInvariant  string   `json:"global_invariant"`
	ProhibitedEffect string   `json:"prohibited_effect"`
	Catalog          string   `json:"catalog"`
	Persisted        bool     `json:"persisted"`
	Unreachable      bool     `json:"unreachable"`
	Tests            Tests    `json:"tests"`
}

// Matrix is the versioned document the loader judges.
type Matrix struct {
	SchemaVersion int         `json:"schema_version"`
	Generator     string      `json:"generator"`
	Modules       []string    `json:"modules"`
	Invariants    []Invariant `json:"invariants"`
}

// Violation is one refusal of the matrix.
type Violation struct {
	Row    string
	Code   string
	Detail string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: %s — %s", v.Row, v.Code, v.Detail)
}

// Violations is the list of refusals.
type Violations []Violation

// loadTreeFile reads one repository-relative file. Only trees inside the
// checkout resolve: absolute locations and parent escapes are refused without
// following them.
func loadTreeFile(root, rel string) ([]byte, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return nil, false
	}
	if rel != filepath.Clean(rel) {
		// The reference must already be clean: a matrix that needs dot
		// segments to name its evidence is a matrix that points outside.
		return nil, false
	}
	if strings.HasPrefix(rel, "..") {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, false
	}
	return data, true
}

func treeDirExists(root, rel string) bool {
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return false
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	return info.IsDir()
}

// ReadMatrix reads the matrix inside the tree and judges its shape. A file
// that is not there, that does not decode, or that holds zero rows verifies
// nothing, so each of those is a refusal.
func ReadMatrix(root, path string) (Matrix, Violations) {
	document, ok := loadTreeFile(root, path)
	if !ok {
		return Matrix{}, Violations{{
			Row:    "matrix",
			Code:   "matrix-missing",
			Detail: fmt.Sprintf("`%s` is not in the checkout: a matrix nobody can open verifies nothing", path),
		}}
	}
	return CheckDocument(document)
}

// CheckDocument judges the raw document without touching the tree. The split
// exists so tests can hold the shape contract without a checkout.
func CheckDocument(document []byte) (Matrix, Violations) {
	var matrix Matrix
	if err := json.Unmarshal(document, &matrix); err != nil {
		return Matrix{}, Violations{{
			Row:    "matrix",
			Code:   "matrix-invalid",
			Detail: fmt.Sprintf("the matrix does not decode as JSON: %v", err),
		}}
	}
	var violations Violations
	if matrix.SchemaVersion != SchemaVersion {
		violations = append(violations, Violation{
			Row:    "matrix",
			Code:   "matrix-version",
			Detail: fmt.Sprintf("schema_version is %d and the loader implements %d", matrix.SchemaVersion, SchemaVersion),
		})
	}
	if len(matrix.Invariants) == 0 {
		violations = append(violations, Violation{
			Row:    "matrix",
			Code:   "matrix-empty",
			Detail: "the matrix holds zero invariants: an empty matrix verifies nothing, so it is refused rather than accepted as vacuously true",
		})
		return matrix, violations
	}
	seen := map[string]bool{}
	for i, inv := range matrix.Invariants {
		row := inv.ID
		if row == "" {
			row = fmt.Sprintf("invariants[%d]", i)
		}
		if inv.ID == "" {
			violations = append(violations, Violation{Row: row, Code: "invariant-id-missing", Detail: "the row names no id: a row without an identity cannot be linked or removed on purpose"})
			continue
		}
		if seen[inv.ID] {
			violations = append(violations, Violation{Row: row, Code: "invariant-id-duplicate", Detail: fmt.Sprintf("duplicate id `%s`: two rows with one identity hide which one the tests prove", inv.ID)})
		}
		seen[inv.ID] = true
		if inv.Module == "" {
			violations = append(violations, Violation{Row: row, Code: "invariant-module-missing", Detail: "the row names no module: a transition without an owner has no test suite to hold it"})
		}
		if len(inv.Packages) == 0 {
			violations = append(violations, Violation{Row: row, Code: "invariant-packages-missing", Detail: "the row names no package: a matrix that points at no code points at nothing"})
		}
		if inv.Risk != "Q0" && inv.Risk != "Q1" {
			violations = append(violations, Violation{Row: row, Code: "invariant-risk-invalid", Detail: fmt.Sprintf("risk `%s` is not Q0 or Q1: only critical and high invariants live in this matrix", inv.Risk)})
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{"transition", inv.Transition},
			{"precondition", inv.Precondition},
			{"postcondition", inv.Postcondition},
			{"global_invariant", inv.GlobalInvariant},
			{"prohibited_effect", inv.ProhibitedEffect},
			{"catalog", inv.Catalog},
		} {
			if strings.TrimSpace(field.value) == "" {
				violations = append(violations, Violation{Row: row, Code: "invariant-field-missing", Detail: fmt.Sprintf("`%s` is empty: a matrix row with a blank column is a sentence nobody finished", field.name)})
			}
		}
		if strings.TrimSpace(inv.Tests.Valid) == "" {
			violations = append(violations, Violation{Row: row, Code: "invariant-test-missing", Detail: "the `valid` case is empty: a transition nobody proves accepted is a transition nobody enforces"})
		}
		if strings.TrimSpace(inv.Tests.Invalid) == "" {
			violations = append(violations, Violation{Row: row, Code: "invariant-test-missing", Detail: "the `invalid` case is empty: a transition nobody proves refused is a transition nobody enforces"})
		}
		if inv.Persisted {
			if strings.TrimSpace(inv.Tests.Limit) == "" {
				violations = append(violations, Violation{Row: row, Code: "invariant-persisted-incomplete", Detail: "a persisted transition names no `limit` case: a boundary nobody proves is a boundary nobody keeps"})
			}
			if strings.TrimSpace(inv.Tests.Replay) == "" {
				violations = append(violations, Violation{Row: row, Code: "invariant-persisted-incomplete", Detail: "a persisted transition names no `replay` case: a replay nobody proves is a replay that duplicates"})
			}
		}
	}
	return matrix, violations
}

// Check judges the matrix against the tree: every reference must resolve,
// every persisted row must carry four resolving cases, every Q0/Q1 catalog
// rule must be linked, every required module must appear, and every required
// module must hold an unreachable proof.
func Check(root string, matrix Matrix) Violations {
	var violations Violations
	for _, inv := range matrix.Invariants {
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
				continue
			}
			if !resolves(root, slot.ref) {
				violations = append(violations, Violation{
					Row:    inv.ID,
					Code:   "invariant-test-unresolved",
					Detail: fmt.Sprintf("the `%s` case `%s` does not resolve: a reference nobody can open is a claim about nothing", slot.name, slot.ref),
				})
			}
		}
		for _, pkg := range inv.Packages {
			if !treeDirExists(root, pkg) {
				violations = append(violations, Violation{
					Row:    inv.ID,
					Code:   "invariant-package-unknown",
					Detail: fmt.Sprintf("package `%s` is not a directory of the checkout", pkg),
				})
			}
		}
	}
	if len(violations) > 0 {
		return violations
	}
	catalog, catalogViolations := readCatalogRisks(root)
	if len(catalogViolations) > 0 {
		return append(violations, catalogViolations...)
	}
	linked := map[string]bool{}
	for _, inv := range matrix.Invariants {
		linked[inv.Catalog] = true
	}
	for id := range catalog {
		if !linked[id] {
			violations = append(violations, Violation{
				Row:    id,
				Code:   "invariants-coverage-missing",
				Detail: fmt.Sprintf("catalog rule `%s` is linked from no invariant row: removing a transition must fail coverage, so an unlinked rule fails it now", id),
			})
		}
	}
	for id := range linked {
		if _, ok := catalog[id]; !ok {
			violations = append(violations, Violation{
				Row:    id,
				Code:   "invariant-catalog-unknown",
				Detail: fmt.Sprintf("catalog rule `%s` is not a Q0/Q1 rule of the checkout", id),
			})
		}
	}
	byModule := map[string]int{}
	unreachableByModule := map[string]int{}
	for _, inv := range matrix.Invariants {
		byModule[inv.Module]++
		if inv.Unreachable {
			unreachableByModule[inv.Module]++
		}
	}
	for _, mod := range RequiredModules {
		if byModule[mod] == 0 {
			violations = append(violations, Violation{
				Row:    mod,
				Code:   "invariants-module-missing",
				Detail: fmt.Sprintf("module `%s` holds zero invariant rows: a business area the matrix does not name is a business area the matrix does not guard", mod),
			})
		}
		if unreachableByModule[mod] == 0 {
			violations = append(violations, Violation{
				Row:    mod,
				Code:   "invariants-unreachable-missing",
				Detail: fmt.Sprintf("module `%s` holds no unreachable proof: a state nobody proves refused is a state somebody will reach", mod),
			})
		}
	}
	return violations
}

func resolves(root, ref string) bool {
	parts := strings.SplitN(ref, "::", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	data, ok := loadTreeFile(root, parts[0])
	if !ok {
		return false
	}
	return bytes.Contains(data, []byte("func "+parts[1]+"("))
}

// readCatalogRisks returns the Q0/Q1 rule identities of the catalog. The
// matrix is linked to the catalog, never a copy of it: a rule the catalog
// stopped declaring must fail here, and a rule nobody links must fail as
// missing coverage.
func readCatalogRisks(root string) (map[string]bool, Violations) {
	data, ok := loadTreeFile(root, CatalogPath)
	if !ok {
		return nil, Violations{{
			Row:    "catalog",
			Code:   "catalog-missing",
			Detail: fmt.Sprintf("`%s` is not in the checkout: a matrix without its catalog has nothing to link to", CatalogPath),
		}}
	}
	var catalog struct {
		Rules []struct {
			ID   string `json:"id"`
			Risk string `json:"risk"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, Violations{{
			Row:    "catalog",
			Code:   "catalog-invalid",
			Detail: fmt.Sprintf("the catalog does not decode as JSON: %v", err),
		}}
	}
	risks := map[string]bool{}
	for _, rule := range catalog.Rules {
		if rule.Risk == "Q0" || rule.Risk == "Q1" {
			risks[rule.ID] = true
		}
	}
	if len(risks) == 0 {
		return nil, Violations{{
			Row:    "catalog",
			Code:   "catalog-empty",
			Detail: "the catalog holds zero Q0/Q1 rules: a matrix linked to nothing verifies nothing",
		}}
	}
	return risks, nil
}
