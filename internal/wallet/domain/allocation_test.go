package domain_test

import (
	"errors"
	"math"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

func TestAllocateDebitPriority(t *testing.T) {
	tests := []struct {
		name       string
		amount     int64
		free       int64
		purchased  int64
		wantFree   int64
		wantBought int64
		wantLines  int
		wantErr    error
	}{
		{
			name: "free only", amount: 3000, free: 5000, purchased: 0,
			wantFree: 3000, wantBought: 0, wantLines: 1,
		},
		{
			name: "exactly the free balance", amount: 5000, free: 5000, purchased: 10000,
			wantFree: 5000, wantBought: 0, wantLines: 1,
		},
		{
			name: "split across buckets", amount: 12000, free: 5000, purchased: 10000,
			wantFree: 5000, wantBought: 7000, wantLines: 2,
		},
		{
			name: "purchased only", amount: 1000, free: 0, purchased: 10000,
			wantFree: 0, wantBought: 1000, wantLines: 1,
		},
		{
			name: "exactly the total balance", amount: 15000, free: 5000, purchased: 10000,
			wantFree: 5000, wantBought: 10000, wantLines: 2,
		},
		{
			name: "insufficient by one", amount: 15001, free: 5000, purchased: 10000,
			wantErr: domain.ErrInsufficientInk,
		},
		{
			name: "empty wallet", amount: 1, free: 0, purchased: 0,
			wantErr: domain.ErrInsufficientInk,
		},
		{
			name: "zero amount", amount: 0, free: 5000, purchased: 5000,
			wantErr: domain.ErrZeroAmount,
		},
		{
			name: "balance sum overflows while amount exceeds free", amount: math.MaxInt64, free: math.MaxInt64 - 1, purchased: math.MaxInt64,
			wantErr: domain.ErrInkOverflow,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			amount := mustInk(t, tc.amount)
			allocation, err := domain.AllocateDebit(amount, mustInk(t, tc.free), mustInk(t, tc.purchased))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("AllocateDebit() error = %v, want %v", err, tc.wantErr)
				}
				if !allocation.IsZero() {
					t.Fatal("failed allocation must be zero")
				}
				return
			}
			if err != nil {
				t.Fatalf("AllocateDebit() error = %v", err)
			}
			if allocation.FromFree().Int64() != tc.wantFree {
				t.Errorf("FromFree() = %d, want %d", allocation.FromFree().Int64(), tc.wantFree)
			}
			if allocation.FromPurchased().Int64() != tc.wantBought {
				t.Errorf("FromPurchased() = %d, want %d", allocation.FromPurchased().Int64(), tc.wantBought)
			}
			total, err := allocation.Total()
			if err != nil || !total.Equals(amount) {
				t.Errorf("Total() = %v, %v; want %d", total.String(), err, tc.amount)
			}
			if got := len(allocation.Lines()); got != tc.wantLines {
				t.Errorf("len(Lines()) = %d, want %d", got, tc.wantLines)
			}
		})
	}
}

func TestAllocationLinePriorityOrder(t *testing.T) {
	allocation, err := domain.AllocateDebit(mustInk(t, 12000), mustInk(t, 5000), mustInk(t, 10000))
	if err != nil {
		t.Fatalf("AllocateDebit() error = %v", err)
	}

	lines := allocation.Lines()
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if lines[0].Bucket != domain.BucketFree || lines[0].Amount.Int64() != 5000 {
		t.Errorf("line 0 = %v/%d, want FREE_INK/5000", lines[0].Bucket, lines[0].Amount.Int64())
	}
	if lines[1].Bucket != domain.BucketPurchased || lines[1].Amount.Int64() != 7000 {
		t.Errorf("line 1 = %v/%d, want PURCHASED_INK/7000", lines[1].Bucket, lines[1].Amount.Int64())
	}

	// Empty buckets never produce ledger lines.
	purchasedOnly, err := domain.AllocateDebit(mustInk(t, 1000), mustInk(t, 0), mustInk(t, 10000))
	if err != nil {
		t.Fatalf("AllocateDebit() error = %v", err)
	}
	lines = purchasedOnly.Lines()
	if len(lines) != 1 || lines[0].Bucket != domain.BucketPurchased {
		t.Fatalf("lines = %+v, want a single PURCHASED_INK line", lines)
	}

	freeOnly, err := domain.AllocateDebit(mustInk(t, 1000), mustInk(t, 5000), mustInk(t, 5000))
	if err != nil {
		t.Fatalf("AllocateDebit() error = %v", err)
	}
	lines = freeOnly.Lines()
	if len(lines) != 1 || lines[0].Bucket != domain.BucketFree {
		t.Fatalf("lines = %+v, want a single FREE_INK line", lines)
	}
}

func TestAllocationValueSemantics(t *testing.T) {
	var zero domain.Allocation
	if !zero.IsZero() {
		t.Error("zero Allocation must report zero")
	}
	if len(zero.Lines()) != 0 {
		t.Error("zero Allocation must produce no lines")
	}
	if total, err := zero.Total(); err != nil || !total.IsZero() {
		t.Errorf("zero Allocation Total() = %v, %v", total.String(), err)
	}

	left := domain.NewAllocation(mustInk(t, 100), mustInk(t, 200))
	same := domain.NewAllocation(mustInk(t, 100), mustInk(t, 200))
	other := domain.NewAllocation(mustInk(t, 100), mustInk(t, 201))

	if !left.Equals(same) {
		t.Error("identical allocations must be equal")
	}
	if left.Equals(other) || left.Equals(zero) {
		t.Error("distinct allocations must not be equal")
	}

	total, err := left.Total()
	if err != nil || total.Int64() != 300 {
		t.Errorf("Total() = %d, %v; want 300", total.Int64(), err)
	}
}
