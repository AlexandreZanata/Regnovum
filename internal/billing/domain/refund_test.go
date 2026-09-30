package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

func TestStripeRefundIdentifiers(t *testing.T) {
	t.Parallel()

	if _, err := domain.ParseStripeRefundID("re_abc123"); err != nil {
		t.Fatalf("ParseStripeRefundID valid: %v", err)
	}
	if _, err := domain.ParseStripeDisputeID("dp_abc123"); err != nil {
		t.Fatalf("ParseStripeDisputeID valid: %v", err)
	}
	if _, err := domain.ParseStripeChargeID("ch_abc123"); err != nil {
		t.Fatalf("ParseStripeChargeID valid: %v", err)
	}

	long := "re_" + strings.Repeat("a", 194)
	if _, err := domain.ParseStripeRefundID(long); err != nil {
		t.Fatalf("ParseStripeRefundID max body: %v", err)
	}
	tooLong := "re_" + strings.Repeat("a", 195)
	if _, err := domain.ParseStripeRefundID(tooLong); !errors.Is(err, domain.ErrInvalidStripeRefundID) {
		t.Fatalf("too long refund id error = %v, want ErrInvalidStripeRefundID", err)
	}

	for _, raw := range []string{"", "re_", "re_abc-def", "re_abc def", "ch_abc", "dp_", "re_ação"} {
		if _, err := domain.ParseStripeRefundID(raw); !errors.Is(err, domain.ErrInvalidStripeRefundID) {
			t.Errorf("ParseStripeRefundID(%q) error = %v, want ErrInvalidStripeRefundID", raw, err)
		}
	}
	if _, err := domain.ParseStripeDisputeID("re_abc"); !errors.Is(err, domain.ErrInvalidStripeDisputeID) {
		t.Errorf("dispute with refund prefix error = %v", err)
	}
	if _, err := domain.ParseStripeChargeID("re_abc"); !errors.Is(err, domain.ErrInvalidStripeChargeID) {
		t.Errorf("charge with refund prefix error = %v", err)
	}
}

func TestRefundSourceVocabulary(t *testing.T) {
	t.Parallel()

	refund, err := domain.ParseRefundSource("refund")
	if err != nil || !refund.IsValid() || refund.IsDispute() {
		t.Fatalf("refund source = %v, %v", refund, err)
	}
	dispute, err := domain.ParseRefundSource("dispute")
	if err != nil || !dispute.IsValid() || !dispute.IsDispute() {
		t.Fatalf("dispute source = %v, %v", dispute, err)
	}
	if _, err := domain.ParseRefundSource("chargeback"); !errors.Is(err, domain.ErrInvalidRefundSource) {
		t.Fatalf("chargeback error = %v, want ErrInvalidRefundSource", err)
	}
	if _, err := domain.ParseRefundSource(""); !errors.Is(err, domain.ErrInvalidRefundSource) {
		t.Fatalf("empty source error = %v", err)
	}
}

func TestRefundStatusVocabulary(t *testing.T) {
	t.Parallel()

	applied, err := domain.ParseRefundStatus("applied")
	if err != nil || applied.NeedsReview() {
		t.Fatalf("applied status = %v, %v", applied, err)
	}
	needs, err := domain.ParseRefundStatus("needs_review")
	if err != nil || !needs.NeedsReview() {
		t.Fatalf("needs_review status = %v, %v", needs, err)
	}
	if _, err := domain.ParseRefundStatus("pending"); !errors.Is(err, domain.ErrInvalidRefundStatus) {
		t.Fatalf("pending error = %v", err)
	}
}

func TestAssessINKFullRefundUnused(t *testing.T) {
	t.Parallel()

	debit, review, err := domain.AssessINK(10000, 990, 990, 10000, domain.RefundSourceRefund)
	if err != nil {
		t.Fatalf("AssessINK: %v", err)
	}
	if debit != 10000 || review {
		t.Errorf("full unused debit = %d review %v, want 10000 false", debit, review)
	}
}

func TestAssessINKPartialRefund(t *testing.T) {
	t.Parallel()

	debit, review, err := domain.AssessINK(10000, 990, 495, 10000, domain.RefundSourceRefund)
	if err != nil {
		t.Fatalf("AssessINK: %v", err)
	}
	if debit != 5000 || review {
		t.Errorf("partial debit = %d review %v, want 5000 false", debit, review)
	}
}

func TestAssessINKCapsAtAvailableWithoutNegative(t *testing.T) {
	t.Parallel()

	debit, review, err := domain.AssessINK(10000, 990, 990, 7000, domain.RefundSourceRefund)
	if err != nil {
		t.Fatalf("AssessINK: %v", err)
	}
	if debit != 7000 || !review {
		t.Errorf("capped debit = %d review %v, want 7000 true", debit, review)
	}

	empty, review, err := domain.AssessINK(10000, 990, 990, 0, domain.RefundSourceRefund)
	if err != nil {
		t.Fatalf("AssessINK empty: %v", err)
	}
	if empty != 0 || !review {
		t.Errorf("empty debit = %d review %v, want 0 true", empty, review)
	}
}

func TestAssessINKChargebackAlwaysNeedsReview(t *testing.T) {
	t.Parallel()

	debit, review, err := domain.AssessINK(10000, 990, 990, 10000, domain.RefundSourceDispute)
	if err != nil {
		t.Fatalf("AssessINK: %v", err)
	}
	if debit != 10000 || !review {
		t.Errorf("chargeback debit = %d review %v, want 10000 true", debit, review)
	}
}

func TestAssessINKRejectsIncoherentInputs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		granted   int64
		paid      int64
		refunded  int64
		available int64
	}{
		{name: "zero grant", granted: 0, paid: 990, refunded: 990, available: 10000},
		{name: "zero paid", granted: 10000, paid: 0, refunded: 100, available: 10000},
		{name: "zero refunded", granted: 10000, paid: 990, refunded: 0, available: 10000},
		{name: "refunded above paid", granted: 10000, paid: 990, refunded: 991, available: 10000},
		{name: "negative available", granted: 10000, paid: 990, refunded: 990, available: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := domain.AssessINK(tc.granted, tc.paid, tc.refunded, tc.available, domain.RefundSourceRefund); err == nil {
				t.Fatal("incoherent inputs must fail")
			}
		})
	}
}

func TestAssessPassesPolicy(t *testing.T) {
	t.Parallel()

	revoke, review, err := domain.AssessPasses(1, 1, false, domain.RefundSourceRefund)
	if err != nil || revoke != 1 || review {
		t.Fatalf("unused full revoke = %d %v %v, want 1 false", revoke, review, err)
	}

	revoke, review, err = domain.AssessPasses(5, 3, false, domain.RefundSourceRefund)
	if err != nil || revoke != 3 || !review {
		t.Fatalf("consumed revoke = %d %v %v, want 3 true", revoke, review, err)
	}

	revoke, review, err = domain.AssessPasses(1, 1, true, domain.RefundSourceRefund)
	if err != nil || revoke != 0 || !review {
		t.Fatalf("partial pass revoke = %d %v %v, want 0 true", revoke, review, err)
	}

	revoke, review, err = domain.AssessPasses(1, 1, false, domain.RefundSourceDispute)
	if err != nil || revoke != 1 || !review {
		t.Fatalf("dispute revoke = %d %v %v, want 1 true", revoke, review, err)
	}

	revoke, review, err = domain.AssessPasses(1, 0, false, domain.RefundSourceRefund)
	if err != nil || revoke != 0 || !review {
		t.Fatalf("fully consumed revoke = %d %v %v, want 0 true", revoke, review, err)
	}
}

func TestRefundEventClassifiers(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"charge.refunded", "refund.created", "refund.updated"} {
		eventType, err := domain.ParseWebhookEventType(raw)
		if err != nil {
			t.Fatalf("ParseWebhookEventType(%q): %v", raw, err)
		}
		if !eventType.IsRefundEvent() || eventType.IsDisputeEvent() {
			t.Errorf("%q must be a refund event", raw)
		}
	}
	for _, raw := range []string{"charge.dispute.created", "charge.dispute.updated", "charge.dispute.closed", "charge.dispute.funds_withdrawn", "charge.dispute.funds_reinstated"} {
		eventType, err := domain.ParseWebhookEventType(raw)
		if err != nil {
			t.Fatalf("ParseWebhookEventType(%q): %v", raw, err)
		}
		if !eventType.IsDisputeEvent() || eventType.IsRefundEvent() {
			t.Errorf("%q must be a dispute event", raw)
		}
	}

	other, _ := domain.ParseWebhookEventType("checkout.session.completed")
	if other.IsRefundEvent() || other.IsDisputeEvent() {
		t.Error("checkout event must not classify as refund/dispute")
	}
}

func TestAssessINKUnitBoundaries(t *testing.T) {
	t.Parallel()

	// One is a valid quantity on every side: the fences refuse only below
	// one, and a full refund of one reverses one (mutation gate:
	// refund.go:184,187,190, grant.go:54).
	debit, review, err := domain.AssessINK(1, 1, 1, 1, domain.RefundSourceRefund)
	if err != nil {
		t.Fatalf("AssessINK(1,1,1,1): %v", err)
	}
	if debit != 1 || review {
		t.Errorf("unit refund debit = %d review %v, want 1 false", debit, review)
	}
	if _, err := domain.NewINKGrant(1); err != nil {
		t.Fatalf("NewINKGrant(1): %v", err)
	}
	for _, args := range [][4]int64{{0, 1, 1, 1}, {1, 0, 1, 1}, {1, 1, 0, 1}} {
		if _, _, err := domain.AssessINK(args[0], args[1], args[2], args[3], domain.RefundSourceRefund); err == nil {
			t.Errorf("AssessINK%v = nil, want refusal below one", args)
		}
	}
}
