package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/transparency/domain"
)

func TestMethodologyVersionIsPinned(t *testing.T) {
	t.Parallel()

	if domain.MethodologyVersion != 1 {
		t.Fatalf("MethodologyVersion = %d, want 1 (changes require conscious edits with recalculation notes)", domain.MethodologyVersion)
	}
}

func TestPeriodValidation(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	period, err := domain.NewPeriod(start, end)
	if err != nil {
		t.Fatalf("NewPeriod: %v", err)
	}
	if !period.Start().Equal(start) || !period.End().Equal(end) {
		t.Fatalf("period = %v/%v", period.Start(), period.End())
	}

	for _, tc := range []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{name: "zero start", start: time.Time{}, end: end},
		{name: "zero end", start: start, end: time.Time{}},
		{name: "equal", start: start, end: start},
		{name: "reversed", start: end, end: start},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := domain.NewPeriod(tc.start, tc.end); !errors.Is(err, domain.ErrInvalidPeriod) {
				t.Fatalf("error = %v, want ErrInvalidPeriod", err)
			}
		})
	}
}

func TestLowCountSuppression(t *testing.T) {
	t.Parallel()

	if got := domain.Suppress(0); got != 0 {
		t.Fatalf("Suppress(0) = %d, want 0", got)
	}
	if got := domain.Suppress(4); got != 0 {
		t.Fatalf("Suppress(4) = %d, want 0 (below threshold)", got)
	}
	if got := domain.Suppress(5); got != 5 {
		t.Fatalf("Suppress(5) = %d, want 5 (threshold is inclusive)", got)
	}
	if got := domain.Suppress(30000); got != 30000 {
		t.Fatalf("Suppress(30000) = %d, want 30000", got)
	}
	if got := domain.Suppress(-7); got != 0 {
		t.Fatalf("Suppress(-7) = %d, want 0 (never negative)", got)
	}
}
