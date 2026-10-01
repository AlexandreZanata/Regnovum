package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxSeasonKeyRunes bounds the purchase book key: long enough for an
// operator-chosen book identifier, short enough to stay out of log
// and index abuse.
const maxSeasonKeyRunes = 128

// CompatSeasonKey is the explicitly inactive namespace for
// pre-season purchase rows. It carries no lifecycle event and is
// never activated: new books name their own key at the port.
const CompatSeasonKey = "compat-legacy"

// SeasonalResetSeconds pins the ninety-day book for the purchase
// family: 90 × 24 × 60 × 60 in UTC. It mirrors the economy calendar
// without importing it: domains stay disjoint by architecture.
const SeasonalResetSeconds = 7776000

// SeasonKey is the identity of one purchase book. Every quote,
// intent and settlement names its book; an absent season is refused
// before any row is read or written.
type SeasonKey string

// ParseSeasonKey validates a purchase book key. Empty, blank,
// overlong and control-carrying keys are refused before they can
// name a book.
func ParseSeasonKey(raw string) (SeasonKey, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrInvalidQuote
	}
	if utf8.RuneCountInString(raw) > maxSeasonKeyRunes {
		return "", ErrInvalidQuote
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidQuote
		}
	}
	return SeasonKey(raw), nil
}

// String returns the stored season key value.
func (k SeasonKey) String() string {
	return string(k)
}

// RequireSeasonalReset refuses a seasonal quotation without an
// explicit reset acknowledgement: an omitted reset never authorizes
// it. The compat-legacy book keeps its legacy contract without one.
func RequireSeasonalReset(season SeasonKey, resetAcknowledged bool) error {
	if season == SeasonKey(CompatSeasonKey) {
		return nil
	}
	if !resetAcknowledged {
		return ErrInvalidQuote
	}
	return nil
}

// CapExpiryBySeasonEnd caps a quotation lifetime by the book end:
// the settlement deadline never passes ends_at. A zero ends_at
// disables the cap for the legacy path only; seasonal books always
// carry a real end.
func CapExpiryBySeasonEnd(accepted, ttlExpiry time.Time, season SeasonKey, endsAt time.Time) time.Time {
	if season == SeasonKey(CompatSeasonKey) || endsAt.IsZero() {
		return ttlExpiry
	}
	if ttlExpiry.After(endsAt) {
		return endsAt.UTC()
	}
	return ttlExpiry
}
