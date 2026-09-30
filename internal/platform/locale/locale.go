// Package locale is the presentation-side locale negotiation of Goyim
// Arena (P02-T08), implementing the mandatory precedence of
// I18N_STANDARD.md §4:
//
//  1. explicit route/action locale (not applicable at this stage);
//  2. authenticated profile preference;
//  3. validated interface cookie;
//  4. Accept-Language negotiated against the allowlist;
//  5. configured default (pt-BR).
//
// Unknown values are never reflected to clients: every inbound value is
// canonicalized as BCP 47 and checked against the catalog allowlist, and a
// miss falls through to the next source. The resolved locale enters the
// request context only in the presentation layer — domain and application
// never import this package (the architecture test enforces the
// cross-module rule).
package locale

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

// Tag is a canonical BCP 47 interface locale tag. Only tags in the catalog
// allowlist (i18n.SupportedLocales) resolve; anything else falls back.
type Tag string

// The supported interface locales of the product.
const (
	BrazilianPortuguese Tag = "pt-BR"
	AmericanEnglish     Tag = "en-US"
)

// maxTagLength bounds parsed tags; real language tags stay far below this
// (RFC 5646 recommends at most 35 characters), and the cap keeps hostile
// input cheap to reject.
const maxTagLength = 35

// ErrInvalidTag reports a value that is not a well-formed BCP 47 language
// tag (or uses tag features this product deliberately does not allowlist,
// such as private-use sequences and grandfathered tags).
var ErrInvalidTag = errors.New("locale: invalid BCP 47 tag")

var supportedOnce sync.Once

var supportedLocales map[string]bool

// supported returns the allowlist derived from the generated catalog, so a
// locale is negotiable exactly when its messages exist.
func supported() map[string]bool {
	supportedOnce.Do(func() {
		supportedLocales = make(map[string]bool)
		for _, name := range i18n.SupportedLocales() {
			supportedLocales[name] = true
		}
	})
	return supportedLocales
}

// Default is the configured product default per I18N_STANDARD.md (pt-BR,
// never inferred from IP or geographic data).
func Default() Tag {
	return BrazilianPortuguese
}

// IsSupported reports whether the tag is in the catalog allowlist.
func IsSupported(tag Tag) bool {
	return supported()[string(tag)]
}

// CacheKey renders the stable cache key of a supported locale. Public
// caching varies only by this controlled key — never by a raw
// Accept-Language header (I18N_STANDARD.md §4).
func CacheKey(tag Tag) string {
	return "v1:" + string(tag)
}

// ParseBCP47 canonicalizes a candidate language tag. It accepts the tag
// shape the product needs — language, optional script and optional region —
// and rejects anything else: empty, oversized, non-ASCII, wildcard,
// private-use (x-...) and grandfathered tags, malformed subtags and
// malformed regions. The returned tag is canonical (canonical case:
// pt-br → pt-BR), so equal inputs always negotiate identically.
func ParseBCP47(value string) (Tag, error) {
	if value == "" {
		return "", fmt.Errorf("%w: empty value", ErrInvalidTag)
	}
	if len(value) > maxTagLength {
		return "", fmt.Errorf("%w: longer than %d characters", ErrInvalidTag, maxTagLength)
	}
	for _, character := range value {
		if character == 0 || character > 127 {
			return "", fmt.Errorf("%w: non-ASCII or control character", ErrInvalidTag)
		}
	}
	if value == "*" {
		return "", fmt.Errorf("%w: wildcard tag", ErrInvalidTag)
	}

	subtags := strings.Split(value, "-")
	if len(subtags) > 3 {
		return "", fmt.Errorf("%w: variants and extensions are not allowed", ErrInvalidTag)
	}

	language := strings.ToLower(subtags[0])
	if len(language) < 2 || len(language) > 8 || !isAlphabetic(language) {
		return "", fmt.Errorf("%w: language subtag %q", ErrInvalidTag, subtags[0])
	}
	if language == "x" {
		return "", fmt.Errorf("%w: private-use tags are not allowed", ErrInvalidTag)
	}

	canonical := language

	// Subtag 1: script (4 letters) or region (2 letters, 3 letters or
	// UN M49 digits), matched case-insensitively and emitted in canonical
	// case.
	if len(subtags) > 1 {
		subtag := strings.ToLower(subtags[1])
		switch {
		case len(subtag) == 4 && isAlphabetic(subtag): // script
			canonical += "-" + strings.ToUpper(subtag[:1]) + subtag[1:]
		case len(subtag) == 2 && isAlphabetic(subtag): // region
			canonical += "-" + strings.ToUpper(subtag)
		case len(subtag) == 3 && isAlphabetic(subtag): // region
			canonical += "-" + strings.ToUpper(subtag)
		case len(subtag) == 3 && isNumeric(subtag): // UN M49 region
			canonical += "-" + subtag
		default:
			return "", fmt.Errorf("%w: script or region subtag %q", ErrInvalidTag, subtags[1])
		}
	}

	// Subtag 2: region, allowed only when subtag 1 was a script.
	if len(subtags) > 2 {
		first := strings.ToLower(subtags[1])
		if len(first) != 4 || !isAlphabetic(first) {
			return "", fmt.Errorf("%w: unexpected subtag %q", ErrInvalidTag, subtags[2])
		}
		subtag := strings.ToLower(subtags[2])
		switch {
		case len(subtag) == 2 && isAlphabetic(subtag):
			canonical += "-" + strings.ToUpper(subtag)
		case len(subtag) == 3 && isAlphabetic(subtag):
			canonical += "-" + strings.ToUpper(subtag)
		case len(subtag) == 3 && isNumeric(subtag):
			canonical += "-" + subtag
		default:
			return "", fmt.Errorf("%w: region subtag %q", ErrInvalidTag, subtags[2])
		}
	}

	return Tag(canonical), nil
}

func isAlphabetic(value string) bool {
	for _, character := range value {
		if character < 'a' || character > 'z' {
			return false
		}
	}
	return len(value) > 0
}

func isNumeric(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return len(value) > 0
}

// contextKey is the unexported context key of the resolved locale.
type contextKey struct{}

// WithLocale stores the resolved locale in the context.
func WithLocale(parent context.Context, tag Tag) context.Context {
	return context.WithValue(parent, contextKey{}, tag)
}

// FromContext extracts the resolved locale from the context; outside the
// presentation middleware it returns the product default, so handlers and
// templates never render with an empty locale.
func FromContext(ctx context.Context) Tag {
	if ctx == nil {
		return Default()
	}
	if tag, ok := ctx.Value(contextKey{}).(Tag); ok && tag != "" {
		return tag
	}
	return Default()
}

// String renders the canonical tag.
func (tag Tag) String() string { return string(tag) }

// parseQuality parses a quality weight per RFC 7231 (qvalue: "0", "1" or
// "0." / "1." followed by up to three digits). Malformed or out-of-range
// values are rejected so hostile weights cannot reorder negotiation.
func parseQuality(raw string) (float64, bool) {
	if len(raw) == 0 || len(raw) > 5 {
		return 0, false
	}
	whole := raw[:1]
	if whole != "0" && whole != "1" {
		return 0, false
	}
	if len(raw) == 1 {
		return float64(whole[0] - '0'), true
	}
	if raw[1] != '.' {
		return 0, false
	}
	fraction := raw[2:]
	if fraction == "" {
		return float64(whole[0] - '0'), true
	}
	for _, character := range fraction {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
