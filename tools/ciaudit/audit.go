// Rules of the CI audit (P19-T08).
//
// Each rule is one way the complete verification could look present and not be:
// a gate wired to no job, a job that needs a database and declares none, a step
// that swallows its own failure, an action that can be moved under a reviewed
// workflow, a token that can write, a secret a run has no business reading, and
// a skip that reduces the gate instead of deferring it.
package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The name of each rule, as it appears in a finding. The names are stable
// because a finding is read as `path:line: rule: message`, and the rule is what
// a reviewer looks up.
const (
	RuleActionsPinned = "actions-pinned"
	RulePermissions   = "permissions-minimal"
	RuleMasking       = "failure-never-masked"
	RuleTargetsExist  = "make-targets-exist"
	RuleGatesWired    = "gates-wired"
	RuleDatabase      = "database-service"
	RuleDraftSkip     = "draft-skip-without-reduction"
	RuleCadence       = "release-only-cadence"
	RuleSurface       = "trigger-and-secret-surface"
	RuleBudget        = "job-budget"
	RuleFullMatrix    = "full-matrix-cadence"
)

// ciBudgetMinutes is the phase's CI budget: `.local/git-flow.sh` waits 1800
// seconds for a pull request's checks before it gives up, so no job may be able
// to outlive that wait.
const ciBudgetMinutes = 30

// gate is one piece of the release verification the plan requires: the subject
// a person reads, and the Makefile target that decides it.
type gate struct {
	Subject string
	Target  string
}

// requiredGates is the phase's list, read from `.local/phases/19-production-
// operations.md` and from `make verify`. Two kinds appear together because both
// are required and only one of them can be a prerequisite: the foundation gates
// run inside `make verify` (an environment with Go, Node and PostgreSQL), and
// the gates that need a Docker daemon, a browser or a scanner run as their own
// job, because a verification that silently dropped them would be the green
// tick this task exists to prevent.
//
// `test-load-smoke` is deliberately absent: it measures capacity against a
// prepared instance, not correctness, and the plan does not put it in the
// release gate (docs/CI.md).
var requiredGates = []gate{
	{"formatting", "fmt-check"},
	{"static analysis", "lint"},
	{"complexity, duplication and size", "audit-complexity"},
	{"dead code, placeholders and impossible paths", "audit-deadcode"},
	{"errors, contexts and resources", "audit-errors"},
	{"generated-artifact provenance", "audit-provenance"},
	{"test quality", "audit-tests"},
	{"production change evidence", "audit-diff"},
	{"dependency provenance", "audit-deps"},
	{"mutation testing of critical rules", "audit-mutations"},
	{"line coverage floors and diff", "audit-coverage"},
	{"generated-artifact drift", "generate-check"},
	{"unit tests", "test-unit"},
	{"PostgreSQL integration", "test-integration"},
	{"selected race detector", "test-race"},
	{"migrations", "test-migration"},
	{"OpenAPI contract", "test-contract"},
	{"security regressions", "test-security"},
	{"frontend build", "test-web"},
	{"strict type checking of the frontend", "typecheck"},
	{"frontend measurement", "audit-web"},
	{"interface language and direction", "audit-i18n"},
	{"browser journeys", "test-e2e"},
	{"dependency vulnerabilities", "vuln"},
	{"production image", "image-verify"},
	{"production image scan", "image-scan"},
	{"ingress configuration", "caddy-verify"},
	{"production topology", "compose-verify"},
	{"backup and point-in-time recovery", "backup-verify"},
	{"deploy and rollback", "deploy-verify"},
	{"aggregate verification", "verify"},
}

// databaseGates are the gates that reach a real PostgreSQL: the aggregate, the
// three that connect to it directly, the security regressions whose adapter
// packages are part of it, and the browser journeys whose harness provisions a
// database beside the configured one.
var databaseGates = []string{"verify", "test-integration", "test-race", "test-migration", "test-security", "test-e2e"}

// Finding is one reason the delivered workflows do not verify what the plan
// requires.
type Finding struct {
	Path    string
	Line    int
	Rule    string
	Message string
}

// String renders a finding as `path:line: rule: message`: the line a reviewer
// opens, and the rule that broke. A finding about the wiring of the file as a
// whole names the rule alone, because there is no line to open.
func (f Finding) String() string {
	location := f.Path
	if f.Line > 0 {
		location = fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	if location == "" {
		return fmt.Sprintf("%s: %s", f.Rule, f.Message)
	}
	return fmt.Sprintf("%s: %s: %s", location, f.Rule, f.Message)
}

// Report is the measurement: what was read, and what it broke.
type Report struct {
	Root      string
	Workflows []string
	Gates     int
	Findings  []Finding
}

// Audit reads the workflows and the Makefile under a root and returns every
// reason the CI does not enforce the phase's verification.
func Audit(root string) (Report, error) {
	workflows, err := loadWorkflows(root)
	if err != nil {
		return Report{}, err
	}
	makefile, err := readMakefile(root + "/Makefile")
	if err != nil {
		return Report{}, err
	}

	report := Report{Root: root, Gates: len(requiredGates)}
	for _, w := range workflows {
		report.Workflows = append(report.Workflows, w.Path)
	}

	report.Findings = append(report.Findings, auditActionsPinned(workflows)...)
	report.Findings = append(report.Findings, auditPermissions(workflows)...)
	report.Findings = append(report.Findings, auditMasking(workflows)...)
	report.Findings = append(report.Findings, auditTargetsExist(workflows, makefile)...)
	report.Findings = append(report.Findings, auditGatesWired(workflows, makefile)...)
	report.Findings = append(report.Findings, auditDatabaseService(workflows)...)
	report.Findings = append(report.Findings, auditDraftSkips(workflows)...)
	report.Findings = append(report.Findings, auditSurface(workflows)...)
	report.Findings = append(report.Findings, auditBudget(workflows)...)
	report.Findings = append(report.Findings, auditFullMatrixCadence(workflows)...)
	return report, nil
}

// fullMatrixTargets are the gates that run the complete verification:
// the P30 canonical tiers and the release aggregate. They execute
// integrally only in P45, so no workflow may trigger them on a pull
// request or a branch push — schedules, tags and manual runs are the
// only cadences that cannot surprise a merge.
var fullMatrixTargets = []string{"quality-nightly", "quality-weekly", "quality-certify", "verify"}

// releaseFiles are the workflows whose triggers the release-only-cadence
// rule already owns; this rule judges every other file, so the two rules
// never double-judge one trigger.
var releaseFiles = map[string]bool{
	".github/workflows/verify.yml":       true,
	".github/workflows/supply-chain.yml": true,
}

// auditFullMatrixCadence requires the complete verification to stay off
// the merge path: a workflow that invokes a full-matrix target on
// pull_request or push branches would run (or block) every merge with a
// suite P45 owns.
func auditFullMatrixCadence(workflows []workflow) []Finding {
	var findings []Finding
	for _, w := range workflows {
		invokes := false
		for _, e := range w.entries {
			if e.Key != "run" {
				continue
			}
			for _, target := range invokedTargets(e.Value) {
				if containsString(fullMatrixTargets, target) {
					invokes = true
				}
			}
		}
		if !invokes || releaseFiles[w.Path] {
			continue
		}
		if w.has("on.pull_request") {
			findings = append(findings, Finding{w.Path, 0, RuleFullMatrix,
				"invokes a full-matrix gate on pull_request: complete verification runs in P45, never on the merge path"})
		}
		if w.has("on.push.branches") {
			findings = append(findings, Finding{w.Path, 0, RuleFullMatrix,
				"invokes a full-matrix gate on branch push: complete verification runs in P45, never on the merge path"})
		}
	}
	return findings
}

// auditActionsPinned requires every third-party action to be a commit SHA with
// the tag it points to beside it. A tag can be moved under a workflow that
// already reviewed it; a SHA cannot.
func auditActionsPinned(workflows []workflow) []Finding {
	var findings []Finding
	for _, w := range workflows {
		for _, e := range w.usesEntries() {
			if strings.HasPrefix(e.Value, "./") {
				continue // an action in this repository is the code being reviewed
			}
			if !shaPinPattern.MatchString(e.Value) {
				findings = append(findings, Finding{w.Path, e.Line, RuleActionsPinned,
					fmt.Sprintf("uses %q, which is not pinned to a 40-character commit SHA", e.Value)})
				continue
			}
			if !versionCommentPattern.MatchString(e.Comment) {
				findings = append(findings, Finding{w.Path, e.Line, RuleActionsPinned,
					fmt.Sprintf("uses %q with no version comment beside the SHA; the tag it points to is what a reviewer checks", e.Value)})
			}
		}
	}
	return findings
}

var (
	shaPinPattern          = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)?@[0-9a-f]{40}$`)
	versionCommentPattern  = regexp.MustCompile(`^v?[0-9]`)
	secretReferencePattern = regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`)
)

// auditPermissions requires the token to be read-only. A verification workflow
// writes nothing; a scope this workflow does not name defaults to the
// repository setting, which is broader than any job here needs.
func auditPermissions(workflows []workflow) []Finding {
	var findings []Finding
	for _, w := range workflows {
		if !w.has("permissions") {
			findings = append(findings, Finding{w.Path, 0, RulePermissions,
				"declares no permissions; the default token is broader than any job here needs"})
		}
		for _, e := range w.entries {
			switch {
			case e.Key == "permissions":
				if e.Value != "" {
					findings = append(findings, Finding{w.Path, e.Line, RulePermissions,
						fmt.Sprintf("sets permissions to %q; name each scope and set it to read", e.Value)})
				}
			case strings.HasSuffix(parentPath(e.Path), "permissions"):
				if value := strings.ToLower(e.Value); value != "read" && value != "none" {
					findings = append(findings, Finding{w.Path, e.Line, RulePermissions,
						fmt.Sprintf("grants %s: %s to a verification workflow, which reads and never writes", e.Key, e.Value)})
				}
			}
		}
	}
	return findings
}

// maskingPatterns are the idioms that turn a failing step green. They are
// checked as text because the failure they cause is textual: a gate whose exit
// status was discarded is a gate that ran for nothing.
var maskingPatterns = []string{"|| true", "|| :", "|| exit 0", "&& exit 0", "set +e", "continue-on-error", "|| echo"}

// auditMasking requires every gate to be able to fail the job it runs in.
func auditMasking(workflows []workflow) []Finding {
	var findings []Finding
	for _, w := range workflows {
		for _, e := range w.entries {
			switch e.Key {
			case "continue-on-error":
				if !strings.EqualFold(e.Value, "false") {
					findings = append(findings, Finding{w.Path, e.Line, RuleMasking,
						fmt.Sprintf("continue-on-error: %s lets this step fail without failing the job", e.Value)})
				}
			case "run":
				for _, pattern := range maskingPatterns {
					if strings.Contains(e.Value, pattern) {
						findings = append(findings, Finding{w.Path, e.Line, RuleMasking,
							fmt.Sprintf("the run block contains %q, which discards the exit status of what it guards", pattern)})
						break
					}
				}
			case "if":
				// A gate that runs regardless of the previous steps, or only
				// after one of them failed, cannot stop a job that has already
				// failed and cannot report one that has not.
				if !strings.Contains(e.Path, ".steps[") {
					continue
				}
				if !strings.Contains(e.Value, "always()") && !strings.Contains(e.Value, "failure()") {
					continue
				}
				if stepRunsGate(w, parentPath(e.Path)) {
					findings = append(findings, Finding{w.Path, e.Line, RuleMasking,
						fmt.Sprintf("a step that runs a gate with `if: %s` no longer depends on the steps before it", e.Value)})
				}
			}
		}
	}
	return findings
}

// stepRunsGate reports whether the step at a path invokes a Makefile target.
func stepRunsGate(w workflow, stepPath string) bool {
	for _, e := range w.under(stepPath) {
		if e.Key == "run" && len(invokedTargets(e.Value)) > 0 {
			return true
		}
	}
	return false
}

// auditTargetsExist requires every target a workflow invokes to exist. A
// workflow calling a target nobody wrote fails at run time; a workflow calling
// a target that was renamed without it is the drift this rule exists to stop.
func auditTargetsExist(workflows []workflow, makefile Makefile) []Finding {
	var findings []Finding
	for _, w := range workflows {
		for _, e := range w.entries {
			if e.Key != "run" {
				continue
			}
			for _, target := range invokedTargets(e.Value) {
				if !makefile.exists(target) {
					findings = append(findings, Finding{w.Path, e.Line, RuleTargetsExist,
						fmt.Sprintf("invokes `make %s` and the Makefile declares no such target", target)})
				}
			}
		}
	}
	return findings
}

// auditGatesWired requires every gate of the phase to be reachable from the CI:
// either a job calls the target, or the target is a prerequisite of the
// aggregate job that is called.
func auditGatesWired(workflows []workflow, makefile Makefile) []Finding {
	invoked := map[string]bool{}
	for _, w := range workflows {
		for _, e := range w.entries {
			if e.Key != "run" {
				continue
			}
			for _, target := range invokedTargets(e.Value) {
				invoked[target] = true
			}
		}
	}

	var findings []Finding
	for _, g := range requiredGates {
		if invoked[g.Target] {
			continue
		}
		if invoked["verify"] && containsString(makefile.prerequisites("verify"), g.Target) {
			continue
		}
		// One gate is asked through an action instead of the target, because the
		// scanner it needs is not installed anywhere in the repository. It
		// counts as wired only when it scans the artifact this run builds with
		// the parameters the target declares.
		if g.Target == "image-scan" {
			if imageScanWired(workflows, makefile) {
				continue
			}
			if alternative := auditImageScanAlternative(workflows, makefile); len(alternative) > 0 {
				findings = append(findings, alternative...)
				continue
			}
		}
		findings = append(findings, Finding{"", 0, RuleGatesWired,
			fmt.Sprintf("the %s gate (`make %s`) is wired to no job: a change can reach main without it", g.Subject, g.Target)})
	}
	return findings
}

// imageScanWired reports whether the image scan is wired through an action that
// asks the Makefile's question.
func imageScanWired(workflows []workflow, makefile Makefile) bool {
	steps := trivySteps(workflows)
	if len(steps) == 0 {
		return false
	}
	severity, exitCode, ignoreUnfixed := imageScanParameters(makefile)
	for _, step := range steps {
		if !strings.Contains(step.with["image-ref"], "env.IMAGE") {
			continue
		}
		if step.with["severity"] != severity || step.with["exit-code"] != exitCode {
			continue
		}
		if (step.with["ignore-unfixed"] == "true") != ignoreUnfixed {
			continue
		}
		return true
	}
	return false
}

// auditImageScanAlternative reports why the action that stands in for `make
// image-scan` does not ask the same question. It is only consulted when the
// target itself is not invoked, so a repository that scans through the target
// is judged by the target alone.
func auditImageScanAlternative(workflows []workflow, makefile Makefile) []Finding {
	steps := trivySteps(workflows)
	if len(steps) == 0 {
		return nil
	}
	severity, exitCode, ignoreUnfixed := imageScanParameters(makefile)
	var findings []Finding
	for _, step := range steps {
		if !strings.Contains(step.with["image-ref"], "env.IMAGE") {
			findings = append(findings, Finding{step.path, step.line, RuleGatesWired,
				fmt.Sprintf("scans %q instead of the image this run builds (${{ env.IMAGE }})", step.with["image-ref"])})
			continue
		}
		var wrong []string
		if step.with["severity"] != severity {
			wrong = append(wrong, fmt.Sprintf("severity %q, and the Makefile declares %q", step.with["severity"], severity))
		}
		if step.with["exit-code"] != exitCode {
			wrong = append(wrong, fmt.Sprintf("exit-code %q, and the Makefile declares %q", step.with["exit-code"], exitCode))
		}
		if (step.with["ignore-unfixed"] == "true") != ignoreUnfixed {
			wrong = append(wrong, fmt.Sprintf("ignore-unfixed %q, and the Makefile declares %v", step.with["ignore-unfixed"], ignoreUnfixed))
		}
		if len(wrong) > 0 {
			findings = append(findings, Finding{step.path, step.line, RuleGatesWired,
				fmt.Sprintf("the scan in CI is weaker than `make image-scan`: %s", strings.Join(wrong, "; "))})
		}
	}
	return findings
}

// imageScanParameters reads the parameters the Makefile's own image scan
// declares, so the CI can only be as strong as the gate an operator runs.
func imageScanParameters(makefile Makefile) (severity, exitCode string, ignoreUnfixed bool) {
	recipe := makefile.recipe("image-scan")
	if match := severityPattern.FindStringSubmatch(recipe); match != nil {
		severity = match[1]
	}
	if match := exitCodePattern.FindStringSubmatch(recipe); match != nil {
		exitCode = match[1]
	}
	ignoreUnfixed = strings.Contains(recipe, "--ignore-unfixed")
	return severity, exitCode, ignoreUnfixed
}

var (
	severityPattern = regexp.MustCompile(`--severity\s+([A-Za-z0-9,]+)`)
	exitCodePattern = regexp.MustCompile(`--exit-code\s+([0-9]+)`)
)

// scanStep is one invocation of the scanner action, with the parameters it
// passes.
type scanStep struct {
	path string
	line int
	with map[string]string
}

// trivySteps finds every invocation of the scanner action.
func trivySteps(workflows []workflow) []scanStep {
	var steps []scanStep
	for _, w := range workflows {
		for _, e := range w.usesEntries() {
			if !strings.Contains(e.Value, "aquasecurity/trivy-action") {
				continue
			}
			step := scanStep{path: w.Path, line: e.Line, with: map[string]string{}}
			for _, with := range w.under(parentPath(e.Path) + ".with") {
				step.with[with.Key] = with.Value
			}
			steps = append(steps, step)
		}
	}
	return steps
}

// auditDatabaseService requires a job that runs a gate reaching PostgreSQL to
// declare the service and the address the gate reads. A gate that fails at
// connect fails for the right reason but at the wrong time — and a gate that
// skipped itself for a missing address would pass for the wrong one.
func auditDatabaseService(workflows []workflow) []Finding {
	var findings []Finding
	for _, w := range workflows {
		workflowDSN, hasWorkflowDSN := w.at("env.ARENA_DATABASE_URL")
		for _, job := range w.jobIDs() {
			needed := databaseGateOfJob(w, job)
			if needed == "" {
				continue
			}
			service := "jobs." + job + ".services.postgres"
			if !w.has(service) {
				findings = append(findings, Finding{w.Path, jobLine(w, job), RuleDatabase,
					fmt.Sprintf("job %q runs `make %s`, which reaches PostgreSQL, and declares no `postgres` service", job, needed)})
				continue
			}
			dsn, ok := w.at("jobs." + job + ".env.ARENA_DATABASE_URL")
			if !ok {
				if !hasWorkflowDSN {
					findings = append(findings, Finding{w.Path, jobLine(w, job), RuleDatabase,
						fmt.Sprintf("job %q runs `make %s` and sets no ARENA_DATABASE_URL, so the gate cannot reach the service it declares", job, needed)})
					continue
				}
				dsn = workflowDSN
			}
			if port := dsnPort(dsn.Value); port != "" && !publishesPort(w, service, port) {
				findings = append(findings, Finding{w.Path, dsn.Line, RuleDatabase,
					fmt.Sprintf("ARENA_DATABASE_URL names port %s and the `postgres` service does not publish it", port)})
			}
		}
	}
	return findings
}

// databaseGateOfJob names the first gate of a job that reaches PostgreSQL, or
// nothing when no gate of the job does.
func databaseGateOfJob(w workflow, job string) string {
	for _, e := range w.under("jobs." + job) {
		if e.Key != "run" {
			continue
		}
		for _, target := range invokedTargets(e.Value) {
			if containsString(databaseGates, target) {
				return target
			}
		}
	}
	return ""
}

// auditDraftSkips requires the two halves of one decision: the complete
// verification skips draft pull requests, and it starts by itself when the pull
// request is marked ready. Only the pair is a deferral; either half alone is a
// gate that never runs.
func auditDraftSkips(workflows []workflow) []Finding {
	// Once a bounded PR workflow exists, the complete workflows must be
	// release-only. A missing quick workflow in that topology is a failure,
	// not permission to fall back to the legacy PR model. Keep the legacy path
	// only for historical fixture falsifications that still have PR full gates.
	quickPresent := false
	releaseOnly := false
	for _, w := range workflows {
		if strings.HasSuffix(w.Path, "/quick.yml") {
			quickPresent = true
		}
		if (strings.HasSuffix(w.Path, "/verify.yml") || strings.HasSuffix(w.Path, "/supply-chain.yml")) && w.has("on.workflow_dispatch") && !w.has("on.pull_request") {
			releaseOnly = true
		}
	}
	if releaseOnly && !quickPresent {
		return []Finding{{".github/workflows/quick.yml", 0, RuleCadence, "mandatory quick workflow is missing"}}
	}
	if quickPresent {
		return auditReleaseCadence(workflows)
	}
	var findings []Finding
	for _, w := range workflows {
		if !w.has("on.pull_request") {
			continue
		}
		findings = append(findings, auditDraftTrigger(w)...)
		for _, job := range w.jobIDs() {
			condition, ok := w.at("jobs." + job + ".if")
			if !ok || !strings.Contains(condition.Value, "github.event.pull_request.draft == false") {
				findings = append(findings, Finding{w.Path, jobLine(w, job), RuleDraftSkip,
					fmt.Sprintf("job %q does not skip draft pull requests: the complete verification is deferred to `ready_for_review`, and a job that ignores that runs on every draft push", job)})
			}
		}
	}
	return findings
}

func auditReleaseCadence(workflows []workflow) []Finding {
	var findings []Finding
	for _, w := range workflows {
		if strings.HasSuffix(w.Path, "/quick.yml") {
			if !w.has("on.pull_request") || !containsString(w.flowList("on.pull_request.types"), "synchronize") || !containsString(w.flowList("on.push.branches"), "main") {
				findings = append(findings, Finding{w.Path, 0, RuleCadence, "quick must run for every pull request update and main push"})
			}
			for _, filter := range []string{"paths", "paths-ignore"} {
				if w.has("on.pull_request." + filter) {
					findings = append(findings, Finding{w.Path, 0, RuleCadence, "quick may not filter pull request paths"})
				}
			}
			found := false
			for _, job := range w.jobIDs() {
				if w.has("jobs." + job + ".if") {
					findings = append(findings, Finding{w.Path, jobLine(w, job), RuleCadence, "quick job may not be conditional"})
				}
				for _, e := range w.under("jobs." + job) {
					if e.Key == "run" && containsString(invokedTargets(e.Value), "quick-verify") {
						found = true
					}
				}
			}
			if !found {
				findings = append(findings, Finding{w.Path, 0, RuleCadence, "quick workflow does not invoke make quick-verify"})
			}
			continue
		}
		if !strings.HasSuffix(w.Path, "/verify.yml") && !strings.HasSuffix(w.Path, "/supply-chain.yml") {
			continue
		}
		if w.has("on.pull_request") || w.has("on.push.branches") || !w.has("on.workflow_dispatch") {
			findings = append(findings, Finding{w.Path, 0, RuleCadence, "complete workflow must be manual/release-only, never a normal PR or main push"})
		}
		tags, ok := w.at("on.push.tags")
		if !ok || !strings.Contains(tags.Value, "v*") {
			findings = append(findings, Finding{w.Path, 0, RuleCadence, "complete workflow must run for version tags"})
		}
	}
	return findings
}

// auditDraftTrigger checks the trigger half: the types that start the
// verification, and the filters that would let a change through without it.
func auditDraftTrigger(w workflow) []Finding {
	var findings []Finding
	line := 0
	if e, ok := w.at("on.pull_request"); ok {
		line = e.Line
	}
	types := w.flowList("on.pull_request.types")
	if !containsString(types, "ready_for_review") {
		findings = append(findings, Finding{w.Path, line, RuleDraftSkip,
			"the pull_request trigger does not list `ready_for_review`, so marking a draft ready would not start the complete verification"})
	}
	for _, filter := range []string{"paths", "paths-ignore"} {
		if e, ok := w.at("on.pull_request." + filter); ok {
			findings = append(findings, Finding{w.Path, e.Line, RuleDraftSkip,
				fmt.Sprintf("the pull_request trigger is filtered by %s (%s): a change outside the filter would reach main unverified", filter, e.Value)})
		}
	}
	return findings
}

// auditSurface requires the trigger set to be one a read-only workflow can be
// trusted with, and the secrets to be the token GitHub issues for the run.
func auditSurface(workflows []workflow) []Finding {
	var findings []Finding
	for _, w := range workflows {
		for _, trigger := range []string{"on.pull_request_target", "on.workflow_run"} {
			if e, ok := w.at(trigger); ok {
				findings = append(findings, Finding{w.Path, e.Line, RuleSurface,
					fmt.Sprintf("%s runs a workflow with the base repository's context; this one verifies a pull request and needs none of it", strings.TrimPrefix(trigger, "on."))})
			}
		}
		for _, e := range w.entries {
			for _, match := range secretReferencePattern.FindAllStringSubmatch(e.Value, -1) {
				if match[1] == "GITHUB_TOKEN" {
					continue
				}
				findings = append(findings, Finding{w.Path, e.Line, RuleSurface,
					fmt.Sprintf("reads secrets.%s; a verification workflow receives the run's token and nothing else", match[1])})
			}
		}
	}
	return findings
}

// auditBudget requires every job to declare a timeout inside the phase's CI
// budget. A job that can outlive the wait turns a hung runner into a failed
// merge instead of a reported one.
func auditBudget(workflows []workflow) []Finding {
	var findings []Finding
	for _, w := range workflows {
		for _, job := range w.jobIDs() {
			e, ok := w.at("jobs." + job + ".timeout-minutes")
			if !ok {
				findings = append(findings, Finding{w.Path, jobLine(w, job), RuleBudget,
					fmt.Sprintf("job %q declares no timeout-minutes; the phase's CI budget is %d minutes", job, ciBudgetMinutes)})
				continue
			}
			minutes, err := strconv.Atoi(e.Value)
			if err != nil {
				findings = append(findings, Finding{w.Path, e.Line, RuleBudget,
					fmt.Sprintf("timeout-minutes: %s is not a number of minutes", e.Value)})
				continue
			}
			if minutes < 1 || minutes > ciBudgetMinutes {
				findings = append(findings, Finding{w.Path, e.Line, RuleBudget,
					fmt.Sprintf("timeout-minutes: %d is outside the phase's CI budget of %d minutes", minutes, ciBudgetMinutes)})
			}
		}
	}
	return findings
}

// invokedTargets lists the Makefile targets a run block invokes.
func invokedTargets(run string) []string {
	var targets []string
	for _, match := range makeInvocationPattern.FindAllStringSubmatch(run, -1) {
		targets = append(targets, match[1])
	}
	return targets
}

var makeInvocationPattern = regexp.MustCompile(`\bmake\s+([A-Za-z0-9][A-Za-z0-9_.-]*)`)

// dsnPort reads the port of a PostgreSQL URL, and reports none when the address
// carries no explicit one.
func dsnPort(value string) string {
	at := strings.LastIndex(value, "@")
	if at < 0 {
		return ""
	}
	rest := value[at+1:]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return ""
	}
	hostPort := rest[:slash]
	colon := strings.LastIndex(hostPort, ":")
	if colon < 0 {
		return ""
	}
	return hostPort[colon+1:]
}

// publishesPort reports whether a service publishes a port, as either side of
// the pair: `54329:5432` publishes the container's 5432 at the host's 54329,
// and a gate may reach the service by either.
func publishesPort(w workflow, service, port string) bool {
	for _, e := range w.under(service + ".ports") {
		for _, side := range strings.Split(e.Value, ":") {
			if strings.TrimSpace(side) == port {
				return true
			}
		}
	}
	return false
}

// jobLine is the line a job opens on, so a finding about a job points at the
// job and not at the file.
func jobLine(w workflow, job string) int {
	if e, ok := w.at("jobs." + job); ok {
		return e.Line
	}
	return 0
}

// containsString reports whether a list holds a value.
func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
