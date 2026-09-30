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
	"regexp"
	"sort"
	"strings"
)

// The rules of the offline audit: one per reason a manifest or a run is refused.
// They are the vocabulary of a failure and of the gate that reads it, and every
// one of them has a fixture in audit_test.go.
const (
	// RuleManifestNonCanonical is a manifest that is not in the order of its
	// format, or that declares another schema. Two runs of the same tree have to
	// produce the same bytes: that is what makes "the same tools were used" a
	// statement somebody can check.
	RuleManifestNonCanonical = "manifest-non-canonical"
	// RuleToolDrift is an installed tool whose version disagrees with the one
	// the tree pins. A pin nobody compares against the machine is a comment.
	RuleToolDrift = "tool-drift"
	// RuleImageUnpinned is an image reference in a file that builds or deploys
	// without a digest: the tag was there yesterday and may mean something else
	// tomorrow.
	RuleImageUnpinned = "image-unpinned"
	// RuleImageUnregistered is an image the development compose runs whose
	// reference is pinned by a digest nowhere in the tree.
	RuleImageUnregistered = "image-unregistered"
	// RuleMissingSource is a file the manifest has to digest and the tree does
	// not have. Evidence of an installation names the lockfiles it came from.
	RuleMissingSource = "missing-source"
	// RuleUnlockedInstall is a package manifest without its lockfile: an
	// installation that would resolve versions on the network.
	RuleUnlockedInstall = "unlocked-install"
	// RuleEgressAttempt is a run that reached out. It comes from the sensor and
	// not from the command's output: what a process printed about itself is not
	// a measurement of what it did.
	RuleEgressAttempt = "egress-attempt"
)

// Rules answers every rule of the audit, in a fixed order.
func Rules() []string {
	return []string{
		RuleManifestNonCanonical,
		RuleToolDrift,
		RuleImageUnpinned,
		RuleImageUnregistered,
		RuleMissingSource,
		RuleUnlockedInstall,
		RuleEgressAttempt,
	}
}

// manifestSchema is the version of the manifest document. It is written into
// the artifact so that a reader of a later format knows which one it holds.
const manifestSchema = 1

// Refusal is why something was refused, named by rule.
type Refusal struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

func (r Refusal) Error() string { return r.Rule + ": " + r.Detail }

// refuse builds a refusal.
func refuse(rule, format string, args ...any) Refusal {
	return Refusal{Rule: rule, Detail: fmt.Sprintf(format, args...)}
}

// The comparison a tool's pin is held to.
const (
	// ComparisonExact requires the measured version to be the declared one.
	ComparisonExact = "exact"
	// ComparisonMajor requires the same major version: the workflow pins the
	// major and lets the patch move, which is the pin the tree states.
	ComparisonMajor = "major"
	// ComparisonRegistered records a tool the tree does not pin because it
	// arrives with another one (npm ships with the Node the workflows pin). The
	// manifest names it so that a reader knows which versions produced the
	// artifact, and no drift is claimed because no pin exists to drift from.
	ComparisonRegistered = "registered"
)

// Tool is one toolchain of the run: where the tree pins it, what the machine
// answers, and how the two are compared.
type Tool struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	Declared   string `json:"declared"`
	Measured   string `json:"measured"`
	Comparison string `json:"comparison"`
}

// Image is one container image reference the tree uses, with the digest it is
// pinned by — empty when it is pinned by a tag alone.
type Image struct {
	Reference   string `json:"reference"`
	Digest      string `json:"digest"`
	Source      string `json:"source"`
	Requirement string `json:"requirement"`
}

// The requirement of an image reference.
const (
	// RequirementDigest is a reference in a file that builds or deploys: it has
	// to carry the digest.
	RequirementDigest = "digest"
	// RequirementRegistration is a reference the development compose runs: it
	// has to appear with a digest somewhere in the tree, because the machine
	// that runs it cannot pin what it does not know.
	RequirementRegistration = "registration"
)

// Source is one file the manifest digests, and what kind of file it is.
type Source struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

// The kinds of source.
const (
	KindLockfile        = "lockfile"
	KindGoModule        = "go-module"
	KindPackageManifest = "package-manifest"
	KindDeployment      = "deployment"
)

// Manifest is the record of the toolchains, the images and the lockfiles one
// run used. It carries no instant, no host name and no absolute path: the same
// tree measured on a machine with the same tools has to answer the same bytes,
// which is what "two runs produce the same manifest" means.
type Manifest struct {
	Schema  int      `json:"schema"`
	Tools   []Tool   `json:"tools"`
	Images  []Image  `json:"images"`
	Sources []Source `json:"sources"`
	Rules   []string `json:"rules"`
}

// Measurer answers the version of an installed tool. It is an interface so that
// a test can hold the manifest against a recorded machine instead of the one
// running the test.
type Measurer interface {
	Version(tool string) (string, error)
}

// hostTools measures the tools of the machine the run is on.
type hostTools struct{}

// Version answers the version of one tool, normalized: the manifest records the
// version, not the command's prose.
func (hostTools) Version(tool string) (string, error) {
	var args []string
	switch tool {
	case "go":
		args = []string{"version"}
	case "node":
		args = []string{"--version"}
	case "npm":
		args = []string{"--version"}
	default:
		return "", fmt.Errorf("unknown tool %q", tool)
	}
	command := exec.Command(tool, args...)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", tool, strings.Join(args, " "), err)
	}
	return normalizeVersion(tool, strings.TrimSpace(string(output))), nil
}

// normalizeVersion reduces what a tool prints to the version it names.
func normalizeVersion(tool, printed string) string {
	fields := strings.Fields(printed)
	switch tool {
	case "go":
		// `go version` answers "go version go1.27.1 linux/amd64".
		for _, field := range fields {
			if strings.HasPrefix(field, "go1") || strings.HasPrefix(field, "go2") {
				return strings.TrimPrefix(field, "go")
			}
		}
		return printed
	default:
		// `node --version` and `npm --version` answer "v24.15.0" and "11.14.1".
		return strings.TrimPrefix(printed, "v")
	}
}

// The files the manifest digests, with what each one is. They are the
// declarations the preload and the offline run are made of: the module and the
// lockfiles fix the dependencies, the deployment files fix the images.
var requiredSources = []struct {
	Path string
	Kind string
}{
	{"go.mod", KindGoModule},
	{"go.sum", KindLockfile},
	{"web/package.json", KindPackageManifest},
	{"web/package-lock.json", KindLockfile},
	{"tools/e2e/package.json", KindPackageManifest},
	{"tools/e2e/package-lock.json", KindLockfile},
	{"Dockerfile", KindDeployment},
	{"compose.yaml", KindDeployment},
	{"compose.production.yaml", KindDeployment},
}

// The files the manifest reads the pins from.
const (
	goModulePath       = "go.mod"
	verifyWorkflowPath = ".github/workflows/verify.yml"
	dockerfilePath     = "Dockerfile"
	developmentCompose = "compose.yaml"
	productionCompose  = "compose.production.yaml"
)

var (
	goDirective       = regexp.MustCompile(`(?m)^go[ \t]+(\S+)[ \t]*$`)
	nodeVersionPin    = regexp.MustCompile(`(?m)^[ \t]*node-version:[ \t]*(\S+)[ \t]*$`)
	dockerfileFrom    = regexp.MustCompile(`(?mi)^FROM[ \t]+(\S+)`)
	composeImage      = regexp.MustCompile(`(?m)^[ \t]*image:[ \t]*(\S+)[ \t]*$`)
	imageDigestSuffix = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
)

// Build reads the tree and measures the machine, answering the manifest of the
// run. It refuses a tree that cannot be described: a source it cannot read, a
// package manifest without its lockfile, an image without a digest where the
// tree deploys one.
func Build(root string, measurer Measurer) (Manifest, error) {
	manifest := Manifest{Schema: manifestSchema, Rules: Rules()}

	// The tree is judged before it is described: a package manifest without its
	// lockfile is the more specific statement, and reporting it as a file the
	// manifest could not digest would hide the reason.
	if refusal := judgeTree(root); refusal != nil {
		return Manifest{}, *refusal
	}

	sources, err := digestSources(root)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Sources = sources

	tools, err := measureTools(root, measurer)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Tools = tools

	images, refusals, err := readImages(root)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Images = images
	if len(refusals) != 0 {
		return Manifest{}, refusals[0]
	}

	manifest.canonicalize()
	return manifest, nil
}

// digestSources reads and digests every file the manifest names.
func digestSources(root string) ([]Source, error) {
	sources := make([]Source, 0, len(requiredSources))
	for _, required := range requiredSources {
		path := filepath.Join(root, filepath.FromSlash(required.Path))
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, refuse(RuleMissingSource, "the tree has no %s: %v", required.Path, err)
		}
		sum := sha256.Sum256(content)
		sources = append(sources, Source{Path: required.Path, Kind: required.Kind, SHA256: hex.EncodeToString(sum[:])})
	}
	return sources, nil
}

// measureTools reads the pins the tree states and measures the machine.
func measureTools(root string, measurer Measurer) ([]Tool, error) {
	module, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(goModulePath)))
	if err != nil {
		return nil, refuse(RuleMissingSource, "the tree has no %s: %v", goModulePath, err)
	}
	declaredGo := ""
	if match := goDirective.FindSubmatch(module); match != nil {
		declaredGo = string(match[1])
	}

	workflow, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(verifyWorkflowPath)))
	if err != nil {
		return nil, refuse(RuleMissingSource, "the tree has no %s: %v", verifyWorkflowPath, err)
	}
	declaredNode := ""
	if match := nodeVersionPin.FindSubmatch(workflow); match != nil {
		declaredNode = string(match[1])
	}

	tools := []Tool{
		{Name: "go", Source: goModulePath, Declared: declaredGo, Comparison: ComparisonExact},
		{Name: "node", Source: verifyWorkflowPath, Declared: declaredNode, Comparison: ComparisonMajor},
		{
			Name: "npm", Source: verifyWorkflowPath, Comparison: ComparisonRegistered,
			Declared: "ships with the Node the workflows pin",
		},
	}
	for index := range tools {
		measured, err := measurer.Version(tools[index].Name)
		if err != nil {
			return nil, refuse(RuleToolDrift, "the machine cannot answer the version of %s: %v", tools[index].Name, err)
		}
		tools[index].Measured = measured
		if violation := toolViolation(tools[index]); violation != nil {
			return nil, *violation
		}
	}
	return tools, nil
}

// toolViolation holds one tool against its pin.
func toolViolation(tool Tool) *Refusal {
	switch tool.Comparison {
	case ComparisonExact:
		if tool.Measured != tool.Declared {
			refusal := refuse(RuleToolDrift, "%s is %s on this machine and %s declares %s", tool.Name, tool.Measured, tool.Source, tool.Declared)
			return &refusal
		}
	case ComparisonMajor:
		if major(tool.Measured) != major(tool.Declared) {
			refusal := refuse(RuleToolDrift, "%s is %s on this machine and %s pins the major %s", tool.Name, tool.Measured, tool.Source, tool.Declared)
			return &refusal
		}
	}
	return nil
}

// major answers the major component of a version, which is what a pin of "24"
// asks about.
func major(version string) string {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if index := strings.IndexAny(trimmed, ".-+ "); index >= 0 {
		return trimmed[:index]
	}
	return trimmed
}

// readImages answers every image reference the tree uses, in canonical order,
// and the first refusal an unpinned or unregistered one produces.
func readImages(root string) ([]Image, []Refusal, error) {
	images := []Image{}
	refusals := []Refusal{}

	// The references that build and deploy: each one has to carry its digest.
	for _, path := range []string{dockerfilePath, productionCompose} {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return nil, nil, refuse(RuleMissingSource, "the tree has no %s: %v", path, err)
		}
		pattern := composeImage
		if path == dockerfilePath {
			pattern = dockerfileFrom
		}
		for _, match := range pattern.FindAllSubmatch(content, -1) {
			reference := string(match[1])
			if strings.Contains(reference, "${") {
				// The operator supplies this one, digest included: the file
				// demands it and cannot state it.
				continue
			}
			registered := imageDigestSuffix.MatchString(reference)
			image := Image{
				Reference:   strings.TrimSuffix(reference, digestOf(reference)),
				Digest:      strings.TrimPrefix(digestOf(reference), "@"),
				Source:      path,
				Requirement: RequirementDigest,
			}
			if !registered {
				refusals = append(refusals, refuse(RuleImageUnpinned, "%s uses %s without a digest", path, reference))
			}
			images = append(images, image)
		}
	}

	// The references the development compose runs: each one has to be
	// registered by a digest somewhere in the tree, which is what lets the
	// development machine and the production deployment name the same bytes.
	pinned := map[string]string{}
	for _, image := range images {
		if image.Digest != "" {
			pinned[image.Reference] = image.Digest
		}
	}
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(developmentCompose)))
	if err != nil {
		return nil, nil, refuse(RuleMissingSource, "the tree has no %s: %v", developmentCompose, err)
	}
	for _, match := range composeImage.FindAllSubmatch(content, -1) {
		reference := string(match[1])
		if strings.Contains(reference, "${") {
			continue
		}
		image := Image{Reference: reference, Source: developmentCompose, Requirement: RequirementRegistration}
		digest, isRegistered := pinned[reference]
		if !isRegistered {
			refusals = append(refusals, refuse(RuleImageUnregistered, "%s runs %s and no file in the tree pins it by digest", developmentCompose, reference))
		}
		image.Digest = digest
		images = append(images, image)
	}

	sort.SliceStable(refusals, func(left, right int) bool {
		if refusals[left].Rule != refusals[right].Rule {
			return refusals[left].Rule < refusals[right].Rule
		}
		return refusals[left].Detail < refusals[right].Detail
	})
	return images, refusals, nil
}

// digestOf answers the digest part of a reference, if it has one.
func digestOf(reference string) string {
	if location := strings.Index(reference, "@sha256:"); location >= 0 {
		return reference[location:]
	}
	return ""
}

// judgeTree refuses a tree whose installation could not be reproduced offline:
// a package manifest without its lockfile resolves versions on the network.
func judgeTree(root string) *Refusal {
	for _, required := range requiredSources {
		if required.Kind != KindPackageManifest {
			continue
		}
		lockfile := strings.TrimSuffix(required.Path, "package.json") + "package-lock.json"
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(lockfile))); err != nil {
			refusal := refuse(RuleUnlockedInstall, "%s has no %s beside it: the installation would resolve versions on the network", required.Path, lockfile)
			return &refusal
		}
	}
	return nil
}

// canonicalize puts the manifest in the order of its format. There is no
// instant to normalize: the manifest deliberately carries none.
func (m *Manifest) canonicalize() {
	sort.SliceStable(m.Tools, func(left, right int) bool { return m.Tools[left].Name < m.Tools[right].Name })
	sort.SliceStable(m.Images, func(left, right int) bool {
		if m.Images[left].Reference != m.Images[right].Reference {
			return m.Images[left].Reference < m.Images[right].Reference
		}
		return m.Images[left].Source < m.Images[right].Source
	})
	sort.SliceStable(m.Sources, func(left, right int) bool { return m.Sources[left].Path < m.Sources[right].Path })
}

// JSON renders the manifest as it is filed: indented, fields in the order the
// struct declares, with a trailing newline, so that two runs of the same tree
// answer the same bytes.
func (m Manifest) JSON() ([]byte, error) {
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// ParseManifest reads a filed manifest, refusing a field this reader does not
// know: a reader that drops a field reports on a document nobody wrote.
func ParseManifest(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded Manifest
	if err := decoder.Decode(&decoded); err != nil {
		return Manifest{}, refuse(RuleManifestNonCanonical, "the manifest is not readable: %v", err)
	}
	if decoder.More() {
		return Manifest{}, refuse(RuleManifestNonCanonical, "the manifest carries more than one document")
	}
	return decoded, nil
}

// Violations answers what is wrong with a filed manifest: what only a reader of
// the artifact can ask, including whether it is in the order its format states.
func Violations(manifest Manifest) []Refusal {
	violations := []Refusal{}
	if manifest.Schema != manifestSchema {
		violations = append(violations, refuse(RuleManifestNonCanonical, "the manifest declares schema %d, and this reader knows %d", manifest.Schema, manifestSchema))
	}
	if len(manifest.Rules) == 0 {
		violations = append(violations, refuse(RuleManifestNonCanonical, "the manifest does not say which rules it was read against"))
	}
	if len(manifest.Tools) == 0 {
		violations = append(violations, refuse(RuleManifestNonCanonical, "the manifest names no tool: evidence of nothing"))
	}
	if len(manifest.Sources) == 0 {
		violations = append(violations, refuse(RuleManifestNonCanonical, "the manifest digests no source: an installation that came from nowhere"))
	}
	for index := 1; index < len(manifest.Tools); index++ {
		if manifest.Tools[index-1].Name >= manifest.Tools[index].Name {
			violations = append(violations, refuse(RuleManifestNonCanonical, "the tools are not in order: %s after %s", manifest.Tools[index].Name, manifest.Tools[index-1].Name))
		}
	}
	for index := 1; index < len(manifest.Sources); index++ {
		if manifest.Sources[index-1].Path >= manifest.Sources[index].Path {
			violations = append(violations, refuse(RuleManifestNonCanonical, "the sources are not in order: %s after %s", manifest.Sources[index].Path, manifest.Sources[index-1].Path))
		}
	}
	for _, tool := range manifest.Tools {
		if tool.Measured == "" {
			violations = append(violations, refuse(RuleToolDrift, "the manifest does not measure %s", tool.Name))
			continue
		}
		if violation := toolViolation(tool); violation != nil {
			violations = append(violations, *violation)
		}
	}
	for _, image := range manifest.Images {
		if image.Reference == "" {
			violations = append(violations, refuse(RuleImageUnpinned, "the manifest carries an empty image reference"))
			continue
		}
		if image.Requirement == RequirementDigest && image.Digest == "" {
			violations = append(violations, refuse(RuleImageUnpinned, "%s is used by %s without a digest", image.Reference, image.Source))
		}
		if image.Requirement == RequirementRegistration && image.Digest == "" {
			violations = append(violations, refuse(RuleImageUnregistered, "%s is run by %s and no file in the tree pins it by digest", image.Reference, image.Source))
		}
	}
	sort.SliceStable(violations, func(left, right int) bool {
		if violations[left].Rule != violations[right].Rule {
			return violations[left].Rule < violations[right].Rule
		}
		return violations[left].Detail < violations[right].Detail
	})
	return violations
}
