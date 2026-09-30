package probe

import "testing"

// TestTheValuesDiffer compares two distinct expressions, and the second test
// compares a value with a constant: neither condition is a comparison of an
// expression with itself.
func TestTheValuesDiffer(t *testing.T) {
	got, want := 3, 4
	if got == want {
		t.Errorf("got = %d, want a value different from %d", got, want)
	}
}

func TestTheValueIsNotTheConstant(t *testing.T) {
	got := 5
	if got != 5 {
		t.Errorf("got = %d, want 5", got)
	}
}
