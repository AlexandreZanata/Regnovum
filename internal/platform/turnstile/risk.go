package turnstile

import (
	"container/list"
	"sync"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
)

// The risk signal turns "challenge after consecutive failures" (THR-AUTH-02)
// into a decision that can be made before the request runs: the failures of
// the past are counted, and the challenge requirement of an action is resolved
// against them.
//
// The signal is deliberately narrow. It counts *consecutive* failures of one
// caller and forgets them on the first success, so it describes a run of
// guesses rather than a lifetime of mistakes; it is keyed on the caller's
// address and not on the account, because the address is what the layer in
// front of authentication can know, and because keying on an email would turn
// the counter into an oracle for whether an account exists; and it holds
// nothing but a count and an instant, so it can never become a record of who
// failed at what.
const (
	// DefaultFailureThreshold is how many consecutive failures make a caller
	// elevated. A person mistyping a password a few times never meets it; a
	// guessing loop meets it quickly.
	DefaultFailureThreshold = 5
	// DefaultFailureWindow is how long a failure is remembered. A run of
	// failures spread across a whole day is not a run.
	DefaultFailureWindow = 15 * time.Minute
	// DefaultTrackedCallers bounds the tracker, in callers. It mirrors the
	// rate limiter's bound and for the same reason: an unbounded map is a
	// denial of service that works by itself.
	DefaultTrackedCallers = 10_000
)

// FailureTracker counts consecutive failures per caller address.
//
// The zero FailureTracker is not usable; build one with NewFailureTracker.
type FailureTracker struct {
	mu        sync.Mutex
	entries   map[string]*failureRecord
	recency   *list.List
	threshold int
	window    time.Duration
	capacity  int
	now       func() time.Time
}

// failureRecord is one caller's run of failures.
type failureRecord struct {
	address   string
	failures  int
	updatedAt time.Time
	position  *list.Element
}

// FailureTrackerOptions configures a tracker. Zero values take the package
// defaults; a negative threshold means every failure is elevated, which is the
// conservative reading of an operator asking for the strictest signal.
type FailureTrackerOptions struct {
	// Threshold is how many consecutive failures make a caller elevated.
	Threshold int
	// Window is how long a failure is remembered.
	Window time.Duration
	// Capacity is how many callers are tracked at most.
	Capacity int
	// Now is the clock. Nil means the system clock, read through the package
	// that owns that effect; a test injects its own source instead, because a
	// failure window counted against the wall clock is a test that agrees with
	// itself only while it runs.
	Now func() time.Time
}

// NewFailureTracker builds a tracker, applying the package defaults.
func NewFailureTracker(options FailureTrackerOptions) *FailureTracker {
	threshold := options.Threshold
	if threshold == 0 {
		threshold = DefaultFailureThreshold
	}
	window := options.Window
	if window <= 0 {
		window = DefaultFailureWindow
	}
	capacity := options.Capacity
	if capacity <= 0 {
		capacity = DefaultTrackedCallers
	}
	now := options.Now
	if now == nil {
		now = clockseed.SystemClockNow
	}

	return &FailureTracker{
		entries:   make(map[string]*failureRecord, capacity),
		recency:   list.New(),
		threshold: threshold,
		window:    window,
		capacity:  capacity,
		now:       now,
	}
}

// Observe records the outcome of one attempt by one caller. A success clears
// the run, because "consecutive" is the property the policy is about; an
// empty address is ignored, because a caller the tracker cannot distinguish
// from every other unnamed caller must not be pooled into a shared run.
func (tracker *FailureTracker) Observe(address string, failed bool) {
	if tracker == nil || address == "" {
		return
	}

	now := tracker.now()

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.sweepLocked(now)

	record, exists := tracker.entries[address]
	if !failed {
		if exists {
			tracker.recency.Remove(record.position)
			delete(tracker.entries, address)
		}
		return
	}

	if !exists {
		record = &failureRecord{address: address, updatedAt: now}
		record.position = tracker.recency.PushFront(record)
		tracker.entries[address] = record
		tracker.evictLocked()
	} else {
		tracker.recency.MoveToFront(record.position)
	}
	record.failures++
	record.updatedAt = now
}

// Elevated reports whether a caller is under elevated risk: a run of failures
// at or above the threshold, still inside the window.
//
// A nil tracker reports every caller as elevated. That is the safe reading of
// a missing signal: without one, the requirement of a risk-gated action cannot
// be decided, and the alternatives would be to skip the challenge for everyone
// (unprotected) or to refuse everyone (broken). Challenging everyone keeps the
// action working and the protection in place, and a test asserts it.
func (tracker *FailureTracker) Elevated(address string) bool {
	if tracker == nil {
		return true
	}
	if address == "" {
		return false
	}

	now := tracker.now()

	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	record, exists := tracker.entries[address]
	if !exists {
		return false
	}
	if now.Sub(record.updatedAt) >= tracker.window {
		return false
	}
	return record.failures >= tracker.threshold
}

// Len reports how many callers are tracked, which is the memory bound under
// test.
func (tracker *FailureTracker) Len() int {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return len(tracker.entries)
}

// sweepLocked drops runs that fell outside the window.
func (tracker *FailureTracker) sweepLocked(now time.Time) {
	for element := tracker.recency.Back(); element != nil; {
		previous := element.Prev()
		record := element.Value.(*failureRecord)
		if now.Sub(record.updatedAt) < tracker.window {
			return
		}
		tracker.recency.Remove(element)
		delete(tracker.entries, record.address)
		element = previous
	}
}

// evictLocked enforces the capacity bound by dropping the least recently
// touched runs. The honest cost is the same as the rate limiter's: an attacker
// flooding distinct addresses can forget the run of the addresses they push
// out, which is why the bound is generous and the residual risk documented.
func (tracker *FailureTracker) evictLocked() {
	for len(tracker.entries) > tracker.capacity {
		oldest := tracker.recency.Back()
		if oldest == nil {
			return
		}
		record := oldest.Value.(*failureRecord)
		tracker.recency.Remove(oldest)
		delete(tracker.entries, record.address)
	}
}
