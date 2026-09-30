// Package toolchainaudit is the version-pin gate of P29-T08: the
// production toolchain (Go, PostgreSQL, TypeScript, staticcheck) is
// declared once in quality/toolchain.json, and every file that builds
// production must pin exactly that version.
//
// The gate judges two rules, all static so it runs anywhere:
//
//   - pin-drift: go.mod, the builder image, package.json, both compose
//     files and the Makefile pin anything but the declared version;
//   - bad-toolchain: the declaration itself is malformed (unknown
//     envelope, missing or empty pins, non-object next map).
//
// The informative job (make toolchain-next, .github/workflows/
// toolchain.yml) reports through Report, which shares the pin readers
// but always exits zero: incompatibility is reported, never hidden, and
// lockfiles are never touched. Promoting an approved next version to
// mandatory takes a task, an ADR and the full Q0 suites — never this
// gate alone.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Finding is one violated rule with the evidence that proves it.
type Finding struct {
	Rule   string
	Detail string
}

// Toolchain is the version declaration of quality/toolchain.json.
type Toolchain struct {
	Schema int               `json:"schema"`
	Pinned map[string]string `json:"pinned"`
	Next   map[string]string `json:"next"`
	Policy string            `json:"policy"`
}

// requiredPins are the axes production must pin. The next map stays free
// form: approving a version is a human decision this gate only reads.
var requiredPins = []string{"go", "postgres", "typescript", "staticcheck"}

// ToolchainPath is the declaration judged by the gate, relative to root.
const ToolchainPath = "quality/toolchain.json"

// Read loads and validates the declaration envelope. A malformed
// declaration is a finding source, never a silent default: there is no
// version the gate may assume.
func Read(root string) (Toolchain, []Finding) {
	raw, err := os.ReadFile(filepath.Join(root, ToolchainPath))
	if err != nil {
		return Toolchain{}, []Finding{{Rule: "bad-toolchain", Detail: err.Error()}}
	}
	var declared Toolchain
	if err := json.Unmarshal(raw, &declared); err != nil {
		return Toolchain{}, []Finding{{Rule: "bad-toolchain", Detail: fmt.Sprintf("%s is not JSON: %v", ToolchainPath, err)}}
	}
	var findings []Finding
	if declared.Schema != 1 {
		findings = append(findings, Finding{Rule: "bad-toolchain", Detail: fmt.Sprintf("schema = %d, want 1", declared.Schema)})
	}
	for _, axis := range requiredPins {
		if strings.TrimSpace(declared.Pinned[axis]) == "" {
			findings = append(findings, Finding{Rule: "bad-toolchain", Detail: fmt.Sprintf("pinned %q is missing or empty", axis)})
		}
	}
	return declared, findings
}

var (
	goModPattern         = regexp.MustCompile(`(?m)^go (\S+)`)
	dockerGoPattern      = regexp.MustCompile(`FROM golang:(\d+\.\d+\.\d+)-bookworm@`)
	typescriptPattern    = regexp.MustCompile(`"typescript":\s*"([^"]+)"`)
	postgresImagePattern = regexp.MustCompile(`image:\s*postgres:([0-9.]+)`)
	staticcheckPattern   = regexp.MustCompile(`(?m)^STATICCHECK_VERSION\s*:=\s*(\S+)`)
)

// pinSource is one production file and the axis it must pin.
type pinSource struct {
	axis string
	file string
	read func(content string) (string, bool)
}

func firstGroup(pattern *regexp.Regexp, content string) (string, bool) {
	match := pattern.FindStringSubmatch(content)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// sources lists every production pin the gate owns: the language, the
// builder image, the frontend compiler and both database images, plus
// the analyzer the lint gate runs.
func sources() []pinSource {
	return []pinSource{
		{axis: "go", file: "go.mod", read: func(content string) (string, bool) {
			return firstGroup(goModPattern, content)
		}},
		{axis: "go", file: "Dockerfile", read: func(content string) (string, bool) {
			return firstGroup(dockerGoPattern, content)
		}},
		{axis: "typescript", file: "web/package.json", read: func(content string) (string, bool) {
			return firstGroup(typescriptPattern, content)
		}},
		{axis: "postgres", file: "compose.yaml", read: func(content string) (string, bool) {
			return firstGroup(postgresImagePattern, content)
		}},
		{axis: "postgres", file: "compose.production.yaml", read: func(content string) (string, bool) {
			return firstGroup(postgresImagePattern, content)
		}},
		{axis: "staticcheck", file: "Makefile", read: func(content string) (string, bool) {
			return firstGroup(staticcheckPattern, content)
		}},
	}
}

// Audit judges every production pin against the declaration and returns
// every drift. A source the gate cannot read is a finding, never an
// assumption that it matches.
func Audit(root string) []Finding {
	declared, findings := Read(root)
	for _, source := range sources() {
		raw, err := os.ReadFile(filepath.Join(root, source.file))
		if err != nil {
			findings = append(findings, Finding{Rule: "pin-drift", Detail: fmt.Sprintf("%s is unreadable: %v", source.file, err)})
			continue
		}
		pinned, ok := source.read(string(raw))
		if !ok {
			findings = append(findings, Finding{Rule: "pin-drift", Detail: fmt.Sprintf("%s declares no %s version", source.file, source.axis)})
			continue
		}
		if want := declared.Pinned[source.axis]; pinned != want {
			findings = append(findings, Finding{Rule: "pin-drift", Detail: fmt.Sprintf("%s pins %s %s, want %s", source.file, source.axis, pinned, want)})
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

// Report renders the informative verdict: which pins hold, which drifted
// and which next versions are approved. It always returns the lines and
// never fails: reporting is the job, and the job must not turn red.
func Report(root string) []string {
	declared, malformed := Read(root)
	var lines []string
	for _, finding := range malformed {
		lines = append(lines, "toolchain: declaration "+finding.Detail)
	}
	for _, source := range sources() {
		raw, err := os.ReadFile(filepath.Join(root, source.file))
		if err != nil {
			lines = append(lines, fmt.Sprintf("toolchain: %s %s unreadable", source.axis, source.file))
			continue
		}
		pinned, ok := source.read(string(raw))
		want := declared.Pinned[source.axis]
		switch {
		case !ok:
			lines = append(lines, fmt.Sprintf("toolchain: %s %s declares nothing (want %s): INCOMPATIBLE", source.axis, source.file, want))
		case pinned != want:
			lines = append(lines, fmt.Sprintf("toolchain: %s %s pins %s (want %s): INCOMPATIBLE", source.axis, source.file, pinned, want))
		default:
			lines = append(lines, fmt.Sprintf("toolchain: %s %s pins %s: holds", source.axis, source.file, pinned))
		}
	}
	if len(declared.Next) == 0 {
		lines = append(lines, "toolchain: no approved next patch: file a task and an ADR to approve one; lockfiles stay untouched")
		return lines
	}
	axes := make([]string, 0, len(declared.Next))
	for axis := range declared.Next {
		axes = append(axes, axis)
	}
	sort.Strings(axes)
	for _, axis := range axes {
		lines = append(lines, fmt.Sprintf("toolchain: approved next %s %s: probe before promoting; promotion needs the full Q0 suites", axis, declared.Next[axis]))
	}
	return lines
}
