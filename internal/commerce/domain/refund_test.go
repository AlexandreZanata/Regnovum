package domain

import (
	"errors"
	"testing"
)

func TestSplitServiceRefundCoversDustAndThousand(t *testing.T) {
	cases := []struct {
		refund   int64
		tithe    int64
		provider int64
	}{
		{1, 0, 1},
		{9, 0, 9},
		{10, 1, 9},
		{11, 1, 10},
		{19, 1, 18},
		{20, 2, 18},
		{1000, 100, 900},
		{20000, 2000, 18000},
	}
	for _, tc := range cases {
		tithe, provider, err := SplitServiceRefund(tc.refund)
		if err != nil {
			t.Fatalf("SplitServiceRefund(%d): %v", tc.refund, err)
		}
		if tithe != tc.tithe || provider != tc.provider {
			t.Fatalf("SplitServiceRefund(%d) = (%d,%d), want (%d,%d)",
				tc.refund, tithe, provider, tc.tithe, tc.provider)
		}
		if tithe+provider != tc.refund {
			t.Fatalf("SplitServiceRefund(%d): outputs %d+%d != refunded %d",
				tc.refund, tithe, provider, tc.refund)
		}
	}
}

func TestSplitServiceRefundRefusesNonPositive(t *testing.T) {
	for _, amount := range []int64{0, -1, -100} {
		if _, _, err := SplitServiceRefund(amount); !errors.Is(err, ErrInvalidContract) {
			t.Fatalf("SplitServiceRefund(%d) = %v, want ErrInvalidContract", amount, err)
		}
	}
}

func TestValidateRefundAccumulationGuardsRemainder(t *testing.T) {
	if err := ValidateRefundAccumulation(20000, 0, 20000); err != nil {
		t.Fatalf("full refund: %v", err)
	}
	if err := ValidateRefundAccumulation(20000, 6000, 14000); err != nil {
		t.Fatalf("exact remainder: %v", err)
	}
	if err := ValidateRefundAccumulation(20000, 6000, 14001); !errors.Is(err, ErrRefundExceedsOriginal) {
		t.Fatalf("over total = %v, want ErrRefundExceedsOriginal", err)
	}
	if err := ValidateRefundAccumulation(20000, 20000, 1); !errors.Is(err, ErrRefundExceedsOriginal) {
		t.Fatalf("after full = %v, want ErrRefundExceedsOriginal", err)
	}
	for _, tc := range [][3]int64{{0, 0, 1}, {20000, -1, 1}, {20000, 20001, 1}, {20000, 0, 0}, {20000, 0, -5}} {
		if err := ValidateRefundAccumulation(tc[0], tc[1], tc[2]); !errors.Is(err, ErrInvalidContract) {
			t.Fatalf("ValidateRefundAccumulation(%v) = %v, want ErrInvalidContract", tc, err)
		}
	}
}
