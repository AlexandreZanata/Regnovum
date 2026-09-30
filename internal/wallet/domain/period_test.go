package domain_test

import (
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

func TestPeriodForAnchorDayClamping(t *testing.T) {
	anchor := time.Date(2026, 1, 31, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name      string
		at        time.Time
		wantIndex int64
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "activation instant",
			at:        anchor,
			wantIndex: 0,
			wantStart: anchor,
			wantEnd:   time.Date(2026, 2, 28, 10, 30, 0, 0, time.UTC),
		},
		{
			name:      "inside the activation period",
			at:        time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
			wantIndex: 0,
			wantStart: anchor,
			wantEnd:   time.Date(2026, 2, 28, 10, 30, 0, 0, time.UTC),
		},
		{
			name:      "first turn into the clamped month",
			at:        time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			wantIndex: 1,
			wantStart: time.Date(2026, 2, 28, 10, 30, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 3, 31, 10, 30, 0, 0, time.UTC),
		},
		{
			name:      "second turn",
			at:        time.Date(2026, 3, 31, 10, 30, 0, 0, time.UTC),
			wantIndex: 2,
			wantStart: time.Date(2026, 3, 31, 10, 30, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 4, 30, 10, 30, 0, 0, time.UTC),
		},
		{
			name:      "third turn clamps april",
			at:        time.Date(2026, 4, 30, 10, 30, 0, 0, time.UTC),
			wantIndex: 3,
			wantStart: time.Date(2026, 4, 30, 10, 30, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 5, 31, 10, 30, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			period := domain.PeriodFor(anchor, tc.at)
			if period.Index() != tc.wantIndex {
				t.Errorf("Index() = %d, want %d", period.Index(), tc.wantIndex)
			}
			if !period.Start().Equal(tc.wantStart) {
				t.Errorf("Start() = %v, want %v", period.Start(), tc.wantStart)
			}
			if !period.End().Equal(tc.wantEnd) {
				t.Errorf("End() = %v, want %v", period.End(), tc.wantEnd)
			}
			if !period.Contains(tc.at) {
				t.Errorf("period does not contain %v", tc.at)
			}
			if period.Contains(period.End()) {
				t.Error("period must be half-open: End is exclusive")
			}
		})
	}
}

func TestPeriodForLeapYear(t *testing.T) {
	anchor := time.Date(2024, 1, 31, 9, 0, 0, 0, time.UTC)
	period := domain.PeriodFor(anchor, time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC))
	if period.Index() != 0 {
		t.Fatalf("Index() = %d, want 0", period.Index())
	}
	wantEnd := time.Date(2024, 2, 29, 9, 0, 0, 0, time.UTC)
	if !period.End().Equal(wantEnd) {
		t.Fatalf("End() = %v, want %v (leap February)", period.End(), wantEnd)
	}

	// The next period starts on the leap day and ends on Mar 31.
	next := period.Next()
	if !next.Start().Equal(wantEnd) {
		t.Errorf("Next().Start() = %v, want %v", next.Start(), wantEnd)
	}
	if wantNextEnd := time.Date(2024, 3, 31, 9, 0, 0, 0, time.UTC); !next.End().Equal(wantNextEnd) {
		t.Errorf("Next().End() = %v, want %v", next.End(), wantNextEnd)
	}
}

func TestPeriodForBoundaries(t *testing.T) {
	anchor := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	first := domain.PeriodFor(anchor, anchor)
	second := domain.PeriodFor(anchor, anchor.AddDate(0, 1, 0))
	justBeforeSecond := domain.PeriodFor(anchor, anchor.AddDate(0, 1, 0).Add(-time.Nanosecond))

	if first.Index() != 0 || justBeforeSecond.Index() != 0 {
		t.Errorf("instants before the first turn must stay in period 0: %d/%d", first.Index(), justBeforeSecond.Index())
	}
	if second.Index() != 1 {
		t.Errorf("exact turn Index() = %d, want 1", second.Index())
	}
	if !second.Contains(anchor.AddDate(0, 1, 0)) {
		t.Error("the turn instant must belong to the new period")
	}

	// Instants before activation map to negative indices and still contain
	// the instant.
	before := domain.PeriodFor(anchor, anchor.Add(-time.Hour))
	if before.Index() != -1 {
		t.Errorf("pre-activation Index() = %d, want -1", before.Index())
	}
	if !before.Contains(anchor.Add(-time.Hour)) {
		t.Error("pre-activation period must contain its instant")
	}
	if !before.End().Equal(anchor) {
		t.Errorf("pre-activation End() = %v, want the anchor %v", before.End(), anchor)
	}
}

func TestPeriodTimeZoneIrrelevance(t *testing.T) {
	anchorUTC := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	brazil := time.FixedZone("BRT", -3*3600)

	// The same instants expressed in another zone produce identical UTC
	// boundaries: a preference timezone can never shift a renewal.
	anchorZoned := anchorUTC.In(brazil)
	atUTC := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	atZoned := atUTC.In(brazil)

	fromUTC := domain.PeriodFor(anchorUTC, atUTC)
	fromZoned := domain.PeriodFor(anchorZoned, atZoned)

	if fromUTC.Index() != fromZoned.Index() {
		t.Fatalf("Index() diverged by zone: %d/%d", fromUTC.Index(), fromZoned.Index())
	}
	if !fromUTC.Start().Equal(fromZoned.Start()) || !fromUTC.End().Equal(fromZoned.End()) {
		t.Fatalf("boundaries diverged by zone: %v–%v vs %v–%v",
			fromUTC.Start(), fromUTC.End(), fromZoned.Start(), fromZoned.End())
	}
	if fromUTC.Start().Location() != time.UTC {
		t.Errorf("Start() location = %v, want UTC", fromUTC.Start().Location())
	}
	if !fromUTC.Contains(atZoned) {
		t.Error("Contains must accept zoned instants")
	}
}

func TestPeriodAtMatchesPeriodFor(t *testing.T) {
	anchor := time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)
	for index := int64(0); index < 15; index++ {
		at := anchor.AddDate(0, int(index), 0)
		fromFor := domain.PeriodFor(anchor, at)
		fromAt := domain.PeriodAt(anchor, fromFor.Index())
		if fromAt.Index() != fromFor.Index() || !fromAt.Start().Equal(fromFor.Start()) || !fromAt.End().Equal(fromFor.End()) {
			t.Fatalf("PeriodAt/PeriodFor diverged at index %d", index)
		}
	}
}

func TestDefaultFreeCyclePolicy(t *testing.T) {
	policy := domain.DefaultFreeCyclePolicy()
	if policy.Franchise.Int64() != domain.FreeMonthlyFranchise || policy.Franchise.Int64() != 5000 {
		t.Fatalf("franchise = %d, want %d", policy.Franchise.Int64(), domain.FreeMonthlyFranchise)
	}
}

func TestPeriodMultiYearDiffAndNegativeMonths(t *testing.T) {
	t.Parallel()

	anchor := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	// A two-year span counts 29 months: the year arithmetic cannot hide
	// behind single-year cases (mutation gate: period.go:93).
	far := domain.PeriodFor(anchor, time.Date(2028, 3, 5, 12, 0, 0, 0, time.UTC))
	if far.Index() != 17 {
		t.Errorf("Index() across years = %d, want 17", far.Index())
	}
	if !far.Start().Equal(time.Date(2028, 2, 17, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Start() across years = %v, want 2028-02-17", far.Start())
	}

	// Negative months walk the floor division backward: -13 lands thirteen
	// months before the anchor, -25 two years and a month back (mutation
	// gate: period.go:101,119,127).
	neg13 := domain.PeriodAt(anchor, -13)
	if neg13.Index() != -13 {
		t.Fatalf("Index() = %d, want -13", neg13.Index())
	}
	if !neg13.Start().Equal(time.Date(2025, 8, 17, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Start() at -13 = %v, want 2025-08-17", neg13.Start())
	}
	if !neg13.Contains(time.Date(2025, 8, 20, 12, 0, 0, 0, time.UTC)) {
		t.Error("period -13 must contain 2025-08-20")
	}
	neg25 := domain.PeriodAt(anchor, -25)
	if !neg25.Start().Equal(time.Date(2024, 8, 17, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Start() at -25 = %v, want 2024-08-17", neg25.Start())
	}
	// An exact multiple of twelve stays exact through the floor helpers.
	neg24 := domain.PeriodAt(anchor, -24)
	if !neg24.Start().Equal(time.Date(2024, 9, 17, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Start() at -24 = %v, want 2024-09-17", neg24.Start())
	}
	// A January anchor makes negative months hit exact multiples of the
	// divisor, which is the only input that tells the remainder check
	// apart (mutation gate: period.go:119).
	january := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	janNeg24 := domain.PeriodAt(january, -24)
	if !janNeg24.Start().Equal(time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Start() at january-24 = %v, want 2024-01-15", janNeg24.Start())
	}
	janNeg25 := domain.PeriodAt(january, -25)
	if !janNeg25.Start().Equal(time.Date(2023, 12, 15, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Start() at january-25 = %v, want 2023-12-15", janNeg25.Start())
	}

	// Thirteen months forward crosses a year boundary with a non-zero
	// floor quotient (mutation gate: period.go:101).
	pos13 := domain.PeriodAt(anchor, 13)
	if !pos13.Start().Equal(time.Date(2027, 10, 17, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Start() at 13 = %v, want 2027-10-17", pos13.Start())
	}
}
