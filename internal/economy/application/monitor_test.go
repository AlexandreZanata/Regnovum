package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubHealthSource stands in for the PostgreSQL adapter where no
// database behavior is under test: the use case judges whatever the
// source returns, and failures close the pass.
type stubHealthSource struct {
	snapshot domain.MonitorSnapshot
	err      error
}

func (s *stubHealthSource) ReadHealthSnapshot(_ context.Context, _ domain.SeasonKey) (domain.MonitorSnapshot, error) {
	return s.snapshot, s.err
}

// stubHealthStore stands in for the incident writer: it records what
// the use case persisted, so the tests prove detection never travels
// without its action.
type stubHealthStore struct {
	frozen      []string
	alerts      []string
	incident    string
	freezeErr   error
	recordErr   error
	shortAlerts bool
}

func (s *stubHealthStore) FreezeWithHealthAlert(_ context.Context, _ domain.SeasonKey, lines []string) (string, error) {
	s.frozen = append(s.frozen, lines...)
	return s.incident, s.freezeErr
}

func (s *stubHealthStore) RecordHealthAlerts(_ context.Context, _ domain.SeasonKey, lines []string) ([]string, error) {
	s.alerts = append(s.alerts, lines...)
	if s.recordErr != nil {
		return nil, s.recordErr
	}
	if s.shortAlerts {
		return []string{}, nil
	}
	ids := make([]string, 0, len(lines))
	for i := range lines {
		ids = append(ids, string(rune('a'+i)))
	}
	return ids, nil
}

func healthSnapshot() domain.MonitorSnapshot {
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
		LagBudgetSeconds:   60,
		MaxPendingWebhooks: 0,
	}
}

func healthCommand() application.MonitorCommand {
	return application.MonitorCommand{
		Season:             "S-2077-MONITOR",
		LagBudgetSeconds:   60,
		MaxPendingWebhooks: 0,
	}
}

// TestMonitorUseCaseGreenWritesNothing proves a conserved book returns
// green without touching the store: detection and action share the
// pass, and there is nothing to act on.
func TestMonitorUseCaseGreenWritesNothing(t *testing.T) {
	t.Parallel()
	source := &stubHealthSource{snapshot: healthSnapshot()}
	store := &stubHealthStore{incident: "incident-1"}
	useCase := application.NewMonitorUseCase(source, store)
	outcome, err := useCase.Execute(context.Background(), healthCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !outcome.Report.Green() || outcome.IncidentID != "" || len(outcome.AlertIDs) != 0 {
		t.Fatalf("green outcome wrote: %+v", outcome)
	}
	if len(store.frozen) != 0 || len(store.alerts) != 0 {
		t.Fatalf("green pass wrote to the store")
	}
}

// TestMonitorUseCaseFreezesWithIncident proves a one-milliINK drift
// freezes with a named incident before the use case returns: the next
// mutation meets the frozen book, never the break.
func TestMonitorUseCaseFreezesWithIncident(t *testing.T) {
	t.Parallel()
	snapshot := healthSnapshot()
	snapshot.Observations[0].Observed++
	source := &stubHealthSource{snapshot: snapshot}
	store := &stubHealthStore{incident: "incident-9"}
	useCase := application.NewMonitorUseCase(source, store)
	outcome, err := useCase.Execute(context.Background(), healthCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Report.Green() || !outcome.Report.FreezeRequired {
		t.Fatalf("drift outcome: %+v", outcome.Report)
	}
	if outcome.IncidentID != "incident-9" {
		t.Fatalf("incident = %q, want incident-9", outcome.IncidentID)
	}
	if len(store.frozen) != 1 {
		t.Fatalf("frozen lines = %d, want 1", len(store.frozen))
	}
}

// TestMonitorUseCaseStaleBlocksWithoutFreezing proves stale authority
// blocks real acts and records its alert while the book keeps moving:
// the fence owns the refusal, the monitor owns the signal.
func TestMonitorUseCaseStaleBlocksWithoutFreezing(t *testing.T) {
	t.Parallel()
	source := &stubHealthSource{snapshot: healthSnapshot()}
	store := &stubHealthStore{incident: "incident-1"}
	useCase := application.NewMonitorUseCase(source, store)
	cmd := healthCommand()
	cmd.AuthorityStale = true
	outcome, err := useCase.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Report.Green() || outcome.Report.FreezeRequired || !outcome.Report.BlocksRealActs {
		t.Fatalf("stale outcome: %+v", outcome.Report)
	}
	if len(outcome.AlertIDs) != 1 || len(store.alerts) != 1 {
		t.Fatalf("alerts = %+v / %d, want exactly one recorded", outcome.AlertIDs, len(store.alerts))
	}
	if len(store.frozen) != 0 {
		t.Fatal("stale authority froze the book")
	}
}

// TestMonitorUseCaseFailsClosed proves every silent path is an error:
// unreadable state, unpersisted freeze, unrecorded alerts and short
// alert batches never return a report, green or otherwise.
func TestMonitorUseCaseFailsClosed(t *testing.T) {
	t.Parallel()
	useCase := application.NewMonitorUseCase(
		&stubHealthSource{err: errors.New("database down")},
		&stubHealthStore{incident: "incident-1"},
	)
	if _, err := useCase.Execute(context.Background(), healthCommand()); err == nil {
		t.Fatal("unreadable snapshot accepted")
	}

	drifted := healthSnapshot()
	drifted.Observations[0].Observed++
	useCase = application.NewMonitorUseCase(
		&stubHealthSource{snapshot: drifted},
		&stubHealthStore{incident: ""},
	)
	if _, err := useCase.Execute(context.Background(), healthCommand()); !errors.Is(err, domain.ErrIncidentNotFound) {
		t.Fatalf("freeze without incident = %v, want ErrIncidentNotFound", err)
	}

	stale := healthSnapshot()
	useCase = application.NewMonitorUseCase(
		&stubHealthSource{snapshot: stale},
		&stubHealthStore{incident: "incident-1", recordErr: errors.New("disk full")},
	)
	cmd := healthCommand()
	cmd.AuthorityStale = true
	if _, err := useCase.Execute(context.Background(), cmd); err == nil {
		t.Fatal("unrecorded alert accepted")
	}

	useCase = application.NewMonitorUseCase(
		&stubHealthSource{snapshot: stale},
		&stubHealthStore{incident: "incident-1", shortAlerts: true},
	)
	if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrAlertWithoutAction) {
		t.Fatalf("short alerts = %v, want ErrAlertWithoutAction", err)
	}

	useCase = application.NewMonitorUseCase(
		&stubHealthSource{snapshot: healthSnapshot()},
		&stubHealthStore{incident: "incident-1"},
	)
	bad := healthCommand()
	bad.Season = ""
	if _, err := useCase.Execute(context.Background(), bad); err == nil {
		t.Fatal("empty season accepted")
	}
	bad = healthCommand()
	bad.LagBudgetSeconds = 0
	if _, err := useCase.Execute(context.Background(), bad); !errors.Is(err, domain.ErrInvalidMonitorBudget) {
		t.Fatalf("missing budget = %v, want ErrInvalidMonitorBudget", err)
	}
}
