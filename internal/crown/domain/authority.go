package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxCrownRunes bounds opaque crown tokens: long enough for
// operation ids and subject labels, short enough to stay out of log
// abuse.
const maxCrownRunes = 128

// SeasonID identifies one season book (INK@season_id). The crown
// never opens books itself: the caller passes the current book and
// its window, and P47 will own holder selection.
type SeasonID string

// HolderSubject identifies one participant account holding or
// claiming the game office. Accounts are never created, altered or
// deleted here: investiture moves power, never data.
type HolderSubject string

// Competence names the power one act requires. It travels as an
// opaque token validated for shape: the closed vocabularies of acts
// and offices arrive in P40-T02/T07, never by inference here.
type Competence string

// ActID identifies one act request. Grants bind it so a recorded
// authorization cannot authorize a different act.
type ActID string

// ReignVersion counts investitures of one season from 1. A new
// holder means a new version: the previous version never decides
// again.
type ReignVersion int

// AuthorityVersion counts versioned changes to one subject's authority,
// consent and credential state. It is monotonic: a version lower than
// the recorded version is a regression and refuses.
type AuthorityVersion int64

// ParseAuthorityVersion validates one authority version: must be at least 1.
func ParseAuthorityVersion(raw int64) (AuthorityVersion, error) {
	if raw < 1 {
		return 0, ErrInvalidAuthority
	}
	return AuthorityVersion(raw), nil
}

// parseToken validates one opaque crown token: exact match, no
// control characters, bounded length.
func parseToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidAuthority
	}
	if utf8.RuneCountInString(raw) > maxCrownRunes {
		return "", ErrInvalidAuthority
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidAuthority
		}
	}
	return raw, nil
}

// ParseSeasonID validates one season book token.
func ParseSeasonID(raw string) (SeasonID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return SeasonID(token), nil
}

// ParseHolderSubject validates one holder token.
func ParseHolderSubject(raw string) (HolderSubject, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return HolderSubject(token), nil
}

// ParseCompetence validates one competence token.
func ParseCompetence(raw string) (Competence, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return Competence(token), nil
}

// ParseActID validates one act token.
func ParseActID(raw string) (ActID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return ActID(token), nil
}

// ParseReignVersion validates one reign count: investitures start at
// 1 and only move forward.
func ParseReignVersion(raw int) (ReignVersion, error) {
	if raw < 1 {
		return 0, ErrInvalidAuthority
	}
	return ReignVersion(raw), nil
}

// Claim is one act authorization request: what act, in which book,
// by whom, under which competence and reign. Operator names whether
// the claimant arrives with a technical administrator credential:
// the caller reads that role from identity, the crown never imports
// it, and an operator claimant is refused as King.
type Claim struct {
	Act        ActID
	Season     SeasonID
	Holder     HolderSubject
	Competence Competence
	Reign      ReignVersion
	Operator   bool
}

// valid checks the claim shape: every token whole, reign from 1.
func (c Claim) valid() error {
	if _, err := ParseActID(string(c.Act)); err != nil {
		return err
	}
	if _, err := ParseSeasonID(string(c.Season)); err != nil {
		return err
	}
	if _, err := ParseHolderSubject(string(c.Holder)); err != nil {
		return err
	}
	if _, err := ParseCompetence(string(c.Competence)); err != nil {
		return err
	}
	if _, err := ParseReignVersion(int(c.Reign)); err != nil {
		return err
	}
	return nil
}

// SovereignSession binds one short authentication to one subject,
// book, reign and competence: when it was granted, when step-up was
// verified, and when it ends. Clocks arrive per call: the domain
// never reads the wall clock.
type SovereignSession struct {
	Season          SeasonID
	Subject         HolderSubject
	Reign           ReignVersion
	Competence      Competence
	AuthenticatedAt time.Time
	MFAAt           time.Time
	ExpiresAt       time.Time
}

// validShape checks the session shape without judging time against
// any clock: tokens whole, reign from 1, instants present and
// ordered.
func (s SovereignSession) validShape() error {
	if _, err := ParseSeasonID(string(s.Season)); err != nil {
		return err
	}
	if _, err := ParseHolderSubject(string(s.Subject)); err != nil {
		return err
	}
	if _, err := ParseReignVersion(int(s.Reign)); err != nil {
		return err
	}
	if _, err := ParseCompetence(string(s.Competence)); err != nil {
		return err
	}
	if s.AuthenticatedAt.IsZero() || s.MFAAt.IsZero() || s.ExpiresAt.IsZero() {
		return ErrInvalidAuthority
	}
	auth, mfa, end := s.AuthenticatedAt.UTC(), s.MFAAt.UTC(), s.ExpiresAt.UTC()
	if mfa.Before(auth) || end.Before(mfa) || !end.After(auth) {
		return ErrInvalidSession
	}
	return nil
}

// Delegation lends operational power inside one book and reign: the
// delegator, the delegate, the competence and the expiry. It never
// widens book, reign or competence, and it dies with its window or
// with the delegator's reign.
type Delegation struct {
	Delegator  HolderSubject
	Delegate   HolderSubject
	Season     SeasonID
	Competence Competence
	Reign      ReignVersion
	ExpiresAt  time.Time
}

// validShape checks the delegation shape: two distinct whole
// holders, whole book and competence, reign from 1, live expiry.
func (d Delegation) validShape() error {
	delegator, err := ParseHolderSubject(string(d.Delegator))
	if err != nil {
		return err
	}
	delegate, err := ParseHolderSubject(string(d.Delegate))
	if err != nil {
		return err
	}
	if delegator == delegate {
		return ErrInvalidDelegation
	}
	if _, err := ParseSeasonID(string(d.Season)); err != nil {
		return err
	}
	if _, err := ParseCompetence(string(d.Competence)); err != nil {
		return err
	}
	if _, err := ParseReignVersion(int(d.Reign)); err != nil {
		return err
	}
	if d.ExpiresAt.IsZero() {
		return ErrInvalidAuthority
	}
	return nil
}

// CurrentReign is the invested snapshot one call judges against:
// the book, its holder, the reign count, the season window and
// whether the book is open. Holder selection lives in P47; here the
// snapshot arrives as data.
type CurrentReign struct {
	Season           SeasonID
	Holder           HolderSubject
	Reign            ReignVersion
	AuthorityVersion AuthorityVersion
	StartsAt         time.Time
	EndsAt           time.Time
	Open             bool
}

// validShape checks the snapshot shape: whole book and holder,
// reign from 1, a non-empty window.
func (c CurrentReign) validShape() error {
	if _, err := ParseSeasonID(string(c.Season)); err != nil {
		return err
	}
	if _, err := ParseHolderSubject(string(c.Holder)); err != nil {
		return err
	}
	if _, err := ParseReignVersion(int(c.Reign)); err != nil {
		return err
	}
	if c.AuthorityVersion < 0 {
		return ErrInvalidAuthority
	}
	if c.StartsAt.IsZero() || c.EndsAt.IsZero() || !c.EndsAt.After(c.StartsAt.UTC()) {
		return ErrInvalidAuthority
	}
	return nil
}

// VerifyAuthorityEffect revalidates authority version and currency at effect time:
// the reign and holder must be active and open, and the presented authority version
// must not have regressed relative to the recorded sovereign authority version.
func VerifyAuthorityEffect(current CurrentReign, recordedVersion, presentedVersion AuthorityVersion) error {
	if err := current.validShape(); err != nil {
		return err
	}
	if !current.Open {
		return ErrSeasonClosed
	}
	if recordedVersion > 0 && presentedVersion < recordedVersion {
		return ErrAuthorityVersionRegression
	}
	return nil
}

// contains reports the half-open book window [starts_at, ends_at):
// the start admits, the exact end already belongs out.
func (c CurrentReign) contains(at time.Time) bool {
	moment := at.UTC()
	return !moment.Before(c.StartsAt.UTC()) && moment.Before(c.EndsAt.UTC())
}

// AuthContext carries the clock and the session policy of one call:
// the instant of judgment and the longest session window the caller
// allows. Durations are policy parameters of the call, never
// ratified values of this module.
type AuthContext struct {
	Now           time.Time
	MaxSessionAge time.Duration
}

// valid checks the context shape: a live instant and a positive
// session cap.
func (c AuthContext) valid() error {
	if c.Now.IsZero() {
		return ErrInvalidAuthority
	}
	if c.MaxSessionAge <= 0 {
		return ErrInvalidAuthority
	}
	return nil
}

// Grant is one recorded authorization: the act it allows, under
// which book, holder, competence and reign, until when. It never
// outlives its session: reuse past expiry, past the reign or past
// the window refuses.
type Grant struct {
	Act        ActID
	Season     SeasonID
	Holder     HolderSubject
	Competence Competence
	Reign      ReignVersion
	ExpiresAt  time.Time
}

// checkSeason judges book and window: same book, open, inside.
func checkSeason(claim Claim, current CurrentReign, now time.Time) error {
	if claim.Season != current.Season {
		return ErrSeasonMismatch
	}
	if !current.Open || !current.contains(now) {
		return ErrSeasonClosed
	}
	return nil
}

// checkReign judges currency: the claim carries the invested reign,
// never an older or a not-yet-invested one.
func checkReign(claim Claim, current CurrentReign) error {
	switch {
	case claim.Reign == current.Reign:
		return nil
	case claim.Reign < current.Reign:
		return ErrStaleReign
	default:
		return ErrFutureReign
	}
}

// checkHolder judges the claimant against the invested holder, or
// chains a delegation to it. An old holder without a fresh chain
// from the new holder stops here: investiture revokes without
// deleting any account.
func checkHolder(claim Claim, delegation *Delegation, current CurrentReign, now time.Time) error {
	if delegation == nil {
		if claim.Holder != current.Holder {
			return ErrNotHolder
		}
		return nil
	}
	if err := delegation.validShape(); err != nil {
		return err
	}
	if claim.Holder == current.Holder {
		return ErrInvalidDelegation
	}
	if delegation.Delegate != claim.Holder || delegation.Delegator != current.Holder {
		return ErrInvalidDelegation
	}
	if delegation.Season != claim.Season || delegation.Season != current.Season {
		return ErrInvalidDelegation
	}
	if delegation.Reign != claim.Reign || delegation.Reign != current.Reign {
		return ErrInvalidDelegation
	}
	if delegation.Competence != claim.Competence {
		return ErrInvalidDelegation
	}
	if !now.UTC().Before(delegation.ExpiresAt.UTC()) {
		return ErrDelegationExpired
	}
	return nil
}

// checkSession binds the session to the claim and the call: same
// subject, book, reign and competence, step-up ordered, live now,
// and no longer than the short window the call allows.
func checkSession(session SovereignSession, claim Claim, ctx AuthContext) error {
	if err := session.validShape(); err != nil {
		return err
	}
	if session.Subject != claim.Holder || session.Season != claim.Season ||
		session.Reign != claim.Reign || session.Competence != claim.Competence {
		return ErrInvalidSession
	}
	now := ctx.Now.UTC()
	auth, end := session.AuthenticatedAt.UTC(), session.ExpiresAt.UTC()
	if now.Before(auth) {
		return ErrInvalidSession
	}
	if !now.Before(end) {
		return ErrSessionExpired
	}
	if end.Sub(auth) > ctx.MaxSessionAge {
		return ErrInvalidSession
	}
	return nil
}

// AuthorizeAct judges one act claim against the invested snapshot,
// its session, an optional delegation and the call clock. Every
// refusal arrives before any effect: this function moves nothing,
// it only allows or denies.
func AuthorizeAct(claim Claim, session SovereignSession, delegation *Delegation, current CurrentReign, ctx AuthContext) (Grant, error) {
	if err := claim.valid(); err != nil {
		return Grant{}, err
	}
	if err := current.validShape(); err != nil {
		return Grant{}, err
	}
	if err := ctx.valid(); err != nil {
		return Grant{}, err
	}
	if claim.Operator {
		return Grant{}, ErrOperatorNotSovereign
	}
	now := ctx.Now.UTC()
	if err := checkSeason(claim, current, now); err != nil {
		return Grant{}, err
	}
	if err := checkReign(claim, current); err != nil {
		return Grant{}, err
	}
	if err := checkHolder(claim, delegation, current, now); err != nil {
		return Grant{}, err
	}
	if err := checkSession(session, claim, ctx); err != nil {
		return Grant{}, err
	}
	return Grant{
		Act: claim.Act, Season: claim.Season, Holder: claim.Holder,
		Competence: claim.Competence, Reign: claim.Reign,
		ExpiresAt: session.ExpiresAt.UTC(),
	}, nil
}

// RevalidateGrant judges reuse of a recorded grant: same book and
// reign still invested, session still live, window still open.
// Anything else refuses: replay authorizes nothing new.
func RevalidateGrant(grant Grant, current CurrentReign, now time.Time) error {
	if _, err := ParseActID(string(grant.Act)); err != nil {
		return err
	}
	if _, err := ParseSeasonID(string(grant.Season)); err != nil {
		return err
	}
	if _, err := ParseHolderSubject(string(grant.Holder)); err != nil {
		return err
	}
	if _, err := ParseCompetence(string(grant.Competence)); err != nil {
		return err
	}
	if _, err := ParseReignVersion(int(grant.Reign)); err != nil {
		return err
	}
	if grant.ExpiresAt.IsZero() {
		return ErrInvalidAuthority
	}
	if err := current.validShape(); err != nil {
		return err
	}
	if now.IsZero() {
		return ErrInvalidAuthority
	}
	if grant.Season != current.Season {
		return ErrSeasonMismatch
	}
	if grant.Reign != current.Reign {
		return ErrStaleReign
	}
	moment := now.UTC()
	if !moment.Before(grant.ExpiresAt.UTC()) {
		return ErrGrantExpired
	}
	if !current.Open || !current.contains(moment) {
		return ErrSeasonClosed
	}
	return nil
}

// EffectFence carries the authority, delegation, and currency parameters
// verified at the moment any royal decree or operational effect takes place.
type EffectFence struct {
	Season               SeasonID
	Reign                ReignVersion
	Competence           Competence
	Author               HolderSubject
	Delegation           *Delegation
	CurrentReign         CurrentReign
	EconomicBacklogClean bool
	Now                  time.Time
}

// ValidateEffectFence fences royal effects by (season_id, reign_version, competência),
// active reign currency, delegation currency and economic checkpoint backlog.
//
// Invariants (P47-T06 / docs/reino/TEMPORADAS_SUCESSAO.md §8):
// 1. Season mismatch: effect outside the current season book refuses.
// 2. Stale reign: acts or permissions from an ex-monarch refuse with ErrStaleReign.
// 3. Stale delegation: delegations from the previous reign refuse with ErrInvalidDelegation.
// 4. Competence: delegation competence cannot widen or differ from the required competence.
// 5. Backlog: unevaluated confirmed economic backlog blocks royal effects with ErrBacklogUnevaluated.
func ValidateEffectFence(f EffectFence) error {
	if err := f.CurrentReign.validShape(); err != nil {
		return err
	}
	if f.Now.IsZero() {
		return ErrInvalidAuthority
	}
	if _, err := ParseCompetence(string(f.Competence)); err != nil {
		return err
	}
	if f.Season != f.CurrentReign.Season {
		return ErrSeasonMismatch
	}
	moment := f.Now.UTC()
	if !f.CurrentReign.Open || !f.CurrentReign.contains(moment) {
		return ErrSeasonClosed
	}
	switch {
	case f.Reign == f.CurrentReign.Reign:
	case f.Reign < f.CurrentReign.Reign:
		return ErrStaleReign
	default:
		return ErrFutureReign
	}
	if !f.EconomicBacklogClean {
		return ErrBacklogUnevaluated
	}

	if f.Delegation == nil {
		if f.Author != f.CurrentReign.Holder {
			return ErrNotHolder
		}
		return nil
	}

	if err := f.Delegation.validShape(); err != nil {
		return err
	}
	if f.Delegation.Delegator != f.CurrentReign.Holder || f.Delegation.Delegate != f.Author {
		return ErrInvalidDelegation
	}
	if f.Delegation.Season != f.CurrentReign.Season || f.Delegation.Reign != f.CurrentReign.Reign {
		return ErrInvalidDelegation
	}
	if f.Delegation.Competence != f.Competence {
		return ErrInvalidDelegation
	}
	if !moment.Before(f.Delegation.ExpiresAt.UTC()) {
		return ErrDelegationExpired
	}
	return nil
}
