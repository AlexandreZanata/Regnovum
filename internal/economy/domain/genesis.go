package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxGenesisKeyRunes bounds the creation event key: long enough for an
// operator-chosen identifier, short enough to stay out of log and index
// abuse.
const maxGenesisKeyRunes = 128

// GenesisKey is the idempotency key of the single creation event. Replays
// of the same key resolve to the original attestation; any other key is
// refused once Genesis happened.
type GenesisKey string

// ParseGenesisKey validates a creation event key. Empty, blank, overlong
// and control-carrying keys are refused before they can name an event.
func ParseGenesisKey(raw string) (GenesisKey, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrInvalidGenesisKey
	}
	if utf8.RuneCountInString(raw) > maxGenesisKeyRunes {
		return "", ErrInvalidGenesisKey
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidGenesisKey
		}
	}
	return GenesisKey(raw), nil
}

// String returns the stored key value.
func (k GenesisKey) String() string {
	return string(k)
}
