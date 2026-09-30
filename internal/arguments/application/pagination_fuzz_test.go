package application

import "testing"

// Seed rationale (P24-T02): pagination bounds are contract (default 20,
// maximum 100), so the corpus is the boundary itself — zero, negatives, the
// edges on both sides and the int64 extremes. A clamp that leaks a value
// outside [1, 100], that is not idempotent or that is nondeterministic
// persists corpus by the native Go fuzz contract.

// FuzzClampArgumentPageLimit proves page limits never escape the contract
// bounds on arbitrary input and that clamping is idempotent: clamping an
// already-clamped limit changes nothing.
func FuzzClampArgumentPageLimit(f *testing.F) {
	f.Add(0)
	f.Add(1)
	f.Add(19)
	f.Add(20)
	f.Add(21)
	f.Add(99)
	f.Add(100)
	f.Add(101)
	f.Add(-1)
	f.Add(-1000000)
	f.Add(1000000)
	f.Add(1 << 62)
	f.Add(-(1 << 62))

	f.Fuzz(func(t *testing.T, limit int) {
		first := clampArgumentPageLimit(limit)
		if first < 1 || first > MaxArgumentPageLimit {
			t.Fatalf("clamp(%d) = %d, outside [1, %d]", limit, first, MaxArgumentPageLimit)
		}
		if limit <= 0 && first != DefaultArgumentPageLimit {
			t.Fatalf("clamp(%d) = %d, want the default %d", limit, first, DefaultArgumentPageLimit)
		}
		if limit > MaxArgumentPageLimit && first != MaxArgumentPageLimit {
			t.Fatalf("clamp(%d) = %d, want the maximum %d", limit, first, MaxArgumentPageLimit)
		}
		if limit >= 1 && limit <= MaxArgumentPageLimit && first != limit {
			t.Fatalf("clamp(%d) = %d, want the limit itself", limit, first)
		}

		// Clamping is idempotent and deterministic.
		if second := clampArgumentPageLimit(first); second != first {
			t.Fatalf("clamp is not idempotent: clamp(%d) = %d", first, second)
		}
		if again := clampArgumentPageLimit(limit); again != first {
			t.Fatalf("clamp is not deterministic for %d", limit)
		}
	})
}
