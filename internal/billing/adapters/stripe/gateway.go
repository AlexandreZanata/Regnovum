// Package stripe is the outbound payment adapter of the billing module
// (P12-T03). It implements the billing application port
// (application.PaymentGateway) over the official Stripe SDK, and it is the
// only package in the repository allowed to import that SDK — a test in
// internal/architecture_test.go fails the build otherwise, per
// docs/DEPENDENCIES.md §3.2 (supplier types never cross an adapter boundary).
//
// Everything provider specific stops here: SDK objects are translated into
// billing domain value objects, provider failures into the application error
// vocabulary, and provider free-form messages are dropped instead of being
// propagated.
//
// Four invariants shape the implementation:
//
//   - No global state. The SDK is configured per instance (its own HTTP client
//     and backends), so the adapter never mutates the package-level defaults
//     and two gateways with different credentials can coexist.
//   - Every mutating call carries an idempotency key chosen by the caller and
//     refuses to reach the provider without one: a retry resolves the same
//     object instead of provisioning a second one.
//   - Every call is bounded by a timeout, applied both as a request context
//     deadline and as the HTTP client timeout.
//   - The secret, the customer, the email and the provider's message are never
//     part of an error, a log line or a diagnostic rendering.
package stripe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/stripe/stripe-go/v78"
	"github.com/stripe/stripe-go/v78/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v78/checkout/session"
	"github.com/stripe/stripe-go/v78/customer"
	"github.com/stripe/stripe-go/v78/subscription"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// maxIdempotencyKeyLength is the provider's documented bound for an
// Idempotency-Key header value.
const maxIdempotencyKeyLength = 255

// maxClientReferenceLength is the provider's documented bound for the
// client_reference_id of a checkout session.
const maxClientReferenceLength = 200

// Config configures the adapter. Both required fields are validated by
// NewGateway, so a partly configured gateway can never reach the provider.
type Config struct {
	// SecretKey is the provider API secret. It is required and never leaves
	// this package: the caller passes it straight from platform configuration
	// (config.Secret.Unredacted) and nothing here logs, formats or returns it.
	SecretKey string

	// Timeout bounds one provider call. It is required: an unbounded call
	// would hold a server goroutine and a database transaction open for as
	// long as the provider stalls.
	Timeout time.Duration

	// BaseURL overrides the provider API endpoint. It exists for tests that
	// exercise this adapter against a stub server, and for nothing else: no
	// environment variable maps to it, so a deployment can never be pointed
	// at another endpoint.
	BaseURL string
}

// Gateway implements application.PaymentGateway over the official Stripe SDK.
type Gateway struct {
	timeout time.Duration
	clients providerClients
}

// providerClients groups the SDK clients in a type that renders redacted, so
// the credentials embedded in the clients can never be printed by accident.
type providerClients struct {
	customers     *customer.Client
	sessions      *checkoutsession.Client
	subscriptions *subscription.Client
	portal        *session.Client
}

// String implements fmt.Stringer without ever printing the credentials held by
// the SDK clients.
func (providerClients) String() string { return "stripe clients [REDACTED]" }

// GoString implements fmt.GoStringer, closing the %#v leak path.
func (providerClients) GoString() string { return "stripe clients [REDACTED]" }

// String implements fmt.Stringer with a fully redacted representation.
func (g Gateway) String() string {
	return fmt.Sprintf("stripe gateway{timeout:%s}", g.timeout)
}

// GoString implements fmt.GoStringer with a fully redacted representation.
func (g Gateway) GoString() string {
	return fmt.Sprintf("stripe gateway{timeout:%s}", g.timeout)
}

// Application port compliance is asserted at compile time.
var _ application.PaymentGateway = (*Gateway)(nil)

// NewGateway builds the adapter. It refuses to build a gateway that could
// reach the provider half configured: without a secret there is nothing to
// authenticate with, without a timeout a call could hang forever, and a base
// URL must be an absolute HTTP endpoint.
func NewGateway(config Config) (*Gateway, error) {
	secret := secretKey(config.SecretKey)
	if !secret.isSet() {
		return nil, ErrMissingSecretKey
	}
	if config.Timeout <= 0 {
		return nil, ErrInvalidTimeout
	}
	if config.BaseURL != "" && !validBaseURL(config.BaseURL) {
		return nil, ErrInvalidBaseURL
	}

	backendConfig := &stripe.BackendConfig{
		// The transport is bounded by the same deadline as the request, and
		// it is the adapter's own client: the SDK package-level client is
		// never touched.
		HTTPClient: &http.Client{Timeout: config.Timeout},
		// Network retries are disabled deliberately. Retrying is a policy of
		// the caller, which owns the idempotency key and the transaction: an
		// implicit retry inside the adapter would hide the first failure and
		// multiply the latency of every unlucky request.
		MaxNetworkRetries: stripe.Int64(0),
		// The SDK logs to stderr by default. Billing code logs through
		// structured logging with redaction, never through a library, so the
		// SDK logger is silenced.
		LeveledLogger: &stripe.LeveledLogger{Level: stripe.LevelNull},
	}
	if config.BaseURL != "" {
		backendConfig.URL = stripe.String(config.BaseURL)
	}
	backends := stripe.NewBackendsWithConfig(backendConfig)

	key := secret.reveal()
	return &Gateway{
		timeout: config.Timeout,
		clients: providerClients{
			customers:     &customer.Client{B: backends.API, Key: key},
			sessions:      &checkoutsession.Client{B: backends.API, Key: key},
			subscriptions: &subscription.Client{B: backends.API, Key: key},
			portal:        &session.Client{B: backends.API, Key: key},
		},
	}, nil
}

// CreateCustomer provisions the provider customer that will be charged for one
// account.
func (g *Gateway) CreateCustomer(ctx context.Context, request application.CreateCustomerRequest) (application.Customer, error) {
	if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
		return application.Customer{}, fmt.Errorf("create customer: %w", err)
	}

	ctx, cancel := g.deadline(ctx)
	defer cancel()

	created, err := g.clients.customers.New(&stripe.CustomerParams{
		Params: stripe.Params{Context: ctx, IdempotencyKey: stripe.String(request.IdempotencyKey)},
	})
	if err != nil {
		return application.Customer{}, fmt.Errorf("create customer: %w", classify(err))
	}

	id, err := domain.ParseStripeCustomerID(created.ID)
	if err != nil {
		return application.Customer{}, fmt.Errorf("create customer: %w", misconfigured(err))
	}
	return application.Customer{ID: id, Livemode: created.Livemode}, nil
}

// CreateCheckoutSession creates the hosted checkout the buyer is sent to.
func (g *Gateway) CreateCheckoutSession(ctx context.Context, request application.CreateCheckoutSessionRequest) (application.CheckoutSession, error) {
	if err := validateSessionRequest(request); err != nil {
		return application.CheckoutSession{}, fmt.Errorf("create checkout session: %w", err)
	}

	ctx, cancel := g.deadline(ctx)
	defer cancel()

	created, err := g.clients.sessions.New(&stripe.CheckoutSessionParams{
		Params:            stripe.Params{Context: ctx, IdempotencyKey: stripe.String(request.IdempotencyKey)},
		Mode:              stripe.String(request.Mode.String()),
		Customer:          stripe.String(request.CustomerID.String()),
		ClientReferenceID: stripe.String(request.ClientReference),
		SuccessURL:        stripe.String(request.SuccessURL),
		CancelURL:         stripe.String(request.CancelURL),
		LineItems: []*stripe.CheckoutSessionLineItemParams{{
			// One unit of the catalog price: the versioned catalog sells
			// distinct prices, never quantities, so the browser has no
			// quantity to influence.
			Price:    stripe.String(request.PriceID.String()),
			Quantity: stripe.Int64(1),
		}},
	})
	if err != nil {
		return application.CheckoutSession{}, fmt.Errorf("create checkout session: %w", classify(err))
	}

	session, err := translateSession(created)
	if err != nil {
		return application.CheckoutSession{}, fmt.Errorf("create checkout session: %w", err)
	}
	return session, nil
}

// GetCheckoutSession reads the current provider state of a session.
func (g *Gateway) GetCheckoutSession(ctx context.Context, id domain.StripeCheckoutSessionID) (application.CheckoutSession, error) {
	if id.IsZero() {
		return application.CheckoutSession{}, fmt.Errorf("get checkout session: %w", application.ErrPaymentGatewayRequestInvalid)
	}

	ctx, cancel := g.deadline(ctx)
	defer cancel()

	fetched, err := g.clients.sessions.Get(id.String(), &stripe.CheckoutSessionParams{
		Params: stripe.Params{Context: ctx},
	})
	if err != nil {
		return application.CheckoutSession{}, fmt.Errorf("get checkout session: %w", classify(err))
	}

	session, err := translateSession(fetched)
	if err != nil {
		return application.CheckoutSession{}, fmt.Errorf("get checkout session: %w", err)
	}
	return session, nil
}

// GetSubscription reads the current provider state of a subscription.
func (g *Gateway) GetSubscription(ctx context.Context, id domain.StripeSubscriptionID) (application.Subscription, error) {
	if id.IsZero() {
		return application.Subscription{}, fmt.Errorf("get subscription: %w", application.ErrPaymentGatewayRequestInvalid)
	}

	ctx, cancel := g.deadline(ctx)
	defer cancel()

	fetched, err := g.clients.subscriptions.Get(id.String(), &stripe.SubscriptionParams{
		Params: stripe.Params{Context: ctx},
	})
	if err != nil {
		return application.Subscription{}, fmt.Errorf("get subscription: %w", classify(err))
	}

	translated, err := translateSubscription(fetched)
	if err != nil {
		return application.Subscription{}, fmt.Errorf("get subscription: %w", err)
	}
	return translated, nil
}

// CreatePortalSession opens the hosted customer portal for one customer.
// The return URL is allowlisted by the caller: this adapter only refuses an
// empty or malformed URL before opening a socket, the single-origin rule
// lives in the typed configuration like the checkout return URLs.
func (g *Gateway) CreatePortalSession(ctx context.Context, request application.CreatePortalSessionRequest) (application.PortalSession, error) {
	if request.CustomerID.IsZero() {
		return application.PortalSession{}, fmt.Errorf("create portal session: %w", application.ErrPaymentGatewayRequestInvalid)
	}
	if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
		return application.PortalSession{}, fmt.Errorf("create portal session: %w", err)
	}
	if request.ReturnURL == "" || !validBaseURL(request.ReturnURL) {
		return application.PortalSession{}, fmt.Errorf("create portal session: %w", application.ErrPaymentGatewayRequestInvalid)
	}

	ctx, cancel := g.deadline(ctx)
	defer cancel()

	created, err := g.clients.portal.New(&stripe.BillingPortalSessionParams{
		Params:    stripe.Params{Context: ctx, IdempotencyKey: stripe.String(request.IdempotencyKey)},
		Customer:  stripe.String(request.CustomerID.String()),
		ReturnURL: stripe.String(request.ReturnURL),
	})
	if err != nil {
		return application.PortalSession{}, fmt.Errorf("create portal session: %w", classify(err))
	}
	if created.URL == "" {
		return application.PortalSession{}, fmt.Errorf("create portal session: %w", misconfigured(errors.New("provider answered no portal url")))
	}
	return application.PortalSession{URL: created.URL}, nil
}

// deadline bounds one provider call. The caller's deadline wins when it is
// earlier, so a request that is already out of time is not extended.
func (g *Gateway) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, g.timeout)
}

// translateSession converts a provider checkout session into billing
// vocabulary. Every field is validated: an answer outside the shapes the
// database accepts is a contract violation, refused instead of stored.
func translateSession(session *stripe.CheckoutSession) (application.CheckoutSession, error) {
	if session == nil {
		return application.CheckoutSession{}, unavailable("the provider answered no checkout session")
	}

	status, err := domain.ParseCheckoutSessionStatus(string(session.Status))
	if err != nil {
		return application.CheckoutSession{}, misconfigured(err)
	}
	paymentStatus, err := domain.ParseCheckoutPaymentStatus(string(session.PaymentStatus))
	if err != nil {
		return application.CheckoutSession{}, misconfigured(err)
	}
	mode, err := domain.ParseCheckoutMode(string(session.Mode))
	if err != nil {
		return application.CheckoutSession{}, misconfigured(err)
	}
	id, err := domain.ParseStripeCheckoutSessionID(session.ID, session.Livemode)
	if err != nil {
		return application.CheckoutSession{}, misconfigured(err)
	}
	currency, err := domain.ParseCurrency(string(session.Currency))
	if err != nil {
		return application.CheckoutSession{}, misconfigured(err)
	}
	paymentIntentID := ""
	if session.PaymentIntent != nil {
		paymentIntentID = session.PaymentIntent.ID
	}
	intentID, err := domain.ParseStripePaymentIntentID(paymentIntentID)
	if err != nil {
		return application.CheckoutSession{}, misconfigured(err)
	}

	translated := application.CheckoutSession{
		ID:              id,
		Status:          status,
		PaymentStatus:   paymentStatus,
		Mode:            mode,
		PaymentIntentID: intentID,
		AmountMinor:     session.AmountTotal,
		Currency:        currency,
		ClientReference: session.ClientReferenceID,
		Livemode:        session.Livemode,
		URL:             session.URL,
	}
	if session.ExpiresAt > 0 {
		expiresAt := time.Unix(session.ExpiresAt, 0).UTC()
		translated.ExpiresAt = &expiresAt
	}
	return translated, nil
}

// translateSubscription converts a provider subscription into billing
// vocabulary.
func translateSubscription(subscription *stripe.Subscription) (application.Subscription, error) {
	if subscription == nil {
		return application.Subscription{}, unavailable("the provider answered no subscription")
	}

	status, err := domain.ParseSubscriptionStatus(string(subscription.Status))
	if err != nil {
		return application.Subscription{}, misconfigured(err)
	}
	if subscription.Customer == nil {
		return application.Subscription{}, misconfigured(errors.New("subscription carries no customer"))
	}
	customerID, err := domain.ParseStripeCustomerID(subscription.Customer.ID)
	if err != nil {
		return application.Subscription{}, misconfigured(err)
	}
	id, err := domain.ParseStripeSubscriptionID(subscription.ID)
	if err != nil {
		return application.Subscription{}, misconfigured(err)
	}
	if subscription.Items == nil || len(subscription.Items.Data) == 0 || subscription.Items.Data[0].Price == nil {
		return application.Subscription{}, misconfigured(errors.New("subscription carries no price"))
	}
	priceID, err := domain.ParseStripePriceID(subscription.Items.Data[0].Price.ID)
	if err != nil {
		return application.Subscription{}, misconfigured(err)
	}

	translated := application.Subscription{
		ID:                id,
		Status:            status,
		CustomerID:        customerID,
		PriceID:           priceID,
		CancelAtPeriodEnd: subscription.CancelAtPeriodEnd,
		Livemode:          subscription.Livemode,
	}
	switch {
	case subscription.CurrentPeriodStart == 0 && subscription.CurrentPeriodEnd == 0:
		// The provider reports no billed period: valid for a subscription
		// that never started.
	case subscription.CurrentPeriodStart == 0 || subscription.CurrentPeriodEnd == 0:
		return application.Subscription{}, misconfigured(errors.New("subscription carries an incomplete billing period"))
	default:
		period, periodErr := domain.NewBillingPeriod(
			time.Unix(subscription.CurrentPeriodStart, 0),
			time.Unix(subscription.CurrentPeriodEnd, 0),
		)
		if periodErr != nil {
			return application.Subscription{}, misconfigured(periodErr)
		}
		translated.CurrentPeriod = &period
	}
	return translated, nil
}

// classify maps any provider or transport failure into the application
// vocabulary. The provider's message is intentionally not part of the result:
// only its class, its code, its parameter and its request id travel, and the
// request id is what makes the full payload reachable in the provider's
// dashboard without carrying it through our logs.
func classify(err error) error {
	var providerErr *stripe.Error
	if errors.As(err, &providerErr) {
		return classifyProviderError(providerErr)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return unavailableOrTimeout(err, true)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return unavailableOrTimeout(urlErr, urlErr.Timeout())
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return unavailableOrTimeout(netErr, netErr.Timeout())
	}
	if errors.Is(err, context.Canceled) {
		// The caller went away (client disconnect, server shutdown). The
		// provider may still complete the operation, so the outcome is
		// unknown: the same class as an unavailable provider.
		return fmt.Errorf("%w: caller cancelled the request", application.ErrPaymentGatewayUnavailable)
	}

	// Anything else is an answer outside the contract this adapter accepts
	// (a body that is not a provider error object, an unparsable payload):
	// retrying the identical request would fail identically.
	return misconfigured(err)
}

// classifyProviderError maps one provider error object by HTTP status, which
// is what actually distinguishes a credential problem from a refusal and from
// a transient outage.
func classifyProviderError(providerErr *stripe.Error) error {
	detail := fmt.Sprintf("provider type %q status %d code %q param %q request_id %q",
		providerErr.Type, providerErr.HTTPStatusCode, providerErr.Code, providerErr.Param, providerErr.RequestID)

	switch {
	case providerErr.Type == stripe.ErrorTypeIdempotency:
		// The key was reused for a different request: a call-site defect, not
		// a provider failure.
		return fmt.Errorf("%w: %s", application.ErrPaymentGatewayRequestInvalid, detail)
	case providerErr.HTTPStatusCode == http.StatusUnauthorized || providerErr.HTTPStatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s", application.ErrPaymentGatewayMisconfigured, detail)
	case providerErr.HTTPStatusCode == http.StatusTooManyRequests || providerErr.HTTPStatusCode >= 500:
		return fmt.Errorf("%w: %s", application.ErrPaymentGatewayUnavailable, detail)
	case providerErr.HTTPStatusCode >= 400 && providerErr.HTTPStatusCode < 500:
		return fmt.Errorf("%w: %s", application.ErrPaymentGatewayRejected, detail)
	default:
		// A provider error object with a success or unrecognizable status.
		return fmt.Errorf("%w: %s", application.ErrPaymentGatewayMisconfigured, detail)
	}
}

// unavailableOrTimeout classifies a transport failure: a timeout means the
// call may still be running at the provider, anything else means it never
// completed.
func unavailableOrTimeout(err error, timeout bool) error {
	if timeout {
		return fmt.Errorf("%w: %v", application.ErrPaymentGatewayTimeout, redactTransportError(err))
	}
	return fmt.Errorf("%w: %v", application.ErrPaymentGatewayUnavailable, redactTransportError(err))
}

// redactTransportError renders a transport error without the request URL, so
// no path, identifier or query ever reaches a log through an error chain.
func redactTransportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s: %v", urlErr.Op, urlErr.Err)
	}
	return err
}

// misconfigured reports an answer or a rendering the adapter cannot accept as
// the provider contract.
func misconfigured(cause error) error {
	return fmt.Errorf("%w: %v", application.ErrPaymentGatewayMisconfigured, cause)
}

// unavailable reports a missing answer from the provider.
func unavailable(reason string) error {
	return fmt.Errorf("%w: %s", application.ErrPaymentGatewayUnavailable, reason)
}

// validateIdempotencyKey enforces the provider's own constraint before any
// request is built, so a mutating call can never reach the provider without a
// key that makes retries safe.
func validateIdempotencyKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: an idempotency key is required for this call", application.ErrPaymentGatewayRequestInvalid)
	}
	if len(key) > maxIdempotencyKeyLength {
		return fmt.Errorf("%w: the idempotency key is longer than %d characters", application.ErrPaymentGatewayRequestInvalid, maxIdempotencyKeyLength)
	}
	if !isPrintableASCII(key) {
		return fmt.Errorf("%w: the idempotency key contains characters outside printable ASCII", application.ErrPaymentGatewayRequestInvalid)
	}
	return nil
}

// validateSessionRequest checks everything the adapter must not let through to
// the provider: each failure is a call-site defect caught before the request.
func validateSessionRequest(request application.CreateCheckoutSessionRequest) error {
	switch {
	case request.CustomerID.IsZero():
		return fmt.Errorf("%w: the checkout session needs a customer", application.ErrPaymentGatewayRequestInvalid)
	case !request.PriceID.IsSet():
		return fmt.Errorf("%w: the checkout session needs a provisioned price", application.ErrPaymentGatewayRequestInvalid)
	case !request.Mode.IsValid():
		return fmt.Errorf("%w: the checkout session mode is outside the supported vocabulary", application.ErrPaymentGatewayRequestInvalid)
	case request.ClientReference == "" || len(request.ClientReference) > maxClientReferenceLength || !isPrintableASCII(request.ClientReference):
		return fmt.Errorf("%w: the checkout session needs a client reference of at most %d printable characters", application.ErrPaymentGatewayRequestInvalid, maxClientReferenceLength)
	case !validReturnURL(request.SuccessURL):
		return fmt.Errorf("%w: the success URL must be an absolute HTTP(S) URL", application.ErrPaymentGatewayRequestInvalid)
	case !validReturnURL(request.CancelURL):
		return fmt.Errorf("%w: the cancel URL must be an absolute HTTP(S) URL", application.ErrPaymentGatewayRequestInvalid)
	}
	return validateIdempotencyKey(request.IdempotencyKey)
}

// validReturnURL accepts only absolute HTTP(S) URLs, so the adapter can never
// hand the buyer to another scheme. Which hosts are allowed is a decision of
// the use case that owns the allowlist (P12-T04), not of the adapter.
func validReturnURL(raw string) bool {
	return validBaseURL(raw)
}

// validBaseURL reports whether the value is an absolute HTTP(S) URL.
func validBaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return parsed.Host != ""
}

// isPrintableASCII reports whether every byte is printable ASCII, the only
// character set the provider accepts in these header values.
func isPrintableASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if character := value[index]; character < '!' || character > '~' {
			return false
		}
	}
	return true
}

// secretKey wraps the provider credential so it can only be revealed
// deliberately, mirroring the platform configuration Secret: it renders
// redacted under every fmt verb.
type secretKey string

// isSet reports whether a credential is present.
func (key secretKey) isSet() bool { return key != "" }

// String implements fmt.Stringer with an always-redacted representation.
func (key secretKey) String() string { return "[REDACTED]" }

// GoString implements fmt.GoStringer, closing the %#v leak path.
func (key secretKey) GoString() string { return "[REDACTED]" }

// reveal exposes the credential to the SDK clients. It is the single hand-off
// point of the secret in this package.
func (key secretKey) reveal() string { return string(key) }
