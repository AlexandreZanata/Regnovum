package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubReconciliationRepository stands in for the PostgreSQL adapter
// where no database behavior is under test: the use cases only forward,
// and validation happens before any call.
type stubReconciliationRepository struct {
	called int
	report *application.ReconciliationReport
	err    error
}

func (s *stubReconciliationRepository) Reconcile(_ context.Context) (*application.ReconciliationReport, error) {
	s.called++
	return s.report, s.err
}

func (s *stubReconciliationRepository) Resolve(_ context.Context, _ application.ResolveCommand) error {
	s.called++
	return s.err
}

func TestReconcileUseCaseForwardsReport(t *testing.T) {
	t.Parallel()

	supply := domain.GenesisSupply()
	stub := &stubReconciliationRepository{
		report: &application.ReconciliationReport{SupplyMillis: supply.Millis()},
	}
	useCase := application.NewReconcileUseCase(stub)
	got, err := useCase.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.SupplyMillis != supply.Millis() || got.Frozen {
		t.Fatalf("Execute did not return the clean report: %+v", got)
	}
}

func TestResolveUseCaseValidatesCompensation(t *testing.T) {
	t.Parallel()

	stub := &stubReconciliationRepository{}
	useCase := application.NewResolveUseCase(stub)
	for _, cmd := range []application.ResolveCommand{
		{},
		{IncidentID: "incident-id"},
		{Note: "compensated"},
	} {
		if err := useCase.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrIncidentNotFound) {
			t.Errorf("Execute(%+v) = %v, want ErrIncidentNotFound", cmd, err)
		}
	}
	if stub.called != 0 {
		t.Fatalf("invalid compensation reached the repository: validation never touches storage")
	}

	if err := useCase.Execute(context.Background(),
		application.ResolveCommand{IncidentID: "incident-id", Note: "compensated via linked transfer"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if stub.called != 1 {
		t.Fatalf("calls = %d, want 1", stub.called)
	}
}
