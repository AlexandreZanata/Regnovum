package clockseed_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

// The ordering scenario of P22-T02, written as a scenario instead of a list of
// assertions: every parameter comes from the registered seed, and the whole plan
// is replayed from it.
//
// What the format guarantees, which is what this scenario measures:
//
//   - the identifier carries the second it was minted in, and later never
//     encodes to an earlier second, so a sequence of mints is ordered by the
//     instant each identifier states;
//   - within one second there is no order to find: the entropy decides, and
//     whoever sorted by the encoded entropy would be sorting randomly.
//
// The second sentence is here because the first version of this scenario
// asserted lexicographic order over the whole string and failed, which is how
// the boundary got written down: a test-side guess about an implementation
// detail is a claim, and the run is what settles claims. (Consumers are told to
// treat the identifier as opaque, so nothing in the product may rely on either
// half of this beyond the format test that lives next door.)
//
// Why it matters: an identifier that could sort before one minted earlier would
// make "the newest" a question nobody can answer without a clock.

// orderingPlan is one run of the scenario: the instants it visits and the
// identifiers it mints along the way.
type orderingPlan struct {
	instants    []time.Time
	identifiers []string
}

// planOrdering generates the scenario from a seed. It is a pure function of it:
// the same seed draws the same advances and the same keys.
func planOrdering(seed int64) orderingPlan {
	clock := testsource.NewClock(time.Unix(1_700_000_000, 0))
	random := testsource.NewRandom(seed)
	generator := clockseed.NewIDGenerator("scenario", random, clock)

	plan := orderingPlan{instants: []time.Time{clock.Now()}}
	for index := 0; index < 24; index++ {
		plan.identifiers = append(plan.identifiers, generator.NewID())
		// Between nothing and 3 seconds: the advances deliberately repeat
		// instants, so the scenario exercises ordering inside one second and
		// not only across seconds.
		plan.instants = append(plan.instants, clock.Advance(time.Duration(random.Int64n(3000))*time.Millisecond))
	}
	return plan
}

// statedSecond reads the second an identifier carries. Parsing it here is
// deliberate and local to the test: the product is told the identifier is
// opaque, and the scenario is checking the format's own promise.
func statedSecond(t *testing.T, identifier string) int64 {
	t.Helper()
	rest := strings.TrimPrefix(identifier, "scenario_")
	seconds, _, found := strings.Cut(rest, "_")
	if !found {
		t.Fatalf("the identifier %q does not carry the segment the format declares", identifier)
	}
	value, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		t.Fatalf("the identifier %q states a second that is not a number: %v", identifier, err)
	}
	return value
}

// TestTheOrderingScenarioReplaysFromItsSeed is the phase's requirement for an
// ordering scenario: the same seed produces the same instants and the same
// identifiers, in the same order.
func TestTheOrderingScenarioReplaysFromItsSeed(t *testing.T) {
	seed := testsource.SeedFor(t)

	first := planOrdering(seed)
	second := planOrdering(seed)

	if len(first.identifiers) != len(second.identifiers) {
		t.Fatalf("the same seed minted %d identifiers and then %d", len(first.identifiers), len(second.identifiers))
	}
	for index := range first.identifiers {
		if first.identifiers[index] != second.identifiers[index] {
			t.Fatalf("identifier %d differs between two runs of seed %d:\n%q\n%q", index, seed, first.identifiers[index], second.identifiers[index])
		}
	}
	for index := range first.instants {
		if !first.instants[index].Equal(second.instants[index]) {
			t.Fatalf("instant %d differs between two runs of seed %d: %s vs %s", index, seed, first.instants[index], second.instants[index])
		}
	}
}

// TestTheMintsNeverStateAnEarlierSecond is the ordering rule the format does
// promise, and the scenario crosses both boundaries on purpose — several
// identifiers inside the same second, and advances that cross a second — so the
// assertion is not satisfied by a sequence of one identifier per second.
func TestTheMintsNeverStateAnEarlierSecond(t *testing.T) {
	seed := testsource.SeedFor(t)
	plan := planOrdering(seed)

	if len(plan.identifiers) == 0 {
		t.Fatal("the scenario minted nothing")
	}
	previous := statedSecond(t, plan.identifiers[0])
	for index, identifier := range plan.identifiers {
		second := statedSecond(t, identifier)
		if second < previous {
			t.Fatalf("identifier %d of seed %d states second %d after %d: a later mint encoded an earlier instant", index, seed, second, previous)
		}
		previous = second
	}

	// Uniqueness is part of the same promise: two identifiers of one run that
	// were equal would be a collision, and the run is where it would appear.
	seen := map[string]bool{}
	for _, identifier := range plan.identifiers {
		if seen[identifier] {
			t.Fatalf("the scenario of seed %d minted %q twice", seed, identifier)
		}
		seen[identifier] = true
	}

	// And the scenario has to reach both shapes, or the two assertions above
	// would hold for a trivial reason.
	sameSecond, crossedSecond := false, false
	for index := 1; index < len(plan.instants); index++ {
		if plan.instants[index].Truncate(time.Second).Equal(plan.instants[index-1].Truncate(time.Second)) {
			sameSecond = true
		} else {
			crossedSecond = true
		}
	}
	if !sameSecond || !crossedSecond {
		t.Fatalf("the scenario of seed %d did not exercise both boundaries (same second: %t, crossed second: %t)", seed, sameSecond, crossedSecond)
	}
}

// TestAnotherSeedIsAnotherRun keeps the replay assertion from being vacuous: a
// source that ignored its seed would satisfy "the same seed replays" perfectly,
// and the rule above would still hold for every seed.
func TestAnotherSeedIsAnotherRun(t *testing.T) {
	seed := testsource.SeedFor(t)

	first := planOrdering(seed)
	other := planOrdering(seed + 1)

	differed := false
	for index := range first.identifiers {
		if first.identifiers[index] != other.identifiers[index] {
			differed = true
			break
		}
	}
	if !differed {
		t.Errorf("two seeds produced the same identifiers:\n%v", first.identifiers)
	}
	if other.instants[0].Equal(first.instants[0]) == false {
		t.Errorf("the scenario starts at a different instant for another seed: %s vs %s", other.instants[0], first.instants[0])
	}
	previous := statedSecond(t, other.identifiers[0])
	for index, identifier := range other.identifiers {
		second := statedSecond(t, identifier)
		if second < previous {
			t.Fatalf("identifier %d of seed %d states second %d after %d", index, seed+1, second, previous)
		}
		previous = second
	}
}
