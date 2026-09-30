package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baselineFacts is a document every rule accepts: the tests below break exactly
// one thing in it at a time, which is what makes "this rule refused it" a
// statement about the rule and not about a fixture that was never valid.
func baselineFacts() Facts {
	return Facts{
		Version: 1,
		RunAt:   "2026-09-22",
		Commit:  "abcdef1",
		Host:    Host{Kernel: "Linux 6.0", CPUs: "8", Docker: "27.0"},
		Dataset: Dataset{Seed: "drill-test", Accounts: 3, INK: 3000, Arena: "arena-test"},
		Baseline: Reading{Ledger: Ledger{
			CapturedAt: "2026-09-22 12:00:00.000000", Target: "arena@127.0.0.1:5432/arena",
			Server: "18.4", ArchiveTimeoutSeconds: 300, Accounts: 3,
			Wallets: []Wallet{{
				AccountID:  "00000000-0000-0000-0000-000000000001",
				StoredFree: 1000, DerivedFree: 1000, Rows: 1, Operations: 1,
			}},
			Operations: 1, Transactions: 1, LedgerDigest: "aaa", BalancesDigest: "bbb",
			FreeINK: 1000, Jobs: 0,
		}},
		Restored: Reading{Ledger: Ledger{
			CapturedAt: "2026-09-22 12:05:00.000000", Target: "arena@127.0.0.1:5432/arena",
			Server: "18.4", ArchiveTimeoutSeconds: 300, Accounts: 3,
			Wallets: []Wallet{{
				AccountID:  "00000000-0000-0000-0000-000000000001",
				StoredFree: 1000, DerivedFree: 1000, Rows: 1, Operations: 1,
			}},
			Operations: 1, Transactions: 1, LedgerDigest: "aaa", BalancesDigest: "bbb",
			FreeINK: 1000, Jobs: 0,
		}},
		Timeline: Timeline{
			BaselineAt: "2026-09-22 12:00:00.000000", TargetAt: "2026-09-22 12:00:01.000000",
			DisasterAt: "2026-09-22 12:00:05.000000", RowsAfterTarget: 0,
			RestoreCommand: "deploy/backup/restore.sh --base drill --target-time ...", PrimaryDestroyed: true,
		},
		RPO: RPO{BoundSeconds: 300, ObservedSeconds: 4, ArchiveLagSeconds: 3, NewestRecoveredCommit: "2026-09-22 12:00:01.000000"},
		RTO: RTO{TargetSeconds: targetRTOSeconds, ToWritableSeconds: 2, ToAppSeconds: 3},
		Outage: Outage{
			Email: EmailOutage{
				AttemptsBefore: 2, ErrorCode: "JOB_HANDLER_ERROR", JobRetained: true,
				DeliveredAfter: true, DeliveredInSeconds: 40,
			},
			Stripe: StripeOutage{
				Routes:  []Probe{{What: "the provider webhook refuses an unverified event", Method: "POST", Path: "/api/v1/webhooks/stripe", Status: 400, Want: 400}},
				Verdict: "refused",
			},
		},
		Load: Load{
			Tool: "k6", Script: "tests/load/smoke.js", Dataset: "drill-test",
			DurationSeconds: 21,
			Thresholds: []Threshold{
				{Metric: "http_req_failed", Bound: "rate<0.05", Measured: "0.001"},
				{Metric: "http_req_duration{workload:login}", Bound: "p(95)<1500", Measured: "42.1"},
			},
		},
		Journeys: Journeys{
			Smoke: []Probe{
				{What: "readiness on the restored data", Method: "GET", Path: "/health/ready", Status: 200, Want: 200},
				{What: "the sign-in page", Method: "GET", Path: "/login", Status: 200, Want: 200},
			},
			E2E: true, E2EDetail: "the versioned harness drove its journeys green",
		},
	}
}

// document renders the facts the way the drill writes them: the prose and the
// block come from one struct, so the prose rule is exercised against the same
// numbers the block carries.
func document(t *testing.T, facts Facts) reportDocument {
	t.Helper()
	rendered := Render(facts)
	fence := strings.Index(rendered, blockFence)
	if fence < 0 {
		t.Fatalf("the renderer wrote no %s block", blockFence)
	}
	return reportDocument{Prose: rendered[:fence], Facts: facts}
}

// rulesFired returns the rule names a document was refused by.
func rulesFired(violations []Violation) map[string]bool {
	fired := make(map[string]bool, len(violations))
	for _, violation := range violations {
		name := violation.Rule
		if index := strings.Index(name, ":"); index >= 0 {
			name = name[:index]
		}
		fired[name] = true
	}
	return fired
}

func TestTheBaselineDocumentIsAccepted(t *testing.T) {
	// The control: without it every mutation test below could pass because the
	// document was refused for something else entirely.
	violations := Check(document(t, baselineFacts()))
	if len(violations) != 0 {
		t.Fatalf("the baseline document was refused: %v", violations)
	}
}

func TestEachRuleRefusesItsOwnMutation(t *testing.T) {
	cases := []struct {
		rule   string
		mutate func(*Facts)
	}{
		{rule: "facts-version", mutate: func(f *Facts) { f.Version = 0 }},
		{rule: "facts-date", mutate: func(f *Facts) { f.RunAt = "22/09/2026" }},
		{rule: "facts-commit", mutate: func(f *Facts) { f.Commit = "  " }},
		{rule: "facts-host", mutate: func(f *Facts) { f.Host.CPUs = "" }},
		{rule: "financial-evidence", mutate: func(f *Facts) { f.Baseline.Ledger.Wallets = nil }},
		{rule: "financial-integrity", mutate: func(f *Facts) {
			f.Violations = []Violation{{Rule: "wallet-lost", Subject: "acct", Detail: "the baseline held this wallet"}}
		}},
		{rule: "loss-direction", mutate: func(f *Facts) { f.Timeline.RowsAfterTarget = 2 }},
		{rule: "loss-direction", mutate: func(f *Facts) { f.Timeline.PrimaryDestroyed = false }},
		{rule: "loss-direction", mutate: func(f *Facts) { f.Timeline.RestoreCommand = "" }},
		{rule: "rpo-bound", mutate: func(f *Facts) { f.RPO.ObservedSeconds = f.RPO.BoundSeconds + 1 }},
		{rule: "rpo-bound", mutate: func(f *Facts) { f.RPO.BoundSeconds = 0 }},
		{rule: "rpo-target", mutate: func(f *Facts) { f.RPO.BoundSeconds, f.RPO.ObservedSeconds = 2000, 1200 }},
		{rule: "rto-target", mutate: func(f *Facts) { f.RTO.TargetSeconds = 60 }},
		{rule: "rto-target", mutate: func(f *Facts) { f.RTO.ToAppSeconds = 0 }},
		{rule: "rto-target", mutate: func(f *Facts) { f.RTO.ToAppSeconds = f.RTO.TargetSeconds + 1 }},
		{rule: "load-registered", mutate: func(f *Facts) { f.Load.Thresholds = nil }},
		{rule: "load-registered", mutate: func(f *Facts) { f.Load.Thresholds[0].Measured = "" }},
		{rule: "load-registered", mutate: func(f *Facts) { f.Load.Thresholds[0].Measured = "unknown" }},
		{rule: "load-registered", mutate: func(f *Facts) { f.Load.Thresholds[0].Measured = "n/a" }},
		{rule: "load-registered", mutate: func(f *Facts) { f.Load.Script = "" }},
		{rule: "load-breach", mutate: func(f *Facts) { f.Load.Breaches = []string{"http_req_failed: 0.09 >= 0.05"} }},
		{rule: "load-thresholds-versioned", mutate: func(f *Facts) {
			f.Load.Thresholds = append(f.Load.Thresholds, Threshold{Metric: "http_req_duration{workload:invented}", Bound: "p(95)<1", Measured: "1"})
		}},
		{rule: "outage-email", mutate: func(f *Facts) { f.Outage.Email.AttemptsBefore = 0 }},
		{rule: "outage-email", mutate: func(f *Facts) { f.Outage.Email.ErrorCode = "" }},
		{rule: "outage-email", mutate: func(f *Facts) { f.Outage.Email.JobRetained = false }},
		{rule: "outage-email", mutate: func(f *Facts) { f.Outage.Email.DeliveredAfter = false }},
		{rule: "outage-stripe", mutate: func(f *Facts) { f.Outage.Stripe.Verdict = "fine" }},
		{rule: "outage-stripe", mutate: func(f *Facts) { f.Outage.Stripe.Routes = nil }},
		{rule: "outage-stripe", mutate: func(f *Facts) { f.Outage.Stripe.LedgerRowsCreated = 1 }},
		{rule: "outage-stripe", mutate: func(f *Facts) {
			f.Outage.Stripe.Verdict = "surface_absent"
			f.Outage.Stripe.Owner, f.Outage.Stripe.Plan = "", ""
		}},
		{rule: "journeys-smoke", mutate: func(f *Facts) { f.Journeys.Smoke = nil }},
		{rule: "journeys-smoke", mutate: func(f *Facts) { f.Journeys.Smoke[0].Status = 503 }},
		{rule: "journeys-e2e", mutate: func(f *Facts) { f.Journeys.E2E = false }},
		{rule: "journeys-e2e", mutate: func(f *Facts) { f.Journeys.E2EDetail = "" }},
	}

	for index, testCase := range cases {
		facts := baselineFacts()
		testCase.mutate(&facts)
		document := document(t, facts)
		violations := Check(document)
		fired := rulesFired(violations)
		if !fired[testCase.rule] {
			t.Errorf("case %d: the mutation did not fire %q (fired: %v)", index, testCase.rule, fired)
		}
	}
}

func TestTheProseRuleRefusesAStoryThatLostItsNumbers(t *testing.T) {
	facts := baselineFacts()
	document := document(t, facts)
	// The prose is the half a reader trusts; deleting what the block carries is
	// the drift this rule exists for.
	document.Prose = strings.ReplaceAll(document.Prose, "4s", "four seconds")
	violations := Check(document)
	if !rulesFired(violations)["prose"] {
		t.Fatalf("the prose that dropped the measured RPO was accepted: %v", violations)
	}
}

func TestReadReportRefusesWhatItCannotJudge(t *testing.T) {
	directory := t.TempDir()
	cases := map[string]string{
		"no-block":      "# a report without a block\n",
		"unclosed":      "# a report\n\n```json\n{\"version\": 1}\n",
		"unknown-field": "# a report\n\n```json\n{\"version\": 1, \"invented\": true}\n```\n",
		"malformed":     "# a report\n\n```json\n{\"version\":\n```\n",
	}
	for name, content := range cases {
		path := filepath.Join(directory, name+".md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := readReport(path); err == nil {
			t.Errorf("%s was read as a report", name)
		}
	}
}

func TestCheckReadsTheDeliveredReport(t *testing.T) {
	// The document the drill publishes is judged by its own gate, as it is: a
	// report that only passes when a test rewrites it is not evidence. The path
	// is the documented one, and the repository root is where it lives — the
	// `t.Skip` that used to stand here was hiding that the read never resolved,
	// so the regression had stopped running without saying so (P23-T07).
	t.Chdir("../..")
	raw, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatalf("the delivered report is part of the tree and this regression audits it as it is: %v", err)
	}
	document, err := readReport(documentPath)
	if err != nil {
		t.Fatalf("%s: %v", documentPath, err)
	}
	if violations := Check(document); len(violations) != 0 {
		t.Fatalf("the delivered report is refused by its own gate: %v", violations)
	}
	if len(raw) == 0 {
		t.Fatal("the delivered report is empty")
	}
}

// The ledger half: the invariants and the comparison, each refused by a mutation
// of a reading that otherwise holds.
func TestTheLedgerRulesRefuseTheirOwnMutations(t *testing.T) {
	healthy := func() Ledger {
		return Ledger{
			Accounts: 2, Operations: 2, Transactions: 2,
			Wallets: []Wallet{
				{AccountID: "acct-a", StoredFree: 100, DerivedFree: 100, Rows: 1, Operations: 1},
				{AccountID: "acct-b", StoredPurchased: 50, DerivedPurchased: 50, Rows: 1, Operations: 1},
			},
			FreeINK: 100, PurchasedINK: 50,
		}
	}
	cases := []struct {
		rule   string
		mutate func(*Ledger)
	}{
		{rule: "negative-balance", mutate: func(l *Ledger) { l.Wallets[0].StoredFree = -1; l.Wallets[0].DerivedFree = -1; l.FreeINK = -1 }},
		{rule: "derived-balance", mutate: func(l *Ledger) { l.Wallets[0].DerivedFree = 99 }},
		{rule: "orphan-operation", mutate: func(l *Ledger) {
			l.Wallets[0].Rows, l.Wallets[0].Operations = 0, 1
			l.Wallets[0].StoredFree, l.Wallets[0].DerivedFree = 0, 0
			l.FreeINK = 0
		}},
		{rule: "duplicate-bucket", mutate: func(l *Ledger) { l.Wallets[0].Rows = 3 }},
		{rule: "aggregate-total", mutate: func(l *Ledger) { l.FreeINK = 99 }},
		{rule: "ledger-smaller-than-operations", mutate: func(l *Ledger) { l.Transactions = 1 }},
		{rule: "ledger-larger-than-operations", mutate: func(l *Ledger) { l.Transactions = 5 }},
	}
	for index, testCase := range cases {
		ledger := healthy()
		testCase.mutate(&ledger)
		if fired := rulesFired(ledger.Violations()); !fired[testCase.rule] {
			t.Errorf("ledger case %d: %q did not fire (fired: %v)", index, testCase.rule, fired)
		}
	}
	if violations := healthy().Violations(); len(violations) != 0 {
		t.Fatalf("the healthy reading was refused: %v", violations)
	}

	baseline := healthy()
	comparison := []struct {
		rule   string
		mutate func(*Ledger)
	}{
		{rule: "wallet-lost", mutate: func(l *Ledger) { l.Wallets = l.Wallets[:1] }},
		{rule: "wallet-mismatch", mutate: func(l *Ledger) { l.Wallets[0].StoredFree, l.Wallets[0].DerivedFree = 90, 90 }},
		{rule: "ledger-digest", mutate: func(l *Ledger) { l.LedgerDigest = "changed" }},
		{rule: "balances-digest", mutate: func(l *Ledger) { l.BalancesDigest = "changed" }},
		{rule: "total-mismatch", mutate: func(l *Ledger) { l.FreeINK = 90 }},
		{rule: "accounts-mismatch", mutate: func(l *Ledger) { l.Accounts = 3 }},
		{rule: "ledger-size", mutate: func(l *Ledger) { l.Operations, l.Transactions = 3, 3 }},
		{rule: "jobs-mismatch", mutate: func(l *Ledger) { l.Jobs = 4 }},
	}
	for index, testCase := range comparison {
		restored := healthy()
		testCase.mutate(&restored)
		if fired := rulesFired(Compare(baseline, restored)); !fired[testCase.rule] {
			t.Errorf("comparison case %d: %q did not fire (fired: %v)", index, testCase.rule, fired)
		}
	}
	if violations := Compare(baseline, healthy()); len(violations) != 0 {
		t.Fatalf("two equal readings were compared unequal: %v", violations)
	}
}

func TestTheFactsBlockIsTheRenderedOne(t *testing.T) {
	// The renderer and the gate read the same struct: a field the block carries
	// but Facts does not would be a claim nothing judges, and reading the block
	// back has to yield exactly what was rendered.
	facts := baselineFacts()
	rendered := Render(facts)
	fence := strings.Index(rendered, blockFence)
	body := rendered[fence+len(blockFence):]
	end := strings.Index(body, "```")
	var decoded Facts
	if err := json.Unmarshal([]byte(body[:end]), &decoded); err != nil {
		t.Fatalf("the rendered block is not readable: %v", err)
	}
	if decoded.RPO.ObservedSeconds != facts.RPO.ObservedSeconds || decoded.Load.Script != facts.Load.Script {
		t.Fatalf("the block carries different facts than were rendered")
	}
}
