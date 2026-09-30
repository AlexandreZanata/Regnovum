package ratelimit

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
)

// SubjectKind names one dimension a policy can be keyed on.
type SubjectKind string

const (
	// SubjectAddress is the caller's network address (clientip decides which
	// address that is, and which forwarding headers were evidence).
	SubjectAddress SubjectKind = "address"
	// SubjectAccount is the authenticated account.
	SubjectAccount SubjectKind = "account"
)

// Subject is one dimension of the caller. A subject with an empty value is
// ignored by the limiter rather than turned into a shared bucket, so a
// forgotten value cannot silently pool unrelated callers into one budget.
type Subject struct {
	Kind  SubjectKind
	Value string
}

// Decision is the outcome of one evaluation.
//
// It deliberately carries no subject value: decisions reach tests and logs,
// and an address is potential personal data. The dimension that refused is
// named by kind, which is enough to reason about a refusal and useless for
// identifying a person.
type Decision struct {
	// Allowed reports whether the action may proceed.
	Allowed bool
	// Refused is the dimension that refused, empty when Allowed is true.
	Refused SubjectKind
	// RetryAfter is how long until the refusing dimension has a token again.
	// It is precise here and rounded up at the response boundary, so a
	// sub-second wait is never advertised as "retry now".
	RetryAfter time.Duration
	// Limit is the burst of the tightest dimension that applied.
	Limit int
	// Remaining is the smallest number of requests left across the applied
	// dimensions.
	Remaining int
}

// Guard is the port the inbound adapters depend on: the decision, without the
// policy table and without the transport.
type Guard interface {
	// Allow reports whether one action by one caller is permitted.
	Allow(ctx context.Context, action Action, subjects ...Subject) (Decision, error)
}

// GuardFunc adapts a function to the Guard port.
type GuardFunc func(ctx context.Context, action Action, subjects ...Subject) (Decision, error)

// Allow implements Guard.
func (function GuardFunc) Allow(ctx context.Context, action Action, subjects ...Subject) (Decision, error) {
	return function(ctx, action, subjects...)
}

// Limiter defaults. The capacity is not a performance knob: it is the memory
// bound, chosen to hold every active key of a busy hour while staying far
// below anything that could be called an allocation risk.
const (
	// DefaultCapacity is how many keys one process holds at most.
	DefaultCapacity = 10_000
	// DefaultIdleTTL is how long a key that nobody touches is kept. It is
	// comfortably above the longest refill window in the table, so an idle key
	// is always one whose bucket had refilled anyway.
	DefaultIdleTTL = 15 * time.Minute
)

// Options configures a Limiter.
type Options struct {
	// Capacity is the maximum number of keys held. Zero means DefaultCapacity.
	Capacity int
	// IdleTTL is how long an untouched key is kept. Zero means DefaultIdleTTL;
	// a negative value disables the idle sweep and leaves the capacity bound
	// as the only one.
	IdleTTL time.Duration
	// Now is the clock. Nil means the system clock, read through the package
	// that owns that effect; a test that wants a reproducible window injects
	// its own source instead (internal/platform/testsource).
	Now func() time.Time
}

// entry is one token bucket plus its place in the recency list.
type entry struct {
	key string
	// tokens is a float so a refill is proportional instead of quantized: with
	// integer tokens, one request every half window would be indistinguishable
	// from a burst.
	tokens  float64
	burst   int
	window  time.Duration
	updated time.Time
	element *list.Element
}

// Limiter is the bounded, single-process implementation of Guard.
//
// It holds at most Options.Capacity keys. When a new key would exceed the
// bound, the least recently used key is evicted, and keys untouched for
// Options.IdleTTL are dropped by an amortized sweep. Both bounds are on
// memory, and the honest cost of a memory bound is that eviction forgets a
// bucket: an attacker who floods distinct keys can reset the accounting of the
// keys they force out. The trade is deliberate — an unbounded map is a denial
// of service that works by itself — and the residual risk is documented in
// docs/SECURITY.md rather than hidden here.
//
// The zero Limiter is not usable; build one with NewLimiter.
type Limiter struct {
	mu        sync.Mutex
	entries   map[string]*entry
	recency   *list.List
	capacity  int
	idleTTL   time.Duration
	now       func() time.Time
	lastSweep time.Time
}

// NewLimiter builds a limiter, applying the package defaults to unset options.
func NewLimiter(options Options) *Limiter {
	capacity := options.Capacity
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	idleTTL := options.IdleTTL
	if idleTTL == 0 {
		idleTTL = DefaultIdleTTL
	}
	now := options.Now
	if now == nil {
		now = clockseed.SystemClockNow
	}

	return &Limiter{
		entries:  make(map[string]*entry, capacity),
		recency:  list.New(),
		capacity: capacity,
		idleTTL:  idleTTL,
		now:      now,
	}
}

// Allow implements Guard.
//
// Every declared dimension is evaluated and the strictest result wins: a
// denial is never softened by a dimension that would have allowed the request,
// and the reported remaining budget is the smallest one, because that is the
// number that actually governs.
func (limiter *Limiter) Allow(_ context.Context, action Action, subjects ...Subject) (Decision, error) {
	policy, _ := PolicyFor(action)
	now := limiter.now()

	dimensions := []struct {
		kind   SubjectKind
		budget Budget
	}{
		{kind: SubjectAddress, budget: policy.Address},
	}
	if policy.Account != nil {
		dimensions = append(dimensions, struct {
			kind   SubjectKind
			budget Budget
		}{kind: SubjectAccount, budget: *policy.Account})
	}

	decision := Decision{Allowed: true}
	applied := false

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.sweepLocked(now)

	for _, dimension := range dimensions {
		value, supplied, usable := subjectValue(subjects, dimension.kind)
		switch {
		case !supplied:
			// The dimension does not apply to this caller: an unauthenticated
			// request has no account. Skipping is safe only because the address
			// dimension is mandatory in every policy, which the policy tests
			// assert.
			continue
		case !usable:
			// The caller supplied the dimension without a value. The limiter
			// cannot account for a caller it cannot distinguish, so the request
			// is refused instead of being silently unthrottled. There is no wait
			// to advertise: nothing will change by retrying the same request.
			return Decision{Allowed: false, Refused: dimension.kind}, nil
		}

		allowed, retryAfter, remaining := limiter.takeLocked(keyFor(action, dimension.kind, value), dimension.budget, now)

		if !applied {
			decision.Limit = dimension.budget.Burst
			decision.Remaining = remaining
			applied = true
		} else {
			if dimension.budget.Burst < decision.Limit {
				decision.Limit = dimension.budget.Burst
			}
			if remaining < decision.Remaining {
				decision.Remaining = remaining
			}
		}

		if !allowed {
			if decision.Allowed || retryAfter > decision.RetryAfter {
				decision.RetryAfter = retryAfter
			}
			decision.Allowed = false
			decision.Refused = dimension.kind
		}
	}

	return decision, nil
}

// Len reports how many keys are held, which is the memory bound under test.
func (limiter *Limiter) Len() int {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return len(limiter.entries)
}

// Sweep drops every key idle for at least the configured TTL. Allow performs
// the same sweep at most once per TTL, so calling this is only useful for an
// operator who wants the memory released at a known moment (a maintenance
// tick), never for correctness.
func (limiter *Limiter) Sweep() {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.sweepLocked(limiter.now())
}

// subjectValue finds one subject by kind and reports two different things: the
// second result is whether the caller supplied the dimension at all, and the
// third is whether the value it supplied can key a bucket. The distinction is
// the difference between "this dimension does not apply here" (an
// unauthenticated request has no account) and "this dimension cannot be
// accounted for" (a value that is not there), which are not the same answer.
func subjectValue(subjects []Subject, kind SubjectKind) (value string, supplied bool, usable bool) {
	for _, subject := range subjects {
		if subject.Kind == kind {
			return subject.Value, true, subject.Value != ""
		}
	}
	return "", false, false
}

// keyFor renders the limiter key of one dimension. The action is part of the
// key so that a caller cannot spend one action's budget on another, and so that
// a key never has to be reinterpreted.
func keyFor(action Action, kind SubjectKind, value string) string {
	return string(action) + "|" + string(kind) + "|" + value
}

// takeLocked spends one token from the bucket of a key.
func (limiter *Limiter) takeLocked(key string, budget Budget, now time.Time) (allowed bool, retryAfter time.Duration, remaining int) {
	held, exists := limiter.entries[key]
	if !exists {
		held = &entry{tokens: float64(budget.Burst), burst: budget.Burst, window: budget.Window, updated: now, key: key}
		held.element = limiter.recency.PushFront(held)
		limiter.entries[key] = held
		limiter.evictLocked()
	} else {
		if held.burst != budget.Burst || held.window != budget.Window {
			// The table changed under a live key. Starting the bucket over is
			// the honest reading of a policy edit: the old tokens were granted
			// by a budget that no longer exists.
			held.tokens = float64(budget.Burst)
			held.burst = budget.Burst
			held.window = budget.Window
		} else {
			limiter.refillLocked(held, budget, now)
		}
		held.updated = now
		limiter.recency.MoveToFront(held.element)
	}

	if held.tokens >= 1 {
		held.tokens--
		return true, 0, int(held.tokens)
	}
	return false, held.waitForToken(budget), 0
}

// refillLocked adds the tokens earned since the last touch, capped at the
// burst. A clock that went backwards earns nothing: a negative elapsed time is
// clamped to zero instead of draining the bucket.
func (limiter *Limiter) refillLocked(held *entry, budget Budget, now time.Time) {
	elapsed := now.Sub(held.updated)
	if elapsed <= 0 {
		return
	}
	rate := budget.rate()
	if rate <= 0 {
		return
	}
	held.tokens += elapsed.Seconds() * rate
	if held.tokens > float64(budget.Burst) {
		held.tokens = float64(budget.Burst)
	}
}

// waitForToken is how long until this bucket has one token again. The duration
// is rounded up to the nanosecond scale of the clock it came from; the response
// layer rounds to seconds.
func (held *entry) waitForToken(budget Budget) time.Duration {
	rate := budget.rate()
	if rate <= 0 {
		// A bucket that never refills: the wait is not "a while", it is the
		// rest of the window. Saying so is more honest than a tiny interval
		// that would invite a retry loop.
		if budget.Window > 0 {
			return budget.Window
		}
		return time.Hour
	}
	deficit := 1 - held.tokens
	if deficit <= 0 {
		return 0
	}
	return time.Duration(deficit / rate * float64(time.Second))
}

// evictLocked enforces the capacity bound by dropping the least recently used
// keys.
func (limiter *Limiter) evictLocked() {
	for len(limiter.entries) > limiter.capacity {
		oldest := limiter.recency.Back()
		if oldest == nil {
			return
		}
		evicted := oldest.Value.(*entry)
		limiter.recency.Remove(oldest)
		delete(limiter.entries, evicted.key)
	}
}

// sweepLocked drops keys idle for at least the TTL, at most once per TTL.
//
// The recency list is ordered by last touch, so the walk starts at the oldest
// key and stops at the first live one: the cost is proportional to what is
// dropped, not to what is held.
func (limiter *Limiter) sweepLocked(now time.Time) {
	if limiter.idleTTL <= 0 {
		return
	}
	if !limiter.lastSweep.IsZero() && now.Sub(limiter.lastSweep) < limiter.idleTTL {
		return
	}
	limiter.lastSweep = now

	for element := limiter.recency.Back(); element != nil; {
		previous := element.Prev()
		held := element.Value.(*entry)
		if now.Sub(held.updated) < limiter.idleTTL {
			return
		}
		limiter.recency.Remove(element)
		delete(limiter.entries, held.key)
		element = previous
	}
}
