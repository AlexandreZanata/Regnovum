package flaky

import "testing"

// TestSteadyControl is the clean companion of the flaky fixture: it
// always passes, so a detector run over both addresses must report
// exactly one flake and one clean test.
func TestSteadyControl(t *testing.T) {
}
