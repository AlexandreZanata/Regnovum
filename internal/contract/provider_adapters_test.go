package contract_test

// P25-T08 — contratos dos providers contra simuladores locais.
//
// Cada adapter externo (Stripe gateway + webhook, Resend, Turnstile,
// Sentry/PostHog) fala com o simulador do seu protocolo em
// internal/platform/providersim, nunca com a rede: o teste prova as quatro
// propriedades que a tarefa exige — campos desconhecidos tolerados conforme
// o contrato, campos obrigatórios ausentes recusados de forma segura, retry
// com a mesma chave sem duplicar efeito e payload sensível fora de erros,
// logs e corpos — além de request/response/assinatura/idempotência/timeouts
// por provider.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	stripeadapter "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/resend"
	notifyapp "github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	notifydomain "github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
	"github.com/AlexandreZanata/Regnovum/internal/platform/providersim"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

const (
	providerTimeout  = 2 * time.Second
	tightTimeout     = 60 * time.Millisecond
	simSlowDelay     = 500 * time.Millisecond
	providerHostname = "arena.example"
)

// waitForCalls polls the simulator until the address received want calls or
// the deadline passes. Telemetry adapters post from a goroutine, so the
// delivery is eventually consistent by contract.
func waitForCalls(t *testing.T, simulator *providersim.Simulator, method, path string, want int) []providersim.Call {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls := simulator.CallsTo(method, path)
		if len(calls) >= want {
			return calls
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s received %d calls to %s %s, want at least %d", simulator.Name(), len(calls), method, path, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func stripeGateway(t *testing.T, simulator *providersim.Simulator, timeout time.Duration) *stripeadapter.Gateway {
	t.Helper()
	gateway, err := stripeadapter.NewGateway(stripeadapter.Config{
		SecretKey: providersim.StripeSecretKey,
		Timeout:   timeout,
		BaseURL:   simulator.URL(),
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return gateway
}

func checkoutRequest() billingapp.CreateCheckoutSessionRequest {
	return billingapp.CreateCheckoutSessionRequest{
		CustomerID:      billingdomain.StripeCustomerID(providersim.StripeCustomerID),
		PriceID:         billingdomain.StripePriceID(providersim.StripePriceID),
		Mode:            billingdomain.CheckoutModeSubscription,
		SuccessURL:      "https://arena.invalid/checkout/success",
		CancelURL:       "https://arena.invalid/checkout/cancel",
		ClientReference: "intent-sim",
		IdempotencyKey:  "intent-sim-1",
	}
}

// withUnknownFields injects unknown top-level fields into a JSON document.
// The adapter must ignore what it does not read.
func withUnknownFields(t *testing.T, document string) string {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(document), &decoded); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	decoded["unknown_future_field"] = "tolerate-me"
	decoded["another_unknown"] = map[string]any{"nested": []any{1, 2, 3}}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("encode document: %v", err)
	}
	return string(encoded)
}

func TestStripeGatewaySuccessAgainstSimulator(t *testing.T) {
	t.Parallel()
	simulator := providersim.NewStripe(t)
	gateway := stripeGateway(t, simulator, providerTimeout)
	ctx := context.Background()

	customer, err := gateway.CreateCustomer(ctx, billingapp.CreateCustomerRequest{IdempotencyKey: "cust-key-1"})
	if err != nil {
		t.Fatalf("CreateCustomer: %v", err)
	}
	if customer.ID.String() != providersim.StripeCustomerID || customer.Livemode {
		t.Fatalf("customer = %+v", customer)
	}

	session, err := gateway.CreateCheckoutSession(ctx, checkoutRequest())
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if session.ID.String() != providersim.StripeCheckoutID || session.AmountMinor != 2490 {
		t.Fatalf("session = %+v", session)
	}

	fetched, err := gateway.GetCheckoutSession(ctx, billingdomain.StripeCheckoutSessionID(providersim.StripeCheckoutID))
	if err != nil {
		t.Fatalf("GetCheckoutSession: %v", err)
	}
	if fetched.ID != session.ID || fetched.PaymentStatus != session.PaymentStatus {
		t.Fatalf("fetched = %+v, created = %+v", fetched, session)
	}

	subscription, err := gateway.GetSubscription(ctx, billingdomain.StripeSubscriptionID(providersim.StripeSubscriptionID))
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if subscription.CustomerID.String() != providersim.StripeCustomerID || subscription.PriceID.String() != providersim.StripePriceID {
		t.Fatalf("subscription = %+v", subscription)
	}

	portal, err := gateway.CreatePortalSession(ctx, billingapp.CreatePortalSessionRequest{
		CustomerID:     billingdomain.StripeCustomerID(providersim.StripeCustomerID),
		ReturnURL:      "https://arena.invalid/billing/return",
		IdempotencyKey: "portal-key-1",
	})
	if err != nil {
		t.Fatalf("CreatePortalSession: %v", err)
	}
	if portal.URL == "" {
		t.Fatal("portal URL must reach the caller")
	}

	// Mutating calls carry the key; reads do not.
	for _, address := range []string{providersim.StripeCustomers, providersim.StripeCheckoutCreate, providersim.StripePortalCreate} {
		method, path, _ := strings.Cut(address, " ")
		for _, call := range simulator.CallsTo(method, path) {
			if call.Header("Idempotency-Key") == "" {
				t.Errorf("%s must carry an Idempotency-Key", address)
			}
		}
	}
	for _, address := range []string{providersim.StripeCheckoutGet, providersim.StripeSubscriptionGet} {
		method, path, _ := strings.Cut(address, " ")
		prefix := strings.TrimSuffix(path, "/*")
		for _, call := range simulator.Calls() {
			if call.Method == method && strings.HasPrefix(call.Path, prefix) {
				if call.Header("Idempotency-Key") != "" {
					t.Errorf("read %s %s must not send an idempotency key", call.Method, call.Path)
				}
			}
		}
	}
	if len(simulator.Unexpected()) != 0 {
		t.Errorf("unexpected calls: %+v", simulator.Unexpected())
	}
}

func TestStripeGatewayToleratesUnknownFields(t *testing.T) {
	t.Parallel()
	simulator := providersim.NewStripe(t)
	simulator.Route(providersim.StripeCheckoutCreate,
		providersim.Reply(http.StatusOK, withUnknownFields(t, providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim"))))
	simulator.Route(providersim.StripeSubscriptionGet,
		providersim.Reply(http.StatusOK, withUnknownFields(t, providersim.StripeSubscriptionBody(providersim.StripeSubscriptionID, providersim.StripeCustomerID, providersim.StripePriceID))))
	gateway := stripeGateway(t, simulator, providerTimeout)

	if _, err := gateway.CreateCheckoutSession(context.Background(), checkoutRequest()); err != nil {
		t.Fatalf("session with unknown fields refused: %v", err)
	}
	if _, err := gateway.GetSubscription(context.Background(), billingdomain.StripeSubscriptionID(providersim.StripeSubscriptionID)); err != nil {
		t.Fatalf("subscription with unknown fields refused: %v", err)
	}
}

func TestStripeGatewayRefusesIncompleteAnswers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		route string
		body  string
		call  func(*stripeadapter.Gateway) error
	}{
		{
			name:  "session without identifier",
			route: providersim.StripeCheckoutCreate,
			body:  `{"object":"checkout.session","status":"complete","payment_status":"paid","mode":"subscription","amount_total":2490,"currency":"brl"}`,
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreateCheckoutSession(context.Background(), checkoutRequest())
				return err
			},
		},
		{
			name:  "session with unknown status",
			route: providersim.StripeCheckoutCreate,
			body:  strings.Replace(providersim.StripeCheckoutSessionBody(providersim.StripeCheckoutID, "intent-sim"), `"status":"complete"`, `"status":"settled"`, 1),
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreateCheckoutSession(context.Background(), checkoutRequest())
				return err
			},
		},
		{
			name:  "subscription without price",
			route: providersim.StripeSubscriptionGet,
			body:  `{"id":"sub_sim","object":"subscription","status":"active","customer":{"id":"cus_sim"},"items":{"object":"list","data":[]}}`,
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.GetSubscription(context.Background(), billingdomain.StripeSubscriptionID(providersim.StripeSubscriptionID))
				return err
			},
		},
		{
			name:  "portal without url",
			route: providersim.StripePortalCreate,
			body:  `{"id":"bps_sim","object":"billing_portal.session","livemode":false}`,
			call: func(gateway *stripeadapter.Gateway) error {
				_, err := gateway.CreatePortalSession(context.Background(), billingapp.CreatePortalSessionRequest{
					CustomerID:     billingdomain.StripeCustomerID(providersim.StripeCustomerID),
					ReturnURL:      "https://arena.invalid/billing/return",
					IdempotencyKey: "portal-key-2",
				})
				return err
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			simulator := providersim.NewStripe(t)
			simulator.Route(testCase.route, providersim.Reply(http.StatusOK, testCase.body))
			gateway := stripeGateway(t, simulator, providerTimeout)

			err := testCase.call(gateway)
			if !errors.Is(err, billingapp.ErrPaymentGatewayMisconfigured) {
				t.Fatalf("error = %v, want ErrPaymentGatewayMisconfigured", err)
			}
			if billingapp.IsRetryablePaymentGatewayError(err) {
				t.Error("a broken contract must not be retried blindly")
			}
		})
	}
}

func TestStripeGatewayRetryKeepsIdempotencyKey(t *testing.T) {
	t.Parallel()
	simulator := providersim.NewStripe(t)
	gateway := stripeGateway(t, simulator, providerTimeout)
	ctx := context.Background()

	for index := 0; index < 2; index++ {
		if _, err := gateway.CreateCustomer(ctx, billingapp.CreateCustomerRequest{IdempotencyKey: "cust-retry-1"}); err != nil {
			t.Fatalf("attempt %d: %v", index, err)
		}
	}
	calls := simulator.CallsTo("POST", "/v1/customers")
	if len(calls) != 2 {
		t.Fatalf("provider received %d requests, want exactly 2 (one per attempt, no hidden retry)", len(calls))
	}
	if calls[0].Header("Idempotency-Key") != "cust-retry-1" || calls[1].Header("Idempotency-Key") != "cust-retry-1" {
		t.Errorf("both attempts must carry the same key: %q and %q", calls[0].Header("Idempotency-Key"), calls[1].Header("Idempotency-Key"))
	}
}

func TestStripeGatewayClassifiesFailuresWithoutLeaking(t *testing.T) {
	t.Parallel()
	const cardHint = "4242424242424242"
	simulator := providersim.NewStripe(t)
	simulator.Route(providersim.StripeCustomers,
		providersim.StripeError(http.StatusPaymentRequired, "card for buyer@example.invalid declined "+cardHint))
	gateway := stripeGateway(t, simulator, providerTimeout)

	_, err := gateway.CreateCustomer(context.Background(), billingapp.CreateCustomerRequest{IdempotencyKey: "cust-1"})
	if !errors.Is(err, billingapp.ErrPaymentGatewayRejected) {
		t.Fatalf("error = %v, want ErrPaymentGatewayRejected", err)
	}
	for _, forbidden := range []string{cardHint, "buyer@example.invalid", providersim.StripeSecretKey} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("error leaked %q: %s", forbidden, err)
		}
	}
	if rendered := strings.Join([]string{
		gateway.String(), func() string { return stringOrEmpty(gateway) }(),
	}, " "); strings.Contains(rendered, providersim.StripeSecretKey) {
		t.Errorf("gateway rendering leaked the secret: %q", rendered)
	}

	down := providersim.NewStripe(t, providersim.WithoutStrictness())
	down.Down(true)
	unavailableGateway := stripeGateway(t, down, providerTimeout)
	_, err = unavailableGateway.CreateCustomer(context.Background(), billingapp.CreateCustomerRequest{IdempotencyKey: "cust-2"})
	if !errors.Is(err, billingapp.ErrPaymentGatewayUnavailable) {
		t.Fatalf("down provider error = %v, want ErrPaymentGatewayUnavailable", err)
	}
	if !billingapp.IsRetryablePaymentGatewayError(err) {
		t.Error("an outage must stay retryable with the same key")
	}
}

func TestStripeGatewayEnforcesTimeout(t *testing.T) {
	t.Parallel()
	simulator := providersim.NewStripe(t)
	simulator.Route(providersim.StripeSubscriptionGet,
		providersim.Slow(providersim.Reply(http.StatusOK, providersim.StripeSubscriptionBody(providersim.StripeSubscriptionID, providersim.StripeCustomerID, providersim.StripePriceID)), simSlowDelay))
	gateway := stripeGateway(t, simulator, tightTimeout)

	started := time.Now()
	_, err := gateway.GetSubscription(context.Background(), billingdomain.StripeSubscriptionID(providersim.StripeSubscriptionID))
	if !errors.Is(err, billingapp.ErrPaymentGatewayTimeout) {
		t.Fatalf("error = %v, want ErrPaymentGatewayTimeout", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("the configured timeout was not enforced: %s", elapsed)
	}
}

func stringOrEmpty(gateway *stripeadapter.Gateway) string {
	return gateway.GoString()
}

func TestStripeWebhookVerifierAgainstSimulator(t *testing.T) {
	t.Parallel()

	clock := testsource.NewClock(time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC))
	verifier, err := stripeadapter.NewWebhookVerifier(providersim.StripeWebhookSecret, 0, clock)
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}

	sign := func(payload string, at time.Time) (string, string) {
		return providersim.SignStripeWebhook(providersim.StripeWebhookSecret, at, payload)
	}

	t.Run("signed delivery verifies", func(t *testing.T) {
		t.Parallel()
		payload := providersim.StripeEventBody("evt_sim_1", "checkout.session.completed", clock.Now(), map[string]any{"id": providersim.StripeCheckoutID})
		signature, timestamp := sign(payload, clock.Now())
		if err := verifier.Verify([]byte(payload), signature, timestamp); err != nil {
			t.Fatalf("Verify: %v", err)
		}
	})

	t.Run("unknown event fields are tolerated", func(t *testing.T) {
		t.Parallel()
		payload := withUnknownFields(t, providersim.StripeEventBody("evt_sim_2", "checkout.session.completed", clock.Now(), map[string]any{"id": providersim.StripeCheckoutID}))
		signature, timestamp := sign(payload, clock.Now())
		if err := verifier.Verify([]byte(payload), signature, timestamp); err != nil {
			t.Fatalf("delivery with unknown fields refused: %v", err)
		}
	})

	t.Run("duplicate delivery verifies identically", func(t *testing.T) {
		t.Parallel()
		payload := providersim.StripeEventBody("evt_sim_3", "invoice.paid", clock.Now(), map[string]any{"id": providersim.StripeSubscriptionID})
		signature, timestamp := sign(payload, clock.Now())
		for index := 0; index < 2; index++ {
			if err := verifier.Verify([]byte(payload), signature, timestamp); err != nil {
				t.Fatalf("attempt %d: %v", index, err)
			}
		}
	})

	t.Run("tampered payload fails safe", func(t *testing.T) {
		t.Parallel()
		payload := providersim.StripeEventBody("evt_sim_4", "invoice.paid", clock.Now(), map[string]any{"id": providersim.StripeSubscriptionID})
		signature, timestamp := sign(payload, clock.Now())
		if err := verifier.Verify([]byte(payload+"tampered"), signature, timestamp); err == nil {
			t.Fatal("tampered payload verified")
		} else if !errors.Is(err, billingapp.ErrWebhookSignatureInvalid) {
			t.Fatalf("error = %v, want ErrWebhookSignatureInvalid", err)
		}
	})

	t.Run("stale and future timestamps fail safe", func(t *testing.T) {
		t.Parallel()
		payload := providersim.StripeEventBody("evt_sim_5", "invoice.paid", clock.Now(), map[string]any{"id": "x"})
		staleAt := clock.Now().Add(-time.Hour)
		stalePayload := providersim.StripeEventBody("evt_sim_5", "invoice.paid", staleAt, map[string]any{"id": "x"})
		signature, timestamp := sign(stalePayload, staleAt)
		if err := verifier.Verify([]byte(stalePayload), signature, timestamp); !errors.Is(err, billingapp.ErrWebhookSignatureInvalid) {
			t.Fatalf("stale delivery error = %v, want ErrWebhookSignatureInvalid", err)
		}

		futureAt := clock.Now().Add(time.Hour)
		futurePayload := providersim.StripeEventBody("evt_sim_6", "invoice.paid", futureAt, map[string]any{"id": "x"})
		futureSignature, futureTimestamp := sign(futurePayload, futureAt)
		if err := verifier.Verify([]byte(futurePayload), futureSignature, futureTimestamp); !errors.Is(err, billingapp.ErrWebhookSignatureInvalid) {
			t.Fatalf("future delivery error = %v, want ErrWebhookSignatureInvalid", err)
		}
		_ = payload
	})

	t.Run("missing headers fail safe without secret", func(t *testing.T) {
		t.Parallel()
		payload := providersim.StripeEventBody("evt_sim_7", "invoice.paid", clock.Now(), map[string]any{"id": "x"})
		_, timestamp := sign(payload, clock.Now())
		if err := verifier.Verify([]byte(payload), "", timestamp); !errors.Is(err, billingapp.ErrWebhookSignatureInvalid) {
			t.Fatalf("empty signature error = %v, want ErrWebhookSignatureInvalid", err)
		}
		signature, _ := sign(payload, clock.Now())
		if err := verifier.Verify([]byte(payload), signature, ""); !errors.Is(err, billingapp.ErrWebhookSignatureInvalid) {
			t.Fatalf("empty timestamp error = %v, want ErrWebhookSignatureInvalid", err)
		}
		if strings.Contains(signature, providersim.StripeWebhookSecret) {
			t.Error("signature must not carry the secret in clear")
		}
	})
}

func resendMessage(t *testing.T) notifydomain.Message {
	t.Helper()
	body, err := notifydomain.NewBody("Confirme seu email", "Use o código K7QP.", "<p>K7QP</p>")
	if err != nil {
		t.Fatalf("NewBody: %v", err)
	}
	message, err := notifydomain.NewMessage(
		"ana.silva@example.com",
		notifydomain.LocaleBrazilianPortuguese,
		notifydomain.TemplateVerification,
		body,
		"email:verification:sim-1",
	)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return message
}

func resendSender(t *testing.T, simulator *providersim.Simulator, logs *bytes.Buffer, timeout time.Duration) *resend.Sender {
	t.Helper()
	sender, err := resend.NewSender(resend.Config{
		APIToken: providersim.ResendAPIKey,
		From:     providersim.ResendSenderAddress,
		BaseURL:  simulator.URL(),
		Timeout:  timeout,
		Logger:   slog.New(slog.NewJSONHandler(logs, nil)),
	})
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	return sender
}

func TestResendSenderAgainstSimulator(t *testing.T) {
	t.Parallel()

	t.Run("success delivers with auth and idempotency", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		simulator := providersim.NewResend(t, providersim.CapturingBodies())
		sender := resendSender(t, simulator, &logs, providerTimeout)

		receipt, err := sender.Send(context.Background(), resendMessage(t))
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if receipt.ProviderID == "" {
			t.Fatal("receipt has no provider id")
		}
		calls := simulator.CallsTo("POST", "/emails")
		if len(calls) != 1 {
			t.Fatalf("provider received %d requests, want 1", len(calls))
		}
		call := calls[0]
		if got := call.Header("Authorization"); got != "Bearer "+providersim.ResendAPIKey {
			t.Errorf("Authorization = %q", got)
		}
		if got := call.Header("Idempotency-Key"); got != "email:verification:sim-1" {
			t.Errorf("Idempotency-Key = %q", got)
		}
		var document map[string]any
		if err := call.Decode(&document); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if document["from"] != providersim.ResendSenderAddress {
			t.Errorf("from = %v", document["from"])
		}
		if strings.Contains(string(call.Body), providersim.ResendAPIKey) {
			t.Error("the credential was serialized into the request body")
		}
	})

	t.Run("unknown success fields are tolerated", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		simulator := providersim.NewResend(t)
		simulator.Route(providersim.ResendEmails, providersim.Reply(http.StatusOK, `{"id":"msg_sim_9","unknown_future":"tolerate","object":"email"}`))
		sender := resendSender(t, simulator, &logs, providerTimeout)
		if _, err := sender.Send(context.Background(), resendMessage(t)); err != nil {
			t.Fatalf("success with unknown fields refused: %v", err)
		}
	})

	t.Run("missing receipt fails retryable", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		simulator := providersim.NewResend(t)
		simulator.Route(providersim.ResendEmails, providersim.Reply(http.StatusOK, `{"object":"email"}`))
		sender := resendSender(t, simulator, &logs, providerTimeout)
		_, err := sender.Send(context.Background(), resendMessage(t))
		if !errors.Is(err, notifyapp.ErrProviderUnavailable) {
			t.Fatalf("error = %v, want ErrProviderUnavailable", err)
		}
		if !notifyapp.IsRetryable(err) {
			t.Error("a missing receipt must stay retryable with the same key")
		}
	})

	t.Run("refusals classify and retry does not duplicate", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		simulator := providersim.NewResend(t)
		simulator.Route(providersim.ResendEmails,
			providersim.ResendError(http.StatusTooManyRequests, "rate_limited", "slow down"),
			providersim.Reply(http.StatusOK, providersim.ResendAcceptedBody("msg_sim_retry")),
		)
		sender := resendSender(t, simulator, &logs, providerTimeout)
		message := resendMessage(t)

		_, err := sender.Send(context.Background(), message)
		if !errors.Is(err, notifyapp.ErrProviderRateLimited) {
			t.Fatalf("first error = %v, want ErrProviderRateLimited", err)
		}
		if !notifyapp.IsRetryable(err) {
			t.Fatal("a throttled send must be retryable")
		}
		if _, err := sender.Send(context.Background(), message); err != nil {
			t.Fatalf("retry: %v", err)
		}
		calls := simulator.CallsTo("POST", "/emails")
		if len(calls) != 2 {
			t.Fatalf("provider received %d requests, want 2 (one per attempt)", len(calls))
		}
		if calls[0].Header("Idempotency-Key") != calls[1].Header("Idempotency-Key") {
			t.Errorf("retry changed the key: %q vs %q", calls[0].Header("Idempotency-Key"), calls[1].Header("Idempotency-Key"))
		}

		rejected := providersim.NewResend(t)
		rejected.Route(providersim.ResendEmails, providersim.ResendError(http.StatusUnprocessableEntity, "validation_error", "invalid to: ana.silva@example.com"))
		var rejectedLogs bytes.Buffer
		rejectedSender := resendSender(t, rejected, &rejectedLogs, providerTimeout)
		_, err = rejectedSender.Send(context.Background(), message)
		if !errors.Is(err, notifyapp.ErrProviderRejected) {
			t.Fatalf("rejection error = %v, want ErrProviderRejected", err)
		}
		if notifyapp.IsRetryable(err) {
			t.Error("a rejection must not be retried")
		}
		if strings.Contains(err.Error(), "ana.silva@example.com") {
			t.Errorf("error quoted the recipient: %s", err)
		}
	})

	t.Run("slow provider crosses the timeout", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		simulator := providersim.NewResend(t)
		simulator.Route(providersim.ResendEmails,
			providersim.Slow(providersim.Reply(http.StatusOK, providersim.ResendAcceptedBody("msg_late")), simSlowDelay))
		sender := resendSender(t, simulator, &logs, tightTimeout)
		_, err := sender.Send(context.Background(), resendMessage(t))
		if !errors.Is(err, notifyapp.ErrProviderTimeout) {
			t.Fatalf("error = %v, want ErrProviderTimeout", err)
		}
		if !notifyapp.IsRetryable(err) {
			t.Error("a timeout must stay retryable with the same key")
		}
	})

	t.Run("logs never carry the recipient or the secrets", func(t *testing.T) {
		t.Parallel()
		for _, outcome := range []struct {
			name   string
			answer providersim.Answer
		}{
			{"success", providersim.Reply(http.StatusOK, providersim.ResendAcceptedBody("msg_sim_log"))},
			{"rejected", providersim.ResendError(http.StatusUnprocessableEntity, "validation_error", "invalid to: ana.silva@example.com")},
		} {
			t.Run(outcome.name, func(t *testing.T) {
				t.Parallel()
				var logs bytes.Buffer
				simulator := providersim.NewResend(t)
				simulator.Route(providersim.ResendEmails, outcome.answer)
				sender := resendSender(t, simulator, &logs, providerTimeout)
				_, _ = sender.Send(context.Background(), resendMessage(t))
				recorded := logs.String()
				if recorded == "" {
					t.Fatal("no log record was written")
				}
				for _, forbidden := range []string{
					"ana.silva@example.com", providersim.ResendAPIKey,
					"email:verification:sim-1", "Use o código",
				} {
					if strings.Contains(recorded, forbidden) {
						t.Errorf("log quotes %q: %s", forbidden, recorded)
					}
				}
			})
		}
	})
}

func turnstileVerifier(t *testing.T, simulator *providersim.Simulator) turnstile.Verifier {
	t.Helper()
	verifier, err := turnstile.New(turnstile.Config{
		SecretKey: providersim.TurnstileSecretKey,
		Hostname:  providerHostname,
		Endpoint:  simulator.URL() + providersim.TurnstileSiteverifyPath,
		Timeout:   providerTimeout,
	}, "test")
	if err != nil {
		t.Fatalf("turnstile.New: %v", err)
	}
	return verifier
}

func TestTurnstileVerifierAgainstSimulator(t *testing.T) {
	t.Parallel()

	t.Run("solved challenge verifies and checks hostname and action", func(t *testing.T) {
		t.Parallel()
		simulator := providersim.NewTurnstile(t, providerHostname, string(turnstile.ActionSignup), providersim.CapturingBodies())
		verifier := turnstileVerifier(t, simulator)

		if err := verifier.Verify(context.Background(), turnstile.Verification{Token: "solved-token-1", Action: turnstile.ActionSignup}); err != nil {
			t.Fatalf("solved challenge refused: %v", err)
		}
		calls := simulator.CallsTo("POST", providersim.TurnstileSiteverifyPath)
		if len(calls) != 1 {
			t.Fatalf("provider received %d requests, want 1", len(calls))
		}
		if body := string(calls[0].Body); !strings.Contains(body, "response=solved-token-1") {
			t.Errorf("provider did not receive the token: %q", body)
		}
		if body := string(calls[0].Body); strings.Contains(body, providerHostname) && strings.Contains(body, "secret=") {
			t.Errorf("hostname must be checked locally, not sent as proof: %q", body)
		}

		// A token minted for another action is refused even though the
		// provider solved it: the simulator answers for signup only.
		if err := verifier.Verify(context.Background(), turnstile.Verification{Token: "solved-token-2", Action: turnstile.ActionArenaPublish}); err == nil {
			t.Fatal("token spent on another action was accepted")
		}
	})

	t.Run("unknown answer fields are tolerated", func(t *testing.T) {
		t.Parallel()
		simulator := providersim.NewTurnstile(t, providerHostname, string(turnstile.ActionSignup))
		simulator.Route(providersim.TurnstileSiteverify,
			providersim.Reply(http.StatusOK, `{"success":true,"hostname":"arena.example","action":"signup","challenge_ts":"2026-06-01T12:00:00Z","unknown_future":{"nested":true},"cdata":"x"}`))
		verifier := turnstileVerifier(t, simulator)
		if err := verifier.Verify(context.Background(), turnstile.Verification{Token: "solved-token-3", Action: turnstile.ActionSignup}); err != nil {
			t.Fatalf("answer with unknown fields refused: %v", err)
		}
	})

	t.Run("missing and refused answers fail safe", func(t *testing.T) {
		t.Parallel()

		refused := providersim.NewTurnstile(t, providerHostname, string(turnstile.ActionSignup))
		refused.Route(providersim.TurnstileSiteverify, providersim.TurnstileRefused("invalid-input-response"))
		if err := turnstileVerifier(t, refused).Verify(context.Background(), turnstile.Verification{Token: "bad-token-1", Action: turnstile.ActionSignup}); err == nil {
			t.Fatal("refused token was accepted")
		} else if turnstile.IsUnavailable(err) {
			t.Errorf("a refused token is not an outage: %v", err)
		}

		malformed := providersim.NewTurnstile(t, providerHostname, string(turnstile.ActionSignup))
		malformed.Route(providersim.TurnstileSiteverify, providersim.Reply(http.StatusOK, `{"success":true}`))
		if err := turnstileVerifier(t, malformed).Verify(context.Background(), turnstile.Verification{Token: "bad-token-2", Action: turnstile.ActionSignup}); err == nil {
			t.Fatal("answer without hostname was accepted")
		}

		broken := providersim.NewTurnstile(t, providerHostname, string(turnstile.ActionSignup))
		broken.Route(providersim.TurnstileSiteverify, providersim.Reply(http.StatusOK, `not json`))
		if err := turnstileVerifier(t, broken).Verify(context.Background(), turnstile.Verification{Token: "bad-token-3", Action: turnstile.ActionSignup}); !turnstile.IsUnavailable(err) {
			t.Fatalf("unparsable answer error = %v, want an outage", err)
		}
	})

	t.Run("replay spends the token once without a second call", func(t *testing.T) {
		t.Parallel()
		simulator := providersim.NewTurnstile(t, providerHostname, string(turnstile.ActionSignup))
		verifier := turnstileVerifier(t, simulator)

		if err := verifier.Verify(context.Background(), turnstile.Verification{Token: "single-use-token", Action: turnstile.ActionSignup}); err != nil {
			t.Fatalf("first use refused: %v", err)
		}
		if err := verifier.Verify(context.Background(), turnstile.Verification{Token: "single-use-token", Action: turnstile.ActionSignup}); err == nil {
			t.Fatal("replayed token was accepted")
		}
		if calls := simulator.CallsTo("POST", providersim.TurnstileSiteverifyPath); len(calls) != 1 {
			t.Errorf("provider received %d requests, want 1: the replay is refused locally", len(calls))
		}
	})

	t.Run("slow provider is an outage and errors hide the token", func(t *testing.T) {
		t.Parallel()
		simulator := providersim.NewTurnstile(t, providerHostname, string(turnstile.ActionSignup))
		simulator.Route(providersim.TurnstileSiteverify,
			providersim.Slow(providersim.Reply(http.StatusOK, providersim.TurnstileSolvedBody(providerHostname, string(turnstile.ActionSignup))), simSlowDelay))
		verifier, err := turnstile.New(turnstile.Config{
			SecretKey: providersim.TurnstileSecretKey,
			Hostname:  providerHostname,
			Endpoint:  simulator.URL() + providersim.TurnstileSiteverifyPath,
			Timeout:   tightTimeout,
		}, "test")
		if err != nil {
			t.Fatalf("turnstile.New: %v", err)
		}
		const token = "solved-but-slow-token"
		err = verifier.Verify(context.Background(), turnstile.Verification{Token: token, Action: turnstile.ActionSignup})
		if !turnstile.IsUnavailable(err) {
			t.Fatalf("slow provider error = %v, want an outage", err)
		}
		for _, forbidden := range []string{token, providersim.TurnstileSecretKey} {
			if err != nil && strings.Contains(err.Error(), forbidden) {
				t.Errorf("error leaked %q: %s", forbidden, err)
			}
		}
	})
}

func testTelemetry(t *testing.T, sentry *providersim.Simulator, posthog *providersim.Simulator) *observability.Telemetry {
	t.Helper()
	telemetry, err := observability.New(observability.Config{
		Logger:            slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Clock:             testsource.NewClock(time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)),
		Environment:       "test",
		SentryDSN:         "https://simkey@o1.ingest.sentry.io/42",
		SentryBaseURL:     sentry.URL(),
		PostHogAPIKey:     providersim.PostHogAPIKey,
		PostHogHost:       posthog.URL(),
		SampleRatePercent: 100,
	})
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}
	t.Cleanup(telemetry.Close)
	return telemetry
}

func TestTelemetryProvidersAgainstSimulators(t *testing.T) {
	t.Parallel()

	t.Run("sentry delivers the bounded envelope", func(t *testing.T) {
		t.Parallel()
		sentry := providersim.NewSentry(t, providersim.CapturingBodies())
		posthog := providersim.NewPostHog(t)
		telemetry := testTelemetry(t, sentry, posthog)

		telemetry.Errors.Report(observability.ErrorReport{
			Message:   "job email_delivery failed",
			Kind:      "handler",
			Operation: "email_delivery",
			RequestID: "req-sim-1",
		})

		calls := waitForCalls(t, sentry, "POST", "/api/42/envelope/", 1)
		call := calls[0]
		if got := call.Header("X-Sentry-Auth"); !strings.Contains(got, "sentry_key=simkey") {
			t.Errorf("X-Sentry-Auth = %q, want the DSN key", got)
		}
		lines := strings.Split(strings.TrimRight(string(call.Body), "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("an envelope is three lines, got %d", len(lines))
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(lines[2]), &event); err != nil {
			t.Fatalf("the item must be JSON: %v", err)
		}
		for key := range event {
			switch key {
			case "event_id", "timestamp", "platform", "level", "logger", "message", "tags", "environment":
			default:
				t.Fatalf("the event carries an unexpected field %q: %v", key, event)
			}
		}
		if event["message"] != "job email_delivery failed" {
			t.Fatalf("message = %v", event["message"])
		}
		if strings.Contains(string(call.Body), "ana.silva@example.com") {
			t.Error("the envelope must not carry request-derived addresses")
		}
		if strings.Contains(call.Path, "simkey") || strings.Contains(sentry.URL(), "simkey") {
			t.Error("the endpoint must not carry the credential")
		}
	})

	t.Run("posthog delivers only admitted properties", func(t *testing.T) {
		t.Parallel()
		sentry := providersim.NewSentry(t)
		posthog := providersim.NewPostHog(t, providersim.CapturingBodies())
		telemetry := testTelemetry(t, sentry, posthog)

		// A full batch flushes without waiting for the interval.
		for index := 0; index < providersim.PostHogBatchSize; index++ {
			telemetry.Events.Capture(observability.Event{
				Name:       observability.EventArenaInfluenceAssigned,
				AccountID:  "acc-sim-1",
				Properties: map[string]any{"locale": "pt-BR", "attributed_count": int64(2)},
				RequestID:  "req-sim-2",
			})
		}

		calls := waitForCalls(t, posthog, "POST", "/batch/", 1)
		var document struct {
			APIKey string `json:"api_key"`
			Batch  []struct {
				Event      string         `json:"event"`
				Properties map[string]any `json:"properties"`
			} `json:"batch"`
		}
		if err := json.Unmarshal(calls[0].Body, &document); err != nil {
			t.Fatalf("the batch must be JSON: %v", err)
		}
		if document.APIKey != providersim.PostHogAPIKey {
			t.Errorf("api_key = %q", document.APIKey)
		}
		if len(document.Batch) != providersim.PostHogBatchSize {
			t.Fatalf("batch size = %d, want %d", len(document.Batch), providersim.PostHogBatchSize)
		}
		for _, item := range document.Batch {
			if _, present := item.Properties["email"]; present {
				t.Fatal("an email must never be sent")
			}
			if item.Properties["distinct_id"] != "acc-sim-1" {
				t.Fatalf("properties = %v, want the account as distinct_id", item.Properties)
			}
		}
		if strings.Contains(string(calls[0].Body), "ana.silva@example.com") {
			t.Error("the batch must not carry request-derived addresses")
		}
	})

	t.Run("telemetry failures stay invisible to the caller", func(t *testing.T) {
		t.Parallel()
		sentry := providersim.NewSentry(t)
		posthog := providersim.NewPostHog(t)
		telemetry := testTelemetry(t, sentry, posthog)
		sentry.Down(true)
		posthog.Down(true)

		done := make(chan struct{})
		go func() {
			defer close(done)
			telemetry.Errors.Report(observability.ErrorReport{Message: "sim outage", Kind: "handler", Operation: "test", RequestID: "req-sim-3"})
			for index := 0; index < 10; index++ {
				telemetry.Events.Capture(observability.Event{
					Name:      observability.EventAccountSignedIn,
					AccountID: "acc-sim-2",
					RequestID: "req-sim-3",
				})
			}
		}()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Report/Capture blocked on providers that are down")
		}
	})

	t.Run("slow telemetry never blocks the request", func(t *testing.T) {
		t.Parallel()
		sentry := providersim.NewSentry(t)
		sentry.Route("POST /api/*", providersim.Slow(providersim.Reply(http.StatusOK, `{"id":"event_sim"}`), simSlowDelay))
		posthog := providersim.NewPostHog(t)
		telemetry := testTelemetry(t, sentry, posthog)

		started := time.Now()
		telemetry.Errors.Report(observability.ErrorReport{Message: "sim slow", Kind: "handler", Operation: "test", RequestID: "req-sim-4"})
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Errorf("Report blocked on a slow provider: %s", elapsed)
		}
	})
}
