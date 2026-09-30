package probe

import (
	"math/rand"
	"testing"
)

// TestTheIdentifiersDiffer draws from the global generator. Its sequence is
// decided by the process, so a failure it produces cannot be replayed from the
// registered seed, and the registered source of this tree is `testsource`.
func TestTheIdentifiersDiffer(t *testing.T) {
	first := rand.Intn(1000)
	second := rand.Intn(1000)
	if first == second {
		t.Logf("the two draws collided, which happens")
	}
}
