package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// Version is one constitutional rule version: v followed by a
// positive integer, ASCII only. Locale-specific digits can never name
// a version, so pt and en holders always mean the same charter. The
// vocabulary mirrors the conversion consent versions without
// importing them: domains stay disjoint by architecture.
type Version string

// ParseVersion validates a version token against the closed
// vocabulary. Matching is exact: no trimming, no case folding, no
// zero-padded numbers.
func ParseVersion(raw string) (Version, error) {
	if len(raw) < 2 || raw[0] != 'v' {
		return "", ErrInvalidCharter
	}
	number, err := strconv.Atoi(raw[1:])
	if err != nil || number < 1 || raw[1] == '0' {
		return "", ErrInvalidCharter
	}
	if "v"+strconv.Itoa(number) != raw {
		return "", ErrInvalidCharter
	}
	return Version(raw), nil
}

// Number renders the ordinal of one version for chain ordering.
func (v Version) Number() (int, error) {
	number, err := strconv.Atoi(string(v[1:]))
	if err != nil {
		return 0, ErrInvalidCharter
	}
	return number, nil
}

// String returns the stored version token.
func (v Version) String() string { return string(v) }

// CharterLocale is the closed vocabulary of charter languages:
// Portuguese and English. The rules are the same either way; each
// language seals its own content digest.
type CharterLocale string

const (
	// CharterLocalePortuguese renders the charter in Portuguese.
	CharterLocalePortuguese CharterLocale = "pt"
	// CharterLocaleEnglish renders the charter in English.
	CharterLocaleEnglish CharterLocale = "en"
)

// ParseCharterLocale validates a locale against the closed
// vocabulary. Matching is exact: no trimming, no case folding.
func ParseCharterLocale(raw string) (CharterLocale, error) {
	locale := CharterLocale(raw)
	switch locale {
	case CharterLocalePortuguese, CharterLocaleEnglish:
		return locale, nil
	default:
		return "", ErrInvalidCharter
	}
}

// String returns the stored locale value.
func (l CharterLocale) String() string { return string(l) }

// isContentHash reports whether raw is 64 lowercase hex digits: the
// canonical text digest shape. Charter text is public rules; no PII
// ever passes here, only the digest.
func isContentHash(raw string) bool {
	if len(raw) != 64 {
		return false
	}
	for _, c := range raw {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Release is one immutable charter publication: its version, its
// language, the instant it takes effect in UTC, the previous version
// of the same language line (empty for a genesis) and the digest of
// its canonical text, sealed by hash. Immutability is structural:
// there is no update path, only a newer release.
type Release struct {
	Version     Version
	Locale      CharterLocale
	EffectiveAt time.Time
	Previous    Version
	ContentHash string
	Hash        string
}

// PublishRequest carries one publication. Every value arrives from
// the caller: no ratified version, locale or instant lives here.
type PublishRequest struct {
	Version     string
	Locale      string
	EffectiveAt time.Time
	Previous    string
	ContentHash string
}

// sealRelease binds version, locale, effective UTC instant, previous
// version and content digest: any reclassification breaks the seal
// first.
func sealRelease(version Version, locale CharterLocale, effective time.Time, previous Version, content string) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s",
		version.String(), locale.String(),
		effective.UTC().Format(time.RFC3339Nano),
		previous.String(), content)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// tipOf returns the latest release of one locale line, or false when
// the locale has no publication yet.
func tipOf(chain []Release, locale CharterLocale) (Release, bool) {
	var tip Release
	found := false
	for _, release := range chain {
		if release.Locale != locale {
			continue
		}
		if !found || release.EffectiveAt.After(tip.EffectiveAt) {
			tip, found = release, true
		}
	}
	return tip, found
}

// checkLink validates one publication against its locale line: a
// genesis needs an empty line and no previous, a continuation needs
// the tip version as previous with a strictly later effective
// instant and a higher version number. Duplicates refuse.
func checkLink(chain []Release, version Version, locale CharterLocale, effective time.Time, previous Version) error {
	tip, found := tipOf(chain, locale)
	number, err := version.Number()
	if err != nil {
		return err
	}
	if !found {
		if previous != "" {
			return ErrUnknownVersion
		}
		return nil
	}
	tipNumber, err := tip.Version.Number()
	if err != nil {
		return ErrInvalidCharter
	}
	if version == tip.Version || number <= tipNumber {
		return ErrInvalidCharter
	}
	if previous != tip.Version {
		return ErrUnknownVersion
	}
	if !effective.After(tip.EffectiveAt) {
		return ErrInvalidCharter
	}
	return nil
}

// Publish seals one immutable charter release on its locale line. A
// genesis opens an empty line; any continuation names the tip
// version with a strictly later effective instant. Missing previous
// versions block instead of inferring: a version nobody published
// never takes effect.
func Publish(chain []Release, req PublishRequest) (Release, error) {
	version, err := ParseVersion(req.Version)
	if err != nil {
		return Release{}, err
	}
	locale, err := ParseCharterLocale(req.Locale)
	if err != nil {
		return Release{}, err
	}
	if req.EffectiveAt.IsZero() {
		return Release{}, ErrInvalidCharter
	}
	if !isContentHash(req.ContentHash) {
		return Release{}, ErrInvalidCharter
	}
	previous := Version(req.Previous)
	if previous != "" {
		if _, err := ParseVersion(previous.String()); err != nil {
			return Release{}, err
		}
	}
	effective := req.EffectiveAt.UTC()
	if err := checkLink(chain, version, locale, effective, previous); err != nil {
		return Release{}, err
	}
	return Release{
		Version: version, Locale: locale, EffectiveAt: effective,
		Previous: previous, ContentHash: req.ContentHash,
		Hash: sealRelease(version, locale, effective, previous, req.ContentHash),
	}, nil
}

// VerifyHash recomputes the seal and refuses a release whose terms no
// longer agree.
func (r Release) VerifyHash() error {
	if r.Hash == "" || sealRelease(r.Version, r.Locale, r.EffectiveAt, r.Previous, r.ContentHash) != r.Hash {
		return ErrInvalidCharter
	}
	return nil
}

// findRelease resolves one published version of one locale,
// verifying its seal. Unknown versions refuse: readers wait for
// publication.
func findRelease(chain []Release, version Version, locale CharterLocale) (Release, error) {
	for _, release := range chain {
		if release.Version == version && release.Locale == locale {
			if err := release.VerifyHash(); err != nil {
				return Release{}, err
			}
			return release, nil
		}
	}
	return Release{}, ErrUnknownVersion
}

// VersionInForce resolves the rule covering one locale at one
// instant: the latest effective release at or before the instant.
// Facts and contracts bind to this version; an instant no release
// covers refuses, blocking new acceptances under nothing.
func VersionInForce(chain []Release, locale CharterLocale, at time.Time) (Release, error) {
	instant := at.UTC()
	var current Release
	found := false
	for _, release := range chain {
		if release.Locale != locale || release.EffectiveAt.After(instant) {
			continue
		}
		if err := release.VerifyHash(); err != nil {
			return Release{}, err
		}
		if !found || release.EffectiveAt.After(current.EffectiveAt) ||
			(release.EffectiveAt.Equal(current.EffectiveAt) && release.Version > current.Version) {
			current, found = release, true
		}
	}
	if !found {
		return Release{}, ErrUnknownVersion
	}
	return current, nil
}

// ResolveSubstantive binds one earlier fact or contract to its
// contemporary rule: the version in force when the fact happened. Old
// contracts keep their version after newer releases take effect;
// locale and fact instant arrive explicitly, never inferred.
func ResolveSubstantive(chain []Release, locale CharterLocale, factAt time.Time) (Release, error) {
	return VersionInForce(chain, locale, factAt)
}
