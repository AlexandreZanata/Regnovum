package locale

import (
	"net/http"
	"sort"
	"strings"
)

// ProfileSource is the hook for the authenticated profile preference
// (precedence 2). It stays a narrow function so the identity module can
// supply it later without this package importing any module: at this stage
// no caller has an authenticated profile and nil means the source is
// absent.
type ProfileSource func(request *http.Request) (Tag, bool)

// Resolver negotiates the interface locale of a request following the
// mandatory precedence of I18N_STANDARD.md §4. It is immutable after
// construction and safe for concurrent use.
type Resolver struct {
	defaultLocale Tag
	profile       ProfileSource
}

// ResolverOption customizes a Resolver at construction time.
type ResolverOption func(*Resolver)

// WithDefault overrides the product default locale (still allowlist-
// checked; the standard default remains pt-BR).
func WithDefault(tag Tag) ResolverOption {
	return func(resolver *Resolver) {
		if IsSupported(tag) {
			resolver.defaultLocale = tag
		}
	}
}

// WithProfileSource installs the authenticated profile preference hook
// (precedence 2). The source must return allowlisted tags; unknown values
// from persistence are ignored, never reflected.
func WithProfileSource(source ProfileSource) ResolverOption {
	return func(resolver *Resolver) {
		resolver.profile = source
	}
}

// NewResolver builds the immutable locale resolver.
func NewResolver(options ...ResolverOption) *Resolver {
	resolver := &Resolver{defaultLocale: Default()}
	for _, option := range options {
		option(resolver)
	}
	return resolver
}

// Resolve applies the full precedence chain and returns the resolved
// allowlisted locale.
func (resolver *Resolver) Resolve(request *http.Request) Tag {
	tag, _ := resolver.resolve(request)
	return tag
}

// source identifies the precedence source of a resolution; sourceNone
// marks the fallback (no usable explicit signal in the request).
type source int

const (
	sourceNone source = iota
	sourceProfile
	sourceCookie
	sourceAcceptLanguage
)

// resolve applies the full precedence chain and reports which source
// produced the locale, so the middleware can count true fallbacks.
func (resolver *Resolver) resolve(request *http.Request) (Tag, source) {
	// 1. Explicit route/action locale: not applicable yet — no localized
	// route prefix exists at this stage (arrives with localized routes).

	// 2. Authenticated profile preference.
	if resolver.profile != nil {
		if tag, ok := resolver.profile(request); ok && IsSupported(tag) {
			return tag, sourceProfile
		}
	}

	// 3. Validated interface cookie.
	if cookie, err := request.Cookie(CookieName); err == nil {
		if tag, err := ParseBCP47(cookie.Value); err == nil && IsSupported(tag) {
			return tag, sourceCookie
		}
	}

	// 4. Accept-Language negotiated against the allowlist.
	if tag, ok := Negotiate(request.Header.Get("Accept-Language")); ok {
		return tag, sourceAcceptLanguage
	}

	// 5. Configured default.
	return resolver.defaultLocale, sourceNone
}

// CookieName is the interface cookie of the platform
// (I18N_STANDARD.md §4, precedence 3).
const CookieName = "arena_locale"

// Negotiate parses an Accept-Language header, honors quality weights
// (including the implicit q=1 and q=0 exclusions) and returns the
// highest-weight allowlisted canonical locale. Ties break deterministically
// on the header order (first wins), and unknown or malformed values never
// reach the caller.
func Negotiate(header string) (Tag, bool) {
	for _, tag := range NegotiatedTags(header) {
		if IsSupported(tag) {
			return tag, true
		}
	}
	return "", false
}

// PrefersPortuguese reports whether an Accept-Language header
// negotiates to Portuguese under the allowlist, falling back to the
// product default when nothing negotiates. Staged adapters share
// this helper instead of copying the negotiate-or-default block, so
// the locale only ever selects titles, never values.
func PrefersPortuguese(header string) bool {
	tag, ok := Negotiate(header)
	if !ok {
		tag = Default()
	}
	return strings.HasPrefix(string(tag), "pt")
}

// NegotiatedTags parses an Accept-Language header into canonical tags
// ordered by descending quality weight, excluding q=0. The full ordered
// list (including unknown locales) is exposed for diagnostics; production
// negotiation goes through Negotiate, which filters by the allowlist.
func NegotiatedTags(header string) []Tag {
	if strings.TrimSpace(header) == "" {
		return nil
	}

	type candidate struct {
		tag     Tag
		quality float64
		order   int
	}

	var candidates []candidate
	order := 0
	for _, part := range strings.Split(header, ",") {
		ranges := strings.Split(part, ";")
		raw := strings.TrimSpace(ranges[0])
		if raw == "" || raw == "*" {
			continue
		}
		tag, err := ParseBCP47(raw)
		if err != nil {
			continue
		}
		quality := 1.0
		for _, parameter := range ranges[1:] {
			parameter = strings.TrimSpace(parameter)
			if !strings.HasPrefix(strings.ToLower(parameter), "q=") {
				continue
			}
			if value, ok := parseQuality(strings.TrimSpace(parameter[2:])); ok {
				quality = value
			} else {
				// Malformed weight: RFC 7231 treats bad qvalues as
				// invalid members; ignoring the range keeps hostile
				// input from reordering negotiation.
				quality = 0
			}
			break
		}
		if quality <= 0 {
			continue
		}
		candidates = append(candidates, candidate{tag: tag, quality: quality, order: order})
		order++
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].quality != candidates[j].quality {
			return candidates[i].quality > candidates[j].quality
		}
		return candidates[i].order < candidates[j].order
	})

	tags := make([]Tag, 0, len(candidates))
	for _, candidate := range candidates {
		tags = append(tags, candidate.tag)
	}
	return tags
}
