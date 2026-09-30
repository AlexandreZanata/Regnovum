package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// TestProviderIdentifiersMirrorSchemaShapes covers every identifier the
// gateway port exchanges. The valid and invalid cases are the shapes of the
// CHECK constraints of migration 00020: a value accepted here is a value the
// database accepts, so a provider answer can never be stored in a shape the
// schema refuses.
func TestProviderIdentifiersMirrorSchemaShapes(t *testing.T) {
	t.Parallel()

	customerValid := []string{"cus_A1b2C3", "cus_" + strings.Repeat("a", 194)}
	for _, raw := range customerValid {
		id, err := domain.ParseStripeCustomerID(raw)
		if err != nil {
			t.Fatalf("ParseStripeCustomerID(%q) error = %v", raw, err)
		}
		if id.String() != raw || id.IsZero() {
			t.Fatalf("ParseStripeCustomerID(%q) = %q", raw, id.String())
		}
	}

	invalid := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "missing prefix", raw: "A1b2C3"},
		{name: "wrong prefix", raw: "sub_a1b2"},
		{name: "empty body", raw: "cus_"},
		{name: "separator inside body", raw: "cus_a_b"},
		{name: "dash inside body", raw: "cus_a-b"},
		{name: "space inside body", raw: "cus_a b"},
		{name: "newline inside body", raw: "cus_a\nb"},
		{name: "non ascii", raw: "cus_ação"},
		{name: "too long", raw: "cus_" + strings.Repeat("a", 195)},
	}
	for _, tc := range invalid {
		t.Run("customer/"+tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := domain.ParseStripeCustomerID(tc.raw)
			if !errors.Is(err, domain.ErrInvalidStripeCustomerID) {
				t.Fatalf("ParseStripeCustomerID(%q) error = %v, want ErrInvalidStripeCustomerID", tc.raw, err)
			}
			if !id.IsZero() {
				t.Fatal("failed parse must yield the zero customer id")
			}
		})
	}

	subscription, err := domain.ParseStripeSubscriptionID("sub_123abc")
	if err != nil {
		t.Fatalf("ParseStripeSubscriptionID error = %v", err)
	}
	if subscription.String() != "sub_123abc" {
		t.Fatalf("subscription = %q", subscription.String())
	}
	if _, err := domain.ParseStripeSubscriptionID("sub_"); !errors.Is(err, domain.ErrInvalidStripeSubscriptionID) {
		t.Fatalf("empty subscription body: error = %v", err)
	}
	if _, err := domain.ParseStripeSubscriptionID("sub_" + strings.Repeat("a", 195)); !errors.Is(err, domain.ErrInvalidStripeSubscriptionID) {
		t.Fatalf("oversized subscription body: error = %v", err)
	}
	if _, err := domain.ParseStripeSubscriptionID("cus_123"); !errors.Is(err, domain.ErrInvalidStripeSubscriptionID) {
		t.Fatalf("foreign prefix: error = %v", err)
	}
}

// TestCheckoutSessionIdentifierPinsTheProviderMode proves the mode of a session
// identifier can never be confused: a live session is refused where a test one
// is expected and vice versa, with a distinct error from a malformed value.
func TestCheckoutSessionIdentifierPinsTheProviderMode(t *testing.T) {
	t.Parallel()

	testSession, err := domain.ParseStripeCheckoutSessionID("cs_test_a1b2", false)
	if err != nil {
		t.Fatalf("test-mode session error = %v", err)
	}
	if testSession.String() != "cs_test_a1b2" {
		t.Fatalf("session = %q", testSession.String())
	}
	if _, err := domain.ParseStripeCheckoutSessionID("cs_live_a1b2", true); err != nil {
		t.Fatalf("live-mode session error = %v", err)
	}

	// The opposite mode is a mix-up, not a malformed value.
	if _, err := domain.ParseStripeCheckoutSessionID("cs_live_a1b2", false); !errors.Is(err, domain.ErrStripeCheckoutSessionModeMismatch) {
		t.Fatalf("live session in test mode: error = %v, want mode mismatch", err)
	}
	if _, err := domain.ParseStripeCheckoutSessionID("cs_test_a1b2", true); !errors.Is(err, domain.ErrStripeCheckoutSessionModeMismatch) {
		t.Fatalf("test session in live mode: error = %v, want mode mismatch", err)
	}

	for _, raw := range []string{"", "cs_a1b2", "cs_test_", "cs_prod_a1b2", "cs_test_a-b"} {
		if _, err := domain.ParseStripeCheckoutSessionID(raw, false); !errors.Is(err, domain.ErrInvalidStripeCheckoutSessionID) {
			t.Fatalf("ParseStripeCheckoutSessionID(%q) error = %v, want ErrInvalidStripeCheckoutSessionID", raw, err)
		}
	}
}

// TestPaymentIntentIdentifierAllowsTheAbsentValue covers the nullable column:
// a subscription checkout never produces a payment intent, so the empty value
// is valid and anything else must carry the prefix.
func TestPaymentIntentIdentifierAllowsTheAbsentValue(t *testing.T) {
	t.Parallel()

	absent, err := domain.ParseStripePaymentIntentID("")
	if err != nil {
		t.Fatalf("empty payment intent error = %v", err)
	}
	if !absent.IsZero() {
		t.Fatal("empty payment intent must be the zero value")
	}

	present, err := domain.ParseStripePaymentIntentID("pi_9x8y")
	if err != nil {
		t.Fatalf("payment intent error = %v", err)
	}
	if present.String() != "pi_9x8y" || present.IsZero() {
		t.Fatalf("payment intent = %q", present.String())
	}

	for _, raw := range []string{"pi_", "pi_a b", "ch_123", "cus_123"} {
		if _, err := domain.ParseStripePaymentIntentID(raw); !errors.Is(err, domain.ErrInvalidStripePaymentIntentID) {
			t.Fatalf("ParseStripePaymentIntentID(%q) error = %v", raw, err)
		}
	}
}

// TestSubscriptionStatusVocabularyMatchesSchema pins the closed vocabulary and
// the two terminal statuses: an out-of-order event may never revive a
// subscription the provider already ended.
func TestSubscriptionStatusVocabularyMatchesSchema(t *testing.T) {
	t.Parallel()

	want := []string{
		"incomplete", "incomplete_expired", "trialing", "active",
		"past_due", "canceled", "unpaid", "paused",
	}
	statuses := domain.AllSubscriptionStatuses()
	if len(statuses) != len(want) {
		t.Fatalf("vocabulary size = %d, want %d", len(statuses), len(want))
	}
	for index, raw := range want {
		if statuses[index].String() != raw {
			t.Fatalf("status %d = %q, want %q", index, statuses[index].String(), raw)
		}
		parsed, err := domain.ParseSubscriptionStatus(raw)
		if err != nil {
			t.Fatalf("ParseSubscriptionStatus(%q) error = %v", raw, err)
		}
		if parsed != statuses[index] {
			t.Fatalf("ParseSubscriptionStatus(%q) = %q", raw, parsed)
		}
	}

	terminal := map[string]bool{"canceled": true, "incomplete_expired": true}
	for _, status := range statuses {
		if got := status.IsTerminal(); got != terminal[status.String()] {
			t.Errorf("IsTerminal(%q) = %v", status.String(), got)
		}
	}

	for _, raw := range []string{"", "cancelled", "ACTIVE", "deleted", "pending"} {
		if _, err := domain.ParseSubscriptionStatus(raw); !errors.Is(err, domain.ErrInvalidSubscriptionStatus) {
			t.Fatalf("ParseSubscriptionStatus(%q) error = %v", raw, err)
		}
	}
}

// TestCheckoutVocabularyAndSettlement covers the checkout statuses, the payment
// statuses that decide whether anything may be granted, and the checkout modes.
func TestCheckoutVocabularyAndSettlement(t *testing.T) {
	t.Parallel()

	if len(domain.AllCheckoutSessionStatuses()) != 3 || len(domain.AllCheckoutPaymentStatuses()) != 2+1 {
		t.Fatal("checkout vocabularies changed size without the schema following")
	}
	if len(domain.AllCheckoutModes()) != 2 {
		t.Fatal("checkout mode vocabulary changed size")
	}

	for _, raw := range []string{"open", "complete", "expired"} {
		if _, err := domain.ParseCheckoutSessionStatus(raw); err != nil {
			t.Fatalf("ParseCheckoutSessionStatus(%q) error = %v", raw, err)
		}
	}
	if _, err := domain.ParseCheckoutSessionStatus("paid"); !errors.Is(err, domain.ErrInvalidCheckoutSessionStatus) {
		t.Fatalf("local status accepted as a provider status: %v", err)
	}

	// Only a settled payment may ever produce an entitlement: a completed
	// session that was not paid grants nothing (THR-STRIPE-02).
	settled := map[string]bool{"paid": true, "unpaid": false, "no_payment_required": true}
	for raw, want := range settled {
		status, err := domain.ParseCheckoutPaymentStatus(raw)
		if err != nil {
			t.Fatalf("ParseCheckoutPaymentStatus(%q) error = %v", raw, err)
		}
		if status.IsSettled() != want {
			t.Errorf("IsSettled(%q) = %v, want %v", raw, status.IsSettled(), want)
		}
	}
	if _, err := domain.ParseCheckoutPaymentStatus("complete"); !errors.Is(err, domain.ErrInvalidCheckoutPaymentStatus) {
		t.Fatalf("session status accepted as a payment status: %v", err)
	}

	for _, raw := range []string{"payment", "subscription"} {
		if _, err := domain.ParseCheckoutMode(raw); err != nil {
			t.Fatalf("ParseCheckoutMode(%q) error = %v", raw, err)
		}
	}
	for _, raw := range []string{"", "setup", "PAYMENT", "one_off"} {
		if _, err := domain.ParseCheckoutMode(raw); !errors.Is(err, domain.ErrInvalidCheckoutMode) {
			t.Fatalf("ParseCheckoutMode(%q) error = %v", raw, err)
		}
	}
}

// TestBillingPeriodRequiresAnAdvancingInterval mirrors the period CHECK of
// migration 00020: both instants or neither, with the end strictly after the
// start, normalized to UTC so instants from the provider compare safely.
func TestBillingPeriodRequiresAnAdvancingInterval(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.FixedZone("BRT", -3*60*60))
	end := start.Add(30 * 24 * time.Hour)
	period, err := domain.NewBillingPeriod(start, end)
	if err != nil {
		t.Fatalf("NewBillingPeriod error = %v", err)
	}
	if !period.Start().Equal(start) || !period.End().Equal(end) {
		t.Fatalf("period = %s", period.String())
	}
	if period.Start().Location() != time.UTC || period.End().Location() != time.UTC {
		t.Fatal("period instants must be normalized to UTC")
	}
	if period.IsZero() {
		t.Fatal("a built period is never zero")
	}

	invalid := []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{name: "no start", start: time.Time{}, end: end},
		{name: "no end", start: start, end: time.Time{}},
		{name: "reversed", start: end, end: start},
		{name: "equal", start: start, end: start},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := domain.NewBillingPeriod(tc.start, tc.end); !errors.Is(err, domain.ErrInvalidBillingPeriod) {
				t.Fatalf("NewBillingPeriod error = %v, want ErrInvalidBillingPeriod", err)
			}
		})
	}

	if !(domain.BillingPeriod{}).IsZero() {
		t.Fatal("the zero period must report itself as unset")
	}
}
