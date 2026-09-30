package turnstile_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

// TestFailureTrackerElevatesOnARunOfFailures is the risk signal's core
// behaviour: a run, not a lifetime; cleared by success; forgotten by time.
func TestFailureTrackerElevatesOnARunOfFailures(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	tracker := turnstile.NewFailureTracker(turnstile.FailureTrackerOptions{
		Threshold: 3,
		Window:    10 * time.Minute,
		Now:       func() time.Time { return now },
	})

	const caller = "198.51.100.20"

	if tracker.Elevated(caller) {
		t.Error("a caller with no failures is elevated")
	}
	for failures := 1; failures <= 3; failures++ {
		tracker.Observe(caller, true)
		elevated := tracker.Elevated(caller)
		if elevated != (failures >= 3) {
			t.Errorf("after %d failure(s): Elevated = %v, want %v", failures, elevated, failures >= 3)
		}
	}

	// A success ends the run.
	tracker.Observe(caller, false)
	if tracker.Elevated(caller) {
		t.Error("a caller whose run was cleared is still elevated")
	}

	// And the window ends it too.
	tracker.Observe(caller, true)
	tracker.Observe(caller, true)
	tracker.Observe(caller, true)
	if !tracker.Elevated(caller) {
		t.Fatal("the caller is not elevated after three failures")
	}
	now = now.Add(11 * time.Minute)
	if tracker.Elevated(caller) {
		t.Error("a caller whose last failure fell outside the window is still elevated")
	}
}

// TestFailureTrackerCountsCallersSeparatelyAndIgnoresEmptyOnes keeps one
// caller's failures from becoming another's, and keeps an unidentifiable
// caller from being pooled into somebody else's run.
func TestFailureTrackerCountsCallersSeparatelyAndIgnoresEmptyOnes(t *testing.T) {
	t.Parallel()

	tracker := turnstile.NewFailureTracker(turnstile.FailureTrackerOptions{Threshold: 2, Window: time.Minute})

	tracker.Observe("198.51.100.1", true)
	tracker.Observe("198.51.100.1", true)
	tracker.Observe("198.51.100.2", true)

	if !tracker.Elevated("198.51.100.1") {
		t.Error("the caller with two failures is not elevated")
	}
	if tracker.Elevated("198.51.100.2") {
		t.Error("a caller with one failure is elevated")
	}

	// An empty address is not tracked at all: it identifies nobody.
	for range 5 {
		tracker.Observe("", true)
	}
	if got := tracker.Len(); got != 2 {
		t.Errorf("Len = %d, want 2 tracked callers", got)
	}
	if tracker.Elevated("") {
		t.Error("the empty address was elevated")
	}
}

// TestFailureTrackerIsBounded is the memory bound under test: the tracker
// cannot grow with traffic, and the eviction drops the least recently touched
// run first.
func TestFailureTrackerIsBounded(t *testing.T) {
	t.Parallel()

	tracker := turnstile.NewFailureTracker(turnstile.FailureTrackerOptions{
		Threshold: 1,
		Window:    time.Hour,
		Capacity:  4,
	})

	for index := range 16 {
		tracker.Observe(fmt.Sprintf("198.51.100.%d", index), true)
	}

	if got := tracker.Len(); got != 4 {
		t.Errorf("Len = %d, want the capacity 4", got)
	}
	if tracker.Elevated("198.51.100.0") {
		t.Error("the first caller survived an eviction that should have dropped it")
	}
	if !tracker.Elevated("198.51.100.15") {
		t.Error("the most recent caller was evicted")
	}
}

// TestANilTrackerReportsElevation states the documented reading of a missing
// signal once more at the tracker's own layer, so the behaviour cannot be
// changed by accident in the enforcer alone.
func TestANilTrackerReportsElevation(t *testing.T) {
	t.Parallel()

	var tracker *turnstile.FailureTracker
	if !tracker.Elevated("198.51.100.1") {
		t.Error("a nil tracker did not report elevation")
	}
	tracker.Observe("198.51.100.1", true) // must not panic
}
