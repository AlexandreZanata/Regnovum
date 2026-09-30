package application

import (
	"context"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// Review reasons are machine-readable causes recorded when a refund waits
// for a human decision. They never carry personal data.
const (
	RefundReviewAlreadyConsumed     = "already_consumed"
	RefundReviewPartialPassRefund   = "partial_pass_refund"
	RefundReviewChargeback          = "chargeback"
	RefundReviewMemberNonRefundable = "member_franchise_non_refundable"
	RefundReviewNothingReversible   = "nothing_reversible"
	RefundReviewIntentNotSettled    = "intent_not_settled"
)

// ApplyRefundCommand is one provider refund or dispute to compensate.
type ApplyRefundCommand struct {
	// SessionID correlates the money-back object with the original checkout
	// intent settled by T06/T07. The session identifier is unique and
	// server-recorded, so a forged session never resolves.
	SessionID domain.StripeCheckoutSessionID
	// ProviderRefundID is the provider refund (re_...) or dispute (dp_...)
	// identifier. It is the idempotency anchor: the same object never
	// compensates twice.
	ProviderRefundID string
	// Source distinguishes a voluntary refund from a contested chargeback.
	Source domain.RefundSource
	// RefundedAmountMinor is the money the provider returned, in minor units
	// of the intent currency. The charged price always comes from the local
	// intent, never from the event, so a tampered amount cannot change the
	// reversible quantity.
	RefundedAmountMinor int64
}

// ApplyRefundResult is the explicit outcome of one refund record.
type ApplyRefundResult struct {
	RefundID      string
	IntentID      string
	AccountID     domain.AccountID
	Status        domain.RefundStatus
	InkRevoked    int64
	PassesRevoked int32
	NeedsReview   bool
	ReviewReason  string
	Replayed      bool
}

// RefundLedger compensates purchased INK with an append-only debit_refund
// entry. The debit is capped at the available balance by the use case, so
// the ledger never goes negative for good-faith consumption.
type RefundLedger interface {
	// PurchasedBalance returns the current PURCHASED_INK balance.
	PurchasedBalance(ctx context.Context, accountID domain.AccountID) (int64, error)
	// DebitPurchasedInk withdraws the exact amount with the refund-scoped
	// idempotency key. A replay of the same key returns Replayed without a
	// second entry.
	DebitPurchasedInk(ctx context.Context, request RefundDebitRequest) (*RefundDebitResult, error)
}

// RefundDebitRequest is one compensating INK withdrawal.
type RefundDebitRequest struct {
	AccountID   domain.AccountID
	Amount      int64
	Reference   domain.Reference
	Idempotency domain.IdempotencyKey
}

// RefundDebitResult reports whether the debit was a replay.
type RefundDebitResult struct {
	Replayed bool
}

// RefundPassLot is the purchased lot relevant to one intent, as observed
// before revocation.
type RefundPassLot struct {
	LotID     string
	Quantity  int32
	Remaining int32
	Found     bool
}

// RefundPassStore revokes purchased passes without deleting history.
type RefundPassStore interface {
	// LotForRefund resolves the PURCHASE lot granted for the intent
	// reference. Found is false when the intent granted no passes (for
	// example an INK pack) or when settlement never ran.
	LotForRefund(ctx context.Context, accountID domain.AccountID, reference domain.Reference) (*RefundPassLot, error)
	// RevokeRemaining zeroes the remaining projection of the lot. It is
	// idempotent: revoking twice keeps zero and reports the same count.
	RevokeRemaining(ctx context.Context, lotID string) (int32, error)
}

// RefundRecord is one stored refund row as read back from persistence.
type RefundRecord struct {
	ID             string
	IntentID       string
	AccountID      domain.AccountID
	ProviderRefund string
	Source         domain.RefundSource
	Status         domain.RefundStatus
	ChargedMinor   int64
	RefundedMinor  int64
	InkRevoked     int64
	PassesRevoked  int32
	NeedsReview    bool
	ReviewReason   string
}

// RefundJournal persists refund records with idempotency anchored on the
// provider refund/dispute identifier.
type RefundJournal interface {
	// GetRefundByProviderID resolves a recorded refund, nil when none exists.
	GetRefundByProviderID(ctx context.Context, providerRefundID string) (*RefundRecord, error)
	// RecordRefund inserts the outcome exactly once per provider object. A
	// replay resolves the stored row instead of writing a second one.
	RecordRefund(ctx context.Context, request RecordRefundRequest) (*RefundRecord, bool, error)
	// ListRefundsByIntent returns the audit trail of one intent, oldest first.
	ListRefundsByIntent(ctx context.Context, intentID string) ([]RefundRecord, error)
}

// RecordRefundRequest is the outcome to persist.
type RecordRefundRequest struct {
	IntentID       string
	AccountID      domain.AccountID
	ProviderRefund string
	Source         domain.RefundSource
	Status         domain.RefundStatus
	ChargedMinor   int64
	RefundedMinor  int64
	InkRevoked     int64
	PassesRevoked  int32
	NeedsReview    bool
	ReviewReason   string
}

// RefundDependencies groups everything the refund use case needs.
type RefundDependencies struct {
	Catalog *domain.Catalog
	Intents CheckoutIntentRepository
	Ledger  RefundLedger
	Passes  RefundPassStore
	Journal RefundJournal
	Clock   Clock
}

// ApplyRefundUseCase compensates one verified refund or chargeback.
//
// The flow is:
//
//  1. Resolve the checkout intent by session and require it paid: only
//     settled intents granted a benefit.
//  2. Resolve the catalog product to learn what was granted.
//  3. Return the stored record when the provider object was already handled.
//  4. For INK packs, prorate the grant by the refunded share, cap the debit
//     at the available PURCHASED balance and flag any shortfall.
//  5. For passes, revoke the remaining projection; any consumed pass, any
//     partial money refund and any dispute becomes a review.
//  6. For Member, record without automatic movement: the franchise has no
//     refund value.
//  7. Persist the explicit record exactly once.
//
// Every mutation is append-only and idempotent: wallet debits carry the
// refund-scoped key, pass revocation zeroes a projection without deleting
// rows, and the journal unique constraint makes replays harmless.
type ApplyRefundUseCase struct {
	deps RefundDependencies
}

// NewApplyRefundUseCase builds the use case, refusing incomplete composition.
func NewApplyRefundUseCase(deps RefundDependencies) (*ApplyRefundUseCase, error) {
	if deps.Catalog == nil || deps.Intents == nil || deps.Ledger == nil ||
		deps.Passes == nil || deps.Journal == nil || deps.Clock == nil {
		return nil, ErrInvalidRefundConfig
	}
	return &ApplyRefundUseCase{deps: deps}, nil
}

// Execute compensates one refund or dispute.
func (uc *ApplyRefundUseCase) Execute(ctx context.Context, cmd ApplyRefundCommand) (*ApplyRefundResult, error) {
	if cmd.SessionID.IsZero() {
		return nil, domain.ErrInvalidStripeCheckoutSessionID
	}
	if !cmd.Source.IsValid() {
		return nil, domain.ErrInvalidRefundSource
	}
	if cmd.RefundedAmountMinor < 1 {
		return nil, domain.ErrInvalidRefundAmount
	}
	providerID, err := normalizeProviderRefundID(cmd.ProviderRefundID)
	if err != nil {
		return nil, err
	}

	intent, err := uc.deps.Intents.GetCheckoutIntentBySession(ctx, cmd.SessionID)
	if err != nil {
		return nil, err
	}
	if intent.Status != domain.CheckoutIntentPaid {
		return nil, ErrRefundIntentNotSettled
	}
	if cmd.RefundedAmountMinor > intent.Amount.MinorUnits() {
		return nil, domain.ErrInvalidRefundAmount
	}

	existing, err := uc.deps.Journal.GetRefundByProviderID(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &ApplyRefundResult{
			RefundID:      existing.ID,
			IntentID:      existing.IntentID,
			AccountID:     existing.AccountID,
			Status:        existing.Status,
			InkRevoked:    existing.InkRevoked,
			PassesRevoked: existing.PassesRevoked,
			NeedsReview:   existing.NeedsReview,
			ReviewReason:  existing.ReviewReason,
			Replayed:      true,
		}, nil
	}

	product, err := uc.deps.Catalog.Product(intent.Market, intent.ProductID)
	if err != nil {
		return nil, err
	}

	charged := intent.Amount.MinorUnits()
	var inkRevoked int64
	var passesRevoked int32
	var needsReview bool
	var reviewReason string

	switch product.Grant().Kind() {
	case domain.GrantKindINK:
		available, err := uc.deps.Ledger.PurchasedBalance(ctx, intent.AccountID)
		if err != nil {
			return nil, fmt.Errorf("refund ledger balance: %w", err)
		}
		debit, review, err := domain.AssessINK(product.Grant().Quantity(), charged, cmd.RefundedAmountMinor, available, cmd.Source)
		if err != nil {
			return nil, err
		}
		needsReview = review
		if cmd.Source.IsDispute() {
			reviewReason = RefundReviewChargeback
		} else if debit < mustReversibleINK(product.Grant().Quantity(), charged, cmd.RefundedAmountMinor) {
			reviewReason = RefundReviewAlreadyConsumed
		}
		if debit > 0 {
			ref, err := domain.ParseReference("refund:" + providerID)
			if err != nil {
				return nil, err
			}
			key, err := domain.ParseIdempotencyKey("refund:" + providerID)
			if err != nil {
				return nil, err
			}
			if _, err := uc.deps.Ledger.DebitPurchasedInk(ctx, RefundDebitRequest{
				AccountID:   intent.AccountID,
				Amount:      debit,
				Reference:   ref,
				Idempotency: key,
			}); err != nil {
				return nil, fmt.Errorf("refund ledger debit: %w", err)
			}
			inkRevoked = debit
		} else {
			needsReview = true
			if reviewReason == "" {
				reviewReason = RefundReviewNothingReversible
			}
		}
		if needsReview && reviewReason == "" {
			reviewReason = RefundReviewAlreadyConsumed
		}
	case domain.GrantKindArenaPass:
		ref, err := domain.ParseReference(intent.ID)
		if err != nil {
			return nil, err
		}
		lot, err := uc.deps.Passes.LotForRefund(ctx, intent.AccountID, ref)
		if err != nil {
			return nil, fmt.Errorf("refund pass lot: %w", err)
		}
		partial := cmd.RefundedAmountMinor < charged
		if lot == nil || !lot.Found {
			needsReview = true
			reviewReason = RefundReviewNothingReversible
			break
		}
		revoke, review, err := domain.AssessPasses(int32(product.Grant().Quantity()), lot.Remaining, partial, cmd.Source)
		if err != nil {
			return nil, err
		}
		needsReview = review
		if partial {
			reviewReason = RefundReviewPartialPassRefund
		} else if cmd.Source.IsDispute() {
			reviewReason = RefundReviewChargeback
		} else if lot.Remaining < lot.Quantity {
			reviewReason = RefundReviewAlreadyConsumed
		}
		if revoke > 0 {
			revoked, err := uc.deps.Passes.RevokeRemaining(ctx, lot.LotID)
			if err != nil {
				return nil, fmt.Errorf("refund pass revoke: %w", err)
			}
			passesRevoked = revoked
		}
		if needsReview && reviewReason == "" {
			reviewReason = RefundReviewAlreadyConsumed
		}
	case domain.GrantKindMember:
		needsReview = true
		reviewReason = RefundReviewMemberNonRefundable
	default:
		return nil, ErrRefundUnsupportedGrant
	}

	status := domain.RefundStatusApplied
	if needsReview {
		status = domain.RefundStatusNeedsReview
	}

	stored, replayed, err := uc.deps.Journal.RecordRefund(ctx, RecordRefundRequest{
		IntentID:       intent.ID,
		AccountID:      intent.AccountID,
		ProviderRefund: providerID,
		Source:         cmd.Source,
		Status:         status,
		ChargedMinor:   charged,
		RefundedMinor:  cmd.RefundedAmountMinor,
		InkRevoked:     inkRevoked,
		PassesRevoked:  passesRevoked,
		NeedsReview:    needsReview,
		ReviewReason:   reviewReason,
	})
	if err != nil {
		return nil, fmt.Errorf("refund journal: %w", err)
	}
	if replayed {
		return &ApplyRefundResult{
			RefundID:      stored.ID,
			IntentID:      stored.IntentID,
			AccountID:     stored.AccountID,
			Status:        stored.Status,
			InkRevoked:    stored.InkRevoked,
			PassesRevoked: stored.PassesRevoked,
			NeedsReview:   stored.NeedsReview,
			ReviewReason:  stored.ReviewReason,
			Replayed:      true,
		}, nil
	}

	return &ApplyRefundResult{
		RefundID:      stored.ID,
		IntentID:      intent.ID,
		AccountID:     intent.AccountID,
		Status:        status,
		InkRevoked:    inkRevoked,
		PassesRevoked: passesRevoked,
		NeedsReview:   needsReview,
		ReviewReason:  reviewReason,
	}, nil
}

// normalizeProviderRefundID validates the provider refund or dispute
// identifier shape without trusting its prefix beyond the two allowed ones.
func normalizeProviderRefundID(raw string) (string, error) {
	if _, err := domain.ParseStripeRefundID(raw); err == nil {
		id, _ := domain.ParseStripeRefundID(raw)
		return id.String(), nil
	}
	if _, err := domain.ParseStripeDisputeID(raw); err == nil {
		id, _ := domain.ParseStripeDisputeID(raw)
		return id.String(), nil
	}
	return "", domain.ErrInvalidStripeRefundID
}

// mustReversibleINK recomputes the uncapped reversible quantity for review
// attribution. It mirrors AssessINK without the balance cap.
func mustReversibleINK(granted, paid, refunded int64) int64 {
	if refunded >= paid {
		return granted
	}
	reversible := granted * refunded / paid
	if reversible < 1 {
		return 1
	}
	return reversible
}
