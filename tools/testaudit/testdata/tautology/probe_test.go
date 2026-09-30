package probe

import "testing"

// TestTheValueIsItself asserts a comparison of an expression with itself. The
// condition does not read the value, so the refusal it guards is decorative.
func TestTheValueIsItself(t *testing.T) {
	got := 3
	if got != got {
		t.Errorf("got = %d, want a value that is not equal to itself", got)
	}
}
