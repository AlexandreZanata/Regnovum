package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repositoryRoot is the tree the document belongs to. The rules that resolve
// the commit read from here, so the fixture is built on the repository's own
// HEAD: a fixture commit the repository does not have would exercise the rule
// that refuses it instead of the rules that accept a real one.
const repositoryRoot = "../.."

// head returns the commit the fixture describes.
func head(t *testing.T) string {
	t.Helper()
	command := exec.Command("git", "-C", repositoryRoot, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("this suite reads the history of the checkout it runs in: %v", err)
	}
	return strings.TrimSpace(string(output))
}

// digest is a well formed sha256 the fixture uses where a digest is expected.
const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// fixture is a green run: the shape of a verification that passed twice, built
// from the repository it describes. The mutations below are each one change to
// this, and the control is that it passes untouched.
func fixture(t *testing.T) Facts {
	t.Helper()
	commands := make([]Command, 0, len(requiredCommands)+1)
	for index, key := range requiredCommands {
		commands = append(commands, Command{
			Key:     key,
			Command: "make " + key,
			Runs: []Run{
				{Seconds: seconds(float64(index) + 1.5), ExitCode: 0},
				{Seconds: seconds(float64(index) + 1.7), ExitCode: 0},
			},
			Note: "mede " + key,
		})
	}
	return Facts{
		Version:    factsVersion,
		VerifiedOn: "2026-09-22",
		Commit:     head(t),
		Branch:     "phase-20-release-readiness",
		Checkout: Checkout{
			Kind:              "git worktree limpo no commit",
			Clean:             true,
			LocalTrackedFiles: 0,
			Generated:         documentPath,
			Dirty:             []string{documentPath},
		},
		Host: Host{
			OS: "Linux 6.8", Arch: "x86_64", CPUs: 8, Go: "go1.27.1", Node: "v24.15.0",
			NPM: "11.14.1", Docker: "27.0.0/27.0.0", Sqlc: "v1.29.0", Postgres: "18.4",
		},
		Lockfiles: []Lockfile{
			{Path: "go.sum", SHA256: strings.Repeat("a", 64), Installs: "go mod download"},
			{Path: "web/package-lock.json", SHA256: strings.Repeat("b", 64), Installs: "npm ci --prefix web"},
			{Path: "tools/e2e/package-lock.json", SHA256: strings.Repeat("c", 64), Installs: "npm ci --prefix tools/e2e"},
		},
		Commands: commands,
		Image: Image{
			Reference: "goyim-arena:release-verify", ID: digest, Digest: digest, SizeBytes: 31457280,
			Smoke: "make image-verify", SmokeDetail: "a imagem sobe e serve uma página", SmokeOK: true,
		},
		Repository: Repository{TrackedFiles: 1200, Fsck: "ok", FsckErrors: 0},
		Governance: Governance{
			Command: "make release-gate", Blocked: true,
			Open: []string{"terms-of-use", "security-channel"},
			Pending: []string{
				"repository-license", "data-subject-channel", "retention-policy", "launch-markets",
			},
			Note: "o portão das decisões humanas",
		},
		Limits: []string{
			"Os gates que exigem ambiente próprio não rodaram aqui.",
			"A verificação mede a máquina desta execução.",
			"A imagem não foi publicada num registry.",
			"O portão de release continua vermelho.",
			"O dataset é criado pelos harnesses.",
			"O documento entra no commit seguinte.",
		},
		Next: []string{"Fechar as decisões e rodar finish.", "As T08 e T09 fecham a fase."},
	}
}

// audit renders one facts value as the document and judges it, which is exactly
// what the gate does with the committed file.
func audit(t *testing.T, facts Facts) []Violation {
	t.Helper()
	rendered, err := Render(facts)
	if err != nil {
		t.Fatalf("rendering the fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "RELEASE_CHECKLIST.md")
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		t.Fatalf("writing the rendered fixture: %v", err)
	}
	document, err := ReadDocument(path)
	if err != nil {
		t.Fatalf("reading the rendered fixture: %v", err)
	}
	return Check(repositoryRoot, document)
}

// auditText judges a document whose text is the subject, which is how the
// parsing rules and the PII scan are falsified.
func auditText(t *testing.T, body string) []Violation {
	t.Helper()
	path := filepath.Join(t.TempDir(), "RELEASE_CHECKLIST.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the document: %v", err)
	}
	document, err := ReadDocument(path)
	if err != nil {
		t.Fatalf("reading the document: %v", err)
	}
	return Check(repositoryRoot, document)
}

func rulesOf(violations []Violation) []string {
	names := make([]string, 0, len(violations))
	for _, violation := range violations {
		names = append(names, violation.Rule)
	}
	return names
}

func hasRule(violations []Violation, rule string) bool {
	return contains(rulesOf(violations), rule)
}

// seconds builds a recorded duration.
func seconds(value float64) *float64 { return &value }

// command returns a pointer to the command with a key, so a mutation addresses
// one entry instead of the first.
func command(facts *Facts, key string) *Command {
	for index := range facts.Commands {
		if facts.Commands[index].Key == key {
			return &facts.Commands[index]
		}
	}
	return nil
}

// TestTheGreenFixtureIsAccepted is the control. Without it every mutation below
// could be passing because the rules refuse everything.
func TestTheGreenFixtureIsAccepted(t *testing.T) {
	facts := fixture(t)
	if violations := audit(t, facts); len(violations) > 0 {
		t.Fatalf("a green run is refused: %s", violations)
	}
	// The fixture is only a model of the document if rendering it produces the
	// two halves the reader and the tool each need.
	rendered, err := Render(facts)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if !strings.Contains(rendered, "## 6. Limitações reais") {
		t.Fatal("the rendered document has no section for the real limitations")
	}
	if !strings.Contains(rendered, registerFence) {
		t.Fatal("the rendered document has no machine-readable block")
	}
}

// TestOneMutationPerRule is the falsification: one change per rule, each one the
// change a careless run or a hasty edit could produce, and each one refused by
// the rule it belongs to.
func TestOneMutationPerRule(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Facts) bool
		rule   string
	}{
		{
			name: "a schema version from another tool",
			mutate: func(facts *Facts) bool {
				facts.Version = 2
				return true
			},
			rule: "version",
		},
		{
			name: "a date no reader can place",
			mutate: func(facts *Facts) bool {
				facts.VerifiedOn = "22/09/2026"
				return true
			},
			rule: "verified-on",
		},
		{
			name: "a commit that is not an identifier",
			mutate: func(facts *Facts) bool {
				facts.Commit = "e63dc29"
				return true
			},
			rule: "commit-format",
		},
		{
			name: "a commit nobody can check out",
			mutate: func(facts *Facts) bool {
				facts.Commit = strings.Repeat("0", 40)
				return true
			},
			rule: "commit-exists",
		},
		{
			name: "no branch recorded",
			mutate: func(facts *Facts) bool {
				facts.Branch = " "
				return true
			},
			rule: "commit-branch",
		},
		{
			name: "gates run against another major version",
			mutate: func(facts *Facts) bool {
				facts.Host.Postgres = "17.6"
				return true
			},
			rule: "host-postgres",
		},
		{
			name: "a toolchain with no version recorded",
			mutate: func(facts *Facts) bool {
				facts.Host.Go = ""
				return true
			},
			rule: "host-go",
		},
		{
			name: "a machine with no processor",
			mutate: func(facts *Facts) bool {
				facts.Host.CPUs = 0
				return true
			},
			rule: "host-cpus",
		},
		{
			name: "a lockfile the repository pins and the document omits",
			mutate: func(facts *Facts) bool {
				facts.Lockfiles = facts.Lockfiles[1:]
				return true
			},
			rule: "lockfile-missing",
		},
		{
			name: "a lockfile without its digest",
			mutate: func(facts *Facts) bool {
				facts.Lockfiles[0].SHA256 = "not-a-digest"
				return true
			},
			rule: "lockfile-digest",
		},
		{
			name: "a lockfile with nothing that installs from it",
			mutate: func(facts *Facts) bool {
				facts.Lockfiles[1].Installs = ""
				return true
			},
			rule: "lockfile-incomplete",
		},
		{
			name: "a command that installs outside the lockfiles",
			mutate: func(facts *Facts) bool {
				command(facts, "web-modules").Command = "npm install --prefix web"
				return true
			},
			rule: "lockfile-bypassed",
		},
		{
			name: "a command the phase names and the document omits",
			mutate: func(facts *Facts) bool {
				kept := facts.Commands[:0]
				for _, entry := range facts.Commands {
					if entry.Key != "image-smoke" {
						kept = append(kept, entry)
					}
				}
				facts.Commands = kept
				return true
			},
			rule: "command-missing",
		},
		{
			name: "the same command recorded twice",
			mutate: func(facts *Facts) bool {
				facts.Commands = append(facts.Commands, facts.Commands[0])
				return true
			},
			rule: "command-duplicate",
		},
		{
			name: "the commands out of the order the phase names them",
			mutate: func(facts *Facts) bool {
				facts.Commands[0], facts.Commands[1] = facts.Commands[1], facts.Commands[0]
				return true
			},
			rule: "command-order",
		},
		{
			name: "a command that ran once",
			mutate: func(facts *Facts) bool {
				entry := command(facts, "verify")
				entry.Runs = entry.Runs[:1]
				return true
			},
			rule: "command-twice",
		},
		{
			name: "a command recorded with no command line",
			mutate: func(facts *Facts) bool {
				command(facts, "database").Command = " "
				return true
			},
			rule: "command-empty",
		},
		{
			name: "a run that was red",
			mutate: func(facts *Facts) bool {
				command(facts, "verify").Runs[1].ExitCode = 1
				return true
			},
			rule: "command-red",
		},
		{
			name: "a run with no duration, which is how a run that never happened is written",
			mutate: func(facts *Facts) bool {
				command(facts, "go-modules").Runs[0].Seconds = nil
				return true
			},
			rule: "command-timing",
		},
		{
			name: "a run with a duration no clock ever produced",
			mutate: func(facts *Facts) bool {
				command(facts, "verify").Runs[0].Seconds = seconds(-1)
				return true
			},
			rule: "command-timing",
		},
		{
			name: "a run that did not start from a clean tree",
			mutate: func(facts *Facts) bool {
				facts.Checkout.Clean = false
				return true
			},
			rule: "checkout-dirty",
		},
		{
			name: "a local file the commit tracks",
			mutate: func(facts *Facts) bool {
				facts.Checkout.LocalTrackedFiles = 1
				return true
			},
			rule: "local-tracked",
		},
		{
			name: "a generated path that is not the checklist",
			mutate: func(facts *Facts) bool {
				facts.Checkout.Generated = "docs/OTHER.md"
				return true
			},
			rule: "checkout-generated",
		},
		{
			name: "a run that left the tree holding more than the document",
			mutate: func(facts *Facts) bool {
				facts.Checkout.Dirty = append(facts.Checkout.Dirty, "web/node_modules")
				return true
			},
			rule: "tree-after",
		},
		{
			name: "a repository fsck complained about",
			mutate: func(facts *Facts) bool {
				facts.Repository.Fsck = "errors"
				facts.Repository.FsckErrors = 2
				return true
			},
			rule: "fsck",
		},
		{
			name: "a commit that tracks nothing",
			mutate: func(facts *Facts) bool {
				facts.Repository.TrackedFiles = 0
				return true
			},
			rule: "repository-empty",
		},
		{
			name: "an image nobody can identify",
			mutate: func(facts *Facts) bool {
				facts.Image.ID = ""
				return true
			},
			rule: "image-unidentified",
		},
		{
			name: "an image with a digest that is not one",
			mutate: func(facts *Facts) bool {
				facts.Image.Digest = "latest"
				return true
			},
			rule: "image-digest",
		},
		{
			name: "an image with no smoke recorded",
			mutate: func(facts *Facts) bool {
				facts.Image.Smoke = ""
				return true
			},
			rule: "image-smoke-absent",
		},
		{
			name: "a smoke that did not pass",
			mutate: func(facts *Facts) bool {
				facts.Image.SmokeOK = false
				return true
			},
			rule: "image-smoke-red",
		},
		{
			name: "an image with no size",
			mutate: func(facts *Facts) bool {
				facts.Image.SizeBytes = 0
				return true
			},
			rule: "image-size",
		},
		{
			name: "no release gate recorded",
			mutate: func(facts *Facts) bool {
				facts.Governance.Command = ""
				return true
			},
			rule: "governance-command",
		},
		{
			name: "a gate reported as refusing with nothing named",
			mutate: func(facts *Facts) bool {
				facts.Governance.Open = nil
				return true
			},
			rule: "governance-open",
		},
		{
			name: "a pending step recorded with no name",
			mutate: func(facts *Facts) bool {
				facts.Governance.Pending = []string{" "}
				return true
			},
			rule: "governance-pending",
		},
		{
			name: "a limitation that states nothing",
			mutate: func(facts *Facts) bool {
				facts.Limits = append(facts.Limits, "")
				return true
			},
			rule: "limit-empty",
		},
		{
			name: "a checklist that admits nothing",
			mutate: func(facts *Facts) bool {
				facts.Limits = facts.Limits[:1]
				return true
			},
			rule: "limits",
		},
		{
			name: "a real address inside the document",
			mutate: func(facts *Facts) bool {
				facts.Limits[0] = "A verificação foi pedida por ana.silva@corp.example-business.com"
				return true
			},
			rule: "checklist-pii",
		},
		{
			name: "an overlay the document does not mention where it describes the tree",
			mutate: func(facts *Facts) bool {
				facts.Checkout.Overlay = []OverlayEntry{{Path: "Makefile", SHA256: digest}}
				facts.Checkout.Dirty = []string{documentPath, "Makefile"}
				return true
			},
			rule: "overlay-undeclared",
		},
		{
			name: "an overlaid file with no digest",
			mutate: func(facts *Facts) bool {
				facts.Checkout.Kind += " com sobreposição"
				facts.Checkout.Overlay = []OverlayEntry{{Path: "Makefile"}}
				facts.Checkout.Dirty = []string{documentPath, "Makefile"}
				return true
			},
			rule: "overlay-incomplete",
		},
		{
			name: "a product file copied over the commit",
			mutate: func(facts *Facts) bool {
				facts.Checkout.Kind += " com sobreposição"
				facts.Checkout.Overlay = []OverlayEntry{{Path: "internal/platform/logging/logging.go", SHA256: digest}}
				facts.Checkout.Dirty = []string{documentPath, "internal/platform/logging/logging.go"}
				return true
			},
			rule: "overlay-scope",
		},
		{
			name: "a frontend source copied over the commit",
			mutate: func(facts *Facts) bool {
				facts.Checkout.Kind += " com sobreposição"
				facts.Checkout.Overlay = []OverlayEntry{{Path: "web/src/main.ts", SHA256: digest}}
				facts.Checkout.Dirty = []string{documentPath, "web/src/main.ts"}
				return true
			},
			rule: "overlay-scope",
		},
		{
			name: "an overlaid file whose digest is not the one in the tree",
			mutate: func(facts *Facts) bool {
				facts.Checkout.Kind += " com sobreposição"
				facts.Checkout.Overlay = []OverlayEntry{{Path: "docs/PRIVACY_AUDIT.md", SHA256: digest}}
				facts.Checkout.Dirty = []string{documentPath, "docs/PRIVACY_AUDIT.md"}
				return true
			},
			rule: "overlay-stale",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			facts := fixture(t)
			if !testCase.mutate(&facts) {
				t.Fatal("the mutation found nothing to change")
			}
			before, _ := json.Marshal(fixture(t))
			after, _ := json.Marshal(facts)
			if bytes.Equal(before, after) {
				t.Fatal("the mutation did not change the facts")
			}
			violations := audit(t, facts)
			if !hasRule(violations, testCase.rule) {
				t.Fatalf("the mutation was not refused by %s; the rules that fired were %v",
					testCase.rule, rulesOf(violations))
			}
		})
	}
}

// TestADeclaredOverlayWithItsDigestIsAccepted is the control for the overlay
// rules. The tool that performs the verification is not inside the commit it
// verifies, so a run that copies it in and declares it, with the digest the tree
// holds, has to pass — otherwise the four mutations below would be refused for
// the wrong reason.
func TestADeclaredOverlayWithItsDigestIsAccepted(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repositoryRoot, "docs/PRIVACY_AUDIT.md"))
	if err != nil {
		t.Fatalf("this control overlays the committed document, which is part of the tree: %v", err)
	}
	facts := fixture(t)
	facts.Checkout.Kind += " com sobreposição"
	facts.Checkout.Overlay = []OverlayEntry{{Path: "docs/PRIVACY_AUDIT.md", SHA256: sha256Of(raw)}}
	facts.Checkout.Dirty = []string{documentPath, "docs/PRIVACY_AUDIT.md"}
	if violations := audit(t, facts); len(violations) > 0 {
		t.Fatalf("a declared overlay with the digest of the tree is refused: %s", violations)
	}
}

// TestAPageOverTheCommitIsAdmitted is the other half of the overlay control, and
// it exists because the handoff task (P20-T08) delivers the root README: the
// overlay carries the instrumentation, the evidence, the Makefile and the prose
// of the repository, and refuses implementation. A run that could not carry its
// own page would refuse to verify the tree being published.
func TestAPageOverTheCommitIsAdmitted(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repositoryRoot, "README.md"))
	if err != nil {
		t.Fatalf("this control reads the root page of the commit, which is part of the tree: %v", err)
	}
	facts := fixture(t)
	facts.Checkout.Kind += " com sobreposição"
	facts.Checkout.Overlay = []OverlayEntry{{Path: "README.md", SHA256: sha256Of(raw)}}
	facts.Checkout.Dirty = []string{documentPath, "README.md"}
	if violations := audit(t, facts); len(violations) > 0 {
		t.Fatalf("the page of the repository carried over the commit is refused: %s", violations)
	}
}

// TestTheDeliveredChecklistIsJudgedAsItIs runs the gate over the document that
// ships. A tool whose tests only judge a fixture can be green while the
// committed document is stale, and this is the assertion that closes that gap.
func TestTheDeliveredChecklistIsJudgedAsItIs(t *testing.T) {
	path := filepath.Join(repositoryRoot, documentPath)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the checklist is a committed document and this control judges it as it is: %v", err)
	}
	document, err := ReadDocument(path)
	if err != nil {
		t.Fatalf("reading the delivered checklist: %v", err)
	}
	if violations := Check(repositoryRoot, document); len(violations) > 0 {
		t.Fatalf("the delivered checklist is refused: %s", violations)
	}
	if len(document.Facts.Commands) < len(requiredCommands) {
		t.Errorf("the delivered checklist records %d command(s) and the phase names %d",
			len(document.Facts.Commands), len(requiredCommands))
	}
}

// TestThePublicSurfaceIsTheDocumentItself covers the two ways the block arrives
// broken: no block at all, a block that never closes, and a block with a field
// the tool cannot judge.
func TestThePublicSurfaceIsTheDocumentItself(t *testing.T) {
	directory := t.TempDir()
	shapes := map[string]string{
		"no-block.md":      "# Checklist\n\nprosa sem bloco\n",
		"unclosed.md":      "# Checklist\n\n" + registerFence + "\n{\"version\": 1}\n",
		"unknown-field.md": "# Checklist\n\n" + registerFence + "\n{\"version\": 1, \"verified_by\": \"eu\"}\n```\n",
	}
	for name, body := range shapes {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		if _, err := ReadDocument(path); err == nil {
			t.Errorf("%s was read as a checklist", name)
		}
	}
}

// TestRenderRefusesToClaimWhatItDidNotMeasure pins the property that makes the
// document trustworthy: the prose is written from the facts, so a run whose
// command list is empty cannot render a sentence saying every command passed
// twice.
func TestRenderRefusesToClaimWhatItDidNotMeasure(t *testing.T) {
	facts := fixture(t)
	facts.Commands = nil
	rendered, err := Render(facts)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if strings.Contains(rendered, "duas vezes cada") && !strings.Contains(rendered, "Execuções") {
		t.Fatal("the document claims a cadence of two runs with no command in it")
	}
	if violations := auditText(t, rendered); !hasRule(violations, "command-missing") {
		t.Fatalf("a document with no command was accepted: %v", rulesOf(violations))
	}
}
