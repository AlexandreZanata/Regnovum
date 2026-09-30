package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixtures of the offline audit. Every rule of Rules has one here, the
// vocabulary test at the end proves it, and the two halves of the mode — the
// manifest that describes the tree and the run that denies egress — are held
// against a tree and a machine this test writes itself.

// pinnedDigest is a digest of the right shape. It is not a digest of anything:
// the manifest records references, and only a registry can say what a digest
// contains.
const pinnedDigest = "sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"

// recordedTools is a machine whose answers the test chooses.
type recordedTools map[string]string

func (r recordedTools) Version(tool string) (string, error) {
	version, exists := r[tool]
	if !exists {
		return "", fmt.Errorf("the recorded machine does not answer for %s", tool)
	}
	return version, nil
}

// agreeingTools is a machine that agrees with the fixture tree.
func agreeingTools() recordedTools {
	return recordedTools{"go": "1.27.1", "node": "24.15.0", "npm": "11.14.1"}
}

// fixtureTree writes the smallest checkout the audit reads, applies the
// mutation, and answers its root. Every fixture starts from a tree that passes,
// because a fixture that starts red proves nothing about the rule it mutates.
func fixtureTree(t *testing.T, mutate func(root string)) string {
	t.Helper()
	root := t.TempDir()

	files := map[string]string{
		"go.mod": "module fixture\n\ngo 1.27.1\n",
		"go.sum": "",
		".github/workflows/verify.yml": "jobs:\n  verify:\n    steps:\n      - uses: actions/setup-node@0000\n" +
			"        with:\n          node-version: 24\n",
		"Dockerfile":                  "FROM golang:1.27.1-bookworm@" + pinnedDigest + " AS build\n",
		"compose.yaml":                "services:\n  database:\n    image: postgres:18.4\n",
		"compose.production.yaml":     "services:\n  database:\n    image: postgres:18.4@" + pinnedDigest + "\n",
		"web/package.json":            "{\n  \"name\": \"web\",\n  \"version\": \"0.0.0\"\n}\n",
		"web/package-lock.json":       "{\n  \"lockfileVersion\": 3\n}\n",
		"tools/e2e/package.json":      "{\n  \"name\": \"e2e\",\n  \"version\": \"0.0.0\"\n}\n",
		"tools/e2e/package-lock.json": "{\n  \"lockfileVersion\": 3\n}\n",
	}
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("cannot create %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("cannot write %s: %v", full, err)
		}
	}
	if mutate != nil {
		mutate(root)
	}
	return root
}

// remove takes a file out of the tree.
func remove(path string) func(root string) {
	return func(root string) {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			panic(err)
		}
	}
}

// rewrite replaces a file of the tree.
func rewrite(path, content string) func(root string) {
	return func(root string) {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
			panic(err)
		}
	}
}

// fixtureRule is one way of being refused and the rule that names it.
type fixtureRule struct {
	name  string
	rule  string
	build func(t *testing.T) error
}

// fixtureRules answers every refusal the audit declares.
func fixtureRules() []fixtureRule {
	fromTree := func(mutate func(root string), tools Measurer) func(t *testing.T) error {
		return func(t *testing.T) error {
			root := fixtureTree(t, mutate)
			_, err := Build(root, tools)
			return err
		}
	}
	fromManifest := func(change func(manifest *Manifest)) func(t *testing.T) error {
		return func(t *testing.T) error {
			manifest, err := Build(fixtureTree(t, nil), agreeingTools())
			if err != nil {
				t.Fatalf("the fixture tree was refused: %v", err)
			}
			change(&manifest)
			violations := Violations(manifest)
			if len(violations) == 0 {
				return errors.New("the manifest was accepted")
			}
			return refusals(violations)
		}
	}
	return []fixtureRule{
		{
			name: "an installed Go that disagrees with the pin in go.mod",
			rule: RuleToolDrift,
			build: fromTree(nil, recordedTools{
				"go": "1.26.5", "node": "24.15.0", "npm": "11.14.1",
			}),
		},
		{
			name: "an installed Node of another major than the workflow pins",
			rule: RuleToolDrift,
			build: fromTree(nil, recordedTools{
				"go": "1.27.1", "node": "22.11.0", "npm": "11.14.1",
			}),
		},
		{
			name: "a machine that cannot answer the version of a tool",
			rule: RuleToolDrift,
			build: fromTree(nil, recordedTools{
				"go": "1.27.1", "node": "24.15.0",
			}),
		},
		{
			name:  "an image the Dockerfile builds from without a digest",
			rule:  RuleImageUnpinned,
			build: fromTree(rewrite("Dockerfile", "FROM golang:1.27.1-bookworm AS build\n"), agreeingTools()),
		},
		{
			name:  "an image the production compose deploys without a digest",
			rule:  RuleImageUnpinned,
			build: fromTree(rewrite("compose.production.yaml", "services:\n  database:\n    image: postgres:18.4\n"), agreeingTools()),
		},
		{
			name:  "an image the development compose runs and no file pins by digest",
			rule:  RuleImageUnregistered,
			build: fromTree(rewrite("compose.yaml", "services:\n  database:\n    image: redis:7-alpine\n"), agreeingTools()),
		},
		{
			name:  "a lockfile the manifest has to digest and the tree does not have",
			rule:  RuleMissingSource,
			build: fromTree(remove("go.sum"), agreeingTools()),
		},
		{
			name:  "a deployment file the manifest has to read and the tree does not have",
			rule:  RuleMissingSource,
			build: fromTree(remove("compose.production.yaml"), agreeingTools()),
		},
		{
			name:  "a package manifest without its lockfile",
			rule:  RuleUnlockedInstall,
			build: fromTree(remove("web/package-lock.json"), agreeingTools()),
		},
		{
			name:  "a filed manifest of another schema",
			rule:  RuleManifestNonCanonical,
			build: fromManifest(func(manifest *Manifest) { manifest.Schema = manifestSchema + 1 }),
		},
		{
			name: "a filed manifest whose tools are out of order",
			rule: RuleManifestNonCanonical,
			build: fromManifest(func(manifest *Manifest) {
				manifest.Tools[0], manifest.Tools[1] = manifest.Tools[1], manifest.Tools[0]
			}),
		},
		{
			name: "a filed manifest whose sources are out of order",
			rule: RuleManifestNonCanonical,
			build: fromManifest(func(manifest *Manifest) {
				manifest.Sources[0], manifest.Sources[1] = manifest.Sources[1], manifest.Sources[0]
			}),
		},
		{
			name:  "a filed manifest that names no tool",
			rule:  RuleManifestNonCanonical,
			build: fromManifest(func(manifest *Manifest) { manifest.Tools = nil }),
		},
		{
			name: "a filed manifest somebody added an instant to",
			rule: RuleManifestNonCanonical,
			build: func(t *testing.T) error {
				// The manifest is a function of the tree: an instant would make two
				// runs of the same tree answer different bytes, which is exactly
				// what the reader refuses.
				manifest, err := Build(fixtureTree(t, nil), agreeingTools())
				if err != nil {
					t.Fatalf("the fixture tree was refused: %v", err)
				}
				encoded, err := manifest.JSON()
				if err != nil {
					t.Fatalf("cannot render: %v", err)
				}
				patched := strings.Replace(string(encoded),
					`"schema": 1,`, `"schema": 1,`+"\n  \"at\": \"2026-09-23T12:00:00Z\",", 1)
				_, err = ParseManifest([]byte(patched))
				return err
			},
		},
		{
			name: "a filed manifest that declares a run which reached out",
			rule: RuleEgressAttempt,
			build: func(t *testing.T) error {
				// The control of the whole mode: a child process of the run
				// tries to reach the internet, the sensor records it, and the
				// run is refused — with no artifact written, because a
				// measurement taken through a hole is not a measurement.
				out := t.TempDir()
				_, err := Run(context.Background(), RunOptions{
					Root:  fixtureTree(t, nil),
					Name:  "the fixture that reaches out",
					Kind:  "audit",
					Rules: []string{"fixture-rule"},
					Out:   out,
				}, helperArgv("reach-out"))
				if err == nil {
					return errors.New("the run reached out and was accepted")
				}
				entries, readErr := os.ReadDir(out)
				if readErr != nil {
					t.Fatalf("cannot read the artifact directory: %v", readErr)
				}
				if len(entries) != 0 {
					return fmt.Errorf("the refused run filed %d artifact(s)", len(entries))
				}
				return err
			},
		},
	}
}

// refusals is every reason one artifact was refused, as one error.
type refusals []Refusal

func (r refusals) Error() string {
	details := make([]string, 0, len(r))
	for _, refusal := range r {
		details = append(details, refusal.Error())
	}
	return strings.Join(details, "; ")
}

func (r refusals) rules() []string {
	rules := make([]string, 0, len(r))
	for _, refusal := range r {
		rules = append(rules, refusal.Rule)
	}
	return rules
}

// refusalRules answers every rule an error names.
func refusalRules(err error) []string {
	var single Refusal
	if errors.As(err, &single) {
		return []string{single.Rule}
	}
	var many refusals
	if errors.As(err, &many) {
		return many.rules()
	}
	return nil
}

// TestEveryDeclaredRuleIsExercisedByAFixture is the vocabulary test: each
// fixture is refused by exactly the rule it exists for — one that breaks two
// rules proves neither — and the union is what the audit declares.
func TestEveryDeclaredRuleIsExercisedByAFixture(t *testing.T) {
	t.Parallel()

	exercised := map[string]bool{}
	for _, fixture := range fixtureRules() {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			err := fixture.build(t)
			if err == nil {
				t.Fatalf("the fixture was accepted: it exists to be refused by %s", fixture.rule)
			}
			rules := refusalRules(err)
			if len(rules) != 1 || rules[0] != fixture.rule {
				t.Fatalf("the fixture was refused by %v, and it exists for %s: %v", rules, fixture.rule, err)
			}
		})
		exercised[fixture.rule] = true
	}

	for _, rule := range Rules() {
		if !exercised[rule] {
			t.Errorf("the rule %s has no fixture: a rule nobody exercises cannot be trusted", rule)
		}
	}
	for rule := range exercised {
		declared := false
		for _, candidate := range Rules() {
			if candidate == rule {
				declared = true
			}
		}
		if !declared {
			t.Errorf("the fixture exercises %s, which the audit does not declare", rule)
		}
	}
}

// TestTwoRunsOfTheSameTreeAnswerTheSameManifest is the task's own validation:
// the manifest carries no instant, no host name and no path of this machine, so
// measuring the same tree twice answers the same bytes.
func TestTwoRunsOfTheSameTreeAnswerTheSameManifest(t *testing.T) {
	t.Parallel()

	root := fixtureTree(t, nil)
	first, err := Build(root, agreeingTools())
	if err != nil {
		t.Fatalf("the fixture tree was refused: %v", err)
	}
	second, err := Build(root, agreeingTools())
	if err != nil {
		t.Fatalf("the fixture tree was refused the second time: %v", err)
	}
	firstBytes, err := first.JSON()
	if err != nil {
		t.Fatalf("cannot render: %v", err)
	}
	secondBytes, err := second.JSON()
	if err != nil {
		t.Fatalf("cannot render the second: %v", err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("two runs of the same tree disagree:\n%s\n%s", firstBytes, secondBytes)
	}
	if bytes.Contains(firstBytes, []byte(root)) {
		t.Fatalf("the manifest carries the machine's path: %s", firstBytes)
	}
	if violations := Violations(first); len(violations) != 0 {
		t.Fatalf("the manifest of the fixture tree is refused: %v", violations)
	}

	// And what it records is what the tree says: the pins, the digests and the
	// lockfiles, each with the file it came from.
	recorded := map[string]Tool{}
	for _, tool := range first.Tools {
		recorded[tool.Name] = tool
	}
	if recorded["go"].Declared != "1.27.1" || recorded["go"].Source != "go.mod" {
		t.Fatalf("the Go pin is recorded as %+v", recorded["go"])
	}
	if recorded["node"].Declared != "24" || recorded["node"].Comparison != ComparisonMajor {
		t.Fatalf("the Node pin is recorded as %+v", recorded["node"])
	}
	pinned := map[string]string{}
	for _, image := range first.Images {
		pinned[image.Source+"|"+image.Reference] = image.Digest
	}
	if pinned["Dockerfile|golang:1.27.1-bookworm"] != pinnedDigest {
		t.Fatalf("the digest of the build image is recorded as %q", pinned["Dockerfile|golang:1.27.1-bookworm"])
	}
	if pinned["compose.yaml|postgres:18.4"] != pinnedDigest {
		t.Fatalf("the development compose reference is not resolved to the digest the production file pins: %q",
			pinned["compose.yaml|postgres:18.4"])
	}
}

// TestTheOfflineEnvironmentOwnsItsVariables is the part of the mode that an
// ambient environment could silently undo: NO_PROXY=* or GOPROXY=direct in the
// shell would leave the run looking hermetic while it reaches anywhere.
func TestTheOfflineEnvironmentOwnsItsVariables(t *testing.T) {
	t.Parallel()

	base := []string{
		"PATH=/usr/bin",
		"NO_PROXY=*",
		"GOPROXY=direct",
		"npm_config_registry=https://registry.npmjs.org/",
		"HOME=/home/somebody",
	}
	environment := DeniedEnvironment(base, "http://127.0.0.1:45678")
	values := ValuesOf(environment)

	if values["PATH"] != "/usr/bin" || values["HOME"] != "/home/somebody" {
		t.Fatalf("the mode dropped the environment it inherits: %v", environment)
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "GOPROXY", "npm_config_registry"} {
		if values[name] != "http://127.0.0.1:45678" {
			t.Fatalf("%s is %q, and the run has to point at the sensor", name, values[name])
		}
	}
	if values["NO_PROXY"] != "127.0.0.1,localhost,::1" {
		t.Fatalf("NO_PROXY is %q: the loopback exemption is what keeps the gates able to reach their database", values["NO_PROXY"])
	}
	if values["npm_config_offline"] != "true" {
		t.Fatalf("npm is not told to work from its cache: %q", values["npm_config_offline"])
	}
	if count := strings.Count(strings.Join(environment, " "), "GOPROXY="); count != 1 {
		t.Fatalf("GOPROXY appears %d times: the ambient one was not replaced", count)
	}
}

// TestTheSensorRefusesAndRecords is the observer itself, held directly: it
// answers a refusal to anything, records what was asked, and never resolves the
// name it was given.
func TestTheSensorRefusesAndRecords(t *testing.T) {
	t.Parallel()

	sensor, err := StartSensor()
	if err != nil {
		t.Fatalf("cannot start the sensor: %v", err)
	}
	defer func() { _ = sensor.Close() }()

	target, err := url.Parse(sensor.URL())
	if err != nil {
		t.Fatalf("the sensor answered an address that is not one: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(target)}}
	request, err := http.NewRequest(http.MethodGet, "http://registry.npmjs.org/left-pad", nil)
	if err != nil {
		t.Fatalf("cannot build the request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("the sensor did not answer at all: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("the sensor answered %d", response.StatusCode)
	}

	attempts := sensor.Attempts()
	if len(attempts) != 1 {
		t.Fatalf("the sensor recorded %v", attempts)
	}
	if attempts[0].Host != "registry.npmjs.org" || !strings.Contains(attempts[0].Path, "left-pad") {
		t.Fatalf("the sensor recorded %+v", attempts[0])
	}

	// A retry loop is one thing a reader has to act on, counted.
	for index := 0; index < 3; index++ {
		retry, err := http.NewRequest(http.MethodGet, "http://registry.npmjs.org/left-pad", nil)
		if err != nil {
			t.Fatalf("cannot build the retry: %v", err)
		}
		answer, err := client.Do(retry)
		if err != nil {
			t.Fatalf("the sensor stopped answering: %v", err)
		}
		_ = answer.Body.Close()
	}
	if len(sensor.Attempts()) != 1 || sensor.Count() != 4 {
		t.Fatalf("the attempts are reported as %d distinct and %d in total", len(sensor.Attempts()), sensor.Count())
	}
}

// TestARunTouchesItsDatabaseAndFilesItsDeclaration is the other half of the
// mode: the loopback addresses stay out of the sensor (otherwise every gate of
// this repository would be refused for talking to its own database), the
// artifacts are written for a green and for a red gate, and the declaration is
// the shape the evidence format reads.
func TestARunTouchesItsDatabaseAndFilesItsDeclaration(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprintln(writer, "the database of the fixture answered")
	}))
	defer server.Close()

	root := fixtureTree(t, nil)
	out := t.TempDir()

	result, err := Run(context.Background(), RunOptions{
		Root:   root,
		Name:   "make fixture-gate",
		Kind:   "go-test",
		Rules:  []string{"fixture-rule-b", "fixture-rule-a"},
		Out:    out,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Now:    func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) },
	}, helperArgv("touch-"+server.URL))
	if err != nil {
		t.Fatalf("a run that only touched loopback was refused: %v", err)
	}
	if result.Status != 0 || len(result.Attempts) != 0 {
		t.Fatalf("the run answered %+v", result)
	}
	if result.ManifestPath == "" || result.DeclarationPath == "" {
		t.Fatalf("the run filed %+v", result)
	}

	content, err := os.ReadFile(result.DeclarationPath)
	if err != nil {
		t.Fatalf("cannot read the declaration: %v", err)
	}
	declaration := Declaration{}
	if err := json.Unmarshal(content, &declaration); err != nil {
		t.Fatalf("the declaration is not readable: %v", err)
	}
	if declaration.Kind != "go-test" || declaration.Status != "pass" {
		t.Fatalf("the declaration says %+v", declaration)
	}
	if declaration.Name != "make fixture-gate" || len(declaration.Rules) != 2 {
		t.Fatalf("the declaration does not carry the suite: %+v", declaration)
	}
	if _, err := time.Parse(time.RFC3339, declaration.At); err != nil {
		t.Fatalf("the declaration does not say when it ran: %q", declaration.At)
	}
	if strings.Join(declaration.Output, "\n") == "" {
		t.Fatal("the declaration kept no output of the run it declares")
	}
	if result.Duration != 0 {
		t.Fatalf("the run reports a duration of %s for a child that did nothing measurable", result.Duration)
	}

	// The manifest of the run is the manifest of the tree, filed.
	manifest, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatalf("cannot read the manifest: %v", err)
	}
	if _, err := ParseManifest(manifest); err != nil {
		t.Fatalf("the manifest of the run is not readable: %v", err)
	}
}

// TestARedGateIsStillEvidence is the boundary the artifacts have to state: a
// failing gate is a result and gets its declaration, with status fail, and the
// run's own status is the gate's.
func TestARedGateIsStillEvidence(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	result, err := Run(context.Background(), RunOptions{
		Root:   fixtureTree(t, nil),
		Name:   "the fixture that fails",
		Kind:   "audit",
		Rules:  []string{"fixture-rule"},
		Out:    out,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}, helperArgv("fail"))
	if err != nil {
		t.Fatalf("a red gate was not accepted as a result: %v", err)
	}
	if result.Status != 3 {
		t.Fatalf("the run answered status %d", result.Status)
	}
	content, err := os.ReadFile(result.DeclarationPath)
	if err != nil {
		t.Fatalf("a red gate filed no declaration: %v", err)
	}
	declaration := Declaration{}
	if err := json.Unmarshal(content, &declaration); err != nil {
		t.Fatalf("the declaration is not readable: %v", err)
	}
	if declaration.Status != "fail" {
		t.Fatalf("the declaration of a red gate says %q", declaration.Status)
	}
}

// TestTheSlugIsAFileName holds the naming of the artifacts: a suite name comes
// from a human and a file name cannot.
func TestTheSlugIsAFileName(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"make test-unit":         "make-test-unit",
		"make test-integration":  "make-test-integration",
		"  QA/Offline Run  ":     "qa-offline-run",
		"":                       "run",
		"!!!":                    "run",
		"um nome com acentuação": "um-nome-com-acentua-o",
	}
	for name, want := range cases {
		if got := Slug(name); got != want {
			t.Errorf("Slug(%q) = %q, and the artifact is called %q", name, got, want)
		}
	}
}

// helperArgv answers the command line of a child of this test binary, which is
// the smallest process that can carry the two halves of a run: a client that
// reaches out and one that stays on loopback.
func helperArgv(mode string) []string {
	return []string{os.Args[0], "-test.run=TestTheChildProcessOfTheRun", "--", mode}
}

// TestTheChildProcessOfTheRun is the child: it does what its first argument
// says, with the environment the run gave it. It is skipped unless it was
// started as a child, which is why the mode travels in the argument list.
func TestTheChildProcessOfTheRun(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		t.Skip("this test is a child process of TestARunTouchesItsDatabaseAndFilesItsDeclaration")
	}
	mode := os.Args[separator+1]

	switch {
	case mode == "fail":
		os.Exit(3)
	case mode == "reach-out":
		client := &http.Client{Timeout: 10 * time.Second}
		_, err := client.Get("https://registry.npmjs.org/left-pad")
		if err == nil {
			fmt.Println("the child reached the registry")
			return
		}
		fmt.Printf("the child could not reach the registry: %v\n", err)
	case strings.HasPrefix(mode, "touch-"):
		target := strings.TrimPrefix(mode, "touch-")
		client := &http.Client{Timeout: 10 * time.Second}
		response, err := client.Get(target)
		if err != nil {
			fmt.Printf("the child could not reach its own database at %s: %v\n", target, err)
			os.Exit(4)
		}
		_ = response.Body.Close()
		fmt.Printf("the child read its own database at %s with status %d\n", target, response.StatusCode)
	default:
		fmt.Printf("the child does not know the mode %q\n", mode)
		os.Exit(5)
	}
}
