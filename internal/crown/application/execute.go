package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

// CustodySnapshot is the conserved custody answer of the economic
// port for one season book and origin: what the free treasury
// holds available and whether the book is frozen. Balances arrive
// as data; this use case never mints, burns or edits them.
type CustodySnapshot struct {
	Available int64
	Frozen    bool
}

// CustodySource is the consumer port for conserved custody: it
// answers availability and freeze of one origin in one season
// book. Implementations stay out of production wiring until the
// release gate: tests stand in with a fake.
type CustodySource interface {
	// Custody returns availability and freeze of one origin. An
	// unknown book refuses with domain.ErrSeasonMismatch; a
	// diverged snapshot refuses with domain.ErrInvalidAuthority.
	Custody(ctx context.Context, season domain.SeasonID, origin string) (CustodySnapshot, error)
}

// LedgerWriter is the consumer port for the economic journal: it
// appends one planned execution as a conserved double movement
// and returns the traceable receipt. The receipt must cite the
// same act, book, reign and digest as the order, or the use case
// refuses: the ledger and the Royal Book name one fact.
type LedgerWriter interface {
	// Append records one conserved movement. Replaying the same
	// order returns the same receipt; a conflicting payload
	// refuses with domain.ErrTamperedAct.
	Append(ctx context.Context, order domain.ExecutionOrder) (domain.ExecutionReceipt, error)
}

// ExecuteCommand carries one monetary decree with its clearance,
// the invested reign lookup, the destination (exactly one of a
// personal beneficiary or an institutional vault), the conflict
// state with its independent review, and the effect instant.
// Raw tokens fail validation before any port is read: shapeless
// requests never reach storage.
type ExecuteCommand struct {
	Act               domain.RoyalAct
	Clearance         domain.Clearance
	Beneficiary       string
	Vault             string
	ConflictPending   bool
	IndependentReview string
	Now               time.Time
}

// ExecuteUseCase moves monetary decrees through the economic
// port only. It loads the invested reign once, reads custody
// once, plans the conserved movement in the domain, appends it
// through the ledger port, and proves the receipt cites the same
// act, book, reign and digest. It wires nothing: fakes stand in
// for the ports until the release gate.
type ExecuteUseCase struct {
	reigns  ReignResolver
	custody CustodySource
	ledger  LedgerWriter
}

// NewExecuteUseCase creates an instance of ExecuteUseCase.
func NewExecuteUseCase(reigns ReignResolver, custody CustodySource, ledger LedgerWriter) *ExecuteUseCase {
	return &ExecuteUseCase{reigns: reigns, custody: custody, ledger: ledger}
}

// Execute plans and appends one monetary decree or refuses before
// any effect.
func (uc *ExecuteUseCase) Execute(ctx context.Context, cmd ExecuteCommand) (domain.ExecutionReceipt, error) {
	if cmd.Now.IsZero() {
		return domain.ExecutionReceipt{}, domain.ErrInvalidAuthority
	}
	sealed, err := domain.DefineAct(cmd.Act)
	if err != nil {
		return domain.ExecutionReceipt{}, err
	}
	current, err := uc.reigns.Current(ctx, sealed.Season)
	if err != nil {
		return domain.ExecutionReceipt{}, err
	}
	snapshot, err := uc.custody.Custody(ctx, sealed.Season, sealed.Origin)
	if err != nil {
		return domain.ExecutionReceipt{}, err
	}
	beneficiary := domain.HolderSubject(cmd.Beneficiary)
	if cmd.Beneficiary != "" {
		if _, err := domain.ParseHolderSubject(cmd.Beneficiary); err != nil {
			return domain.ExecutionReceipt{}, err
		}
	}
	order, err := domain.PlanExecution(sealed, cmd.Clearance, current, domain.CustodyView{
		Origin: string(sealed.Origin), Beneficiary: beneficiary, Vault: cmd.Vault,
		Available: snapshot.Available, Frozen: snapshot.Frozen,
		ConflictPending: cmd.ConflictPending, IndependentReview: cmd.IndependentReview,
	}, cmd.Now)
	if err != nil {
		return domain.ExecutionReceipt{}, err
	}
	receipt, err := uc.ledger.Append(ctx, order)
	if err != nil {
		return domain.ExecutionReceipt{}, err
	}
	if receipt.Act != order.Act || receipt.Season != order.Season ||
		receipt.Reign != order.Reign || receipt.Digest != order.Digest ||
		receipt.Amount != order.Amount || receipt.Origin != order.Origin {
		return domain.ExecutionReceipt{}, domain.ErrTamperedAct
	}
	return receipt, nil
}
