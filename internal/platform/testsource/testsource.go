// Package testsource provides the deterministic sources of the test platform
// (P22-T02): an explicit clock, an explicit entropy source and the delivered
// identifier generator built from both, every one of them a pure function of a
// single seed.
//
// It exists because of two failures the phase is written against. The first is
// a test that agrees with itself only while it runs: a window, a deadline or a
// single-use token decided against the wall clock passes today and fails on a
// slow machine, and the failure looks like a product defect. The second is a
// failure nobody can replay: a scenario that draws its instants and its keys
// from an unrepeatable source can be red once and green forever after, and the
// honest thing to call it is what it is.
//
// So the seed is *registered*: SeedFor logs it on every run, and
// `ARENA_TEST_SEED=<seed> go test ./...` replays a failure exactly. A seed that
// is set and does not parse is a failure rather than a silent fallback, because
// a run that believes it is replaying something it is not is worse than a run
// that says it cannot.
//
// Nothing the product ships may import this package: the sources here answer
// with a deterministic stream where production uses the system clock and
// crypto/rand (internal/platform/clockseed), and the architecture test proves
// that no non-test file in the tree reaches for them.
package testsource

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// SeedVariable is the environment variable that fixes the seed of a run.
const SeedVariable = "ARENA_TEST_SEED"

// DefaultSeed is the seed of a run that did not choose one. It is a constant
// rather than a value read from the clock for the same reason the whole package
// exists: two runs of the same commit have to do the same thing.
const DefaultSeed int64 = 20260923

// ParseSeed is the registry's rule, as a function: an empty value means the
// run did not choose a seed, and anything else has to be a number. It is
// exported so that the refusal below is testable without failing a test on
// purpose.
func ParseSeed(raw string) (int64, error) {
	if raw == "" {
		return DefaultSeed, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a seed", SeedVariable, raw)
	}
	return value, nil
}

// Seed answers the seed of this run without registering anything: the value of
// SeedVariable when it is set and parses, and DefaultSeed otherwise. Code that
// builds a source should prefer SeedFor, which says out loud which seed is in
// use.
func Seed() int64 {
	value, err := ParseSeed(os.Getenv(SeedVariable))
	if err != nil {
		return DefaultSeed
	}
	return value
}

// SeedFor resolves the seed of one test and registers it in the test log. It
// fails the test when the variable is set to something that is not a seed: a
// run that thinks it is replaying a failure must not silently run the default
// fixture instead.
func SeedFor(t *testing.T) int64 {
	t.Helper()
	raw := os.Getenv(SeedVariable)
	value, err := ParseSeed(raw)
	if err != nil {
		t.Fatalf("testsource: %v — a run that cannot register its seed cannot be replayed", err)
	}
	if raw == "" {
		t.Logf("testsource: seed %d (set %s=%d to replay this run)", value, SeedVariable, value)
		return value
	}
	t.Logf("testsource: seed %d, from %s", value, SeedVariable)
	return value
}

// Clock is an explicit clock. It answers the instant it was told and moves only
// when a test moves it, which is what makes a window, a deadline or an expiry
// something a test can cross deliberately instead of wait for.
type Clock struct {
	mu      sync.Mutex
	instant time.Time
}

var _ ports.Clock = (*Clock)(nil)

// NewClock returns a clock stopped at the given instant. A zero instant is
// replaced by the Unix epoch so that the date a scenario runs on is never part
// of its result.
func NewClock(instant time.Time) *Clock {
	if instant.IsZero() {
		instant = time.Unix(0, 0).UTC()
	}
	return &Clock{instant: instant.UTC()}
}

// Now returns the instant the clock was stopped at.
func (clock *Clock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.instant
}

// Advance moves the clock forward and answers the new instant. It refuses to
// move backwards, because a scenario that runs time in reverse is a scenario
// whose result nobody can narrate.
func (clock *Clock) Advance(duration time.Duration) time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	if duration < 0 {
		duration = 0
	}
	clock.instant = clock.instant.Add(duration)
	return clock.instant
}

// Random is a deterministic entropy source: the same seed produces the same
// stream, on any machine, in any order of tests.
//
// It is deliberately *not* a cryptographic source, and it cannot be reached
// from delivered code — the architecture test refuses an import of this package
// outside tests, which is the only thing that keeps "a fake source exists in
// the tree" from being a footgun.
type Random struct {
	mu     sync.Mutex
	source *rand.Rand
}

var _ ports.Random = (*Random)(nil)

// NewRandom starts the stream of one seed. The two halves of the seed are
// different multipliers of the same value so that a run with seed N does not
// begin where a run with seed N+1 continues.
func NewRandom(seed int64) *Random {
	return &Random{source: rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))}
}

// Read fills buffer with bytes derived from the stream and reports how many it
// wrote. It never returns fewer bytes with a nil error, like the port it
// implements.
func (random *Random) Read(buffer []byte) (int, error) {
	random.mu.Lock()
	defer random.mu.Unlock()
	written := 0
	for written < len(buffer) {
		value := random.source.Uint64()
		for index := 0; index < 8 && written < len(buffer); index++ {
			buffer[written] = byte(value)
			value >>= 8
			written++
		}
	}
	return written, nil
}

// Int64n answers a value in [0, n) drawn from the stream. It is how a scenario
// takes one of its parameters from the seed instead of from a literal, so that
// replaying a seed replays the scenario and not only its outcome.
func (random *Random) Int64n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	random.mu.Lock()
	defer random.mu.Unlock()
	return random.source.Int64N(n)
}

// NewIDs builds the delivered identifier generator over explicit sources. The
// generator is the product's own (internal/platform/clockseed), which is the
// point: a scenario that orders identifiers is exercising the format the ledger
// and the contract actually carry, not a test-only lookalike.
func NewIDs(prefix string, random ports.Random, clock ports.Clock) ports.IDGenerator {
	return clockseed.NewIDGenerator(prefix, random, clock)
}
