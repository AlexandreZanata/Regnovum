package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// SubscriptionStatus is the private projection of the owner's Member
// subscription: lifecycle and period only. Provider identifiers never leave
// the billing module.
type SubscriptionStatus struct {
	HasSubscription   bool
	Status            domain.SubscriptionStatus
	Product           domain.ProductID
	Market            domain.Market
	CurrentPeriodEnd  *time.Time
	CancelAtPeriodEnd bool
}

// GetSubscriptionStatusUseCase answers the owner's subscription projection
// from the local mirror. It never reaches the provider: the mirror updated
// by verified webhooks is the source, so the route stays fast and cannot
// leak provider state.
type GetSubscriptionStatusUseCase struct {
	subs SubscriptionRepository
}

// NewGetSubscriptionStatusUseCase creates the use case.
func NewGetSubscriptionStatusUseCase(subs SubscriptionRepository) *GetSubscriptionStatusUseCase {
	return &GetSubscriptionStatusUseCase{subs: subs}
}

// Execute returns the active or trialing subscription, or an empty
// projection when the account holds none.
func (uc *GetSubscriptionStatusUseCase) Execute(ctx context.Context, accountID domain.AccountID) (*SubscriptionStatus, error) {
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	record, err := uc.subs.GetActiveSubscriptionByAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return &SubscriptionStatus{}, nil
	}
	return &SubscriptionStatus{
		HasSubscription:   true,
		Status:            record.Status,
		Product:           record.ProductID,
		Market:            record.Market,
		CurrentPeriodEnd:  record.CurrentPeriodEnd,
		CancelAtPeriodEnd: record.CancelAtPeriodEnd,
	}, nil
}
