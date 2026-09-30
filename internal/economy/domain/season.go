package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxSeasonKeyRunes bounds the season book key: long enough for an
// operator-chosen book identifier, short enough to stay out of log
// and index abuse.
const maxSeasonKeyRunes = 128

// CompatSeasonKey is the explicitly inactive namespace for
// pre-season technical data. It binds legacy rows, carries no
// lifecycle event and is never activated: new books name their own
// key at the port.
const CompatSeasonKey = "compat-legacy"

// SeasonKey is the identity of one season book (INK@season_id).
// Every Genesis, transfer, intention, hold, balance and
// reconciliation names its book; an absent season is refused before
// any leg is read or written.
type SeasonKey string

// ParseSeasonKey validates a season book key. Empty, blank,
// overlong and control-carrying keys are refused before they can
// name a book.
func ParseSeasonKey(raw string) (SeasonKey, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrMissingSeason
	}
	if utf8.RuneCountInString(raw) > maxSeasonKeyRunes {
		return "", ErrMissingSeason
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrMissingSeason
		}
	}
	return SeasonKey(raw), nil
}

// String returns the stored season key value.
func (k SeasonKey) String() string {
	return string(k)
}
