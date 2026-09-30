// Package application defines the use cases, orchestrations and
// consumer-oriented ports of the billing module.
package application

import "errors"

var (
	// ErrArenaAlreadyConsumed indicates the arena already consumed a pass that
	// belongs to another account: a pass can never be transferred between
	// accounts.
	ErrArenaAlreadyConsumed = errors.New("application: arena pass was already consumed by another account")

	// ErrInvalidCursor indicates a malformed, forged or version-mismatched
	// history cursor. Unknown values are never reflected back to callers.
	ErrInvalidCursor = errors.New("application: pass history cursor is invalid")

	// ErrWeakHistoryCursorSecret indicates the configured cursor signing
	// secret is shorter than the 256-bit minimum.
	ErrWeakHistoryCursorSecret = errors.New("application: pass history cursor secret must be at least 32 bytes")
)

// Payment gateway error vocabulary (P12-T03).
//
// The port classifies every provider failure into one of these sentinels, so
// use cases decide on meaning (retry, reconcile, alert an operator) instead of
// inspecting provider errors. Two rules hold for all of them, enforced by the
// adapter and covered by tests:
//
//   - the provider's free-form message never travels: it can echo request
//     values, and the payload of a diagnostic belongs to the request log the
//     provider keeps, reachable through the request id that IS attached;
//   - a failure never carries the credentials, the customer, the email or any
//     other personal data.
var (
	// ErrPaymentGatewayUnavailable indicates the provider could not be
	// reached or answered a server-side failure or a rate limit. The outcome
	// of the call is unknown, so a retry must reuse the same idempotency key.
	ErrPaymentGatewayUnavailable = errors.New("application: payment gateway is unavailable")

	// ErrPaymentGatewayTimeout indicates the call exceeded the configured
	// deadline. Nothing about the outcome is known: the operation may or may
	// not have completed at the provider, so the only safe continuations are
	// retrying with the same idempotency key or reconciling the object.
	ErrPaymentGatewayTimeout = errors.New("application: payment gateway did not answer within the configured timeout")

	// ErrPaymentGatewayRejected indicates the provider refused the request
	// itself (invalid parameters, a card the issuer declined). Retrying the
	// same request is pointless.
	ErrPaymentGatewayRejected = errors.New("application: payment gateway refused the request")

	// ErrPaymentGatewayMisconfigured indicates the integration with the
	// provider is broken: the credentials were rejected or the provider
	// answered something outside the contract this adapter accepts. Retrying
	// does not help; an operator must act.
	ErrPaymentGatewayMisconfigured = errors.New("application: payment gateway credentials or contract are misconfigured")

	// ErrPaymentGatewayRequestInvalid indicates the port was called with a
	// request the gateway cannot serve (a missing idempotency key, an
	// identifier from the wrong provider mode). It is a programming error at
	// the call site and is detected before any request reaches the provider.
	ErrPaymentGatewayRequestInvalid = errors.New("application: payment gateway request is invalid")
)

// IsRetryablePaymentGatewayError reports whether a failed gateway call may be
// retried with the same idempotency key. Only failures whose outcome is
// unknown qualify; a refusal or a misconfiguration is permanent.
func IsRetryablePaymentGatewayError(err error) bool {
	return errors.Is(err, ErrPaymentGatewayUnavailable) || errors.Is(err, ErrPaymentGatewayTimeout)
}

// Webhook error vocabulary (P12-T05).
//
// The messages never carry the payload, the signature or any personal data:
// a refusal says what rule was broken, never the secret or the body.
var (
	// ErrWebhookPayloadTooLarge indicates the raw body exceeds the hard
	// limit. It is rejected before signature verification, so an attacker
	// cannot exhaust memory or CPU with an oversized payload.
	ErrWebhookPayloadTooLarge = errors.New("application: webhook payload exceeds the size limit")

	// ErrWebhookSignatureInvalid indicates the payload signature or
	// timestamp verification failed. The payload must be rejected with
	// HTTP 400 and must never be persisted.
	ErrWebhookSignatureInvalid = errors.New("application: webhook signature is invalid")

	// ErrWebhookPayloadMalformed indicates the event body could not be
	// parsed into the expected provider structure. It may be corrupted or
	// from a different version of the provider contract.
	ErrWebhookPayloadMalformed = errors.New("application: webhook payload is malformed")
)

// Checkout error vocabulary (P12-T04).
//
// The messages never carry the email, the account identifier, the provider
// customer or any amount that a caller supplied: a refusal says what rule was
// broken, never who broke it.
var (
	// ErrPurchaserNotFound indicates no account carries the acting
	// identifier. It is distinct from an ineligible account so a forged
	// identifier is never treated as a legitimate buyer.
	ErrPurchaserNotFound = errors.New("application: account was not found")

	// ErrPurchaserNotEligible indicates the account exists but may not
	// purchase: it is not active or its email is not verified
	// (docs/BUSINESS_RULES.md §7, REQ-AUTH-02).
	ErrPurchaserNotEligible = errors.New("application: account is not eligible to purchase")

	// ErrProviderModeChanged indicates the account's stored provider customer
	// belongs to the other provider mode (test vs live). Test and live
	// objects are never mixed, so the flow stops instead of charging a live
	// session against a test mapping.
	ErrProviderModeChanged = errors.New("application: account provider customer belongs to the other mode")

	// ErrProviderAmountMismatch indicates the provider would charge something
	// other than the price the versioned catalog resolved. The buyer is never
	// shown one price and charged another.
	ErrProviderAmountMismatch = errors.New("application: provider price does not match the resolved catalog price")

	// ErrInvalidCheckoutConfig indicates the checkout use case could not be
	// built from the given configuration (missing or incoherent return URLs).
	// It is a composition error, raised before the process serves anything.
	ErrInvalidCheckoutConfig = errors.New("application: checkout configuration is invalid")

	// Settle checkout error vocabulary (P12-T06).

	// ErrCheckoutIntentNotFound indicates no intent carries the provider
	// session identifier. The webhook event may reference a session that
	// was never recorded locally.
	ErrCheckoutIntentNotFound = errors.New("application: checkout intent was not found")

	// ErrSettleCheckoutWrongGrantKind indicates the product grants
	// something other than INK. This settle use case handles only INK
	// purchases; Arena Pass and Member grants are handled by T07 and T08.
	ErrSettleCheckoutWrongGrantKind = errors.New("application: product does not grant INK")

	// Member entitlement error vocabulary (P12-T08).

	// ErrMemberWrongGrantKind indicates the product resolved by the price ID
	// is not a Member subscription product.
	ErrMemberWrongGrantKind = errors.New("application: product does not grant Member entitlement")

	// ErrInvalidMemberConfig indicates the member entitlements use case could
	// not be built from the given configuration.
	ErrInvalidMemberConfig = errors.New("application: member entitlements configuration is invalid")

	// Refund error vocabulary (P12-T09).

	// ErrInvalidRefundConfig indicates the refund use case could not be built
	// from the given configuration.
	ErrInvalidRefundConfig = errors.New("application: refund configuration is invalid")

	// ErrRefundIntentNotSettled indicates the intent never settled: only a
	// paid intent granted a benefit, so only it can be compensated.
	ErrRefundIntentNotSettled = errors.New("application: checkout intent never settled")

	// ErrRefundUnsupportedGrant indicates the product grant has no refund
	// policy. INK packs, passes and Member are covered; anything else is a
	// catalog error.
	ErrRefundUnsupportedGrant = errors.New("application: product grant has no refund policy")

	// Reconciliation error vocabulary (P12-T10).

	// ErrInvalidReconciliationConfig indicates the reconciliation job could
	// not be built from the given configuration.
	ErrInvalidReconciliationConfig = errors.New("application: reconciliation configuration is invalid")

	// ErrPortalCustomerNotFound indicates the account has no stored provider
	// customer, so there is no portal to open.
	ErrPortalCustomerNotFound = errors.New("application: billing customer was not found")

	// INK purchase intent error vocabulary (P35-T05).

	// ErrInvalidPurchaseIntentConfig indicates the purchase intent use case
	// could not be built from the given configuration.
	ErrInvalidPurchaseIntentConfig = errors.New("application: purchase intent configuration is invalid")

	// ErrPurchaseQuoteNotFound indicates no quotation carries the
	// identifier: an unknown quote never opens an intent.
	ErrPurchaseQuoteNotFound = errors.New("application: purchase quotation was not found")

	// ErrPurchaseQuoteExpired indicates the quotation lapsed before
	// acceptance: the lifetime counts from acceptance terms sealed
	// earlier, never from a later arrival.
	ErrPurchaseQuoteExpired = errors.New("application: purchase quotation expired before acceptance")

	// ErrPurchaseIntentConflict indicates the intent key already settled
	// different terms for the account: a retry must repeat the purchase,
	// never rewrite it.
	ErrPurchaseIntentConflict = errors.New("application: purchase intent key already settled different terms")

	// ErrInsufficientCommercialStock indicates the commercial vault cannot
	// cover the derived INK: the intent, the hold and every leg refuse
	// together, so no charge exists without stock.
	ErrInsufficientCommercialStock = errors.New("application: commercial stock cannot cover the purchase")

	// INK purchase settlement error vocabulary (P35-T06).

	// ErrPurchaseIntentNotFound indicates no acceptance carries the key
	// the event names: an event without an intent settles nothing.
	ErrPurchaseIntentNotFound = errors.New("application: purchase intent was not found")

	// ErrPurchaseEventNotSettling indicates the event does not order a
	// payment: success pages, failures and unknown statuses never move
	// committed INK, only a paid event settles.
	ErrPurchaseEventNotSettling = errors.New("application: purchase event does not settle")

	// ErrPurchaseSettlementMismatch indicates the event disagrees with
	// the sealed intent on amount or currency: the buyer is never shown
	// one ticket and settled on another.
	ErrPurchaseSettlementMismatch = errors.New("application: purchase event does not match the sealed intent")

	// INK purchase chargeback error vocabulary (P35-T08).

	// ErrSettlementNotFound indicates no liquidation carries the intent:
	// without a settlement there is nothing to reverse.
	ErrSettlementNotFound = errors.New("application: purchase settlement was not found")

	// ErrChargebackConflict indicates the liquidation already carries a
	// different dispute: a second dispute replays instead of revoking
	// twice.
	ErrChargebackConflict = errors.New("application: purchase liquidation already carries a dispute")

	// ErrInsufficientTreasuryFunds indicates the operator cannot cover
	// the difference without minting: the dispute refuses with
	// everything untouched instead of inventing INK or implying a
	// negative balance.
	ErrInsufficientTreasuryFunds = errors.New("application: treasury cannot cover the dispute without minting")
)
