package testsupport

import (
	"time"

	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// The synthetic commercial values of a scenario. The amounts are the minor
// units of a currency, never a float, and the reference names the fixture.
const (
	defaultInkAmount        = 1000
	defaultPassQuantity     = int32(1)
	defaultReference        = "testsupport-scenario"
	defaultProductID        = "arena_pass_single"
	defaultAmountMinorUnits = int64(2990)
)

// Ink builds one non-negative INK quantity.
func (b *Builder) Ink(amount int64) (walletdomain.Ink, error) {
	b.t.Helper()
	return walletdomain.NewInk(amount)
}

// Allocation builds a spend plan drawn from the two buckets, which is what a
// debit of a mixed balance is decided with.
func (b *Builder) Allocation(fromFree, fromPurchased int64) (walletdomain.Allocation, error) {
	b.t.Helper()
	free, err := walletdomain.NewInk(fromFree)
	if err != nil {
		return walletdomain.Allocation{}, err
	}
	purchased, err := walletdomain.NewInk(fromPurchased)
	if err != nil {
		return walletdomain.Allocation{}, err
	}
	allocation := walletdomain.NewAllocation(free, purchased)
	if _, err := allocation.Total(); err != nil {
		return walletdomain.Allocation{}, err
	}
	return allocation, nil
}

// CreditOption varies the INK credit a builder makes.
type CreditOption func(*creditSpec)

type creditSpec struct {
	bucket        string
	operationType string
	reference     string
}

// WithCreditBucket credits the FREE_INK or PURCHASED_INK bucket, for the
// scenario that reads the split back.
func WithCreditBucket(bucket string) CreditOption {
	return func(spec *creditSpec) { spec.bucket = bucket }
}

// WithCreditOperationType credits through a specific operation type, which is
// what separates a member grant from a purchase in the ledger.
func WithCreditOperationType(operationType string) CreditOption {
	return func(spec *creditSpec) { spec.operationType = operationType }
}

// WithCreditReference credits with a specific stable reference.
func WithCreditReference(reference string) CreditOption {
	return func(spec *creditSpec) { spec.reference = reference }
}

// CreditInk builds the command that gives an account INK: the amount, the
// bucket, the operation type, the reference and an idempotency key of the
// scenario's own, every one of them parsed by the wallet domain.
//
// The key is drawn per call, so replaying one credit means holding the command
// this builder answered, not calling it twice.
func (b *Builder) CreditInk(account *identitydomain.Account, amount int64, options ...CreditOption) (walletapp.CreditInkCommand, error) {
	b.t.Helper()
	if account == nil {
		return walletapp.CreditInkCommand{}, walletdomain.ErrEmptyAccountID
	}
	spec := creditSpec{
		bucket:        string(walletdomain.BucketFree),
		operationType: string(walletdomain.OperationCreditFree),
		reference:     defaultReference,
	}
	for _, option := range options {
		option(&spec)
	}

	// The quantity is judged before the command exists: a credit of nothing is
	// not a scenario with a smaller number, it is a ledger line the product
	// refuses.
	switch {
	case amount < 0:
		return walletapp.CreditInkCommand{}, walletdomain.ErrNegativeInk
	case amount == 0:
		return walletapp.CreditInkCommand{}, walletdomain.ErrZeroAmount
	}
	if _, err := walletdomain.NewInk(amount); err != nil {
		return walletapp.CreditInkCommand{}, err
	}
	bucket, err := walletdomain.ParseBucket(spec.bucket)
	if err != nil {
		return walletapp.CreditInkCommand{}, err
	}
	operationType, err := walletdomain.ParseOperationType(spec.operationType)
	if err != nil {
		return walletapp.CreditInkCommand{}, err
	}
	reference, err := walletdomain.ParseReference(spec.reference)
	if err != nil {
		return walletapp.CreditInkCommand{}, err
	}
	key, err := walletdomain.ParseIdempotencyKey(b.id())
	if err != nil {
		return walletapp.CreditInkCommand{}, err
	}

	return walletapp.CreditInkCommand{
		AccountID:      account.ID().String(),
		Bucket:         bucket.String(),
		OperationType:  operationType.String(),
		Amount:         amount,
		Reference:      reference.String(),
		IdempotencyKey: key.String(),
	}, nil
}

// PassLotOption varies the entitlement a builder makes.
type PassLotOption func(*passLotSpec)

type passLotSpec struct {
	quantity  int32
	expiresAt *time.Time
}

// WithPassQuantity grants a specific number of Arena Passes.
func WithPassQuantity(quantity int32) PassLotOption {
	return func(spec *passLotSpec) { spec.quantity = quantity }
}

// WithPassExpiry grants passes that expire at a specific instant, which is how
// a scenario crosses the expiry without waiting for it.
func WithPassExpiry(expiresAt time.Time) PassLotOption {
	return func(spec *passLotSpec) {
		instant := expiresAt.UTC()
		spec.expiresAt = &instant
	}
}

// PassLot builds the entitlement record of a purchased Arena Pass: the owner,
// the origin, the granted quantity, what remains and the stable reference of
// the cause. The defaults are a single, fully available, non-expiring pass,
// which is the state a published Arena consumes.
func (b *Builder) PassLot(account *identitydomain.Account, options ...PassLotOption) (*billingdomain.PassLot, error) {
	b.t.Helper()
	if account == nil {
		return nil, billingdomain.ErrEmptyAccountID
	}
	spec := passLotSpec{quantity: defaultPassQuantity}
	for _, option := range options {
		option(&spec)
	}
	quantity, err := billingdomain.NewQuantity(spec.quantity)
	if err != nil {
		return nil, err
	}
	reference, err := billingdomain.ParseReference(defaultReference)
	if err != nil {
		return nil, err
	}
	return billingdomain.ReconstitutePassLot(
		billingdomain.LotID(b.Identifier()),
		billingdomain.AccountID(account.ID().String()),
		billingdomain.OriginPurchase,
		quantity,
		quantity.Int32(),
		spec.expiresAt,
		reference,
		b.Now(),
	)
}

// BillingOption varies the sellable catalog a builder makes.
type BillingOption func(*billingSpec)

type billingSpec struct {
	market       string
	amount       int64
	passQuantity int32
	productID    string
	stripePrice  string
}

// WithBillingMarket sells in a specific market, which also decides the
// currency.
func WithBillingMarket(market string) BillingOption {
	return func(spec *billingSpec) { spec.market = market }
}

// WithBillingAmount sells for a specific price in minor units.
func WithBillingAmount(minorUnits int64) BillingOption {
	return func(spec *billingSpec) { spec.amount = minorUnits }
}

// WithBillingPassQuantity sells a grant of a specific number of Arena Passes.
func WithBillingPassQuantity(quantity int32) BillingOption {
	return func(spec *billingSpec) { spec.passQuantity = quantity }
}

// WithBillingProductID sells under a specific product identifier.
func WithBillingProductID(productID string) BillingOption {
	return func(spec *billingSpec) { spec.productID = productID }
}

// WithBillingStripePrice attaches the provider price the product is sold
// through. The empty value is the documented "not provisioned in this
// environment" state, which is why it is the default.
func WithBillingStripePrice(priceID string) BillingOption {
	return func(spec *billingSpec) { spec.stripePrice = priceID }
}

// Product builds one catalog entry: the market decides the currency, the price
// is an integer in minor units and the grant is the Arena Pass it sells.
func (b *Builder) Product(options ...BillingOption) (billingdomain.Product, error) {
	b.t.Helper()
	spec, err := b.billingSpec(options...)
	if err != nil {
		return billingdomain.Product{}, err
	}
	market, err := billingdomain.ParseMarket(spec.market)
	if err != nil {
		return billingdomain.Product{}, err
	}
	currency, err := market.Currency()
	if err != nil {
		return billingdomain.Product{}, err
	}
	amount, err := billingdomain.NewMoney(spec.amount, currency)
	if err != nil {
		return billingdomain.Product{}, err
	}
	productID, err := billingdomain.ParseProductID(spec.productID)
	if err != nil {
		return billingdomain.Product{}, err
	}
	grant, err := billingdomain.NewArenaPassGrant(spec.passQuantity)
	if err != nil {
		return billingdomain.Product{}, err
	}
	price, err := billingdomain.ParseStripePriceID(spec.stripePrice)
	if err != nil {
		return billingdomain.Product{}, err
	}
	return billingdomain.NewProduct(market, productID, amount, grant, price)
}

// Catalog builds the versioned catalog a checkout is decided against, with one
// entry unless the caller adds products of its own.
func (b *Builder) Catalog(options ...BillingOption) (*billingdomain.Catalog, error) {
	b.t.Helper()
	product, err := b.Product(options...)
	if err != nil {
		return nil, err
	}
	return billingdomain.NewCatalog(1, []billingdomain.Product{product})
}

// billingSpec resolves the options, leaving the parsing to the builder that
// consumes them.
func (b *Builder) billingSpec(options ...BillingOption) (billingSpec, error) {
	b.t.Helper()
	spec := billingSpec{
		market:       string(billingdomain.MarketBrazil),
		amount:       defaultAmountMinorUnits,
		passQuantity: defaultPassQuantity,
		productID:    defaultProductID,
	}
	for _, option := range options {
		option(&spec)
	}
	return spec, nil
}
