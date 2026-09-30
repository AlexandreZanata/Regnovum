package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// maxProviderIdempotencyKeyLength is the provider's documented bound for an
// Idempotency-Key header. The adapter enforces it too; the use case refuses to
// build a key that could never reach the provider, so a configuration mistake
// surfaces here and not as a refused purchase.
const maxProviderIdempotencyKeyLength = 255

// checkoutKeyPrefix and customerKeyPrefix namespace the keys the server
// derives, so a retry of one operation can never be mistaken for another.
const (
	checkoutKeyPrefix = "checkout:"
	customerKeyPrefix = "customer:"
)

// CheckoutReturnURLs are the allowlisted return URLs of this deployment: where
// the provider sends the buyer after paying and after cancelling.
//
// They are configuration, not input (P12-T04): no request field can add to,
// replace or influence them, so a caller cannot turn the checkout into an open
// redirect, and the provider never receives a URL this deployment did not
// declare.
//
// The use case requires both to be present but deliberately does NOT parse
// them: URL syntax is a transport detail, and domain/application layers may not
// import net/url (internal/architecture_test.go). The allowlist is enforced
// where a URL is actually handled — the typed configuration refuses a
// non-absolute, credentialed, fragment-bearing or foreign-origin URL at boot
// (internal/platform/config), and the payment adapter refuses a malformed URL
// before it opens a socket (internal/billing/adapters/stripe).
type CheckoutReturnURLs struct {
	Success string
	Cancel  string
}

// CheckoutDependencies are everything the checkout use case drives. They are
// passed together because the use case is only meaningful when all of them are
// present, and the constructor fails fast when one is missing.
type CheckoutDependencies struct {
	// Catalog is the versioned price list in force, the only source of the
	// product, the amount and the currency.
	Catalog *domain.Catalog
	// Gateway reaches the payment provider through the billing port.
	Gateway PaymentGateway
	// Purchasers answers the eligibility question.
	Purchasers PurchaserDirectory
	// Customers persists the account→provider customer correlation.
	Customers StripeCustomerRepository
	// Intents persists the commercial decision.
	Intents CheckoutIntentRepository
	// Clock supplies the instants of the local record.
	Clock Clock
	// Returns carries the allowlisted return URLs.
	Returns CheckoutReturnURLs
}

// CreateCheckoutCommand is everything a caller may name: which account buys,
// which product, in which commercial region, under which operation token.
//
// Amount, currency, price identifier and return URLs are deliberately absent:
// the server resolves them from the versioned catalog and from configuration,
// so the browser can never propose a price or a destination.
type CreateCheckoutCommand struct {
	AccountID string
	// Market is the commercial region the buyer chose explicitly. It is never
	// inferred from an IP address (docs/MONETIZATION.md §4).
	Market string
	// Product is the catalog product being bought.
	Product string
	// IdempotencyKey is the caller's operation token: the same token must
	// always describe the same logical purchase, so a retry resolves the
	// session already created instead of provisioning a second one. The
	// server namespaces it by account before it reaches the provider, and a
	// token reused with different parameters is refused by the provider
	// (surfacing as ErrPaymentGatewayRequestInvalid) instead of being
	// silently charged.
	IdempotencyKey string
}

// CheckoutResult is the outcome of one checkout attempt: the local intent, the
// provider session state and the URL the buyer must be sent to.
type CheckoutResult struct {
	// IntentID is the local record of the commercial decision.
	IntentID string
	// Status is the local lifecycle; only a settled intent ever produces an
	// entitlement, and only a webhook settles one.
	Status domain.CheckoutIntentStatus
	// SessionStatus is the provider's own view of the session. A caller must
	// only redirect the buyer when it is open: complete means the buyer
	// already submitted the session and expired means it can no longer be
	// paid.
	SessionStatus  domain.CheckoutSessionStatus
	SessionID      domain.StripeCheckoutSessionID
	Market         domain.Market
	ProductID      domain.ProductID
	CatalogVersion int
	Amount         domain.Money
	RedirectURL    string
	ExpiresAt      *time.Time
	Replayed       bool
}

// CreateCheckoutUseCase creates the checkout of one catalog product for one
// eligible account, resolving every commercial fact on the server.
type CreateCheckoutUseCase struct {
	catalog    *domain.Catalog
	gateway    PaymentGateway
	purchasers PurchaserDirectory
	customers  StripeCustomerRepository
	intents    CheckoutIntentRepository
	clock      Clock
	returns    CheckoutReturnURLs
}

// NewCreateCheckoutUseCase builds the use case, refusing an incomplete or
// incoherent composition: it is better to fail at boot than to discover a
// missing return URL in the middle of a purchase.
func NewCreateCheckoutUseCase(dependencies CheckoutDependencies) (*CreateCheckoutUseCase, error) {
	if dependencies.Catalog == nil {
		return nil, fmt.Errorf("%w: the versioned catalog is required", ErrInvalidCheckoutConfig)
	}
	if dependencies.Gateway == nil {
		return nil, fmt.Errorf("%w: the payment gateway port is required", ErrInvalidCheckoutConfig)
	}
	if dependencies.Purchasers == nil {
		return nil, fmt.Errorf("%w: the purchaser directory port is required", ErrInvalidCheckoutConfig)
	}
	if dependencies.Customers == nil {
		return nil, fmt.Errorf("%w: the provider customer port is required", ErrInvalidCheckoutConfig)
	}
	if dependencies.Intents == nil {
		return nil, fmt.Errorf("%w: the checkout intent port is required", ErrInvalidCheckoutConfig)
	}
	if dependencies.Clock == nil {
		return nil, fmt.Errorf("%w: a clock is required", ErrInvalidCheckoutConfig)
	}
	if err := validateReturnURLs(dependencies.Returns); err != nil {
		return nil, err
	}

	return &CreateCheckoutUseCase{
		catalog:    dependencies.Catalog,
		gateway:    dependencies.Gateway,
		purchasers: dependencies.Purchasers,
		customers:  dependencies.Customers,
		intents:    dependencies.Intents,
		clock:      dependencies.Clock,
		returns:    dependencies.Returns,
	}, nil
}

// Execute resolves the product from the catalog, authorizes the purchase,
// provisions the provider objects and persists the local intent exactly once
// per provider session.
func (uc *CreateCheckoutUseCase) Execute(ctx context.Context, command CreateCheckoutCommand) (*CheckoutResult, error) {
	accountID := domain.AccountID(command.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	market, err := domain.ParseMarket(command.Market)
	if err != nil {
		return nil, err
	}
	productID, err := domain.ParseProductID(command.Product)
	if err != nil {
		return nil, err
	}
	key, err := domain.ParseIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return nil, err
	}

	// The amount and the currency come from the versioned catalog and nowhere
	// else. An unknown product, a product without a price in this environment
	// and a region this deployment does not serve are all refused here, before
	// any provider object exists.
	product, err := uc.catalog.Product(market, productID)
	if err != nil {
		return nil, err
	}

	purchaser, err := uc.purchasers.PurchaserForCheckout(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !purchaser.Eligible {
		return nil, ErrPurchaserNotEligible
	}

	customer, err := uc.ensureCustomer(ctx, accountID)
	if err != nil {
		return nil, err
	}

	checkoutKey, err := deriveKey(checkoutKeyPrefix, accountID, key)
	if err != nil {
		return nil, err
	}
	session, err := uc.gateway.CreateCheckoutSession(ctx, CreateCheckoutSessionRequest{
		CustomerID: customer.CustomerID,
		PriceID:    product.PriceID(),
		Mode:       modeFor(product.Grant().Kind()),
		SuccessURL: uc.returns.Success,
		CancelURL:  uc.returns.Cancel,
		// The provider session carries the caller's operation token, so an
		// inspection in the provider dashboard correlates the session with the
		// attempt; our own correlation is the session identifier, which is
		// unique and recorded locally.
		ClientReference: key.String(),
		IdempotencyKey:  checkoutKey,
	})
	if err != nil {
		return nil, err
	}

	if session.Livemode != customer.Livemode {
		return nil, ErrProviderModeChanged
	}
	if err := verifyChargedPrice(product, session); err != nil {
		return nil, err
	}

	// A session the provider reports as no longer payable is recorded as an
	// expired intent: the attempt is a fact, and the buyer must start a new
	// one. A completed session is still recorded as open, because settlement
	// belongs to the verified webhook and to nothing else.
	status := domain.CheckoutIntentOpen
	var closedAt *time.Time
	if session.Status == domain.CheckoutStatusExpired {
		status = domain.CheckoutIntentExpired
		closed := uc.clock.Now().UTC()
		closedAt = &closed
	}

	recorded, err := uc.intents.RecordCheckoutIntent(ctx, RecordCheckoutIntentRequest{
		AccountID:      accountID,
		Market:         product.Market(),
		ProductID:      product.ID(),
		CatalogVersion: uc.catalog.Version(),
		Amount:         product.Amount(),
		Livemode:       session.Livemode,
		SessionID:      session.ID,
		Status:         status,
		ClosedAt:       closedAt,
	})
	if err != nil {
		return nil, err
	}

	return &CheckoutResult{
		IntentID:       recorded.Intent.ID,
		Status:         recorded.Intent.Status,
		SessionStatus:  session.Status,
		SessionID:      session.ID,
		Market:         recorded.Intent.Market,
		ProductID:      recorded.Intent.ProductID,
		CatalogVersion: recorded.Intent.CatalogVersion,
		Amount:         recorded.Intent.Amount,
		RedirectURL:    session.URL,
		ExpiresAt:      session.ExpiresAt,
		Replayed:       recorded.Replayed,
	}, nil
}

// ensureCustomer resolves the provider customer of the account, provisioning it
// once when the account has none. The provisioning key is derived from the
// account alone, so two simultaneous attempts collapse into one customer at the
// provider and into one mapping locally.
func (uc *CreateCheckoutUseCase) ensureCustomer(ctx context.Context, accountID domain.AccountID) (*StripeCustomerRecord, error) {
	stored, err := uc.customers.StripeCustomer(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if stored != nil {
		return stored, nil
	}

	key, err := deriveKey(customerKeyPrefix, accountID, domain.IdempotencyKey{})
	if err != nil {
		return nil, err
	}
	customer, err := uc.gateway.CreateCustomer(ctx, CreateCustomerRequest{IdempotencyKey: key})
	if err != nil {
		return nil, err
	}

	record, err := uc.customers.RecordStripeCustomer(ctx, RecordStripeCustomerRequest{
		AccountID:  accountID,
		CustomerID: customer.ID,
		Livemode:   customer.Livemode,
	})
	if err != nil {
		return nil, err
	}
	return record, nil
}

// modeFor maps the entitlement a product confers onto the checkout the
// provider must open: the recurring Member is a subscription, the one-off INK
// pack and Arena Pass are single payments.
func modeFor(kind domain.GrantKind) domain.CheckoutMode {
	if kind == domain.GrantKindMember {
		return domain.CheckoutModeSubscription
	}
	return domain.CheckoutModePayment
}

// deriveKey namespaces a provider idempotency key by account and operation, so
// a key reused by another account (or by another operation of the same account)
// can never collide at the provider.
func deriveKey(prefix string, accountID domain.AccountID, key domain.IdempotencyKey) (string, error) {
	derived := prefix + accountID.String()
	if !key.IsZero() {
		derived += ":" + key.String()
	}
	if len(derived) > maxProviderIdempotencyKeyLength {
		return "", domain.ErrIdempotencyKeyTooLong
	}
	return derived, nil
}

// verifyChargedPrice refuses a provider session that would charge something
// other than the price the catalog resolved: a price edited directly in the
// provider dashboard must never make the buyer pay a different amount than the
// one the server decided (and recorded).
func verifyChargedPrice(product domain.Product, session CheckoutSession) error {
	if session.Currency != product.Currency() {
		return fmt.Errorf("%w: provider charges %s, catalog resolves %s",
			ErrProviderAmountMismatch, session.Currency, product.Currency())
	}
	if session.AmountMinor == 0 && session.Mode == domain.CheckoutModeSubscription {
		// A recurring checkout may report no total at all: the amount of a
		// subscription lives on the subscription, not on the session.
		return nil
	}
	if session.AmountMinor != product.Amount().MinorUnits() {
		return fmt.Errorf("%w: provider charges %d minor units, catalog resolves %d",
			ErrProviderAmountMismatch, session.AmountMinor, product.Amount().MinorUnits())
	}
	return nil
}

// validateReturnURLs requires the deployment to have declared both return
// URLs, so an attempt can never be created without a destination for the buyer.
// Their syntax and their single-origin rule belong to the two layers that
// actually handle URLs (see CheckoutReturnURLs): the use case only refuses to
// run without them.
func validateReturnURLs(returns CheckoutReturnURLs) error {
	if strings.TrimSpace(returns.Success) == "" {
		return fmt.Errorf("%w: the success return URL is required", ErrInvalidCheckoutConfig)
	}
	if strings.TrimSpace(returns.Cancel) == "" {
		return fmt.Errorf("%w: the cancel return URL is required", ErrInvalidCheckoutConfig)
	}
	return nil
}
