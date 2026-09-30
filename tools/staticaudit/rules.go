// Package main is the static analysis gate of P23-T02 ("toolchain estática
// profissional") and the record of what it refuses.
//
// The gate pins two analyzers and holds them to the toolchain of the
// repository:
//
//   - `go vet ./...`, the standard library's own analyzers, pinned by the Go
//     version `go.mod` declares;
//   - `staticcheck` at the module version the Makefile pins, built with that
//     same toolchain — the analyzer reads the export data of the standard
//     library it analyzes, so an analyzer built with an older Go refuses a
//     module that declares a newer one (the ADR records the exact refusal).
//
// It refuses, naming the rule:
//
//   - a finding that is not in the versioned baseline, or whose count in a file
//     grew: the baseline is a ratchet that can only shrink, and every entry of
//     it carries the task that owns the debt and why it is accepted here;
//
//   - a baseline entry the tree no longer produces: an accepted finding that
//     disappeared has to leave the baseline, or the file becomes a description
//     of a past that nobody can check;
//
//   - a suppression that is not local, that does not name a check, that carries
//     no reason, that is written in a vocabulary the pinned analyzer does not
//     read (`//nolint:staticcheck` is a directive of another linter: the pinned
//     analyzer ignores it, so the comment would silence nothing while looking
//     like it does), or that the analyzer answered as a directive which matched
//     nothing — a silence nobody needed looks exactly like a silence that
//     works, and only one of them is honest;
//
//   - the directives it judges are read as comments of the parsed file, never as
//     the bytes of the line: a test that quotes `//lint:ignore SA1012 reason`
//     inside a string is describing a directive, not writing one, and a scanner
//     that matched text would refuse the tests of this gate before it refused a
//     real finding;
//
//   - a family of rules that stopped being refused: every family with an
//     analyzer owner has a fixture under testdata/ that must produce its check,
//     so a rule that stopped biting fails the gate instead of passing quietly.
//
// The fixture directories live under testdata/, which the go tool skips when a
// pattern ends in ./..., so they are never judged as delivered code: they exist
// to be refused.
package main

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Finding is one diagnostic of one analyzer, as the analyzer printed it.
type Finding struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Check   string `json:"check"`
	Message string `json:"message"`
}

// findingLine matches what staticcheck prints:
// `path/to/file.go:12:34: message (CHECK)`. The check is what makes a finding
// comparable across edits: the message explains it and the line moves.
var findingLine = regexp.MustCompile(`^(.+\.go):(\d+):(\d+): (.+) \(([A-Z]+\d+)\)$`)

// parseFindings reads the analyzer's output and keeps the diagnostics it
// recognizes. A line that is not a diagnostic (a note, a warning about a
// package pattern, a stack trace) is ignored, because inventing a finding out
// of prose would let a broken run look like a clean one.
func parseFindings(output string) []Finding {
	findings := []Finding{}
	for _, line := range strings.Split(output, "\n") {
		match := findingLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		lineNumber, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		findings = append(findings, Finding{
			Path:    filepath.ToSlash(match[1]),
			Line:    lineNumber,
			Message: match[4],
			Check:   match[5],
		})
	}
	sort.Slice(findings, func(one, other int) bool {
		if findings[one].Path != findings[other].Path {
			return findings[one].Path < findings[other].Path
		}
		if findings[one].Check != findings[other].Check {
			return findings[one].Check < findings[other].Check
		}
		return findings[one].Line < findings[other].Line
	})
	return findings
}

// Group is one comparable identity of the baseline: the same check with the
// same message in the same file. The count is part of it, so a second identical
// finding is a change the baseline has to name.
//
// The line is deliberately not part of the identity: a finding is the same
// finding after the code above it moves, and a baseline keyed by line would
// turn every unrelated edit into a new violation.
type Group struct {
	Path    string `json:"path"`
	Check   string `json:"check"`
	Message string `json:"message"`
	Count   int    `json:"count"`
	Line    int    `json:"line"`
}

// groupFindings collapses the findings into the identities the baseline holds.
func groupFindings(findings []Finding) []Group {
	index := map[string]*Group{}
	order := []string{}
	for _, finding := range findings {
		key := finding.Path + "\x00" + finding.Check + "\x00" + finding.Message
		group, seen := index[key]
		if !seen {
			group = &Group{Path: finding.Path, Check: finding.Check, Message: finding.Message, Line: finding.Line}
			index[key] = group
			order = append(order, key)
		}
		group.Count++
	}
	groups := make([]Group, 0, len(order))
	for _, key := range order {
		groups = append(groups, *index[key])
	}
	sort.Slice(groups, func(one, other int) bool {
		if groups[one].Path != groups[other].Path {
			return groups[one].Path < groups[other].Path
		}
		if groups[one].Check != groups[other].Check {
			return groups[one].Check < groups[other].Check
		}
		return groups[one].Message < groups[other].Message
	})
	return groups
}

// baselineSchema is the version of the baseline document. The gate refuses a
// file whose schema it does not know: a baseline read by the wrong reader is a
// baseline that accepts the wrong things.
const baselineSchema = 1

// Entry is one accepted finding: what it is, how many of them the tree has, who
// owns the debt and why it is accepted today.
type Entry struct {
	Path    string `json:"path"`
	Check   string `json:"check"`
	Message string `json:"message"`
	Count   int    `json:"count"`
	Owner   string `json:"owner"`
	Reason  string `json:"reason"`
}

// Baseline is the versioned list of accepted findings.
type Baseline struct {
	Schema int     `json:"schema"`
	Note   string  `json:"note"`
	Entry  []Entry `json:"entries"`
}

// ownerPattern is the shape of an owner: a phase task that will pay the debt,
// or a named follow-up. An empty owner would make the baseline anonymous, which
// is how a list of accepted findings becomes a list nobody owns.
var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// loadBaseline reads and judges the document itself before any comparison.
func loadBaseline(path string) (*Baseline, []string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("read the baseline %s: %v", path, err)}
	}
	baseline := &Baseline{}
	if err := json.Unmarshal(raw, baseline); err != nil {
		return nil, []string{fmt.Sprintf("parse the baseline %s: %v", path, err)}
	}
	violations := []string{}
	if baseline.Schema != baselineSchema {
		violations = append(violations, fmt.Sprintf("the baseline declares schema %d; this gate reads schema %d", baseline.Schema, baselineSchema))
	}
	seen := map[string]bool{}
	for _, entry := range baseline.Entry {
		key := entry.Path + "\x00" + entry.Check + "\x00" + entry.Message
		if seen[key] {
			violations = append(violations, fmt.Sprintf("the baseline lists %s %s twice: one identity, one entry", entry.Path, entry.Check))
		}
		seen[key] = true
		if entry.Path == "" || entry.Check == "" || entry.Message == "" {
			violations = append(violations, fmt.Sprintf("the baseline has an entry without a path, a check or a message: %+v", entry))
		}
		if entry.Count < 1 {
			violations = append(violations, fmt.Sprintf("the baseline accepts %d of %s %s: an accepted finding is one or more", entry.Count, entry.Path, entry.Check))
		}
		if !ownerPattern.MatchString(entry.Owner) {
			violations = append(violations, fmt.Sprintf("the baseline entry for %s %s has no owner: an accepted finding names who pays the debt", entry.Path, entry.Check))
		}
		if strings.TrimSpace(entry.Reason) == "" {
			violations = append(violations, fmt.Sprintf("the baseline entry for %s %s has no reason: an accepted finding says why it is accepted", entry.Path, entry.Check))
		}
	}
	if len(violations) > 0 {
		return nil, violations
	}
	return baseline, nil
}

// baselineRefusal compares the tree with the baseline in both directions: a
// finding that appeared or grew is new debt, and an entry that disappeared is a
// line of the baseline that has to go.
func baselineRefusal(groups []Group, baseline *Baseline) []string {
	accepted := map[string]Entry{}
	for _, entry := range baseline.Entry {
		accepted[entry.Path+"\x00"+entry.Check+"\x00"+entry.Message] = entry
	}
	violations := []string{}
	current := map[string]bool{}
	for _, group := range groups {
		key := group.Path + "\x00" + group.Check + "\x00" + group.Message
		current[key] = true
		entry, known := accepted[key]
		if !known {
			violations = append(violations, fmt.Sprintf(
				"%s:%d: %s: %s — not in the baseline (quality/lint-baseline.json): fix it, or have a human accept it with an owner and a reason",
				group.Path, group.Line, group.Check, group.Message))
			continue
		}
		if group.Count > entry.Count {
			violations = append(violations, fmt.Sprintf(
				"%s: %s appears %d times and the baseline accepts %d — the baseline is a ratchet, and it only shrinks",
				group.Path, group.Check, group.Count, entry.Count))
		}
	}
	for _, entry := range baseline.Entry {
		key := entry.Path + "\x00" + entry.Check + "\x00" + entry.Message
		if !current[key] {
			violations = append(violations, fmt.Sprintf(
				"the baseline still accepts %s %s, which the tree no longer produces: remove the entry (owner was %s)", entry.Path, entry.Check, entry.Owner))
		}
	}
	return violations
}

// Suppression is one directive that asks an analyzer to stay silent, with what
// the gate could read out of it.
type Suppression struct {
	Path    string
	Line    int
	Kind    string // "lint" (the pinned analyzer's own) or "nolint" (another linter's vocabulary)
	Check   string
	Reason  string
	FileWid bool
}

var (
	// `//lint:ignore SA1012 reason` and `//lint:file-ignore SA1012 reason` are
	// the directives the pinned analyzer reads.
	lintDirective = regexp.MustCompile(`^//lint:(ignore|file-ignore)\s+([A-Z]+\d+)\s*(.*)$`)
	// `//nolint:staticcheck reason` is the vocabulary of another linter. The
	// pinned analyzer does not read it, so a comment in that shape is a
	// suppression that suppresses nothing.
	noLintDirective = regexp.MustCompile(`^//nolint(?::([a-zA-Z0-9_,-]+))?\s*(.*)$`)
)

// suppressionRefusal judges every directive the tree carries. It refuses what
// the pinned analyzer would ignore, a file-wide silence, a directive without a
// check, a directive without a reason, and a file it cannot read; what it
// accepts, it reports.
//
// The directives come from the parsed file's own comments. A file whose
// directives cannot be read is a file whose silences cannot be judged, and this
// gate answers that with a violation rather than with silence.
func suppressionRefusal(root string, files []string) ([]Suppression, []string) {
	accepted := []Suppression{}
	violations := []string{}
	positions := token.NewFileSet()
	for _, relative := range files {
		parsed, err := parser.ParseFile(positions, filepath.Join(root, filepath.FromSlash(relative)), nil, parser.ParseComments)
		if err != nil {
			violations = append(violations, fmt.Sprintf(
				"%s: the gate cannot read the file whose suppressions it judges: %v", relative, err))
			continue
		}
		for _, group := range parsed.Comments {
			for _, comment := range group.List {
				line := positions.Position(comment.Slash).Line
				if match := lintDirective.FindStringSubmatch(comment.Text); match != nil {
					suppression := Suppression{
						Path: relative, Line: line, Kind: "lint",
						Check: match[2], Reason: strings.TrimSpace(match[3]), FileWid: match[1] == "file-ignore",
					}
					if suppression.FileWid {
						violations = append(violations, fmt.Sprintf(
							"%s:%d: a file-wide suppression silences a file, not a finding: silence the line, and let the report name it",
							relative, line))
						continue
					}
					if suppression.Reason == "" {
						violations = append(violations, fmt.Sprintf(
							"%s:%d: the suppression of %s carries no reason: an analyzer is silenced with an argument, not with silence",
							relative, line, suppression.Check))
						continue
					}
					accepted = append(accepted, suppression)
					continue
				}
				match := noLintDirective.FindStringSubmatch(comment.Text)
				if match == nil {
					continue
				}
				check := strings.TrimSpace(match[1])
				if !strings.Contains(check, "staticcheck") && !strings.Contains(check, "all") {
					// A directive for a linter this gate does not pin: the gate does
					// not own that vocabulary and must not pretend to judge it.
					continue
				}
				violations = append(violations, fmt.Sprintf(
					"%s:%d: `//nolint:staticcheck` is not the pinned analyzer's vocabulary and it silences nothing there — write `//lint:ignore <CHECK> <reason>` (or fix the finding)",
					relative, line))
			}
		}
	}
	sort.Slice(accepted, func(one, other int) bool {
		if accepted[one].Path != accepted[other].Path {
			return accepted[one].Path < accepted[other].Path
		}
		return accepted[one].Line < accepted[other].Line
	})
	return accepted, violations
}

// unusedDirective matches the analyzer's own answer to a directive that matched
// nothing: `path.go:12:2: this linter directive didn't match anything; should
// it be removed? (staticcheck)`. The check name is the analyzer's, not a
// numbered check, and a gate that only parsed numbered checks would throw this
// answer away — which is precisely the answer that says a suppression is a
// leftover.
var unusedDirective = regexp.MustCompile(`^(.+\.go):(\d+):(\d+): (this linter directive didn't match anything[^(]*)\(staticcheck\)$`)

// unusedDirectiveRefusal turns the analyzer's answer into a violation: a
// directive the analyzer read and never needed is a comment that reads like a
// suppression and suppresses nothing, which is the failure mode this gate
// exists for.
func unusedDirectiveRefusal(output string) []string {
	violations := []string{}
	for _, line := range strings.Split(output, "\n") {
		match := unusedDirective.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		violations = append(violations, fmt.Sprintf(
			"%s:%s: %s the analyzer read the directive and it matched nothing: a suppression nobody needs is a silence that reads like a guarantee — delete the comment",
			filepath.ToSlash(match[1]), match[2], strings.TrimSpace(match[4])))
	}
	return violations
}

// family is one family of rules the phase names, with the owner that enforces
// it and the proof that the owner still bites.
type family struct {
	Name   string
	Owner  string // "vet", "staticcheck" or a test that owns the rule
	Check  string // the check that must fire, empty when a test owns the family
	Match  string // a substring of the diagnostic, for the analyzers that print no check
	Target string // the fixture directory, or the file that owns the rule
	Proof  string // the test that owns the family, empty when an analyzer does
	Reason string
}

// families is the honest map of the phase's rule families. Every entry either
// has an analyzer owner and a fixture that must be refused, or names the test
// that owns the family — and the gate verifies that the proof exists, so a
// mapping cannot survive the code it describes.
//
// shadowing is the one family with no owner: no check in the pinned analyzers
// covers shadowed identifiers (verified against the analyzer's own list of 149
// checks), and the ADR records the gap instead of pretending the gate closes
// it.
var families = []family{
	{
		Name: "correctness", Owner: "vet", Match: "has arg name of wrong type",
		Target: "tools/staticaudit/testdata/correctness",
		Reason: "the standard library's printf analyzer is the floor of the toolchain; it catches the wrong verb before anybody reads the line",
	},
	{
		Name: "error handling", Owner: "staticcheck", Check: "ST1008",
		Target: "tools/staticaudit/testdata/error_handling",
		Reason: "an error that is not the last result makes every caller check it somewhere else",
	},
	{
		Name: "nilness", Owner: "staticcheck", Check: "SA5000",
		Target: "tools/staticaudit/testdata/nilness",
		Reason: "a write into a nil map cannot work at runtime and a reader does not see it",
	},
	{
		Name: "context", Owner: "staticcheck", Check: "SA1012",
		Target: "tools/staticaudit/testdata/context",
		Reason: "a nil context panics far away from the line that caused it",
	},
	{
		Name: "SQL", Owner: "the architecture gate", Proof: "TestOwnedDependenciesStayWithTheirOwners",
		Target: "internal/architecture_boundaries_test.go",
		Reason: "no analyzer knows which package may talk SQL; the boundary gate confines the driver to the PostgreSQL adapter and refuses an application import (P23-T01)",
	},
	{
		Name: "HTTP", Owner: "the architecture gate", Proof: "TestLayerDependenciesPointInward",
		Target: "internal/architecture_boundaries_test.go",
		Reason: "the transport package belongs to the transport layers; the boundary gate refuses it anywhere else, and `go vet` registers the httpresponse check over the tree",
	},
	{
		Name: "security", Owner: "the architecture gate", Proof: "TestTheBoundaryRulesRefuseFixtures",
		Target: "internal/architecture_boundaries_test.go",
		Reason: "provider and driver types stay behind their adapters; dependency CVEs are `make vuln` (govulncheck) and the release audit is `make security-audit`, neither of them a staticcheck rule",
	},
}

// uncoveredFamilies are the families of the phase that the pinned toolchain
// does not enforce, with the reason. They are printed by the gate: a gap that
// nobody prints is a gap that reads like coverage.
var uncoveredFamilies = []string{
	"shadowing: no check of the pinned analyzers covers shadowed identifiers; adding one means a third toolchain, which the ADR did not admit",
}
