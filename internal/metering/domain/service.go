package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxServiceRunes bounds a service slug: long enough for a publication
// or institutional service name, short enough to stay out of log and
// index abuse.
const maxServiceRunes = 64

// ServiceID names one priced service: the publication or institutional
// service the price catalog charges for. Slugs are lowercase ASCII
// (`a-z`, `0-9`, `-`) so two spellings can never name one service.
// No service is approved here: entries arrive per deployment with
// their own validity and authority, and an unknown service simply
// has no price.
type ServiceID string

// ParseServiceID validates a service slug. Matching is exact:
// surrounding whitespace is not trimmed.
func ParseServiceID(raw string) (ServiceID, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidService
	}
	if utf8.RuneCountInString(raw) > maxServiceRunes {
		return "", ErrInvalidService
	}
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return "", ErrInvalidService
		}
		if unicode.IsControl(r) {
			return "", ErrInvalidService
		}
	}
	return ServiceID(raw), nil
}

// String returns the stored service slug.
func (s ServiceID) String() string { return string(s) }

// BillingUnit names the canonical Unicode unit one price charges for.
// The only unit in this version is the perceived character of
// docs/reino/PRECIFICACAO.md §1 (UAX #29 grapheme clusters, never
// bytes nor runes). Future units arrive by explicit task, never by
// inference from a translation or an example.
type BillingUnit string

const (
	// UnitGraphemeCluster charges one price step per UAX #29
	// grapheme cluster of the canonical final content.
	UnitGraphemeCluster BillingUnit = "grapheme-cluster"
)

// AllBillingUnits returns the closed vocabulary in canonical order.
func AllBillingUnits() []BillingUnit {
	return []BillingUnit{UnitGraphemeCluster}
}

// ParseBillingUnit validates a unit against the closed vocabulary.
// Matching is exact: surrounding whitespace is not trimmed and case
// is not folded. Unknown units refuse before any price is read.
func ParseBillingUnit(raw string) (BillingUnit, error) {
	unit := BillingUnit(raw)
	if !unit.IsValid() {
		return "", ErrUnknownUnit
	}
	return unit, nil
}

// IsValid reports whether the unit belongs to the closed vocabulary.
func (u BillingUnit) IsValid() bool {
	return u == UnitGraphemeCluster
}

// String returns the stored unit value.
func (u BillingUnit) String() string { return string(u) }

// maxAuthorityRunes bounds the approving authority name: long enough
// for a role or office name, short enough to stay out of log abuse.
const maxAuthorityRunes = 128

// ParseAuthority validates the approving authority recorded beside
// one price entry. Matching is exact: surrounding whitespace is not
// trimmed, control characters are refused, and the empty string
// never stands for an approval. The value only records who approved
// the entry supplied by the caller; it never ratifies anything by
// itself.
func ParseAuthority(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidAuthority
	}
	if utf8.RuneCountInString(raw) > maxAuthorityRunes {
		return "", ErrInvalidAuthority
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidAuthority
		}
	}
	return raw, nil
}
