package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "staticaudit: %v\n", err)
		os.Exit(1)
	}
}

// options are the pins and the document the gate reads. Every one of them is
// passed by the Makefile, which is the single place that declares them: a pin
// written twice is a pin that drifts.
type options struct {
	root               string
	baselinePath       string
	staticcheckModule  string
	staticcheckVersion string
	toolchain          string
	printFindings      bool
}

func run(args []string) error {
	flags := flag.NewFlagSet("staticaudit", flag.ContinueOnError)
	root := flags.String("root", ".", "the repository root")
	baselinePath := flags.String("baseline", "quality/lint-baseline.json", "the versioned list of accepted findings")
	staticcheckModule := flags.String("staticcheck-module", "", "the module path of the pinned analyzer")
	staticcheckVersion := flags.String("staticcheck-version", "", "the pinned analyzer version")
	toolchain := flags.String("toolchain", "", "the Go toolchain the analyzer must be built with")
	printFindings := flags.Bool("print-findings", false, "print the grouped findings of the tree as JSON and stop (used to regenerate the baseline)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	options := options{
		root: *root, baselinePath: *baselinePath,
		staticcheckModule: *staticcheckModule, staticcheckVersion: *staticcheckVersion,
		toolchain: *toolchain, printFindings: *printFindings,
	}
	for name, value := range map[string]string{
		"-staticcheck-module":  options.staticcheckModule,
		"-staticcheck-version": options.staticcheckVersion,
		"-toolchain":           options.toolchain,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required: the gate holds the machine to the pin the tree declares, and an unpinned gate measures nothing", name)
		}
	}
	if err := os.Chdir(options.root); err != nil {
		return fmt.Errorf("enter %s: %w", options.root, err)
	}
	return audit(options)
}

func runCommand(env []string, name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	command.Env = append(os.Environ(), env...)
	var buffer bytes.Buffer
	command.Stdout = &buffer
	command.Stderr = &buffer
	err := command.Run()
	return buffer.String(), err
}

func audit(options options) error {
	// 0. The pinned analyzer answers its own version. This is the measurement
	// the pin is held to: an analyzer built with another toolchain reads
	// another language version, and a gate that runs it anyway measures a
	// different tree than the one that ships.
	version, err := analyzerVersion(options)
	if err != nil {
		return err
	}
	if version != normalizeVersion(options.staticcheckVersion) {
		return fmt.Errorf(
			"the installed analyzer answers %s and the tree pins %s — run `make lint` with the pinned version (GOTOOLCHAIN=%s go install %s@%s): an analyzer that drifts from the pin analyzes another language",
			version, options.staticcheckVersion, options.toolchain, options.staticcheckModule, options.staticcheckVersion)
	}

	// 1. The standard library's analyzers over the delivered tree.
	vetOutput, vetErr := runCommand(nil, "go", "vet", "./...")
	if vetErr != nil {
		return fmt.Errorf("go vet ./... refused the tree:\n%s", strings.TrimSpace(vetOutput))
	}
	if !options.printFindings {
		fmt.Printf("staticaudit: go vet ./... — clean (the toolchain of go.mod)\n")
	}

	// 2. The families the analyzers own, each one refused by its fixture, before
	// the tree is judged: a rule that stopped biting has to be visible as a
	// broken gate, not as a clean run.
	covered, uncovered, err := auditFamilies(options)
	if err != nil {
		return err
	}

	// 3. The pinned analyzer over the delivered tree.
	output, analyzerErr := staticcheck(options, "./...")
	findings := parseFindings(output)
	if analyzerErr != nil && len(findings) == 0 {
		return fmt.Errorf("staticcheck refused to run:\n%s", strings.TrimSpace(output))
	}
	groups := groupFindings(findings)
	if options.printFindings {
		encoded, err := json.MarshalIndent(groups, "", "  ")
		if err != nil {
			return err
		}
		fmt.Printf("%s\n", encoded)
		return nil
	}

	baseline, baselineViolations := loadBaseline(options.baselinePath)
	if len(baselineViolations) > 0 {
		return fmt.Errorf("the baseline is not readable:\n  - %s", strings.Join(baselineViolations, "\n  - "))
	}
	ratchet := baselineRefusal(groups, baseline)

	// 4. The suppressions: what the analyzers were told to skip, whether the
	// tree asked in the vocabulary the pinned analyzer reads, and whether a
	// directive it read was needed at all.
	files, err := goFiles(options.root)
	if err != nil {
		return err
	}
	suppressions, suppressionViolations := suppressionRefusal(options.root, files)
	stale := unusedDirectiveRefusal(output)

	// 5. The report. Everything the gate measured is printed, including the
	// baseline it accepted and the families nobody pins: a gap that is not
	// printed reads like coverage.
	fmt.Printf("staticaudit: staticcheck %s (GOTOOLCHAIN=%s)\n", version, options.toolchain)
	fmt.Printf("staticaudit: %d finding(s) in the tree, %d accepted by the baseline and %d entry(ies) of it\n",
		len(findings), len(findings), len(baseline.Entry))
	for _, group := range groups {
		entry := baselineEntryFor(baseline, group)
		if entry == nil {
			continue
		}
		fmt.Printf("staticaudit:   accepted %s:%d %s (%d×, owner %s) — %s\n",
			group.Path, group.Line, group.Check, group.Count, entry.Owner, entry.Reason)
	}
	for _, entry := range covered {
		fmt.Printf("staticaudit: family %s — refused by %s (%s), fixture %s\n", entry.Name, entry.Owner, entry.checkLabel(), entry.Target)
	}
	for _, suppression := range suppressions {
		fmt.Printf("staticaudit: suppression %s:%d %s — %s\n", suppression.Path, suppression.Line, suppression.Check, suppression.Reason)
	}
	for range stale {
		fmt.Printf("staticaudit: the analyzer read a suppression it never needed (see the refusal below)\n")
	}
	for _, gap := range uncovered {
		fmt.Printf("staticaudit: not enforced by the pinned toolchain — %s\n", gap)
	}

	violations := append([]string{}, ratchet...)
	violations = append(violations, suppressionViolations...)
	violations = append(violations, stale...)
	if len(violations) > 0 {
		sort.Strings(violations)
		return fmt.Errorf("the static analysis gate refused the tree:\n  - %s", strings.Join(violations, "\n  - "))
	}
	fmt.Printf("staticaudit: OK — vet and staticcheck green, the baseline did not grow\n")
	return nil
}

// checkLabel names the check a family is proven by, for the report.
func (f family) checkLabel() string {
	if f.Check != "" {
		return f.Check
	}
	if f.Match != "" {
		return "the analyzer's own message"
	}
	return "the owning test"
}

// analyzerVersion runs the pinned analyzer's own version check.
func analyzerVersion(options options) (string, error) {
	output, err := staticcheck(options, "-version")
	if err != nil {
		return "", fmt.Errorf("the pinned analyzer did not answer its version (%s@%s built with GOTOOLCHAIN=%s):\n%s",
			options.staticcheckModule, options.staticcheckVersion, options.toolchain, strings.TrimSpace(output))
	}
	match := analyzerVersionLine.FindStringSubmatch(output)
	if match == nil {
		return "", fmt.Errorf("the analyzer answered a version this gate cannot read: %q", strings.TrimSpace(output))
	}
	return match[1], nil
}

// analyzerVersionLine reads the module version the analyzer prints inside its
// prose — "staticcheck 2026.2.1 (0.8.1)" and "staticcheck 2026.1 (v0.7.0)" are
// the same shape to this gate, which pins the module and not the release name.
var analyzerVersionLine = regexp.MustCompile(`\(v?([0-9]+\.[0-9]+\.[0-9]+[^)]*)\)`)

// normalizeVersion drops the leading `v` a Go module version may carry.
func normalizeVersion(version string) string { return strings.TrimPrefix(version, "v") }

// staticcheck runs the pinned analyzer with the pinned toolchain.
func staticcheck(options options, args ...string) (string, error) {
	module := options.staticcheckModule + "@" + options.staticcheckVersion
	full := append([]string{"run", module}, args...)
	return runCommand([]string{"GOTOOLCHAIN=" + options.toolchain}, "go", full...)
}

// auditFamilies runs every fixture of every analyzer-owned family and requires
// the owner to refuse it, then verifies the proof of the families a test owns.
func auditFamilies(options options) ([]family, []string, error) {
	covered := []family{}
	for _, entry := range families {
		switch entry.Owner {
		case "vet":
			output, err := runCommand(nil, "go", "vet", "./"+entry.Target+"/")
			if err == nil || !strings.Contains(output, entry.Match) {
				return nil, nil, fmt.Errorf(
					"the %s family is no longer refused: `go vet ./%s/` answered %q and this gate requires a diagnostic naming %q",
					entry.Name, entry.Target, strings.TrimSpace(output), entry.Match)
			}
		case "staticcheck":
			output, err := staticcheck(options, "./"+entry.Target+"/")
			if err == nil || !strings.Contains(output, "("+entry.Check+")") {
				return nil, nil, fmt.Errorf(
					"the %s family is no longer refused: the pinned analyzer answered %q over ./%s/ and this gate requires %s",
					entry.Name, strings.TrimSpace(output), entry.Target, entry.Check)
			}
		default:
			// A family pinned by a test: the test has to exist, or the claim of
			// coverage outlives the code that made it true.
			raw, err := os.ReadFile(entry.Target)
			if err != nil {
				return nil, nil, fmt.Errorf("the %s family names %s as its owner and that file is not there: %w", entry.Name, entry.Target, err)
			}
			if !strings.Contains(string(raw), "func "+entry.Proof+"(") {
				return nil, nil, fmt.Errorf("the %s family names %s in %s as its owner and that test does not exist", entry.Name, entry.Proof, entry.Target)
			}
		}
		covered = append(covered, entry)
	}
	return covered, uncoveredFamilies, nil
}

// goFiles lists every Go file the suppression policy judges: the delivered
// tree and its tests, never what other gates keep under testdata/ to be
// refused.
func goFiles(root string) ([]string, error) {
	files := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "testdata", ".git", "node_modules", "vendor", ".local", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Strings(files)
	return files, nil
}

// baselineEntryFor finds the baseline entry of one group.
func baselineEntryFor(baseline *Baseline, group Group) *Entry {
	for index, entry := range baseline.Entry {
		if entry.Path == group.Path && entry.Check == group.Check && entry.Message == group.Message {
			return &baseline.Entry[index]
		}
	}
	return nil
}
