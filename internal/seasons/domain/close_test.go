package domain

import (
	"errors"
	"testing"
	"time"
)

func closeBeginReq(owner string, now time.Time) BeginRequest {
	return BeginRequest{
		Season: "temporada-fecho-1", Owner: owner,
		EndsAt: time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC), Now: now,
		LeaseTTL: time.Minute, SnapshotMilli: 700000000,
	}
}

func mustBeginClose(t *testing.T, now time.Time) CloseRun {
	t.Helper()
	run, err := BeginCloseRun(closeBeginReq("fechador-1", now))
	if err != nil {
		t.Fatalf("BeginCloseRun: %v", err)
	}
	if run.Generation != 1 || run.State != CloseStateClosing {
		t.Fatalf("run = %+v, want generation 1 closing", run)
	}
	return run
}

func TestBeginCloseNeedsCutoff(t *testing.T) {
	ends := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	if _, err := BeginCloseRun(closeBeginReq("fechador-1", ends.Add(-time.Nanosecond))); !errors.Is(err, ErrCloseNotDue) {
		t.Fatalf("before end = %v, want ErrCloseNotDue: cutoff is observed, never anticipated", err)
	}
	at, err := BeginCloseRun(closeBeginReq("fechador-1", ends))
	if err != nil || !at.CutoffAt.Equal(ends) {
		t.Fatalf("at end = %+v/%v, want barrier with cutoff at ends_at: requests at the limit are refused, the barrier is not", at, err)
	}
	after := mustBeginClose(t, ends.Add(time.Hour))
	if !after.CutoffAt.Equal(ends.Add(time.Hour)) {
		t.Fatalf("cutoff = %v, want database now carried as cutoff", after.CutoffAt)
	}
	for _, req := range []BeginRequest{
		{Season: "", Owner: "fechador-1", EndsAt: ends, Now: ends.Add(time.Hour), LeaseTTL: time.Minute},
		{Season: "temporada-fecho-1", Owner: "  ", EndsAt: ends, Now: ends.Add(time.Hour), LeaseTTL: time.Minute},
		{Season: "temporada-fecho-1", Owner: "fechador-1", EndsAt: ends, Now: ends.Add(time.Hour)},
	} {
		if _, err := BeginCloseRun(req); err == nil {
			t.Fatalf("shapeless begin %+v passed", req)
		}
	}
}

func TestTakeoverNeedsLapsedLease(t *testing.T) {
	ends := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	run := mustBeginClose(t, ends.Add(time.Hour))
	if _, err := TakeoverRun(run, "fechador-2", ends.Add(time.Hour).Add(30*time.Second), time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("held takeover = %v, want ErrLeaseHeld", err)
	}
	next, err := TakeoverRun(run, "fechador-2", ends.Add(time.Hour).Add(61*time.Second), time.Minute)
	if err != nil || next.Generation != 2 || next.LeaseOwner != "fechador-2" || next.State != CloseStateClosing {
		t.Fatalf("takeover = %+v/%v, want generation 2 closing under fechador-2", next, err)
	}
	sealed := next
	sealed.State = CloseStateSealed
	if _, err := TakeoverRun(sealed, "fechador-3", ends.Add(3*time.Hour), time.Minute); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("sealed takeover = %v, want ErrInvalidTransition: the archive never reopens", err)
	}
}

func TestFenceRefusesStaleAndForeign(t *testing.T) {
	ends := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	run := mustBeginClose(t, ends.Add(time.Hour))
	good := CloseDrain{Season: "temporada-fecho-1", Generation: 1, Owner: "fechador-1"}
	if err := CheckFence(run, good); err != nil {
		t.Fatalf("current fence: %v", err)
	}
	old, _ := TakeoverRun(run, "fechador-2", ends.Add(3*time.Hour), time.Minute)
	if err := CheckFence(old, good); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("old worker = %v, want ErrStaleGeneration: after a takeover only the new generation moves", err)
	}
	foreign := CloseDrain{Season: "temporada-fecho-1", Generation: 2, Owner: "fechador-1"}
	if err := CheckFence(old, foreign); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("foreign owner = %v, want ErrLeaseHeld", err)
	}
	cross := CloseDrain{Season: "temporada-fecho-2", Generation: 2, Owner: "fechador-2"}
	if err := CheckFence(old, cross); err == nil {
		t.Fatal("cross-book fence passed: one run never moves another book")
	}
	sealed := old
	sealed.State = CloseStateSealed
	current := CloseDrain{Season: "temporada-fecho-1", Generation: 2, Owner: "fechador-2"}
	if err := CheckFence(sealed, current); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("sealed fence = %v, want ErrInvalidTransition", err)
	}
}

func TestAdvanceMovesForwardOnly(t *testing.T) {
	ends := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	run := mustBeginClose(t, ends.Add(time.Hour))
	next, err := AdvanceCursor(run, "hold-aaa", "escrow-aaa", 2, 1)
	if err != nil || next.Drained != 2 || next.Blocked != 1 {
		t.Fatalf("advance = %+v/%v, want 2 drained 1 blocked", next, err)
	}
	if _, err := AdvanceCursor(next, "hold-000", "", 0, 0); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("backward cursor = %v, want refusal: cursors order batches, never rewind", err)
	}
	if _, err := AdvanceCursor(next, "", "", -1, 0); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("negative delta = %v, want refusal", err)
	}
}

func TestFailureParksCursor(t *testing.T) {
	ends := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	run, _ := AdvanceCursor(mustBeginClose(t, ends.Add(time.Hour)), "hold-aaa", "", 1, 0)
	parked, err := RecordFailure(run, CloseFailure{Code: "drain-port", Detail: "hold repo unavailable"})
	if err != nil || parked.State != CloseStateFailed || parked.LastHoldKey != "hold-aaa" {
		t.Fatalf("failure = %+v/%v, want failed with cursor kept for resume", parked, err)
	}
	sealed := run
	sealed.State = CloseStateSealed
	if _, err := RecordFailure(sealed, CloseFailure{Code: "x"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("sealed failure = %v, want ErrInvalidTransition", err)
	}
}

func TestSealNeedsEmptyObligationsAndConservedSnapshot(t *testing.T) {
	book := SealSnapshot{Milli: 700, Legs: 10, Intentions: 5}
	if err := CanSeal(1, 0, book, book); !errors.Is(err, ErrCloseBlocked) {
		t.Fatalf("open escrow = %v, want ErrCloseBlocked: indecision preserves custody", err)
	}
	if err := CanSeal(0, 1, book, book); !errors.Is(err, ErrCloseBlocked) {
		t.Fatalf("active hold = %v, want ErrCloseBlocked", err)
	}
	minted := SealSnapshot{Milli: 699, Legs: 10, Intentions: 5}
	if err := CanSeal(0, 0, book, minted); err == nil {
		t.Fatal("diverged S sealed: the Genesis amount never moves")
	}
	rewound := SealSnapshot{Milli: 700, Legs: 9, Intentions: 5}
	if err := CanSeal(0, 0, book, rewound); err == nil {
		t.Fatal("rewound legs sealed: history never rewinds")
	}
	// Admitted in-flight work may land past the snapshot: growth is
	// inclusion once, never duplication.
	grown := SealSnapshot{Milli: 700, Legs: 12, Intentions: 6}
	if err := CanSeal(0, 0, book, grown); err != nil {
		t.Fatalf("grown snapshot: %v", err)
	}
}

func TestClassifyCloseEscrow(t *testing.T) {
	for _, terminal := range []string{CloseEscrowReleased, CloseEscrowRefunded, CloseEscrowResolved} {
		if got := ClassifyCloseEscrow(terminal); got != CloseNoEffect {
			t.Fatalf("terminal %q = %q, want no-effect with receipt preserved", terminal, got)
		}
	}
	for _, open := range []string{CloseEscrowFunded, CloseEscrowAccepted, CloseEscrowExpired, "desconhecido", ""} {
		if got := ClassifyCloseEscrow(open); got != CloseEscrowBlocked {
			t.Fatalf("open %q = %q, want blocked: absence of proof never settles", open, got)
		}
	}
}
