package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Interval is the closed vocabulary of maintenance cadences.
//
// The vocabulary is deliberately small. A cadence is a product decision about
// how often the platform settles grants, expires passes, runs retention or
// cleans sessions; anything finer than a day would also make the period in the
// payload carry an hour, and the recovery arithmetic of a schedule nobody uses
// is a liability rather than a feature.
type Interval string

const (
	// IntervalDaily runs once per UTC calendar day.
	IntervalDaily Interval = "daily"
	// IntervalMonthly runs once per UTC calendar month.
	IntervalMonthly Interval = "monthly"
)

// AllIntervals lists the cadences in a deterministic order.
var AllIntervals = []Interval{IntervalDaily, IntervalMonthly}

// IsValid reports whether the cadence belongs to the closed vocabulary.
func (i Interval) IsValid() bool {
	for _, known := range AllIntervals {
		if i == known {
			return true
		}
	}
	return false
}

// String returns the wire value of the cadence.
func (i Interval) String() string { return string(i) }

// Period is one cadence slot, identified by its canonical UTC label:
// "2026-09-18" for a day and "2026-09" for a month.
//
// A period is a value and not an instant on purpose. It is the identity of the
// work — the monthly grant of September, the retention pass of the 18th — so
// it survives a restart, a redeployment and an hour of clock skew, and it is
// what makes a missed period recoverable: the same slot can be run later and
// still be the same slot.
type Period struct {
	interval Interval
	label    string
}

// ErrInvalidPeriod reports a label that is not in the canonical shape of its
// cadence, or a cadence outside the vocabulary.
var ErrInvalidPeriod = errors.New("jobs: invalid period")

// PeriodAt returns the period that contains the instant, in UTC.
func PeriodAt(interval Interval, instant time.Time) (Period, error) {
	if !interval.IsValid() {
		return Period{}, ErrInvalidPeriod
	}
	utc := instant.UTC()
	switch interval {
	case IntervalMonthly:
		return Period{interval: interval, label: fmt.Sprintf("%04d-%02d", utc.Year(), int(utc.Month()))}, nil
	case IntervalDaily:
		return Period{interval: interval, label: utc.Format("2006-01-02")}, nil
	default:
		return Period{}, ErrInvalidPeriod
	}
}

// ParsePeriod reads a canonical label back into a period of the cadence.
func ParsePeriod(interval Interval, label string) (Period, error) {
	if !interval.IsValid() {
		return Period{}, ErrInvalidPeriod
	}
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return Period{}, ErrInvalidPeriod
	}
	parsed, err := startOf(interval, trimmed)
	if err != nil {
		return Period{}, err
	}
	return PeriodAt(interval, parsed)
}

// Interval returns the cadence the period belongs to.
func (p Period) Interval() Interval { return p.interval }

// String returns the canonical label.
func (p Period) String() string { return p.label }

// IsZero reports whether the period is the uninitialized value.
func (p Period) IsZero() bool { return p.label == "" || p.interval == "" }

// Start returns the instant the period begins, in UTC. It is the reference a
// lag measurement is taken against.
func (p Period) Start() (time.Time, error) {
	if p.IsZero() {
		return time.Time{}, ErrInvalidPeriod
	}
	return startOf(p.interval, p.label)
}

// Previous returns the period immediately before this one. Month arithmetic
// steps by year and month rather than subtracting a nominal thirty days, so
// December precedes January correctly and February is as short as it is.
func (p Period) Previous() (Period, error) {
	start, err := p.Start()
	if err != nil {
		return Period{}, err
	}
	switch p.interval {
	case IntervalMonthly:
		year, month := start.Year(), int(start.Month())-1
		if month == 0 {
			month = 12
			year--
		}
		if year < 1 {
			return Period{}, ErrInvalidPeriod
		}
		return Period{interval: p.interval, label: fmt.Sprintf("%04d-%02d", year, month)}, nil
	case IntervalDaily:
		previous := start.AddDate(0, 0, -1)
		return Period{interval: p.interval, label: previous.Format("2006-01-02")}, nil
	default:
		return Period{}, ErrInvalidPeriod
	}
}

// Back returns the given number of periods before this one. A non-positive
// count returns the period itself, which keeps the catch-up loop a plain
// repetition with no special case.
func (p Period) Back(steps int) (Period, error) {
	current := p
	for step := 0; step < steps; step++ {
		previous, err := current.Previous()
		if err != nil {
			return Period{}, err
		}
		current = previous
	}
	return current, nil
}

// startOf parses a canonical label into the instant the period begins.
func startOf(interval Interval, label string) (time.Time, error) {
	switch interval {
	case IntervalMonthly:
		parsed, err := time.Parse("2006-01", label)
		if err != nil {
			return time.Time{}, ErrInvalidPeriod
		}
		return parsed.UTC(), nil
	case IntervalDaily:
		parsed, err := time.Parse("2006-01-02", label)
		if err != nil {
			return time.Time{}, ErrInvalidPeriod
		}
		return parsed.UTC(), nil
	default:
		return time.Time{}, ErrInvalidPeriod
	}
}

// The payload of a scheduled run carries the period and nothing else. It is
// the whole contract between the scheduler and the maintenance handler, and it
// is why a recovered period runs the work of that period instead of the work
// of today.
const (
	payloadPrefix = `{"period":"`
	payloadSuffix = `"}`
	// MaxPeriodLabelLength bounds the label; a canonical one is far shorter.
	MaxPeriodLabelLength = 10
)

// PeriodPayload returns the canonical payload of a scheduled run of the period.
//
// The object is built rather than encoded because the domain stays standard
// library only, and it is safe to build because the only value it carries is a
// validated period label: the closed shape of the label (digits and hyphens) is
// what makes hand-built JSON total, with no character that JSON would escape.
func PeriodPayload(period Period) ([]byte, error) {
	if period.IsZero() || len(period.label) > MaxPeriodLabelLength {
		return nil, ErrInvalidPeriod
	}
	if _, err := startOf(period.interval, period.label); err != nil {
		return nil, err
	}
	return []byte(payloadPrefix + period.label + payloadSuffix), nil
}

// ParsePeriodPayload reads the payload of a scheduled run.
//
// The shape is checked structurally, not parsed: the payload has exactly one
// key, its value is a validated label, and no escapable character can appear
// in one, so an exact prefix and suffix check is both total and sufficient.
// Anything else — a new key, an escaped value, trailing content — is refused.
func ParsePeriodPayload(interval Interval, payload []byte) (Period, error) {
	text := string(payload)
	if !strings.HasPrefix(text, payloadPrefix) || !strings.HasSuffix(text, payloadSuffix) {
		return Period{}, ErrInvalidPeriod
	}
	if len(text) < len(payloadPrefix)+len(payloadSuffix) {
		return Period{}, ErrInvalidPeriod
	}
	label := text[len(payloadPrefix) : len(text)-len(payloadSuffix)]
	if strings.ContainsAny(label, `"\`) {
		return Period{}, ErrInvalidPeriod
	}
	return ParsePeriod(interval, label)
}
