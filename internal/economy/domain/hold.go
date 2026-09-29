package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxHoldPurposeRunes bounds the hold purpose: long enough for a human
// reason, short enough to stay out of log and index abuse.
const maxHoldPurposeRunes = 280

// HoldPurpose names why value is locked. It travels verbatim into the
// journal-adjacent record, so blank, overlong and control-carrying
// purposes are refused before they can name a hold.
type HoldPurpose string

// ParseHoldPurpose validates a hold purpose.
func ParseHoldPurpose(raw string) (HoldPurpose, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrInvalidHold
	}
	if utf8.RuneCountInString(raw) > maxHoldPurposeRunes {
		return "", ErrInvalidHold
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidHold
		}
	}
	return HoldPurpose(raw), nil
}

// String returns the stored purpose.
func (p HoldPurpose) String() string {
	return string(p)
}
