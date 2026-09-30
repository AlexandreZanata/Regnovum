package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// TestWebhookEventIDValidation proves the identifier mirrors the CHECK
// constraint of migration 00020 and refuses every shape the schema rejects.
func TestWebhookEventIDValidation(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name  string
		input string
	}{
		{name: "short", input: "evt_abc"},
		{name: "maximum body", input: "evt_" + strings.Repeat("A", 194)},
		{name: "digits and mixed case", input: "evt_123AbCdEfGhIjKlMn"},
	}
	for _, testCase := range valid {
		t.Run("valid/"+testCase.name, func(t *testing.T) {
			t.Parallel()
			id, err := domain.ParseWebhookEventID(testCase.input)
			if err != nil {
				t.Fatalf("ParseWebhookEventID(%q) error = %v", testCase.input, err)
			}
			if id.String() != testCase.input {
				t.Fatalf("String() = %q, want %q", id.String(), testCase.input)
			}
			if id.IsZero() {
				t.Fatal("a valid identifier must not be zero")
			}
		})
	}

	invalid := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyWebhookEventID},
		{name: "no prefix", input: "abc123", want: domain.ErrInvalidWebhookEventID},
		{name: "wrong prefix", input: "pi_abc", want: domain.ErrInvalidWebhookEventID},
		{name: "underscore in body", input: "evt_a_b", want: domain.ErrInvalidWebhookEventID},
		{name: "hyphen in body", input: "evt_a-b", want: domain.ErrInvalidWebhookEventID},
		{name: "space in body", input: "evt_a b", want: domain.ErrInvalidWebhookEventID},
		{name: "body too long", input: "evt_" + strings.Repeat("A", 195), want: domain.ErrInvalidWebhookEventID},
	}
	for _, testCase := range invalid {
		t.Run("invalid/"+testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := domain.ParseWebhookEventID(testCase.input)
			if testCase.want != nil && !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
			if testCase.want == nil && err == nil {
				// The valid cases above are handled by the valid loop; this
				// branch handles any accidental additions.
				t.Fatal("expected error")
			}
		})
	}

	zero := domain.WebhookEventID{}
	if !zero.IsZero() {
		t.Fatal("zero value must report IsZero")
	}
	if zero.String() != "" {
		t.Fatalf("zero value String() = %q", zero.String())
	}
}

// TestWebhookEventTypeValidation proves the type mirrors the CHECK constraint
// of migration 00020 and refuses shapes the schema rejects.
func TestWebhookEventTypeValidation(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name  string
		input string
	}{
		{name: "simple", input: "checkout.session.completed"},
		{name: "single segment", input: "payment_intent"},
		{name: "nested", input: "customer.subscription.updated"},
		{name: "with digits", input: "invoice.payment_failed"},
		{name: "maximum length", input: "a." + strings.Repeat("b", 118)},
	}
	for _, testCase := range valid {
		t.Run("valid/"+testCase.name, func(t *testing.T) {
			t.Parallel()
			eventType, err := domain.ParseWebhookEventType(testCase.input)
			if err != nil {
				t.Fatalf("ParseWebhookEventType(%q) error = %v", testCase.input, err)
			}
			if eventType.String() != testCase.input {
				t.Fatalf("String() = %q, want %q", eventType.String(), testCase.input)
			}
			if eventType.IsZero() {
				t.Fatal("a valid type must not be zero")
			}
		})
	}

	invalid := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyWebhookEventType},
		{name: "uppercase start", input: "Checkout.Session.Completed", want: domain.ErrInvalidWebhookEventType},
		{name: "uppercase in segment", input: "checkout.Session.completed", want: domain.ErrInvalidWebhookEventType},
		{name: "hyphen", input: "checkout.session-completed", want: domain.ErrInvalidWebhookEventType},
		{name: "space", input: "checkout.session completed", want: domain.ErrInvalidWebhookEventType},
		{name: "leading digit", input: "1checkout.session.completed", want: domain.ErrInvalidWebhookEventType},
		{name: "too long", input: "a." + strings.Repeat("b", 119), want: domain.ErrInvalidWebhookEventType},
		{name: "empty segment", input: "checkout..completed", want: domain.ErrInvalidWebhookEventType},
		{name: "trailing dot", input: "checkout.session.", want: domain.ErrInvalidWebhookEventType},
	}
	for _, testCase := range invalid {
		t.Run("invalid/"+testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := domain.ParseWebhookEventType(testCase.input)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}

	zero := domain.WebhookEventType{}
	if !zero.IsZero() {
		t.Fatal("zero value must report IsZero")
	}
	if zero.String() != "" {
		t.Fatalf("zero value String() = %q", zero.String())
	}
}

// TestWebhookEventTypeClassifiers proves the classification predicates used
// by the use case to route events to the correct handler.
func TestWebhookEventTypeClassifiers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input               string
		wantCheckoutClosed  bool
		wantCheckoutExpired bool
		wantSubscription    bool
	}{
		{input: "checkout.session.completed", wantCheckoutClosed: true},
		{input: "checkout.session.expired", wantCheckoutExpired: true},
		{input: "customer.subscription.created", wantSubscription: true},
		{input: "customer.subscription.updated", wantSubscription: true},
		{input: "customer.subscription.deleted", wantSubscription: true},
		{input: "invoice.payment_failed", wantCheckoutClosed: false, wantCheckoutExpired: false, wantSubscription: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			t.Parallel()
			eventType, err := domain.ParseWebhookEventType(testCase.input)
			if err != nil {
				t.Fatalf("ParseWebhookEventType(%q) error = %v", testCase.input, err)
			}
			if got := eventType.IsCheckoutSessionCompleted(); got != testCase.wantCheckoutClosed {
				t.Errorf("IsCheckoutSessionCompleted() = %v, want %v", got, testCase.wantCheckoutClosed)
			}
			if got := eventType.IsCheckoutSessionExpired(); got != testCase.wantCheckoutExpired {
				t.Errorf("IsCheckoutSessionExpired() = %v, want %v", got, testCase.wantCheckoutExpired)
			}
			if got := eventType.IsSubscriptionEvent(); got != testCase.wantSubscription {
				t.Errorf("IsSubscriptionEvent() = %v, want %v", got, testCase.wantSubscription)
			}
		})
	}
}
