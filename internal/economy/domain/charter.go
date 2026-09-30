package domain

import "regexp"

// charterVersionPattern pins the version vocabulary: v followed by a
// positive integer, ASCII only. Locale-specific digits can never name a
// version, so pt and en holders always mean the same charter.
var charterVersionPattern = regexp.MustCompile(`^v[1-9][0-9]*$`)

// CharterVersion is one versioned Charter both holder and service cite.
// Acceptance never carries across versions silently: a new version needs
// a new verdict.
type CharterVersion string

// ParseCharterVersion validates a charter version token.
func ParseCharterVersion(raw string) (CharterVersion, error) {
	if !charterVersionPattern.MatchString(raw) {
		return "", ErrInvalidCharter
	}
	return CharterVersion(raw), nil
}

// String returns the stored version token.
func (v CharterVersion) String() string {
	return string(v)
}

// ConsentDecision is the closed verdict vocabulary: acceptance unlocks
// new activities under the cited version, refusal preserves history,
// export, recourse and settlement of earlier rights.
type ConsentDecision string

const (
	// ConsentAccepted records an express acceptance.
	ConsentAccepted ConsentDecision = "accepted"
	// ConsentRefused records an express refusal.
	ConsentRefused ConsentDecision = "refused"
)

// ParseConsentDecision validates a verdict token.
func ParseConsentDecision(raw string) (ConsentDecision, error) {
	switch ConsentDecision(raw) {
	case ConsentAccepted, ConsentRefused:
		return ConsentDecision(raw), nil
	default:
		return "", ErrInvalidCharter
	}
}

// String returns the stored verdict token.
func (d ConsentDecision) String() string {
	return string(d)
}

// PreservedRights names what the holder keeps under a verdict, derived
// from the decision itself rather than stored: refusal changes nothing
// about earlier rights, and acceptance adds new activities without
// touching them either.
func PreservedRights(decision ConsentDecision) []string {
	preserved := []string{"history", "export", "recourse", "settlement"}
	if decision == ConsentAccepted {
		return append(preserved, "new-activities")
	}
	return preserved
}

// ConversionRate is the exact rational price of one legacy INK unit in
// milliINK: Num per Den, both positive. Ratios never round: terms
// recorded here are matched verbatim at conversion time, and no rate is
// approved until the titular ratifies one, so every rate travels
// explicitly with its opt-in.
type ConversionRate struct {
	Num int64
	Den int64
}

// ParseConversionRate validates a conversion rate pair.
func ParseConversionRate(num, den int64) (ConversionRate, error) {
	if num <= 0 || den <= 0 {
		return ConversionRate{}, ErrInvalidCharter
	}
	return ConversionRate{Num: num, Den: den}, nil
}

// Equals reports whether two rates are exactly the same pair.
func (r ConversionRate) Equals(other ConversionRate) bool {
	return r.Num == other.Num && r.Den == other.Den
}
