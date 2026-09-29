package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
)

// stubReconciliationRepository stands in for the PostgreSQL adapter
// where no database behavior is under test.
type stubReconciliationRepository struct {
	report *application.ReconciliationReport
	err    error
}

func (s *stubReconciliationRepository) Reconcile(context.Context) (*application.ReconciliationReport, error) {
	return s.report, s.err
}

func TestReconcilePurchasesReturnsReport(t *testing.T) {
	t.Parallel()

	want := &application.ReconciliationReport{
		Alerts: []application.ReconciliationAlert{
			{Kind: application.AlertCapturedWithoutTransfer, IntentKey: "intent-1"},
		},
		Transitions: []application.ReconciliationTransition{
			{IntentKey: "intent-2", From: "pending", To: "failed", Released: true},
		},
	}
	uc, err := application.NewReconcilePurchasesUseCase(&stubReconciliationRepository{report: want})
	if err != nil {
		t.Fatalf("NewReconcilePurchasesUseCase: %v", err)
	}
	got, err := uc.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(got.Alerts) != 1 || len(got.Transitions) != 1 {
		t.Fatalf("report changed: %+v", got)
	}
	if _, err := application.NewReconcilePurchasesUseCase(nil); err == nil {
		t.Fatalf("nil repository must refuse composition")
	}
	stub := &stubReconciliationRepository{err: errors.New("database down")}
	uc, err = application.NewReconcilePurchasesUseCase(stub)
	if err != nil {
		t.Fatalf("NewReconcilePurchasesUseCase: %v", err)
	}
	if _, err := uc.Execute(context.Background()); err == nil {
		t.Fatalf("Execute = nil, want the repository failure")
	}
}
