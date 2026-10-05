package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// ActivationCommand names the season book the canary gate judges,
// the cohort asking to open, the operator budgets, and the reversal
// plan that proves the way back before anything opens.
type ActivationCommand struct {
	Season             string
	Cohort             string
	LagBudgetSeconds   int64
	MaxPendingWebhooks int64
	AuthorityStale     bool
	Reversal           domain.ReversalPlan
}

// ActivationOutcome is one successful gate decision: the cohort and
// book judged, with the green report implied. Activation writes
// nothing: it only decides, so the product stays disabled while the
// gate watches test books.
type ActivationOutcome struct {
	Cohort domain.ActivationCohort
	Season domain.SeasonKey
}

// ActivationHealthSource reads one authoritative snapshot of the
// judged book. A read error fails the gate closed.
type ActivationHealthSource interface {
	ReadHealthSnapshot(ctx context.Context, season domain.SeasonKey) (domain.MonitorSnapshot, error)
}

// ActivationFreezeReader reports whether new mutations are frozen.
// Reads never freeze; the gate refuses to open while frozen holds.
type ActivationFreezeReader interface {
	IsEconomyFrozen(ctx context.Context) (bool, error)
}

// ActivationUseCase judges the canary gate: cohort, freeze, health
// and reversal plan, in that order, without writing anything.
type ActivationUseCase struct {
	source  ActivationHealthSource
	freezes ActivationFreezeReader
}

// NewActivationUseCase creates an instance of ActivationUseCase.
func NewActivationUseCase(source ActivationHealthSource, freezes ActivationFreezeReader) *ActivationUseCase {
	return &ActivationUseCase{source: source, freezes: freezes}
}

func parseActivationCommand(cmd ActivationCommand) (domain.SeasonKey, domain.ActivationCohort, error) {
	season, err := domain.ParseSeasonKey(cmd.Season)
	if err != nil {
		return domain.SeasonKey(""), "", err
	}
	cohort, err := domain.ParseActivationCohort(cmd.Cohort)
	if err != nil {
		return domain.SeasonKey(""), "", err
	}
	if cmd.LagBudgetSeconds <= 0 || cmd.MaxPendingWebhooks < 0 {
		return domain.SeasonKey(""), "", domain.ErrInvalidMonitorBudget
	}
	return season, cohort, nil
}

// Execute runs the canary gate over one season book. The four
// controls refuse independently; a green gate returns the judged
// cohort and book and writes nothing.
func (uc *ActivationUseCase) Execute(ctx context.Context, cmd ActivationCommand) (*ActivationOutcome, error) {
	season, cohort, err := parseActivationCommand(cmd)
	if err != nil {
		return nil, err
	}
	frozen, err := uc.freezes.IsEconomyFrozen(ctx)
	if err != nil {
		return nil, err
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
	if err := domain.AuthorizeCanaryActivation(cohort, report, frozen, cmd.Reversal); err != nil {
		return nil, err
	}
	return &ActivationOutcome{Cohort: cohort, Season: season}, nil
}

// KillSwitchCommand names the season book the guard watches and the
// operator budgets it judges with.
type KillSwitchCommand struct {
	Season             string
	LagBudgetSeconds   int64
	MaxPendingWebhooks int64
	AuthorityStale     bool
}

// KillSwitchOutcome is one guard pass: whether it tripped, how long
// detection plus freezing took, the naming incident, and the judged
// report. A pass that does not trip writes nothing.
type KillSwitchOutcome struct {
	Tripped    bool
	Elapsed    time.Duration
	IncidentID string
	Report     domain.MonetaryHealthReport
}

// KillSwitchSource reads one authoritative snapshot of the watched book.
type KillSwitchSource interface {
	ReadHealthSnapshot(ctx context.Context, season domain.SeasonKey) (domain.MonitorSnapshot, error)
}

// KillSwitchStore freezes new mutations with its redacted alert in
// one transaction, returning the naming incident.
type KillSwitchStore interface {
	FreezeWithHealthAlert(ctx context.Context, season domain.SeasonKey, lines []string) (string, error)
}

// KillSwitchUseCase watches one book and freezes new mutations when
// the guard trips. Reads, exports and appeals keep serving: only
// new mutations wait for the compensated resolution. The clock is
// injected so elapsed shutdown time stays deterministic under test.
type KillSwitchUseCase struct {
	source KillSwitchSource
	store  KillSwitchStore
	clock  Clock
}

// NewKillSwitchUseCase creates an instance of KillSwitchUseCase.
func NewKillSwitchUseCase(source KillSwitchSource, store KillSwitchStore, clock Clock) *KillSwitchUseCase {
	return &KillSwitchUseCase{source: source, store: store, clock: clock}
}

func killLines(season domain.SeasonKey, report domain.MonetaryHealthReport) []string {
	lines := make([]string, 0, len(report.Findings))
	for _, finding := range report.Findings {
		lines = append(lines, finding.Redacted(season))
	}
	return lines
}

// Execute runs one guard pass: read, judge, then freeze with its
// alert when the guard trips. The elapsed time covers detection
// plus the freezing commit, so the test proves the shutdown delay.
func (uc *KillSwitchUseCase) Execute(ctx context.Context, cmd KillSwitchCommand) (*KillSwitchOutcome, error) {
	started := uc.clock.Now()
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
	outcome := &KillSwitchOutcome{Report: report, Elapsed: uc.clock.Now().Sub(started)}
	if !domain.KillSwitchTrips(report) {
		return outcome, nil
	}
	lines := killLines(season, report)
	incident, err := uc.store.FreezeWithHealthAlert(ctx, season, lines)
	if err != nil {
		return nil, err
	}
	if incident == "" {
		return nil, domain.ErrIncidentNotFound
	}
	outcome.Tripped = true
	outcome.IncidentID = incident
	outcome.Elapsed = uc.clock.Now().Sub(started)
	return outcome, nil
}
