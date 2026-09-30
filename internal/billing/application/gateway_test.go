package application_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// fakeGateway is the contract fake of the payment port: it implements the
// interface completely using only billing vocabulary, which is itself the
// first proof that no provider type is needed to satisfy the port. The tests
// below also use it to assert the shape of every request the use cases build.
type fakeGateway struct {
	requests []string
	answers  []application.CheckoutSession

	// Recording of the full requests, used by the checkout use case tests.
	customerRequests []application.CreateCustomerRequest
	sessionRequests  []application.CreateCheckoutSessionRequest

	// Configured answers and failures. An explicit checkoutSession wins, then
	// the per-price answers (what a real provider would charge for each
	// price), then the queue above.
	createCustomer  application.Customer
	checkoutSession application.CheckoutSession
	sessionByPrice  map[string]application.CheckoutSession
	customerErr     error
	checkoutErr     error
}

var _ application.PaymentGateway = (*fakeGateway)(nil)

func (g *fakeGateway) CreateCustomer(_ context.Context, request application.CreateCustomerRequest) (application.Customer, error) {
	g.requests = append(g.requests, "create customer "+request.IdempotencyKey)
	g.customerRequests = append(g.customerRequests, request)
	if g.customerErr != nil {
		return application.Customer{}, g.customerErr
	}
	if g.createCustomer.ID.IsZero() {
		return application.Customer{ID: domain.StripeCustomerID("cus_fake"), Livemode: false}, nil
	}
	return g.createCustomer, nil
}

func (g *fakeGateway) CreateCheckoutSession(_ context.Context, request application.CreateCheckoutSessionRequest) (application.CheckoutSession, error) {
	g.requests = append(g.requests, "create session "+request.ClientReference)
	g.sessionRequests = append(g.sessionRequests, request)
	if g.checkoutErr != nil {
		return application.CheckoutSession{}, g.checkoutErr
	}
	if !g.checkoutSession.ID.IsZero() {
		return g.checkoutSession, nil
	}
	if answer, found := g.sessionByPrice[request.PriceID.String()]; found {
		return answer, nil
	}
	if len(g.answers) == 0 {
		return application.CheckoutSession{}, errors.New("fake gateway: no answer queued")
	}
	answer := g.answers[0]
	g.answers = g.answers[1:]
	return answer, nil
}

func (g *fakeGateway) GetCheckoutSession(_ context.Context, id domain.StripeCheckoutSessionID) (application.CheckoutSession, error) {
	g.requests = append(g.requests, "get session "+id.String())
	if !g.checkoutSession.ID.IsZero() {
		return g.checkoutSession, nil
	}
	if len(g.answers) == 0 {
		return application.CheckoutSession{}, nil
	}
	answer := g.answers[0]
	g.answers = g.answers[1:]
	return answer, nil
}

func (g *fakeGateway) GetSubscription(_ context.Context, id domain.StripeSubscriptionID) (application.Subscription, error) {
	g.requests = append(g.requests, "get subscription "+id.String())
	return application.Subscription{ID: id, Status: domain.SubscriptionActive}, nil
}

func (g *fakeGateway) CreatePortalSession(_ context.Context, request application.CreatePortalSessionRequest) (application.PortalSession, error) {
	g.requests = append(g.requests, "create portal "+request.CustomerID.String())
	return application.PortalSession{URL: "https://billing.example/portal/session"}, nil
}

// TestPaymentGatewayPortIsConsumedWithoutTheProvider exercises the whole port
// through the fake: provisioning a customer, starting a subscription checkout
// and reading both objects back. The flow is expressed with billing vocabulary
// only, so any provider type leaking into the port would break it.
func TestPaymentGatewayPortIsConsumedWithoutTheProvider(t *testing.T) {
	t.Parallel()

	var gateway application.PaymentGateway = &fakeGateway{
		answers: []application.CheckoutSession{{
			ID:            domain.StripeCheckoutSessionID("cs_test_fake"),
			Status:        domain.CheckoutStatusComplete,
			PaymentStatus: domain.CheckoutPaymentPaid,
			Mode:          domain.CheckoutModeSubscription,
			AmountMinor:   2490,
			Currency:      domain.CurrencyBRL,
			Livemode:      false,
			URL:           "https://checkout.example/session",
		}},
	}

	customer, err := gateway.CreateCustomer(context.Background(), application.CreateCustomerRequest{
		IdempotencyKey: "intent-1",
	})
	if err != nil {
		t.Fatalf("CreateCustomer error = %v", err)
	}
	if customer.ID.String() != "cus_fake" {
		t.Fatalf("customer = %q", customer.ID.String())
	}

	session, err := gateway.CreateCheckoutSession(context.Background(), application.CreateCheckoutSessionRequest{
		CustomerID:      customer.ID,
		PriceID:         domain.StripePriceID("price_br_member"),
		Mode:            domain.CheckoutModeSubscription,
		SuccessURL:      "https://arena.example/checkout/success",
		CancelURL:       "https://arena.example/checkout/cancel",
		ClientReference: "intent-1",
		IdempotencyKey:  "intent-1",
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession error = %v", err)
	}
	if !session.PaymentStatus.IsSettled() || session.Status != domain.CheckoutStatusComplete {
		t.Fatalf("session settlement = %v/%v", session.Status, session.PaymentStatus)
	}
	if session.AmountMinor != 2490 || session.Currency != domain.CurrencyBRL {
		t.Fatalf("session amount = %s", domain.Money{})
	}

	if _, err := gateway.GetCheckoutSession(context.Background(), session.ID); err != nil {
		t.Fatalf("GetCheckoutSession error = %v", err)
	}
	subscription, err := gateway.GetSubscription(context.Background(), domain.StripeSubscriptionID("sub_fake"))
	if err != nil {
		t.Fatalf("GetSubscription error = %v", err)
	}
	if subscription.Status != domain.SubscriptionActive || subscription.Status.IsTerminal() {
		t.Fatalf("subscription status = %q", subscription.Status)
	}
}

// TestPortTypesNeverMentionTheProvider walks the interface and every request
// and answer type through reflection and refuses any type that comes from a
// provider package. It is the mechanical form of "supplier types never cross
// the port": adding a Stripe type to a signature or to a field fails here.
func TestPortTypesNeverMentionTheProvider(t *testing.T) {
	t.Parallel()

	// Packages a port type may legitimately come from: builtins, the standard
	// library packages the port uses, and the billing module itself.
	allowedPackages := map[string]bool{
		"":        true,
		"context": true,
		"time":    true,
		"github.com/AlexandreZanata/Regnovum/internal/billing/application": true,
		"github.com/AlexandreZanata/Regnovum/internal/billing/domain":      true,
	}

	seen := map[reflect.Type]bool{}
	var check func(path string, value reflect.Type)
	check = func(path string, value reflect.Type) {
		if value == nil || seen[value] {
			return
		}
		seen[value] = true

		switch value.Kind() {
		case reflect.Func:
			for index := 0; index < value.NumIn(); index++ {
				check(fmt.Sprintf("%s: parameter %d", path, index), value.In(index))
			}
			for index := 0; index < value.NumOut(); index++ {
				check(fmt.Sprintf("%s: result %d", path, index), value.Out(index))
			}
			return
		case reflect.Pointer, reflect.Slice, reflect.Array:
			check(path, value.Elem())
			return
		case reflect.Map:
			check(path, value.Key())
			check(path, value.Elem())
			return
		case reflect.Struct:
			if value.Name() == "" || value.PkgPath() == "time" && value.Name() == "Time" {
				// time.Time and unnamed structs are standard library values.
				return
			}
			if !allowedPackages[value.PkgPath()] {
				t.Errorf("%s uses type %s from %q", path, value, value.PkgPath())
				return
			}
			for index := 0; index < value.NumField(); index++ {
				field := value.Field(index)
				check(fmt.Sprintf("%s.%s", path, field.Name), field.Type)
			}
			return
		case reflect.Interface:
			for index := 0; index < value.NumMethod(); index++ {
				check(path+"."+value.Method(index).Name, value.Method(index).Type)
			}
			return
		}

		if value.PkgPath() != "" && !allowedPackages[value.PkgPath()] {
			t.Errorf("%s uses type %s from %q", path, value, value.PkgPath())
		}
	}

	portType := reflect.TypeOf((*application.PaymentGateway)(nil)).Elem()
	for index := 0; index < portType.NumMethod(); index++ {
		method := portType.Method(index)
		check(portType.Name()+"."+method.Name, method.Type)
	}

	// Prove the walk really reached every request and answer type instead of
	// trusting a count: a signature that stopped mentioning one of them would
	// silently stop being checked.
	required := []reflect.Type{
		reflect.TypeOf((*application.CreateCustomerRequest)(nil)).Elem(),
		reflect.TypeOf((*application.Customer)(nil)).Elem(),
		reflect.TypeOf((*application.CreateCheckoutSessionRequest)(nil)).Elem(),
		reflect.TypeOf((*application.CheckoutSession)(nil)).Elem(),
		reflect.TypeOf((*application.Subscription)(nil)).Elem(),
		reflect.TypeOf((*application.CreatePortalSessionRequest)(nil)).Elem(),
		reflect.TypeOf((*application.PortalSession)(nil)).Elem(),
	}
	for _, want := range required {
		if !seen[want] {
			t.Errorf("reflection walk never reached %s: the type is no longer part of the port", want)
		}
	}
}

// TestGatewayErrorClassification covers the operational meaning of the port's
// error vocabulary: only failures whose outcome is unknown may be retried.
func TestGatewayErrorClassification(t *testing.T) {
	t.Parallel()

	retryable := map[error]bool{
		application.ErrPaymentGatewayUnavailable:    true,
		application.ErrPaymentGatewayTimeout:        true,
		application.ErrPaymentGatewayRejected:       false,
		application.ErrPaymentGatewayMisconfigured:  false,
		application.ErrPaymentGatewayRequestInvalid: false,
	}
	for err, want := range retryable {
		if got := application.IsRetryablePaymentGatewayError(err); got != want {
			t.Errorf("IsRetryablePaymentGatewayError(%v) = %v, want %v", err, got, want)
		}
		// Wrapping must not change the classification: adapters wrap the
		// sentinel with the provider's request id.
		wrapped := fmt.Errorf("create checkout session: %w: request_id %q", err, "req_1")
		if got := application.IsRetryablePaymentGatewayError(wrapped); got != want {
			t.Errorf("wrapped %v classified as %v, want %v", err, got, want)
		}
		if !errors.Is(wrapped, err) {
			t.Errorf("wrapped %v is not errors.Is-compatible", err)
		}
	}

	if application.IsRetryablePaymentGatewayError(nil) {
		t.Error("nil error must not be classified as retryable")
	}
	if application.IsRetryablePaymentGatewayError(errors.New("unrelated")) {
		t.Error("an unrelated error must not be classified as retryable")
	}
}

// TestGatewayErrorMessagesCarryNoDiagnosticPayloads keeps the vocabulary
// honest about its own promise: the sentinels themselves never embed provider
// detail, which only the adapter may attach (redacted).
func TestGatewayErrorMessagesCarryNoDiagnosticPayloads(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		application.ErrPaymentGatewayUnavailable,
		application.ErrPaymentGatewayTimeout,
		application.ErrPaymentGatewayRejected,
		application.ErrPaymentGatewayMisconfigured,
		application.ErrPaymentGatewayRequestInvalid,
	} {
		message := err.Error()
		for _, forbidden := range []string{"cus_", "cs_", "pi_", "sub_", "sk_", "@", "http"} {
			if strings.Contains(message, forbidden) {
				t.Errorf("error %q mentions %q", message, forbidden)
			}
		}
	}
}

// TestGatewayClocklessTypes pins the value types the port exchanges, so a
// change in the wire contract is a conscious edit here.
func TestGatewayClocklessTypes(t *testing.T) {
	t.Parallel()

	period, err := domain.NewBillingPeriod(
		time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("NewBillingPeriod error = %v", err)
	}
	subscription := application.Subscription{
		ID:                domain.StripeSubscriptionID("sub_1"),
		Status:            domain.SubscriptionPastDue,
		CustomerID:        domain.StripeCustomerID("cus_1"),
		PriceID:           domain.StripePriceID("price_1"),
		CurrentPeriod:     &period,
		CancelAtPeriodEnd: true,
	}
	if subscription.CurrentPeriod.End().Before(subscription.CurrentPeriod.Start()) {
		t.Fatal("the port must carry an interval that advances")
	}
	if subscription.Status.IsTerminal() {
		t.Fatal("past_due is recoverable: it must not be terminal")
	}
}
