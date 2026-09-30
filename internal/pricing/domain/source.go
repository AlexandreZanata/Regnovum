package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxSourceRunes bounds a source slug: long enough for a provider name,
// short enough to stay out of log and index abuse.
const maxSourceRunes = 64

// SourceID names one approved rate source: the provenance anchor every
// observation carries. Slugs are lowercase ASCII (`a-z`, `0-9`, `-`)
// so two spellings can never name one source. No live provider is
// approved here: registration happens per deployment in the adapter,
// and the slug never carries a URL, a key or a secret.
type SourceID string

// ParseSourceID validates a source slug. Matching is exact:
// surrounding whitespace is not trimmed.
func ParseSourceID(raw string) (SourceID, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidSource
	}
	if utf8.RuneCountInString(raw) > maxSourceRunes {
		return "", ErrInvalidSource
	}
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return "", ErrInvalidSource
		}
		if unicode.IsControl(r) {
			return "", ErrInvalidSource
		}
	}
	return SourceID(raw), nil
}

// String returns the stored source slug.
func (s SourceID) String() string { return string(s) }
