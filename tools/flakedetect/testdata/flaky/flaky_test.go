// Controlled flaky fixture for the flakedetect gate: it fails exactly
// when ARENA_TEST_SEED is odd and passes otherwise, so a detector run
// that varies seeds must report it as a flake. It lives under testdata
// so the normal `go test ./...` never executes it; only an explicit run
// of this address does.
//
// A real intermittent failure (timing, order, shared state) looks the
// same to the detector: pass and fail for the same test across runs.
package flaky

import (
	"os"
	"strconv"
	"testing"
)

func TestFlakyCoin(t *testing.T) {
	seed, err := strconv.ParseInt(os.Getenv("ARENA_TEST_SEED"), 10, 64)
	if err != nil {
		t.Fatalf("flake fixture needs ARENA_TEST_SEED set, got %q", os.Getenv("ARENA_TEST_SEED"))
	}
	if seed%2 != 0 {
		t.Fatalf("flake fixture fails on odd seed %d by design", seed)
	}
}
