package application

import (
	"context"
	"fmt"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// ApplyMemberEntitlementsCommand contains the subscription state delivered by
// the provider to apply to the local account.
type ApplyMemberEntitlementsCommand struct {
	StripeSubscriptionID domain.StripeSubscriptionID
	CustomerID           domain.StripeCustomerID
	Status               domain.SubscriptionStatus
	PriceID              domain.StripePriceID
	CurrentPeriodStart   *time.Time
	CurrentPeriodEnd     *time.Time
	CancelAtPeriodEnd    bool
	CanceledAt           *time.Time
	Livemode             bool
}

// ApplyMemberEntitlementsResult is the outcome of processing subscription
// entitlements for a period.
type ApplyMemberEntitlementsResult struct {
	SubscriptionID    string
	AccountID         domain.AccountID
	Status            domain.SubscriptionStatus
	PassGranted       bool
	PassReplayed      bool
	InkGranted        bool
	InkReplayed       bool
	IgnoredOutOfOrder bool
}

// MemberEntitlementsDependencies groups all the collaborators needed to
// synchronize subscription state and apply Member entitlements.
type MemberEntitlementsDependencies struct {
	Catalog       *domain.Catalog
	Subscriptions SubscriptionRepository
	Customers     StripeCustomerRepository
	PassLots      PassLotRepository
	Inker         Inker
	Clock         Clock
}

// ApplyMemberEntitlementsUseCase synchronizes subscription status from Stripe
// and grants per-period Member entitlements (30k INK total and 1 expiring Arena Pass)
// idempotently (P12-T08; MONETIZATION §2.2, §3).
type ApplyMemberEntitlementsUseCase struct {
	deps MemberEntitlementsDependencies
}

// NewApplyMemberEntitlementsUseCase constructs the use case after validating
// all mandatory dependencies.
func NewApplyMemberEntitlementsUseCase(deps MemberEntitlementsDependencies) (*ApplyMemberEntitlementsUseCase, error) {
	if deps.Catalog == nil || deps.Subscriptions == nil || deps.Customers == nil ||
		deps.PassLots == nil || deps.Inker == nil || deps.Clock == nil {
		return nil, ErrInvalidMemberConfig
	}
	return &ApplyMemberEntitlementsUseCase{deps: deps}, nil
}

// Execute processes the incoming subscription state:
//  1. Validates input identifiers and status.
//  2. Correlates the provider customer to the local account.
//  3. Resolves the subscription product in the versioned catalog via price ID.
//  4. Guards against out-of-order deliveries (terminal status cannot be revived,
//     older periods cannot regress state).
//  5. Updates the local subscription record.
//  6. When active or trialing, idempotently grants 1 expiring Arena Pass and
//     30,000 INK for the billing period.
func (uc *ApplyMemberEntitlementsUseCase) Execute(ctx context.Context, cmd ApplyMemberEntitlementsCommand) (*ApplyMemberEntitlementsResult, error) {
	if cmd.StripeSubscriptionID.IsZero() {
		return nil, domain.ErrInvalidStripeSubscriptionID
	}
	if cmd.CustomerID.IsZero() {
		return nil, domain.ErrInvalidStripeCustomerID
	}
	if cmd.PriceID.IsZero() {
		return nil, domain.ErrInvalidStripePriceID
	}
	if !cmd.Status.IsValid() {
		return nil, domain.ErrInvalidSubscriptionStatus
	}

	accountID, err := uc.deps.Customers.AccountIDByStripeCustomer(ctx, cmd.CustomerID)
	if err != nil {
		return nil, err
	}

	product, err := uc.deps.Catalog.ProductByPriceID(cmd.PriceID)
	if err != nil {
		return nil, err
	}
	if product.Grant().Kind() != domain.GrantKindMember {
		return nil, ErrMemberWrongGrantKind
	}

	existing, err := uc.deps.Subscriptions.GetSubscriptionByStripeID(ctx, cmd.StripeSubscriptionID)
	if err != nil {
		return nil, err
	}

	if existing != nil {
		// Out-of-order check 1: A terminal subscription (canceled or incomplete_expired)
		// can never be revived or updated by an out-of-order non-terminal event.
		if existing.Status.IsTerminal() && !cmd.Status.IsTerminal() {
			return &ApplyMemberEntitlementsResult{
				SubscriptionID:    existing.ID,
				AccountID:         existing.AccountID,
				Status:            existing.Status,
				IgnoredOutOfOrder: true,
			}, nil
		}

		// Out-of-order check 2: An event for an older period cannot regress the
		// subscription period or grant stale entitlements.
		if existing.CurrentPeriodStart != nil && cmd.CurrentPeriodStart != nil &&
			cmd.CurrentPeriodStart.Before(*existing.CurrentPeriodStart) {
			return &ApplyMemberEntitlementsResult{
				SubscriptionID:    existing.ID,
				AccountID:         existing.AccountID,
				Status:            existing.Status,
				IgnoredOutOfOrder: true,
			}, nil
		}
	}

	// Active and trialing statuses require a valid non-inverted billing period.
	if cmd.Status == domain.SubscriptionActive || cmd.Status == domain.SubscriptionTrialing {
		if cmd.CurrentPeriodStart == nil || cmd.CurrentPeriodEnd == nil ||
			!cmd.CurrentPeriodEnd.After(*cmd.CurrentPeriodStart) {
			return nil, domain.ErrInvalidBillingPeriod
		}
	}

	// Set canceled_at if canceled and not provided.
	if cmd.Status == domain.SubscriptionCanceled && cmd.CanceledAt == nil {
		now := uc.deps.Clock.Now()
		cmd.CanceledAt = &now
	}

	record, err := uc.deps.Subscriptions.UpsertSubscription(ctx, UpsertSubscriptionRequest{
		AccountID:            accountID,
		StripeSubscriptionID: cmd.StripeSubscriptionID,
		Status:               cmd.Status,
		Livemode:             cmd.Livemode,
		Market:               product.Market(),
		ProductID:            product.ID(),
		CatalogVersion:       uc.deps.Catalog.Version(),
		StripePriceID:        cmd.PriceID,
		CurrentPeriodStart:   cmd.CurrentPeriodStart,
		CurrentPeriodEnd:     cmd.CurrentPeriodEnd,
		CancelAtPeriodEnd:    cmd.CancelAtPeriodEnd,
		CanceledAt:           cmd.CanceledAt,
	})
	if err != nil {
		return nil, err
	}

	// Only active or trialing status qualifies for period entitlements.
	if cmd.Status != domain.SubscriptionActive && cmd.Status != domain.SubscriptionTrialing {
		return &ApplyMemberEntitlementsResult{
			SubscriptionID: record.ID,
			AccountID:      accountID,
			Status:         record.Status,
		}, nil
	}

	// Benefit A: 1 Arena Pass expiring at the end of the period.
	// Reference is scoped to the subscription and period start timestamp.
	periodStartUnix := cmd.CurrentPeriodStart.Unix()
	refString := fmt.Sprintf("member:%s:%d", cmd.StripeSubscriptionID.String(), periodStartUnix)
	passRef, err := domain.ParseReference(refString)
	if err != nil {
		return nil, err
	}

	passQuantity, err := domain.NewQuantity(1)
	if err != nil {
		return nil, err
	}

	passResult, err := uc.deps.PassLots.GrantPassLot(ctx, GrantPassLotRequest{
		AccountID: accountID,
		Origin:    domain.OriginMember,
		Quantity:  passQuantity,
		Reference: passRef,
		ExpiresAt: cmd.CurrentPeriodEnd,
		GrantedAt: uc.deps.Clock.Now(),
	})
	if err != nil {
		return nil, fmt.Errorf("grant member arena pass: %w", err)
	}

	// Benefit B: 30,000 INK total for the period, credited to FREE_INK
	// under operation credit_member.
	inkIdempotency := fmt.Sprintf("member-ink:%s:%d", cmd.StripeSubscriptionID.String(), periodStartUnix)
	inkResult, err := uc.deps.Inker.CreditMemberInk(ctx, InkerCreditRequest{
		AccountID:   accountID.String(),
		Amount:      30000,
		Reference:   refString,
		Idempotency: inkIdempotency,
	})
	if err != nil {
		return nil, fmt.Errorf("grant member ink: %w", err)
	}

	return &ApplyMemberEntitlementsResult{
		SubscriptionID: record.ID,
		AccountID:      accountID,
		Status:         record.Status,
		PassGranted:    !passResult.Replayed,
		PassReplayed:   passResult.Replayed,
		InkGranted:     !inkResult.Replayed,
		InkReplayed:    inkResult.Replayed,
	}, nil
}
