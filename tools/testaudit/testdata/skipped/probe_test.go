package probe

import "testing"

// TestTheJourneyRuns is the anti-pattern the program prohibits by name: the test
// that decides for itself that it does not run. A suite that skipped is a suite
// nobody can tell apart from one that passed, so this file is refused.
func TestTheJourneyRuns(t *testing.T) {
	if true {
		t.Skip("the journey needs an environment this run does not have")
	}
	t.Errorf("unreachable")
}
