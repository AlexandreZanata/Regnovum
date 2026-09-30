package probe

import "testing"

// TestProbeSkipped is the fixture a pack must refuse: a test that does not
// run is indistinguishable from a test that passed.
func TestProbeSkipped(t *testing.T) {
	t.Helper()
	t.Skip("fixture skip the skipped-test family refuses")
}
