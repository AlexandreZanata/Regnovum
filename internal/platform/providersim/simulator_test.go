// Fixtures of the provider simulators (P22-T05): every one of them drives a
// real adapter of the product — the Stripe gateway, the Resend mail sender, the
// Turnstile verifier and the composed telemetry — against a fake provider over
// HTTP, and every one of them points that adapter at a loopback address with a
// synthetic credential, so nothing here needs a key, an account or the
// internet.
package providersim_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	stripeadapter "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	notificationsdomain "github.com/AlexandreZanata/Regnovum/internal/notifications/domain"

	resendadapter "github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/resend"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
	"github.com/AlexandreZanata/Regnovum/internal/platform/providersim"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsupport"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

// The synthetic values of the fixtures. Every address is inside a reserved
// example domain and every credential is a literal of this file.
const (
	fixtureRecipient = "owner-sim@example.test"
	fixtureSite      = "arena.example.test"
	fixtureLocale    = "pt-BR"
	// fixtureAccount is the attribution every analytics event must carry. The
	// analytics front refuses an event with no account, and a fixture that
	// omitted it would be refused for a reason it never names.
	fixtureAccount = "acc-sim-1"
	// refusedEventName is a name the allowlist does not know, spelled the way a
	// product event is: the point of the control is that a plausible name is
	// still refused, counted and delivered nowhere.
	refusedEventName = "arena.not_allowlisted"
)

// instant is the fixed moment the fixtures run at. Nothing here reads the wall
// clock: the signature timestamps and the verifier's tolerance window are
// decided against this instant, which is what makes the replay and the stale
// event reproducible.
var instant = time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)

// TestThePaymentAdapterTalksToTheFakeProvider covers the success path of the
// payment surface: the five calls the gateway makes reach the fake provider
// authenticated and carrying the caller's idempotency key, and the documents
// come back as billing vocabulary.
func TestThePaymentAdapterTalksToTheFakeProvider(t *testing.T) {
	t.Parallel()

	fake := providersim.NewStripe(t)
	gateway := paymentGateway(t, fake)

	customer, err := gateway.CreateCustomer(context.Background(), billingapp.CreateCustomerRequest{IdempotencyKey: "sim-customer-1"})
	if err != nil {
		t.Fatalf("CreateCustomer: %v", err)
	}
	if customer.ID.String() != providersim.StripeCustomerID {
		t.Fatalf("customer = %s, want %s", customer.ID, providersim.StripeCustomerID)
	}

	session, err := gateway.CreateCheckoutSession(context.Background(), billingapp.CreateCheckoutSessionRequest{
		CustomerID:      customer.ID,
		PriceID:         billingdomain.StripePriceID(providersim.StripePriceID),
		Mode:            billingdomain.CheckoutModeSubscription,
		SuccessURL:      "https://arena.example.test/billing/return",
		CancelURL:       "https://arena.example.test/billing/cancel",
		ClientReference: "intent-sim",
		IdempotencyKey:  "sim-checkout-1",
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if session.ID.String() != providersim.StripeCheckoutID || session.Status != billingdomain.CheckoutStatusComplete {
		t.Fatalf("session = %s/%s, want %s/complete", session.ID, session.Status, providersim.StripeCheckoutID)
	}

	fetched, err := gateway.GetCheckoutSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetCheckoutSession: %v", err)
	}
	if fetched.PaymentStatus != billingdomain.CheckoutPaymentPaid {
		t.Fatalf("payment status = %s, want paid", fetched.PaymentStatus)
	}

	subscription, err := gateway.GetSubscription(context.Background(), billingdomain.StripeSubscriptionID(providersim.StripeSubscriptionID))
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if subscription.CustomerID.String() != providersim.StripeCustomerID {
		t.Fatalf("subscription customer = %s", subscription.CustomerID)
	}

	portal, err := gateway.CreatePortalSession(context.Background(), billingapp.CreatePortalSessionRequest{
		CustomerID:     customer.ID,
		ReturnURL:      "https://arena.example.test/billing",
		IdempotencyKey: "sim-portal-1",
	})
	if err != nil {
		t.Fatalf("CreatePortalSession: %v", err)
	}
	if portal.URL != providersim.StripePortalURL {
		t.Fatalf("portal url = %q", portal.URL)
	}

	// The provider saw exactly the five calls the adapter promises, every one
	// of them authenticated and, where it creates something, keyed.
	authorized := fake.CallsTo("POST", "/v1/customers")
	if len(authorized) != 1 {
		t.Fatalf("the fake provider recorded %d customer creations, want 1", len(authorized))
	}
	if got := authorized[0].Header("Authorization"); !strings.HasPrefix(got, "Bearer ") {
		t.Fatalf("the customer creation arrived without a bearer credential: %q", got)
	}
	if got := authorized[0].Header("Idempotency-Key"); got != "sim-customer-1" {
		t.Fatalf("the customer creation carried idempotency key %q", got)
	}
	if unexpected := fake.Unexpected(); len(unexpected) != 0 {
		t.Fatalf("the adapter called something nobody scripted: %v", unexpected)
	}
}

// TestThePaymentAdapterClassifiesWhatTheFakeProviderAnswers is the other half
// of the payment surface: the statuses and documents a provider produces when
// it is not successful. Each one must reach the caller as an error, and in no
// case may the adapter retry its way around the scripted answer.
func TestThePaymentAdapterClassifiesWhatTheFakeProviderAnswers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		state func(simulator *providersim.Simulator)
	}{
		{
			name: "a provider failure",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.StripeCustomers, providersim.StripeError(http.StatusInternalServerError, "the provider is having a bad day"))
			},
		},
		{
			name: "a refusal the adapter must not retry",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.StripeCustomers, providersim.StripeError(http.StatusBadRequest, "the request was refused"))
			},
		},
		{
			name: "a response the parser refuses",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.StripeCustomers, providersim.Reply(http.StatusOK, `{"id": "cus_truncated`))
			},
		},
		{
			name: "a provider slower than the client",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.StripeCustomers, providersim.Slow(
					providersim.Reply(http.StatusOK, providersim.StripeCustomerBody(providersim.StripeCustomerID)), 300*time.Millisecond,
				))
			},
		},
		{
			name:  "a provider that is up and not serving",
			state: func(simulator *providersim.Simulator) { simulator.Down(true) },
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := providersim.NewStripe(t)
			testCase.state(fake)
			gateway := paymentGateway(t, fake)

			if _, err := gateway.CreateCustomer(context.Background(), billingapp.CreateCustomerRequest{IdempotencyKey: "sim-customer-fault"}); err == nil {
				t.Fatal("the adapter answered a successful customer for a provider that failed")
			}
		})
	}

	t.Run("a provider that is not there at all", func(t *testing.T) {
		fake := providersim.NewStripe(t)
		gateway := paymentGateway(t, fake)
		fake.Stop()

		if _, err := gateway.CreateCustomer(context.Background(), billingapp.CreateCustomerRequest{IdempotencyKey: "sim-customer-gone"}); err == nil {
			t.Fatal("the adapter answered a successful customer for an address nobody answers")
		}
	})
}

// paymentGateway builds the real payment adapter pointed at the fake provider
// with a synthetic key and a bounded timeout.
func paymentGateway(t *testing.T, fake *providersim.Simulator) *stripeadapter.Gateway {
	t.Helper()
	gateway, err := stripeadapter.NewGateway(stripeadapter.Config{
		SecretKey: providersim.StripeSecretKey,
		Timeout:   100 * time.Millisecond,
		BaseURL:   fake.URL(),
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return gateway
}

// TestTheFakeProviderSignsWebhooksTheProductVerifies covers the direction the
// payment adapter does not reach: the provider speaks and the product listens.
// The simulated provider signs the events it delivers, and the product's own
// verifier is what judges them — so a signature that stopped matching, a
// duplicated delivery and a reordered one are all states of this fixture.
func TestTheFakeProviderSignsWebhooksTheProductVerifies(t *testing.T) {
	t.Parallel()

	verifier, err := stripeadapter.NewWebhookVerifier(
		providersim.StripeWebhookSecret, 5*time.Minute, testsource.NewClock(instant),
	)
	if err != nil {
		t.Fatalf("NewWebhookVerifier: %v", err)
	}

	// The listener is the product's endpoint: it runs the real verifier and
	// answers what it decided, which is what makes the delivery observable.
	delivered := make(chan error, 8)
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if _, err := io.ReadFull(r.Body, body); err != nil {
			delivered <- err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		decided := verifier.Verify(body, r.Header.Get("Stripe-Signature"), r.Header.Get("Stripe-Timestamp"))
		delivered <- decided
		if decided != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(listener.Close)

	payment := providersim.StripeEventBody("evt_sim_1", "checkout.session.completed", instant,
		map[string]any{"id": providersim.StripeCheckoutID})
	delivery := providersim.StripeDelivery{Payload: payment, At: instant}

	statuses, err := providersim.DeliverStripeWebhooks(listener.Client(), listener.URL, providersim.StripeWebhookSecret,
		providersim.Duplicate(delivery))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(statuses) != 2 || statuses[0] != http.StatusOK || statuses[1] != http.StatusOK {
		t.Fatalf("the duplicated delivery answered %v, want two accepted events", statuses)
	}
	for index := 0; index < 2; index++ {
		if err := <-delivered; err != nil {
			t.Fatalf("the duplicate %d was refused by the verifier: %v", index, err)
		}
	}

	// Reordering: the later event arrives first, and both are still within the
	// window, so order alone decides nothing.
	second := providersim.StripeEventBody("evt_sim_2", "invoice.paid", instant.Add(time.Minute), map[string]any{})
	statuses, err = providersim.DeliverStripeWebhooks(listener.Client(), listener.URL, providersim.StripeWebhookSecret,
		providersim.Reversed([]providersim.StripeDelivery{{Payload: payment, At: instant}, {Payload: second, At: instant.Add(time.Minute)}}))
	if err != nil {
		t.Fatalf("deliver reversed: %v", err)
	}
	if len(statuses) != 2 || statuses[0] != http.StatusOK || statuses[1] != http.StatusOK {
		t.Fatalf("the reordered delivery answered %v, want two accepted events", statuses)
	}
	for index := 0; index < 2; index++ {
		if err := <-delivered; err != nil {
			t.Fatalf("the reordered event %d was refused by the verifier: %v", index, err)
		}
	}

	// And the states the verifier must refuse: a stale event (what a replay
	// beyond the tolerance window looks like) and a tampered payload.
	stale := providersim.StripeDelivery{Payload: payment, At: instant.Add(-time.Hour)}
	if statuses, err = providersim.DeliverStripeWebhooks(listener.Client(), listener.URL, providersim.StripeWebhookSecret, []providersim.StripeDelivery{stale}); err != nil {
		t.Fatalf("deliver stale: %v", err)
	}
	if len(statuses) != 1 || statuses[0] != http.StatusBadRequest {
		t.Fatalf("the stale event answered %v, want a refusal", statuses)
	}
	if err := <-delivered; err == nil {
		t.Fatal("the verifier accepted an event older than its window")
	}

	// Tampering is not a scenario the provider produces — it is what somebody
	// else does to the wire — so the fixture signs the document and delivers a
	// different one under that signature.
	tampered := strings.Replace(payment, providersim.StripeCheckoutID, "cs_test_stolen", 1)
	signature, timestamp := providersim.SignStripeWebhook(providersim.StripeWebhookSecret, instant, payment)
	request, err := http.NewRequest(http.MethodPost, listener.URL, strings.NewReader(tampered))
	if err != nil {
		t.Fatalf("build the tampered delivery: %v", err)
	}
	request.Header.Set("Stripe-Signature", signature)
	request.Header.Set("Stripe-Timestamp", timestamp)
	response, err := listener.Client().Do(request)
	if err != nil {
		t.Fatalf("deliver tampered: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("the tampered event answered %d, want a refusal", response.StatusCode)
	}
	if err := <-delivered; err == nil {
		t.Fatal("the verifier accepted a payload the signature does not cover")
	}
}

// TestTheMailAdapterTalksToTheFakeProvider covers the mail surface: the real
// sender posts the rendered message to the fake provider with its bearer
// credential, and every state the provider can answer with reaches the caller
// as a classified failure.
func TestTheMailAdapterTalksToTheFakeProvider(t *testing.T) {
	t.Parallel()

	t.Run("an accepted message", func(t *testing.T) {
		fake := providersim.NewResend(t, providersim.CapturingBodies())
		sender := mailSender(t, fake)

		receipt, err := sender.Send(context.Background(), mailMessage(t))
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if receipt.ProviderID == "" {
			t.Fatal("the accepted message came back without the provider identifier")
		}

		calls := fake.CallsTo("POST", "/emails")
		if len(calls) != 1 {
			t.Fatalf("the fake provider recorded %d sends, want 1", len(calls))
		}
		if got := calls[0].Header("Authorization"); got != "Bearer "+providersim.ResendAPIKey {
			t.Fatalf("the send arrived with authorization %q", got)
		}
		var payload struct {
			From    string   `json:"from"`
			To      []string `json:"to"`
			Subject string   `json:"subject"`
		}
		if err := calls[0].Decode(&payload); err != nil {
			t.Fatalf("the captured body is not the provider's document: %v", err)
		}
		if payload.From != providersim.ResendSenderAddress {
			t.Fatalf("the send came from %q", payload.From)
		}
		if len(payload.To) != 1 || payload.To[0] != fixtureRecipient {
			t.Fatalf("the send went to %v", payload.To)
		}
		if payload.Subject == "" {
			t.Fatal("the send carried no subject")
		}
	})

	faults := []struct {
		name  string
		state func(simulator *providersim.Simulator)
	}{
		{
			name: "a refused message",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.ResendEmails, providersim.ResendError(http.StatusUnprocessableEntity, "validation_error", "the recipient was refused"))
			},
		},
		{
			name: "a provider failure",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.ResendEmails, providersim.ResendError(http.StatusInternalServerError, "internal_error", "the provider failed"))
			},
		},
		{
			name: "a receipt without an identifier",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.ResendEmails, providersim.Reply(http.StatusOK, `{"id":""}`))
			},
		},
		{
			name: "a document the parser refuses",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.ResendEmails, providersim.Reply(http.StatusOK, "not a document"))
			},
		},
		{
			name: "a provider slower than the client",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.ResendEmails, providersim.Slow(providersim.Reply(http.StatusOK, providersim.ResendAcceptedBody("msg_late")), 300*time.Millisecond))
			},
		},
		{
			name:  "a provider that is up and not serving",
			state: func(simulator *providersim.Simulator) { simulator.Down(true) },
		},
	}

	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			fake := providersim.NewResend(t)
			fault.state(fake)
			sender := mailSender(t, fake)

			if _, err := sender.Send(context.Background(), mailMessage(t)); err == nil {
				t.Fatal("the sender reported a delivered message for a provider that refused it")
			}
		})
	}

	t.Run("a provider that is not there at all", func(t *testing.T) {
		fake := providersim.NewResend(t)
		sender := mailSender(t, fake)
		fake.Stop()

		if _, err := sender.Send(context.Background(), mailMessage(t)); err == nil {
			t.Fatal("the sender reported a delivered message for an address nobody answers")
		}
	})
}

// mailSender builds the real mail adapter pointed at the fake provider.
func mailSender(t *testing.T, fake *providersim.Simulator) *resendadapter.Sender {
	t.Helper()
	sender, err := resendadapter.NewSender(resendadapter.Config{
		APIToken: providersim.ResendAPIKey,
		From:     providersim.ResendSenderAddress,
		BaseURL:  fake.URL(),
		Timeout:  100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	return sender
}

// mailMessage renders the synthetic message the fixtures send.
func mailMessage(t *testing.T) notificationsdomain.Message {
	t.Helper()
	locale, err := notificationsdomain.ParseLocale(fixtureLocale)
	if err != nil {
		t.Fatalf("ParseLocale: %v", err)
	}
	body, err := notificationsdomain.NewBody(
		"Confirme o seu endereço",
		"Cenário sintético de teste, sem relação com pessoas reais.",
		"<p>Cenário sintético de teste, sem relação com pessoas reais.</p>",
	)
	if err != nil {
		t.Fatalf("NewBody: %v", err)
	}
	message, err := notificationsdomain.NewMessage(fixtureRecipient, locale, notificationsdomain.TemplateVerification, body, "sim-delivery-1")
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return message
}

// TestTheChallengeAdapterTalksToTheFakeProvider covers the challenge surface:
// the real verifier posts the visitor's token as a form and classifies what the
// provider answered — a solved challenge, a refusal with the provider's codes,
// a challenge solved for another site, and the outages.
func TestTheChallengeAdapterTalksToTheFakeProvider(t *testing.T) {
	t.Parallel()

	t.Run("a solved challenge", func(t *testing.T) {
		// The bodies are captured here and only here: this is the fixture with a
		// question about the form the adapter posts, and every other challenge
		// fixture asks about the classification alone.
		fake := providersim.NewTurnstile(t, fixtureSite, string(turnstile.ActionSignup), providersim.CapturingBodies())
		verifier := challengeVerifier(t, fake)

		if err := verifier.Verify(context.Background(), turnstile.Verification{Token: "token-sim", Action: turnstile.ActionSignup, Client: "127.0.0.1"}); err != nil {
			t.Fatalf("Verify: %v", err)
		}

		calls := fake.CallsTo("POST", providersim.TurnstileSiteverifyPath)
		if len(calls) != 1 {
			t.Fatalf("the fake provider recorded %d verifications, want 1", len(calls))
		}
		// The form the adapter posts is the provider's documented one: the
		// server side secret, the token and the advisory address.
		form := string(calls[0].Body)
		for _, field := range []string{"secret=", "response=token-sim", "remoteip=127.0.0.1"} {
			if !strings.Contains(form, field) {
				t.Fatalf("the verification form does not carry %q: %s", field, form)
			}
		}
		if unexpected := fake.Unexpected(); len(unexpected) != 0 {
			t.Fatalf("the verifier called something nobody scripted: %v", unexpected)
		}
	})

	faults := []struct {
		name  string
		state func(simulator *providersim.Simulator)
	}{
		{
			name: "a challenge that was not solved",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.TurnstileSiteverify, providersim.TurnstileRefused("invalid-input-response"))
			},
		},
		{
			name: "a deployment whose secret is wrong",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.TurnstileSiteverify, providersim.TurnstileRefused("invalid-input-secret"))
			},
		},
		{
			name: "a challenge solved for another site",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.TurnstileSiteverify, providersim.Reply(http.StatusOK, providersim.TurnstileSolvedBody("someone-else.example.test", string(turnstile.ActionSignup))))
			},
		},
		{
			name: "a challenge solved for another action",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.TurnstileSiteverify, providersim.Reply(http.StatusOK, providersim.TurnstileSolvedBody(fixtureSite, string(turnstile.ActionPasswordReset))))
			},
		},
		{
			name: "a provider failure",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.TurnstileSiteverify, providersim.TurnstileUnavailable(http.StatusInternalServerError))
			},
		},
		{
			name: "a document the parser refuses",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.TurnstileSiteverify, providersim.Reply(http.StatusOK, `{"success":`))
			},
		},
		{
			name: "a provider slower than the client",
			state: func(simulator *providersim.Simulator) {
				simulator.Route(providersim.TurnstileSiteverify, providersim.Slow(providersim.Reply(http.StatusOK, providersim.TurnstileSolvedBody(fixtureSite, string(turnstile.ActionSignup))), 300*time.Millisecond))
			},
		},
		{
			name:  "a provider that is up and not serving",
			state: func(simulator *providersim.Simulator) { simulator.Down(true) },
		},
	}

	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			fake := providersim.NewTurnstile(t, fixtureSite, string(turnstile.ActionSignup))
			fault.state(fake)
			verifier := challengeVerifier(t, fake)

			if err := verifier.Verify(context.Background(), turnstile.Verification{Token: "token-sim", Action: turnstile.ActionSignup}); err == nil {
				t.Fatal("the verifier accepted a challenge the fake provider did not solve for this site")
			}
		})
	}

	t.Run("a provider that is not there at all", func(t *testing.T) {
		fake := providersim.NewTurnstile(t, fixtureSite, string(turnstile.ActionSignup))
		verifier := challengeVerifier(t, fake)
		fake.Stop()

		if err := verifier.Verify(context.Background(), turnstile.Verification{Token: "token-sim", Action: turnstile.ActionSignup}); err == nil {
			t.Fatal("the verifier accepted a challenge from an address nobody answers")
		}
	})
}

// challengeVerifier builds the real challenge verifier pointed at the fake
// provider.
func challengeVerifier(t *testing.T, fake *providersim.Simulator) turnstile.Verifier {
	t.Helper()
	verifier, err := turnstile.New(turnstile.Config{
		SecretKey: providersim.TurnstileSecretKey,
		Hostname:  fixtureSite,
		Endpoint:  fake.URL() + providersim.TurnstileSiteverifyPath,
		// The client's own bound, lowered so that the fixture which crosses it
		// declares the crossing in the client instead of guessing how slow a
		// provider has to be against the five-second default. The delay of the
		// scenario stays where the delay belongs: in the fake provider's answer.
		Timeout: 100 * time.Millisecond,
	}, config.EnvTest)
	if err != nil {
		t.Fatalf("turnstile.New: %v", err)
	}
	return verifier
}

// TestTheTelemetryAdaptersTalkToTheFakeProviders covers the observability
// surface, which is the one client whose provider failures are never the
// caller's problem: the composed telemetry posts an error envelope to the fake
// tracker and a batch to the fake analytics service, and a provider that
// answers a failure costs a dropped report instead of a broken request.
func TestTheTelemetryAdaptersTalkToTheFakeProviders(t *testing.T) {
	t.Parallel()

	fakeTracker := providersim.NewSentry(t, providersim.CapturingBodies())
	fakeAnalytics := providersim.NewPostHog(t, providersim.CapturingBodies())
	telemetry := composedTelemetry(t, fakeTracker, fakeAnalytics)
	defer telemetry.Close()

	telemetry.Errors.Report(observability.ErrorReport{
		Message:   "the email delivery of " + fixtureRecipient + " failed",
		Kind:      "job",
		Operation: "email_delivery",
		RequestID: "req-sim",
	})
	// The analytics client is a batching client: it delivers when a batch is
	// full or when its interval expires. The fixture fills the batch, because a
	// fixture that captured a single event would be asserting about the
	// interval instead of about the delivery — and both the name and the
	// account are the product's own vocabulary, so the fixture cannot capture
	// an event the allowlist quietly refuses.
	captureAcceptedEvents(t, telemetry, providersim.PostHogBatchSize)

	// The refusal control: a name the allowlist does not know is refused at the
	// call site and delivered nowhere. Without it, "the batch carried the
	// accepted event" would prove only that something was sent.
	telemetry.Events.Capture(observability.Event{Name: refusedEventName, AccountID: fixtureAccount})
	captureAcceptedEvents(t, telemetry, providersim.PostHogBatchSize)

	// Why, not only whether: the front counts what it received, refused,
	// sampled out and handed on, so a delivery that never happens says which of
	// the four it was instead of timing out without a reason. The single
	// refusal is the control above, counted where the process counts it.
	accepted := 2 * providersim.PostHogBatchSize
	exposition := telemetry.Metrics.Render()
	for name, want := range map[string]string{
		"telemetry_events_received_total":    strconv.Itoa(accepted + 1),
		"telemetry_events_refused_total":     "1",
		"telemetry_events_sampled_out_total": "0",
		"telemetry_events_dropped_total":     "0",
		"telemetry_events_handed_total":      strconv.Itoa(accepted),
	} {
		if got := exposedCounter(t, exposition, name); got != want {
			t.Fatalf("%s is %s, want %s", name, got, want)
		}
	}

	waitForCallCount(t, fakeTracker, "POST", "/api/"+providersim.SentryProject+"/envelope/", 1)
	waitForCallCount(t, fakeAnalytics, "POST", providersim.PostHogBatchPath, 2)

	envelope := fakeTracker.CallsTo("POST", "/api/"+providersim.SentryProject+"/envelope/")[0]
	if got := envelope.Header("X-Sentry-Auth"); !strings.Contains(got, "sentry_version=7") {
		t.Fatalf("the envelope arrived with authentication %q", got)
	}
	delivered := fakeAnalytics.CallsTo("POST", providersim.PostHogBatchPath)
	if len(delivered) != 2 {
		t.Fatalf("the fake provider received %d batches, want the 2 the fixture filled", len(delivered))
	}
	if !strings.Contains(string(delivered[0].Body), providersim.PostHogAPIKey) {
		t.Fatal("the analytics batch arrived without the project key")
	}
	// Every batch is the events the client accepted and nobody else: each one
	// carries exactly the batch the fixture filled, every item is the allowlisted
	// event, and the refused name reached no document at all.
	for index, call := range delivered {
		var document struct {
			Batch []struct {
				Event string `json:"event"`
			} `json:"batch"`
		}
		if err := call.Decode(&document); err != nil {
			t.Fatalf("decode the analytics batch %d: %v", index, err)
		}
		if len(document.Batch) != providersim.PostHogBatchSize {
			t.Fatalf("the batch %d carries %d events, want the %d the fixture captured", index, len(document.Batch), providersim.PostHogBatchSize)
		}
		for _, item := range document.Batch {
			if item.Event != observability.EventArenaPositionConfirmed {
				t.Fatalf("the batch %d carries the event %q, which nobody captured", index, item.Event)
			}
		}
		if strings.Contains(string(call.Body), refusedEventName) {
			t.Fatalf("the batch %d delivered the event the allowlist refused", index)
		}
	}

	// A provider that fails must not turn into a failure of the caller: the
	// next report is still attempted, and the process stays alive.
	fakeTracker.Route(providersim.SentryEnvelopePath, providersim.Reply(http.StatusInternalServerError, `{"detail":"nope"}`))
	telemetry.Errors.Report(observability.ErrorReport{Message: "a second failure", Kind: "handler", Operation: "GET /d/{slug}"})
	waitForCallCount(t, fakeTracker, "POST", "/api/"+providersim.SentryProject+"/envelope/", 2)
}

// TestTheTelemetryDocumentsCarryNothingSensitive is the redaction fixture: the
// documents the adapters deliver are read as they were sent, and the detector
// of the test platform refuses any of them that carries a value outside the
// reserved domains or a credential. The second half is the control: a document
// that carries a real-looking address is refused, so "the envelope passed" is
// not the answer of a check that never bites.
func TestTheTelemetryDocumentsCarryNothingSensitive(t *testing.T) {
	t.Parallel()

	fakeTracker := providersim.NewSentry(t, providersim.CapturingBodies())
	telemetry := composedTelemetry(t, fakeTracker, nil)
	defer telemetry.Close()

	telemetry.Errors.Report(observability.ErrorReport{
		Message:   "the email delivery of " + fixtureRecipient + " failed",
		Kind:      "job",
		Operation: "email_delivery",
		RequestID: "req-sim",
	})
	waitForCallCount(t, fakeTracker, "POST", "/api/"+providersim.SentryProject+"/envelope/", 1)

	envelope := fakeTracker.CallsTo("POST", "/api/"+providersim.SentryProject+"/envelope/")[0]
	if findings := testsupport.SensitiveFindings(envelope.Body); len(findings) != 0 {
		t.Fatalf("the delivered envelope carries %d sensitive values: %v", len(findings), findings)
	}
	if !strings.Contains(string(envelope.Body), "email_delivery") {
		t.Fatal("the envelope does not carry the operation that failed: the check would pass on an empty document")
	}

	// The control: the same detector over a document that carries somebody's
	// real address and a credential must refuse both.
	unsanitized := []byte(`{"message":"delivery to ana.silva@corp.example-business.com failed","key":"sk_live_51H8xQ2eZvKYlo2C"}`)
	rules := map[string]bool{}
	for _, finding := range testsupport.SensitiveFindings(unsanitized) {
		rules[finding.Rule] = true
	}
	if !rules["real-email-domain"] || !rules["provider-secret"] {
		t.Fatalf("the detector answered %v for a document that carries an address and a credential", rules)
	}
}

// composedTelemetry builds the real telemetry pointed at the fake providers.
// The analytics service is optional because a fixture that asks about the error
// reporter does not need one.
func composedTelemetry(t *testing.T, tracker, analytics *providersim.Simulator) *observability.Telemetry {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	settings := observability.Config{
		Logger:      logger,
		Clock:       testsource.NewClock(instant),
		Environment: "test",
		// The DSN keeps the provider's documented https shape; the loopback
		// allowance is the base URL, which is what points the reporter at the
		// fake tracker instead of at the internet.
		SentryDSN:         "https://" + providersim.SentryKey + "@sentry.invalid/" + providersim.SentryProject,
		SentryBaseURL:     tracker.URL(),
		SampleRatePercent: 100,
	}
	if analytics != nil {
		settings.PostHogAPIKey = providersim.PostHogAPIKey
		settings.PostHogHost = analytics.URL()
	}
	telemetry, err := observability.New(settings)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}
	return telemetry
}

// exposedCounter answers the value of one counter in the process exposition.
// The telemetry fixture reads the counters because they are the only place the
// process records *why* an event did not travel: refused by the allowlist, out
// of sample, or dropped by a full queue. A counter the exposition does not
// carry is a failure, not an empty answer.
func exposedCounter(t *testing.T, exposition, name string) string {
	t.Helper()
	for _, line := range strings.Split(exposition, "\n") {
		if value, found := strings.CutPrefix(line, name+" "); found {
			return value
		}
	}
	t.Fatalf("the process exposition does not carry %s", name)
	return ""
}

// TestTheRecordedCallsCarryTheInjectedInstant holds the determinism rule of the
// package: no simulator reads the wall clock, so a fixture can assert about
// when a provider was called. The first half asserts the default — the recorded
// instant is not the moment of the call — and the control asserts that an
// injected clock is the one the record follows, because "it does not read the
// clock" is also true of a recorder that stamps nothing at all.
func TestTheRecordedCallsCarryTheInjectedInstant(t *testing.T) {
	t.Parallel()

	t.Run("the default instant is not the moment of the call", func(t *testing.T) {
		fake := providersim.NewStripe(t, providersim.WithoutStrictness())
		call(t, fake, "/v1/charges")

		recorded := fake.Calls()
		if len(recorded) != 1 {
			t.Fatalf("the fake provider recorded %d calls, want 1", len(recorded))
		}
		if recorded[0].At.IsZero() {
			t.Fatal("the recorded call carries no instant")
		}
		if !recorded[0].At.Before(time.Now().Add(-time.Minute)) {
			t.Fatalf("the recorded instant %s is the moment of the call: the record depends on when the suite ran", recorded[0].At)
		}
	})

	t.Run("an injected clock is the one the record follows", func(t *testing.T) {
		clock := testsource.NewClock(instant)
		fake := providersim.NewStripe(t, providersim.WithClock(clock), providersim.WithoutStrictness())

		call(t, fake, "/v1/charges")
		clock.Advance(time.Minute)
		call(t, fake, "/v1/customers")

		recorded := fake.Calls()
		if len(recorded) != 2 {
			t.Fatalf("the fake provider recorded %d calls, want 2", len(recorded))
		}
		if !recorded[0].At.Equal(instant) || !recorded[1].At.Equal(instant.Add(time.Minute)) {
			t.Fatalf("the record carries %s and %s, want %s and %s", recorded[0].At, recorded[1].At, instant, instant.Add(time.Minute))
		}
	})
}

// call makes one request to an address of the fake provider and closes the
// answer. It refuses a call that does not reach the provider, so a fixture that
// uses it cannot pass over a listener that never started.
func call(t *testing.T, fake *providersim.Simulator, path string) {
	t.Helper()
	response, err := http.Post(fake.URL()+path, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("the call to %s did not reach the fake provider: %v", path, err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close the answer of %s: %v", path, err)
	}
}

// captureAcceptedEvents captures the allowlisted event `count` times, which is
// what fills a batch: the analytics client delivers when the batch is full
// rather than when an interval expires, so the delivery is immediate and the
// fixture waits on the batch instead of on a clock. Both the name and the
// account are the product's own vocabulary, so an event the allowlist refuses
// is a red fixture and not an empty batch.
func captureAcceptedEvents(t *testing.T, telemetry *observability.Telemetry, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		telemetry.Events.Capture(observability.Event{
			Name:       observability.EventArenaPositionConfirmed,
			AccountID:  fixtureAccount,
			RequestID:  fmt.Sprintf("sim-event-%d-%d", count, index),
			Properties: map[string]any{"locale": fixtureLocale},
		})
	}
}

// waitForCallCount waits until the fake provider received `want` calls at one
// address, because both telemetry adapters deliver from their own goroutine:
// the report is queued and the batch leaves when it is full. A fixture that
// asserted after the first delivery would be asserting about the queue of the
// second one.
func waitForCallCount(t *testing.T, fake *providersim.Simulator, method, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.CallsTo(method, path)) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the fake provider %s received %d calls to %s %s, want %d", fake.Name(), len(fake.CallsTo(method, path)), method, path, want)
}

// recorder is a reporter that keeps what it was told, which is what lets a
// fixture observe the unexpected-call rule without failing itself.
type recorder struct {
	mu       chan struct{}
	messages []string
}

func newRecorder() *recorder {
	return &recorder{mu: make(chan struct{}, 1)}
}

func (r *recorder) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
	select {
	case r.mu <- struct{}{}:
	default:
	}
}

// TestAnUnexpectedCallIsRecordedAndRefused covers the rule that keeps a fixture
// honest: a call to an address nobody scripted is recorded, answered with the
// provider's own refusal document, and announced through the reporter — which
// with a testing.TB attached is the assertion that fails the test.
func TestAnUnexpectedCallIsRecordedAndRefused(t *testing.T) {
	t.Parallel()

	t.Run("the call is recorded and refused", func(t *testing.T) {
		fake := providersim.NewStripe(t, providersim.WithoutStrictness())

		response, err := http.Get(fake.URL() + "/v1/charges")
		if err != nil {
			t.Fatalf("the unexpected call did not reach the fake provider: %v", err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != providersim.UnexpectedStatus {
			t.Fatalf("an unscripted call answered %d, want %d", response.StatusCode, providersim.UnexpectedStatus)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read the refusal: %v", err)
		}
		if !strings.Contains(string(body), "not scripted") {
			t.Fatalf("the refusal does not name the call: %s", body)
		}
		unexpected := fake.Unexpected()
		if len(unexpected) != 1 || unexpected[0].Path != "/v1/charges" {
			t.Fatalf("the fake provider recorded %v as unexpected", unexpected)
		}
	})

	t.Run("the reporter is told", func(t *testing.T) {
		announcements := newRecorder()
		fake := providersim.NewStandalone("stripe", providersim.WithReporter(announcements))
		server := httptest.NewServer(fake)
		t.Cleanup(server.Close)

		response, err := http.Get(server.URL + "/v1/charges")
		if err != nil {
			t.Fatalf("the unexpected call did not reach the fake provider: %v", err)
		}
		_ = response.Body.Close()

		select {
		case <-announcements.mu:
		case <-time.After(2 * time.Second):
			t.Fatal("the unexpected call was not announced to the reporter")
		}
		if len(announcements.messages) == 0 || !strings.Contains(announcements.messages[0], "unexpected call") {
			t.Fatalf("the reporter was told %v", announcements.messages)
		}
	})
}
