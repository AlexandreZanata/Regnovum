// Tests of the CI audit (P19-T08).
//
// Two properties have to hold, and a test with only the first would be
// worthless:
//
//   - the workflow set this repository delivers passes every rule (the
//     baseline, which is also audited as a file on disk);
//   - each rule refuses a workflow that breaks exactly it. The mutation cases
//     below mutate one thing in a clean fixture and require the finding, so a
//     rule that stopped working fails here instead of certifying the CI;
//     and the mutation is applied with a replacement that fails the test if
//     the text it targets is gone, so a case cannot pass by mutating nothing.
package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fixtureMakefile is the part of the Makefile the audit reads: the aggregate's
// prerequisites, and the image scan whose parameters the CI has to match. Only
// the invoked targets are declared — the rule checks invocations, not the whole
// file — and the aggregate lists the real foundation gates.
const fixtureMakefile = `IMAGE ?= goyim-arena:local

verify: fmt-check lint audit-complexity audit-deadcode audit-errors audit-provenance audit-tests audit-diff audit-deps audit-mutations audit-coverage generate-check test-unit test-integration test-race test-migration test-contract test-security test-web typecheck audit-web audit-i18n
	@echo "verify: ok"

fmt-check:
	@gofmt -l .

lint:
	@echo "lint: ok"

audit-complexity:
	@echo "audit-complexity: ok"

audit-deadcode:
	@echo "audit-deadcode: ok"

audit-errors:
	@echo "audit-errors: ok"

audit-provenance:
	@echo "audit-provenance: ok"

audit-tests:
	@echo "audit-tests: ok"

audit-diff:
	@echo "audit-diff: ok"

audit-deps:
	@echo "audit-deps: ok"

audit-mutations:
	@echo "audit-mutations: ok"

audit-coverage:
	@echo "audit-coverage: ok"

generate-check:
	@echo "generate-check: ok"

test-unit:
	@echo "test-unit: ok"

test-integration:
	@echo "test-integration: ok"

test-race:
	@echo "test-race: ok"

test-migration:
	@echo "test-migration: ok"

test-contract:
	@echo "test-contract: ok"

test-security:
	@echo "test-security: ok"

test-web:
	@echo "test-web: ok"

typecheck:
	@echo "typecheck: ok"

audit-web:
	@echo "audit-web: ok"

audit-i18n:
	@echo "audit-i18n: ok"

vuln:
	@govulncheck ./...

test-e2e:
	@echo "test-e2e: ok"

image-verify:
	@echo "image-verify: ok"

image-scan: image-build
	trivy image --scanners vuln --severity CRITICAL,HIGH --ignore-unfixed --exit-code 1 $(IMAGE)

caddy-verify:
	@echo "caddy-verify: ok"

compose-verify:
	@echo "compose-verify: ok"

backup-verify:
	@echo "backup-verify: ok"

deploy-verify:
	@echo "deploy-verify: ok"
`

// fixtureWorkflow is a complete, minimal workflow set: every gate the phase
// requires is wired, the foundation ones through the aggregate, the
// environment ones through their own jobs, the image scan through an action
// that asks the Makefile's question.
const fixtureWorkflow = `name: verify

on:
  push:
    branches: [main]
  pull_request:
    types: [opened, synchronize, reopened, ready_for_review]

permissions:
  contents: read

env:
  IMAGE: goyim-arena:verify

jobs:
  foundation:
    name: Foundation verification
    if: github.event_name != 'pull_request' || github.event.pull_request.draft == false
    runs-on: ubuntu-latest
    timeout-minutes: 30
    services:
      postgres:
        image: postgres:18.4
        ports:
          - 54329:5432
    env:
      ARENA_DATABASE_URL: postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable
    steps:
      - name: Checkout repository
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1

      - name: Run every foundation gate
        run: make verify

  browser:
    name: Browser journeys
    if: github.event_name != 'pull_request' || github.event.pull_request.draft == false
    runs-on: ubuntu-latest
    timeout-minutes: 30
    services:
      postgres:
        image: postgres:18.4
        ports:
          - 54329:5432
    env:
      ARENA_DATABASE_URL: postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable
    steps:
      - name: Run the browser journeys
        run: make test-e2e

  scanner:
    name: Dependency vulnerabilities
    if: github.event_name != 'pull_request' || github.event.pull_request.draft == false
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - name: Scan the Go dependencies
        run: make vuln

  production:
    name: Production image and behaviours
    if: github.event_name != 'pull_request' || github.event.pull_request.draft == false
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - name: Build and audit the production image
        run: make image-verify

      - name: Scan the production image
        uses: aquasecurity/trivy-action@57a97c7e7821a5776cebc9bb87c984fa69cba8f1 # v0.35.0
        with:
          image-ref: ${{ env.IMAGE }}
          scanners: vuln
          severity: CRITICAL,HIGH
          ignore-unfixed: true
          exit-code: '1'

      - name: Verify the ingress
        run: make caddy-verify

      - name: Verify the topology
        run: make compose-verify

      - name: Verify backup and recovery
        run: make backup-verify

      - name: Verify deploy and rollback
        run: make deploy-verify
`

// writeFixture lays a workflow set out in a temporary root, the way the real
// files are laid out.
func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("prepare fixture: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("prepare fixture: %v", err)
		}
	}
	return root
}

// fixture is a mutable copy of the baseline.
func fixture() map[string]string {
	return map[string]string{
		".github/workflows/verify.yml": fixtureWorkflow,
		"Makefile":                     fixtureMakefile,
	}
}

// replaceInWorkflow applies a mutation to the fixture's workflow, and fails
// when the text it targets is gone: a mutation that did not apply would let the
// case pass without testing anything.
func replaceInWorkflow(t *testing.T, files map[string]string, old, new string) {
	t.Helper()
	const path = ".github/workflows/verify.yml"
	body := files[path]
	if !strings.Contains(body, old) {
		t.Fatalf("the fixture no longer contains %q, so this mutation no longer applies", old)
	}
	files[path] = strings.Replace(body, old, new, 1)
}

// auditFixture runs the audit over a fixture root.
func auditFixture(t *testing.T, files map[string]string) []Finding {
	t.Helper()
	report, err := Audit(writeFixture(t, files))
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	return report.Findings
}

// rulesOf lists the rules a finding set broke, without repetition and in a
// stable order, so a case can require exactly one.
func rulesOf(findings []Finding) []string {
	seen := map[string]bool{}
	var rules []string
	for _, finding := range findings {
		if !seen[finding.Rule] {
			seen[finding.Rule] = true
			rules = append(rules, finding.Rule)
		}
	}
	sort.Strings(rules)
	return rules
}

// TestBaselinePassesEveryRule is the fixture's own verdict. Every mutation case
// below depends on it: without it, a case could pass because the baseline was
// already broken.
func TestBaselinePassesEveryRule(t *testing.T) {
	if findings := auditFixture(t, fixture()); len(findings) != 0 {
		for _, finding := range findings {
			t.Errorf("baseline: %s", finding)
		}
		t.Fatalf("the baseline is not clean, so no mutation case means anything")
	}
}

// TestEachRuleRefusesItsMutation is the falsification of every rule: one
// mutation, one expected rule, and no other rule broken by it.
func TestEachRuleRefusesItsMutation(t *testing.T) {
	cases := []struct {
		name string
		// rules is the exact set the mutation must break: naming it in full
		// keeps a mutation that reaches further than its intent from passing
		// as if it reached exactly its intent.
		rules  []string
		mutate func(*testing.T, map[string]string)
	}{
		{
			name:  "an action pinned to a tag instead of a commit",
			rules: []string{RuleActionsPinned},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1", "actions/checkout@v7")
			},
		},
		{
			name:  "an action pinned to a commit with no version comment",
			rules: []string{RuleActionsPinned},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1", "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1")
			},
		},
		{
			name:  "a workflow that declares no permissions",
			rules: []string{RulePermissions},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "\npermissions:\n  contents: read\n", "\n")
			},
		},
		{
			name:  "a workflow that grants write to the token",
			rules: []string{RulePermissions},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "  contents: read\n\nenv:", "  contents: write\n\nenv:")
			},
		},
		{
			name:  "a workflow that sets permissions to read-all",
			rules: []string{RulePermissions},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "permissions:\n  contents: read\n", "permissions: read-all\n")
			},
		},
		{
			name:  "a gate whose failure is discarded by || true",
			rules: []string{RuleMasking},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "run: make verify", "run: make verify || true")
			},
		},
		{
			name:  "a gate that may fail the step and not the job",
			rules: []string{RuleMasking},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "run: make test-e2e\n", "run: make test-e2e\n        continue-on-error: true\n")
			},
		},
		{
			name:  "a gate that runs regardless of the steps before it",
			rules: []string{RuleMasking},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "      - name: Scan the Go dependencies\n        run: make vuln", "      - name: Scan the Go dependencies\n        if: always()\n        run: make vuln")
			},
		},
		{
			name: "a workflow that invokes a target the Makefile does not declare",
			// The typo is also a gate nothing runs any more, which is the truth
			// about a mistyped target and the reason both rules report it.
			rules: []string{RuleGatesWired, RuleTargetsExist},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "run: make vuln", "run: make vulnn")
			},
		},
		{
			name:  "a gate wired to no job at all",
			rules: []string{RuleGatesWired},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "run: make test-e2e\n", "run: echo \"the journeys are coming\"\n")
			},
		},
		{
			name:  "the aggregate gate replaced by one of its parts",
			rules: []string{RuleGatesWired},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "run: make verify", "run: make test-unit")
			},
		},
		{
			name:  "a scan in CI weaker than the gate the operator runs",
			rules: []string{RuleGatesWired},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "severity: CRITICAL,HIGH", "severity: CRITICAL")
			},
		},
		{
			name:  "a scan of an image the run does not build",
			rules: []string{RuleGatesWired},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "image-ref: ${{ env.IMAGE }}", "image-ref: postgres:18.6-alpine")
			},
		},
		{
			name:  "a job that reaches PostgreSQL and declares no service",
			rules: []string{RuleDatabase},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "    services:\n      postgres:\n        image: postgres:18.4\n        ports:\n          - 54329:5432\n    env:\n      ARENA_DATABASE_URL: postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable\n    steps:\n      - name: Run the browser journeys", "    env:\n      ARENA_DATABASE_URL: postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable\n    steps:\n      - name: Run the browser journeys")
			},
		},
		{
			name:  "a job whose database address names a port the service does not publish",
			rules: []string{RuleDatabase},
			mutate: func(t *testing.T, files map[string]string) {
				// The address the journeys reach and the port the service
				// publishes have to be the same port; a job that connects to
				// nothing would fail at run time with a message nobody reads.
				replaceInWorkflow(t, files, "ARENA_DATABASE_URL: postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable\n    steps:\n      - name: Run the browser journeys", "ARENA_DATABASE_URL: postgres://arena:arena-local-dev@127.0.0.1:54330/arena?sslmode=disable\n    steps:\n      - name: Run the browser journeys")
			},
		},
		{
			name:  "a job that does not skip draft pull requests",
			rules: []string{RuleDraftSkip},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "    if: github.event_name != 'pull_request' || github.event.pull_request.draft == false\n    runs-on: ubuntu-latest\n    timeout-minutes: 15", "    runs-on: ubuntu-latest\n    timeout-minutes: 15")
			},
		},
		{
			name:  "a trigger that would not start the verification on a ready pull request",
			rules: []string{RuleDraftSkip},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "types: [opened, synchronize, reopened, ready_for_review]", "types: [opened, synchronize, reopened]")
			},
		},
		{
			name:  "a trigger filtered by path",
			rules: []string{RuleDraftSkip},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "types: [opened, synchronize, reopened, ready_for_review]", "types: [opened, synchronize, reopened, ready_for_review]\n    paths: [docs/**]")
			},
		},
		{
			name:  "a trigger that runs with the base repository's context",
			rules: []string{RuleSurface},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "on:\n  push:", "on:\n  pull_request_target:\n  push:")
			},
		},
		{
			name:  "a workflow that reads a secret other than the run's token",
			rules: []string{RuleSurface},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "    env:\n      ARENA_DATABASE_URL:", "    env:\n      ARENA_PAYMENT_KEY: ${{ secrets.STRIPE_SECRET_KEY }}\n      ARENA_DATABASE_URL:")
			},
		},
		{
			name:  "a job with no timeout",
			rules: []string{RuleBudget},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "    name: Production image and behaviours\n    if: github.event_name != 'pull_request' || github.event.pull_request.draft == false\n    runs-on: ubuntu-latest\n    timeout-minutes: 30", "    name: Production image and behaviours\n    if: github.event_name != 'pull_request' || github.event.pull_request.draft == false\n    runs-on: ubuntu-latest")
			},
		},
		{
			name:  "a job that may outlive the phase's CI budget",
			rules: []string{RuleBudget},
			mutate: func(t *testing.T, files map[string]string) {
				replaceInWorkflow(t, files, "    name: Production image and behaviours\n    if: github.event_name != 'pull_request' || github.event.pull_request.draft == false\n    runs-on: ubuntu-latest\n    timeout-minutes: 30", "    name: Production image and behaviours\n    if: github.event_name != 'pull_request' || github.event.pull_request.draft == false\n    runs-on: ubuntu-latest\n    timeout-minutes: 45")
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			files := fixture()
			testCase.mutate(t, files)
			findings := auditFixture(t, files)
			if len(findings) == 0 {
				t.Fatalf("nothing was refused, and the rule that should have refused it is %s", strings.Join(testCase.rules, ", "))
			}
			rules := rulesOf(findings)
			if strings.Join(rules, ",") != strings.Join(testCase.rules, ",") {
				for _, finding := range findings {
					t.Errorf("finding: %s", finding)
				}
				t.Fatalf("the mutation broke %v; it should have broken %v", rules, testCase.rules)
			}
		})
	}
}

// TestDeliveredWorkflowsVerifyEveryGate audits the files this repository
// commits. The fixture above is a model; these are the ones that decide a
// merge.
func TestDeliveredWorkflowsVerifyEveryGate(t *testing.T) {
	root := filepath.Join("..", "..")
	report, err := Audit(root)
	if err != nil {
		t.Fatalf("Audit(%s): %v", root, err)
	}
	for _, finding := range report.Findings {
		t.Errorf("delivered workflow: %s", finding)
	}
	if len(report.Workflows) < 2 {
		t.Fatalf("read %d workflow(s); verify.yml and supply-chain.yml are both delivered", len(report.Workflows))
	}
	if report.Gates != len(requiredGates) {
		t.Fatalf("the audit reports %d gates and the table holds %d", report.Gates, len(requiredGates))
	}
}

func TestReleaseCadenceRejectsBypasses(t *testing.T) {
	base := map[string]string{
		".github/workflows/quick.yml": `name: quick
on:
  pull_request:
    types: [opened, synchronize, reopened, ready_for_review]
  push:
    branches: [main]
jobs:
  quick:
    name: Quick verification
    timeout-minutes: 12
    steps:
      - run: make quick-verify
`,
		".github/workflows/verify.yml": `name: verify
on:
  workflow_dispatch:
  push:
    tags: ['v*']
`,
		".github/workflows/supply-chain.yml": `name: supply-chain
on:
  workflow_dispatch:
  push:
    tags: ['v*']
`,
		"Makefile": "quick-verify:\n\t@true\n",
	}
	tests := []struct {
		name, file, old, replacement string
	}{
		{"quick missing synchronize", ".github/workflows/quick.yml", "opened, synchronize, reopened", "opened, reopened"},
		{"quick path filtered", ".github/workflows/quick.yml", "    types: [opened, synchronize, reopened, ready_for_review]", "    types: [opened, synchronize, reopened, ready_for_review]\n    paths: [docs/**]"},
		{"quick conditionally skipped", ".github/workflows/quick.yml", "    name: Quick verification", "    name: Quick verification\n    if: false"},
		{"quick no target", ".github/workflows/quick.yml", "make quick-verify", "echo green"},
		{"full on every PR", ".github/workflows/verify.yml", "  workflow_dispatch:", "  pull_request:\n  workflow_dispatch:"},
		{"full missing version tag", ".github/workflows/verify.yml", "tags: ['v*']", "tags: ['ignored']"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := make(map[string]string, len(base))
			for k, v := range base {
				files[k] = v
			}
			files[tc.file] = strings.Replace(files[tc.file], tc.old, tc.replacement, 1)
			report, err := Audit(writeFixture(t, files))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, finding := range report.Findings {
				found = found || finding.Rule == RuleCadence
			}
			if !found {
				t.Fatal("cadence bypass was not rejected")
			}
		})
	}
	t.Run("quick workflow removed", func(t *testing.T) {
		files := make(map[string]string, len(base)-1)
		for k, v := range base {
			if k != ".github/workflows/quick.yml" {
				files[k] = v
			}
		}
		report, err := Audit(writeFixture(t, files))
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range report.Findings {
			if finding.Rule == RuleCadence {
				return
			}
		}
		t.Fatal("missing quick workflow was not rejected")
	})
}

// TestReaderRefusesAFixtureItCannotTrust requires the reader to fail instead of
// guessing. A reader that misread a file would let every rule pass over it.
func TestReaderRefusesAFixtureItCannotTrust(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		expect string
	}{
		{
			name:   "a tab indents a line",
			body:   "jobs:\n\tfoundation:\n",
			expect: "tab",
		},
		{
			name:   "a value nests under a scalar",
			body:   "jobs:\n  foundation:\n    runs-on: ubuntu-latest\n      nested: value\n",
			expect: "nesting",
		},
		{
			name:   "a line opens no mapping entry",
			body:   "jobs:\n  foundation\n",
			expect: "opens no mapping",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseWorkflow("fixture.yml", []byte(testCase.body))
			if err == nil {
				t.Fatalf("the reader accepted a document it should have refused")
			}
			if !strings.Contains(err.Error(), testCase.expect) {
				t.Fatalf("the refusal reads %q and should explain %q", err, testCase.expect)
			}
		})
	}
}

// TestFullMatrixCadencePerLayer falsifies every layer: adding a merge-path
// trigger to the nightly, weekly, release or aggregate invocation blocks
// on full-matrix-cadence, while the manual definitions hold.
func TestFullMatrixCadencePerLayer(t *testing.T) {
	base := map[string]string{
		".github/workflows/nightly.yml": "name: nightly\n" + layerBody("quality-nightly"),
		".github/workflows/weekly.yml":  "name: weekly\n" + layerBody("quality-weekly"),
		".github/workflows/release.yml": "name: release\n" + layerBody("quality-certify"),
		"Makefile":                      "quality-nightly:\n\t@true\nquality-weekly:\n\t@true\nquality-certify:\n\t@true\nverify:\n\t@true\n",
	}
	t.Run("manual layers hold", func(t *testing.T) {
		report, err := Audit(writeFixture(t, base))
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range report.Findings {
			if finding.Rule == RuleFullMatrix {
				t.Fatalf("manual layer blocked: %s", finding)
			}
		}
	})

	triggers := []struct{ file, trigger string }{
		{".github/workflows/nightly.yml", "  pull_request:\n"},
		{".github/workflows/weekly.yml", "  push:\n    branches: [main]\n"},
		{".github/workflows/release.yml", "  pull_request:\n"},
	}
	for _, tc := range triggers {
		t.Run(tc.file+" blocks on the merge path", func(t *testing.T) {
			files := make(map[string]string, len(base))
			for k, v := range base {
				files[k] = v
			}
			files[tc.file] = strings.Replace(files[tc.file], "  workflow_dispatch:\n", "  workflow_dispatch:\n"+tc.trigger, 1)
			report, err := Audit(writeFixture(t, files))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, finding := range report.Findings {
				if finding.Rule == RuleFullMatrix && finding.Path == tc.file {
					found = true
				}
			}
			if !found {
				t.Fatalf("merge-path trigger on %s was not blocked", tc.file)
			}
		})
	}
}

func layerBody(target string) string {
	return `on:
  workflow_dispatch:
permissions:
  contents: read
jobs:
  layer:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - run: make ` + target + `
`
}
