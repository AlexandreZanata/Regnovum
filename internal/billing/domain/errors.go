package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	CodeEmptyAccountID      ErrorCode = "BILLING_EMPTY_ACCOUNT_ID"
	CodeEmptyLotID          ErrorCode = "BILLING_EMPTY_LOT_ID"
	CodeInvalidPassOrigin   ErrorCode = "BILLING_INVALID_PASS_ORIGIN"
	CodeInvalidQuantity     ErrorCode = "BILLING_INVALID_QUANTITY"
	CodeInvalidRemaining    ErrorCode = "BILLING_INVALID_REMAINING"
	CodeEmptyReference      ErrorCode = "BILLING_EMPTY_REFERENCE"
	CodeInvalidReference    ErrorCode = "BILLING_INVALID_REFERENCE"
	CodeReferenceTooLong    ErrorCode = "BILLING_REFERENCE_TOO_LONG"
	CodeExpirationRequired  ErrorCode = "BILLING_EXPIRATION_REQUIRED"
	CodeExpirationForbidden ErrorCode = "BILLING_EXPIRATION_FORBIDDEN"
	CodeEmptyArenaID        ErrorCode = "BILLING_EMPTY_ARENA_ID"
	CodeInvalidArenaID      ErrorCode = "BILLING_INVALID_ARENA_ID"
	CodeNoPassAvailable     ErrorCode = "BILLING_NO_PASS_AVAILABLE"

	// Catalog errors (P12-T01): the versioned price list and the commercial
	// regions of the deployment.
	CodeInvalidMarket          ErrorCode = "BILLING_INVALID_MARKET"
	CodeDuplicateMarket        ErrorCode = "BILLING_DUPLICATE_MARKET"
	CodeUnsupportedCurrency    ErrorCode = "BILLING_UNSUPPORTED_CURRENCY"
	CodeMarketCurrencyMismatch ErrorCode = "BILLING_MARKET_CURRENCY_MISMATCH"
	CodeInvalidMoney           ErrorCode = "BILLING_INVALID_MONEY"
	CodeInvalidProductID       ErrorCode = "BILLING_INVALID_PRODUCT_ID"
	CodeDuplicateProduct       ErrorCode = "BILLING_DUPLICATE_PRODUCT"
	CodeUnknownProduct         ErrorCode = "BILLING_UNKNOWN_PRODUCT"
	CodeProductNotPriced       ErrorCode = "BILLING_PRODUCT_NOT_PRICED"
	CodeUnknownMarket          ErrorCode = "BILLING_UNKNOWN_MARKET"
	CodeMarketNotEnabled       ErrorCode = "BILLING_MARKET_NOT_ENABLED"
	CodeDuplicatePrice         ErrorCode = "BILLING_DUPLICATE_PRICE"
	CodeInvalidStripePriceID   ErrorCode = "BILLING_INVALID_STRIPE_PRICE_ID"
	CodePriceIDRequired        ErrorCode = "BILLING_PRICE_ID_REQUIRED"
	CodeInvalidCatalogVersion  ErrorCode = "BILLING_INVALID_CATALOG_VERSION"
	CodeInvalidGrant           ErrorCode = "BILLING_INVALID_GRANT"
	CodeNoMarketEnabled        ErrorCode = "BILLING_NO_MARKET_ENABLED"

	// Provider vocabulary errors (P12-T03): the identifiers and statuses the
	// payment gateway port exchanges, mirrored from migration 00020.
	CodeInvalidStripeCustomerID      ErrorCode = "BILLING_INVALID_STRIPE_CUSTOMER_ID"
	CodeInvalidStripeSessionID       ErrorCode = "BILLING_INVALID_STRIPE_SESSION_ID"
	CodeStripeSessionModeMismatch    ErrorCode = "BILLING_STRIPE_SESSION_MODE_MISMATCH"
	CodeInvalidStripePaymentIntentID ErrorCode = "BILLING_INVALID_STRIPE_PAYMENT_INTENT_ID"
	CodeInvalidStripeSubscriptionID  ErrorCode = "BILLING_INVALID_STRIPE_SUBSCRIPTION_ID"
	CodeInvalidCheckoutSessionStatus ErrorCode = "BILLING_INVALID_CHECKOUT_SESSION_STATUS"
	CodeInvalidSubscriptionStatus    ErrorCode = "BILLING_INVALID_SUBSCRIPTION_STATUS"
	CodeInvalidCheckoutMode          ErrorCode = "BILLING_INVALID_CHECKOUT_MODE"
	CodeInvalidBillingPeriod         ErrorCode = "BILLING_INVALID_BILLING_PERIOD"
	CodeInvalidCheckoutPaymentStatus ErrorCode = "BILLING_INVALID_CHECKOUT_PAYMENT_STATUS"

	// Checkout intent errors (P12-T04): the local record of one purchase
	// attempt and the retry key that makes it replayable.
	CodeEmptyIdempotencyKey         ErrorCode = "BILLING_EMPTY_IDEMPOTENCY_KEY"
	CodeInvalidIdempotencyKey       ErrorCode = "BILLING_INVALID_IDEMPOTENCY_KEY"
	CodeIdempotencyKeyTooLong       ErrorCode = "BILLING_IDEMPOTENCY_KEY_TOO_LONG"
	CodeInvalidCheckoutIntentStatus ErrorCode = "BILLING_INVALID_CHECKOUT_INTENT_STATUS"

	// Webhook errors (P12-T05): the verified inbound event from the provider.
	CodeEmptyWebhookEventID     ErrorCode = "BILLING_EMPTY_WEBHOOK_EVENT_ID"
	CodeInvalidWebhookEventID   ErrorCode = "BILLING_INVALID_WEBHOOK_EVENT_ID"
	CodeEmptyWebhookEventType   ErrorCode = "BILLING_EMPTY_WEBHOOK_EVENT_TYPE"
	CodeInvalidWebhookEventType ErrorCode = "BILLING_INVALID_WEBHOOK_EVENT_TYPE"

	// Refund errors (P12-T09): explicit policy for money going back.
	CodeInvalidStripeRefundID  ErrorCode = "BILLING_INVALID_STRIPE_REFUND_ID"
	CodeInvalidStripeDisputeID ErrorCode = "BILLING_INVALID_STRIPE_DISPUTE_ID"
	CodeInvalidStripeChargeID  ErrorCode = "BILLING_INVALID_STRIPE_CHARGE_ID"
	CodeInvalidRefundSource    ErrorCode = "BILLING_INVALID_REFUND_SOURCE"
	CodeInvalidRefundStatus    ErrorCode = "BILLING_INVALID_REFUND_STATUS"
	CodeInvalidRefundAmount    ErrorCode = "BILLING_INVALID_REFUND_AMOUNT"
	CodeInvalidRefundAssess    ErrorCode = "BILLING_INVALID_REFUND_ASSESSMENT"

	// Reconciliation errors (P12-T10): windowed comparison without silent
	// correction.
	CodeInvalidReconciliationKind   ErrorCode = "BILLING_INVALID_RECONCILIATION_KIND"
	CodeInvalidReconciliationWindow ErrorCode = "BILLING_INVALID_RECONCILIATION_WINDOW"
	CodeInvalidSeason               ErrorCode = "BILLING_INVALID_SEASON"
)

// DomainError represents an invariant or rule failure in the billing domain.
type DomainError struct {
	Code    ErrorCode
	Message string
}

func (e DomainError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e DomainError) Is(target error) bool {
	t, ok := target.(DomainError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

var (
	ErrEmptyAccountID      = DomainError{Code: CodeEmptyAccountID, Message: "account identifier cannot be empty"}
	ErrEmptyLotID          = DomainError{Code: CodeEmptyLotID, Message: "pass lot identifier cannot be empty"}
	ErrInvalidPassOrigin   = DomainError{Code: CodeInvalidPassOrigin, Message: "pass origin is unrecognized"}
	ErrInvalidQuantity     = DomainError{Code: CodeInvalidQuantity, Message: "pass quantity must be a positive integer"}
	ErrInvalidRemaining    = DomainError{Code: CodeInvalidRemaining, Message: "remaining passes must stay between zero and the granted quantity"}
	ErrEmptyReference      = DomainError{Code: CodeEmptyReference, Message: "grant reference cannot be empty"}
	ErrInvalidReference    = DomainError{Code: CodeInvalidReference, Message: "grant reference contains unsupported characters"}
	ErrReferenceTooLong    = DomainError{Code: CodeReferenceTooLong, Message: "grant reference exceeds the maximum allowed length"}
	ErrExpirationRequired  = DomainError{Code: CodeExpirationRequired, Message: "this pass origin requires an expiration"}
	ErrExpirationForbidden = DomainError{Code: CodeExpirationForbidden, Message: "this pass origin does not expire"}
	ErrEmptyArenaID        = DomainError{Code: CodeEmptyArenaID, Message: "arena identifier cannot be empty"}
	ErrInvalidArenaID      = DomainError{Code: CodeInvalidArenaID, Message: "arena identifier contains unsupported characters"}
	ErrNoPassAvailable     = DomainError{Code: CodeNoPassAvailable, Message: "no valid arena pass is available"}

	ErrInvalidMarket          = DomainError{Code: CodeInvalidMarket, Message: "market is outside the supported commercial regions"}
	ErrDuplicateMarket        = DomainError{Code: CodeDuplicateMarket, Message: "market is enabled more than once"}
	ErrUnsupportedCurrency    = DomainError{Code: CodeUnsupportedCurrency, Message: "currency is outside the supported ISO 4217 vocabulary"}
	ErrMarketCurrencyMismatch = DomainError{Code: CodeMarketCurrencyMismatch, Message: "currency does not match the currency this market charges"}
	ErrInvalidMoney           = DomainError{Code: CodeInvalidMoney, Message: "monetary amount must be a positive number of minor units"}
	ErrInvalidProductID       = DomainError{Code: CodeInvalidProductID, Message: "product identifier is not lower snake case of three to 64 characters"}
	ErrDuplicateProduct       = DomainError{Code: CodeDuplicateProduct, Message: "catalog carries two entries for the same market and product"}
	ErrUnknownProduct         = DomainError{Code: CodeUnknownProduct, Message: "product is not part of the catalog"}
	ErrProductNotPriced       = DomainError{Code: CodeProductNotPriced, Message: "product has no Stripe price provisioned in this environment"}
	ErrUnknownMarket          = DomainError{Code: CodeUnknownMarket, Message: "market has no products in the catalog"}
	ErrMarketNotEnabled       = DomainError{Code: CodeMarketNotEnabled, Message: "market is not enabled in this deployment"}
	ErrDuplicatePrice         = DomainError{Code: CodeDuplicatePrice, Message: "Stripe price is configured twice for the same market and product"}
	ErrInvalidStripePriceID   = DomainError{Code: CodeInvalidStripePriceID, Message: "Stripe price identifier is malformed"}
	ErrPriceIDRequired        = DomainError{Code: CodePriceIDRequired, Message: "production requires a Stripe price for every enabled product"}
	ErrInvalidCatalogVersion  = DomainError{Code: CodeInvalidCatalogVersion, Message: "catalog version must be a positive integer"}
	ErrInvalidGrant           = DomainError{Code: CodeInvalidGrant, Message: "grant does not carry exactly the quantities its kind describes"}
	ErrNoMarketEnabled        = DomainError{Code: CodeNoMarketEnabled, Message: "production requires at least one enabled commercial region"}

	ErrInvalidStripeCustomerID           = DomainError{Code: CodeInvalidStripeCustomerID, Message: "Stripe customer identifier is malformed"}
	ErrInvalidStripeCheckoutSessionID    = DomainError{Code: CodeInvalidStripeSessionID, Message: "Stripe checkout session identifier is malformed"}
	ErrStripeCheckoutSessionModeMismatch = DomainError{Code: CodeStripeSessionModeMismatch, Message: "Stripe checkout session belongs to the other provider mode"}
	ErrInvalidStripePaymentIntentID      = DomainError{Code: CodeInvalidStripePaymentIntentID, Message: "Stripe payment intent identifier is malformed"}
	ErrInvalidStripeSubscriptionID       = DomainError{Code: CodeInvalidStripeSubscriptionID, Message: "Stripe subscription identifier is malformed"}
	ErrInvalidCheckoutSessionStatus      = DomainError{Code: CodeInvalidCheckoutSessionStatus, Message: "checkout session status is outside the provider vocabulary"}
	ErrInvalidSubscriptionStatus         = DomainError{Code: CodeInvalidSubscriptionStatus, Message: "subscription status is outside the provider vocabulary"}
	ErrInvalidCheckoutMode               = DomainError{Code: CodeInvalidCheckoutMode, Message: "checkout mode is outside the supported vocabulary"}
	ErrInvalidBillingPeriod              = DomainError{Code: CodeInvalidBillingPeriod, Message: "billing period must be a complete interval with the end after the start"}
	ErrInvalidCheckoutPaymentStatus      = DomainError{Code: CodeInvalidCheckoutPaymentStatus, Message: "checkout payment status is outside the provider vocabulary"}

	ErrEmptyIdempotencyKey         = DomainError{Code: CodeEmptyIdempotencyKey, Message: "idempotency key cannot be empty"}
	ErrInvalidIdempotencyKey       = DomainError{Code: CodeInvalidIdempotencyKey, Message: "idempotency key contains unsupported characters"}
	ErrIdempotencyKeyTooLong       = DomainError{Code: CodeIdempotencyKeyTooLong, Message: "idempotency key exceeds the maximum allowed length"}
	ErrInvalidCheckoutIntentStatus = DomainError{Code: CodeInvalidCheckoutIntentStatus, Message: "checkout intent status is outside the local vocabulary"}

	ErrEmptyWebhookEventID     = DomainError{Code: CodeEmptyWebhookEventID, Message: "webhook event identifier cannot be empty"}
	ErrInvalidWebhookEventID   = DomainError{Code: CodeInvalidWebhookEventID, Message: "webhook event identifier is malformed"}
	ErrEmptyWebhookEventType   = DomainError{Code: CodeEmptyWebhookEventType, Message: "webhook event type cannot be empty"}
	ErrInvalidWebhookEventType = DomainError{Code: CodeInvalidWebhookEventType, Message: "webhook event type is outside the provider vocabulary"}

	ErrInvalidStripeRefundID   = DomainError{Code: CodeInvalidStripeRefundID, Message: "Stripe refund identifier is malformed"}
	ErrInvalidStripeDisputeID  = DomainError{Code: CodeInvalidStripeDisputeID, Message: "Stripe dispute identifier is malformed"}
	ErrInvalidStripeChargeID   = DomainError{Code: CodeInvalidStripeChargeID, Message: "Stripe charge identifier is malformed"}
	ErrInvalidRefundSource     = DomainError{Code: CodeInvalidRefundSource, Message: "refund source is outside the supported vocabulary"}
	ErrInvalidRefundStatus     = DomainError{Code: CodeInvalidRefundStatus, Message: "refund status is outside the supported vocabulary"}
	ErrInvalidRefundAmount     = DomainError{Code: CodeInvalidRefundAmount, Message: "refunded amount must be a positive share of the charged price"}
	ErrInvalidRefundAssessment = DomainError{Code: CodeInvalidRefundAssess, Message: "refund assessment inputs are incoherent"}

	ErrInvalidReconciliationKind   = DomainError{Code: CodeInvalidReconciliationKind, Message: "reconciliation kind is outside the supported vocabulary"}
	ErrInvalidReconciliationWindow = DomainError{Code: CodeInvalidReconciliationWindow, Message: "reconciliation window must be a complete interval with the end after the start"}
	ErrInvalidSeason               = DomainError{Code: CodeInvalidSeason, Message: "purchase book needs a non-blank key without control characters"}
)
