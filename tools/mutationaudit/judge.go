package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// manifestKey identifies one manifest entry the way a report mutant does:
// package, file and mutant kind. Counts travel separately so a new mutant
// of a listed kind is a refusal instead of a silent guest.
type manifestKey struct {
	Package string
	File    string
	Mutant  string
}

// audit runs the whole gate: pins, coherence, measurement and judgment.
// A runner executes the tool per package; tests inject a fake that replays
// fixture reports, so the judgment is proved without running mutants.
func audit(pins toolPins, registerPath string, runner func(args []string) error) []string {
	register, violations := ReadRegister(".", registerPath)
	if len(violations) > 0 {
		return violations
	}
	violations = append(violations, checkPins(register, pins)...)
	violations = append(violations, checkCoherence(".", register)...)
	if len(violations) > 0 {
		return violations
	}
	reports, measurement := measure(".", register, runner)
	violations = append(violations, measurement...)
	if len(violations) > 0 {
		return violations
	}
	return judgeReports(register, reports)
}

// checkPins holds the Makefile flags to the register: a pin written twice
// is a pin that drifts. The `go run module@version` form needs no runtime
// banner check — it resolves and builds exactly the pinned version, or it
// refuses to run at all.
func checkPins(register Register, pins toolPins) []string {
	var violations []string
	fail := func(rule, message string) {
		violations = append(violations, rule+": "+message)
	}
	if pins.module != register.Tool.Module {
		fail("tool-pin-mismatch", fmt.Sprintf("Makefile module %q != register %q", pins.module, register.Tool.Module))
	}
	if pins.version != register.Tool.Version {
		fail("tool-pin-mismatch", fmt.Sprintf("Makefile version %q != register %q", pins.version, register.Tool.Version))
	}
	if pins.timeoutCoefficient != register.Tool.TimeoutCoefficient {
		fail("tool-pin-mismatch", fmt.Sprintf("Makefile timeout coefficient %d != register %d", pins.timeoutCoefficient, register.Tool.TimeoutCoefficient))
	}
	if pins.workers != register.Tool.Workers {
		fail("tool-pin-mismatch", fmt.Sprintf("Makefile workers %d != register %d", pins.workers, register.Tool.Workers))
	}
	return violations
}

// catalogRisks reads the rule catalog into module-to-risk sets, so target
// risks are judged against the catalog instead of asserted beside it.
func catalogRisks(root string) (map[string]map[string]bool, []string) {
	risks := map[string]map[string]bool{}
	raw, err := os.ReadFile(root + "/" + CatalogPath)
	if err != nil {
		return risks, []string{fmt.Sprintf("catalog-unreadable: %s: %v", CatalogPath, err)}
	}
	var catalog struct {
		Rules []struct {
			Modules []string `json:"modules"`
			Risk    string   `json:"risk"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return risks, []string{fmt.Sprintf("catalog-malformed: %s: %v", CatalogPath, err)}
	}
	for _, rule := range catalog.Rules {
		for _, module := range rule.Modules {
			if risks[module] == nil {
				risks[module] = map[string]bool{}
			}
			risks[module][rule.Risk] = true
		}
	}
	return risks, nil
}

// packageModule resolves internal/<module>/<kind> to internal/<module>.
func packageModule(packageName string) string {
	parts := strings.Split(packageName, "/")
	if len(parts) != 3 || parts[0] != "internal" || (parts[2] != "domain" && parts[2] != "application") {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// hasGoFiles reports whether the directory carries delivered Go sources.
func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			return true
		}
	}
	return false
}

// checkCoherence judges everything decidable without running mutants:
// targets exist with catalog-backed risks, zero paths exist, exclusions
// hold generated-or-boilerplate files, and no covered package is left out
// of both lists.
func checkCoherence(root string, register Register) []string {
	var violations []string
	fail := func(rule, message string) {
		violations = append(violations, rule+": "+message)
	}
	risks, catalogViolations := catalogRisks(root)
	for _, violation := range catalogViolations {
		fail(strings.SplitN(violation, ": ", 2)[0], strings.SplitN(violation, ": ", 2)[1])
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
	}
	for _, area := range register.ZeroAreas {
		for _, path := range area.Paths {
			full := root + "/" + path
			info, err := os.Stat(full)
			if err != nil {
				fail("invalid-zero-path", fmt.Sprintf("%s names missing %q", area.Area, path))
				continue
			}
			if !info.IsDir() && !strings.HasSuffix(path, ".go") {
				fail("invalid-zero-path", fmt.Sprintf("%s names %q, neither a package nor a Go file", area.Area, path))
			}
			if !info.IsDir() {
				packageName := packageModule(strings.TrimSuffix(path, "/"+filepath.Base(path)))
				if packageName == "" {
					fail("invalid-zero-path", fmt.Sprintf("%s names %q outside internal/<module>/{domain,application}", area.Area, path))
				}
			}
		}
	}
	for _, exclusion := range register.Exclusions {
		full := root + "/" + exclusion.Path
		if _, err := os.Stat(full); err != nil {
			fail("invalid-exclusion", fmt.Sprintf("excluded %q does not exist", exclusion.Path))
		}
	}
	for _, module := range phaseModules {
		for _, kind := range []string{"domain", "application"} {
			packageName := "internal/" + module + "/" + kind
			if !hasGoFiles(root + "/" + packageName) {
				continue
			}
			if !covered[packageName] {
				fail("unknown-target", fmt.Sprintf("%q is neither measured nor deferred", packageName))
			}
		}
	}
	return violations
}

// operatorArgs renders the pinned operator set as gremlins flags: every
// known operator is passed explicitly, enabled or not, so a tool upgrade
// that flips a default cannot silently change the gate.
func operatorArgs(register Register) ([]string, []string) {
	enabled := map[string]bool{}
	for _, name := range register.Operators.Enabled {
		enabled[name] = true
	}
	names := make([]string, 0, len(operatorFlag))
	for name := range operatorFlag {
		names = append(names, name)
	}
	sort.Strings(names)
	var args []string
	for _, name := range names {
		if enabled[name] {
			args = append(args, operatorFlag[name])
		} else {
			args = append(args, operatorFlag[name]+"=false")
		}
	}
	var violations []string
	for _, name := range register.Operators.Enabled {
		if _, known := operatorFlag[name]; !known {
			violations = append(violations, fmt.Sprintf("unknown-operator: %q is not a known mutant type", name))
		}
	}
	return args, violations
}

// measure executes the tool once per measured target and decodes every
// report. A target that yields no mutants at all is a refusal: an empty
// measurement is the shape of a gate that stopped working.
func measure(root string, register Register, runner func(args []string) error) ([]PackageReport, []string) {
	var reports []PackageReport
	var violations []string
	_ = root
	for _, target := range register.Targets {
		if target.Status != "measured" {
			continue
		}
		report, reportViolations := runTarget(register, target, runner)
		violations = append(violations, reportViolations...)
		if len(reportViolations) > 0 {
			return reports, violations
		}
		if len(report.Mutants) == 0 {
			violations = append(violations, fmt.Sprintf("empty-measurement: %s produced no mutants", target.Package))
			return reports, violations
		}
		reports = append(reports, report)
	}
	return reports, violations
}

// runTarget executes the tool once for a package, decoding its report. A
// report carrying timeouts runs once more and the fresh report wins: a
// transient scheduling flake resolves, while a genuine hang reproduces and
// is judged as one. The retry never masks a lived or killed verdict — it
// only gives timeouts a second chance to resolve into one.
func runTarget(register Register, target Target, runner func(args []string) error) (PackageReport, []string) {
	report, violations := runOnce(register, target, runner)
	if len(violations) > 0 {
		return report, violations
	}
	for _, mutant := range report.Mutants {
		if mutant.Status == statusTimedOut {
			return runOnce(register, target, runner)
		}
	}
	return report, nil
}

func runOnce(register Register, target Target, runner func(args []string) error) (PackageReport, []string) {
	output, err := os.CreateTemp("", "mutations-*.json")
	if err != nil {
		return PackageReport{}, []string{fmt.Sprintf("report-unwritable: %v", err)}
	}
	outputPath := output.Name()
	output.Close()
	defer os.Remove(outputPath)
	args := []string{"run", register.Tool.Module + "@" + register.Tool.Version,
		"unleash", "./" + target.Package + "/",
		"--timeout-coefficient", fmt.Sprint(register.Tool.TimeoutCoefficient),
		"--workers", fmt.Sprint(register.Tool.Workers),
	}
	operatorFlags, operatorViolations := operatorArgs(register)
	if len(operatorViolations) > 0 {
		return PackageReport{}, operatorViolations
	}
	args = append(args, operatorFlags...)
	args = append(args, "-o", outputPath)
	if err := runner(args); err != nil {
		return PackageReport{}, []string{fmt.Sprintf("measurement-failed: %s: %v", target.Package, err)}
	}
	return ReadReport(outputPath, target.Package)
}

// areaOf resolves a mutant location to its critical area, if any: package
// paths cover every file beneath them, file paths cover exactly one.
func areaOf(register Register, packageName, file string) string {
	for _, area := range register.ZeroAreas {
		for _, path := range area.Paths {
			if path == packageName || path == packageName+"/"+file {
				return area.Area
			}
		}
	}
	return ""
}

// judgeReports enforces thresholds, the zero areas and the manifest over
// decoded reports. It is pure: the same reports always judge the same.
func judgeReports(register Register, reports []PackageReport) []string {
	var violations []string
	fail := func(rule, message string) {
		violations = append(violations, rule+": "+message)
	}
	// Manifest matching first: every lived mutant needs an equivalent
	// entry and every timed-out mutant a hangs entry, with exact counts.
	// A count that drifted is a refusal either way: a new survivor is not
	// a guest, and a proof without a mutant has lapsed.
	matched := manifestIndex(register)
	violations = append(violations, matchSurvivors(register, reports, matched, fail)...)
	if len(violations) > 0 {
		return violations
	}
	// Risk thresholds over measured targets: equivalents stay in the
	// denominator, listed loudly instead of counted silently.
	violations = append(violations, judgeThresholds(register, reports, fail)...)
	return violations
}

// manifestMatch is one manifest entry with its quota and current tally.
type manifestMatch struct {
	wantClass string
	want      int
	got       int
}

func manifestIndex(register Register) map[manifestKey]*manifestMatch {
	matched := map[manifestKey]*manifestMatch{}
	for _, entry := range register.Equivalents {
		key := manifestKey{Package: entry.Package, File: entry.File, Mutant: entry.Mutant}
		wantClass := "LIVED"
		if entry.Class == "hangs" {
			wantClass = "TIMED OUT"
		}
		matched[key] = &manifestMatch{wantClass: wantClass, want: entry.Count}
	}
	return matched
}

// matchSurvivors attributes every lived and timed-out mutant to the
// manifest, naming the critical area for unlisted ones, and checks every
// quota exactly.
func matchSurvivors(register Register, reports []PackageReport, matched map[manifestKey]*manifestMatch, fail func(rule, message string)) []string {
	var violations []string
	for _, report := range reports {
		for _, mutant := range report.Mutants {
			if mutant.Status != statusLived && mutant.Status != statusTimedOut {
				continue
			}
			key := manifestKey{Package: report.Package, File: mutant.File, Mutant: mutant.Type}
			entry, ok := matched[key]
			if !ok {
				attributeUnlisted(register, report.Package, mutant, fail)
				continue
			}
			if mutant.Status != entry.wantClass {
				fail("misclassified-manifest", fmt.Sprintf("%s %s %s is %s, want %s", report.Package, mutant.File, mutant.Type, mutant.Status, entry.wantClass))
				continue
			}
			entry.got++
		}
	}
	for key, entry := range matched {
		if entry.got != entry.want {
			fail("survivor-count-drift", fmt.Sprintf("%s %s %s matches %d mutants, want exactly %d", key.Package, key.File, key.Mutant, entry.got, entry.want))
		}
	}
	return violations
}

// attributeUnlisted names an unmatched mutant: the critical area when the
// path is listed there, a plain unlisted finding otherwise.
func attributeUnlisted(register Register, packageName string, mutant Mutant, fail func(rule, message string)) {
	area := areaOf(register, packageName, mutant.File)
	if area == "" {
		if mutant.Status == statusLived {
			fail("unlisted-survivor", fmt.Sprintf("%s %s %s:%d lives without a manifest entry", packageName, mutant.Type, mutant.File, mutant.Line))
		} else {
			fail("unlisted-timeout", fmt.Sprintf("%s %s %s:%d timed out without a hangs entry", packageName, mutant.Type, mutant.File, mutant.Line))
		}
		return
	}
	if mutant.Status == statusLived {
		fail("zero-survivor-"+area, fmt.Sprintf("%s %s %s:%d lives in a zero area without a manifest entry", packageName, mutant.Type, mutant.File, mutant.Line))
	} else {
		fail("zero-timeout-"+area, fmt.Sprintf("%s %s %s:%d timed out in a zero area without a hangs entry", packageName, mutant.Type, mutant.File, mutant.Line))
	}
}

// judgeThresholds enforces the risk bars over measured targets.
func judgeThresholds(register Register, reports []PackageReport, fail func(rule, message string)) []string {
	var violations []string
	riskKilled := map[string]int{}
	riskLived := map[string]int{}
	targetRisk := map[string][]string{}
	for _, target := range register.Targets {
		if target.Status == "measured" {
			targetRisk[target.Package] = target.Risk
		}
	}
	for _, report := range reports {
		risks, ok := targetRisk[report.Package]
		if !ok {
			continue
		}
		lived := 0
		for _, mutant := range report.Mutants {
			if mutant.Status == statusLived {
				lived++
			}
		}
		for _, risk := range risks {
			riskKilled[risk] += report.Killed
			riskLived[risk] += lived
		}
	}
	for _, risk := range []string{"Q0", "Q1"} {
		threshold, ok := register.Thresholds[risk]
		if !ok {
			continue
		}
		denominator := riskKilled[risk] + riskLived[risk]
		if denominator == 0 {
			fail("empty-risk", fmt.Sprintf("no measured mutants carry %s", risk))
			continue
		}
		efficacy := float64(riskKilled[risk]) * 100 / float64(denominator)
		if efficacy < threshold {
			fail(fmt.Sprintf("threshold-breach-%s", risk), fmt.Sprintf("efficacy %.2f%% under %v%% (%d killed, %d lived)", efficacy, threshold, riskKilled[risk], riskLived[risk]))
		}
	}
	return violations
}
