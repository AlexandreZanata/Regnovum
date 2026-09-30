package turnstile_test

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

// The expiry scenario of P22-T02: a window is crossed deliberately, with every
// instant and every token drawn from the registered seed.
//
// What it is guarding is the single-use rule of a challenge token (THR-AUTH-02):
// a token that was redeemed is refused while it is remembered, and redeemable
// again once the window has passed — the second half is what keeps a
// remembered token from becoming a permanent ban for the caller who presents
// it. The scenario is replayed from its seed, so a failure says which seed
// reproduces it.

// expiryWindow is the window the scenario crosses. It is short because the
// scenario crosses it by moving a clock, not by waiting.
const expiryWindow = 45 * time.Second

// expiryStep is one tick of the scenario: the token presented, and how far the
// clock had moved since the previous tick.
type expiryStep struct {
	token   string
	advance time.Duration
}

// expiryPlan is one run: the steps and the verdicts the memory answered, plus
// the instants, so that the model below can be checked against what happened.
type expiryPlan struct {
	steps    []expiryStep
	verdicts []bool
}

// planExpiry generates the scenario from a seed: a pool of tokens, and a walk
// through it where each step moves the clock by an amount derived from the same
// stream. Advances reach past the window on purpose — a plan that never crossed
// it would not exercise an expiry at all.
func planExpiry(seed int64) expiryPlan {
	random := testsource.NewRandom(seed)
	pool := make([]string, 0, 6)
	raw := make([]byte, 8)
	for index := 0; index < 6; index++ {
		if _, err := random.Read(raw); err != nil {
			panic("the deterministic source failed: " + err.Error())
		}
		pool = append(pool, hex.EncodeToString(raw))
	}

	plan := expiryPlan{steps: make([]expiryStep, 0, 40)}
	for index := 0; index < 40; index++ {
		token := pool[random.Int64n(int64(len(pool)))]
		advance := time.Duration(random.Int64n(int64(expiryWindow/time.Second)*2+1)) * time.Second
		plan.steps = append(plan.steps, expiryStep{token: token, advance: advance})
	}
	return plan
}

// verdictsOf runs the plan against the delivered memory and answers what it
// said, step by step.
func verdictsOf(seed int64) expiryPlan {
	plan := planExpiry(seed)
	clock := testsource.NewClock(time.Unix(1_700_000_000, 0))
	memory := turnstile.NewReplayMemory(0, expiryWindow, clock.Now)

	plan.verdicts = make([]bool, 0, len(plan.steps))
	for _, step := range plan.steps {
		clock.Advance(step.advance)
		plan.verdicts = append(plan.verdicts, memory.Redeem(step.token))
	}
	return plan
}

// TestTheExpiryScenarioReplaysFromItsSeed is the phase's requirement for an
// expiry scenario: the same registered seed answers the same verdicts.
func TestTheExpiryScenarioReplaysFromItsSeed(t *testing.T) {
	seed := testsource.SeedFor(t)

	first := verdictsOf(seed)
	second := verdictsOf(seed)

	if len(first.verdicts) != len(second.verdicts) {
		t.Fatalf("the same seed answered %d verdicts and then %d", len(first.verdicts), len(second.verdicts))
	}
	for index := range first.verdicts {
		if first.verdicts[index] != second.verdicts[index] {
			t.Fatalf("step %d differs between two runs of seed %d: %t vs %t", index, seed, first.verdicts[index], second.verdicts[index])
		}
	}
}

// TestTheWindowIsWhatDecidesEveryVerdict checks the verdicts against a model of
// the rule written from the documentation rather than from the implementation:
// a token never presented is redeemable, a token presented inside the window is
// refused, and a token whose window has passed is redeemable again.
//
// It is a stronger statement than "the same seed replays": a memory that
// answered false forever would replay perfectly and be a denial of service for
// every caller who ever solved a challenge.
func TestTheWindowIsWhatDecidesEveryVerdict(t *testing.T) {
	seed := testsource.SeedFor(t)
	plan := verdictsOf(seed)

	clock := testsource.NewClock(time.Unix(1_700_000_000, 0))
	lastRedeemed := map[string]time.Time{}
	refused, allowedAgain := false, false

	for index, step := range plan.steps {
		clock.Advance(step.advance)
		now := clock.Now()
		want := true
		if previous, seen := lastRedeemed[step.token]; seen {
			if now.Sub(previous) <= expiryWindow {
				want = false
				refused = true
			} else {
				allowedAgain = true
			}
		}
		if plan.verdicts[index] != want {
			t.Fatalf(
				"step %d: the memory answered %t and the window says %t (token %q at %s)",
				index, plan.verdicts[index], want, step.token, now,
			)
		}
		// The memory remembers a redemption only when it accepted one.
		if want {
			lastRedeemed[step.token] = now
		}
	}

	if !refused {
		t.Errorf("the scenario of seed %d never presented a token twice inside its window: the refusal was never exercised", seed)
	}
	if !allowedAgain {
		t.Errorf("the scenario of seed %d never presented a token whose window had passed: the expiry was never exercised", seed)
	}
}

// TestAnotherSeedIsAnotherExpiryPlan keeps the replay assertion from being
// vacuous — a source that ignored the seed would satisfy it — and re-states the
// rule for the second plan.
func TestAnotherSeedIsAnotherExpiryPlan(t *testing.T) {
	seed := testsource.SeedFor(t)

	first := planExpiry(seed)
	other := planExpiry(seed + 1)

	differed := false
	for index := range first.steps {
		if first.steps[index] != other.steps[index] {
			differed = true
			break
		}
	}
	if !differed {
		t.Errorf("two seeds produced the same plan: %v", first.steps)
	}
}
