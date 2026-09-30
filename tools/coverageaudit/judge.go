package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// coverEpsilon absorbs the one-decimal rounding of `go test -cover` (the
// floors are recorded to one decimal, the exact covered/total quotient may
// sit up to half a tenth below it) without opening a ratchet gap: a real
// drop of a tenth still fails.
const coverEpsilon = 0.051

// audit runs the whole gate: document, coherence, measurement and judgment.
// Runners execute the toolchain and git per package; tests inject fakes
// that replay fixtures, so the judgment is proved without running the
// suite.
func audit(registerPath string, runner packageRunner, git gitRunner) []string {
	register, violations := ReadRegister(".", registerPath)
	if len(violations) > 0 {
		return violations
	}
	violations = append(violations, checkCoherence(".", register)...)
	if len(violations) > 0 {
		return violations
	}
	covers, measurement := measure(register, runner)
	if len(measurement) > 0 {
		return measurement
	}
	return judgeReports(register, covers, git)
}

// checkCoherence judges everything decidable without running tests:
// targets exist with catalog-backed risks, the allowlist names existing
// paths, and no covered package is left out of the register.
func checkCoherence(root string, register Register) []string {
	var violations []string
	fail := func(rule, message string) {
		violations = append(violations, rule+": "+message)
	}
	risks, catalogViolations := catalogRisks(root)
	for _, violation := range catalogViolations {
		parts := strings.SplitN(violation, ": ", 2)
		if len(parts) == 2 {
			fail(parts[0], parts[1])
		} else {
			fail("catalog-unreadable", violation)
		}
	}
	covered := map[string]bool{}
	for _, target := range register.Targets {
		module := packageModule(target.Package)
		if module == "" {
			fail("malformed-target", fmt.Sprintf("%q is not internal/<module>/{domain,application}", target.Package))
			continue
		}
		if !hasGoFiles(root + "/" + target.Package) {
			fail("missing-target", fmt.Sprintf("%q names no delivered Go sources", target.Package))
			continue
		}
		covered[target.Package] = true
		for _, risk := range target.Risk {
			if !risks[module][risk] {
				fail("invalid-risk", fmt.Sprintf("%q claims %s with no such catalog rule for %s", target.Package, risk, module))
			}
		}
		peak := maxThreshold(target.Risk, register.Thresholds)
		if target.Status == "measured" && target.Floor+coverEpsilon < peak {
			fail("floor-below-threshold", fmt.Sprintf("%q floor %.1f under its %s %.0f: debt must be deferred with reason and gate", target.Package, target.Floor, strings.Join(target.Risk, "/"), peak))
		}
	}
	for _, entry := range register.Allowlist {
		if _, err := os.Stat(root + "/" + entry.Path); err != nil {
			fail("invalid-allowance", fmt.Sprintf("allowlisted %q does not exist", entry.Path))
		}
	}
	for _, module := range phaseModules {
		for _, kind := range []string{"domain", "application"} {
			packageName := "internal/" + module + "/" + kind
			if !hasGoFiles(root + "/" + packageName) {
				continue
			}
			if !covered[packageName] {
				fail("unknown-target", fmt.Sprintf("%q is neither floored nor deferred", packageName))
			}
		}
	}
	return violations
}

// judgeReports enforces floors, the risk, global and diff thresholds, and
// prints the mutation thresholds beside the coverage numbers so the report
// proves the two gates are independent. It is pure given the measurements
// except for the diff, which reads git.
func judgeReports(register Register, covers map[string]packageCover, git gitRunner) []string {
	var violations []string
	fail := func(rule, message string) {
		violations = append(violations, rule+": "+message)
	}
	// Per-package ratchet: lowering a floor or losing coverage fails, and
	// omitting a package already failed in coherence.
	for _, target := range register.Targets {
		cover, ok := covers[target.Package]
		if !ok {
			fail("missing-measurement", fmt.Sprintf("%q was not measured", target.Package))
			continue
		}
		if cover.percent+coverEpsilon < target.Floor {
			fail("floor-breach", fmt.Sprintf("%s %.1f%% under floor %.1f", target.Package, cover.percent, target.Floor))
		}
	}
	if len(violations) > 0 {
		return violations
	}
	// Risk thresholds over measured (non-debt) packages only: debt is
	// explicit with a P45 gate and still ratcheted above, but it does not
	// dilute the bar the measured population owes.
	judgeRiskThresholds(register, covers, fail)
	if len(violations) > 0 {
		return violations
	}
	// Global threshold over the whole population, allowlist subtracted.
	judgeGlobalThreshold(register, covers, fail)
	if len(violations) > 0 {
		return violations
	}
	// Diff threshold over added statement lines in scope.
	covered, total, diffViolations := diffCoverage(register, covers, git)
	violations = append(violations, diffViolations...)
	if len(violations) > 0 {
		return violations
	}
	threshold := register.Thresholds["diff"]
	if total == 0 {
		return violations
	}
	efficacy := float64(covered) * 100 / float64(total)
	if efficacy+coverEpsilon < threshold {
		fail("diff-breach", fmt.Sprintf("diff coverage %.1f%% under %.0f%% (%d of %d added statement lines covered)", efficacy, threshold, covered, total))
	}
	return violations
}

// judgeRiskThresholds enforces Q0/Q1/Q2 over the measured population.
func judgeRiskThresholds(register Register, covers map[string]packageCover, fail func(rule, message string)) {
	measured := map[string]bool{}
	for _, target := range register.Targets {
		if target.Status == "measured" {
			measured[target.Package] = true
		}
	}
	for _, risk := range riskNames {
		threshold, ok := register.Thresholds[risk]
		if !ok {
			continue
		}
		total := 0
		hit := 0
		for pkg, cover := range covers {
			if !measured[pkg] {
				continue
			}
			target := targetOf(register, pkg)
			if target == nil || !containsRisk(target.Risk, risk) {
				continue
			}
			total += cover.total
			hit += cover.covered
		}
		if total == 0 {
			fail("empty-risk", fmt.Sprintf("no measured package carries %s", risk))
			continue
		}
		efficacy := float64(hit) * 100 / float64(total)
		if efficacy+coverEpsilon < threshold {
			fail(fmt.Sprintf("threshold-breach-%s", risk), fmt.Sprintf("%s %.1f%% under %.0f%% over measured (%d of %d statements)", risk, efficacy, threshold, hit, total))
		}
	}
}

// judgeGlobalThreshold enforces the global floor over every target.
func judgeGlobalThreshold(register Register, covers map[string]packageCover, fail func(rule, message string)) {
	threshold, ok := register.Thresholds["global"]
	if !ok {
		return
	}
	total := 0
	hit := 0
	for _, cover := range covers {
		total += cover.total
		hit += cover.covered
	}
	if total == 0 {
		fail("empty-measurement", "no statements measured")
		return
	}
	efficacy := float64(hit) * 100 / float64(total)
	if efficacy+coverEpsilon < threshold {
		fail("threshold-breach-global", fmt.Sprintf("global %.1f%% under %.0f%% (%d of %d statements)", efficacy, threshold, hit, total))
	}
}

// targetOf resolves a package to its register entry.
func targetOf(register Register, pkg string) *Target {
	for i := range register.Targets {
		if register.Targets[i].Package == pkg {
			return &register.Targets[i]
		}
	}
	return nil
}

// containsRisk reports whether a risk list holds a value.
func containsRisk(risks []string, value string) bool {
	for _, risk := range risks {
		if risk == value {
			return true
		}
	}
	return false
}

// mutationThresholds reads the mutation policy thresholds for the report:
// the coverage report names them to prove the gates are independent (line
// execution versus fault detection), never to judge them.
func mutationThresholds(root string) map[string]float64 {
	raw, err := os.ReadFile(root + "/" + MutationsPath)
	if err != nil {
		return map[string]float64{}
	}
	var decoded struct {
		Thresholds map[string]float64 `json:"thresholds"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]float64{}
	}
	return decoded.Thresholds
}
