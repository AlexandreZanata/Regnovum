package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// CompatSeasonKey names the dormant pre-season crumb book. It
// carries no lifecycle event and is never activated: fresh books
// arrive named at the port, and legacy rows keep their earlier
// bargain untouched.
const CompatSeasonKey = "compat-legacy"

// SeasonalResetSeconds fixes the crumb book at ninety days in UTC
// seconds. The calendar is mirrored locally so the domain never
// reaches into another module.
const SeasonalResetSeconds = 7776000

// maxSeasonKeyRunes caps the crumb book identifier: room for an
// operator-chosen season key without room for log abuse.
const maxSeasonKeyRunes = 128

// SeasonKey identifies a single crumb book (INK@season_id).
// Eligibility, weekly epochs, reflux and budgets all name theirs up
// front; a missing book fails before storage is consulted.
// Production stays disabled until P44: this names books inertly,
// never activating them.
type SeasonKey string

// ParseSeasonKey checks a crumb book name. Blank, overlong or
// control-bearing input cannot address a book.
func ParseSeasonKey(raw string) (SeasonKey, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrInvalidNewcomer
	}
	if utf8.RuneCountInString(raw) > maxSeasonKeyRunes {
		return "", ErrInvalidNewcomer
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidNewcomer
		}
	}
	return SeasonKey(raw), nil
}

// String exposes the stored book name.
func (k SeasonKey) String() string {
	return string(k)
}

// RequireSeasonalReset demands the reset notice outside the dormant
// book. Skipping it never opens a seasonal book, while the dormant
// one honours its earlier bargain untouched. Paid credit without a
// new acceptance stays valid by preservation, never by conversion
// here.
func RequireSeasonalReset(season SeasonKey, resetAcknowledged bool) error {
	if season == SeasonKey(CompatSeasonKey) {
		return nil
	}
	if resetAcknowledged {
		return nil
	}
	return ErrInvalidNewcomer
}

// CheckSeasonWindow admits only the half-open book span
// [startsAt, endsAt): the opening edge passes, the exact closing
// edge already belongs to the successor. The dormant book skips the
// calendar and stays usable for its earlier path.
func CheckSeasonWindow(season SeasonKey, at, startsAt, endsAt time.Time) error {
	if season == SeasonKey(CompatSeasonKey) {
		return nil
	}
	if startsAt.IsZero() || endsAt.IsZero() {
		return ErrInvalidNewcomer
	}
	moment := at.UTC()
	if moment.Before(startsAt.UTC()) || !moment.Before(endsAt.UTC()) {
		return ErrInvalidNewcomer
	}
	return nil
}

// CheckSeasonMatch keeps each grant inside its own book: the same
// key in another book settles its own outcome instead of redirecting
// a replay. The receipt stays pinned to its original book.
func CheckSeasonMatch(storedSeason, requestSeason SeasonKey) error {
	if storedSeason == requestSeason {
		return nil
	}
	return ErrInvalidNewcomer
}

// SeasonEpoch binds one weekly epoch to its book: the identity of
// one crumb week is (season_id, iso_week). A week counts for R4,
// distribution and reflux only when fully contained in its book.
type SeasonEpoch struct {
	Season SeasonKey
	Epoch  Epoch
}

// Key renders the canonical seasonal week label:
// "temporada-1|2026-W12".
func (s SeasonEpoch) Key() (string, error) {
	if _, err := ParseSeasonKey(s.Season.String()); err != nil {
		return "", err
	}
	epochKey, err := s.Epoch.Key()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%s", s.Season.String(), epochKey), nil
}

// IsEpochContained reports whether one weekly epoch lies fully
// inside the book window: Start >= seasonStart and End <=
// seasonEnd, all in UTC. Partial head and tail weeks never count:
// the opening week starting before the book and the closing week
// ending after it are excluded, even when they overlap it. An
// invalid epoch or an empty book window reads as not contained.
func IsEpochContained(epoch Epoch, seasonStart, seasonEnd time.Time) bool {
	if seasonStart.IsZero() || seasonEnd.IsZero() {
		return false
	}
	start, err := epoch.Start()
	if err != nil {
		return false
	}
	end, err := epoch.End()
	if err != nil {
		return false
	}
	if start.UTC().Before(seasonStart.UTC()) {
		return false
	}
	if end.UTC().After(seasonEnd.UTC()) {
		return false
	}
	return true
}

// IsContained reports whether the bound week lies fully inside the
// given book window.
func (s SeasonEpoch) IsContained(seasonStart, seasonEnd time.Time) bool {
	return IsEpochContained(s.Epoch, seasonStart, seasonEnd)
}

// SeasonWeek carries one sealed weekly net with its epoch for
// seasonal R4 selection: only weeks fully inside the book feed the
// median.
type SeasonWeek struct {
	Epoch Epoch
	Net   int64
}

// SelectContainedNets keeps the nets of weeks fully inside the book,
// in input order. Unsealable epochs and partial head/tail weeks are
// dropped; an empty book window keeps nothing. More than four kept
// nets refuse later in MedianR4: R4 never looks past four weeks.
func SelectContainedNets(weeks []SeasonWeek, seasonStart, seasonEnd time.Time) []int64 {
	nets := make([]int64, 0, len(weeks))
	for _, w := range weeks {
		if !IsEpochContained(w.Epoch, seasonStart, seasonEnd) {
			continue
		}
		nets = append(nets, w.Net)
	}
	return nets
}

// SeasonalMedianR4 nets the R4 reference of one book: the median of
// the nets of sealed weeks fully contained in it, floored on even
// straddles. Weeks outside the book — earlier history, partial head
// and tail weeks, and later books — never enter: with no applicable
// history the budget is defined zero, without failing.
func SeasonalMedianR4(weeks []SeasonWeek, seasonStart, seasonEnd time.Time) (int64, error) {
	return MedianR4(SelectContainedNets(weeks, seasonStart, seasonEnd))
}

// SeasonalAdmitRequest carries one seasonal newcomer claim: the
// opaque account, the minimized person digest, the book with its
// weekly epoch, and the per-call standing and antifraud verdicts.
// Antifraud and sanction signals stay global: creating an account or
// changing books never clears them, so the caller passes the current
// verdicts here. There is no activity field and no PII field.
type SeasonalAdmitRequest struct {
	Account           string
	PersonProofHash   string
	Season            SeasonKey
	EpochKey          string
	AccountActive     bool
	AntifraudClear    bool
	ResetAcknowledged bool
}

// SeasonalGrant is one seasonal newcomer decision: the opaque
// account, the minimized uniqueness digest, the book it was claimed
// in with its weekly epoch, and the lifecycle status, sealed by
// hash. The seal binds account, digest, book and epoch: any
// reclassification breaks the seal first. One concession per person
// per book: the same person may ingress a later book once, never a
// second account in the same book.
type SeasonalGrant struct {
	Account    string
	PersonHash string
	Season     SeasonKey
	EpochKey   string
	Status     NewcomerStatus
	Hash       string
}

// sealSeasonalGrant binds account, digest, book and epoch.
func sealSeasonalGrant(account, personHash, season, epochKey string) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%s", account, personHash, season, epochKey)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// buildSeasonalGrant seals one seasonal grant in the given status.
func buildSeasonalGrant(account, personHash string, season SeasonKey, epochKey string, status NewcomerStatus) SeasonalGrant {
	return SeasonalGrant{
		Account: account, PersonHash: personHash, Season: season, EpochKey: epochKey,
		Status: status, Hash: sealSeasonalGrant(account, personHash, season.String(), epochKey),
	}
}

// validateSeasonalAdmitRequest checks the seasonal claim shape: an
// opaque account, a digest proof, a named book with reset notice,
// and a sealable weekly epoch.
func validateSeasonalAdmitRequest(req SeasonalAdmitRequest) (account, personHash string, season SeasonKey, epochKey string, err error) {
	account, err = parseNewcomerToken(req.Account)
	if err != nil {
		return "", "", "", "", err
	}
	if !isNewcomerHex64(req.PersonProofHash) {
		return "", "", "", "", ErrInvalidNewcomer
	}
	season, err = ParseSeasonKey(req.Season.String())
	if err != nil {
		return "", "", "", "", err
	}
	if err = RequireSeasonalReset(season, req.ResetAcknowledged); err != nil {
		return "", "", "", "", err
	}
	if _, err = ParseEpochKey(req.EpochKey); err != nil {
		return "", "", "", "", ErrInvalidNewcomer
	}
	return account, req.PersonProofHash, season, req.EpochKey, nil
}

// seasonalMatch reports how one seasonal claim relates to
// ever-admitted seasonal grants of any book: an exact replay in the
// same book and epoch, a person already counted in this book, or an
// account epoch pair of this book claiming a different person.
// Other books never decide: the same person in another book is
// another claim, and the same account there replays there.
func seasonalMatch(admitted []SeasonalGrant, account, personHash string, season SeasonKey, epochKey string) (replay SeasonalGrant, replayed, duplicate, conflict bool) {
	for _, g := range admitted {
		if g.Season != season {
			continue
		}
		if g.PersonHash == personHash && g.Account == account && g.EpochKey == epochKey {
			return g, true, false, false
		}
		if g.PersonHash == personHash {
			duplicate = true
		}
		if g.Account == account && g.EpochKey == epochKey {
			conflict = true
		}
	}
	return SeasonalGrant{}, false, duplicate, conflict
}

// AdmitSeasonalNewcomer records one eligible newcomer exactly once
// per person per book: a standing account with a clear proportional
// antifraud check and a person digest never admitted in this book.
// The admitted slice holds ever-admitted seasonal grants of all
// books (including cancelled and erased: withdrawing never frees a
// second grant in the same book); blocked attempts never join it.
//
// An exact replay (same account, digest, book and epoch) returns the
// recorded grant idempotently; a replay in another book settles
// there. A later account of the same person in the same book blocks
// with ErrDuplicateNewcomer and appeal. The same account in a later
// book admits again when approved: old accounts ingress once per
// book. A blocked standing, a failed antifraud check or a missing
// reset blocks with appeal or refusal: the first seasonal
// eligibility is never a fake new account, and global sanctions are
// never reset by a new book. A same account epoch pair of the same
// book claiming a different person refuses without a grant.
func AdmitSeasonalNewcomer(req SeasonalAdmitRequest, admitted []SeasonalGrant) (SeasonalGrant, error) {
	account, personHash, season, epochKey, err := validateSeasonalAdmitRequest(req)
	if err != nil {
		return SeasonalGrant{}, err
	}
	replay, replayed, duplicate, conflict := seasonalMatch(admitted, account, personHash, season, epochKey)
	if replayed {
		return replay, nil
	}
	if duplicate {
		return buildSeasonalGrant(account, personHash, season, epochKey, NewcomerBlocked), ErrDuplicateNewcomer
	}
	if conflict {
		return SeasonalGrant{}, ErrInvalidNewcomer
	}
	if !req.AccountActive || !req.AntifraudClear {
		return buildSeasonalGrant(account, personHash, season, epochKey, NewcomerBlocked), ErrAccountBlocked
	}
	return buildSeasonalGrant(account, personHash, season, epochKey, NewcomerAdmitted), nil
}

// VerifyHash recomputes the seasonal seal and refuses a grant whose
// terms no longer agree.
func (g SeasonalGrant) VerifyHash() error {
	if g.Hash == "" || sealSeasonalGrant(g.Account, g.PersonHash, g.Season.String(), g.EpochKey) != g.Hash {
		return ErrInvalidNewcomer
	}
	return nil
}

// Appeal records the holder challenge of a blocked seasonal grant:
// blocked grants only, by the holder only.
func (g SeasonalGrant) Appeal(by string) (SeasonalGrant, error) {
	if err := g.VerifyHash(); err != nil {
		return SeasonalGrant{}, err
	}
	if g.Status != NewcomerBlocked {
		return SeasonalGrant{}, ErrNewcomerState
	}
	if by != g.Account || strings.TrimSpace(by) == "" {
		return SeasonalGrant{}, ErrNewcomerNotParty
	}
	next := g
	next.Status = NewcomerAppealed
	next.Hash = sealSeasonalGrant(next.Account, next.PersonHash, next.Season.String(), next.EpochKey)
	return next, nil
}

// Cancel withdraws an admitted, blocked or appealed seasonal grant
// before any distribution: terminal, with no payment.
func (g SeasonalGrant) Cancel() (SeasonalGrant, error) {
	if err := g.VerifyHash(); err != nil {
		return SeasonalGrant{}, err
	}
	if g.Status != NewcomerAdmitted && g.Status != NewcomerBlocked && g.Status != NewcomerAppealed {
		return SeasonalGrant{}, ErrNewcomerState
	}
	next := g
	next.Status = NewcomerCancelled
	next.Hash = sealSeasonalGrant(next.Account, next.PersonHash, next.Season.String(), next.EpochKey)
	return next, nil
}

// Erase removes the account identifier per retention policy: the
// uniqueness digest, book and epoch stay so the person never counts
// twice in the same book, and the seal is recomputed over the erased
// form.
func (g SeasonalGrant) Erase() (SeasonalGrant, error) {
	if err := g.VerifyHash(); err != nil {
		return SeasonalGrant{}, err
	}
	if g.Status == NewcomerErased {
		return SeasonalGrant{}, ErrNewcomerState
	}
	next := g
	next.Account = ""
	next.Status = NewcomerErased
	next.Hash = sealSeasonalGrant(next.Account, next.PersonHash, next.Season.String(), next.EpochKey)
	return next, nil
}
