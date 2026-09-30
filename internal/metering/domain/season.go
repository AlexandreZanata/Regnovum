package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxPublicationBookRunes caps the publication book identifier: room
// for an operator-chosen season key without room for log abuse.
const maxPublicationBookRunes = 128

// CompatSeasonKey names the dormant pre-season publication ledger.
// No lifecycle event ever points at it and activation never selects
// it: fresh books arrive named at the port.
const CompatSeasonKey = "compat-legacy"

// SeasonalResetSeconds fixes the publication book at ninety days in
// UTC seconds. The calendar is mirrored locally so the domain never
// reaches into another module.
const SeasonalResetSeconds = 7776000

// SeasonKey identifies a single publication ledger. Previews,
// intentions and charges all state theirs up front; a missing book
// fails before storage is consulted.
type SeasonKey string

// ParseSeasonKey checks a publication ledger name. Blank, overlong
// or control-bearing input cannot address a book.
func ParseSeasonKey(raw string) (SeasonKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed != raw {
		return "", ErrInvalidQuote
	}
	if utf8.RuneCountInString(raw) > maxPublicationBookRunes {
		return "", ErrInvalidQuote
	}
	for _, point := range raw {
		if unicode.IsControl(point) {
			return "", ErrInvalidQuote
		}
	}
	return SeasonKey(raw), nil
}

// String exposes the stored ledger name.
func (k SeasonKey) String() string {
	return string(k)
}

// RequireSeasonalReset demands the reset notice outside the dormant
// ledger. Skipping it never opens a seasonal book, while the dormant
// one honours its earlier bargain untouched.
func RequireSeasonalReset(season SeasonKey, resetAcknowledged bool) error {
	if string(season) == CompatSeasonKey {
		return nil
	}
	if resetAcknowledged {
		return nil
	}
	return ErrInvalidQuote
}

// CapExpiryBySeasonEnd trims a quotation to the ledger close: paid
// delivery never outlives ends_at. An empty end only excuses the
// dormant ledger; live books always bring a real close.
func CapExpiryBySeasonEnd(ttlExpiry time.Time, season SeasonKey, endsAt time.Time) time.Time {
	if string(season) == CompatSeasonKey {
		return ttlExpiry
	}
	if endsAt.IsZero() {
		return ttlExpiry
	}
	if endsAt.UTC().Before(ttlExpiry) {
		return endsAt.UTC()
	}
	return ttlExpiry
}

// CheckSeasonWindow admits only the half-open ledger span
// [startsAt, endsAt). Opening edge passes, closing edge already
// belongs ahead. The dormant ledger skips the calendar and stays
// usable for its earlier path.
func CheckSeasonWindow(season SeasonKey, at, startsAt, endsAt time.Time) error {
	if string(season) == CompatSeasonKey {
		return nil
	}
	if startsAt.IsZero() || endsAt.IsZero() {
		return ErrInvalidQuote
	}
	moment := at.UTC()
	start := startsAt.UTC()
	finish := endsAt.UTC()
	if moment.Before(start) || !moment.Before(finish) {
		return ErrQuoteExpired
	}
	return nil
}

// CheckSeasonMatch keeps each preview inside its own ledger: a seal
// from elsewhere never funds here, so receipts never wander books.
func CheckSeasonMatch(quoteSeason, requestSeason SeasonKey) error {
	if string(quoteSeason) == string(requestSeason) {
		return nil
	}
	return ErrQuoteMismatch
}
