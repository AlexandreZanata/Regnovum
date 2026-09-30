package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// phaseModules are the business modules the phase judges: every
// domain/application package under one of them carries a floor, never
// silently out of scope. The population matches tools/mutationaudit;
// the signal differs (executed lines versus killed faults).
var phaseModules = []string{
	"identity", "profiles", "wallet", "billing",
	"arenas", "positions", "arguments", "persuasion",
	"moderation", "transparency", "jobs",
}

// riskNames lists the risks the gate understands. The numbers live in the
// register; this table only names the vocabulary.
var riskNames = []string{"Q0", "Q1", "Q2"}

// Register is the decoded coverage policy.
type Register struct {
	SchemaVersion int                `json:"schema_version"`
	Thresholds    map[string]float64 `json:"thresholds"`
	Targets       []Target           `json:"targets"`
	Allowlist     []Allowance        `json:"allowlist"`
}

// Target is one domain/application package with its ratchet floor.
type Target struct {
	Package string   `json:"package"`
	Risk    []string `json:"risk"`
	Floor   float64  `json:"floor"`
	Status  string   `json:"status"`
	Reason  string   `json:"reason"`
	Gate    string   `json:"gate"`
}

// Allowance keeps a generated output out of the measurement, with the
// justification on record. The same record would hold a truly uncoverable
// defensive line; the twelve unreachable proofs of the phase are all met
// by refusal tests, so this tree lists generated paths only.
type Allowance struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Owner  string `json:"owner"`
	Reason string `json:"reason"`
}

// ReadRegister loads and structurally checks the register. Document errors
// stay here; every check against the checkout lives in the judge.
func ReadRegister(root, registerPath string) (Register, []string) {
	var register Register
	data, readErr := os.ReadFile(root + "/" + registerPath)
	if readErr != nil {
		return register, []string{"register-unreadable: " + registerPath + ": " + readErr.Error()}
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if decodeErr := dec.Decode(&register); decodeErr != nil {
		return register, []string{"register-malformed: " + registerPath + ": " + decodeErr.Error()}
	}
	return register, validateRegister(register)
}

func validateRegister(register Register) []string {
	var violations []string
	deny := func(rule, message string) {
		violations = append(violations, rule+": "+message)
	}
	if register.SchemaVersion != 1 {
		deny("unsupported-register", fmt.Sprintf("schema_version holds %d, want 1", register.SchemaVersion))
	}
	validateThresholds(register.Thresholds, deny)
	validateTargets(register.Targets, deny)
	validateAllowlist(register.Allowlist, deny)
	return violations
}

func validateThresholds(thresholds map[string]float64, deny func(rule, message string)) {
	for _, risk := range []string{"Q0", "Q1", "Q2", "global", "diff"} {
		threshold, ok := thresholds[risk]
		if !ok {
			deny("missing-threshold", "threshold for "+risk+" is absent")
			continue
		}
		if threshold < 0 || threshold > 100 {
			deny("absurd-threshold", fmt.Sprintf("threshold for %s holds %v, want 0-100", risk, threshold))
		}
	}
}

func validateTargets(targets []Target, deny func(rule, message string)) {
	seen := map[string]bool{}
	for _, target := range targets {
		if seen[target.Package] {
			deny("duplicate-target", fmt.Sprintf("%q appears twice", target.Package))
		}
		seen[target.Package] = true
		if target.Status != "measured" && target.Status != "deferred" {
			deny("unknown-status", fmt.Sprintf("status of %q holds %q, want measured or deferred", target.Package, target.Status))
		}
		if len(target.Risk) == 0 {
			deny("unrated-target", fmt.Sprintf("%q holds no risk class", target.Package))
		}
		for _, risk := range target.Risk {
			if risk != "Q0" && risk != "Q1" && risk != "Q2" {
				deny("unknown-risk", fmt.Sprintf("risk of %q holds %q, want Q0, Q1 or Q2", target.Package, risk))
			}
		}
		if target.Floor < 0 || target.Floor > 100 {
			deny("absurd-floor", fmt.Sprintf("floor of %q holds %v, want 0-100", target.Package, target.Floor))
		}
		if target.Status == "deferred" && (strings.TrimSpace(target.Reason) == "" || strings.TrimSpace(target.Gate) == "") {
			deny("unjustified-deferral", fmt.Sprintf("%q stays deferred without reason and gate", target.Package))
		}
	}
}

func validateAllowlist(allowlist []Allowance, deny func(rule, message string)) {
	for _, entry := range allowlist {
		if entry.Kind != "generated" && entry.Kind != "unreachable" {
			deny("unknown-allowance", fmt.Sprintf("kind of %q holds %q, want generated or unreachable", entry.Path, entry.Kind))
		}
		if strings.TrimSpace(entry.Reason) == "" || strings.TrimSpace(entry.Owner) == "" {
			deny("unjustified-allowance", fmt.Sprintf("%q holds no reason and owner", entry.Path))
		}
	}
}
