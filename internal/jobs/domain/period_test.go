package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

func instant(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestPeriodAtLabelsTheSlotInUTC(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		interval domain.Interval
		at       time.Time
		want     string
	}{
		{"daily", domain.IntervalDaily, instant("2026-09-18T23:59:59Z"), "2026-09-18"},
		{"monthly", domain.IntervalMonthly, instant("2026-09-18T00:00:00Z"), "2026-09"},
		{"daily across midnight", domain.IntervalDaily, instant("2026-01-01T00:00:00Z"), "2026-01-01"},
		{"monthly in another zone", domain.IntervalMonthly, instant("2026-09-30T22:00:00-03:00"), "2026-10"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			period, err := domain.PeriodAt(testCase.interval, testCase.at)
			if err != nil {
				t.Fatalf("PeriodAt() error = %v", err)
			}
			if period.String() != testCase.want {
				t.Errorf("period = %q, want %q", period, testCase.want)
			}
			if period.Interval() != testCase.interval {
				t.Errorf("interval = %q, want %q", period.Interval(), testCase.interval)
			}
		})
	}
	if _, err := domain.PeriodAt("weekly", time.Now()); !errors.Is(err, domain.ErrInvalidPeriod) {
		t.Errorf("PeriodAt(weekly) error = %v, want ErrInvalidPeriod", err)
	}
}

func TestPreviousPeriodSkipsMonthsCorrectly(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		interval domain.Interval
		label    string
		want     string
	}{
		{"january precedes december", domain.IntervalMonthly, "2026-01", "2025-12"},
		{"march precedes february", domain.IntervalMonthly, "2026-03", "2026-02"},
		{"first day precedes the last day of the year", domain.IntervalDaily, "2026-01-01", "2025-12-31"},
		{"leap day", domain.IntervalDaily, "2024-03-01", "2024-02-29"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			period, err := domain.ParsePeriod(testCase.interval, testCase.label)
			if err != nil {
				t.Fatalf("ParsePeriod() error = %v", err)
			}
			previous, err := period.Previous()
			if err != nil {
				t.Fatalf("Previous() error = %v", err)
			}
			if previous.String() != testCase.want {
				t.Errorf("previous = %q, want %q", previous, testCase.want)
			}
			// A non-positive step count is not a step forward: it leaves the
			// period alone, so the catch-up loop needs no branching.
			for _, steps := range []int{0, -1} {
				same, err := previous.Back(steps)
				if err != nil {
					t.Fatalf("Back(%d) error = %v", steps, err)
				}
				if same.String() != previous.String() {
					t.Errorf("Back(%d) = %q, want %q", steps, same, previous)
				}
			}
		})
	}
}

func TestBackStepsOverAnyNumberOfPeriods(t *testing.T) {
	period, err := domain.ParsePeriod(domain.IntervalMonthly, "2026-09")
	if err != nil {
		t.Fatalf("ParsePeriod() error = %v", err)
	}
	older, err := period.Back(12)
	if err != nil {
		t.Fatalf("Back(12) error = %v", err)
	}
	if older.String() != "2025-09" {
		t.Errorf("Back(12) = %q, want 2025-09", older)
	}
	same, err := period.Back(0)
	if err != nil {
		t.Fatalf("Back(0) error = %v", err)
	}
	if same.String() != "2026-09" {
		t.Errorf("Back(0) = %q, want the same period", same)
	}
}

func TestParsePeriodRefusesLabelsOutsideTheShape(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		interval domain.Interval
		label    string
	}{
		{"empty", domain.IntervalDaily, ""},
		{"blank", domain.IntervalDaily, "   "},
		{"day label for a month", domain.IntervalDaily, "2026-09"},
		{"month label for a day", domain.IntervalMonthly, "2026-09-18"},
		{"unknown cadence", "weekly", "2026-09-18"},
		{"impossible day", domain.IntervalDaily, "2026-02-31"},
		{"impossible month", domain.IntervalMonthly, "2026-13"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := domain.ParsePeriod(testCase.interval, testCase.label); !errors.Is(err, domain.ErrInvalidPeriod) {
				t.Errorf("ParsePeriod() error = %v, want ErrInvalidPeriod", err)
			}
		})
	}
}

func TestPeriodPayloadRoundTrips(t *testing.T) {
	for _, interval := range domain.AllIntervals {
		period, err := domain.PeriodAt(interval, instant("2026-09-18T12:00:00Z"))
		if err != nil {
			t.Fatalf("PeriodAt() error = %v", err)
		}
		payload, err := domain.PeriodPayload(period)
		if err != nil {
			t.Fatalf("PeriodPayload() error = %v", err)
		}
		if err := domain.ValidatePayload(payload); err != nil {
			t.Errorf("the period payload is not queueable: %v", err)
		}
		decoded, err := domain.ParsePeriodPayload(interval, payload)
		if err != nil {
			t.Fatalf("ParsePeriodPayload() error = %v", err)
		}
		if decoded.String() != period.String() {
			t.Errorf("round trip = %q, want %q", decoded, period)
		}
	}
}

func TestParsePeriodPayloadRefusesAnythingElse(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		payload string
	}{
		{"empty", ""},
		{"not an object", "2026-09-18"},
		{"another key", `{"day":"2026-09-18"}`},
		{"extra key", `{"period":"2026-09-18","x":1}`},
		{"trailing content", `{"period":"2026-09-18"} `},
		{"escaped value", `{"period":"2026-09-1\u0038"}`},
		{"empty value", `{"period":""}`},
		{"wrong shape", `{"period":"18/09/2026"}`},
		{"nested", `{"period":{"label":"2026-09-18"}}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := domain.ParsePeriodPayload(domain.IntervalDaily, []byte(testCase.payload)); !errors.Is(err, domain.ErrInvalidPeriod) {
				t.Errorf("ParsePeriodPayload() error = %v, want ErrInvalidPeriod", err)
			}
		})
	}
	// The payload of a month is not the payload of a day.
	if _, err := domain.ParsePeriodPayload(domain.IntervalMonthly, []byte(`{"period":"2026-09-18"}`)); !errors.Is(err, domain.ErrInvalidPeriod) {
		t.Error("a day label was accepted as a month period")
	}
}

func TestPeriodPayloadRefusesAZeroPeriod(t *testing.T) {
	if _, err := domain.PeriodPayload(domain.Period{}); !errors.Is(err, domain.ErrInvalidPeriod) {
		t.Errorf("PeriodPayload(zero) error = %v, want ErrInvalidPeriod", err)
	}
	var zero domain.Period
	if !zero.IsZero() || zero.String() != "" {
		t.Errorf("the zero period claims to be %q", zero)
	}
	if _, err := zero.Start(); !errors.Is(err, domain.ErrInvalidPeriod) {
		t.Errorf("Start() on the zero period error = %v, want ErrInvalidPeriod", err)
	}
	if _, err := zero.Previous(); !errors.Is(err, domain.ErrInvalidPeriod) {
		t.Errorf("Previous() on the zero period error = %v, want ErrInvalidPeriod", err)
	}
}

func TestPeriodStartIsTheNominalInstant(t *testing.T) {
	period, err := domain.ParsePeriod(domain.IntervalMonthly, "2026-09")
	if err != nil {
		t.Fatalf("ParsePeriod() error = %v", err)
	}
	start, err := period.Start()
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !start.Equal(instant("2026-09-01T00:00:00Z")) {
		t.Errorf("start = %s, want the first instant of the month", start)
	}
	if strings.Contains(start.String(), "2026-09-18") {
		t.Error("the start of the period drifted to another day")
	}
}
