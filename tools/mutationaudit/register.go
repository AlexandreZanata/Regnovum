package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// toolPins are the Makefile-declared pins the gate holds the machine to. A
// pin written twice is a pin that drifts, so the Makefile owns them and the
// register repeats them for the judge to compare.
type toolPins struct {
	module             string
	version            string
	timeoutCoefficient int
	workers            int
}

// operatorFlag maps every known gremlins mutant type to its CLI flag. An
// operator the table does not know is refused instead of passed through:
// the gate pins the vocabulary, not just the values.
var operatorFlag = map[string]string{
	"ARITHMETIC_BASE":         "--arithmetic-base",
	"CONDITIONALS_BOUNDARY":   "--conditionals-boundary",
	"CONDITIONALS_NEGATION":   "--conditionals-negation",
	"INCREMENT_DECREMENT":     "--increment-decrement",
	"INVERT_NEGATIVES":        "--invert-negatives",
	"INVERT_ASSIGNMENTS":      "--invert-assignments",
	"INVERT_BITWISE":          "--invert-bitwise",
	"INVERT_BWASSIGN":         "--invert-bwassign",
	"INVERT_LOGICAL":          "--invert-logical",
	"INVERT_LOOPCTRL":         "--invert-loopctrl",
	"REMOVE_SELF_ASSIGNMENTS": "--remove-self-assignments",
}

// phaseModules are the business modules the phase judges: every
// domain/application package under one of them is measured or explicitly
// deferred, never silently out of scope.
var phaseModules = []string{
	"identity", "profiles", "wallet", "billing",
	"arenas", "positions", "arguments", "persuasion",
	"moderation", "transparency", "jobs",
}

// Register is the decoded mutation policy.
type Register struct {
	SchemaVersion int                `json:"schema_version"`
	Tool          RegisterTool       `json:"tool"`
	Operators     Operators          `json:"operators"`
	Thresholds    map[string]float64 `json:"thresholds"`
	Targets       []Target           `json:"targets"`
	ZeroAreas     []ZeroArea         `json:"zero_areas"`
	Equivalents   []ManifestEntry    `json:"equivalents"`
	Exclusions    []Exclusion        `json:"exclusions"`
}

// RegisterTool is the pinned tool and its execution shape.
type RegisterTool struct {
	Module             string `json:"module"`
	Version            string `json:"version"`
	TimeoutCoefficient int    `json:"timeout_coefficient"`
	Workers            int    `json:"workers"`
}

// Operators names the enabled mutant types and justifies every deferred one.
type Operators struct {
	Enabled  []string          `json:"enabled"`
	Deferred map[string]string `json:"deferred"`
}

// Target is one Q0/Q1 package: measured now or deferred with a reason.
type Target struct {
	Package string   `json:"package"`
	Risk    []string `json:"risk"`
	Areas   []string `json:"areas"`
	Status  string   `json:"status"`
	Reason  string   `json:"reason"`
	Gate    string   `json:"gate"`
}

// ZeroArea names one critical area and the paths where nothing unlisted
// may survive.
type ZeroArea struct {
	Area  string   `json:"area"`
	Paths []string `json:"paths"`
}

// ManifestEntry is one proven equivalent or hang: the exact mutant class
// it covers, how many report mutants it accounts for, why, and who answers
// for it.
type ManifestEntry struct {
	Package string `json:"package"`
	File    string `json:"file"`
	Mutant  string `json:"mutant"`
	Class   string `json:"class"`
	Count   int    `json:"count"`
	Reason  string `json:"reason"`
	Owner   string `json:"owner"`
}

// Exclusion removes generated or boilerplate paths from the measurement,
// and only those, with the reason on record.
type Exclusion struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Owner  string `json:"owner"`
}

// ReadRegister decodes and structurally validates the register. It judges
// the document, not the tree: every cross-check against the checkout lives
// in the judge.
func ReadRegister(root, registerPath string) (Register, []string) {
	var register Register
	raw, err := os.ReadFile(root + "/" + registerPath)
	if err != nil {
		return register, []string{fmt.Sprintf("register-unreadable: %s: %v", registerPath, err)}
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&register); err != nil {
		return register, []string{fmt.Sprintf("register-malformed: %s: %v", registerPath, err)}
	}
	return register, validateRegister(register)
}

func validateRegister(register Register) []string {
	var violations []string
	fail := func(rule, message string) {
		violations = append(violations, rule+": "+message)
	}
	if register.SchemaVersion != 1 {
		fail("unsupported-register", fmt.Sprintf("schema_version = %d, want 1", register.SchemaVersion))
	}
	validateTool(register.Tool, fail)
	validateOperators(register.Operators, fail)
	validateThresholds(register.Thresholds, fail)
	validateTargets(register.Targets, fail)
	validateZeroAreas(register.ZeroAreas, fail)
	validateManifest(register.Equivalents, fail)
	validateExclusions(register.Exclusions, fail)
	return violations
}

func validateTool(tool RegisterTool, fail func(rule, message string)) {
	if strings.TrimSpace(tool.Module) == "" || strings.TrimSpace(tool.Version) == "" {
		fail("incomplete-tool", "the tool needs its module and version pins")
	}
	if tool.TimeoutCoefficient <= 0 || tool.Workers <= 0 {
		fail("incomplete-tool", "the tool needs a positive timeout coefficient and worker count")
	}
}

func validateOperators(operators Operators, fail func(rule, message string)) {
	seenOperators := map[string]bool{}
	if len(operators.Enabled) == 0 {
		fail("no-operators", "at least one mutant type must be enabled")
	}
	for _, name := range operators.Enabled {
		if _, known := operatorFlag[name]; !known {
			fail("unknown-operator", fmt.Sprintf("%q is not a known mutant type", name))
		}
		if seenOperators[name] {
			fail("duplicate-operator", fmt.Sprintf("%q is enabled twice", name))
		}
		seenOperators[name] = true
	}
	for name, reason := range operators.Deferred {
		if _, known := operatorFlag[name]; !known {
			fail("unknown-operator", fmt.Sprintf("deferred %q is not a known mutant type", name))
		}
		if seenOperators[name] {
			fail("contradictory-operator", fmt.Sprintf("%q is both enabled and deferred", name))
		}
		if strings.TrimSpace(reason) == "" {
			fail("unjustified-operator", fmt.Sprintf("deferred %q carries no reason", name))
		}
	}
}

func validateThresholds(thresholds map[string]float64, fail func(rule, message string)) {
	for _, risk := range []string{"Q0", "Q1"} {
		threshold, ok := thresholds[risk]
		if !ok {
			fail("missing-threshold", fmt.Sprintf("no threshold for %s", risk))
			continue
		}
		if threshold < 0 || threshold > 100 {
			fail("absurd-threshold", fmt.Sprintf("%s threshold = %v, want 0-100", risk, threshold))
		}
	}
}

func validateTargets(targets []Target, fail func(rule, message string)) {
	seenTargets := map[string]bool{}
	for _, target := range targets {
		if seenTargets[target.Package] {
			fail("duplicate-target", fmt.Sprintf("%q is listed twice", target.Package))
		}
		seenTargets[target.Package] = true
		if target.Status != "measured" && target.Status != "deferred" {
			fail("unknown-status", fmt.Sprintf("%q status = %q, want measured or deferred", target.Package, target.Status))
		}
		if len(target.Risk) == 0 {
			fail("unrated-target", fmt.Sprintf("%q carries no risk class", target.Package))
		}
		for _, risk := range target.Risk {
			if risk != "Q0" && risk != "Q1" {
				fail("unknown-risk", fmt.Sprintf("%q risk = %q, want Q0 or Q1", target.Package, risk))
			}
		}
		if target.Status == "deferred" && (strings.TrimSpace(target.Reason) == "" || strings.TrimSpace(target.Gate) == "") {
			fail("unjustified-deferral", fmt.Sprintf("%q is deferred without reason and gate", target.Package))
		}
	}
}

func validateZeroAreas(areas []ZeroArea, fail func(rule, message string)) {
	if len(areas) == 0 {
		fail("no-zero-areas", "at least one critical area must name its paths")
	}
	for _, area := range areas {
		if strings.TrimSpace(area.Area) == "" || len(area.Paths) == 0 {
			fail("empty-zero-area", "every zero area names its area and paths")
		}
	}
}

func validateManifest(entries []ManifestEntry, fail func(rule, message string)) {
	for _, entry := range entries {
		if entry.Class != "equivalent" && entry.Class != "hangs" {
			fail("unknown-manifest-class", fmt.Sprintf("%s/%s class = %q, want equivalent or hangs", entry.Package, entry.File, entry.Class))
		}
		if _, known := operatorFlag[entry.Mutant]; !known {
			fail("unknown-operator", fmt.Sprintf("manifest %s/%s names unknown mutant %q", entry.Package, entry.File, entry.Mutant))
		}
		if entry.Count <= 0 {
			fail("countless-manifest", fmt.Sprintf("%s/%s %s carries no count", entry.Package, entry.File, entry.Mutant))
		}
		if strings.TrimSpace(entry.Reason) == "" || strings.TrimSpace(entry.Owner) == "" {
			fail("unproven-manifest", fmt.Sprintf("%s/%s %s carries no reason and owner", entry.Package, entry.File, entry.Mutant))
		}
	}
}

func validateExclusions(exclusions []Exclusion, fail func(rule, message string)) {
	for _, exclusion := range exclusions {
		if exclusion.Kind != "generated" && exclusion.Kind != "boilerplate" {
			fail("unknown-exclusion", fmt.Sprintf("%q kind = %q, want generated or boilerplate", exclusion.Path, exclusion.Kind))
		}
		if strings.TrimSpace(exclusion.Reason) == "" || strings.TrimSpace(exclusion.Owner) == "" {
			fail("unjustified-exclusion", fmt.Sprintf("%q carries no reason and owner", exclusion.Path))
		}
	}
}
