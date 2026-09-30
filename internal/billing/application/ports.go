// Package application defines the use cases, orchestrations and
// consumer-oriented ports of the billing module.
package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// Clock exposes wall-clock time to billing use cases, keeping them
// deterministic under test (ADR-012).
type Clock interface {
	Now() time.Time
}

// GrantPassLotRequest is a validated Arena Pass grant to persist atomically.
type GrantPassLotRequest struct {
	AccountID domain.AccountID
	Origin    domain.PassOrigin
	Quantity  domain.Quantity
	Reference domain.Reference
	// ExpiresAt is nil for grants that never expire (bought passes) and an
	// exact UTC instant for period-bound grants.
	ExpiresAt *time.Time
	GrantedAt time.Time
}

// GrantPassLotResult is the outcome of a grant: the lot the
// (account, origin, reference) key resolves to, and whether it was a replay
// of an earlier attempt.
type GrantPassLotResult struct {
	Lot      domain.PassLot
	Replayed bool
}

// PassLotRepository persists Arena Pass grants with idempotency anchored on
// (account, origin, reference).
type PassLotRepository interface {
	// GrantPassLot inserts the lot exactly once. A duplicate grant resolves
	// the original lot with Replayed set and writes nothing.
	GrantPassLot(ctx context.Context, request GrantPassLotRequest) (*GrantPassLotResult, error)
}

// Purchaser is the billing view of the account that wants to buy: its
// identifier and whether it may purchase at all.
type Purchaser struct {
	AccountID domain.AccountID
	// Eligible reports an active account with a verified email
	// (docs/BUSINESS_RULES.md §7, REQ-AUTH-02). Nothing else about the
	// account — email, credentials, profile — ever crosses this port.
	Eligible bool
}

// PurchaserDirectory answers the purchase eligibility question without
// exposing the identity module: the composition root satisfies it, and the
// eligibility rule stays in one documented predicate.
type PurchaserDirectory interface {
	// PurchaserForCheckout returns the acting account's eligibility. It
	// returns ErrPurchaserNotFound when no account carries the identifier,
	// so an unknown account can never be treated as an eligible one.
	PurchaserForCheckout(ctx context.Context, accountID domain.AccountID) (Purchaser, error)
}

// StripeCustomerRecord is the stored correlation between an account and its
// provider customer. The provider identifier is private: it is persisted and
// correlated locally, never projected to a client.
type StripeCustomerRecord struct {
	AccountID  domain.AccountID
	CustomerID domain.StripeCustomerID
	// Livemode pins the provider mode of the mapping: test and live objects
	// are never mixed for one account.
	Livemode  bool
	CreatedAt time.Time
}

// RecordStripeCustomerRequest stores a provider customer mapping.
type RecordStripeCustomerRequest struct {
	AccountID  domain.AccountID
	CustomerID domain.StripeCustomerID
	Livemode   bool
}

// StripeCustomerRepository persists the account→customer correlation, which is
// the anchor every checkout of the account is created against.
type StripeCustomerRepository interface {
	// StripeCustomer returns the stored mapping, nil when the account has
	// none yet.
	StripeCustomer(ctx context.Context, accountID domain.AccountID) (*StripeCustomerRecord, error)

	// RecordStripeCustomer stores the mapping exactly once per account. A
	// concurrent creation resolves the stored mapping instead of overwriting
	// it, so a retried request never leaves the account charged through two
	// different provider customers.
	RecordStripeCustomer(ctx context.Context, request RecordStripeCustomerRequest) (*StripeCustomerRecord, error)

	// AccountIDByStripeCustomer resolves the account ID from the stored
	// provider customer mapping. It returns ErrPurchaserNotFound when no
	// mapping carries the customer identifier.
	AccountIDByStripeCustomer(ctx context.Context, customerID domain.StripeCustomerID) (domain.AccountID, error)
}

// RecordCheckoutIntentRequest is the commercial decision to persist, already
// resolved by the server from the versioned catalog. No field of it can come
// from a browser.
type RecordCheckoutIntentRequest struct {
	AccountID      domain.AccountID
	Market         domain.Market
	ProductID      domain.ProductID
	CatalogVersion int
	Amount         domain.Money
	Livemode       bool
	// SessionID is the provider session the decision was shipped as.
	SessionID domain.StripeCheckoutSessionID
	// Status is open for a payable session and expired when the provider
	// reports a session that can no longer be paid.
	Status domain.CheckoutIntentStatus
	// ClosedAt is required exactly when Status is expired: the instant the
	// intent stopped being payable.
	ClosedAt *time.Time
}

// CheckoutIntentRecord is one stored checkout intent as read back from
// persistence.
type CheckoutIntentRecord struct {
	ID             string
	AccountID      domain.AccountID
	Market         domain.Market
	ProductID      domain.ProductID
	CatalogVersion int
	Amount         domain.Money
	Livemode       bool
	Status         domain.CheckoutIntentStatus
	SessionID      domain.StripeCheckoutSessionID
	CreatedAt      time.Time
}

// RecordCheckoutIntentResult is the outcome of recording an intent: the stored
// record and whether the provider session had already been recorded.
type RecordCheckoutIntentResult struct {
	Intent   CheckoutIntentRecord
	Replayed bool
}

// CheckoutIntentRepository persists checkout intents with idempotency anchored
// on the provider session identifier, which is unique by schema constraint.
type CheckoutIntentRepository interface {
	// RecordCheckoutIntent inserts the intent exactly once per provider
	// session. A replay resolves the stored intent with Replayed set and
	// writes nothing; a session already recorded for another account is
	// refused instead of being disclosed.
	RecordCheckoutIntent(ctx context.Context, request RecordCheckoutIntentRequest) (*RecordCheckoutIntentResult, error)

	// GetCheckoutIntentBySession resolves the intent a provider session
	// stands for. It returns ErrCheckoutIntentNotFound when no intent
	// carries the session identifier.
	GetCheckoutIntentBySession(ctx context.Context, sessionID domain.StripeCheckoutSessionID) (*CheckoutIntentRecord, error)

	// MarkCheckoutIntentPaid transitions the intent to the paid terminal
	// state and records the settlement instant. It is a no-op when the
	// intent is already paid (a replay of the same webhook event).
	MarkCheckoutIntentPaid(ctx context.Context, sessionID domain.StripeCheckoutSessionID) error
}

// SubscriptionRecord is one stored subscription mirror as read back from
// persistence.
type SubscriptionRecord struct {
	ID                   string
	AccountID            domain.AccountID
	StripeSubscriptionID domain.StripeSubscriptionID
	Status               domain.SubscriptionStatus
	Livemode             bool
	Market               domain.Market
	ProductID            domain.ProductID
	CatalogVersion       int
	StripePriceID        domain.StripePriceID
	CurrentPeriodStart   *time.Time
	CurrentPeriodEnd     *time.Time
	CancelAtPeriodEnd    bool
	CanceledAt           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// UpsertSubscriptionRequest holds the parameters to insert or update a
// subscription mirror.
type UpsertSubscriptionRequest struct {
	AccountID            domain.AccountID
	StripeSubscriptionID domain.StripeSubscriptionID
	Status               domain.SubscriptionStatus
	Livemode             bool
	Market               domain.Market
	ProductID            domain.ProductID
	CatalogVersion       int
	StripePriceID        domain.StripePriceID
	CurrentPeriodStart   *time.Time
	CurrentPeriodEnd     *time.Time
	CancelAtPeriodEnd    bool
	CanceledAt           *time.Time
}

// SubscriptionRepository persists subscription mirrors with idempotency anchored
// on the provider subscription identifier (unique by schema constraint).
type SubscriptionRepository interface {
	// GetSubscriptionByStripeID resolves a subscription by its provider
	// identifier. It returns nil, nil when no subscription exists yet.
	GetSubscriptionByStripeID(ctx context.Context, subID domain.StripeSubscriptionID) (*SubscriptionRecord, error)

	// UpsertSubscription inserts or updates the subscription mirror.
	UpsertSubscription(ctx context.Context, request UpsertSubscriptionRequest) (*SubscriptionRecord, error)

	// GetActiveSubscriptionByAccount returns the most recent active or
	// trialing subscription for an account, or nil if none exists.
	GetActiveSubscriptionByAccount(ctx context.Context, accountID domain.AccountID) (*SubscriptionRecord, error)
}
