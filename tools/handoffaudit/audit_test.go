package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testRoot is the repository the rules read, from the package directory.
const testRoot = "../.."

// testDocument is the delivered document, read once.
func testDocument(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testRoot, "README.md"))
	if err != nil {
		t.Fatalf("read the delivered document: %v", err)
	}
	return string(raw)
}

// factsOf builds the facts of a document text against the real repository.
func factsOf(t *testing.T, text string) *facts {
	t.Helper()
	loaded, err := newFacts(testRoot, parseDocument("README.md", text))
	if err != nil {
		t.Fatalf("load the facts: %v", err)
	}
	return loaded
}

// change is one textual mutation.
type change struct{ old, new string }

// edit applies each change and fails when its anchor does not exist: a mutation
// that silently does nothing would make its rule look sound.
func edit(t *testing.T, text string, changes ...change) string {
	t.Helper()
	for _, candidate := range changes {
		if !strings.Contains(text, candidate.old) {
			t.Fatalf("mutation anchor %q is not in the document", candidate.old)
		}
		text = strings.ReplaceAll(text, candidate.old, candidate.new)
	}
	return text
}

// ruleNames returns the names some violation carries.
func ruleNames(violations []violation) map[string]bool {
	names := map[string]bool{}
	for _, found := range violations {
		names[found.Rule] = true
	}
	return names
}

// TestTheDeliveredDocumentIsAccepted is the control: the README delivered by the
// task passes every rule as it is.
func TestTheDeliveredDocumentIsAccepted(t *testing.T) {
	found := audit(factsOf(t, testDocument(t)))
	if len(found) > 0 {
		for _, candidate := range found {
			t.Errorf("the delivered document is refused: %s: %s", candidate.Rule, candidate.Detail)
		}
	}
}

// mutations is one falsification per rule: each one is a real edit of the
// delivered document that the named rule has to refuse.
func mutations() []struct {
	name  string
	rule  string
	apply func(*testing.T, string) string
} {
	return []struct {
		name  string
		rule  string
		apply func(*testing.T, string) string
	}{
		{
			name: "a section the phase names, gone",
			rule: "section-missing",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{"## Operação", "## Como operar"})
			},
		},
		{
			name: "a command the reader cannot run",
			rule: "command-unknown",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{"`make image-scan`", "`make image-scans`"})
			},
		},
		{
			name: "the quickstart marker renamed",
			rule: "quickstart-markers",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{quickstartBegin, "<!-- quickstart:start -->"})
			},
		},
		{
			name: "the serve marker renamed",
			rule: "serve-markers",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{serveBegin, "<!-- serve:start -->"})
			},
		},
		{
			name: "the smoke dropped from the walkthrough",
			rule: "quickstart-command-missing",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{"\nmake image-verify\n", "\n"})
			},
		},
		{
			name: "the walkthrough assuming a variable nobody set",
			rule: "quickstart-export-missing",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{"export ARENA_CURSOR_SECRET='dev-cursor-secret-0123456789abcdef'\n", ""})
			},
		},
		{
			name: "the serve block that starts nothing",
			rule: "serve-command-missing",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{"go run ./cmd/arena server", "go run ./cmd/arena"})
			},
		},
		{
			name: "a subcommand the binary does not have",
			rule: "subcommand-unknown",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{"`arena projections rebuild`", "`arena projection rebuild`"})
			},
		},
		{
			name: "a subcommand an operator needs, unmentioned",
			rule: "subcommand-missing",
			apply: func(t *testing.T, text string) string {
				return edit(t, text,
					change{"| `arena worker` | consome os jobs duráveis até receber `SIGTERM`/`SIGINT` |\n", ""},
					change{"`arena server` e `arena worker` compartilham", "`arena server` e o processo da fila compartilham"},
				)
			},
		},
		{
			name: "a variable outside the environment template",
			rule: "env-unknown",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{"| `ARENA_LOG_LEVEL` |", "| `ARENA_LOG_VERBOSITY` |"})
			},
		},
		{
			name: "a variable a developer needs, unnamed",
			rule: "env-missing",
			apply: func(t *testing.T, text string) string {
				return edit(t, text,
					change{"| `ARENA_ASSETS_DIR` | `web/dist` |", "| `ARENA_ASSETS_PATH` | `web/dist` |"},
					change{"o que `ARENA_ASSETS_DIR` aponta por padrão", "o que o diretório de assets aponta por padrão"},
					change{"ARENA_ENV=development ARENA_ASSETS_DIR=web/dist", "ARENA_ENV=development"},
				)
			},
		},
		{
			name: "a path the reader cannot open",
			rule: "path-missing",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{"docs/RUNBOOKS.md", "docs/RUNBOOK.md"})
			},
		},
		{
			name: "the handoff without its evidence",
			rule: "evidence-missing",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{"[docs/GOVERNANCE.md](docs/GOVERNANCE.md)", "[o log de decisões](docs/DECISIONS.md)"})
			},
		},
		{
			name: "a surface the smoke needs, unnamed",
			rule: "surface-missing",
			apply: func(t *testing.T, text string) string {
				return edit(t, text, change{"/health/live", "/health/alive"})
			},
		},
		{
			name: "a real address in the document",
			rule: "address-not-reserved",
			apply: func(t *testing.T, text string) string {
				return text + "\nDúvidas: ana.silva@corp-exemplo.com\n"
			},
		},
		{
			name: "a credential written in the open",
			rule: "secret-shaped",
			apply: func(t *testing.T, text string) string {
				return text + "\n```\nexport ARENA_STRIPE_SECRET_KEY=sk_live_51AbCdEfGhIjKlMnOp\n```\n"
			},
		},
	}
}

// TestOneMutationPerRule runs every mutation and requires the rule it names to
// refuse it, and requires the mapping to be complete in both directions: a rule
// with no mutation is a rule nobody has falsified, and a mutation naming a rule
// that does not exist is a test of nothing.
func TestOneMutationPerRule(t *testing.T) {
	document := testDocument(t)
	covered := map[string]bool{}
	for _, candidate := range mutations() {
		covered[candidate.rule] = true
		t.Run(candidate.name, func(t *testing.T) {
			mutated := candidate.apply(t, document)
			if mutated == document {
				t.Fatal("the mutation left the document unchanged")
			}
			if names := ruleNames(audit(factsOf(t, mutated))); !names[candidate.rule] {
				t.Fatalf("the mutation was accepted: %s did not fire (fired: %v)", candidate.rule, names)
			}
		})
	}
	declared := map[string]bool{}
	for _, candidate := range catalogue {
		declared[candidate.name] = true
		if !covered[candidate.name] {
			t.Errorf("rule %q has no mutation: nobody has falsified it", candidate.name)
		}
	}
	for name := range covered {
		if !declared[name] {
			t.Errorf("a mutation names rule %q, which is not in the catalogue", name)
		}
	}
}

// TestFamiliesAndIgnoredPathsAreJudged is the two-sided control of the rules
// that could be dismissed as noise. A family declared with a wildcard is
// honest when the template has a member of it and a hole when it does not, and
// a path a fresh checkout does not hold is fine when the tree ignores it —
// `web/generated` is a build output — and a violation when nothing does.
func TestFamiliesAndIgnoredPathsAreJudged(t *testing.T) {
	document := testDocument(t)

	accepted := edit(t, document, change{
		"| `ARENA_LOG_LEVEL` | `info` | `debug`, `info`, `warn` ou `error` |\n",
		"| `ARENA_LOG_LEVEL` | `info` | `debug`, `info`, `warn` ou `error` |\n| `ARENA_BILLING_*` | — | a família inteira |\n| `web/generated` | — | a árvore ignora, e o rule não reclama |\n",
	})
	if names := ruleNames(audit(factsOf(t, accepted))); names["env-unknown"] || names["path-missing"] {
		t.Fatalf("a declared family or an ignored output was refused: %v", names)
	}

	for _, refused := range []struct {
		name string
		rule string
		new  string
	}{
		{
			name: "a family with no member in the template",
			rule: "env-unknown",
			new:  "| `ARENA_LOG_LEVEL` | `info` | `debug` |\n| `ARENA_NOPE_*` | — | a família que ninguém declarou |\n",
		},
		{
			name: "a path the tree does not ignore",
			rule: "path-missing",
			new:  "| `ARENA_LOG_LEVEL` | `info` | `web/out` e mais |\n",
		},
	} {
		t.Run(refused.name, func(t *testing.T) {
			mutated := edit(t, document, change{
				"| `ARENA_LOG_LEVEL` | `info` | `debug`, `info`, `warn` ou `error` |\n",
				refused.new,
			})
			if names := ruleNames(audit(factsOf(t, mutated))); !names[refused.rule] {
				t.Fatalf("%s was accepted (fired: %v)", refused.name, names)
			}
		})
	}
}

// TestIgnoredOutputsAreJudgedInACleanCheckout is the regression of the defect the
// first walkthrough found. The tree ignores its build outputs with directory
// patterns (`/web/dist/`), and git only answers "ignored" for a directory that
// exists: in a clean checkout, where it does not exist yet, the bare path is
// refused and the path a reader would open is not. The rule has to ask the
// second question, and this holds it to that against a real worktree at the
// commit.
func cleanCheckout(t *testing.T) string {
	t.Helper()
	sandbox, err := os.MkdirTemp("", "arena-handoff-test-")
	if err != nil {
		t.Fatalf("create the sandbox: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sandbox) })
	checkout := filepath.Join(sandbox, "checkout")

	add := exec.Command("git", "worktree", "add", "--detach", checkout, "HEAD")
	add.Dir = testRoot
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("create the worktree: %v: %s", err, output)
	}
	t.Cleanup(func() {
		remove := exec.Command("git", "worktree", "remove", "--force", checkout)
		remove.Dir = testRoot
		_ = remove.Run()
	})
	return checkout
}

func TestIgnoredOutputsAreJudgedInACleanCheckout(t *testing.T) {
	checkout := cleanCheckout(t)
	loaded, err := newFacts(checkout, parseDocument("README.md", "veja `web/dist`, `web/generated` e `.env`; e `web/out`, que ninguém ignora\n"))
	if err != nil {
		t.Fatalf("load the facts: %v", err)
	}
	if loaded.exists("web/dist") {
		t.Fatal("the clean checkout should not hold the build output")
	}
	if !loaded.ignored("web/dist") {
		t.Fatal("the build output of a clean checkout is not recognized as ignored")
	}

	found := rulePaths(loaded)
	if len(found) != 1 || !strings.Contains(found[0].Detail, "web/out") {
		t.Fatalf("the rule judged the ignored outputs instead of the one nobody ignores: %+v", found)
	}
}

// TestPorcelainPathsHoldsTheFirstColumn is the regression of the second defect
// the walkthrough found: the status output was trimmed as if it were a sentence,
// so the first line lost its status column and ` M Makefile` became `akefile`,
// and the Makefile — the file that declares the targets the document names — was
// never copied over the checkout.
func TestPorcelainPathsHoldsTheFirstColumn(t *testing.T) {
	status := strings.Join([]string{
		" M Makefile",
		"?? tools/handoffaudit/main.go",
		"R  docs/OLD.md -> docs/NEW.md",
		" M .local/PROGRESS.md",
		"",
	}, "\n")
	got := porcelainPaths(status)
	want := []string{"Makefile", "docs/NEW.md", "tools/handoffaudit/main.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the status was read as %v, and it says %v", got, want)
	}
}

// TestATrackedArtifactIsAPathOfTheRepository is the regression of the third
// defect, and the one the verification of the release produced: when the
// checklist is already committed, the run that regenerates it removes the
// previous document from its own checkout before measuring — it is that run's
// output — and the README, which cites the artifact, would be refused in exactly
// that checkout. A path a commit holds is a path of the repository, whichever
// working tree is looking at it.
func TestATrackedArtifactIsAPathOfTheRepository(t *testing.T) {
	checkout := cleanCheckout(t)
	artifact := "docs/RELEASE_CHECKLIST.md"
	if err := os.Remove(filepath.Join(checkout, artifact)); err != nil {
		t.Fatalf("this regression is about a committed artifact, and %s is part of the tree: %v", artifact, err)
	}
	loaded, err := newFacts(checkout, parseDocument("README.md", "a evidência está em `"+artifact+"`\n"))
	if err != nil {
		t.Fatalf("load the facts: %v", err)
	}
	if loaded.exists(artifact) {
		t.Fatal("the artifact should be gone from this checkout")
	}
	if !loaded.tracked(artifact) {
		t.Fatal("a file the commit holds is not recognized as tracked")
	}
	if found := rulePaths(loaded); len(found) > 0 {
		t.Fatalf("the committed artifact was refused in a checkout that had it removed: %+v", found)
	}
}

// TestTheOverlayNeverCarriesTheLocalPlan holds the never-ships rule of this
// repository: the local directory is not copied into anything, not even by the
// walkthrough that copies the working tree.
func TestTheOverlayNeverCarriesTheLocalPlan(t *testing.T) {
	run := &walkthrough{root: testRoot, file: "README.md"}
	files, err := run.changedFiles()
	if err != nil {
		t.Fatalf("list the changes: %v", err)
	}
	seenDocument := false
	for _, candidate := range files {
		if candidate == ".local" || strings.HasPrefix(candidate, ".local/") {
			t.Fatalf("the overlay carries %s", candidate)
		}
		if candidate == "README.md" {
			seenDocument = true
		}
	}
	if !seenDocument {
		t.Fatal("the overlay does not carry the document it follows")
	}
}

// TestTheDocumentIsNotRewritten is the property every audit of this repository
// holds: the tool judges, and the correction belongs to whoever changed the code
// or wrote the page.
func TestTheDocumentIsNotRewritten(t *testing.T) {
	path := filepath.Join(testRoot, "README.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the document: %v", err)
	}
	audit(factsOf(t, string(before)))
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the document again: %v", err)
	}
	if digestOf(before) != digestOf(after) {
		t.Fatalf("the audit rewrote the document: %s -> %s", digestOf(before), digestOf(after))
	}
}

// TestWalkthroughRefusesADocumentWithoutBlocks covers the walkthrough's guard:
// a document that declares no block to follow is refused before anything is
// checked out or started.
func TestWalkthroughRefusesADocumentWithoutBlocks(t *testing.T) {
	document := edit(t, testDocument(t),
		change{quickstartBegin + "\n", ""},
		change{serveBegin + "\n", ""},
	)
	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatalf("write the mutated document: %v", err)
	}
	err := runWalkthrough(testRoot, path, os.Stdout, os.Stderr)
	if err == nil {
		t.Fatal("the walkthrough accepted a document that declares no block")
	}
	if !strings.Contains(err.Error(), "no quickstart or serve block") {
		t.Fatalf("the walkthrough refused for the wrong reason: %v", err)
	}
}
