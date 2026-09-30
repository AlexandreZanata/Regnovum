package probe

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTheContractHolds is the shape the tree is allowed to have: it asserts on
// its own, it reads the fixture it declares by name, and it waits for nothing.
func TestTheContractHolds(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "sample.json"))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	if len(raw) == 0 {
		t.Errorf("the fixture is empty")
	}
}
