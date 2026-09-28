package probe

import "testing"

// TestProbeClean is the only test this fixture declares; the pack cites
// TestProbeAbsent, which does not exist.
func TestProbeClean(t *testing.T) {
	t.Helper()
	if 1+1 != 2 {
		t.Fatal("arithmetic broke")
	}
}
