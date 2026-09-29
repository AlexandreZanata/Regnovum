package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

// measuredHashVersion prefixes the canonical hash so the algorithm
// can evolve without invalidating stored rows.
const measuredHashVersion = "v1"

// measuredHashDomain separates metering content hashes from any other
// hash computed with the same digest.
const measuredHashDomain = "goyim-arena/metering/content/v1"

// GraphemeCounter counts extended grapheme clusters (Unicode UAX #29)
// of a string. The domain stays standard-library only (AGENTS.md):
// the concrete counter lives in internal/platform/text and is
// injected by the caller, per ADR-013.
type GraphemeCounter func(value string) int

// MeasuredHash is the versioned canonical hash of measured content.
// It is stable across executions: the same normalized content always
// produces the same value.
type MeasuredHash struct {
	value string
}

// HashMeasuredContent returns the versioned canonical hash of already
// normalized content: SHA-256 over a domain-separated payload,
// rendered as "v1:<64 hex chars>".
func HashMeasuredContent(normalized string) MeasuredHash {
	sum := sha256.Sum256([]byte(measuredHashDomain + "\x00" + normalized))
	return MeasuredHash{value: measuredHashVersion + ":" + hex.EncodeToString(sum[:])}
}

// ParseMeasuredHash validates and constructs a MeasuredHash from its
// stored canonical form.
func ParseMeasuredHash(raw string) (MeasuredHash, error) {
	trimmed := strings.TrimSpace(raw)
	prefix := measuredHashVersion + ":"
	if !strings.HasPrefix(trimmed, prefix) {
		return MeasuredHash{}, ErrInvalidMeasuredHash
	}
	digest := trimmed[len(prefix):]
	if len(digest) != sha256.Size*2 {
		return MeasuredHash{}, ErrInvalidMeasuredHash
	}
	for i := 0; i < len(digest); i++ {
		c := digest[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return MeasuredHash{}, ErrInvalidMeasuredHash
		}
	}
	return MeasuredHash{value: trimmed}, nil
}

// String returns the canonical hash value.
func (h MeasuredHash) String() string { return h.value }

// IsZero reports whether the MeasuredHash is the uninitialized zero value.
func (h MeasuredHash) IsZero() bool { return h.value == "" }

// Equals reports whether two hashes are identical.
func (h MeasuredHash) Equals(other MeasuredHash) bool { return h.value == other.value }

// MeasuredContent is the immutable canonical text of one publication
// candidate together with its grapheme units and canonical hash. It
// is normalized once, at construction: newlines become "\n",
// surrounding whitespace is trimmed, and the units are counted over
// the normalized text by the injected UAX #29 counter.
//
// Only the final validated content is measured: keystrokes, drafts
// and previews never enter this shape. The hash binds the later
// quote to these exact bytes: edited text hashes differently and
// never reuses the earlier quote.
type MeasuredContent struct {
	value string
	units int
	hash  MeasuredHash
}

// ParseMeasuredContent validates, normalizes and measures candidate
// content. The grapheme budget arrives per call and the counter is
// injected: no ratified limit and no counter live here.
func ParseMeasuredContent(raw string, counter GraphemeCounter, maxUnits int) (MeasuredContent, error) {
	if counter == nil {
		return MeasuredContent{}, ErrMissingMeterCounter
	}
	if maxUnits <= 0 {
		return MeasuredContent{}, ErrInvalidMeasuredLimit
	}
	if !utf8.ValidString(raw) {
		return MeasuredContent{}, ErrInvalidMeasuredContent
	}
	normalized := normalizeMeasured(raw)
	if normalized == "" {
		return MeasuredContent{}, ErrEmptyMeasuredContent
	}
	if containsUnsupportedMeasuredRunes(normalized) {
		return MeasuredContent{}, ErrInvalidMeasuredContent
	}
	units := counter(normalized)
	if units < 1 {
		return MeasuredContent{}, ErrEmptyMeasuredContent
	}
	if units > maxUnits {
		return MeasuredContent{}, ErrMeasuredContentTooLong
	}
	return MeasuredContent{value: normalized, units: units, hash: HashMeasuredContent(normalized)}, nil
}

// ReconstituteMeasured rebuilds measured content, validating the
// stored units range and the recorded hash against a fresh canonical
// computation. Adapters use it to map rows; the grapheme counter is
// not needed because the units were measured at quote time.
func ReconstituteMeasured(value string, units int, hash MeasuredHash, maxUnits int) (MeasuredContent, error) {
	if maxUnits <= 0 {
		return MeasuredContent{}, ErrInvalidMeasuredLimit
	}
	if value == "" || strings.TrimSpace(value) == "" {
		return MeasuredContent{}, ErrEmptyMeasuredContent
	}
	if units < 1 || units > maxUnits {
		return MeasuredContent{}, ErrMeasuredContentTooLong
	}
	if hash.IsZero() {
		return MeasuredContent{}, ErrInvalidMeasuredHash
	}
	if !HashMeasuredContent(value).Equals(hash) {
		return MeasuredContent{}, ErrMeasuredContentHashMismatch
	}
	return MeasuredContent{value: value, units: units, hash: hash}, nil
}

// String returns the normalized canonical text.
func (c MeasuredContent) String() string { return c.value }

// Units returns the UAX #29 cluster count of the normalized text:
// the billing unit of the T01 price catalog.
func (c MeasuredContent) Units() int { return c.units }

// Hash returns the versioned canonical hash.
func (c MeasuredContent) Hash() MeasuredHash { return c.hash }

// IsZero reports whether the MeasuredContent is the uninitialized
// zero value.
func (c MeasuredContent) IsZero() bool { return c.value == "" }

// Equals reports whether two contents are the same normalized text.
func (c MeasuredContent) Equals(other MeasuredContent) bool { return c.value == other.value }

// VerifyQuote refuses content edited after its quote: only the exact
// quoted bytes pass. A single added, removed or reordered byte is a
// new intention with a new measurement and a new quote.
func (c MeasuredContent) VerifyQuote(quoted MeasuredHash) error {
	if quoted.IsZero() || !c.hash.Equals(quoted) {
		return ErrMeasuredContentHashMismatch
	}
	return nil
}

// normalizeMeasured canonicalizes line breaks and trims surrounding
// whitespace; inner text is preserved exactly.
func normalizeMeasured(raw string) string {
	replaced := strings.ReplaceAll(raw, "\r\n", "\n")
	replaced = strings.ReplaceAll(replaced, "\r", "\n")
	return strings.TrimSpace(replaced)
}

// containsUnsupportedMeasuredRunes reports whether the text carries
// control characters (newline excepted) or bidirectional overrides
// that could reorder displayed content.
func containsUnsupportedMeasuredRunes(value string) bool {
	for _, r := range value {
		if isMeasuredBidiOverride(r) {
			return true
		}
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// isMeasuredBidiOverride reports whether the rune is a bidirectional
// control.
func isMeasuredBidiOverride(r rune) bool {
	switch r {
	case '\u200E', '\u200F', '\u061C',
		'\u202A', '\u202B', '\u202C', '\u202D', '\u202E',
		'\u2066', '\u2067', '\u2068', '\u2069':
		return true
	default:
		return false
	}
}
