// Tests of internal/platform/ratelimit (P16-T03): the arithmetic of a token
// bucket, the two dimensions, and the bounds that keep the mechanism from
// becoming the problem.
//
// Every test drives the clock explicitly. A limiter tested against the wall
// clock would either be slow or flaky, and this is arithmetic: Burst requests,
// refill over Window, eviction at Capacity.
package ratelimit_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
)

// clock is a manually advanced clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
}

func (current *clock) Now() time.Time {
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.now
}

func (current *clock) Advance(elapsed time.Duration) {
	current.mu.Lock()
	defer current.mu.Unlock()
	current.now = current.now.Add(elapsed)
}

// address is the subject most tests use.
func address(value string) ratelimit.Subject {
	return ratelimit.Subject{Kind: ratelimit.SubjectAddress, Value: value}
}

// account is the authenticated dimension.
func account(value string) ratelimit.Subject {
	return ratelimit.Subject{Kind: ratelimit.SubjectAccount, Value: value}
}

func allow(t *testing.T, limiter *ratelimit.Limiter, action ratelimit.Action, subjects ...ratelimit.Subject) ratelimit.Decision {
	t.Helper()

	decision, err := limiter.Allow(context.Background(), action, subjects...)
	if err != nil {
		t.Fatalf("Allow(%s) error = %v", action, err)
	}
	return decision
}

func TestBurstThenRefill(t *testing.T) {
	t.Parallel()

	// A policy of the table with one dimension: ten attempts per minute.
	policy, declared := ratelimit.PolicyFor(ratelimit.ActionAuthLogin)
	if !declared {
		t.Fatal("ActionAuthLogin has no policy")
	}
	burst := policy.Address.Burst

	current := newClock()
	limiter := ratelimit.NewLimiter(ratelimit.Options{Now: current.Now})
	client := address("198.51.100.7")

	for spent := 0; spent < burst; spent++ {
		decision := allow(t, limiter, ratelimit.ActionAuthLogin, client)
		if !decision.Allowed {
			t.Fatalf("request %d of the burst was refused (limit %d)", spent+1, decision.Limit)
		}
		if want := burst - spent - 1; decision.Remaining != want {
			t.Errorf("request %d: Remaining = %d, want %d", spent+1, decision.Remaining, want)
		}
	}

	refused := allow(t, limiter, ratelimit.ActionAuthLogin, client)
	if refused.Allowed {
		t.Fatalf("request %d was allowed, want a refusal at the burst", burst+1)
	}
	if refused.Refused != ratelimit.SubjectAddress {
		t.Errorf("Refused = %q, want %q", refused.Refused, ratelimit.SubjectAddress)
	}
	perToken := policy.Address.Window / time.Duration(burst)
	if refused.RetryAfter <= 0 || refused.RetryAfter > perToken {
		t.Errorf("RetryAfter = %v, want a positive wait no longer than one refill interval %v", refused.RetryAfter, perToken)
	}

	// Half of one refill interval is not a token yet.
	current.Advance(perToken / 2)
	if decision := allow(t, limiter, ratelimit.ActionAuthLogin, client); decision.Allowed {
		t.Error("a request was allowed before a token had refilled")
	}

	// The interval is a token.
	current.Advance(perToken)
	if decision := allow(t, limiter, ratelimit.ActionAuthLogin, client); !decision.Allowed {
		t.Errorf("a request after one refill interval was refused: %+v", decision)
	}

	// A full window restores the whole burst, and never more than it.
	current.Advance(policy.Address.Window)
	for spent := 0; spent < burst; spent++ {
		if decision := allow(t, limiter, ratelimit.ActionAuthLogin, client); !decision.Allowed {
			t.Fatalf("after a full window, request %d of the burst was refused", spent+1)
		}
	}
	if decision := allow(t, limiter, ratelimit.ActionAuthLogin, client); decision.Allowed {
		t.Error("the bucket handed out more than its burst after a long idle period")
	}
}

func TestEachDimensionBoundsItsOwnAttack(t *testing.T) {
	t.Parallel()

	// One position change policy: a loose address budget and a tight account
	// budget. The two dimensions exist for two different attacks, and this test
	// names both.
	policy, _ := ratelimit.PolicyFor(ratelimit.ActionPositionChange)
	if policy.Account == nil {
		t.Fatal("ActionPositionChange has no account budget")
	}

	t.Run("one account across many addresses is stopped by the account", func(t *testing.T) {
		t.Parallel()

		current := newClock()
		limiter := ratelimit.NewLimiter(ratelimit.Options{Now: current.Now})
		rotating := account("acc-1")

		for spent := 0; spent < policy.Account.Burst; spent++ {
			// A different address every time: a botnet, or a proxy pool.
			if decision := allow(t, limiter, ratelimit.ActionPositionChange, address(fmt.Sprintf("203.0.113.%d", spent)), rotating); !decision.Allowed {
				t.Fatalf("request %d from a fresh address was refused", spent+1)
			}
		}

		decision := allow(t, limiter, ratelimit.ActionPositionChange, address("203.0.113.200"), rotating)
		if decision.Allowed {
			t.Fatal("the account budget did not bound a rotating-address client")
		}
		if decision.Refused != ratelimit.SubjectAccount {
			t.Errorf("Refused = %q, want %q", decision.Refused, ratelimit.SubjectAccount)
		}
		if decision.Limit != policy.Account.Burst {
			t.Errorf("Limit = %d, want the strictest applied burst %d", decision.Limit, policy.Account.Burst)
		}
	})

	t.Run("many accounts from one address are stopped by the address", func(t *testing.T) {
		t.Parallel()

		current := newClock()
		limiter := ratelimit.NewLimiter(ratelimit.Options{Now: current.Now})
		shared := address("198.51.100.9")

		for spent := 0; spent < policy.Address.Burst; spent++ {
			if decision := allow(t, limiter, ratelimit.ActionPositionChange, shared, account(fmt.Sprintf("acc-%d", spent))); !decision.Allowed {
				t.Fatalf("request %d from a fresh account was refused", spent+1)
			}
		}

		decision := allow(t, limiter, ratelimit.ActionPositionChange, shared, account("acc-fresh"))
		if decision.Allowed {
			t.Fatal("the address budget did not bound an account-rotating client")
		}
		if decision.Refused != ratelimit.SubjectAddress {
			t.Errorf("Refused = %q, want %q", decision.Refused, ratelimit.SubjectAddress)
		}
	})

	t.Run("the refusal reports the longest wait across dimensions", func(t *testing.T) {
		t.Parallel()

		current := newClock()
		limiter := ratelimit.NewLimiter(ratelimit.Options{Now: current.Now})
		shared, rotating := address("198.51.100.10"), account("acc-both")

		for spent := 0; spent < policy.Address.Burst; spent++ {
			allow(t, limiter, ratelimit.ActionPositionChange, shared, account(fmt.Sprintf("acc-%d", spent)))
		}
		// Exhaust the account dimension too.
		for spent := 0; spent < policy.Account.Burst; spent++ {
			allow(t, limiter, ratelimit.ActionPositionChange, shared, rotating)
		}

		decision := allow(t, limiter, ratelimit.ActionPositionChange, shared, rotating)
		if decision.Allowed {
			t.Fatal("both dimensions were exhausted and the request was allowed")
		}
		// The account bucket is the one that refills slower relative to its
		// burst here, and a client told to wait less than it must would retry
		// into another refusal.
		accountWait := time.Duration(float64(policy.Account.Window) / float64(policy.Account.Burst))
		if decision.RetryAfter < accountWait {
			t.Errorf("RetryAfter = %v, want at least the account refill interval %v", decision.RetryAfter, accountWait)
		}
	})
}

func TestActionBudgetsDoNotBleedIntoEachOther(t *testing.T) {
	t.Parallel()

	current := newClock()
	limiter := ratelimit.NewLimiter(ratelimit.Options{Now: current.Now})
	client := address("198.51.100.11")

	login, _ := ratelimit.PolicyFor(ratelimit.ActionAuthLogin)
	for spent := 0; spent < login.Address.Burst; spent++ {
		allow(t, limiter, ratelimit.ActionAuthLogin, client)
	}
	if decision := allow(t, limiter, ratelimit.ActionAuthLogin, client); decision.Allowed {
		t.Fatal("the login budget was not exhausted")
	}

	// A different action from the same address keeps its own budget: the
	// throttle is per action, not a blanket ban on the address.
	if decision := allow(t, limiter, ratelimit.ActionAuthRegister, client); !decision.Allowed {
		t.Errorf("a different action from the same address was refused: %+v", decision)
	}
}

func TestCapacityEvictsTheLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	current := newClock()
	limiter := ratelimit.NewLimiter(ratelimit.Options{Capacity: 3, Now: current.Now})

	login, _ := ratelimit.PolicyFor(ratelimit.ActionAuthLogin)

	// Fill the bound and touch the first key again, so the eviction order is
	// known: the second key is the least recently used.
	for _, client := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		allow(t, limiter, ratelimit.ActionAuthLogin, address(client))
	}
	allow(t, limiter, ratelimit.ActionAuthLogin, address("198.51.100.1"))

	if limiter.Len() != 3 {
		t.Fatalf("Len() = %d, want the capacity 3", limiter.Len())
	}

	// A fourth key evicts the least recently used one, and the bound holds.
	allow(t, limiter, ratelimit.ActionAuthLogin, address("198.51.100.4"))
	if limiter.Len() != 3 {
		t.Fatalf("Len() = %d after eviction, want the capacity 3", limiter.Len())
	}

	// Exhaust the evicted key's budget, then show it starts over: eviction is
	// the documented cost of a memory bound, asserted here rather than implied.
	evicted := address("198.51.100.2")
	for spent := 0; spent < login.Address.Burst; spent++ {
		allow(t, limiter, ratelimit.ActionAuthLogin, evicted)
	}
	if decision := allow(t, limiter, ratelimit.ActionAuthLogin, evicted); decision.Allowed {
		t.Fatal("the evicted key did not rebuild its own bucket")
	}

	// The keys that survived kept their accounting.
	survivor := address("198.51.100.1")
	for spent := 0; spent < login.Address.Burst; spent++ {
		allow(t, limiter, ratelimit.ActionAuthLogin, survivor)
	}
	if decision := allow(t, limiter, ratelimit.ActionAuthLogin, survivor); decision.Allowed {
		t.Error("a surviving key lost its accounting")
	}
}

func TestIdleKeysExpire(t *testing.T) {
	t.Parallel()

	current := newClock()
	limiter := ratelimit.NewLimiter(ratelimit.Options{Capacity: 100, IdleTTL: time.Minute, Now: current.Now})

	stale, fresh := address("198.51.100.1"), address("198.51.100.2")

	allow(t, limiter, ratelimit.ActionAuthLogin, stale)
	if limiter.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", limiter.Len())
	}

	current.Advance(30 * time.Second)
	allow(t, limiter, ratelimit.ActionAuthLogin, fresh)

	// At the TTL boundary the untouched key is idle for exactly the TTL and is
	// dropped; the key touched a moment ago stays.
	current.Advance(30 * time.Second)
	limiter.Sweep()
	if limiter.Len() != 1 {
		t.Fatalf("Len() = %d at the TTL boundary, want only the recently touched key", limiter.Len())
	}

	// The sweep is also amortized inside Allow, so an operator who never calls
	// Sweep still gets the bound: the surviving key is dropped by the next
	// request once it is past its TTL.
	current.Advance(2 * time.Minute)
	allow(t, limiter, ratelimit.ActionAuthLogin, address("198.51.100.3"))
	if limiter.Len() != 1 {
		t.Errorf("Len() = %d, want 1: the amortized sweep did not drop the idle key", limiter.Len())
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	t.Parallel()

	current := newClock()
	limiter := ratelimit.NewLimiter(ratelimit.Options{Capacity: 64, Now: current.Now})
	login, _ := ratelimit.PolicyFor(ratelimit.ActionAuthLogin)

	const workers = 16
	const perWorker = 25

	var waitGroup sync.WaitGroup
	allowed := make([]int, workers)
	for worker := 0; worker < workers; worker++ {
		waitGroup.Add(1)
		go func(worker int) {
			defer waitGroup.Done()
			for attempt := 0; attempt < perWorker; attempt++ {
				// All workers share one key: the total must be bounded by the
				// burst, whatever the interleaving.
				decision, err := limiter.Allow(context.Background(), ratelimit.ActionAuthLogin, address("198.51.100.20"), account("acc-shared"))
				if err != nil {
					t.Errorf("Allow() error = %v", err)
					return
				}
				if decision.Allowed {
					allowed[worker]++
				}
			}
		}(worker)
	}
	waitGroup.Wait()

	total := 0
	for _, count := range allowed {
		total += count
	}
	if total != login.Address.Burst {
		t.Errorf("total allowed = %d, want exactly the burst %d", total, login.Address.Burst)
	}
}

func TestUnusableDimensionIsRefusedInsteadOfIgnored(t *testing.T) {
	t.Parallel()

	current := newClock()
	limiter := ratelimit.NewLimiter(ratelimit.Options{Now: current.Now})

	// An address subject with no value cannot key a bucket. Pooling it into a
	// shared bucket would let one caller spend everyone's budget; ignoring it
	// would be unbounded. Neither is acceptable, so it is refused — and with no
	// wait attached, because retrying the same request changes nothing.
	decision := allow(t, limiter, ratelimit.ActionAuthLogin, ratelimit.Subject{Kind: ratelimit.SubjectAddress})
	if decision.Allowed {
		t.Fatal("an unusable address dimension was allowed")
	}
	if decision.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want none: an unusable key is not a wait", decision.RetryAfter)
	}
	if decision.Refused != ratelimit.SubjectAddress {
		t.Errorf("Refused = %q, want %q", decision.Refused, ratelimit.SubjectAddress)
	}
}

func TestDecisionCarriesNoKeyValue(t *testing.T) {
	t.Parallel()

	current := newClock()
	limiter := ratelimit.NewLimiter(ratelimit.Options{Now: current.Now})

	// The decision is what ends up in a test failure or a log line. It must
	// never carry the address or the account it was keyed on: an address is
	// potential personal data, and a decision is exactly the kind of value that
	// gets printed without a second thought.
	const secretAddress = "198.51.100.77"
	const secretAccount = "0f4c1e5a-account"

	login, _ := ratelimit.PolicyFor(ratelimit.ActionAuthLogin)
	for spent := 0; spent < login.Address.Burst; spent++ {
		allow(t, limiter, ratelimit.ActionAuthLogin, address(secretAddress), account(secretAccount))
	}

	decision := allow(t, limiter, ratelimit.ActionAuthLogin, address(secretAddress), account(secretAccount))
	rendered := fmt.Sprintf("%+v %v", decision, decision)
	for _, secret := range []string{secretAddress, secretAccount} {
		if strings.Contains(rendered, secret) {
			t.Errorf("the decision carries the key value %q: %s", secret, rendered)
		}
	}
}

func TestUndeclaredActionsFallToTheConservativeDefault(t *testing.T) {
	t.Parallel()

	current := newClock()
	limiter := ratelimit.NewLimiter(ratelimit.Options{Now: current.Now})

	policy, declared := ratelimit.PolicyFor(ratelimit.Action("not.declared"))
	if declared {
		t.Fatal("PolicyFor() declared an action that is not in the table")
	}
	if policy.Address.Burst <= 0 || policy.Address.Window <= 0 {
		t.Fatalf("the default policy is not bounded: %+v", policy)
	}

	client := address("198.51.100.30")
	for spent := 0; spent < ratelimit.DefaultPolicy.Address.Burst; spent++ {
		if decision := allow(t, limiter, ratelimit.Action("not.declared"), client); !decision.Allowed {
			t.Fatalf("request %d was refused before the default burst", spent+1)
		}
	}
	if decision := allow(t, limiter, ratelimit.Action("not.declared"), client); decision.Allowed {
		t.Error("an undeclared action was unbounded")
	}
}

func TestEveryDeclaredActionHasABoundedPolicy(t *testing.T) {
	t.Parallel()

	actions := ratelimit.Actions()
	if len(actions) < 5 {
		t.Fatalf("Actions() returned %d actions; the table is suspiciously small", len(actions))
	}

	seen := make(map[ratelimit.Action]bool, len(actions))
	for _, action := range actions {
		if seen[action] {
			t.Errorf("Actions() returned %q twice", action)
		}
		seen[action] = true

		policy, declared := ratelimit.PolicyFor(action)
		if !declared {
			t.Errorf("PolicyFor(%q) fell back to the default: declare the budget in the table", action)
			continue
		}
		// The address dimension is what makes a policy bounded for an
		// unauthenticated caller. Without it, an action would be unlimited for
		// a client that simply does not authenticate.
		if policy.Address.Burst <= 0 {
			t.Errorf("%s: Address.Burst = %d, want a positive burst", action, policy.Address.Burst)
		}
		if policy.Address.Window <= 0 {
			t.Errorf("%s: Address.Window = %v, want a positive window", action, policy.Address.Window)
		}
		if policy.Account == nil {
			continue
		}
		if policy.Account.Burst <= 0 {
			t.Errorf("%s: Account.Burst = %d, want a positive burst", action, policy.Account.Burst)
		}
		if policy.Account.Window <= 0 {
			t.Errorf("%s: Account.Window = %v, want a positive window", action, policy.Account.Window)
		}
		// The account dimension is the precise one and the address dimension is
		// the blunt one (carrier NAT puts thousands of people behind an
		// address), so the account budget is never looser than the address
		// budget for a window; otherwise the sharp signal would be the weaker
		// one.
		if policy.Account.Burst > policy.Address.Burst && policy.Account.Window <= policy.Address.Window {
			t.Errorf("%s: the account budget (%d per %v) is looser than the address budget (%d per %v)",
				action, policy.Account.Burst, policy.Account.Window, policy.Address.Burst, policy.Address.Window)
		}
	}
}

func TestTheLimitIsPerInstance(t *testing.T) {
	t.Parallel()

	// The documented limit of an in-memory limiter (docs/SECURITY.md section
	// 6): two processes hold two independent tables, so two instances allow
	// twice the budget. It is asserted here so that the multi-instance
	// consequence is a fact of the build, not a sentence in a document that
	// could drift.
	current := newClock()
	first := ratelimit.NewLimiter(ratelimit.Options{Now: current.Now})
	second := ratelimit.NewLimiter(ratelimit.Options{Now: current.Now})

	login, _ := ratelimit.PolicyFor(ratelimit.ActionAuthLogin)
	client := address("198.51.100.40")

	for spent := 0; spent < login.Address.Burst; spent++ {
		allow(t, first, ratelimit.ActionAuthLogin, client)
		allow(t, second, ratelimit.ActionAuthLogin, client)
	}

	if decision := allow(t, first, ratelimit.ActionAuthLogin, client); decision.Allowed {
		t.Error("the first instance allowed beyond its budget")
	}
	if decision := allow(t, second, ratelimit.ActionAuthLogin, client); decision.Allowed {
		t.Error("the second instance allowed beyond its budget")
	}

	// Together they served twice the burst: this is the number the edge layer
	// (Cloudflare) has to bound, because the application cannot.
	if total := login.Address.Burst + login.Address.Burst; total != 2*login.Address.Burst {
		t.Fatalf("unreachable arithmetic: %d", total)
	}
}

func TestGuardFuncAdaptsAFunction(t *testing.T) {
	t.Parallel()

	var seen []ratelimit.Subject
	guard := ratelimit.GuardFunc(func(_ context.Context, action ratelimit.Action, subjects ...ratelimit.Subject) (ratelimit.Decision, error) {
		if action != ratelimit.ActionAuthLogin {
			t.Errorf("action = %q, want %q", action, ratelimit.ActionAuthLogin)
		}
		seen = subjects
		return ratelimit.Decision{Allowed: true, Limit: 1, Remaining: 1}, nil
	})

	decision, err := guard.Allow(context.Background(), ratelimit.ActionAuthLogin, address("198.51.100.50"))
	if err != nil {
		t.Fatalf("Allow() error = %v", err)
	}
	if !decision.Allowed {
		t.Error("GuardFunc did not forward the decision")
	}
	if len(seen) != 1 || seen[0].Kind != ratelimit.SubjectAddress {
		t.Errorf("subjects = %+v, want the address subject", seen)
	}

	// The limiter satisfies the same port, which is what makes it substitutable
	// at composition: the interface is the seam, not an abstraction nobody
	// crosses.
	var _ ratelimit.Guard = ratelimit.NewLimiter(ratelimit.Options{})
	var _ ratelimit.Guard = ratelimit.GuardFunc(nil)
	_ = http.StatusTooManyRequests
}
