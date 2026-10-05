package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxPatentRunes bounds opaque patent tokens: long enough for
// grant, holder, season, title and terms references, short enough
// to stay out of log and index abuse.
const maxPatentRunes = 128

// maxPatentDetailRunes bounds one cosmetic privilege detail: long
// enough to name the visible mark, short enough to stay out of
// log abuse.
const maxPatentDetailRunes = 2000

// parsePatentToken validates one opaque token: exact match,
// bounded, never blank, no control characters.
func parsePatentToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidGrant
	}
	if utf8.RuneCountInString(raw) > maxPatentRunes {
		return "", ErrInvalidGrant
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidGrant
		}
	}
	return raw, nil
}

// parsePatentDetail validates one cosmetic privilege detail.
func parsePatentDetail(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidGrant
	}
	if utf8.RuneCountInString(raw) > maxPatentDetailRunes {
		return "", ErrInvalidGrant
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidGrant
		}
	}
	return raw, nil
}

// SeasonKey is the identity of one season book the patent belongs
// to. Patents never cross books: the same honor in another season
// is another grant, and historic honor grants no current power.
type SeasonKey string

// ParseSeasonKey validates a season book key.
func ParseSeasonKey(raw string) (SeasonKey, error) {
	token, err := parsePatentToken(raw)
	if err != nil {
		return "", err
	}
	return SeasonKey(token), nil
}

// String returns the stored season key value.
func (k SeasonKey) String() string { return string(k) }

// ApprovedTerms carries the ratified sale decision: its version,
// price in minor units with ISO currency, seat cap, duration and
// the cosmetic privilege it covers. Every field arrives in the
// approval: the domain holds no price, seat, duration or title
// default, so a missing term blocks the sale instead of inventing
// one.
type ApprovedTerms struct {
	Version         string
	PriceMinorUnits int64
	Currency        string
	Seats           int64
	Duration        time.Duration
	CosmeticGrant   string
}

// checkTerms validates ratified terms: whole version, positive
// price and seats, positive duration and a named cosmetic grant.
// Currency is an exact ISO token: no trimming, no case folding.
func checkTerms(terms *ApprovedTerms) (ApprovedTerms, error) {
	if terms == nil {
		return ApprovedTerms{}, ErrTermsMissing
	}
	version, err := parsePatentToken(terms.Version)
	if err != nil {
		return ApprovedTerms{}, ErrTermsMissing
	}
	currency, err := parsePatentToken(terms.Currency)
	if err != nil {
		return ApprovedTerms{}, ErrTermsMissing
	}
	cosmetic, err := parsePatentDetail(terms.CosmeticGrant)
	if err != nil {
		return ApprovedTerms{}, ErrTermsMissing
	}
	if terms.PriceMinorUnits <= 0 || terms.Seats <= 0 || terms.Duration <= 0 {
		return ApprovedTerms{}, ErrTermsMissing
	}
	return ApprovedTerms{
		Version:         version,
		PriceMinorUnits: terms.PriceMinorUnits,
		Currency:        currency,
		Seats:           terms.Seats,
		Duration:        terms.Duration,
		CosmeticGrant:   cosmetic,
	}, nil
}

// PatentGrant is one honorific patent: its identity, holder,
// season book, cosmetic title, when it was granted, when it
// expires and which ratified terms version sold it. It carries
// no authority, vote, truth, reputation, wealth or office: the
// struct has no such field on purpose, and the predicates below
// always answer the negative.
type PatentGrant struct {
	ID           string
	Holder       string
	Season       SeasonKey
	Title        string
	GrantedAt    time.Time
	ExpiresAt    time.Time
	TermsVersion string
}

// GrantRequest carries one patent sale: the grant identity,
// holder, season book and cosmetic title, when the sale happens,
// the season window it lives in, and the ratified terms that
// authorize price, seats and duration.
type GrantRequest struct {
	ID          string
	Holder      string
	Season      string
	Title       string
	GrantedAt   time.Time
	SeasonStart time.Time
	SeasonEnd   time.Time
	Terms       *ApprovedTerms
}

// GrantPatent sells one honorific patent under ratified terms.
// The grant starts at the sale instant and expires at the
// earlier of sale plus ratified duration and season end, so the
// status never survives its book. A missing term, a malformed
// identity, or a sale outside the season window refuses with the
// grant untouched.
func GrantPatent(req GrantRequest) (PatentGrant, error) {
	terms, err := checkTerms(req.Terms)
	if err != nil {
		return PatentGrant{}, err
	}
	id, err := parsePatentToken(req.ID)
	if err != nil {
		return PatentGrant{}, err
	}
	holder, err := parsePatentToken(req.Holder)
	if err != nil {
		return PatentGrant{}, err
	}
	season, err := ParseSeasonKey(req.Season)
	if err != nil {
		return PatentGrant{}, err
	}
	title, err := parsePatentDetail(req.Title)
	if err != nil {
		return PatentGrant{}, err
	}
	if req.GrantedAt.IsZero() || req.SeasonStart.IsZero() || req.SeasonEnd.IsZero() {
		return PatentGrant{}, ErrInvalidGrant
	}
	start := req.SeasonStart.UTC()
	end := req.SeasonEnd.UTC()
	at := req.GrantedAt.UTC()
	if !end.After(start) {
		return PatentGrant{}, ErrInvalidGrant
	}
	if at.Before(start) || !at.Before(end) {
		return PatentGrant{}, ErrExpired
	}
	expires := at.Add(terms.Duration)
	if !expires.After(at) || expires.After(end) {
		expires = end
	}
	return PatentGrant{
		ID: id, Holder: holder, Season: season, Title: title,
		GrantedAt: at, ExpiresAt: expires, TermsVersion: terms.Version,
	}, nil
}

// IsActive answers whether the grant is live at the instant: the
// window is [granted, expiry), so the exact expiry tick already
// counts as expired.
func (g PatentGrant) IsActive(at time.Time) bool {
	if g.GrantedAt.IsZero() || g.ExpiresAt.IsZero() {
		return false
	}
	moment := at.UTC()
	return !moment.Before(g.GrantedAt.UTC()) && moment.Before(g.ExpiresAt.UTC())
}

// TransferRequest carries a handover attempt: the grant and the
// account that would receive it.
type TransferRequest struct {
	Grant PatentGrant
	To    string
}

// TransferPatent always refuses: patents are bound to one holder
// and never move, not even with a well-formed recipient.
func TransferPatent(req TransferRequest) error {
	if _, err := parsePatentToken(req.To); err != nil {
		return ErrInvalidGrant
	}
	return ErrNonTransferable
}

// AuthorizesOffice always answers false: a patent never seats its
// holder in an office API, whatever the title says.
func (g PatentGrant) AuthorizesOffice() bool { return false }

// RankingWeight always answers zero: a patent never moves a
// factual ranking, whatever the title says.
func (g PatentGrant) RankingWeight() int64 { return 0 }

// HistoricHonor is the fact of a past season patent: whose honor
// it was, in which book, under which title and terms. It is a
// record for display only: it authorizes nothing and discounts
// nothing.
type HistoricHonor struct {
	Holder       string
	Season       SeasonKey
	Title        string
	TermsVersion string
}

// RememberHonor keeps the past season honor as history: the fact
// stays named, with no live window and no value attached.
func RememberHonor(grant PatentGrant) HistoricHonor {
	return HistoricHonor{
		Holder: grant.Holder, Season: grant.Season,
		Title: grant.Title, TermsVersion: grant.TermsVersion,
	}
}

// RenewRequest carries a renewal attempt: the remembered honor
// and the new season book it would enter.
type RenewRequest struct {
	Honor     HistoricHonor
	NewSeason string
}

// RequireNewGrant always refuses automatic renewal: the new book
// needs a new sale under its own ratified terms, with new
// acceptance, so history never becomes a discount or a power.
func RequireNewGrant(req RenewRequest) error {
	if _, err := ParseSeasonKey(string(req.Honor.Season)); err != nil {
		return ErrInvalidGrant
	}
	if _, err := ParseSeasonKey(req.NewSeason); err != nil {
		return ErrInvalidGrant
	}
	if req.Honor.Holder == "" || req.Honor.Title == "" {
		return ErrInvalidGrant
	}
	return ErrNewGrantRequired
}
