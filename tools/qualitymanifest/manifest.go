// Package qualitymanifest builds and verifies deterministic quality
// evidence bundles (P30-T03): one directory holding manifest.json, its
// checksum, and every artifact the certification reads.
//
// The bundle contains the commit, the clean-tree fact, the toolchain
// pins, the catalog, the waivers, the run results, coverage, mutation,
// security, SLO/capacity evidence and a checksum per file. A missing
// field, a missing artifact or a changed checksum fails verification;
// any PII or secret detected in an artifact refuses the build. The same
// evidence always produces byte-identical output: sorted entries, fixed
// JSON encoding, no timestamps. External signatures stay out until the
// infrastructure decision; the checksum file is the tamper evidence.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testsupport"
)

// ManifestSchema is the only envelope this tool writes and reads.
const ManifestSchema = 1

// Artifact is one bundled file with its checksum: the checksum is over
// the bytes stored in the bundle, so verification needs nothing but the
// bundle itself.
type Artifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// Manifest is the bundle descriptor. Toolchains mirrors the pinned map
// of quality/toolchain.json; Artifacts covers the tree files and
// Evidence the run-result files, both sorted by name.
type Manifest struct {
	Schema     int               `json:"schema"`
	Commit     string            `json:"commit"`
	Clean      bool              `json:"clean"`
	Toolchains map[string]string `json:"toolchains"`
	Artifacts  []Artifact        `json:"artifacts"`
	Evidence   []Artifact        `json:"evidence"`
}

// treeArtifacts are the tree files the bundle must contain, as
// manifest name and tree-relative path. They cover the catalog, the
// waivers, coverage, mutation, security (SBOM), SLO/capacity and the
// toolchain and tier declarations.
var treeArtifacts = []struct{ name, path string }{
	{"catalog", "quality/catalog.json"},
	{"waivers", "quality/waivers.json"},
	{"test-waivers", "quality/test-waivers.json"},
	{"coverage", "quality/coverage.json"},
	{"mutations", "quality/mutations.json"},
	{"sbom", "quality/sbom.json"},
	{"capacity-baseline", "quality/capacity-baseline.json"},
	{"toolchain", "quality/toolchain.json"},
	{"tiers", "quality/tiers.json"},
	{"slo", "docs/SLO.md"},
}

// Inputs are everything Assemble needs, with no environment reads: the
// commit, the clean-tree fact, the toolchain pins and the file bytes by
// manifest name. Collect gathers them from a checkout; tests build them
// by hand.
type Inputs struct {
	Commit     string
	Clean      bool
	Toolchains map[string]string
	Tree       map[string][]byte
	Evidence   map[string][]byte
}

// Finding is one violated rule with the evidence that proves it.
type Finding struct {
	Rule   string
	Detail string
}

// Assemble validates the inputs and renders the canonical manifest. It
// refuses a missing field, a missing artifact, an empty evidence set
// and any PII or secret any artifact carries.
func Assemble(inputs Inputs) (Manifest, []Finding) {
	var findings []Finding
	if strings.TrimSpace(inputs.Commit) == "" {
		findings = append(findings, Finding{Rule: "missing-field", Detail: "commit is empty"})
	}
	if !inputs.Clean {
		findings = append(findings, Finding{Rule: "dirty-tree", Detail: "the bundle refuses a tree with uncommitted changes"})
	}
	if len(inputs.Toolchains) == 0 {
		findings = append(findings, Finding{Rule: "missing-field", Detail: "toolchains is empty"})
	}
	if len(inputs.Evidence) == 0 {
		findings = append(findings, Finding{Rule: "missing-field", Detail: "evidence holds no run results"})
	}

	manifest := Manifest{
		Schema:     ManifestSchema,
		Commit:     inputs.Commit,
		Clean:      inputs.Clean,
		Toolchains: inputs.Toolchains,
	}
	for _, want := range treeArtifacts {
		content, ok := inputs.Tree[want.name]
		if !ok {
			findings = append(findings, Finding{Rule: "missing-artifact", Detail: fmt.Sprintf("tree artifact %q (%s) is absent", want.name, want.path)})
			continue
		}
		if sensitive := testsupport.SensitiveFindings(content); len(sensitive) != 0 {
			findings = append(findings, Finding{Rule: "sensitive-content", Detail: fmt.Sprintf("tree artifact %q carries %s", want.name, sensitive[0])})
			continue
		}
		manifest.Artifacts = append(manifest.Artifacts, describe(want.name, "artifacts/"+want.name, content))
	}
	names := make([]string, 0, len(inputs.Evidence))
	for name := range inputs.Evidence {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		content := inputs.Evidence[name]
		if sensitive := testsupport.SensitiveFindings(content); len(sensitive) != 0 {
			findings = append(findings, Finding{Rule: "sensitive-content", Detail: fmt.Sprintf("evidence %q carries %s", name, sensitive[0])})
			continue
		}
		manifest.Evidence = append(manifest.Evidence, describe(name, "evidence/"+name, content))
	}
	sort.Slice(manifest.Artifacts, func(i, j int) bool { return manifest.Artifacts[i].Name < manifest.Artifacts[j].Name })
	return manifest, findings
}

func describe(name, path string, content []byte) Artifact {
	sum := sha256.Sum256(content)
	return Artifact{Name: name, Path: path, SHA256: hex.EncodeToString(sum[:]), Bytes: len(content)}
}

// canonical renders the manifest deterministically: Go marshals map keys
// sorted and struct fields in order, with fixed indentation, so the same
// inputs always produce the same bytes on every machine.
func canonical(manifest Manifest) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(manifest); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Collect gathers bundle inputs from a checkout: the HEAD commit, the
// clean-tree fact, the toolchain pins and every required file. Anything
// it cannot read becomes part of the inputs for Assemble to refuse.
func Collect(root, evidenceDir string) Inputs {
	inputs := Inputs{
		Toolchains: map[string]string{},
		Tree:       map[string][]byte{},
		Evidence:   map[string][]byte{},
	}
	if out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output(); err == nil {
		inputs.Commit = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output(); err == nil {
		inputs.Clean = strings.TrimSpace(string(out)) == ""
	}
	if raw, err := os.ReadFile(filepath.Join(root, "quality", "toolchain.json")); err == nil {
		var declared struct {
			Pinned map[string]string `json:"pinned"`
		}
		if err := json.Unmarshal(raw, &declared); err == nil {
			inputs.Toolchains = declared.Pinned
		}
	}
	for _, want := range treeArtifacts {
		if content, err := os.ReadFile(filepath.Join(root, want.path)); err == nil {
			inputs.Tree[want.name] = content
		}
	}
	if entries, err := os.ReadDir(evidenceDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if content, err := os.ReadFile(filepath.Join(evidenceDir, entry.Name())); err == nil {
				inputs.Evidence[entry.Name()] = content
			}
		}
	}
	return inputs
}

// Build assembles the inputs into a bundle directory: manifest.json, the
// checksum file and a copy of every artifact. It refuses to write a
// bundle the verifier would reject.
func Build(bundleDir string, inputs Inputs) []Finding {
	manifest, findings := Assemble(inputs)
	if len(findings) != 0 {
		return findings
	}
	encoded, err := canonical(manifest)
	if err != nil {
		return []Finding{{Rule: "unencodable-manifest", Detail: err.Error()}}
	}
	write := func(name string, content []byte) []Finding {
		full := filepath.Join(bundleDir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return []Finding{{Rule: "unwritable-bundle", Detail: err.Error()}}
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			return []Finding{{Rule: "unwritable-bundle", Detail: err.Error()}}
		}
		return nil
	}
	if findings := write("manifest.json", encoded); findings != nil {
		return findings
	}
	sum := sha256.Sum256(encoded)
	if findings := write("manifest.sha256", []byte(hex.EncodeToString(sum[:])+"\n")); findings != nil {
		return findings
	}
	for _, artifact := range manifest.Artifacts {
		if findings := write(filepath.Join("artifacts", artifact.Name), inputs.Tree[artifact.Name]); findings != nil {
			return findings
		}
	}
	for _, artifact := range manifest.Evidence {
		if findings := write(filepath.Join("evidence", artifact.Name), inputs.Evidence[artifact.Name]); findings != nil {
			return findings
		}
	}
	return nil
}

// Verify judges a bundle directory: the checksum file must match the
// manifest, every listed artifact must be present with matching bytes,
// and every required name must be listed. It reads nothing but the
// bundle.
func Verify(bundleDir string) []Finding {
	encoded, err := os.ReadFile(filepath.Join(bundleDir, "manifest.json"))
	if err != nil {
		return []Finding{{Rule: "missing-field", Detail: fmt.Sprintf("manifest.json is unreadable: %v", err)}}
	}
	sealed, err := os.ReadFile(filepath.Join(bundleDir, "manifest.sha256"))
	if err != nil {
		return []Finding{{Rule: "missing-field", Detail: fmt.Sprintf("manifest.sha256 is unreadable: %v", err)}}
	}
	sum := sha256.Sum256(encoded)
	if strings.TrimSpace(string(sealed)) != hex.EncodeToString(sum[:]) {
		return []Finding{{Rule: "checksum-mismatch", Detail: "manifest.sha256 does not match manifest.json"}}
	}
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return []Finding{{Rule: "missing-field", Detail: fmt.Sprintf("manifest.json is not JSON: %v", err)}}
	}
	var findings []Finding
	if manifest.Schema != ManifestSchema {
		findings = append(findings, Finding{Rule: "missing-field", Detail: fmt.Sprintf("schema = %d, want %d", manifest.Schema, ManifestSchema)})
	}
	if strings.TrimSpace(manifest.Commit) == "" {
		findings = append(findings, Finding{Rule: "missing-field", Detail: "commit is empty"})
	}
	if !manifest.Clean {
		findings = append(findings, Finding{Rule: "dirty-tree", Detail: "the manifest records an unclean tree"})
	}
	required := map[string]bool{}
	for _, want := range treeArtifacts {
		required[want.name] = true
	}
	for _, artifact := range append(append([]Artifact{}, manifest.Artifacts...), manifest.Evidence...) {
		delete(required, artifact.Name)
		content, err := os.ReadFile(filepath.Join(bundleDir, artifact.Path))
		if err != nil {
			findings = append(findings, Finding{Rule: "missing-artifact", Detail: fmt.Sprintf("%s is unreadable: %v", artifact.Path, err)})
			continue
		}
		actual := sha256.Sum256(content)
		if hex.EncodeToString(actual[:]) != artifact.SHA256 {
			findings = append(findings, Finding{Rule: "checksum-mismatch", Detail: fmt.Sprintf("%s bytes changed", artifact.Path)})
		}
	}
	for name := range required {
		findings = append(findings, Finding{Rule: "missing-artifact", Detail: fmt.Sprintf("required artifact %q is not listed", name)})
	}
	if len(manifest.Evidence) == 0 {
		findings = append(findings, Finding{Rule: "missing-field", Detail: "evidence holds no run results"})
	}
	return findings
}
