package probe

import (
	"testing"
	"time"
)

// TestTheJobFinishes waits for the condition it asserts, with a deadline, and
// the poll inside the loop is measured by the gate instead of refused: it
// re-reads the condition, which the bare pause does not do.
func TestTheJobFinishes(t *testing.T) {
	deadline := time.Now().Add(time.Second)
	finished := false
	for time.Now().Before(deadline) {
		if done() {
			finished = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !finished {
		t.Fatalf("the job did not finish inside the deadline")
	}
}

// TestTheSlowProviderIsStaged hands a pause to the double the test gives to the
// product: the test never runs that body, so the pause is the timing of the
// subject under test and the gate counts it instead of refusing it. The count
// is printed on every run, which is what keeps the exception reviewable.
func TestTheSlowProviderIsStaged(t *testing.T) {
	handler := func() {
		time.Sleep(time.Second)
	}
	served := register(handler)
	if !served {
		t.Errorf("the slow provider was not used")
	}
}
