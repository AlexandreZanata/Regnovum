package broken

import "testing"

// TestAlwaysBroken is the consistent-failure fixture: it fails every run,
// so the detector must report a violation, never a flake.
func TestAlwaysBroken(t *testing.T) {
	t.Fatal("broken fixture fails by design")
}
