package domain

import (
	"fmt"
	"time"
)

// Epoch is one weekly accounting period: the ISO 8601 year and week
// in UTC. The window is [Start, End): Monday 00:00 UTC inclusive to
// the next Monday 00:00 UTC exclusive. Local zones and their DST
// never enter the calendar: only the absolute instant decides.
type Epoch struct {
	Year int
	Week int
}

// EpochOf resolves the epoch holding one instant. The instant travels
// in any zone; only its absolute UTC value counts.
func EpochOf(instant time.Time) Epoch {
	year, week := instant.UTC().ISOWeek()
	return Epoch{Year: year, Week: week}
}

// mondayOfWeekOne returns the Monday 00:00 UTC opening ISO week 1:
// the Monday of the week holding January 4th, which always belongs
// to week 1.
func mondayOfWeekOne(year int) time.Time {
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	// Sunday reads 0: shift to Monday-first 0..6.
	offset := (int(jan4.Weekday()) + 6) % 7
	return jan4.AddDate(0, 0, -offset)
}

// HasWeek53 reports whether one ISO year holds a 53rd week: exactly
// when December 28th still reads week 53.
func HasWeek53(year int) bool {
	_, week := time.Date(year, time.December, 28, 0, 0, 0, 0, time.UTC).ISOWeek()
	return week == 53
}

// validate guards the closed calendar: positive years and weeks the
// year holds. W53 in a 52-week year refuses before anything seals.
func (e Epoch) validate() error {
	if e.Year < 1 || e.Year > 9999 || e.Week < 1 {
		return ErrInvalidEpoch
	}
	if e.Week > 52 && !HasWeek53(e.Year) {
		return ErrInvalidEpoch
	}
	if e.Week > 53 {
		return ErrInvalidEpoch
	}
	return nil
}

// Start returns the inclusive window opening: Monday 00:00 UTC.
func (e Epoch) Start() (time.Time, error) {
	if err := e.validate(); err != nil {
		return time.Time{}, err
	}
	return mondayOfWeekOne(e.Year).AddDate(0, 0, (e.Week-1)*7), nil
}

// End returns the exclusive window closing: the next Monday 00:00
// UTC. At the exact closing tick the epoch already expired.
func (e Epoch) End() (time.Time, error) {
	start, err := e.Start()
	if err != nil {
		return time.Time{}, err
	}
	return start.AddDate(0, 0, 7), nil
}

// Contains reports whether one instant settles inside the window:
// [Start, End). The tick before End belongs here; End itself belongs
// to the next epoch.
func (e Epoch) Contains(instant time.Time) bool {
	start, err := e.Start()
	if err != nil {
		return false
	}
	end, err := e.End()
	if err != nil {
		return false
	}
	utc := instant.UTC()
	return !utc.Before(start) && utc.Before(end)
}

// Complete reports whether one epoch may seal at the given instant:
// only whole weeks integrate R4, so sealing needs now at or past
// the exclusive end. Incomplete and current weeks never count.
func (e Epoch) Complete(now time.Time) bool {
	end, err := e.End()
	if err != nil {
		return false
	}
	return !now.UTC().Before(end)
}

// AssignEpoch classifies one posting by its database clock: the
// posted_at read inside the committing transaction decides, never
// the moment the transaction began. A transaction started before the
// seal and committed after it lands in the next epoch; the seal
// barrier orders in-flight work by commit visibility, and late events
// never reopen a sealed week without a current corrective entry.
func AssignEpoch(postedAt time.Time) Epoch {
	return EpochOf(postedAt)
}

// Key renders the canonical epoch label: "2026-W53".
func (e Epoch) Key() (string, error) {
	if err := e.validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d-W%02d", e.Year, e.Week), nil
}

// ParseEpochKey validates a canonical label against the closed
// calendar. Matching is exact: no trimming, no case folding, no
// bare week numbers. Unknown labels refuse before any seal.
func ParseEpochKey(raw string) (Epoch, error) {
	var year, week int
	if len(raw) != 8 || raw[4] != '-' || raw[5] != 'W' {
		return Epoch{}, ErrInvalidEpoch
	}
	for _, c := range raw[:4] {
		if c < '0' || c > '9' {
			return Epoch{}, ErrInvalidEpoch
		}
	}
	for _, c := range raw[6:] {
		if c < '0' || c > '9' {
			return Epoch{}, ErrInvalidEpoch
		}
	}
	year = int(raw[0]-'0')*1000 + int(raw[1]-'0')*100 + int(raw[2]-'0')*10 + int(raw[3]-'0')
	week = int(raw[6]-'0')*10 + int(raw[7]-'0')
	epoch := Epoch{Year: year, Week: week}
	if err := epoch.validate(); err != nil {
		return Epoch{}, err
	}
	return epoch, nil
}

// JobKey renders the idempotency token of the epoch sealing job:
// one key per epoch, so a late or retried job replays the same seal
// instead of opening a second one.
func (e Epoch) JobKey() (string, error) {
	key, err := e.Key()
	if err != nil {
		return "", err
	}
	return "crumbs-seal-" + key, nil
}

// String returns the canonical epoch label, or "invalid" when the
// epoch cannot exist.
func (e Epoch) String() string {
	key, err := e.Key()
	if err != nil {
		return "invalid"
	}
	return key
}
