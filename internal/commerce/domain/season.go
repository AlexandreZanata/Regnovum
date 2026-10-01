package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxSeasonKeyRunes bounds the commerce book key: long enough for an
// operator-chosen book identifier, short enough to stay out of log
// and index abuse.
const maxSeasonKeyRunes = 128

// CompatSeasonKey is the explicitly inactive namespace for
// pre-season commerce rows. It carries no lifecycle event and is
// never activated: new books name their own key at the port.
const CompatSeasonKey = "compat-legacy"

// SeasonalResetSeconds pins the ninety-day book for the commerce
// family: 90 × 24 × 60 × 60 in UTC. It mirrors the season calendar
// without importing it: domains stay disjoint by architecture.
const SeasonalResetSeconds = 7776000

// SeasonKey is the identity of one commerce book. Every transfer,
// contract and settlement names its book; an absent season is
// refused before any row is read or written.
type SeasonKey string

// ParseSeasonKey validates a commerce book key. Empty, blank,
// overlong and control-carrying keys are refused before they can
// name a book.
func ParseSeasonKey(raw string) (SeasonKey, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrInvalidContract
	}
	if utf8.RuneCountInString(raw) > maxSeasonKeyRunes {
		return "", ErrInvalidContract
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidContract
		}
	}
	return SeasonKey(raw), nil
}

// String returns the stored season key value.
func (k SeasonKey) String() string {
	return string(k)
}

// RequireSeasonalReset refuses a seasonal commerce mutation without
// an explicit reset acknowledgement: an omitted reset never
// authorizes it. The compat-legacy book keeps its legacy contract
// without one.
func RequireSeasonalReset(season SeasonKey, resetAcknowledged bool) error {
	if season == SeasonKey(CompatSeasonKey) {
		return nil
	}
	if !resetAcknowledged {
		return ErrInvalidContract
	}
	return nil
}

// CheckSeasonWindow refuses admission outside the half-open book
// window [startsAt, endsAt): the start admits, the exact end already
// belongs to the successor. The compat-legacy book bypasses the
// window: it carries no lifecycle event and stays admissible for the
// legacy path.
func CheckSeasonWindow(season SeasonKey, at, startsAt, endsAt time.Time) error {
	if season == SeasonKey(CompatSeasonKey) {
		return nil
	}
	if startsAt.IsZero() || endsAt.IsZero() {
		return ErrInvalidContract
	}
	moment := at.UTC()
	if moment.Before(startsAt.UTC()) || !moment.Before(endsAt.UTC()) {
		return ErrContractState
	}
	return nil
}

// CheckSeasonMatch refuses a cross-book reuse: the same key in
// another book settles its own outcome instead of redirecting a
// replay. The receipt stays pinned to its original book.
func CheckSeasonMatch(storedSeason, requestSeason SeasonKey) error {
	if storedSeason != requestSeason {
		return ErrIntentionConflict
	}
	return nil
}

// SeasonalTradeTerms carries the terminal clause of section 5.1 of
// docs/reino/TEMPORADAS_SUCESSAO.md beside the escrow: the book, the
// terminal policy reference with its hash, the accept evidence of
// both parties recorded before funding, and the exclusive book end
// bounding the contract deadline. The charter offers Carta/accepts,
// disputes offers procedure/decision: neither is a second ledger
// here, and accepting the Carta never accepts this contract.
type SeasonalTradeTerms struct {
	Season            SeasonKey
	PolicyRef         string
	PolicyHash        string
	BuyerAccept       string
	ProviderAccept    string
	SeasonEndsAt      time.Time
	ResetAcknowledged bool
}

// parseTradeToken validates one opaque trade term token: exact
// match, no surrounding whitespace, no control characters, bounded
// length.
func parseTradeToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidContract
	}
	if utf8.RuneCountInString(raw) > maxContractRunes {
		return "", ErrInvalidContract
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidContract
		}
	}
	return raw, nil
}

// ValidateSeasonalTradeTerms binds one funding to its terminal
// clause before any lock is taken: the book, a non-blank policy
// reference with a 64-hex hash, accept evidence of both parties and
// a deadline at or before the book end. Absence or divergence
// refuses new seasonal funding; the migration never completes old
// accepts, and no new INK performance is admitted past the end.
func ValidateSeasonalTradeTerms(terms SeasonalTradeTerms, expiresAt time.Time) error {
	if terms.Season == SeasonKey(CompatSeasonKey) {
		return nil
	}
	if _, err := ParseSeasonKey(terms.Season.String()); err != nil {
		return err
	}
	if err := RequireSeasonalReset(terms.Season, terms.ResetAcknowledged); err != nil {
		return err
	}
	if _, err := parseTradeToken(terms.PolicyRef); err != nil {
		return err
	}
	if len(terms.PolicyHash) != 64 || !isSeasonHex64(terms.PolicyHash) {
		return ErrInvalidContract
	}
	if _, err := parseTradeToken(terms.BuyerAccept); err != nil {
		return err
	}
	if _, err := parseTradeToken(terms.ProviderAccept); err != nil {
		return err
	}
	if terms.SeasonEndsAt.IsZero() {
		return ErrInvalidContract
	}
	if expiresAt.IsZero() || expiresAt.UTC().After(terms.SeasonEndsAt.UTC()) {
		return ErrInvalidContract
	}
	return nil
}

// isSeasonHex64 reports whether s is 64 lowercase hex characters.
func isSeasonHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
