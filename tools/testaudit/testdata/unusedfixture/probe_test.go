package probe

import "testing"

// TestSomethingElse checks something else entirely, next to a file kept under
// `testdata/` that no Go file names: a fixture nobody reads is a fixture that
// stopped checking something and nobody noticed.
func TestSomethingElse(t *testing.T) {
	if 2+2 != 4 {
		t.Errorf("arithmetic stopped working")
	}
}
