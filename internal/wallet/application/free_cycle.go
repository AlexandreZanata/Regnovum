package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// PeriodRenewal describes one processed free-cycle period. Replayed marks a
// period whose ledger entries already existed: nothing was written by the
// current call.
type PeriodRenewal struct {
	PeriodStart time.Time
	Expired     domain.Ink
	Granted     domain.Ink
	Replayed    bool
}

// RenewFreeCycleResult lists every period processed by a renewal call, in
// ascending order.
type RenewFreeCycleResult struct {
	CurrentPeriodStart time.Time
	Renewals           []PeriodRenewal
}

// FreeCycleRenewalRequest is the validated input of one atomic period
// renewal: expire whatever remains of the previous franchise and grant the
// next one.
type FreeCycleRenewalRequest struct {
	AccountID   domain.AccountID
	PeriodStart time.Time
	ExpireKey   domain.IdempotencyKey
	GrantKey    domain.IdempotencyKey
	Reference   domain.Reference
	Franchise   domain.Ink
	ChangedAt   time.Time
}

// FreeCycleRepository persists the free cycle anchor and performs atomic,
// idempotent period renewals.
type FreeCycleRepository interface {
	// FreeCycleAnchor returns the persisted anchor instant of the account
	// cycle, or ErrWalletNotFound.
	FreeCycleAnchor(ctx context.Context, accountID domain.AccountID) (time.Time, error)

	// RenewFreePeriod atomically expires the remaining FREE_INK balance and
	// grants the period franchise under the wallet lock. The period grant key
	// makes it idempotent: a retry returns the original outcome untouched.
	RenewFreePeriod(ctx context.Context, request FreeCycleRenewalRequest) (*PeriodRenewal, error)
}
