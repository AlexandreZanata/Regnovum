package application

import (
	"context"
)

// ReconciliationAlertKind names one divergence the job refuses to
// fix silently: a captured payment without its transfer, or a
// transfer without its capture. Alerts never write: they name what a
// human or a retry must address.
type ReconciliationAlertKind string

const (
	// AlertCapturedWithoutTransfer is a paid gateway event with no
	// settlement: the capture exists, the INK never moved.
	AlertCapturedWithoutTransfer ReconciliationAlertKind = "captured-without-transfer"
	// AlertTransferWithoutCapture is a settlement with no paid gateway
	// event: INK moved without a capture behind it.
	AlertTransferWithoutCapture ReconciliationAlertKind = "transfer-without-capture"
)

// ReconciliationAlert is one divergence with the intent it belongs
// to and a human-readable account of it. No identifier beside the
// intent key travels: amounts stay out of alert lines.
type ReconciliationAlert struct {
	Kind      ReconciliationAlertKind
	IntentKey string
	Detail    string
}

// ReconciliationTransition is one lifecycle move the job performed
// loudly: the intent, its edge and whether the backing hold
// released. Holds release only on proven terminal failure, never on
// divergence, never on review.
type ReconciliationTransition struct {
	IntentKey string
	From      string
	To        string
	Released  bool
}

// ReconciliationReport is one job pass: every alert needing a human
// or a retry, and every transition performed with its release truth.
type ReconciliationReport struct {
	Alerts      []ReconciliationAlert
	Transitions []ReconciliationTransition
}

// PurchaseReconciliationRepository runs one reconciliation pass over
// gateway events, intents, settlements, holds and legs. Reads never
// correct: alerts name divergences, and only proven terminal
// failures transition with their hold release, all reported.
type PurchaseReconciliationRepository interface {
	// Reconcile compares the four books and advances proven terminals,
	// returning everything it found and everything it did.
	Reconcile(ctx context.Context) (*ReconciliationReport, error)
}

// ReconcilePurchasesUseCase runs one purchase reconciliation pass.
// It is an internal operation: no public surface calls it.
type ReconcilePurchasesUseCase struct {
	reconciliation PurchaseReconciliationRepository
}

// NewReconcilePurchasesUseCase creates an instance of
// ReconcilePurchasesUseCase, refusing incomplete composition.
func NewReconcilePurchasesUseCase(reconciliation PurchaseReconciliationRepository) (*ReconcilePurchasesUseCase, error) {
	if reconciliation == nil {
		return nil, ErrInvalidPurchaseIntentConfig
	}
	return &ReconcilePurchasesUseCase{reconciliation: reconciliation}, nil
}

// Execute runs one pass and returns its report.
func (uc *ReconcilePurchasesUseCase) Execute(ctx context.Context) (*ReconciliationReport, error) {
	return uc.reconciliation.Reconcile(ctx)
}
