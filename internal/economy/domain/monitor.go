package domain

import (
	"fmt"
	"sort"
)

// MonitorObservable is the closed vocabulary of signals the continuous
// monetary monitor judges. The five money signals are conservation
// equalities in milliINK; the last two are budgets the operator sets
// per pass, never ratified parameters.
type MonitorObservable string

const (
	// MonitorSupply is the journal net (credits minus debits) of one book.
	MonitorSupply MonitorObservable = "supply"
	// MonitorVaults is the sum of custody balances of one book.
	MonitorVaults MonitorObservable = "vaults"
	// MonitorObligations is the hold-custody backing against the holds registry.
	MonitorObligations MonitorObservable = "obligations"
	// MonitorProjections is the stored wealth-projection sum of one season.
	MonitorProjections MonitorObservable = "projections"
	// MonitorStock is the commercial-stock partition balance of one book.
	MonitorStock MonitorObservable = "stock"
	// MonitorPendingWebhooks counts unsettled external events.
	MonitorPendingWebhooks MonitorObservable = "pending_webhooks"
	// MonitorSuccessionLag counts seconds since the last evaluated checkpoint.
	MonitorSuccessionLag MonitorObservable = "succession_lag"
)

// AllMonitorObservables returns the closed vocabulary in canonical order.
func AllMonitorObservables() []MonitorObservable {
	return []MonitorObservable{
		MonitorSupply,
		MonitorVaults,
		MonitorObligations,
		MonitorProjections,
		MonitorStock,
		MonitorPendingWebhooks,
		MonitorSuccessionLag,
	}
}

// ParseMonitorObservable validates one signal name. Matching is exact:
// two spellings never name one signal, and unknown signals are refused
// before any reading happens.
func ParseMonitorObservable(raw string) (MonitorObservable, error) {
	observable := MonitorObservable(raw)
	switch observable {
	case MonitorSupply, MonitorVaults, MonitorObligations, MonitorProjections,
		MonitorStock, MonitorPendingWebhooks, MonitorSuccessionLag:
		return observable, nil
	default:
		return "", ErrUnknownMonitorObservable
	}
}

// String returns the stored signal value.
func (o MonitorObservable) String() string { return string(o) }

// IsMoneySignal reports whether the observable is a conservation
// equality judged by strict difference instead of a budget.
func (o MonitorObservable) IsMoneySignal() bool {
	switch o {
	case MonitorSupply, MonitorVaults, MonitorObligations, MonitorProjections, MonitorStock:
		return true
	default:
		return false
	}
}

// MonitorObservation is one expected-vs-observed pair in milliINK (or a
// count for the budget signals, carried here only for shape checks).
type MonitorObservation struct {
	Observable MonitorObservable
	Expected   int64
	Observed   int64
}

// MonitorArchive is the sealed-book receipt judged against the fixed
// supply: a sealed book without a receipt, or a receipt whose snapshot
// is not byte-equal to S, blocks the successor.
type MonitorArchive struct {
	Sealed         bool
	HasReceipt     bool
	SnapshotMillis int64
	ExpectedMillis int64
}

// MonitorSnapshot is everything one pass judges: the money equalities,
// the archive receipt, the pending backlog against its budget, the
// succession lag against its budget, and the caller-asserted authority
// currency (the succession evaluator reports its own staleness; the
// monitor judges the signal, it does not invent it).
type MonitorSnapshot struct {
	Season             SeasonKey
	Observations       []MonitorObservation
	Archive            MonitorArchive
	PendingWebhooks    int64
	MaxPendingWebhooks int64
	LagObservedSeconds int64
	LagBudgetSeconds   int64
	AuthorityStale     bool
}

// MonetaryFinding is one judged break: code, severity, owner role,
// runbook, the action the alert carries, and the expected-vs-observed
// integers that prove the break. An alert without an action is a
// failure, so the registry below must name all four for every code
// the evaluator can produce.
type MonetaryFinding struct {
	Code     string
	Severity string
	Owner    string
	Runbook  string
	Action   string
	Expected int64
	Observed int64
}

// MonetaryHealthReport is one pass outcome. Green is derived, never
// asserted: it holds exactly when no finding exists, so a false green
// cannot be constructed while any signal diverges.
type MonetaryHealthReport struct {
	Findings        []MonetaryFinding
	FreezeRequired  bool
	BlocksSuccessor bool
	BlocksRealActs  bool
}

// Green reports whether the pass found nothing to act on.
func (r MonetaryHealthReport) Green() bool { return len(r.Findings) == 0 }

// monitorFindingSpec is the registry row for one finding code.
type monitorFindingSpec struct {
	severity string
	owner    string
	runbook  string
	action   string
}

// monitorRegistry maps every finding code the evaluator emits to its
// severity, owner, runbook and action. Owners are roles, never people.
var monitorRegistry = map[string]monitorFindingSpec{
	"supply-drift":      {severity: "critical", owner: "tesouro", runbook: "R3", action: "freeze-and-investigate"},
	"vaults-drift":      {severity: "critical", owner: "tesouro", runbook: "R3", action: "freeze-and-investigate"},
	"obligations-drift": {severity: "critical", owner: "tesouro", runbook: "R3", action: "freeze-and-investigate"},
	"projections-drift": {severity: "critical", owner: "livros", runbook: "R3", action: "freeze-and-investigate"},
	"stock-drift":       {severity: "critical", owner: "tesouro", runbook: "R3", action: "freeze-and-investigate"},
	"webhooks-backlog":  {severity: "high", owner: "economia", runbook: "R5", action: "drain-and-reconcile"},
	"succession-lag":    {severity: "high", owner: "coroa", runbook: "R6", action: "catch-up-and-alert"},
	"stale-authority":   {severity: "high", owner: "coroa", runbook: "R1", action: "refuse-real-acts-and-alert"},
	"archive-missing":   {severity: "critical", owner: "livros", runbook: "R4", action: "freeze-and-block-successor"},
	"archive-diverged":  {severity: "critical", owner: "livros", runbook: "R4", action: "freeze-and-block-successor"},
}

// MonetaryFindingCodes returns every code the evaluator can emit, sorted.
func MonetaryFindingCodes() []string {
	codes := make([]string, 0, len(monitorRegistry))
	for code := range monitorRegistry {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// findFinding resolves one code through the registry. A code without a
// complete row is a programming error, refused fail-closed: an alert
// without severity, owner, runbook or action must never be emitted.
func findFinding(code string, expected, observed int64) (MonetaryFinding, error) {
	spec, ok := monitorRegistry[code]
	if !ok {
		return MonetaryFinding{}, ErrUnknownMonitorFinding
	}
	if spec.severity == "" || spec.owner == "" || spec.runbook == "" || spec.action == "" {
		return MonetaryFinding{}, ErrAlertWithoutAction
	}
	return MonetaryFinding{
		Code:     code,
		Severity: spec.severity,
		Owner:    spec.owner,
		Runbook:  spec.runbook,
		Action:   spec.action,
		Expected: expected,
		Observed: observed,
	}, nil
}

// Redacted renders the finding as a private log line: codes, season,
// integer amounts and the action only. No account label, transfer
// identifier, email or secret ever enters this shape.
func (f MonetaryFinding) Redacted(season SeasonKey) string {
	return fmt.Sprintf("code=%s severity=%s season=%s expected=%d observed=%d owner=%s runbook=%s action=%s",
		f.Code, f.Severity, season, f.Expected, f.Observed, f.Owner, f.Runbook, f.Action)
}

// moneyCode maps a money observable to its finding code.
func moneyCode(observable MonitorObservable) string {
	return observable.String() + "-drift"
}

// EvaluateMonetaryHealth judges one snapshot. Any nonzero money
// difference — one milliINK or more — freezes; a sealed book without a
// byte-equal receipt blocks the successor; lag beyond the operator
// budget or stale authority blocks real acts and alerts; a webhook
// backlog beyond budget alerts without freezing. Invalid shapes fail
// closed: the monitor never reports green on input it cannot judge.
func checkMonitorShape(snapshot MonitorSnapshot) error {
	if _, err := ParseSeasonKey(snapshot.Season.String()); err != nil {
		return err
	}
	if snapshot.LagBudgetSeconds <= 0 {
		return ErrInvalidMonitorBudget
	}
	if snapshot.MaxPendingWebhooks < 0 || snapshot.PendingWebhooks < 0 || snapshot.LagObservedSeconds < 0 {
		return ErrInvalidMonitorBudget
	}
	seen := map[MonitorObservable]bool{}
	for _, observation := range snapshot.Observations {
		if _, err := ParseMonitorObservable(observation.Observable.String()); err != nil {
			return err
		}
		if !observation.Observable.IsMoneySignal() {
			return ErrUnknownMonitorObservable
		}
		if seen[observation.Observable] {
			return ErrDuplicateMonitorObservable
		}
		seen[observation.Observable] = true
		if observation.Expected < 0 || observation.Observed < 0 {
			return ErrInvalidMonitorAmount
		}
	}
	return nil
}

func judgeMoneySignals(snapshot MonitorSnapshot, report *MonetaryHealthReport) error {
	for _, observation := range snapshot.Observations {
		if observation.Expected == observation.Observed {
			continue
		}
		finding, err := findFinding(moneyCode(observation.Observable), observation.Expected, observation.Observed)
		if err != nil {
			return err
		}
		report.Findings = append(report.Findings, finding)
		report.FreezeRequired = true
	}
	return nil
}

func judgeArchiveSeal(archive MonitorArchive, report *MonetaryHealthReport) error {
	if !archive.Sealed {
		return nil
	}
	if !archive.HasReceipt {
		finding, err := findFinding("archive-missing", 1, 0)
		if err != nil {
			return err
		}
		report.Findings = append(report.Findings, finding)
		report.FreezeRequired = true
		report.BlocksSuccessor = true
		return nil
	}
	if archive.SnapshotMillis == archive.ExpectedMillis {
		return nil
	}
	finding, err := findFinding("archive-diverged", archive.ExpectedMillis, archive.SnapshotMillis)
	if err != nil {
		return err
	}
	report.Findings = append(report.Findings, finding)
	report.FreezeRequired = true
	report.BlocksSuccessor = true
	return nil
}

func judgeBacklog(snapshot MonitorSnapshot, report *MonetaryHealthReport) error {
	if snapshot.PendingWebhooks <= snapshot.MaxPendingWebhooks {
		return nil
	}
	finding, err := findFinding("webhooks-backlog", snapshot.MaxPendingWebhooks, snapshot.PendingWebhooks)
	if err != nil {
		return err
	}
	report.Findings = append(report.Findings, finding)
	return nil
}

func judgeSuccessionCurrency(snapshot MonitorSnapshot, report *MonetaryHealthReport) error {
	if snapshot.LagObservedSeconds > snapshot.LagBudgetSeconds {
		finding, err := findFinding("succession-lag", snapshot.LagBudgetSeconds, snapshot.LagObservedSeconds)
		if err != nil {
			return err
		}
		report.Findings = append(report.Findings, finding)
		report.BlocksRealActs = true
	}
	if !snapshot.AuthorityStale {
		return nil
	}
	finding, err := findFinding("stale-authority", 0, 1)
	if err != nil {
		return err
	}
	report.Findings = append(report.Findings, finding)
	report.BlocksRealActs = true
	return nil
}

func EvaluateMonetaryHealth(snapshot MonitorSnapshot) (MonetaryHealthReport, error) {
	var report MonetaryHealthReport
	if err := checkMonitorShape(snapshot); err != nil {
		return report, err
	}
	if err := judgeMoneySignals(snapshot, &report); err != nil {
		return report, err
	}
	if err := judgeArchiveSeal(snapshot.Archive, &report); err != nil {
		return report, err
	}
	if err := judgeBacklog(snapshot, &report); err != nil {
		return report, err
	}
	if err := judgeSuccessionCurrency(snapshot, &report); err != nil {
		return report, err
	}
	return report, nil
}
