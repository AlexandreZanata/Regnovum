package probe

import "testing"

// TestTheFlagObeys asks one subject for both boolean values at once. The two
// outcomes cannot both be its value, so the condition is constant and the
// refusal it guards can never decide anything.
func TestTheFlagObeys(t *testing.T) {
	ok := flag()
	if ok == true || ok == false {
		t.Errorf("the flag is neither of the two values a bool holds: %v", ok)
	}
}
