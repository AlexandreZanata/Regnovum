package domain

import (
	"strings"
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

// SeasonKey is the identity of one purchase book. Every intent and
// settlement names its book; an absent season is refused before any
// row is read or written.
type SeasonKey string

// ParseSeasonKey validates a purchase book key. Empty, blank,
// overlong and control-carrying keys are refused before they can
// name a book.
func ParseSeasonKey(raw string) (SeasonKey, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrInvalidSeason
	}
	if utf8.RuneCountInString(raw) > maxSeasonKeyRunes {
		return "", ErrInvalidSeason
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidSeason
		}
	}
	return SeasonKey(raw), nil
}

// String returns the stored season key value.
func (k SeasonKey) String() string {
	return string(k)
}

// RequireSeasonalReset refuses a seasonal purchase without an
// explicit reset acknowledgement: an omitted reset never authorizes
// it. The compat-legacy book keeps its legacy contract without one.
func RequireSeasonalReset(season SeasonKey, resetAcknowledged bool) error {
	if season == SeasonKey(CompatSeasonKey) {
		return nil
	}
	if !resetAcknowledged {
		return ErrInvalidSeason
	}
	return nil
}
