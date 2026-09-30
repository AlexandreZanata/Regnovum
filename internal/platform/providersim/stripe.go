package providersim

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// The Stripe surface: the five addresses the product's payment adapter reaches,
// as the official SDK renders them, and the webhook direction, where the
// provider is the one that speaks and the product is the one that listens.
//
// The documents are the ones the adapter's parser reads, field by field: a
// session is answered with the status, the payment status, the mode, the amount
// and the currency it translates into billing vocabulary, so a simulator that
// answered a shorter document would not exercise the translation at all.
const (
	StripeCustomers       = "POST /v1/customers"
	StripeCheckoutCreate  = "POST /v1/checkout/sessions"
	StripeCheckoutGet     = "GET /v1/checkout/sessions/*"
	StripeSubscriptionGet = "GET /v1/subscriptions/*"
	StripePortalCreate    = "POST /v1/billing_portal/sessions"
)

// The synthetic values of the fake provider. They are literals of the fixture,
// composed so that a secret scanner reads no complete credential in this file,
// and no fixture reads a real one from anywhere: pointing these adapters at the
// simulator is what makes an external key unnecessary.
const (
	StripeSecretKey      = "sk" + "_test_sim_synthetic_secret_key"
	StripeWebhookSecret  = "whsec" + "_sim_synthetic_webhook_secret"
	StripeCustomerID     = "cus_sim"
	StripeCheckoutID     = "cs_test_sim"
	StripeSubscriptionID = "sub_sim"
	// StripePriceID is the price the fake provider sells through. It is shaped
	// the way the product's own parser requires — the prefix and then only
	// letters and digits — because the read of a subscription passes through
	// that parser: a fixture identifier with an underscore in it answered a
	// document the domain refuses, and the success path of the adapter could not
	// be exercised at all.
	StripePriceID         = "price_1SimMember00000000000000"
	StripePaymentIntentID = "pi_sim"
	StripePortalURL       = "https://billing.stripe.invalid/session"
	StripeCheckoutURL     = "https://checkout.stripe.invalid/session"
	stripeCreatedInstant  = int64(1789000000)
	stripeExpiresInstant  = int64(1790000000)
)

// NewStripe builds the fake payment provider with every address the adapter
// reaches answered successfully: one customer, one checkout session, the read
// of that session, the read of a subscription and one billing portal session.
// A fixture scripts a state by re-declaring the address it cares about, which
// replaces the success answer instead of adding to it.
func NewStripe(t testing.TB, options ...Option) *Simulator {
	t.Helper()
	simulator := New(t, "stripe", options...)
	simulator.handler = stripeRefusal
	simulator.Route(StripeCustomers, Reply(http.StatusOK, StripeCustomerBody(StripeCustomerID)))
	simulator.Route(StripeCheckoutCreate, Reply(http.StatusOK, StripeCheckoutSessionBody(StripeCheckoutID, "intent-sim")))
	simulator.Route(StripeCheckoutGet, Reply(http.StatusOK, StripeCheckoutSessionBody(StripeCheckoutID, "intent-sim")))
	simulator.Route(StripeSubscriptionGet, Reply(http.StatusOK, StripeSubscriptionBody(StripeSubscriptionID, StripeCustomerID, StripePriceID)))
	simulator.Route(StripePortalCreate, Reply(http.StatusOK, StripePortalSessionBody(StripePortalURL)))
	return simulator
}

// stripeRefusal is what the fake provider answers a call nobody scripted: the
// provider's own error document, so the adapter classifies it instead of
// crashing on a document it did not expect.
func stripeRefusal(method, path string) (int, string) {
	return UnexpectedStatus, StripeErrorBody(UnexpectedStatus, method+" "+path+" is not scripted")
}

// StripeCustomerBody renders the customer the adapter reads an identifier from.
func StripeCustomerBody(id string) string {
	return encodedDocument(map[string]any{
		"id":       id,
		"object":   "customer",
		"livemode": false,
		"created":  stripeCreatedInstant,
	})
}

// StripeCheckoutSessionBody renders a completed, paid checkout session. The
// adapter translates every field it reads, so the document carries all of them.
func StripeCheckoutSessionBody(id, clientReference string) string {
	return encodedDocument(map[string]any{
		"id":                  id,
		"object":              "checkout.session",
		"status":              "complete",
		"payment_status":      "paid",
		"mode":                "subscription",
		"amount_total":        2490,
		"currency":            "brl",
		"client_reference_id": clientReference,
		"livemode":            false,
		"url":                 StripeCheckoutURL,
		"expires_at":          stripeExpiresInstant,
		"created":             stripeCreatedInstant,
		"payment_intent":      map[string]any{"id": StripePaymentIntentID},
	})
}

// StripeSubscriptionBody renders one active subscription with its single item.
func StripeSubscriptionBody(id, customerID, priceID string) string {
	return encodedDocument(map[string]any{
		"id":                   id,
		"object":               "subscription",
		"status":               "active",
		"livemode":             false,
		"cancel_at_period_end": false,
		"current_period_start": stripeCreatedInstant,
		"current_period_end":   stripeExpiresInstant,
		"created":              stripeCreatedInstant,
		"customer":             map[string]any{"id": customerID},
		"items": map[string]any{
			"object": "list",
			"data": []any{
				map[string]any{
					"id":     "si_sim",
					"object": "subscription_item",
					"price":  map[string]any{"id": priceID},
				},
			},
		},
	})
}

// StripePortalSessionBody renders the billing portal session the adapter reads
// a URL from.
func StripePortalSessionBody(url string) string {
	return encodedDocument(map[string]any{
		"id":       "bps_sim",
		"object":   "billing_portal.session",
		"url":      url,
		"livemode": false,
		"created":  stripeCreatedInstant,
	})
}

// StripeErrorBody renders the provider's error document for a status. The class
// decides the type the adapter and the SDK read, so a 4xx is an invalid request
// and a 5xx is a provider failure — the two outcomes a caller must treat
// differently.
func StripeErrorBody(status int, message string) string {
	kind := "api_error"
	if status >= 400 && status < 500 {
		kind = "invalid_request_error"
	}
	return encodedDocument(map[string]any{
		"error": map[string]any{
			"type":    kind,
			"code":    "sim_" + strconv.Itoa(status),
			"message": message,
		},
	})
}

// StripeError is the answer of a provider that refuses one call.
func StripeError(status int, message string) Answer {
	return Reply(status, StripeErrorBody(status, message))
}

// StripeEventBody renders one webhook event document of the provider's shape.
func StripeEventBody(id, eventType string, at time.Time, object any) string {
	return encodedDocument(map[string]any{
		"id":       id,
		"object":   "event",
		"type":     eventType,
		"created":  at.UTC().Unix(),
		"livemode": false,
		"data": map[string]any{
			"object": object,
		},
	})
}

// StripeDelivery is one event as it is delivered: the document and the instant
// the provider signed it with. The instant is separate from the document
// because that is what the signature covers and what the verifier's tolerance
// window judges.
type StripeDelivery struct {
	Payload string
	At      time.Time
}

// SignStripeWebhook renders the two headers of one delivery, in the documented
// scheme: the header carries the timestamp and the HMAC-SHA256 of
// "<timestamp>.<payload>" keyed by the signing secret.
func SignStripeWebhook(secret string, at time.Time, payload string) (signature string, timestamp string) {
	timestamp = strconv.FormatInt(at.UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + payload))
	return "t=" + timestamp + ",v1=" + hex.EncodeToString(mac.Sum(nil)), timestamp
}

// DeliverStripeWebhooks posts the deliveries to a target, in the order given,
// signing each one with its own instant, and answers the status of every
// delivery. It is how a fixture produces the states the product cannot ask for:
// the same event twice (duplication) and events in the reverse of the order
// they were created (reordering).
func DeliverStripeWebhooks(client *http.Client, target, secret string, deliveries []StripeDelivery) ([]int, error) {
	statuses := make([]int, 0, len(deliveries))
	for _, delivery := range deliveries {
		signature, timestamp := SignStripeWebhook(secret, delivery.At, delivery.Payload)
		request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader([]byte(delivery.Payload)))
		if err != nil {
			return statuses, fmt.Errorf("providersim: stripe delivery: %w", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Stripe-Signature", signature)
		request.Header.Set("Stripe-Timestamp", timestamp)
		response, err := client.Do(request)
		if err != nil {
			return statuses, fmt.Errorf("providersim: stripe delivery: %w", err)
		}
		statuses = append(statuses, response.StatusCode)
		if err := response.Body.Close(); err != nil {
			return statuses, fmt.Errorf("providersim: stripe delivery: %w", err)
		}
	}
	return statuses, nil
}

// Duplicate answers the deliveries of the same event twice, which is what the
// provider does when it does not receive the acknowledgement of the first
// attempt: the product sees the event again and must resolve it instead of
// applying it twice.
func Duplicate(delivery StripeDelivery) []StripeDelivery {
	return []StripeDelivery{delivery, delivery}
}

// Reversed answers the deliveries in the reverse of the order they are given,
// signed with their own instants: the product receives the later event first,
// which is the reordering a queue with retries produces.
func Reversed(deliveries []StripeDelivery) []StripeDelivery {
	reversed := make([]StripeDelivery, 0, len(deliveries))
	for index := len(deliveries) - 1; index >= 0; index-- {
		reversed = append(reversed, deliveries[index])
	}
	return reversed
}

// encodedDocument renders a document, and it is the one place a marshal failure
// is not a scenario: every document here is built from literals of this
// package, so a failure is a defect and a panic is the honest answer.
func encodedDocument(document any) string {
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(fmt.Sprintf("providersim: the simulator documents do not encode: %v", err))
	}
	return string(encoded)
}
