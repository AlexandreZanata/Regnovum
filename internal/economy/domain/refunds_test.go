package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func TestParseRefundSource(t *testing.T) {
	t.Parallel()

	if source, err := domain.ParseRefundSource("refund"); err != nil || source != domain.RefundSourceRefund || source.IsDispute() {
		t.Fatalf("refund = %q, %v", source, err)
	}
	if source, err := domain.ParseRefundSource("dispute"); err != nil || !source.IsDispute() {
		t.Fatalf("dispute = %q, %v", source, err)
	}
	if _, err := domain.ParseRefundSource("chargeback"); !errors.Is(err, domain.ErrInvalidRefund) {
		t.Fatalf("chargeback = %v, want ErrInvalidRefund", err)
	}
}

func TestParseRefundProviderID(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"re_abc123", "dp_X9"} {
		if got, err := domain.ParseRefundProviderID(raw); err != nil || got != raw {
			t.Errorf("ParseRefundProviderID(%q) = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "re_", "ch_abc", "re_a b", "re_é", "dp_"} {
		if _, err := domain.ParseRefundProviderID(raw); !errors.Is(err, domain.ErrInvalidRefund) {
			t.Errorf("ParseRefundProviderID(%q) = %v, want ErrInvalidRefund", raw, err)
		}
	}
}

func TestAssessRefusalRefundFullAndPartial(t *testing.T) {
	t.Parallel()

	revoke, review, err := domain.AssessRefusalRefund(100, 100, 990, 990, domain.RefundSourceRefund)
	if err != nil || revoke != 100 || review {
		t.Fatalf("full unused = %d, %v, %v; want 100, false, nil", revoke, review, err)
	}
	revoke, review, err = domain.AssessRefusalRefund(100, 100, 1000, 500, domain.RefundSourceRefund)
	if err != nil || revoke != 50 || review {
		t.Fatalf("half = %d, %v, %v; want 50, false, nil", revoke, review, err)
	}
	revoke, review, err = domain.AssessRefusalRefund(7, 7, 1000, 333, domain.RefundSourceRefund)
	if err != nil || revoke != 2 || review {
		t.Fatalf("floored = %d, %v, %v; want 2, false, nil", revoke, review, err)
	}
}

func TestAssessRefusalRefundConsumedNeverDebts(t *testing.T) {
	t.Parallel()

	revoke, review, err := domain.AssessRefusalRefund(100, 30, 990, 990, domain.RefundSourceRefund)
	if err != nil || revoke != 30 || !review {
		t.Fatalf("consumed full = %d, %v, %v; want 30, true, nil", revoke, review, err)
	}
	revoke, review, err = domain.AssessRefusalRefund(100, 0, 990, 990, domain.RefundSourceRefund)
	if err != nil || revoke != 0 || !review {
		t.Fatalf("empty = %d, %v, %v; want 0, true, nil", revoke, review, err)
	}
	revoke, review, err = domain.AssessRefusalRefund(0, 0, 990, 990, domain.RefundSourceRefund)
	if err != nil || revoke != 0 || !review {
		t.Fatalf("franchise-only = %d, %v, %v; want 0, true, nil", revoke, review, err)
	}
}

func TestAssessRefusalRefundDisputeAlwaysReviews(t *testing.T) {
	t.Parallel()

	revoke, review, err := domain.AssessRefusalRefund(100, 100, 990, 990, domain.RefundSourceDispute)
	if err != nil || revoke != 100 || !review {
		t.Fatalf("dispute full = %d, %v, %v; want 100, true, nil", revoke, review, err)
	}
}

func TestAssessRefusalRefundRefusesIncoherent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                               string
		granted, remaining, paid, refunded int64
		source                             domain.RefundSource
	}{
		{"negative granted", -1, 0, 100, 100, domain.RefundSourceRefund},
		{"remaining above granted", 10, 11, 100, 100, domain.RefundSourceRefund},
		{"zero paid", 10, 10, 0, 0, domain.RefundSourceRefund},
		{"refunded above paid", 10, 10, 100, 101, domain.RefundSourceRefund},
		{"zero refunded", 10, 10, 100, 0, domain.RefundSourceRefund},
		{"bad source", 10, 10, 100, 100, domain.RefundSource("chargeback")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, _, err := domain.AssessRefusalRefund(tc.granted, tc.remaining, tc.paid, tc.refunded, tc.source); !errors.Is(err, domain.ErrInvalidRefund) {
				t.Fatalf("Assess = %v, want ErrInvalidRefund", err)
			}
		})
	}
}

func TestRefusalResponseDueAddsFifteenDays(t *testing.T) {
	t.Parallel()

	decided := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	due := domain.RefusalResponseDue(decided)
	want := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if !due.Equal(want) {
		t.Fatalf("due = %v, want %v", due, want)
	}
	if domain.RefusalResponseDays != 15 {
		t.Fatalf("response window = %d, want 15", domain.RefusalResponseDays)
	}
}
