package probe

import "testing"

// TestTheStatusIsOneOfTheAcceptedValues asks one subject for two distinct
// values, which is what a refusal does: the condition can be false, so it
// decides something.
func TestTheStatusIsOneOfTheAcceptedValues(t *testing.T) {
	got := status()
	if got != 200 && got != 204 {
		t.Errorf("status = %d, want 200 or 204", got)
	}
}
