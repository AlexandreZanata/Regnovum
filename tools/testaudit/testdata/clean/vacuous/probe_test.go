package probe

import "testing"

// TestThePortIsSatisfied hands the *testing.T to the harness of the tree, which
// is how this repository proves a port contract: the test is observable even
// though the assertion lives in the harness, and the gate measures that
// difference instead of refusing it.
func TestThePortIsSatisfied(t *testing.T) {
	contract.RunSenderContract(t, newHarness)
}

// TestTheValueIsChecked asserts on its own, which is the other legal shape.
func TestTheValueIsChecked(t *testing.T) {
	if got := 2 + 2; got != 4 {
		t.Errorf("got = %d, want 4", got)
	}
}
