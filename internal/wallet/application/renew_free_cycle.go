package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// RenewFreeCycleCommand holds the parameters for renewing the free plan
// cycle of one account.
type RenewFreeCycleCommand struct {
	AccountID string
}

// RenewFreeCycleUseCase renews the monthly FREE_INK cycle anchored on the
// wallet activation instant. Every elapsed period is processed in order:
// the remaining franchise is expired with an explicit ledger operation and
// the plan franchise is granted. Each period carries its own idempotency
// keys, so retries, delays and concurrent workers never duplicate a grant or
// an expiry.
type RenewFreeCycleUseCase struct {
	cycles FreeCycleRepository
	policy domain.FreeCyclePolicy
	clock  Clock
}

// NewRenewFreeCycleUseCase creates an instance of RenewFreeCycleUseCase.
func NewRenewFreeCycleUseCase(cycles FreeCycleRepository, policy domain.FreeCyclePolicy, clock Clock) *RenewFreeCycleUseCase {
	return &RenewFreeCycleUseCase{cycles: cycles, policy: policy, clock: clock}
}

// Execute renews every period between the activation period and the period
// containing now. Accounts still inside the activation period have nothing
// to renew and receive an empty result.
func (uc *RenewFreeCycleUseCase) Execute(ctx context.Context, cmd RenewFreeCycleCommand) (*RenewFreeCycleResult, error) {
	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	anchor, err := uc.cycles.FreeCycleAnchor(ctx, accountID)
	if err != nil {
		return nil, err
	}

	now := uc.clock.Now()
	current := domain.PeriodFor(anchor, now)
	result := &RenewFreeCycleResult{CurrentPeriodStart: current.Start()}

	for index := int64(1); index <= current.Index(); index++ {
		period := domain.PeriodAt(anchor, index)
		renewal, err := uc.renewPeriod(ctx, accountID, period, now)
		if err != nil {
			return nil, err
		}
		result.Renewals = append(result.Renewals, *renewal)
	}

	return result, nil
}

// renewPeriod builds the period keys and delegates the atomic renewal. The
// keys embed the account and the period start, so two accounts activated in
// the same month never collide, and a relabeled period can never be
// mistaken for another.
func (uc *RenewFreeCycleUseCase) renewPeriod(ctx context.Context, accountID domain.AccountID, period domain.MonthlyPeriod, now time.Time) (*PeriodRenewal, error) {
	stamp := period.Start().UTC().Format(time.RFC3339)
	prefix := "free-cycle:" + accountID.String() + ":" + stamp

	reference, err := domain.ParseReference("free:" + accountID.String() + ":" + stamp)
	if err != nil {
		return nil, err
	}
	expireKey, err := domain.ParseIdempotencyKey(prefix + ":expire")
	if err != nil {
		return nil, err
	}
	grantKey, err := domain.ParseIdempotencyKey(prefix + ":grant")
	if err != nil {
		return nil, err
	}

	return uc.cycles.RenewFreePeriod(ctx, FreeCycleRenewalRequest{
		AccountID:   accountID,
		PeriodStart: period.Start(),
		ExpireKey:   expireKey,
		GrantKey:    grantKey,
		Reference:   reference,
		Franchise:   uc.policy.Franchise,
		ChangedAt:   now,
	})
}
