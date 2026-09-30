package domain

import (
	"errors"
	"testing"
	"time"
)

func epochStart(t *testing.T, year, week int) time.Time {
	t.Helper()
	start, err := Epoch{Year: year, Week: week}.Start()
	if err != nil {
		t.Fatalf("Start(%d-W%02d): %v", year, week, err)
	}
	return start
}

func epochEnd(t *testing.T, year, week int) time.Time {
	t.Helper()
	end, err := Epoch{Year: year, Week: week}.End()
	if err != nil {
		t.Fatalf("End(%d-W%02d): %v", year, week, err)
	}
	return end
}

func TestWeek53ExistsOnlyWhereISOCountsIt(t *testing.T) {
	if !HasWeek53(2020) || !HasWeek53(2026) {
		t.Fatal("2020 and 2026 hold W53: Thursday Jan 1st years count 53 weeks")
	}
	for _, year := range []int{2021, 2022, 2023, 2024, 2025, 2027} {
		if HasWeek53(year) {
			t.Fatalf("%d holds no W53", year)
		}
	}
	start := epochStart(t, 2020, 53)
	if !start.Equal(time.Date(2020, 12, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("2020-W53 starts %s, want Monday 2020-12-28 UTC", start)
	}
	if end := epochEnd(t, 2020, 53); !end.Equal(time.Date(2021, 1, 4, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("2020-W53 ends %s, want Monday 2021-01-04 UTC", end)
	}
	if _, err := (Epoch{Year: 2021, Week: 53}).Start(); !errors.Is(err, ErrInvalidEpoch) {
		t.Fatalf("2021-W53 Start = nil, want ErrInvalidEpoch: the barrier rejects nonexistent weeks")
	}
}

func TestEpochWindowIsHalfOpenOnTheExactTick(t *testing.T) {
	epoch := Epoch{Year: 2026, Week: 53}
	start := epochStart(t, 2026, 53)
	end := epochEnd(t, 2026, 53)
	for _, probe := range []struct {
		instant time.Time
		want    bool
	}{
		{start, true},
		{start.Add(time.Nanosecond), true},
		{end.Add(-time.Nanosecond), true},
		{end, false},
		{end.Add(time.Nanosecond), false},
		{start.Add(-time.Nanosecond), false},
	} {
		if got := epoch.Contains(probe.instant); got != probe.want {
			t.Fatalf("Contains(%s) = %v, want %v: the window is [start, end)", probe.instant, got, probe.want)
		}
	}
	if got := EpochOf(start); got != epoch {
		t.Fatalf("EpochOf(start) = %v, want %v", got, epoch)
	}
	if got := EpochOf(end); got == epoch {
		t.Fatalf("EpochOf(end) = %v, want the next epoch: at the exact closing tick the week expired", got)
	}
	if got := EpochOf(end.Add(-time.Nanosecond)); got != epoch {
		t.Fatalf("EpochOf(end-1ns) = %v, want %v", got, epoch)
	}
}

func TestYearTurnFallsInsideWeek53(t *testing.T) {
	newYear := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := EpochOf(newYear); got != (Epoch{Year: 2026, Week: 53}) {
		t.Fatalf("EpochOf(2027-01-01) = %v, want 2026-W53: the turn falls inside the week", got)
	}
	if got := EpochOf(time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)); got != (Epoch{Year: 2020, Week: 53}) {
		t.Fatalf("EpochOf(2021-01-01) = %v, want 2020-W53", got)
	}
	if got := EpochOf(time.Date(2021, 1, 4, 0, 0, 0, 0, time.UTC)); got != (Epoch{Year: 2021, Week: 1}) {
		t.Fatalf("EpochOf(2021-01-04) = %v, want 2021-W01", got)
	}
}

func TestLocalZoneNeverDecidesTheEpoch(t *testing.T) {
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	// Fall back 2026-11-01: 01:30 happens twice on the wall clock.
	first := time.Date(2026, 11, 1, 1, 30, 0, 0, eastern)
	second := first.Add(time.Hour)
	if first.Equal(second) {
		t.Fatal("fixture needs two distinct instants behind one wall reading")
	}
	if got := EpochOf(first); got != EpochOf(second) {
		t.Fatalf("ambiguous wall maps to %v vs %v: the absolute instant decides, never the local clock", EpochOf(first), EpochOf(second))
	}
	same := first.UTC()
	if EpochOf(time.Date(2026, 11, 1, 1, 30, 0, 0, eastern)) != EpochOf(same.In(eastern)) {
		t.Fatal("same absolute instant in two zones must share one epoch")
	}
	spring := time.Date(2026, 3, 8, 3, 30, 0, 0, eastern)
	if got := EpochOf(spring); got != EpochOf(spring.UTC()) {
		t.Fatal("spring-forward wall must resolve through UTC")
	}
}

func TestInFlightTransactionsSettleByCommitVisibility(t *testing.T) {
	end := epochEnd(t, 2026, 53)
	next := Epoch{Year: 2027, Week: 1}
	begunBefore, postedAfter := end.Add(-time.Hour), end.Add(time.Millisecond)
	if got := AssignEpoch(postedAfter); got != next {
		t.Fatalf("AssignEpoch = %v, want %v: begun %s but posted %s, the database clock at commit decides", got, next, begunBefore, postedAfter)
	}
	postedBefore := end.Add(-time.Hour)
	if got := AssignEpoch(postedBefore); got != (Epoch{Year: 2026, Week: 53}) {
		t.Fatalf("AssignEpoch = %v, want 2026-W53", got)
	}
}

func TestLateJobsReplayTheSameSeal(t *testing.T) {
	epoch := Epoch{Year: 2026, Week: 53}
	end := epochEnd(t, 2026, 53)
	if epoch.Complete(end.Add(-time.Nanosecond)) {
		t.Fatal("incomplete week seals: only whole weeks integrate R4")
	}
	if !epoch.Complete(end) || !epoch.Complete(end.Add(30*24*time.Hour)) {
		t.Fatal("a late job must still seal its explicit epoch: lateness never skips or duplicates")
	}
	first, err := epoch.JobKey()
	if err != nil {
		t.Fatalf("JobKey: %v", err)
	}
	second, err := epoch.JobKey()
	if err != nil || first != second || first != "crumbs-seal-2026-W53" {
		t.Fatalf("JobKey = %q/%q, want one deterministic crumbs-seal-2026-W53", first, second)
	}
	if (Epoch{Year: 2021, Week: 53}).Complete(end) {
		t.Fatal("nonexistent week seals: Complete refuses what validate refuses")
	}
}

func TestEpochKeysParseExactly(t *testing.T) {
	epoch, err := ParseEpochKey("2026-W53")
	if err != nil {
		t.Fatalf("ParseEpochKey: %v", err)
	}
	if epoch != (Epoch{Year: 2026, Week: 53}) {
		t.Fatalf("parsed = %v, want 2026-W53", epoch)
	}
	for _, raw := range []string{"", "2026-W53 ", " 2026-W53", "2026-w53", "2026W53", "26-W53", "2026-W5", "2021-W53", "2026-W54", "2026-W00", "0000-W01"} {
		if _, err := ParseEpochKey(raw); !errors.Is(err, ErrInvalidEpoch) {
			t.Fatalf("ParseEpochKey(%q) = nil, want ErrInvalidEpoch", raw)
		}
	}
	if key, err := epoch.Key(); err != nil || key != "2026-W53" {
		t.Fatalf("Key() = %q/%v, want 2026-W53", key, err)
	}
	if got := (Epoch{Year: 2021, Week: 53}).String(); got != "invalid" {
		t.Fatalf("String() = %q, want invalid", got)
	}
}
