package domain

import (
	"errors"
	"math"
	"testing"
)

func TestMedianR4NeedsSealedWeeksOnly(t *testing.T) {
	if got, err := MedianR4(nil); err != nil || got != 0 {
		t.Fatalf("empty R4 = %d/%v, want defined zero", got, err)
	}
	single, err := MedianR4([]int64{1400})
	if err != nil || single != 1400 {
		t.Fatalf("single week = %d/%v, want 1400", single, err)
	}
	if _, err := MedianR4([]int64{1, 2, 3, 4, 5}); !errors.Is(err, ErrInvalidBudget) {
		t.Fatalf("five weeks = %v, want ErrInvalidBudget: R4 never looks past four", err)
	}
}

func TestMedianR4OrdersBeforeArithmetic(t *testing.T) {
	orders := [][]int64{
		{100, 300, 200, 400},
		{400, 100, 300, 200},
		{200, 400, 100, 300},
	}
	for _, order := range orders {
		got, err := MedianR4(order)
		if err != nil {
			t.Fatalf("MedianR4(%v): %v", order, err)
		}
		// Ordered 100,200,300,400: central 200+300=500, floor 250.
		if got != 250 {
			t.Fatalf("MedianR4(%v) = %d, want floored 250", order, got)
		}
	}
	odd, err := MedianR4([]int64{300, 100, 200})
	if err != nil || odd != 200 {
		t.Fatalf("odd median = %d/%v, want 200", odd, err)
	}
}

func TestMedianR4FloorsEvenSamplesDown(t *testing.T) {
	got, err := MedianR4([]int64{3, 4})
	if err != nil || got != 3 {
		t.Fatalf("median(3,4) = %d/%v, want floored 3", got, err)
	}
	// Negative straddle floors toward negative infinity, never up.
	negative, err := MedianR4([]int64{-1, 0})
	if err != nil || negative != -1 {
		t.Fatalf("median(-1,0) = %d/%v, want floored -1", negative, err)
	}
	mixed, err := MedianR4([]int64{-3, 4})
	if err != nil || mixed != 0 {
		t.Fatalf("median(-3,4) = %d/%v, want floored 0", mixed, err)
	}
	if _, err := MedianR4([]int64{math.MaxInt64, math.MaxInt64}); !errors.Is(err, ErrInvalidBudget) {
		t.Fatalf("overflowing centre = %v, want ErrInvalidBudget instead of wrapping", err)
	}
	if _, err := MedianR4([]int64{math.MinInt64, math.MinInt64}); !errors.Is(err, ErrInvalidBudget) {
		t.Fatalf("underflowing centre = %v, want ErrInvalidBudget instead of wrapping", err)
	}
}

func TestBoundWeeklyBudgetCapsToFreeTreasury(t *testing.T) {
	capped, err := BoundWeeklyBudget(2500, 1000)
	if err != nil || capped != 1000 {
		t.Fatalf("capped budget = %d/%v, want free 1000", capped, err)
	}
	roomy, err := BoundWeeklyBudget(800, 1000)
	if err != nil || roomy != 800 {
		t.Fatalf("roomy budget = %d/%v, want R4 800", roomy, err)
	}
	if got, err := BoundWeeklyBudget(0, 1000); err != nil || got != 0 {
		t.Fatalf("zero R4 = %d/%v, want defined zero", got, err)
	}
	if got, err := BoundWeeklyBudget(2500, 0); err != nil || got != 0 {
		t.Fatalf("zero Treasury = %d/%v, want defined zero", got, err)
	}
	if _, err := BoundWeeklyBudget(100, -1); !errors.Is(err, ErrInvalidBudget) {
		t.Fatalf("negative Treasury = %v, want ErrInvalidBudget", err)
	}
}

func TestNegativeR4BlocksWithoutNegativeTransfer(t *testing.T) {
	got, err := BoundWeeklyBudget(-5000, 100000)
	if !errors.Is(err, ErrNegativeR4Blocked) {
		t.Fatalf("negative R4 = %d/%v, want ErrNegativeR4Blocked", got, err)
	}
	if got != 0 {
		t.Fatalf("blocked budget = %d, want exactly zero: never a negative transfer", got)
	}
	// A refund-only run stays negative through the median as well.
	median, err := MedianR4([]int64{-5000})
	if err != nil || median != -5000 {
		t.Fatalf("refund-only median = %d/%v, want -5000 visible", median, err)
	}
	if _, err := BoundWeeklyBudget(median, 0); !errors.Is(err, ErrNegativeR4Blocked) {
		t.Fatalf("negative R4 with empty Treasury = %v, want the block, not silent zero", err)
	}
}
