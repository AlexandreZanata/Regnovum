package probe

import "testing"

// TestProbeClean is the only test this fixture declares; the pack cites a
// rule the catalog never declared.
func TestProbeClean(t *testing.T) {
	t.Helper()
	if 1+1 != 2 {
		t.Fatal("arithmetic broke")
	}
}
