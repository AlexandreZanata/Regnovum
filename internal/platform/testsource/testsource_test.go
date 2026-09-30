package testsource_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// draw takes a stream of bytes and a handful of identifiers from one seed, so
// that "the same seed replays the same run" is measured on the two things a
// scenario is built from.
func draw(t *testing.T, seed int64) ([]byte, []string) {
	t.Helper()

	clock := testsource.NewClock(time.Unix(1_700_000_000, 0))
	random := testsource.NewRandom(seed)
	identifiers := testsource.NewIDs("test", random, clock)

	buffer := make([]byte, 64)
	if _, err := random.Read(buffer); err != nil {
		t.Fatalf("the deterministic source failed to fill a buffer: %v", err)
	}

	sequence := make([]string, 0, 4)
	for index := 0; index < 4; index++ {
		sequence = append(sequence, identifiers.NewID())
		// The instant advances by a seeded amount: a scenario that wanted a
		// single second would not exercise the timestamp the format carries.
		clock.Advance(time.Duration(random.Int64n(2000)) * time.Millisecond)
	}
	return buffer, sequence
}

// TestTheSameSeedReplaysTheSameRun is the phase's own requirement, measured:
// with the seed registered, a failing scenario can be run again and do exactly
// what it did.
func TestTheSameSeedReplaysTheSameRun(t *testing.T) {
	seed := testsource.SeedFor(t)

	firstBytes, firstIDs := draw(t, seed)
	secondBytes, secondIDs := draw(t, seed)

	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("the same seed produced different bytes:\n%x\n%x", firstBytes, secondBytes)
	}
	if len(firstIDs) != len(secondIDs) {
		t.Fatalf("the same seed produced %d identifiers and then %d", len(firstIDs), len(secondIDs))
	}
	for index := range firstIDs {
		if firstIDs[index] != secondIDs[index] {
			t.Fatalf("identifier %d differs between two runs of seed %d: %q vs %q", index, seed, firstIDs[index], secondIDs[index])
		}
	}
}

// TestADifferentSeedIsADifferentRun is the other half, and without it
// "reproducible" would also be satisfied by a source that ignored the seed.
func TestADifferentSeedIsADifferentRun(t *testing.T) {
	seed := testsource.SeedFor(t)

	firstBytes, firstIDs := draw(t, seed)
	otherBytes, otherIDs := draw(t, seed+1)

	if bytes.Equal(firstBytes, otherBytes) {
		t.Errorf("two different seeds produced the same bytes")
	}
	same := firstIDs[0] == otherIDs[0]
	for index := range firstIDs {
		same = same && firstIDs[index] == otherIDs[index]
	}
	if same {
		t.Errorf("two different seeds produced the same identifiers: %v", firstIDs)
	}
}

// TestTheClockOnlyMovesWhenTold is what makes a window something a test can
// cross instead of wait for: two calls to Now answer the same instant, and only
// Advance moves it.
func TestTheClockOnlyMovesWhenTold(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	clock := testsource.NewClock(start)

	if first, second := clock.Now(), clock.Now(); !first.Equal(second) {
		t.Errorf("the explicit clock moved on its own: %s then %s", first, second)
	}
	if now := clock.Now(); !now.Equal(start) {
		t.Errorf("the clock answered %s and was stopped at %s", now, start)
	}
	if moved := clock.Advance(90 * time.Second); !moved.Equal(start.Add(90 * time.Second)) {
		t.Errorf("advancing 90s moved the clock to %s", moved)
	}
	// Running time backwards is refused rather than accepted quietly: a
	// scenario that goes back in time is one nobody can narrate afterwards.
	if moved := clock.Advance(-time.Hour); !moved.Equal(start.Add(90 * time.Second)) {
		t.Errorf("a negative advance moved the clock to %s", moved)
	}
	if zero := testsource.NewClock(time.Time{}); zero.Now().IsZero() {
		t.Errorf("a zero instant stayed zero instead of the epoch")
	}
}

// TestTheSourcesSatisfyThePorts is the compile-time half of the contract, kept
// as a test so that a change to the ports fails here rather than in a scenario
// somewhere else.
func TestTheSourcesSatisfyThePorts(t *testing.T) {
	clock := testsource.NewClock(time.Unix(0, 0))
	random := testsource.NewRandom(1)

	var (
		_ ports.Clock       = clock
		_ ports.Random      = random
		_ ports.IDGenerator = testsource.NewIDs("test", random, clock)
	)

	// The entropy source answers in full even for a size that is not a multiple
	// of the machine word, which is the shape a caller composes a token from.
	for _, size := range []int{0, 1, 7, 8, 9, 33} {
		buffer := make([]byte, size)
		written, err := random.Read(buffer)
		if err != nil {
			t.Fatalf("read of %d bytes failed: %v", size, err)
		}
		if written != size {
			t.Errorf("read of %d bytes wrote %d", size, written)
		}
	}
	// And a scenario parameter drawn from the stream stays inside its bound,
	// including the degenerate one.
	if value := random.Int64n(10); value < 0 || value >= 10 {
		t.Errorf("Int64n(10) answered %d", value)
	}
	if value := random.Int64n(0); value != 0 {
		t.Errorf("Int64n(0) answered %d", value)
	}
}

// TestTheSeedIsRegisteredAndRefusesNonsense states the registry's rules: an
// absent value means the default fixture, a number is taken as given, and
// anything else is refused rather than quietly replaced by the default — a run
// that believes it is replaying a failure must not be running something else.
func TestTheSeedIsRegisteredAndRefusesNonsense(t *testing.T) {
	if value, err := testsource.ParseSeed(""); err != nil || value != testsource.DefaultSeed {
		t.Errorf("an absent seed answered (%d, %v)", value, err)
	}
	if value, err := testsource.ParseSeed("1234"); err != nil || value != 1234 {
		t.Errorf("an explicit seed answered (%d, %v)", value, err)
	}
	for _, refused := range []string{"not-a-seed", "12x4", " "} {
		if value, err := testsource.ParseSeed(refused); err == nil {
			t.Errorf("the registry accepted %q as seed %d", refused, value)
		}
	}

	// SeedFor logs the seed on every path: the log is the registration, and the
	// value it answers is the one the sources are built from.
	registered := testsource.SeedFor(t)
	if registered != testsource.Seed() {
		t.Fatalf("SeedFor answered %d and Seed answered %d", registered, testsource.Seed())
	}

	t.Setenv(testsource.SeedVariable, "1234")
	if seed := testsource.Seed(); seed != 1234 {
		t.Errorf("Seed answered %d with %s=1234", seed, testsource.SeedVariable)
	}
	if seed := testsource.SeedFor(t); seed != 1234 {
		t.Errorf("SeedFor answered %d with %s=1234", seed, testsource.SeedVariable)
	}

	// The default is a constant and not a reading of the clock: two runs of the
	// same commit have to do the same thing.
	if testsource.DefaultSeed == 0 {
		t.Errorf("the default seed is zero, which says nothing about the run")
	}
}
