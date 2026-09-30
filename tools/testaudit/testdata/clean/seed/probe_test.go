package probe

import (
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

// TestTheIdentifiersComeFromTheSeed takes its entropy from the registered
// source, whose seed is printed on every run and replayed with
// `ARENA_TEST_SEED`.
func TestTheIdentifiersComeFromTheSeed(t *testing.T) {
	source := testsource.NewSource(testsource.Seed())
	first := source.Stream("probe").Int64()
	second := source.Stream("probe").Int64()
	if first == second {
		t.Errorf("two draws of one stream answered %d twice", first)
	}
}
