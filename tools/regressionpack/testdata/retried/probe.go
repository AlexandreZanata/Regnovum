package probe

import "testing"

// Retry repeats until it passes: the fixture the retried-test family
// refuses. Business idempotency is not this; this is the test retrying
// itself.
func Retry() {}

// TestProbeRetried cites test-retry machinery instead of asserting once.
func TestProbeRetried(t *testing.T) {
	t.Helper()
	Retry()
}
