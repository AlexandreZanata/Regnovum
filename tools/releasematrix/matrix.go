// Package main is the release evidence matrix gate (P45-T02).
//
// It judges the versioned manifest quality/release-matrix.json and,
// given a frozen candidate SHA, one directory of per-gate result
// artifacts. A finding names exactly one broken rule; an empty set
// is the only PASS. It reads and judges; it never writes.
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

// Rules are the closed vocabulary of findings, in stable order.
const (
	RuleManifestUnreadable = "manifest-unreadable"
	RuleSchemaUnknown      = "schema-unknown"
	RuleMissingField       = "missing-field"
	RuleDuplicateID        = "duplicate-id"
	RuleAreaUnknown        = "area-unknown"
	RuleAreaUncovered      = "area-uncovered"
	RuleCommandMissing     = "command-missing"
	RuleEvidenceDuplicate  = "evidence-duplicate"
	RuleArtifactTemplate   = "artifact-template"
	RuleSkipAllowed        = "skip-allowed"
	RuleGateMissing        = "gate-missing"
	RuleGateOptional       = "gate-optional"
	RuleGateSkipped        = "gate-skipped"
	RuleGateFailed         = "gate-failed"
	RuleFalseGreen         = "false-green"
	RuleSHAMismatch        = "sha-mismatch"
	RuleResultMalformed    = "result-malformed"
	RuleResultUnknown      = "result-unknown"
)

// AllRules answers every rule this gate can emit.
func AllRules() []string {
	return []string{
		RuleManifestUnreadable,
		RuleSchemaUnknown,
		RuleMissingField,
		RuleDuplicateID,
		RuleAreaUnknown,
		RuleAreaUncovered,
		RuleCommandMissing,
		RuleEvidenceDuplicate,
		RuleArtifactTemplate,
		RuleSkipAllowed,
		RuleGateMissing,
		RuleGateOptional,
		RuleGateSkipped,
		RuleGateFailed,
		RuleFalseGreen,
		RuleSHAMismatch,
		RuleResultMalformed,
		RuleResultUnknown,
	}
}

// requiredAreas is the closed set of matrix areas from the P45-T02
// scope. Hardcoded on purpose: dropping one from the manifest has
// to be a deliberate change here, never a silent shrink.
var requiredAreas = []string{
	"verify", "quality-certify", "unit", "integration", "contract",
	"security", "privacy", "e2e", "i18n", "dast", "mutation",
	"flake", "race", "load", "restore", "upgrade", "operations",
	"supply-chain", "frontend",
}

// Manifest is the versioned release evidence matrix.
type Manifest struct {
	Schema         int            `json:"schema"`
	Note           string         `json:"note"`
	ArtifactFormat ArtifactFormat `json:"artifactFormat"`
	SkippedPolicy  SkippedPolicy  `json:"skippedPolicy"`
	Gates          []Gate         `json:"gates"`
}

// ArtifactFormat fixes the per-SHA artifact shape.
type ArtifactFormat struct {
	Schema int      `json:"schema"`
	Path   string   `json:"path"`
	Fields []string `json:"fields"`
	Note   string   `json:"note"`
}

// SkippedPolicy fixes the SKIPPED rule. allowedFor must stay empty:
// a skipped required gate is a FAIL, never a waiver.
type SkippedPolicy struct {
	Rule       string   `json:"rule"`
	AllowedFor []string `json:"allowedFor"`
}

// Gate is one matrix row: owner, command, environment, inputs,
// outputs, PASS/FAIL criteria and the evidence artifact name.
type Gate struct {
	ID          string   `json:"id"`
	Area        string   `json:"area"`
	Owner       string   `json:"owner"`
	Command     string   `json:"command"`
	Environment string   `json:"environment"`
	Inputs      []string `json:"inputs"`
	Outputs     []string `json:"outputs"`
	Pass        string   `json:"pass"`
	Fail        string   `json:"fail"`
	Evidence    string   `json:"evidence"`
	Required    *bool    `json:"required,omitempty"`
}

// Result is one per-gate run artifact for a frozen candidate SHA.
type Result struct {
	Schema      int    `json:"schema"`
	Gate        string `json:"gate"`
	Commit      string `json:"commit"`
	Verdict     string `json:"verdict"`
	ExitCode    int    `json:"exitCode"`
	Command     string `json:"command"`
	Environment string `json:"environment"`
	Skipped     bool   `json:"skipped"`
}

var targetPattern = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]*)\s*:`)

var makeCommandPattern = regexp.MustCompile(`^make ([A-Za-z0-9][A-Za-z0-9_.-]*)$`)

// LoadManifest reads the manifest file. An unreadable or malformed
// file is itself a finding, never a pass.
func LoadManifest(path string) (Manifest, []string) {
	var manifest Manifest
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifest, []string{RuleManifestUnreadable}
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, []string{RuleManifestUnreadable + ":malformed"}
	}
	return manifest, nil
}

// JudgeManifest judges the manifest against the Makefile at root.
// Every required area needs a gate, and every gate needs an owner,
// a real command, an environment, criteria and a unique evidence
// name. A fixture that breaks exactly one of these gets exactly
// its finding.
func JudgeManifest(root string, manifest Manifest) []string {
	findings := []string{}
	if manifest.Schema != 1 {
		findings = append(findings, RuleSchemaUnknown)
	}
	findings = append(findings, judgeArtifactFormat(manifest)...)
	findings = append(findings, judgeSkippedPolicy(manifest)...)
	byID := judgeGateFields(manifest, &findings)
	findings = append(findings, judgeAreaCoverage(manifest)...)
	findings = append(findings, judgeCommands(root, manifest, byID)...)
	return sortedUnique(findings)
}

func judgeArtifactFormat(manifest Manifest) []string {
	format := manifest.ArtifactFormat
	if format.Schema != 1 || strings.TrimSpace(format.Path) == "" {
		return []string{RuleMissingField + ":artifactFormat"}
	}
	if !strings.Contains(format.Path, "{commit}") || !strings.Contains(format.Path, "{gate}") {
		return []string{RuleArtifactTemplate}
	}
	for _, field := range []string{"schema", "gate", "commit", "verdict", "skipped"} {
		found := false
		for _, declared := range format.Fields {
			if declared == field {
				found = true
			}
		}
		if !found {
			return []string{RuleMissingField + ":artifactFormat.fields:" + field}
		}
	}
	return nil
}

func judgeSkippedPolicy(manifest Manifest) []string {
	if strings.TrimSpace(manifest.SkippedPolicy.Rule) == "" {
		return []string{RuleMissingField + ":skippedPolicy"}
	}
	if len(manifest.SkippedPolicy.AllowedFor) != 0 {
		return []string{RuleSkipAllowed}
	}
	return nil
}

func judgeGateFields(manifest Manifest, findings *[]string) map[string]Gate {
	byID := map[string]Gate{}
	evidence := map[string]string{}
	for _, gate := range manifest.Gates {
		if strings.TrimSpace(gate.ID) == "" {
			*findings = append(*findings, RuleMissingField+":gates.id")
			continue
		}
		if _, dup := byID[gate.ID]; dup {
			*findings = append(*findings, RuleDuplicateID+":"+gate.ID)
			continue
		}
		byID[gate.ID] = gate
		if !areaKnown(gate.Area) {
			*findings = append(*findings, RuleAreaUnknown+":"+gate.Area)
		}
		for _, field := range []string{"owner", "command", "environment", "pass", "fail", "evidence"} {
			if gateField(gate, field) == "" {
				*findings = append(*findings, RuleMissingField+":gates."+gate.ID+"."+field)
			}
		}
		if gate.Required != nil && !*gate.Required {
			*findings = append(*findings, RuleGateOptional+":"+gate.ID)
		}
		if gate.Evidence != "" {
			if prev, dup := evidence[gate.Evidence]; dup {
				*findings = append(*findings, RuleEvidenceDuplicate+":"+gate.Evidence+":"+prev+"+"+gate.ID)
			} else {
				evidence[gate.Evidence] = gate.ID
			}
		}
	}
	if len(manifest.Gates) == 0 {
		*findings = append(*findings, RuleMissingField+":gates")
	}
	return byID
}

func gateField(gate Gate, field string) string {
	switch field {
	case "owner":
		return gate.Owner
	case "command":
		return gate.Command
	case "environment":
		return gate.Environment
	case "pass":
		return gate.Pass
	case "fail":
		return gate.Fail
	case "evidence":
		return gate.Evidence
	}
	return ""
}

func areaKnown(area string) bool {
	for _, required := range requiredAreas {
		if area == required {
			return true
		}
	}
	return false
}

func judgeAreaCoverage(manifest Manifest) []string {
	covered := map[string]bool{}
	for _, gate := range manifest.Gates {
		if areaKnown(gate.Area) {
			covered[gate.Area] = true
		}
	}
	findings := []string{}
	for _, required := range requiredAreas {
		if !covered[required] {
			findings = append(findings, RuleAreaUncovered+":"+required)
		}
	}
	return findings
}

func judgeCommands(root string, manifest Manifest, byID map[string]Gate) []string {
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return []string{RuleManifestUnreadable + ":makefile"}
	}
	targets := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || line[0] == '\t' || line[0] == ' ' || line[0] == '#' {
			continue
		}
		if match := targetPattern.FindStringSubmatch(line); match != nil {
			targets[match[1]] = true
		}
	}
	findings := []string{}
	for id := range byID {
		gate := byID[id]
		match := makeCommandPattern.FindStringSubmatch(strings.TrimSpace(gate.Command))
		if match == nil || !targets[match[1]] {
			findings = append(findings, RuleCommandMissing+":"+id)
		}
	}
	return findings
}

// JudgeResults judges one directory of per-gate result artifacts
// against the manifest for the frozen candidate commit. A required
// gate without a passing, unskipped result on the frozen SHA is a
// FAIL; a declared pass with a non-zero exit is a false green.
func JudgeResults(manifest Manifest, dir, commit string) []string {
	if strings.TrimSpace(commit) == "" {
		return []string{RuleMissingField + ":commit"}
	}
	byID := map[string]Gate{}
	for _, gate := range manifest.Gates {
		byID[gate.ID] = gate
	}
	findings := []string{}
	seen := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{RuleManifestUnreadable + ":results"}
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		result, finding := loadResult(filepath.Join(dir, entry.Name()))
		if finding != "" {
			findings = append(findings, finding+":"+entry.Name())
			continue
		}
		if _, ok := byID[result.Gate]; !ok {
			findings = append(findings, RuleResultUnknown+":"+result.Gate)
			continue
		}
		seen[result.Gate] = true
		findings = append(findings, judgeOneResult(byID[result.Gate], result, commit)...)
	}
	for id := range byID {
		if !seen[id] {
			findings = append(findings, RuleGateMissing+":"+id)
		}
	}
	return sortedUnique(findings)
}

func loadResult(path string) (Result, string) {
	var result Result
	raw, err := os.ReadFile(path)
	if err != nil {
		return result, RuleManifestUnreadable
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, RuleResultMalformed
	}
	if result.Schema != 1 || strings.TrimSpace(result.Gate) == "" {
		return result, RuleResultMalformed
	}
	switch result.Verdict {
	case "pass", "fail", "skip":
	default:
		return result, RuleResultMalformed
	}
	return result, ""
}

func judgeOneResult(gate Gate, result Result, commit string) []string {
	if result.Commit != commit {
		return []string{RuleSHAMismatch + ":" + gate.ID}
	}
	if result.Skipped || result.Verdict == "skip" {
		return []string{RuleGateSkipped + ":" + gate.ID}
	}
	if result.Verdict == "pass" && result.ExitCode != 0 {
		return []string{RuleFalseGreen + ":" + gate.ID}
	}
	if result.Verdict != "pass" || result.ExitCode != 0 {
		return []string{RuleGateFailed + ":" + gate.ID}
	}
	return nil
}

func sortedUnique(findings []string) []string {
	seen := map[string]bool{}
	unique := []string{}
	for _, finding := range findings {
		if !seen[finding] {
			seen[finding] = true
			unique = append(unique, finding)
		}
	}
	sort.Strings(unique)
	return unique
}

// FormatError renders one finding for the CLI.
func FormatError(finding string) string {
	return fmt.Sprintf("releasematrix: %s", finding)
}
