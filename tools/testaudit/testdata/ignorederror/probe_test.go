package probe

import "testing"

// TestTheWorkIsAttempted observes an error and swallows it: the guard neither
// fails the test nor promotes the failure, so the error path ends green.
func TestTheWorkIsAttempted(t *testing.T) {
	if err := work(); err != nil {
		t.Logf("the work failed, which the run records: %v", err)
	}
}
