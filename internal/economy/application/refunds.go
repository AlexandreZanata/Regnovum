package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// Refusal review reasons are machine-readable causes recorded when a
// refusal exit waits for a human decision. They never carry personal data.
const (
	RefusalReviewAlreadyConsumed   = "already_consumed"
	RefusalReviewChargeback        = "chargeback"
	RefusalReviewNothingReversible = "nothing_reversible"
)

// RefusalRefundCommand settles one refused holder's legacy exit: the
// account, the refused charter version, the fiat price charged and
// returned, the provider fact source and its correlation identifier.
// Only refusal exits arrive here: acceptance converts elsewhere.
type RefusalRefundCommand struct {
	AccountID      string
	Charter        string
	PaidMinor      int64
	RefundedMinor  int64
	Source         string
	ProviderRefund string
}

// RefusalRefundRequest is the validated exit for the repository port.
type RefusalRefundRequest struct {
	AccountID  string
	Charter    domain.CharterVersion
	PaidMinor  int64
	Refunded   int64
	Source     domain.RefundSource
	ProviderID string
}

// RefusalRefundResult is the explicit outcome: the revoked legacy units,
// the reconciled fiat, whether a human must decide and why, whether the
// call replayed the stored settlement and when the answer is due.
// Export, history, recourse and settlement stay preserved regardless.
type RefusalRefundResult struct {
	RevokeUnits  int64
	FiatMinor    int64
	NeedsReview  bool
	ReviewReason string
	Replayed     bool
	RespondBy    time.Time
}

// RefusalRefundRepository settles refusal exits in the legacy books only:
// the purchased remainder revokes, the economy journal never moves, and
// one provider object settles at most once. Callers only ever address
// their own account.
type RefusalRefundRepository interface {
	// FindConsent resolves one account's verdict for one version, or
	// reports its absence.
	FindConsent(ctx context.Context, accountID string, charter domain.CharterVersion) (*ConsentView, error)
	// SettleRefusalRefund revokes the unused purchased remainder in one
	// transaction, or replays the stored settlement untouched.
	SettleRefusalRefund(ctx context.Context, request RefusalRefundRequest) (*RefusalRefundResult, error)
}

// RefusalRefundUseCase settles one refused charter exit without creating
// value and without touching circulating INK. It is an internal
// operation: no public surface calls it.
type RefusalRefundUseCase struct {
	refunds RefusalRefundRepository
}

// NewRefusalRefundUseCase creates an instance of RefusalRefundUseCase.
func NewRefusalRefundUseCase(refunds RefusalRefundRepository) *RefusalRefundUseCase {
	return &RefusalRefundUseCase{refunds: refunds}
}

// Execute validates the exit against the standing refusal and settles it.
func (uc *RefusalRefundUseCase) Execute(ctx context.Context, cmd RefusalRefundCommand) (*RefusalRefundResult, error) {
	if cmd.AccountID == "" {
		return nil, domain.ErrInvalidRefund
	}
	charter, err := domain.ParseCharterVersion(cmd.Charter)
	if err != nil {
		return nil, err
	}
	source, err := domain.ParseRefundSource(cmd.Source)
	if err != nil {
		return nil, err
	}
	provider, err := domain.ParseRefundProviderID(cmd.ProviderRefund)
	if err != nil {
		return nil, err
	}
	if cmd.PaidMinor < 1 || cmd.RefundedMinor < 1 || cmd.RefundedMinor > cmd.PaidMinor {
		return nil, domain.ErrInvalidRefund
	}
	consent, err := uc.refunds.FindConsent(ctx, cmd.AccountID, charter)
	if err != nil {
		return nil, err
	}
	if consent == nil || consent.Decision != domain.ConsentRefused {
		return nil, domain.ErrConsentRequired
	}
	return uc.refunds.SettleRefusalRefund(ctx, RefusalRefundRequest{
		AccountID:  cmd.AccountID,
		Charter:    charter,
		PaidMinor:  cmd.PaidMinor,
		Refunded:   cmd.RefundedMinor,
		Source:     source,
		ProviderID: provider,
	})
}
