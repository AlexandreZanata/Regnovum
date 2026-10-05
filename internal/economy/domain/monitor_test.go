package domain_test

import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// cleanMonitorSnapshot is one conserved book: every money signal at S,
// no archive seal, empty backlog, lag within budget, fresh authority.
func cleanMonitorSnapshot() domain.MonitorSnapshot {
	supply := domain.GenesisSupplyMillis
	observations := []domain.MonitorObservation{}
	for _, observable := range []domain.MonitorObservable{
		domain.MonitorSupply,
		domain.MonitorVaults,
		domain.MonitorObligations,
		domain.MonitorProjections,
		domain.MonitorStock,
	} {
		observations = append(observations, domain.MonitorObservation{
			Observable: observable,
			Expected:   supply,
			Observed:   supply,
		})
	}
	return domain.MonitorSnapshot{
		Season:             domain.SeasonKey("S-2077-MONITOR"),
		Observations:       observations,
		PendingWebhooks:    0,
		MaxPendingWebhooks: 0,
		LagObservedSeconds: 0,
		LagBudgetSeconds:   60,
	}
}

// TestMonitorCleanBookIsGreen proves the conserved book reports green
// with no freeze, no successor block and no real-act block.
func TestMonitorCleanBookIsGreen(t *testing.T) {
	t.Parallel()
	report, err := domain.EvaluateMonetaryHealth(cleanMonitorSnapshot())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !report.Green() || len(report.Findings) != 0 {
		t.Fatalf("clean book reported %+v", report)
	}
	if report.FreezeRequired || report.BlocksSuccessor || report.BlocksRealActs {
		t.Fatalf("clean book blocks: %+v", report)
	}
}

// TestMonitorOneMilliDeviationFreezes falsifies every money signal by
// exactly one milliINK in both directions: each deviation freezes with
// its own critical finding, never a green.
func TestMonitorOneMilliDeviationFreezes(t *testing.T) {
	t.Parallel()
	for _, observable := range []domain.MonitorObservable{
		domain.MonitorSupply,
		domain.MonitorVaults,
		domain.MonitorObligations,
		domain.MonitorProjections,
		domain.MonitorStock,
	} {
		for _, delta := range []int64{1, -1} {
			snapshot := cleanMonitorSnapshot()
			for i, observation := range snapshot.Observations {
				if observation.Observable == observable {
					snapshot.Observations[i].Observed += delta
				}
			}
			report, err := domain.EvaluateMonetaryHealth(snapshot)
			if err != nil {
				t.Fatalf("%s %+d: Evaluate: %v", observable, delta, err)
			}
			if report.Green() {
				t.Fatalf("%s %+d: false green", observable, delta)
			}
			if !report.FreezeRequired {
				t.Fatalf("%s %+d: deviation without freeze", observable, delta)
			}
			if len(report.Findings) != 1 || report.Findings[0].Code != observable.String()+"-drift" {
				t.Fatalf("%s %+d: findings = %+v", observable, delta, report.Findings)
			}
			if report.Findings[0].Severity != "critical" {
				t.Fatalf("%s %+d: severity = %q, want critical", observable, delta, report.Findings[0].Severity)
			}
		}
	}
}

// TestMonitorArchiveDivergenceBlocksSuccessor proves a sealed book
// without a receipt, or with a receipt snapshot that is not byte-equal
// to S, freezes and blocks the successor.
func TestMonitorArchiveDivergenceBlocksSuccessor(t *testing.T) {
	t.Parallel()
	sealed := cleanMonitorSnapshot()
	sealed.Archive = domain.MonitorArchive{Sealed: true}
	report, err := domain.EvaluateMonetaryHealth(sealed)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if report.Green() || !report.FreezeRequired || !report.BlocksSuccessor {
		t.Fatalf("sealed book without receipt: %+v", report)
	}
	if report.Findings[0].Code != "archive-missing" {
		t.Fatalf("code = %q, want archive-missing", report.Findings[0].Code)
	}

	diverged := cleanMonitorSnapshot()
	diverged.Archive = domain.MonitorArchive{
		Sealed:         true,
		HasReceipt:     true,
		SnapshotMillis: domain.GenesisSupplyMillis - 1,
		ExpectedMillis: domain.GenesisSupplyMillis,
	}
	report, err = domain.EvaluateMonetaryHealth(diverged)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if report.Green() || !report.FreezeRequired || !report.BlocksSuccessor {
		t.Fatalf("diverged archive: %+v", report)
	}
	if report.Findings[0].Code != "archive-diverged" {
		t.Fatalf("code = %q, want archive-diverged", report.Findings[0].Code)
	}

	sealedClean := cleanMonitorSnapshot()
	sealedClean.Archive = domain.MonitorArchive{
		Sealed:         true,
		HasReceipt:     true,
		SnapshotMillis: domain.GenesisSupplyMillis,
		ExpectedMillis: domain.GenesisSupplyMillis,
	}
	report, err = domain.EvaluateMonetaryHealth(sealedClean)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !report.Green() {
		t.Fatalf("sealed book with byte-equal receipt: %+v", report)
	}
}

// TestMonitorLagAndStaleBlockRealActs proves succession lag beyond the
// operator budget and caller-asserted stale authority block real acts
// and alert, without freezing the book.
func TestMonitorLagAndStaleBlockRealActs(t *testing.T) {
	t.Parallel()
	lagged := cleanMonitorSnapshot()
	lagged.LagObservedSeconds = 61
	report, err := domain.EvaluateMonetaryHealth(lagged)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if report.Green() || report.FreezeRequired || !report.BlocksRealActs {
		t.Fatalf("lagged succession: %+v", report)
	}
	if report.Findings[0].Code != "succession-lag" {
		t.Fatalf("code = %q, want succession-lag", report.Findings[0].Code)
	}

	stale := cleanMonitorSnapshot()
	stale.AuthorityStale = true
	report, err = domain.EvaluateMonetaryHealth(stale)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if report.Green() || report.FreezeRequired || !report.BlocksRealActs {
		t.Fatalf("stale authority: %+v", report)
	}
	if report.Findings[0].Code != "stale-authority" {
		t.Fatalf("code = %q, want stale-authority", report.Findings[0].Code)
	}

	atBudget := cleanMonitorSnapshot()
	atBudget.LagObservedSeconds = 60
	report, err = domain.EvaluateMonetaryHealth(atBudget)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !report.Green() {
		t.Fatalf("lag at budget: %+v", report)
	}
}

// TestMonitorBacklogAlertsWithoutFreezing proves a webhook backlog
// beyond budget alerts with owner and runbook while the book keeps
// moving: delay is not divergence.
func TestMonitorBacklogAlertsWithoutFreezing(t *testing.T) {
	t.Parallel()
	backlogged := cleanMonitorSnapshot()
	backlogged.PendingWebhooks = 1
	report, err := domain.EvaluateMonetaryHealth(backlogged)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if report.Green() || report.FreezeRequired || report.BlocksRealActs || report.BlocksSuccessor {
		t.Fatalf("backlog: %+v", report)
	}
	if report.Findings[0].Code != "webhooks-backlog" || report.Findings[0].Runbook != "R5" {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

// TestMonitorRegistryIsComplete proves every code the evaluator can
// emit carries severity, owner, runbook and action: an alert without
// an action is a failure, so the registry cannot have a hole.
func TestMonitorRegistryIsComplete(t *testing.T) {
	t.Parallel()
	codes := domain.MonetaryFindingCodes()
	if len(codes) == 0 {
		t.Fatal("registry names no finding codes")
	}
	seen := map[string]bool{}
	collect := func(report domain.MonetaryHealthReport) {
		for _, finding := range report.Findings {
			seen[finding.Code] = true
			if finding.Severity == "" || finding.Owner == "" || finding.Runbook == "" || finding.Action == "" {
				t.Fatalf("%s: incomplete row %+v", finding.Code, finding)
			}
		}
	}
	drifted := cleanMonitorSnapshot()
	for i := range drifted.Observations {
		probe := cleanMonitorSnapshot()
		probe.Observations[i].Observed++
		report, err := domain.EvaluateMonetaryHealth(probe)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		collect(report)
	}
	backlogged := cleanMonitorSnapshot()
	backlogged.PendingWebhooks = 1
	report, err := domain.EvaluateMonetaryHealth(backlogged)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	collect(report)
	lagged := cleanMonitorSnapshot()
	lagged.LagObservedSeconds = 61
	report, err = domain.EvaluateMonetaryHealth(lagged)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	collect(report)
	stale := cleanMonitorSnapshot()
	stale.AuthorityStale = true
	report, err = domain.EvaluateMonetaryHealth(stale)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	collect(report)
	sealed := cleanMonitorSnapshot()
	sealed.Archive = domain.MonitorArchive{Sealed: true}
	report, err = domain.EvaluateMonetaryHealth(sealed)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	collect(report)
	diverged := cleanMonitorSnapshot()
	diverged.Archive = domain.MonitorArchive{Sealed: true, HasReceipt: true, SnapshotMillis: 1, ExpectedMillis: 2}
	report, err = domain.EvaluateMonetaryHealth(diverged)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	collect(report)
	for _, code := range codes {
		if !seen[code] {
			t.Fatalf("registry code %q never produced by the evaluator", code)
		}
	}
}

// TestMonitorRejectsUnjudgeableInput proves fail-closed shapes: unknown
// or duplicated signals, budget signals in the equality list, negative
// money, and missing budgets never report green.
func TestMonitorRejectsUnjudgeableInput(t *testing.T) {
	t.Parallel()
	unknown := cleanMonitorSnapshot()
	unknown.Observations = append(unknown.Observations, domain.MonitorObservation{Observable: "mint", Expected: 1, Observed: 1})
	if _, err := domain.EvaluateMonetaryHealth(unknown); err == nil {
		t.Fatal("unknown observable accepted")
	}
	duplicated := cleanMonitorSnapshot()
	duplicated.Observations = append(duplicated.Observations, duplicated.Observations[0])
	if _, err := domain.EvaluateMonetaryHealth(duplicated); err == nil {
		t.Fatal("duplicated observable accepted")
	}
	budgetAsMoney := cleanMonitorSnapshot()
	budgetAsMoney.Observations = append(budgetAsMoney.Observations, domain.MonitorObservation{Observable: domain.MonitorPendingWebhooks, Expected: 0, Observed: 0})
	if _, err := domain.EvaluateMonetaryHealth(budgetAsMoney); err == nil {
		t.Fatal("budget signal in the equality list accepted")
	}
	negative := cleanMonitorSnapshot()
	negative.Observations[0].Expected = -1
	if _, err := domain.EvaluateMonetaryHealth(negative); err == nil {
		t.Fatal("negative money accepted")
	}
	noBudget := cleanMonitorSnapshot()
	noBudget.LagBudgetSeconds = 0
	if _, err := domain.EvaluateMonetaryHealth(noBudget); err == nil {
		t.Fatal("missing lag budget accepted")
	}
	empty := cleanMonitorSnapshot()
	empty.Season = ""
	if _, err := domain.EvaluateMonetaryHealth(empty); err == nil {
		t.Fatal("empty season accepted")
	}
}

// TestMonitorRedactionCarriesNoPII proves the private log line holds
// codes, season, integers and the action only: no account, email,
// transfer identifier or secret travels with the alert.
func TestMonitorRedactionCarriesNoPII(t *testing.T) {
	t.Parallel()
	drifted := cleanMonitorSnapshot()
	drifted.Observations[0].Observed++
	report, err := domain.EvaluateMonetaryHealth(drifted)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	line := report.Findings[0].Redacted(drifted.Season)
	for _, marker := range []string{"@", "sk_", "whsec_", "BEGIN", "transfer", "custody", "email"} {
		if strings.Contains(strings.ToLower(line), marker) {
			t.Fatalf("redacted line leaks %q: %s", marker, line)
		}
	}
	for _, want := range []string{"supply-drift", "S-2077-MONITOR", "tesouro", "R3", "freeze-and-investigate"} {
		if !strings.Contains(line, want) {
			t.Fatalf("redacted line misses %q: %s", want, line)
		}
	}
}
