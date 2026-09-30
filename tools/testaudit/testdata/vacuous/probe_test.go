package probe

import "testing"

// TestTheRuleIsProved is the shape a generated test has when nobody read the
// rule it was supposed to prove: it declares a value, uses it, calls nothing,
// asserts nothing and cannot fail.
func TestTheRuleIsProved(t *testing.T) {
	value := 2 + 2
	_ = value
}
