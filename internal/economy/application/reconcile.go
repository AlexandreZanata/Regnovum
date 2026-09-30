package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// CustodyBalance is one recomputed custody position in raw millis:
// broken books can hold negative balances, and the report must show
// them instead of refusing to render.
type CustodyBalance struct {
	CustodyID string
	Kind      domain.CustodyKind
	Label     string
	Millis    int64
	Legs      int64
	LastEntry string
}

// ReconciliationReport is the outcome of one conservation pass: the
// global supply in raw millis, every custody position, the transfers
// missing a leg and the mismatches that froze the book, if any.
// IncidentID names the freezing incident when Frozen holds, so the
// compensated resolution can address exactly that break.
type ReconciliationReport struct {
	SupplyMillis int64
	Custodies    []CustodyBalance
	Unpaired     []string
	Mismatch     []string
	Frozen       bool
	IncidentID   string
}

// ResolveCommand closes one open break with its audited compensation:
// the incident that froze the book and a note naming the fix.
type ResolveCommand struct {
	IncidentID string
	Note       string
}

// ReconciliationRepository recomputes conservation and owns the
// read-only mode. Detection and freezing share one transaction, so a
// break cannot slip between the report and the flag.
type ReconciliationRepository interface {
	// Reconcile recomputes supply, custody positions and pairing, and
	// freezes the book with an incident when anything diverges. Clean
	// books report no mismatch and stay open.
	Reconcile(ctx context.Context) (*ReconciliationReport, error)
	// Resolve records the compensated resolution of one open break and
	// reopens the book. Resolutions without an open break are refused.
	Resolve(ctx context.Context, cmd ResolveCommand) error
}

// ReconcileUseCase recomputes conservation and freezes on mismatch. It
// is an internal operation: no public surface calls it.
type ReconcileUseCase struct {
	reconciliation ReconciliationRepository
}

// NewReconcileUseCase creates an instance of ReconcileUseCase.
func NewReconcileUseCase(reconciliation ReconciliationRepository) *ReconcileUseCase {
	return &ReconcileUseCase{reconciliation: reconciliation}
}

// Execute runs one conservation pass, freezing on mismatch.
func (uc *ReconcileUseCase) Execute(ctx context.Context) (*ReconciliationReport, error) {
	return uc.reconciliation.Reconcile(ctx)
}

// ResolveUseCase reopens the book after an audited compensation. It is
// an internal operation: no public surface calls it.
type ResolveUseCase struct {
	reconciliation ReconciliationRepository
}

// NewResolveUseCase creates an instance of ResolveUseCase.
func NewResolveUseCase(reconciliation ReconciliationRepository) *ResolveUseCase {
	return &ResolveUseCase{reconciliation: reconciliation}
}

// Execute records the compensation and reopens the book.
func (uc *ResolveUseCase) Execute(ctx context.Context, cmd ResolveCommand) error {
	if cmd.IncidentID == "" || cmd.Note == "" {
		return domain.ErrIncidentNotFound
	}
	return uc.reconciliation.Resolve(ctx, cmd)
}
