package stripe_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	stripeadapter "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// stubSecret is the credential the stub provider expects. It is deliberately
// recognizable so tests can prove it never leaks into an error or a rendering.
const stubSecret = "sk_test_stub_secret_value"

// stubProvider is a local provider endpoint: the adapter is pointed at it
// through Config.BaseURL, so every test exercises the real SDK code path —
// request building, header handling, response parsing and error mapping —
// without ever reaching the network.
type stubProvider struct {
	t         *testing.T
	server    *httptest.Server
	responses map[string]stubResponse
	delay     time.Duration

	mu   sync.Mutex
	seen []recordedRequest
}

// stubResponse is the canned answer of one route.
type stubResponse struct {
	status int
	body   string
}

// recordedRequest is one request the adapter actually sent.
type recordedRequest struct {
	method  string
	path    string
	headers http.Header
	form    url.Values
}

// newStubProvider starts the stub endpoint and registers its cleanup.
func newStubProvider(t *testing.T, responses map[string]stubResponse) *stubProvider {
	t.Helper()

	provider := &stubProvider{t: t, responses: responses}
	provider.server = httptest.NewServer(provider)
	t.Cleanup(provider.server.Close)
	return provider
}

// ServeHTTP records the request and answers the canned route.
func (provider *stubProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if provider.delay > 0 {
		time.Sleep(provider.delay)
	}

	body, err := io.ReadAll(request.Body)
	if err != nil {
		provider.t.Errorf("read request body: %v", err)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		provider.t.Errorf("parse request form: %v", err)
	}

	recorded := recordedRequest{
		method:  request.Method,
		path:    request.URL.Path,
		headers: request.Header.Clone(),
		form:    form,
	}
	provider.mu.Lock()
	provider.seen = append(provider.seen, recorded)
	provider.mu.Unlock()

	answer, found := provider.responses[request.Method+" "+request.URL.Path]
	if !found {
		provider.t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		answer = stubResponse{status: http.StatusNotFound, body: `{"error":{"type":"invalid_request_error","message":"no stub route"}}`}
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(answer.status)
	if _, err := writer.Write([]byte(answer.body)); err != nil {
		provider.t.Errorf("write response: %v", err)
	}
}

// requests returns a copy of everything the adapter sent.
func (provider *stubProvider) requests() []recordedRequest {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	recorded := make([]recordedRequest, len(provider.seen))
	copy(recorded, provider.seen)
	return recorded
}

// gateway builds an adapter pointed at the stub.
func (provider *stubProvider) gateway(t *testing.T, timeout time.Duration) *stripeadapter.Gateway {
	t.Helper()

	gateway, err := stripeadapter.NewGateway(stripeadapter.Config{
		SecretKey: stubSecret,
		Timeout:   timeout,
		BaseURL:   provider.server.URL,
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return gateway
}

const stubSessionJSON = `{
  "id": "cs_test_stub",
  "object": "checkout.session",
  "status": "complete",
  "payment_status": "paid",
  "mode": "subscription",
  "amount_total": 2490,
  "currency": "brl",
  "client_reference_id": "intent-stub",
  "livemode": false,
  "url": "https://checkout.stripe.invalid/session",
  "expires_at": 1790000000,
  "payment_intent": {"id": "pi_stub"}
}`

const stubSubscriptionJSON = `{
  "id": "sub_stub",
  "object": "subscription",
  "status": "active",
  "livemode": false,
  "cancel_at_period_end": true,
  "current_period_start": 1789000000,
  "current_period_end": 1791592000,
  "customer": {"id": "cus_stub"},
  "items": {"object": "list", "data": [{"id": "si_stub", "object": "subscription_item", "price": {"id": "price_1QintlMember"}}]}
}`

// TestCreateCustomerProvisionsOnceWithAnIdempotencyKey proves the mutating call
// reaches the provider authenticated, carrying the caller's key, and that the
// object comes back as billing vocabulary.
func TestCreateCustomerProvisionsOnceWithAnIdempotencyKey(t *testing.T) {
	t.Parallel()

	provider := newStubProvider(t, map[string]stubResponse{
		"POST /v1/customers": {status: http.StatusOK, body: `{"id":"cus_stub","object":"customer","livemode":false}`},
	})
	gateway := provider.gateway(t, 5*time.Second)

	customer, err := gateway.CreateCustomer(context.Background(), application.CreateCustomerRequest{
		IdempotencyKey: "intent-42",
	})
	if err != nil {
		t.Fatalf("CreateCustomer error = %v", err)
	}
	if customer.ID.String() != "cus_stub" || customer.Livemode {
		t.Fatalf("customer = %+v", customer)
	}

	requests := provider.requests()
	if len(requests) != 1 {
		t.Fatalf("provider received %d requests, want exactly 1 (no implicit retry)", len(requests))
	}
	request := requests[0]
	if request.method != http.MethodPost || request.path != "/v1/customers" {
		t.Fatalf("request = %s %s", request.method, request.path)
	}
	if got := request.headers.Get("Authorization"); got != "Bearer "+stubSecret {
		t.Errorf("Authorization = %q", got)
	}
	if got := request.headers.Get("Idempotency-Key"); got != "intent-42" {
		t.Errorf("Idempotency-Key = %q", got)
	}
	if request.headers.Get("Stripe-Version") == "" {
		t.Error("the provider version header must be pinned by the SDK")
	}
}

// TestCreateCheckoutSessionSendsTheServerDecidedPrice proves the browser has
// nothing to influence: the adapter sends exactly the price the catalog
// resolved, one unit of it, the customer, the return URLs and the client
// reference, all under an idempotency key.
func TestCreateCheckoutSessionSendsTheServerDecidedPrice(t *testing.T) {
	t.Parallel()

	provider := newStubProvider(t, map[string]stubResponse{
		"POST /v1/checkout/sessions": {status: http.StatusOK, body: stubSessionJSON},
	})
	gateway := provider.gateway(t, 5*time.Second)

	session, err := gateway.CreateCheckoutSession(context.Background(), application.CreateCheckoutSessionRequest{
		CustomerID:      domain.StripeCustomerID("cus_stub"),
		PriceID:         domain.StripePriceID("price_1QbrMember"),
		Mode:            domain.CheckoutModeSubscription,
		SuccessURL:      "https://arena.invalid/checkout/success",
		CancelURL:       "https://arena.invalid/checkout/cancel",
		ClientReference: "intent-stub",
		IdempotencyKey:  "intent-stub",
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession error = %v", err)
	}

	// The answer is translated into billing vocabulary, currency included.
	if session.ID.String() != "cs_test_stub" || session.Status != domain.CheckoutStatusComplete {
		t.Fatalf("session = %+v", session)
	}
	if session.PaymentStatus != domain.CheckoutPaymentPaid || !session.PaymentStatus.IsSettled() {
		t.Fatalf("payment status = %q", session.PaymentStatus)
	}
	if session.Mode != domain.CheckoutModeSubscription {
		t.Fatalf("mode = %q", session.Mode)
	}
	if session.PaymentIntentID.String() != "pi_stub" {
		t.Fatalf("payment intent = %q", session.PaymentIntentID)
	}
	if session.AmountMinor != 2490 || session.Currency != domain.CurrencyBRL {
		t.Fatalf("amount = %d %s", session.AmountMinor, session.Currency)
	}
	if session.ClientReference != "intent-stub" || session.Livemode {
		t.Fatalf("reference = %q livemode = %v", session.ClientReference, session.Livemode)
	}
	if session.ExpiresAt == nil || !session.ExpiresAt.Equal(time.Unix(1790000000, 0).UTC()) {
		t.Fatalf("expires at = %v", session.ExpiresAt)
	}
	if session.URL == "" {
		t.Fatal("the hosted checkout URL must reach the caller")
	}

	requests := provider.requests()
	if len(requests) != 1 {
		t.Fatalf("provider received %d requests, want exactly 1", len(requests))
	}
	request := requests[0]
	want := map[string]string{
		"mode":                    "subscription",
		"customer":                "cus_stub",
		"line_items[0][price]":    "price_1QbrMember",
		"line_items[0][quantity]": "1",
		"client_reference_id":     "intent-stub",
		"success_url":             "https://arena.invalid/checkout/success",
		"cancel_url":              "https://arena.invalid/checkout/cancel",
	}
	for field, value := range want {
		if got := request.form.Get(field); got != value {
			t.Errorf("form field %s = %q, want %q", field, got, value)
		}
	}
	if got := request.headers.Get("Idempotency-Key"); got != "intent-stub" {
		t.Errorf("Idempotency-Key = %q", got)
	}
	// Nothing about the amount is accepted from a caller: the price governs.
	for _, field := range []string{"amount_total", "amount", "price"} {
		if request.form.Has(field) {
			t.Errorf("the adapter must never send a caller-supplied %s", field)
		}
	}
}

// TestReadsTranslateProviderState covers both reads: the adapter asks the
// provider for one object by identifier and returns the state as billing
// vocabulary, with the session mode validated and the period normalized.
func TestReadsTranslateProviderState(t *testing.T) {
	t.Parallel()

	provider := newStubProvider(t, map[string]stubResponse{
		"GET /v1/checkout/sessions/cs_test_stub": {status: http.StatusOK, body: stubSessionJSON},
		"GET /v1/subscriptions/sub_stub":         {status: http.StatusOK, body: stubSubscriptionJSON},
	})
	gateway := provider.gateway(t, 5*time.Second)

	session, err := gateway.GetCheckoutSession(context.Background(), domain.StripeCheckoutSessionID("cs_test_stub"))
	if err != nil {
		t.Fatalf("GetCheckoutSession error = %v", err)
	}
	if session.Status != domain.CheckoutStatusComplete || session.AmountMinor != 2490 {
		t.Fatalf("session = %+v", session)
	}

	subscription, err := gateway.GetSubscription(context.Background(), domain.StripeSubscriptionID("sub_stub"))
	if err != nil {
		t.Fatalf("GetSubscription error = %v", err)
	}
	if subscription.ID.String() != "sub_stub" || subscription.Status != domain.SubscriptionActive {
		t.Fatalf("subscription = %+v", subscription)
	}
	if subscription.CustomerID.String() != "cus_stub" || subscription.PriceID.String() != "price_1QintlMember" {
		t.Fatalf("subscription = %+v", subscription)
	}
	if !subscription.CancelAtPeriodEnd || subscription.Livemode {
		t.Fatalf("subscription = %+v", subscription)
	}
	if subscription.CurrentPeriod == nil {
		t.Fatal("the billed period must be carried")
	}
	wantStart := time.Unix(1789000000, 0).UTC()
	if !subscription.CurrentPeriod.Start().Equal(wantStart) || !subscription.CurrentPeriod.End().After(wantStart) {
		t.Fatalf("period = %s", subscription.CurrentPeriod.String())
	}

	requests := provider.requests()
	if len(requests) != 2 {
		t.Fatalf("provider received %d requests, want 2", len(requests))
	}
	// Reads carry no idempotency key: they are naturally repeatable.
	for _, request := range requests {
		if request.headers.Get("Idempotency-Key") != "" {
			t.Errorf("%s %s must not send an idempotency key", request.method, request.path)
		}
	}
}

// TestProviderErrorsMapToTheApplicationVocabulary is the timeout/error mapping
// proof: each provider status lands in the class the use cases decide on, the
// provider request id travels (so the dashboard payload stays reachable) and
// the provider's own message never does.
func TestProviderErrorsMapToTheApplicationVocabulary(t *testing.T) {
	t.Parallel()

	const leakedMessage = "card 4242424242424242 declined for buyer@example.invalid"

	cases := []struct {
		name string
		// answer is the provider's status and error type.
		status  int
		errType string
		want    error
	}{
		{name: "invalid request", status: http.StatusBadRequest, errType: "invalid_request_error", want: application.ErrPaymentGatewayRejected},
		{name: "card declined", status: http.StatusPaymentRequired, errType: "card_error", want: application.ErrPaymentGatewayRejected},
		{name: "unauthenticated", status: http.StatusUnauthorized, errType: "invalid_request_error", want: application.ErrPaymentGatewayMisconfigured},
		{name: "forbidden", status: http.StatusForbidden, errType: "invalid_request_error", want: application.ErrPaymentGatewayMisconfigured},
		{name: "rate limited", status: http.StatusTooManyRequests, errType: "invalid_request_error", want: application.ErrPaymentGatewayUnavailable},
		{name: "provider failure", status: http.StatusInternalServerError, errType: "api_error", want: application.ErrPaymentGatewayUnavailable},
		{name: "provider unavailable", status: http.StatusServiceUnavailable, errType: "api_error", want: application.ErrPaymentGatewayUnavailable},
		{name: "idempotency conflict", status: http.StatusConflict, errType: "idempotency_error", want: application.ErrPaymentGatewayRequestInvalid},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// The message carries data the adapter must never propagate.
			body := map[string]any{"error": map[string]any{
				"type":    testCase.errType,
				"code":    "some_code",
				"param":   "line_items[0][price]",
				"message": leakedMessage,
			}}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}

			provider := newStubProvider(t, map[string]stubResponse{
				"POST /v1/customers": {status: testCase.status, body: string(encoded)},
			})
			gateway := provider.gateway(t, 5*time.Second)

			_, callErr := gateway.CreateCustomer(context.Background(), application.CreateCustomerRequest{
				IdempotencyKey: "intent-1",
			})
			if !errors.Is(callErr, testCase.want) {
				t.Fatalf("error = %v, want %v", callErr, testCase.want)
			}
			if got := application.IsRetryablePaymentGatewayError(callErr); got != (testCase.want == application.ErrPaymentGatewayUnavailable) {
				t.Errorf("retryable = %v for %v", got, callErr)
			}

			message := callErr.Error()
			for _, forbidden := range []string{leakedMessage, "4242", "buyer@example.invalid"} {
				if strings.Contains(message, forbidden) {
					t.Errorf("error leaked provider payload %q: %s", forbidden, message)
				}
			}
			if !strings.Contains(message, "status") || !strings.Contains(message, "request_id") {
				t.Errorf("error must carry the classification detail: %s", message)
			}

			// A refused call is never retried behind the caller's back.
			if requests := provider.requests(); len(requests) != 1 {
				t.Errorf("provider received %d requests, want exactly 1", len(requests))
			}
		})
	}
}

// TestTransportFailuresAreRetryable distinguishes a provider that cannot be
// reached from one that does not answer in time: both leave the outcome
// unknown, so both are retryable with the same idempotency key.
func TestTransportFailuresAreRetryable(t *testing.T) {
	t.Parallel()

	t.Run("unreachable provider", func(t *testing.T) {
		t.Parallel()

		provider := newStubProvider(t, nil)
		gateway := provider.gateway(t, 2*time.Second)
		provider.server.Close() // the endpoint goes away after the gateway exists

		_, err := gateway.GetSubscription(context.Background(), domain.StripeSubscriptionID("sub_stub"))
		if !errors.Is(err, application.ErrPaymentGatewayUnavailable) {
			t.Fatalf("error = %v, want ErrPaymentGatewayUnavailable", err)
		}
		if !application.IsRetryablePaymentGatewayError(err) {
			t.Error("an unreachable provider must be retryable")
		}
		if strings.Contains(err.Error(), provider.server.URL) {
			t.Errorf("error must not echo the endpoint: %s", err)
		}
	})

	t.Run("provider stalls past the deadline", func(t *testing.T) {
		t.Parallel()

		provider := newStubProvider(t, map[string]stubResponse{
			"GET /v1/subscriptions/sub_stub": {status: http.StatusOK, body: stubSubscriptionJSON},
		})
		provider.delay = 400 * time.Millisecond
		gateway := provider.gateway(t, 50*time.Millisecond)

		started := time.Now()
		_, err := gateway.GetSubscription(context.Background(), domain.StripeSubscriptionID("sub_stub"))
		elapsed := time.Since(started)

		if !errors.Is(err, application.ErrPaymentGatewayTimeout) {
			t.Fatalf("error = %v, want ErrPaymentGatewayTimeout", err)
		}
		if !application.IsRetryablePaymentGatewayError(err) {
			t.Error("a timeout must be retryable with the same idempotency key")
		}
		if elapsed > time.Second {
			t.Errorf("the configured 50ms deadline was not enforced: %s", elapsed)
		}
	})

	t.Run("provider stalls past the deadline on a mutation", func(t *testing.T) {
		t.Parallel()

		provider := newStubProvider(t, map[string]stubResponse{
			"POST /v1/checkout/sessions": {status: http.StatusOK, body: stubSessionJSON},
		})
		provider.delay = 400 * time.Millisecond
		gateway := provider.gateway(t, 50*time.Millisecond)

		_, err := gateway.CreateCheckoutSession(context.Background(), application.CreateCheckoutSessionRequest{
			CustomerID:      domain.StripeCustomerID("cus_stub"),
			PriceID:         domain.StripePriceID("price_1QbrMember"),
			Mode:            domain.CheckoutModePayment,
			SuccessURL:      "https://arena.invalid/ok",
			CancelURL:       "https://arena.invalid/no",
			ClientReference: "intent-1",
			IdempotencyKey:  "intent-1",
		})
		if !errors.Is(err, application.ErrPaymentGatewayTimeout) {
			t.Fatalf("error = %v, want ErrPaymentGatewayTimeout", err)
		}
	})
}

// TestAnswersOutsideTheContractAreRefused proves a provider answer the schema
// could not store is refused as a broken integration instead of being trusted:
// silently accepting it would corrupt local state.
func TestAnswersOutsideTheContractAreRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		route string
		body  string
		call  func(gateway *stripeadapter.Gateway) error
	}{
		{
			name:  "session identifier without the mode prefix",
			route: "POST /v1/checkout/sessions",
			body:  strings.Replace(stubSessionJSON, "cs_test_stub", "cs_stub", 1),
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreateCheckoutSession(context.Background(), application.CreateCheckoutSessionRequest{
					CustomerID:      domain.StripeCustomerID("cus_stub"),
					PriceID:         domain.StripePriceID("price_1QbrMember"),
					Mode:            domain.CheckoutModeSubscription,
					SuccessURL:      "https://arena.invalid/ok",
					CancelURL:       "https://arena.invalid/no",
					ClientReference: "intent-1",
					IdempotencyKey:  "intent-1",
				})
				return err
			},
		},
		{
			name:  "session from the other provider mode",
			route: "POST /v1/checkout/sessions",
			body:  strings.Replace(stubSessionJSON, "cs_test_stub", "cs_live_stub", 1),
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreateCheckoutSession(context.Background(), application.CreateCheckoutSessionRequest{
					CustomerID:      domain.StripeCustomerID("cus_stub"),
					PriceID:         domain.StripePriceID("price_1QbrMember"),
					Mode:            domain.CheckoutModeSubscription,
					SuccessURL:      "https://arena.invalid/ok",
					CancelURL:       "https://arena.invalid/no",
					ClientReference: "intent-1",
					IdempotencyKey:  "intent-1",
				})
				return err
			},
		},
		{
			name:  "unknown session status",
			route: "POST /v1/checkout/sessions",
			body:  strings.Replace(stubSessionJSON, `"status": "complete"`, `"status": "settled"`, 1),
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreateCheckoutSession(context.Background(), application.CreateCheckoutSessionRequest{
					CustomerID:      domain.StripeCustomerID("cus_stub"),
					PriceID:         domain.StripePriceID("price_1QbrMember"),
					Mode:            domain.CheckoutModeSubscription,
					SuccessURL:      "https://arena.invalid/ok",
					CancelURL:       "https://arena.invalid/no",
					ClientReference: "intent-1",
					IdempotencyKey:  "intent-1",
				})
				return err
			},
		},
		{
			name:  "currency outside the catalog vocabulary",
			route: "POST /v1/checkout/sessions",
			body:  strings.Replace(stubSessionJSON, `"currency": "brl"`, `"currency": "eur"`, 1),
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreateCheckoutSession(context.Background(), application.CreateCheckoutSessionRequest{
					CustomerID:      domain.StripeCustomerID("cus_stub"),
					PriceID:         domain.StripePriceID("price_1QbrMember"),
					Mode:            domain.CheckoutModeSubscription,
					SuccessURL:      "https://arena.invalid/ok",
					CancelURL:       "https://arena.invalid/no",
					ClientReference: "intent-1",
					IdempotencyKey:  "intent-1",
				})
				return err
			},
		},
		{
			name:  "unknown subscription status",
			route: "GET /v1/subscriptions/sub_stub",
			body:  strings.Replace(stubSubscriptionJSON, `"status": "active"`, `"status": "dunning"`, 1),
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.GetSubscription(context.Background(), domain.StripeSubscriptionID("sub_stub"))
				return err
			},
		},
		{
			name:  "subscription without a price",
			route: "GET /v1/subscriptions/sub_stub",
			body:  `{"id":"sub_stub","status":"active","customer":{"id":"cus_stub"},"items":{"data":[]}}`,
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.GetSubscription(context.Background(), domain.StripeSubscriptionID("sub_stub"))
				return err
			},
		},
		{
			name:  "half a billing period",
			route: "GET /v1/subscriptions/sub_stub",
			body:  strings.Replace(stubSubscriptionJSON, `"current_period_end": 1791592000`, `"current_period_end": 0`, 1),
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.GetSubscription(context.Background(), domain.StripeSubscriptionID("sub_stub"))
				return err
			},
		},
		{
			name:  "error body that is not a provider error",
			route: "POST /v1/customers",
			body:  `{"oops": true}`,
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreateCustomer(context.Background(), application.CreateCustomerRequest{IdempotencyKey: "intent-1"})
				return err
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			provider := newStubProvider(t, map[string]stubResponse{
				testCase.route: {status: http.StatusOK, body: testCase.body},
			})
			gateway := provider.gateway(t, 5*time.Second)

			err := testCase.call(gateway)
			if !errors.Is(err, application.ErrPaymentGatewayMisconfigured) {
				t.Fatalf("error = %v, want ErrPaymentGatewayMisconfigured", err)
			}
			if application.IsRetryablePaymentGatewayError(err) {
				t.Error("a broken contract must not be retried blindly")
			}
		})
	}
}

// TestCallerDefectsNeverReachTheProvider proves every request the adapter
// refuses is refused before a socket is opened: a caller defect must not turn
// into a provider request.
func TestCallerDefectsNeverReachTheProvider(t *testing.T) {
	t.Parallel()

	sessionRequest := application.CreateCheckoutSessionRequest{
		CustomerID:      domain.StripeCustomerID("cus_stub"),
		PriceID:         domain.StripePriceID("price_1QbrMember"),
		Mode:            domain.CheckoutModePayment,
		SuccessURL:      "https://arena.invalid/ok",
		CancelURL:       "https://arena.invalid/no",
		ClientReference: "intent-1",
		IdempotencyKey:  "intent-1",
	}

	cases := []struct {
		name string
		call func(gateway *stripeadapter.Gateway) error
	}{
		{
			name: "customer without an idempotency key",
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreateCustomer(context.Background(), application.CreateCustomerRequest{})
				return err
			},
		},
		{
			name: "idempotency key too long",
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreateCustomer(context.Background(), application.CreateCustomerRequest{
					IdempotencyKey: strings.Repeat("a", 256),
				})
				return err
			},
		},
		{
			name: "idempotency key with a newline",
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreateCustomer(context.Background(), application.CreateCustomerRequest{
					IdempotencyKey: "intent\n1",
				})
				return err
			},
		},
		{
			name: "session without a customer",
			call: func(gateway *stripeadapter.Gateway) error {
				request := sessionRequest
				request.CustomerID = ""
				_, err := gateway.CreateCheckoutSession(context.Background(), request)
				return err
			},
		},
		{
			name: "session without a price",
			call: func(gateway *stripeadapter.Gateway) error {
				request := sessionRequest
				request.PriceID = ""
				_, err := gateway.CreateCheckoutSession(context.Background(), request)
				return err
			},
		},
		{
			name: "session with an unsupported mode",
			call: func(gateway *stripeadapter.Gateway) error {
				request := sessionRequest
				request.Mode = domain.CheckoutMode("setup")
				_, err := gateway.CreateCheckoutSession(context.Background(), request)
				return err
			},
		},
		{
			name: "session without a client reference",
			call: func(gateway *stripeadapter.Gateway) error {
				request := sessionRequest
				request.ClientReference = ""
				_, err := gateway.CreateCheckoutSession(context.Background(), request)
				return err
			},
		},
		{
			name: "session with a relative success URL",
			call: func(gateway *stripeadapter.Gateway) error {
				request := sessionRequest
				request.SuccessURL = "/checkout/success"
				_, err := gateway.CreateCheckoutSession(context.Background(), request)
				return err
			},
		},
		{
			name: "session with a non-HTTP cancel URL",
			call: func(gateway *stripeadapter.Gateway) error {
				request := sessionRequest
				request.CancelURL = "javascript:alert(1)"
				_, err := gateway.CreateCheckoutSession(context.Background(), request)
				return err
			},
		},
		{
			name: "session without an idempotency key",
			call: func(gateway *stripeadapter.Gateway) error {
				request := sessionRequest
				request.IdempotencyKey = ""
				_, err := gateway.CreateCheckoutSession(context.Background(), request)
				return err
			},
		},
		{
			name: "read of an empty session identifier",
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.GetCheckoutSession(context.Background(), "")
				return err
			},
		},
		{
			name: "read of an empty subscription identifier",
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.GetSubscription(context.Background(), "")
				return err
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			provider := newStubProvider(t, map[string]stubResponse{})
			gateway := provider.gateway(t, 5*time.Second)

			err := testCase.call(gateway)
			if !errors.Is(err, application.ErrPaymentGatewayRequestInvalid) {
				t.Fatalf("error = %v, want ErrPaymentGatewayRequestInvalid", err)
			}
			if requests := provider.requests(); len(requests) != 0 {
				t.Errorf("the provider received %d requests; a caller defect must not reach it", len(requests))
			}
		})
	}
}

// TestGatewayNeverRendersTheCredential closes the leak paths of the adapter:
// no rendering of the gateway may print the secret, in any verb.
func TestGatewayNeverRendersTheCredential(t *testing.T) {
	t.Parallel()

	provider := newStubProvider(t, map[string]stubResponse{
		"POST /v1/customers": {status: http.StatusUnauthorized, body: `{"error":{"type":"invalid_request_error","message":"invalid key"}}`},
	})
	gateway := provider.gateway(t, 5*time.Second)

	renderings := []string{
		fmt.Sprintf("%v", gateway),
		fmt.Sprintf("%s", gateway),
		fmt.Sprintf("%+v", gateway),
		fmt.Sprintf("%#v", gateway),
	}
	for index, rendered := range renderings {
		if strings.Contains(rendered, stubSecret) || strings.Contains(rendered, "sk_test") {
			t.Errorf("rendering %d leaked the credential: %q", index, rendered)
		}
	}
	if !strings.HasPrefix(renderings[0], "stripe gateway{") {
		t.Errorf("the adapter should render a diagnostic form instead of its fields, got %q", renderings[0])
	}

	// The failure path must not carry the credential either.
	_, err := gateway.CreateCustomer(context.Background(), application.CreateCustomerRequest{IdempotencyKey: "intent-1"})
	if err == nil {
		t.Fatal("expected the provider rejection to surface")
	}
	if strings.Contains(err.Error(), stubSecret) || strings.Contains(err.Error(), "sk_test") {
		t.Errorf("error leaked the credential: %v", err)
	}
}

// TestNewGatewayRefusesHalfConfiguration covers the boot-time gate: a gateway
// that could reach the provider half configured is never built.
func TestNewGatewayRefusesHalfConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		config stripeadapter.Config
		want   error
	}{
		{name: "no secret", config: stripeadapter.Config{Timeout: time.Second}, want: stripeadapter.ErrMissingSecretKey},
		{name: "no timeout", config: stripeadapter.Config{SecretKey: stubSecret}, want: stripeadapter.ErrInvalidTimeout},
		{name: "negative timeout", config: stripeadapter.Config{SecretKey: stubSecret, Timeout: -time.Second}, want: stripeadapter.ErrInvalidTimeout},
		{name: "relative base URL", config: stripeadapter.Config{SecretKey: stubSecret, Timeout: time.Second, BaseURL: "/v1"}, want: stripeadapter.ErrInvalidBaseURL},
		{name: "unsupported scheme", config: stripeadapter.Config{SecretKey: stubSecret, Timeout: time.Second, BaseURL: "ftp://stripe.invalid"}, want: stripeadapter.ErrInvalidBaseURL},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			gateway, err := stripeadapter.NewGateway(testCase.config)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
			if gateway != nil {
				t.Fatal("a refused configuration must not build a gateway")
			}
		})
	}

	gateway, err := stripeadapter.NewGateway(stripeadapter.Config{SecretKey: stubSecret, Timeout: time.Second})
	if err != nil {
		t.Fatalf("a complete configuration must build: %v", err)
	}
	if gateway == nil {
		t.Fatal("expected a gateway")
	}
}

const stubPortalJSON = `{
  "id": "bps_stub",
  "object": "billing_portal.session",
  "url": "https://billing.stripe.invalid/session/bps_stub"
}`

// TestCreatePortalSessionOpensAllowlistedReturn proves the portal call reaches
// the provider authenticated, carrying the customer, the allowlisted return
// URL and the caller's key, and that only the URL leaves the adapter.
func TestCreatePortalSessionOpensAllowlistedReturn(t *testing.T) {
	t.Parallel()

	provider := newStubProvider(t, map[string]stubResponse{
		"POST /v1/billing_portal/sessions": {status: http.StatusOK, body: stubPortalJSON},
	})
	gateway := provider.gateway(t, 5*time.Second)

	customerID, _ := domain.ParseStripeCustomerID("cus_stub")
	session, err := gateway.CreatePortalSession(context.Background(), application.CreatePortalSessionRequest{
		CustomerID:     customerID,
		ReturnURL:      "https://arena.invalid/billing/return",
		IdempotencyKey: "portal-intent-1",
	})
	if err != nil {
		t.Fatalf("CreatePortalSession error = %v", err)
	}
	if session.URL != "https://billing.stripe.invalid/session/bps_stub" {
		t.Fatalf("portal URL = %q", session.URL)
	}

	requests := provider.requests()
	if len(requests) != 1 {
		t.Fatalf("provider received %d requests, want exactly 1", len(requests))
	}
	request := requests[0]
	if request.form.Get("customer") != "cus_stub" {
		t.Errorf("customer = %q, want cus_stub", request.form.Get("customer"))
	}
	if request.form.Get("return_url") != "https://arena.invalid/billing/return" {
		t.Errorf("return_url = %q", request.form.Get("return_url"))
	}
	if got := request.headers.Get("Idempotency-Key"); got != "portal-intent-1" {
		t.Errorf("Idempotency-Key = %q", got)
	}
	if strings.Contains(session.URL, stubSecret) {
		t.Fatal("portal URL must never carry the secret")
	}
}
