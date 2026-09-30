// Package runbookaudit is the executable verification of docs/RUNBOOKS.md
// (P29-T07): the incident procedures are only useful when their commands
// are safe, their references resolve and their signals reproduce.
//
// The gate reads the document and judges four rules, all static so the
// gate runs anywhere without a live stack:
//
//   - unmarked-destructive: a section holding destructive tokens (removal,
//     DDL/DML destruction, --apply, kill, container removal) must carry
//     the ⚠ marker, exactly like R4 marks its retention --apply;
//   - unknown-host: hosts in commands must be loopback or operator
//     variables ($SITE for the public certificate check); a hardcoded
//     external host is a finding;
//   - broken-link: relative document links must resolve to files and
//     #anchors to headings of the same document;
//   - unknown-path: HTTP paths in executable blocks must be registered
//     routes (routes.go Path entries and mux Handle patterns, tests and
//     fixtures excluded), script paths must exist in the tree and make
//     targets must exist in the Makefile.
//
// The three executable tabletops (R1 5xx, R3 pool, R6 lag) live in
// tabletop_test.go and run the runbook's own checks against an isolated
// stack, producing JSON evidence. The gate never rewrites anything.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Finding is one violated rule with the location that proves it.
type Finding struct {
	Rule   string
	Detail string
}

// Paths judged by the gate, relative to the repository root.
const (
	runbookPath = "docs/RUNBOOKS.md"
	makefile    = "Makefile"
)

// Audit judges the runbook document against the tree at root and returns
// every finding. An unreadable document is itself a finding, never a pass.
func Audit(root string) []Finding {
	raw, err := os.ReadFile(filepath.Join(root, runbookPath))
	if err != nil {
		return []Finding{{Rule: "unreadable-runbook", Detail: err.Error()}}
	}
	document := string(raw)
	sections := splitSections(document)

	var findings []Finding
	findings = append(findings, checkDestructiveMarkers(sections)...)
	findings = append(findings, checkHosts(extractBash(document))...)
	findings = append(findings, checkLinks(root, document)...)
	findings = append(findings, checkCommands(root, document)...)
	return findings
}

// section is one ## scope of the document with the text it owns.
type section struct {
	heading string
	body    string
}

// splitSections cuts the document at level-2 headings; text before the
// first one belongs to the preamble section.
func splitSections(document string) []section {
	var sections []section
	current := section{heading: "preamble"}
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(line, "## ") {
			sections = append(sections, current)
			current = section{heading: strings.TrimSpace(line[3:])}
			continue
		}
		current.body += line + "\n"
	}
	return append(sections, current)
}

// destructivePatterns are tokens no incident procedure runs without an
// explicit mark: removal, data destruction, applied retention, process
// and container killing, host power operations.
var destructivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\brm\s+-[a-z]*r`),
	regexp.MustCompile(`\bDROP\b`),
	regexp.MustCompile(`\bDELETE\s+FROM\b`),
	regexp.MustCompile(`\bTRUNCATE\b`),
	regexp.MustCompile(`--apply\b`),
	regexp.MustCompile(`\bkill(all)?\b`),
	regexp.MustCompile(`\bpkill\b`),
	regexp.MustCompile(`docker\s+(rm|down)\b`),
	regexp.MustCompile(`\b(shutdown|reboot|mkfs)\b`),
}

// checkDestructiveMarkers requires the ⚠ marker in every section whose
// executable lines hold destructive tokens. Comments and prose are out
// of scope: a commented step is not runnable, and prose that explicitly
// negates the flag ("sem --apply") documents the safe form. What the
// rule judges is what an operator copy-pastes.
func checkDestructiveMarkers(sections []section) []Finding {
	var findings []Finding
	for _, current := range sections {
		var runnable []string
		for _, block := range extractBash(current.body) {
			for _, line := range strings.Split(strings.ReplaceAll(block, "\\\n", " "), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				runnable = append(runnable, trimmed)
			}
		}
		executable := strings.Join(runnable, "\n")
		var hit string
		for _, pattern := range destructivePatterns {
			if match := pattern.FindString(executable); match != "" {
				hit = match
				break
			}
		}
		if hit == "" {
			continue
		}
		if !strings.Contains(current.body, "⚠") {
			findings = append(findings, Finding{
				Rule:   "unmarked-destructive",
				Detail: fmt.Sprintf("section %q runs %q without the ⚠ marker", current.heading, hit),
			})
		}
	}
	return findings
}

var (
	bashBlockPattern = regexp.MustCompile("(?s)```bash\n(.*?)```")
	urlPattern       = regexp.MustCompile(`https?://([^\s/:$'"\]]+)((?::\d+)?(?:/[^\s?$'"\]]*)?)`)
	connectPattern   = regexp.MustCompile(`-connect\s+["']?(\$[A-Za-z_]+|[\w.\-]+)`)
)

// extractBash returns the executable blocks of the document in order.
func extractBash(document string) []string {
	var blocks []string
	for _, match := range bashBlockPattern.FindAllStringSubmatch(document, -1) {
		blocks = append(blocks, match[1])
	}
	return blocks
}

// loopbackHosts are the only hardcoded hosts incident commands may name;
// anything else must arrive as an operator variable ($SITE and friends).
var loopbackHosts = map[string]bool{
	"127.0.0.1": true,
	"localhost": true,
	"::1":       true,
}

// checkHosts requires every hardcoded host in executable blocks to be
// loopback: a procedure that phones an external host by default runs
// outside the marked environment.
func checkHosts(blocks []string) []Finding {
	var findings []Finding
	for _, block := range blocks {
		for _, match := range urlPattern.FindAllStringSubmatch(block, -1) {
			host := strings.Trim(match[1], "[]")
			if strings.HasPrefix(host, "$") || loopbackHosts[host] {
				continue
			}
			findings = append(findings, Finding{Rule: "unknown-host", Detail: fmt.Sprintf("hardcoded external host %q", match[1])})
		}
		for _, match := range connectPattern.FindAllStringSubmatch(block, -1) {
			host := match[1]
			if strings.HasPrefix(host, "$") || loopbackHosts[strings.Trim(host, "[]")] {
				continue
			}
			findings = append(findings, Finding{Rule: "unknown-host", Detail: fmt.Sprintf("hardcoded connect target %q", host)})
		}
	}
	return findings
}

var (
	linkPattern   = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	headingPrefix = regexp.MustCompile(`(?m)^#{1,6}\s+(.*)$`)
)

// slugify renders a heading the way GitHub anchors it: lowercase,
// unicode letters and numbers kept, spaces to hyphens, everything else
// dropped without collapsing.
func slugify(heading string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			out.WriteRune(r)
		case unicode.IsSpace(r) || r == '-':
			out.WriteRune('-')
		}
	}
	return out.String()
}

// checkLinks requires every relative document link to resolve to a file
// and every same-document #anchor to match a heading.
func checkLinks(root, document string) []Finding {
	anchors := map[string]bool{}
	for _, match := range headingPrefix.FindAllStringSubmatch(document, -1) {
		anchors[slugify(match[1])] = true
	}
	var findings []Finding
	for _, match := range linkPattern.FindAllStringSubmatch(document, -1) {
		target := match[2]
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		if strings.HasPrefix(target, "#") {
			if !anchors[strings.ToLower(target[1:])] {
				findings = append(findings, Finding{Rule: "broken-anchor", Detail: fmt.Sprintf("anchor %q matches no heading", target)})
			}
			continue
		}
		file, anchor, _ := strings.Cut(target, "#")
		if file == "" {
			if anchor != "" && !anchors[strings.ToLower(anchor)] {
				findings = append(findings, Finding{Rule: "broken-anchor", Detail: fmt.Sprintf("anchor %q matches no heading", target)})
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "docs", file)); err != nil {
			findings = append(findings, Finding{Rule: "broken-link", Detail: fmt.Sprintf("link %q resolves to nothing", target)})
		}
	}
	return findings
}

var (
	routePathPattern   = regexp.MustCompile(`Path:\s*"([^"]+)"`)
	routeHandlePattern = regexp.MustCompile(`Handle(?:Func)?\(\s*"(?:[A-Z]+\s+)?([^"\s]+)"`)
	makeTargetPattern  = regexp.MustCompile(`(?m)^([a-z][\w-]*):`)
	makeInvokePattern  = regexp.MustCompile(`(?:^|[\s;&])make\s+([a-z][\w-]*)`)
	composeFilePattern = regexp.MustCompile(`-f\s+([^\s\\;]+)`)
	// scriptPathPattern only judges invocations with a directory: a bare
	// filename in a comment is a mention, not something runnable.
	scriptPathPattern = regexp.MustCompile(`(?:^|[\s;])([\w][\w\-.]*\/[\w\-./]*\.sh)\b`)
)

// routeAuthority collects every registered HTTP path of the tree:
// routes.go Path entries and mux Handle patterns, excluding tests and
// fixtures, which never serve production traffic.
func routeAuthority(root string) (map[string]bool, error) {
	authority := map[string]bool{}
	walk := func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if name == ".git" || name == "node_modules" || name == "testdata" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range routePathPattern.FindAllStringSubmatch(string(raw), -1) {
			authority[match[1]] = true
		}
		for _, match := range routeHandlePattern.FindAllStringSubmatch(string(raw), -1) {
			authority[strings.TrimSpace(match[1])] = true
		}
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		return nil, err
	}
	return authority, nil
}

// checkCommands judges the executable blocks: URL paths must be
// registered routes, compose files and scripts must exist in the tree,
// and make targets must exist in the Makefile.
func checkCommands(root, document string) []Finding {
	authority, err := routeAuthority(root)
	if err != nil {
		return []Finding{{Rule: "unreadable-tree", Detail: err.Error()}}
	}
	targets := map[string]bool{}
	if raw, err := os.ReadFile(filepath.Join(root, makefile)); err == nil {
		for _, match := range makeTargetPattern.FindAllStringSubmatch(string(raw), -1) {
			targets[match[1]] = true
		}
	}

	var findings []Finding
	seen := map[string]bool{}
	report := func(rule, detail string) {
		key := rule + "\x00" + detail
		if seen[key] {
			return
		}
		seen[key] = true
		findings = append(findings, Finding{Rule: rule, Detail: detail})
	}
	for _, block := range extractBash(document) {
		for _, match := range urlPattern.FindAllStringSubmatch(block, -1) {
			rest := match[2]
			if strings.HasPrefix(rest, ":") {
				if slash := strings.Index(rest, "/"); slash >= 0 {
					rest = rest[slash:]
				} else {
					continue
				}
			}
			if rest == "" || rest == "/" {
				continue
			}
			path := rest
			if !authority[path] {
				report("unknown-path", fmt.Sprintf("URL path %q is not a registered route", path))
			}
		}
		joined := strings.ReplaceAll(block, "\\\n", " ")
		for _, match := range composeFilePattern.FindAllStringSubmatch(joined, -1) {
			if _, err := os.Stat(filepath.Join(root, match[1])); err != nil {
				report("missing-file", fmt.Sprintf("compose file %q does not exist", match[1]))
			}
		}
		for _, match := range scriptPathPattern.FindAllStringSubmatch(joined, -1) {
			if _, err := os.Stat(filepath.Join(root, match[1])); err != nil {
				report("missing-script", fmt.Sprintf("script %q does not exist", match[1]))
			}
		}
		for _, match := range makeInvokePattern.FindAllStringSubmatch(joined, -1) {
			if !targets[match[1]] {
				report("missing-target", fmt.Sprintf("make target %q does not exist", match[1]))
			}
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Rule != findings[j].Rule {
			return findings[i].Rule < findings[j].Rule
		}
		return findings[i].Detail < findings[j].Detail
	})
	return findings
}
