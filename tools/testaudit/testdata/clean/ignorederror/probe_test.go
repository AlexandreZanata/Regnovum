package probe

import "testing"

// TestTheWorkIsProved fails the test when the error arrives, which is the shape
// the rule asks for.
func TestTheWorkIsProved(t *testing.T) {
	value, err := work()
	if err != nil {
		t.Fatalf("work() error = %v", err)
	}
	if value == 0 {
		t.Errorf("work() = 0, want a value")
	}
}

// TestTheCasesThatDoNotApply declares the case away with `continue`, which is
// the other legal answer: it says the row does not apply instead of hiding a
// failure.
func TestTheCasesThatDoNotApply(t *testing.T) {
	for _, row := range rows() {
		result, err := prepare(row)
		if err != nil {
			continue
		}
		if result == "" {
			t.Errorf("row %v prepared an empty result", row)
		}
	}
}

// helperThatPropagates returns the error to its caller, which is how a helper of
// a test file hands a failure on.
func helperThatPropagates(t *testing.T) error {
	if _, err := work(); err != nil {
		return err
	}
	return nil
}
