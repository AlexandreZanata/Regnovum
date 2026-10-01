package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ArchiveReceipt is the sealed proof one book carries into its
// successor opening: the book, its manifesto digest, the barrier
// cutoff, the seal instant and the conservation snapshot. It travels
// as values so the opener reads the database once and judges purely.
type ArchiveReceipt struct {
	Season       string
	ManifestHash string
	CutoffAt     time.Time
	SealedAt     time.Time
	Milli        int64
	Legs         int64
	Intentions   int64
}

// Valid checks the receipt shape: a named book, a 64-hex digest, two
// live instants and non-negative counters. Money rules are judged by
// the conservation check, never here.
func (r ArchiveReceipt) Valid() error {
	if _, err := parseSeasonToken(r.Season); err != nil {
		return err
	}
	if len(r.ManifestHash) != 64 {
		return ErrInvalidSeason
	}
	for _, c := range r.ManifestHash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ErrInvalidSeason
		}
	}
	if r.CutoffAt.IsZero() || r.SealedAt.IsZero() {
		return ErrInvalidSeason
	}
	if r.Milli < 0 || r.Legs < 0 || r.Intentions < 0 {
		return ErrInvalidSeason
	}
	return nil
}

// VerifyArchiveManifest refuses a manifesto whose seal no longer
// agrees: an altered charter, policy, monarch or window never opens a
// successor.
func VerifyArchiveManifest(manifest Manifest) error {
	return manifest.VerifyManifestHash()
}

// VerifyConservationForOpening judges the seal for opening: the book
// Genesis S reads byte-equal and legs and intentions never rewound.
// Growth past the barrier snapshot is allowed: admitted in-flight work
// completed once in the old book. A diverged S or a rewound history
// refuses with custody preserved and no successor.
func VerifyConservationForOpening(snapshot, current SealSnapshot) error {
	return CanSeal(0, 0, snapshot, current)
}

// VerifySuccessorContinuity checks the calendar link between one sealed
// book and its planned successor: the next ordinal, a fresh id and the
// exact predecessor end as start. Gaps refuse: downtime never
// auto-fills lapsed seasons and never stretches the calendar.
func VerifySuccessorContinuity(previous Season, req ManifestRequest) (Manifest, error) {
	next, err := PlanNext(previous, req)
	if err != nil {
		return Manifest{}, err
	}
	return next.Manifest, nil
}

// VerifyFreshBook refuses carry-over into a new book: the Treasury
// holds exactly S and every other custody holds zero. A winner's
// balance never seeds the next economy; prestige stays history.
func VerifyFreshBook(treasuryMilli, otherMilli, expectedMilli int64) error {
	if expectedMilli <= 0 {
		return ErrInvalidSeason
	}
	if treasuryMilli != expectedMilli {
		return ErrInvalidSeason
	}
	if otherMilli != 0 {
		return ErrInvalidSeason
	}
	return nil
}

// ParseArchiveDetail validates one short opaque archive cause: exact
// match, no control characters, bounded length. It keeps failure
// records out of log abuse.
func ParseArchiveDetail(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidSeason
	}
	if utf8.RuneCountInString(raw) > maxSeasonRunes {
		return "", ErrInvalidSeason
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidSeason
		}
	}
	return raw, nil
}
