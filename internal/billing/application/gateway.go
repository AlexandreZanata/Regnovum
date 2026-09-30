package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// PaymentGateway is the outbound port through which billing use cases talk to
// the payment provider (P12-T03).
//
// The port is the only surface the use cases know: no provider type, client or
// error ever crosses it, so the entire integration can be replaced by
// rewriting one adapter. Provider objects enter as identifiers and statuses of
// the billing domain (internal/billing/domain/provider.go) and leave as the
// provider's vocabulary, mirrored from the schema of migration 00020.
//
// Two guarantees are part of the contract, not of any single adapter:
//
//   - Every mutating call carries an IdempotencyKey chosen by the caller and
//     stable across retries, so a retried request resolves the same provider
//     object instead of provisioning a second one.
//   - Errors are classified by the vocabulary of errors.go
//     (ErrPaymentGatewayUnavailable, ErrPaymentGatewayTimeout,
//     ErrPaymentGatewayRejected, ErrPaymentGatewayMisconfigured,
//     ErrPaymentGatewayRequestInvalid) instead of leaking provider errors.
type PaymentGateway interface {
	// CreateCustomer provisions the provider customer that will be charged
	// for one account.
	CreateCustomer(ctx context.Context, request CreateCustomerRequest) (Customer, error)

	// CreateCheckoutSession creates the hosted checkout the buyer is sent to.
	CreateCheckoutSession(ctx context.Context, request CreateCheckoutSessionRequest) (CheckoutSession, error)

	// GetCheckoutSession reads the current provider state of a session, the
	// primitive that lets reconciliation and settlement compare our records
	// against the provider instead of trusting a browser visit.
	GetCheckoutSession(ctx context.Context, id domain.StripeCheckoutSessionID) (CheckoutSession, error)

	// GetSubscription reads the current provider state of a subscription.
	GetSubscription(ctx context.Context, id domain.StripeSubscriptionID) (Subscription, error)

	// CreatePortalSession opens the hosted customer portal for one customer.
	// The return URL is allowlisted by the caller: the provider only sends
	// the buyer back to a destination this deployment declared.
	CreatePortalSession(ctx context.Context, request CreatePortalSessionRequest) (PortalSession, error)
}

// CreateCustomerRequest asks the gateway to provision the provider customer of
// an account.
//
// The request deliberately carries no account identifier and no email: the
// local mapping (app.stripe_customers) is the only place where an account and
// a provider customer are correlated, and the decision to send personal data
// to the provider belongs to the use case that already holds the account
// (P12-T04), never to this port.
type CreateCustomerRequest struct {
	// IdempotencyKey is the caller-chosen key that makes a retried creation
	// resolve the same customer. It is required: the adapter refuses to call
	// the provider without one.
	IdempotencyKey string
}

// Customer is a provisioned provider customer.
type Customer struct {
	// ID is the private provider identifier (cus_...).
	ID domain.StripeCustomerID
	// Livemode reports whether the object belongs to the live provider mode.
	// Test and live objects are never mixed, so the caller stores this
	// alongside the identifier.
	Livemode bool
}

// CreateCheckoutSessionRequest starts a checkout for a product the server has
// already priced: the amount, the currency and the product come from the
// versioned catalog, and the provider only receives the price it must charge.
type CreateCheckoutSessionRequest struct {
	// CustomerID is the provider customer being charged.
	CustomerID domain.StripeCustomerID
	// PriceID is the provider price of the catalog product.
	PriceID domain.StripePriceID
	// Mode selects a one-off payment or the first period of a subscription.
	Mode domain.CheckoutMode
	// SuccessURL and CancelURL are the return URLs the buyer is sent to.
	// They are allowlisted by the use case before reaching the port.
	SuccessURL string
	// CancelURL is where the buyer returns after cancelling.
	CancelURL string
	// ClientReference correlates the provider session with our own record. It
	// is an opaque, non-personal reference (the local checkout intent).
	ClientReference string
	// IdempotencyKey is the caller-chosen key that makes a retried creation
	// resolve the same session. It is required.
	IdempotencyKey string
}

// CheckoutSession is the provider state of a hosted checkout session
// translated into billing vocabulary.
type CheckoutSession struct {
	// ID is the private session identifier (cs_test_... or cs_live_...),
	// validated against Livemode so the two modes can never be confused.
	ID domain.StripeCheckoutSessionID
	// Status is the provider status of the session; Complete only means the
	// buyer submitted the form.
	Status domain.CheckoutSessionStatus
	// PaymentStatus reports whether the session was actually paid. A
	// completed session that is not paid never grants anything.
	PaymentStatus domain.CheckoutPaymentStatus
	// Mode is the kind of purchase the session collects.
	Mode domain.CheckoutMode
	// PaymentIntentID is the private payment intent behind the session,
	// empty when the provider has not created one.
	PaymentIntentID domain.StripePaymentIntentID
	// AmountMinor and Currency are what the provider actually charges, so the
	// use case can compare the settled amount against the catalog price.
	AmountMinor int64
	// Currency is the currency the provider charges in.
	Currency domain.Currency
	// ClientReference is the reference echoed back by the provider.
	ClientReference string
	// Livemode reports whether the session belongs to the live provider mode.
	Livemode bool
	// URL is the hosted checkout page the buyer is redirected to.
	URL string
	// ExpiresAt is the instant the session stops being payable, when the
	// provider reports one.
	ExpiresAt *time.Time
}

// Subscription is the provider state of a subscription translated into billing
// vocabulary.
type Subscription struct {
	// ID is the private subscription identifier (sub_...).
	ID domain.StripeSubscriptionID
	// Status is the provider status of the subscription.
	Status domain.SubscriptionStatus
	// CustomerID is the customer the subscription belongs to.
	CustomerID domain.StripeCustomerID
	// PriceID is the provider price in use; reconciliation compares it with
	// the versioned catalog.
	PriceID domain.StripePriceID
	// CurrentPeriod is the billed period, nil when the provider reports none.
	CurrentPeriod *domain.BillingPeriod
	// CancelAtPeriodEnd reports a cancellation scheduled for the end of the
	// period: the benefits last until then.
	CancelAtPeriodEnd bool
	// Livemode reports whether the subscription belongs to the live mode.
	Livemode bool
}

// CreatePortalSessionRequest asks the gateway to open the hosted customer
// portal for one customer.
type CreatePortalSessionRequest struct {
	// CustomerID is the private provider customer.
	CustomerID domain.StripeCustomerID
	// ReturnURL is where the provider sends the buyer after the portal.
	// It is allowlisted by the caller and never comes from the browser.
	ReturnURL string
	// IdempotencyKey makes a retried opening resolve the same session.
	IdempotencyKey string
}

// PortalSession is the hosted portal the buyer is sent to.
type PortalSession struct {
	// URL is the hosted portal page. It is the only provider value that
	// leaves the adapter toward the browser; identifiers never do.
	URL string
}
