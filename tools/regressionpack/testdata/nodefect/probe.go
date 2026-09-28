package probe

import "testing"

// TestProbeClean is the fixture test the clean pack cites: it asserts by
// itself, never skips, and never repeats.
func TestProbeClean(t *testing.T) {
	t.Helper()
	if 1+1 != 2 {
		t.Fatal("arithmetic broke")
	}
}
