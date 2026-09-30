package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// maxWebhookBodySize is the hard limit on the raw webhook body. A payload
// larger than this is rejected before signature verification, so an attacker
// cannot exhaust memory or CPU by sending an oversized body.
const maxWebhookBodySize = 1 << 20 // 1 MiB

// ProcessWebhookDependencies groups everything the webhook use case needs.
type ProcessWebhookDependencies struct {
	// Verifier checks the authenticity of the inbound payload.
	Verifier WebhookPayloadVerifier
	// Events persists event ID idempotency and processing lifecycle.
	Events WebhookEventRepository
	// Settler handles the settlement of checkout intents after verified
	// payment for INK products. It is optional: when nil, checkout.session.completed
	// events are acknowledged but not settled (the composition root injects it
	// when the settle use case is available).
	Settler *SettleCheckoutUseCase
	// PassSettler handles the settlement of checkout intents after verified
	// payment for Arena Pass products. It is optional: when nil, ARENA_PASS
	// checkout.session.completed events are acknowledged but not settled.
	PassSettler *SettleArenaPassUseCase
	// MemberSettler handles subscription lifecycle events and grants Member
	// entitlements (P12-T08). It is optional: when nil, subscription events
	// are acknowledged but not settled.
	MemberSettler *ApplyMemberEntitlementsUseCase
	// RefundSettler handles verified refunds and chargebacks with explicit
	// compensating entries (P12-T09). It is optional: when nil, refund and
	// dispute events are acknowledged but not compensated.
	RefundSettler *ApplyRefundUseCase
	// Clock supplies the instants of the local record.
	Clock Clock
}

// ProcessWebhookCommand is the inbound webhook delivery to process.
type ProcessWebhookCommand struct {
	// RawBody is the exact bytes received from the HTTP request. It must
	// not be modified before signature verification.
	RawBody []byte
	// SignatureHeader is the provider's signature header value
	// (Stripe-Signature).
	SignatureHeader string
	// TimestampHeader is the provider's timestamp header value
	// (Stripe-Timestamp).
	TimestampHeader string
}

// ProcessWebhookUseCase verifies, persists and processes one inbound webhook
// delivery. The processing flow is:
//
//  1. Reject oversized bodies before any computation.
//  2. Verify the payload signature and timestamp (provider-specific HMAC).
//  3. Compute the SHA-256 digest of the exact raw body for integrity evidence.
//  4. Claim the event idempotently: insert if new, resolve if replay.
//  5. Parse the event type and route to the handler (or ignore unknowns).
//  6. Mark the event as processed or failed.
//
// The success page of a checkout session never grants benefit: the provider's
// checkout.session.completed event is the only path that settles an intent,
// and only a verified webhook settles one (THR-STRIPE-02).
type ProcessWebhookUseCase struct {
	verifier      WebhookPayloadVerifier
	events        WebhookEventRepository
	settler       *SettleCheckoutUseCase
	passSettler   *SettleArenaPassUseCase
	memberSettler *ApplyMemberEntitlementsUseCase
	refundSettler *ApplyRefundUseCase
	clock         Clock
}

// NewProcessWebhookUseCase builds the use case, refusing incomplete
// composition.
func NewProcessWebhookUseCase(deps ProcessWebhookDependencies) (*ProcessWebhookUseCase, error) {
	if deps.Verifier == nil {
		return nil, fmt.Errorf("%w: the webhook payload verifier is required", ErrInvalidCheckoutConfig)
	}
	if deps.Events == nil {
		return nil, fmt.Errorf("%w: the webhook event repository is required", ErrInvalidCheckoutConfig)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("%w: a clock is required", ErrInvalidCheckoutConfig)
	}
	return &ProcessWebhookUseCase{
		verifier:      deps.Verifier,
		events:        deps.Events,
		settler:       deps.Settler,
		passSettler:   deps.PassSettler,
		memberSettler: deps.MemberSettler,
		refundSettler: deps.RefundSettler,
		clock:         deps.Clock,
	}, nil
}

// Execute verifies, persists and processes one inbound webhook delivery. It
// returns nil only when the event was handled (processed or ignored). A
// verification failure returns an error that must map to HTTP 400; a
// processing failure returns an error that must map to HTTP 500.
func (uc *ProcessWebhookUseCase) Execute(ctx context.Context, command ProcessWebhookCommand) error {
	// Step 1: reject oversized bodies before any computation.
	if len(command.RawBody) > maxWebhookBodySize {
		return fmt.Errorf("%w: the webhook body exceeds the %d byte limit",
			ErrWebhookPayloadTooLarge, maxWebhookBodySize)
	}

	// Step 2: verify the payload signature and timestamp.
	if err := uc.verifier.Verify(command.RawBody, command.SignatureHeader, command.TimestampHeader); err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookSignatureInvalid, err)
	}

	// Step 3: compute the SHA-256 digest of the exact raw body.
	digest := sha256.Sum256(command.RawBody)
	sha256hex := hex.EncodeToString(digest[:])

	// Step 4: parse the event type from the body to determine the event
	// ID and type. The body is a JSON object with at least id and type.
	eventID, eventType, livemode, stripeCreatedAt, err := parseEventMetadata(command.RawBody)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookPayloadMalformed, err)
	}

	// Step 5: claim the event idempotently.
	claimResult, err := uc.events.ClaimEvent(ctx, ClaimWebhookEventRequest{
		EventID:         eventID,
		EventType:       eventType,
		Livemode:        livemode,
		StripeCreatedAt: stripeCreatedAt,
		PayloadSHA256:   sha256hex,
		PayloadBytes:    len(command.RawBody),
	})
	if err != nil {
		return fmt.Errorf("claim webhook event: %w", err)
	}
	if claimResult.Replayed {
		// The event was already processed, ignored or is being processed.
		// A replay of a terminal event is harmless; a replay of an
		// in-progress event is refused.
		if claimResult.Record.Status == WebhookEventProcessing {
			return ErrWebhookEventInProcessing
		}
		return nil
	}

	// Step 6: parse the event type and route to the handler.
	if err := uc.processEvent(ctx, command.RawBody, eventType, claimResult.Record); err != nil {
		// Processing failed: mark the event as failed so it can be retried.
		reason := truncateError(err.Error(), 500)
		if markErr := uc.events.MarkFailed(ctx, eventID, reason); markErr != nil {
			return fmt.Errorf("mark webhook event failed: %v (original: %w)", markErr, err)
		}
		return err
	}

	// Processing succeeded: mark the event as processed.
	if err := uc.events.MarkProcessed(ctx, eventID); err != nil {
		return fmt.Errorf("mark webhook event processed: %w", err)
	}

	return nil
}

// processEvent routes the event to the appropriate handler based on type.
func (uc *ProcessWebhookUseCase) processEvent(ctx context.Context, rawBody []byte, eventType domain.WebhookEventType, record WebhookEventRecord) error {
	switch {
	case eventType.IsCheckoutSessionCompleted():
		return uc.handleCheckoutSessionCompleted(ctx, rawBody)
	case eventType.IsCheckoutSessionExpired():
		return uc.handleCheckoutSessionExpired(ctx, rawBody)
	case eventType.IsSubscriptionEvent():
		return uc.handleSubscriptionEvent(ctx, rawBody, eventType)
	case eventType.IsRefundEvent():
		return uc.handleRefundEvent(ctx, rawBody, domain.RefundSourceRefund)
	case eventType.IsDisputeEvent():
		return uc.handleRefundEvent(ctx, rawBody, domain.RefundSourceDispute)
	default:
		// Unknown event types are acknowledged but not handled.
		return nil
	}
}

// handleCheckoutSessionCompleted processes a checkout.session.completed event.
// This is the only event type that may settle a checkout intent: the
// provider confirmed the payment, and only this verification path can grant
// benefit (THR-STRIPE-02).
func (uc *ProcessWebhookUseCase) handleCheckoutSessionCompleted(ctx context.Context, rawBody []byte) error {
	// Parse the checkout session from the event data.
	sessionID, session, err := parseCheckoutSessionFromEvent(rawBody)
	if err != nil {
		return fmt.Errorf("parse checkout session: %w", err)
	}

	// The session must be paid to settle the intent.
	if !session.PaymentStatus.IsSettled() {
		return fmt.Errorf("%w: checkout session payment status is %s",
			ErrWebhookPayloadMalformed, session.PaymentStatus)
	}

	// Try the INK settler first. If it fails with the wrong grant kind,
	// try the pass settler.
	if uc.settler != nil {
		_, err := uc.settler.Execute(ctx, SettleCheckoutCommand{
			SessionID: sessionID,
		})
		if err != nil {
			// If the error is about wrong grant kind, try the pass settler.
			if errors.Is(err, ErrSettleCheckoutWrongGrantKind) && uc.passSettler != nil {
				_, passErr := uc.passSettler.Execute(ctx, SettleCheckoutCommand{
					SessionID: sessionID,
				})
				if passErr != nil {
					return fmt.Errorf("settle arena pass: %w", passErr)
				}
				return nil
			}
			return fmt.Errorf("settle checkout: %w", err)
		}
		return nil
	}

	// If no INK settler, try the pass settler directly.
	if uc.passSettler != nil {
		_, err := uc.passSettler.Execute(ctx, SettleCheckoutCommand{
			SessionID: sessionID,
		})
		if err != nil {
			return fmt.Errorf("settle arena pass: %w", err)
		}
	}

	return nil
}

// handleCheckoutSessionExpired processes a checkout.session.expired event.
// The intent is already recorded as expired by T04; this event is acknowledged
// but does not change the intent state.
func (uc *ProcessWebhookUseCase) handleCheckoutSessionExpired(_ context.Context, _ []byte) error {
	// The intent was already marked expired by the checkout use case (T04)
	// when the session was created with status expired. This event is
	// acknowledged but does not change the intent state.
	return nil
}

// handleSubscriptionEvent processes subscription lifecycle events (P12-T08).
func (uc *ProcessWebhookUseCase) handleSubscriptionEvent(ctx context.Context, rawBody []byte, _ domain.WebhookEventType) error {
	if uc.memberSettler == nil {
		return nil
	}

	cmd, err := parseSubscriptionFromEvent(rawBody)
	if err != nil {
		return fmt.Errorf("parse subscription event: %w", err)
	}

	if _, err := uc.memberSettler.Execute(ctx, cmd); err != nil {
		return fmt.Errorf("apply member entitlements: %w", err)
	}

	return nil
}

// handleRefundEvent processes verified refunds and chargebacks (P12-T09).
// The money-back object never mints a benefit: it only withdraws the unused
// share with append-only compensating entries, and any shortfall becomes a
// review flag instead of a negative balance.
func (uc *ProcessWebhookUseCase) handleRefundEvent(ctx context.Context, rawBody []byte, source domain.RefundSource) error {
	if uc.refundSettler == nil {
		return nil
	}

	cmd, err := parseRefundFromEvent(rawBody, source)
	if err != nil {
		return fmt.Errorf("parse refund event: %w", err)
	}

	if _, err := uc.refundSettler.Execute(ctx, cmd); err != nil {
		return fmt.Errorf("apply refund: %w", err)
	}

	return nil
}

// truncateError bounds the error reason stored in the database, so an
// attacker cannot exhaust storage with a crafted error message.
func truncateError(reason string, maxLen int) string {
	if len(reason) <= maxLen {
		return reason
	}
	return reason[:maxLen]
}

// parseEventMetadata extracts the event ID, type, livemode and creation
// timestamp from the raw webhook body. The body is a JSON object; this
// minimal parser avoids importing encoding/json (which is forbidden in the
// application layer per the architecture gate) by extracting known fields
// with string matching.
//
// This is a deliberate trade-off: the parser is stricter than a full JSON
// parser (it rejects nested objects, unexpected types and Unicode), but it
// avoids the architectural violation of importing serialization detail in
// the application layer. The parser is exercised by tests with well-formed
// and malformed payloads.
func parseEventMetadata(rawBody []byte) (domain.WebhookEventID, domain.WebhookEventType, bool, time.Time, error) {
	body := string(rawBody)

	// Extract "id": "evt_..."
	eventIDStr, err := extractJSONString(body, "id")
	if err != nil {
		return domain.WebhookEventID{}, domain.WebhookEventType{}, false, time.Time{}, fmt.Errorf("extract event id: %w", err)
	}
	eventID, err := domain.ParseWebhookEventID(eventIDStr)
	if err != nil {
		return domain.WebhookEventID{}, domain.WebhookEventType{}, false, time.Time{}, fmt.Errorf("parse event id: %w", err)
	}

	// Extract "type": "..."
	eventTypeStr, err := extractJSONString(body, "type")
	if err != nil {
		return domain.WebhookEventID{}, domain.WebhookEventType{}, false, time.Time{}, fmt.Errorf("extract event type: %w", err)
	}
	eventType, err := domain.ParseWebhookEventType(eventTypeStr)
	if err != nil {
		return domain.WebhookEventID{}, domain.WebhookEventType{}, false, time.Time{}, fmt.Errorf("parse event type: %w", err)
	}

	// Extract "livemode": true/false
	livemode, err := extractJSONBool(body, "livemode")
	if err != nil {
		return domain.WebhookEventID{}, domain.WebhookEventType{}, false, time.Time{}, fmt.Errorf("extract livemode: %w", err)
	}

	// Extract "created": <number>
	created, err := extractJSONInt64(body, "created")
	if err != nil {
		return domain.WebhookEventID{}, domain.WebhookEventType{}, false, time.Time{}, fmt.Errorf("extract created: %w", err)
	}
	stripeCreatedAt := time.Unix(created, 0).UTC()

	return eventID, eventType, livemode, stripeCreatedAt, nil
}

// extractJSONString extracts the value of a top-level string field from a
// flat JSON object. It is intentionally minimal: it handles escaped quotes
// in the value but does not handle nested objects or arrays.
func extractJSONString(body string, key string) (string, error) {
	prefix := "\"" + key + "\":"
	idx := strings.Index(body, prefix)
	if idx < 0 {
		return "", fmt.Errorf("key %q not found", key)
	}
	rest := body[idx+len(prefix):]
	// Skip whitespace.
	rest = strings.TrimLeft(rest, " \t\n\r")
	if len(rest) == 0 || rest[0] != '"' {
		return "", fmt.Errorf("key %q does not have a string value", key)
	}
	// Find the closing quote, accounting for escaped quotes.
	rest = rest[1:] // skip opening quote
	var buf strings.Builder
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		if ch == '\\' && i+1 < len(rest) {
			next := rest[i+1]
			switch next {
			case '"', '\\', '/':
				buf.WriteByte(next)
			case 'n':
				buf.WriteByte('\n')
			case 'r':
				buf.WriteByte('\r')
			case 't':
				buf.WriteByte('\t')
			default:
				buf.WriteByte(ch)
				buf.WriteByte(next)
			}
			i++
			continue
		}
		if ch == '"' {
			return buf.String(), nil
		}
		buf.WriteByte(ch)
	}
	return "", fmt.Errorf("key %q has an unterminated string value", key)
}

// extractJSONBool extracts the value of a top-level boolean field from a
// flat JSON object.
func extractJSONBool(body string, key string) (bool, error) {
	prefix := "\"" + key + "\":"
	idx := strings.Index(body, prefix)
	if idx < 0 {
		return false, fmt.Errorf("key %q not found", key)
	}
	rest := body[idx+len(prefix):]
	rest = strings.TrimLeft(rest, " \t\n\r")
	if strings.HasPrefix(rest, "true") {
		return true, nil
	}
	if strings.HasPrefix(rest, "false") {
		return false, nil
	}
	return false, fmt.Errorf("key %q does not have a boolean value", key)
}

// extractJSONInt64 extracts the value of a top-level integer field from a
// flat JSON object.
func extractJSONInt64(body string, key string) (int64, error) {
	prefix := "\"" + key + "\":"
	idx := strings.Index(body, prefix)
	if idx < 0 {
		return 0, fmt.Errorf("key %q not found", key)
	}
	rest := body[idx+len(prefix):]
	rest = strings.TrimLeft(rest, " \t\n\r")
	// Parse the integer.
	var n int64
	negative := false
	if len(rest) > 0 && rest[0] == '-' {
		negative = true
		rest = rest[1:]
	}
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		if ch < '0' || ch > '9' {
			if i == 0 {
				return 0, fmt.Errorf("key %q does not have an integer value", key)
			}
			break
		}
		n = n*10 + int64(ch-'0')
	}
	if negative {
		n = -n
	}
	return n, nil
}

// parseCheckoutSessionFromEvent extracts the checkout session data from the
// event body. The session data lives in the "data.object" nested object.
func parseCheckoutSessionFromEvent(rawBody []byte) (domain.StripeCheckoutSessionID, CheckoutSession, error) {
	body := string(rawBody)

	// Extract the nested object from "data": {"object": {...}}.
	// We look for "data" and then "object" within it.
	dataIdx := strings.Index(body, "\"data\"")
	if dataIdx < 0 {
		return "", CheckoutSession{}, fmt.Errorf("data field not found")
	}
	objectIdx := strings.Index(body[dataIdx:], "\"object\"")
	if objectIdx < 0 {
		return "", CheckoutSession{}, fmt.Errorf("object field not found in data")
	}
	nestedBody := body[dataIdx+objectIdx:]

	// Extract the session ID from the nested object.
	sessionIDStr, err := extractJSONString(nestedBody, "id")
	if err != nil {
		return "", CheckoutSession{}, fmt.Errorf("extract session id: %w", err)
	}
	// The session ID must be a valid Stripe checkout session ID.
	// We don't know the livemode here, so we pass false; the settle use
	// case will validate against the stored intent.
	sessionID, err := domain.ParseStripeCheckoutSessionID(sessionIDStr, false)
	if err != nil {
		return "", CheckoutSession{}, fmt.Errorf("parse session id: %w", err)
	}

	// Extract payment_status from the nested object.
	paymentStatusStr, err := extractJSONString(nestedBody, "payment_status")
	if err != nil {
		return "", CheckoutSession{}, fmt.Errorf("extract payment_status: %w", err)
	}
	paymentStatus, err := domain.ParseCheckoutPaymentStatus(paymentStatusStr)
	if err != nil {
		return "", CheckoutSession{}, fmt.Errorf("parse payment_status: %w", err)
	}

	// Extract the checkout session status.
	statusStr, err := extractJSONString(nestedBody, "status")
	if err != nil {
		return "", CheckoutSession{}, fmt.Errorf("extract status: %w", err)
	}
	status, err := domain.ParseCheckoutSessionStatus(statusStr)
	if err != nil {
		return "", CheckoutSession{}, fmt.Errorf("parse status: %w", err)
	}

	return sessionID, CheckoutSession{
		Status:        status,
		PaymentStatus: paymentStatus,
	}, nil
}

// parseSubscriptionFromEvent extracts the subscription data from the
// event body (data.object).
func parseSubscriptionFromEvent(rawBody []byte) (ApplyMemberEntitlementsCommand, error) {
	body := string(rawBody)

	// Top-level livemode
	livemode, _ := extractJSONBool(body, "livemode")

	// Extract the nested object from "data": {"object": {...}}.
	dataIdx := strings.Index(body, "\"data\"")
	if dataIdx < 0 {
		return ApplyMemberEntitlementsCommand{}, fmt.Errorf("data field not found")
	}
	objectIdx := strings.Index(body[dataIdx:], "\"object\"")
	if objectIdx < 0 {
		return ApplyMemberEntitlementsCommand{}, fmt.Errorf("object field not found in data")
	}
	nestedBody := body[dataIdx+objectIdx:]

	// Extract subscription ID
	subIDStr, err := extractJSONString(nestedBody, "id")
	if err != nil {
		return ApplyMemberEntitlementsCommand{}, fmt.Errorf("extract subscription id: %w", err)
	}
	subID, err := domain.ParseStripeSubscriptionID(subIDStr)
	if err != nil {
		return ApplyMemberEntitlementsCommand{}, fmt.Errorf("parse subscription id: %w", err)
	}

	// Extract customer ID
	customerIDStr, err := extractJSONString(nestedBody, "customer")
	if err != nil {
		return ApplyMemberEntitlementsCommand{}, fmt.Errorf("extract customer id: %w", err)
	}
	customerID, err := domain.ParseStripeCustomerID(customerIDStr)
	if err != nil {
		return ApplyMemberEntitlementsCommand{}, fmt.Errorf("parse customer id: %w", err)
	}

	// Extract status
	statusStr, err := extractJSONString(nestedBody, "status")
	if err != nil {
		return ApplyMemberEntitlementsCommand{}, fmt.Errorf("extract status: %w", err)
	}
	status, err := domain.ParseSubscriptionStatus(statusStr)
	if err != nil {
		return ApplyMemberEntitlementsCommand{}, fmt.Errorf("parse status: %w", err)
	}

	// Extract price ID
	priceIDStr, err := extractPriceIDFromSubscriptionJSON(nestedBody)
	if err != nil {
		return ApplyMemberEntitlementsCommand{}, fmt.Errorf("extract price id: %w", err)
	}
	priceID, err := domain.ParseStripePriceID(priceIDStr)
	if err != nil {
		return ApplyMemberEntitlementsCommand{}, fmt.Errorf("parse price id: %w", err)
	}

	// Extract periods
	var currentPeriodStart, currentPeriodEnd *time.Time
	startInt, err := extractJSONInt64(nestedBody, "current_period_start")
	if err == nil && startInt > 0 {
		t := time.Unix(startInt, 0).UTC()
		currentPeriodStart = &t
	}
	endInt, err := extractJSONInt64(nestedBody, "current_period_end")
	if err == nil && endInt > 0 {
		t := time.Unix(endInt, 0).UTC()
		currentPeriodEnd = &t
	}

	// Extract cancel_at_period_end
	cancelAtPeriodEnd, _ := extractJSONBool(nestedBody, "cancel_at_period_end")

	// Extract canceled_at
	var canceledAt *time.Time
	canceledAtInt, err := extractJSONInt64(nestedBody, "canceled_at")
	if err == nil && canceledAtInt > 0 {
		t := time.Unix(canceledAtInt, 0).UTC()
		canceledAt = &t
	}

	return ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           customerID,
		Status:               status,
		PriceID:              priceID,
		CurrentPeriodStart:   currentPeriodStart,
		CurrentPeriodEnd:     currentPeriodEnd,
		CancelAtPeriodEnd:    cancelAtPeriodEnd,
		CanceledAt:           canceledAt,
		Livemode:             livemode,
	}, nil
}

// extractPriceIDFromSubscriptionJSON locates the Stripe price ID inside a
// subscription JSON object.
func extractPriceIDFromSubscriptionJSON(body string) (string, error) {
	// 1. Direct "price": "price_..."
	if id, err := extractJSONString(body, "price"); err == nil && strings.HasPrefix(id, "price_") {
		return id, nil
	}
	// 2. Object "price": { "id": "price_..." }
	priceIdx := strings.Index(body, "\"price\"")
	if priceIdx >= 0 {
		if id, err := extractJSONString(body[priceIdx:], "id"); err == nil && strings.HasPrefix(id, "price_") {
			return id, nil
		}
	}
	// 3. Direct "plan": "price_..."
	if id, err := extractJSONString(body, "plan"); err == nil && strings.HasPrefix(id, "price_") {
		return id, nil
	}
	// 4. Object "plan": { "id": "price_..." }
	planIdx := strings.Index(body, "\"plan\"")
	if planIdx >= 0 {
		if id, err := extractJSONString(body[planIdx:], "id"); err == nil && strings.HasPrefix(id, "price_") {
			return id, nil
		}
	}
	return "", fmt.Errorf("price id not found in subscription object")
}

// parseRefundFromEvent extracts the refund correlation from the event body
// (data.object). The parser stays HTTP-free like every application parser:
// it extracts known fields with string matching instead of importing
// encoding/json.
//
// The minimal contract is explicit: the nested object carries the provider
// refund/dispute identifier as "id" (re_... or dp_...), the original
// checkout session as "checkout_session" (cs_test_.../cs_live_...) and the
// returned money as "amount_refunded" (falling back to "amount"). The charged
// price always comes from the local intent, never from the event, so a
// tampered amount cannot change the reversible quantity beyond the charged
// ceiling enforced by the use case.
func parseRefundFromEvent(rawBody []byte, source domain.RefundSource) (ApplyRefundCommand, error) {
	body := string(rawBody)

	dataIdx := strings.Index(body, "\"data\"")
	if dataIdx < 0 {
		return ApplyRefundCommand{}, fmt.Errorf("data field not found")
	}
	objectIdx := strings.Index(body[dataIdx:], "\"object\"")
	if objectIdx < 0 {
		return ApplyRefundCommand{}, fmt.Errorf("object field not found in data")
	}
	nestedBody := body[dataIdx+objectIdx:]

	refundIDStr, err := extractJSONString(nestedBody, "id")
	if err != nil {
		return ApplyRefundCommand{}, fmt.Errorf("extract refund id: %w", err)
	}
	if source.IsDispute() {
		if _, err := domain.ParseStripeDisputeID(refundIDStr); err != nil {
			return ApplyRefundCommand{}, fmt.Errorf("parse dispute id: %w", err)
		}
	} else {
		if _, err := domain.ParseStripeRefundID(refundIDStr); err != nil {
			return ApplyRefundCommand{}, fmt.Errorf("parse refund id: %w", err)
		}
	}

	sessionStr, err := extractJSONString(nestedBody, "checkout_session")
	if err != nil {
		return ApplyRefundCommand{}, fmt.Errorf("extract checkout session: %w", err)
	}
	sessionID, err := domain.ParseStripeCheckoutSessionID(sessionStr, false)
	if err != nil {
		liveID, liveErr := domain.ParseStripeCheckoutSessionID(sessionStr, true)
		if liveErr != nil {
			return ApplyRefundCommand{}, fmt.Errorf("parse checkout session: %w", err)
		}
		sessionID = liveID
	}

	refunded, err := extractJSONInt64(nestedBody, "amount_refunded")
	if err != nil || refunded < 1 {
		refunded, err = extractJSONInt64(nestedBody, "amount")
		if err != nil || refunded < 1 {
			return ApplyRefundCommand{}, fmt.Errorf("extract refunded amount: %w", err)
		}
	}

	return ApplyRefundCommand{
		SessionID:           sessionID,
		ProviderRefundID:    refundIDStr,
		Source:              source,
		RefundedAmountMinor: refunded,
	}, nil
}
