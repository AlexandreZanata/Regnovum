package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// MonitorCommand names the season book one health pass judges and the
// operator budgets it judges with: the lag budget in seconds, the
// webhook backlog budget in events, and the caller-asserted authority
// currency. Budgets are explicit per pass, never inferred constants.
type MonitorCommand struct {
	Season             string
	LagBudgetSeconds   int64
	MaxPendingWebhooks int64
	AuthorityStale     bool
}

// MonetaryHealthSource reads one authoritative snapshot of the judged
// book. A read error fails the pass closed: the monitor never reports
// green on state it could not read.
type MonetaryHealthSource interface {
	ReadHealthSnapshot(ctx context.Context, season domain.SeasonKey) (domain.MonitorSnapshot, error)
}

// MonetaryHealthStore persists detection outcomes. Freezing and its
// alert commit together, so a freeze is never silent; every
// non-freezing finding is recorded with its returned identifier, so
// an alert without a persisted action is a failure, never a log line.
type MonetaryHealthStore interface {
	// FreezeWithHealthAlert freezes mutations and persists the
	// redacted alert lines in one transaction, returning the
	// incident that names the freeze.
	FreezeWithHealthAlert(ctx context.Context, season domain.SeasonKey, lines []string) (string, error)
	// RecordHealthAlerts persists one redacted line per finding and
	// returns one identifier per line, in order.
	RecordHealthAlerts(ctx context.Context, season domain.SeasonKey, lines []string) ([]string, error)
}

// MonetaryHealthOutcome is one pass result: the judged report plus
// the persisted identifiers that prove the action happened.
type MonetaryHealthOutcome struct {
	Report     domain.MonetaryHealthReport
	IncidentID string
	AlertIDs   []string
}

// MonitorUseCase runs one continuous-reconciliation pass: read, judge,
// then freeze-with-alert or record alerts before returning. It is an
// internal operation: no public surface calls it, and the economy
// stays disabled while it watches test books.
type MonitorUseCase struct {
	source MonetaryHealthSource
	store  MonetaryHealthStore
}

// NewMonitorUseCase creates an instance of MonitorUseCase.
func NewMonitorUseCase(source MonetaryHealthSource, store MonetaryHealthStore) *MonitorUseCase {
	return &MonitorUseCase{source: source, store: store}
}

// Execute runs one health pass over one season book. Detection and
// its persisted action share the pass: a freeze without an incident,
// or findings without recorded alerts, are refused instead of
// returned as a false green.
func (uc *MonitorUseCase) Execute(ctx context.Context, cmd MonitorCommand) (*MonetaryHealthOutcome, error) {
	season, err := domain.ParseSeasonKey(cmd.Season)
	if err != nil {
		return nil, err
	}
	if cmd.LagBudgetSeconds <= 0 || cmd.MaxPendingWebhooks < 0 {
		return nil, domain.ErrInvalidMonitorBudget
	}
	snapshot, err := uc.source.ReadHealthSnapshot(ctx, season)
	if err != nil {
		return nil, err
	}
	snapshot.Season = season
	snapshot.LagBudgetSeconds = cmd.LagBudgetSeconds
	snapshot.MaxPendingWebhooks = cmd.MaxPendingWebhooks
	snapshot.AuthorityStale = cmd.AuthorityStale
	report, err := domain.EvaluateMonetaryHealth(snapshot)
	if err != nil {
		return nil, err
	}
	outcome := &MonetaryHealthOutcome{Report: report}
	if report.Green() {
		return outcome, nil
	}
	lines := make([]string, 0, len(report.Findings))
	for _, finding := range report.Findings {
		lines = append(lines, finding.Redacted(season))
	}
	if report.FreezeRequired {
		incident, err := uc.store.FreezeWithHealthAlert(ctx, season, lines)
		if err != nil {
			return nil, err
		}
		if incident == "" {
			return nil, domain.ErrIncidentNotFound
		}
		outcome.IncidentID = incident
		return outcome, nil
	}
	alerts, err := uc.store.RecordHealthAlerts(ctx, season, lines)
	if err != nil {
		return nil, err
	}
	if len(alerts) != len(lines) {
		return nil, domain.ErrAlertWithoutAction
	}
	for _, alert := range alerts {
		if alert == "" {
			return nil, domain.ErrAlertWithoutAction
		}
	}
	outcome.AlertIDs = alerts
	return outcome, nil
}
