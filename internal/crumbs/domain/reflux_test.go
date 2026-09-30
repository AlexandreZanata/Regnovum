package domain

import (
	"errors"
	"testing"
)

func TestClassifyKeepsOnlyRegularTreasuryLegs(t *testing.T) {
	regular := []struct {
		leg   RefluxLeg
		class RefluxClass
	}{
		{RefluxLeg{RefluxPublication, "credit", 2750}, RefluxServiceCharge},
		{RefluxLeg{RefluxTitheSettlement, "credit", 2000}, RefluxTithe},
		{RefluxLeg{RefluxMeteringRefund, "debit", 2750}, RefluxMeteringReversal},
		{RefluxLeg{RefluxServiceRefund, "debit", 600}, RefluxTitheReversal},
	}
	for _, tc := range regular {
		got := Classify(tc.leg)
		if got != tc.class {
			t.Fatalf("Classify(%+v) = %q, want %q", tc.leg, got, tc.class)
		}
		if !got.Regular() {
			t.Fatalf("class %q must enter the net", got)
		}
	}
	excluded := []RefluxLeg{
		{RefluxPublication, "debit", 2750},
		{RefluxTitheSettlement, "debit", 2000},
		{RefluxMeteringRefund, "credit", 2750},
		{RefluxServiceRefund, "credit", 600},
		{RefluxSale, "credit", 100000},
		{RefluxSale, "debit", 100000},
		{RefluxConfiscation, "credit", 1},
		{RefluxDeathSettlement, "credit", 1},
		{RefluxCorrection, "credit", 1},
		{RefluxTreasuryMove, "credit", 5000},
		{RefluxGift, "credit", 5000},
		{RefluxEscrow, "credit", 20000},
		{RefluxUnknown, "credit", 100000},
		{RefluxUnknown, "debit", 100000},
		{RefluxOrigin("airdrop"), "credit", 100000},
	}
	for _, leg := range excluded {
		if got := Classify(leg); got != RefluxExcluded || got.Regular() || got.Sign() != 0 {
			t.Fatalf("Classify(%+v) = %q, want excluded fail-closed", leg, got)
		}
	}
	if len(AllRefluxOrigins()) != 12 {
		t.Fatalf("origins = %d, want the closed twelve", len(AllRefluxOrigins()))
	}
}

func TestSumRegularNetsRefundsBelowZero(t *testing.T) {
	legs := []RefluxLeg{
		{RefluxPublication, "credit", 2750},
		{RefluxTitheSettlement, "credit", 2000},
		{RefluxMeteringRefund, "debit", 2750},
		{RefluxServiceRefund, "debit", 600},
		{RefluxSale, "credit", 100000},
		{RefluxUnknown, "credit", 100000},
	}
	got, err := SumRegular(legs)
	if err != nil {
		t.Fatalf("SumRegular: %v", err)
	}
	if want := int64(2750 + 2000 - 2750 - 600); got != want {
		t.Fatalf("net = %d, want %d", got, want)
	}
	// A week of refunds outrunning revenue nets negative: documented,
	// never hidden, never clamped here.
	negative, err := SumRegular([]RefluxLeg{{RefluxMeteringRefund, "debit", 5000}})
	if err != nil || negative != -5000 {
		t.Fatalf("refund-only week = %d/%v, want -5000", negative, err)
	}
	empty, err := SumRegular(nil)
	if err != nil || empty != 0 {
		t.Fatalf("empty week = %d/%v, want defined zero", empty, err)
	}
	if _, err := SumRegular([]RefluxLeg{{RefluxPublication, "credit", 0}}); !errors.Is(err, ErrInvalidReflux) {
		t.Fatalf("zero amount = %v, want ErrInvalidReflux", err)
	}
	if _, err := SumRegular([]RefluxLeg{{RefluxPublication, "credit", 9223372036854775807}, {RefluxPublication, "credit", 1}}); !errors.Is(err, ErrInvalidReflux) {
		t.Fatalf("overflow = %v, want ErrInvalidReflux instead of wrapping", err)
	}
}
