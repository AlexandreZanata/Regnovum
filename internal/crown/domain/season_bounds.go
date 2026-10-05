package domain

import (
	"strings"
	"time"
)

// SeasonalWindowSeconds binds the crown to the published 90-day
// calendar: 90 × 24 × 60 × 60 seconds in UTC. The crown never
// stretches, shortens or reinterprets the window: a divergent end
// refuses as a prohibited alteration, never as a quiet adjustment.
const SeasonalWindowSeconds = 90 * 24 * 60 * 60

// SuccessionReasonInitialInvestiture names the first investiture of
// one season book from its published manifesto. It is never a
// conquest: no wealth was measured, no rival was beaten, and the
// outcome records the registered policy instead of a fictitious
// victory.
const SuccessionReasonInitialInvestiture SuccessionReason = "initial-investiture"

// InitialInvestitureInput carries the published manifesto terms of
// one successor book plus the predecessor seal state and the
// founder eligibility resolved by the caller through the P47-T05
// verifiable-consent path. Wealth never travels here: every new
// book starts at zero and old-season P/C never enter the decision.
type InitialInvestitureInput struct {
	Policy            WealthPolicyVersion
	Season            SeasonID
	StartsAt          time.Time
	EndsAt            time.Time
	PredecessorSealed bool
	InitialMonarch    HolderSubject
	Regent            HolderSubject
	MonarchPresent    bool
	MonarchEligible   bool
}

// InitialReignOutcome is the first invested authority of one book:
// reign 1, zero measured wealth, the registered policy and either
// the published founder or the limited technical regent.
type InitialReignOutcome struct {
	Season           SeasonID
	Holder           HolderSubject
	Predecessor      HolderSubject
	Reign            ReignVersion
	IsRegent         bool
	Reason           SuccessionReason
	Policy           WealthPolicyVersion
	WinningWealth    int64
	AttainedRevision int64
}

// DecideInitialReign invests the first authority of one season book
// from its manifesto, without inherited wealth or office.
//
// Invariants (P47-T07 / TEMPORADAS_SUCESSAO.md §§3–4,8):
//  1. The window is exactly 90 days: a stretched or shortened end
//     refuses with ErrProhibitedAlteration.
//  2. A successor never opens over an unsealed predecessor: without
//     the seal the economy stays blocked with ErrSeasonClosed.
//  3. The founder comes from initial_monarch_account_id only when
//     present and eligible; otherwise the published regent assumes
//     limited authority; otherwise zero kings with
//     ErrNoQualifiedSuccessor — never a fictitious conquest.
//  4. Measured wealth is always zero: old-season P never triggers
//     the new algorithm and no balance is carried.
func DecideInitialReign(in InitialInvestitureInput) (InitialReignOutcome, error) {
	if err := checkInitialReignWindow(in); err != nil {
		return InitialReignOutcome{}, err
	}
	if err := checkInitialReignParties(in); err != nil {
		return InitialReignOutcome{}, err
	}
	return decideInitialHolder(in)
}

// checkInitialReignWindow judges the calendar half of one
// investiture: a ratified policy, a whole season, a non-empty
// window of exactly 90 days and a sealed predecessor. Anything
// else keeps the economy blocked.
func checkInitialReignWindow(in InitialInvestitureInput) error {
	if _, err := ParseWealthPolicyVersion(string(in.Policy)); err != nil {
		return err
	}
	if _, err := ParseSeasonID(string(in.Season)); err != nil {
		return err
	}
	if in.StartsAt.IsZero() || in.EndsAt.IsZero() {
		return ErrInvalidAuthority
	}
	start, end := in.StartsAt.UTC(), in.EndsAt.UTC()
	if !end.After(start) {
		return ErrInvalidAuthority
	}
	if end.Sub(start) != SeasonalWindowSeconds*time.Second {
		return ErrProhibitedAlteration
	}
	if !in.PredecessorSealed {
		return ErrSeasonClosed
	}
	return nil
}

// checkInitialReignParties judges the holder half of one
// investiture: whole founder and regent tokens outside the
// institutional subject, never the same account twice and never
// both absent.
func checkInitialReignParties(in InitialInvestitureInput) error {
	if err := checkInitialReignParty(in.InitialMonarch); err != nil {
		return err
	}
	if err := checkInitialReignParty(in.Regent); err != nil {
		return err
	}
	if in.InitialMonarch != "" && in.InitialMonarch == in.Regent {
		return ErrAmbiguousFixture
	}
	if in.InitialMonarch == "" && in.Regent == "" {
		return ErrAmbiguousFixture
	}
	return nil
}

// checkInitialReignParty judges one published holder token: absent
// is allowed here, present must be whole and never the
// institutional Crown.
func checkInitialReignParty(holder HolderSubject) error {
	if holder == "" {
		return nil
	}
	if _, err := ParseHolderSubject(string(holder)); err != nil {
		return err
	}
	if holder == InstitutionalCrownSubject {
		return ErrAmbiguousFixture
	}
	return nil
}

// decideInitialHolder invests the first authority once the window
// and the parties check out: the present and eligible founder, else
// the published regent with the founder linked, else zero kings.
func decideInitialHolder(in InitialInvestitureInput) (InitialReignOutcome, error) {
	if in.MonarchPresent && in.MonarchEligible && in.InitialMonarch != "" {
		return InitialReignOutcome{
			Season: in.Season, Holder: in.InitialMonarch,
			Reign: 1, IsRegent: false,
			Reason:           SuccessionReasonInitialInvestiture,
			Policy:           in.Policy,
			WinningWealth:    0,
			AttainedRevision: 0,
		}, nil
	}
	if in.Regent != "" {
		predecessor := HolderSubject("")
		if in.MonarchPresent && in.InitialMonarch != "" {
			predecessor = in.InitialMonarch
		}
		return InitialReignOutcome{
			Season: in.Season, Holder: in.Regent, Predecessor: predecessor,
			Reign: 1, IsRegent: true,
			Reason:           SuccessionReasonRegency,
			Policy:           in.Policy,
			WinningWealth:    0,
			AttainedRevision: 0,
		}, nil
	}
	return InitialReignOutcome{}, ErrNoQualifiedSuccessor
}

// IsSeasonalAuthorityLive reports whether one invested reign may
// still decide at the given instant: the book is open and the
// instant falls in [starts_at, ends_at). The exact end already
// belongs out: an act on the final tick refuses with
// ErrSeasonClosed instead of stretching the calendar.
func IsSeasonalAuthorityLive(current CurrentReign, now time.Time) error {
	if err := current.validShape(); err != nil {
		return err
	}
	if now.IsZero() {
		return ErrInvalidAuthority
	}
	if !current.Open || !current.contains(now.UTC()) {
		return ErrSeasonClosed
	}
	return nil
}

// GuardSuccessionAgainstClose blocks the succession evaluator while
// the closing barrier owns the book: an unevaluated backlog, a
// closed book or an instant at/past the exclusive end refuses with
// ErrSeasonClosed or ErrBacklogUnevaluated before any investiture.
// Concurrent succession and close therefore never produce two
// authorities: close wins and succession waits.
func GuardSuccessionAgainstClose(open bool, endsAt, now time.Time, backlogClean bool) error {
	if now.IsZero() || endsAt.IsZero() {
		return ErrInvalidAuthority
	}
	if !backlogClean {
		return ErrBacklogUnevaluated
	}
	if !open || !now.UTC().Before(endsAt.UTC()) {
		return ErrSeasonClosed
	}
	return nil
}

// TerminateReignAtCutoff ends reigns and game offices at the cutoff:
// past the exclusive end (or with the book closed) the returned
// reign is closed and only permitted technical liquidation may run.
// Inside the window with the book open the reign passes through
// unchanged. Offices never survive the end without fresh
// designation in the successor book.
func TerminateReignAtCutoff(current CurrentReign, now time.Time) (terminated CurrentReign, technicalOnly bool) {
	if now.IsZero() {
		closed := current
		closed.Open = false
		return closed, true
	}
	if err := current.validShape(); err != nil {
		closed := current
		closed.Open = false
		return closed, true
	}
	if current.Open && current.contains(now.UTC()) {
		return current, false
	}
	closed := current
	closed.Open = false
	return closed, true
}

// AssertSingleAuthority proves there is zero or one authorized King
// for one state: two distinct live holders refuse with
// ErrActiveReignConflict. Empty and duplicated entries collapse to
// one; anything else is a second throne.
func AssertSingleAuthority(holders []HolderSubject) error {
	seen := make(map[HolderSubject]bool, len(holders))
	for _, h := range holders {
		if h == "" {
			continue
		}
		if strings.TrimSpace(string(h)) != string(h) {
			return ErrInvalidAuthority
		}
		if _, err := ParseHolderSubject(string(h)); err != nil {
			return err
		}
		seen[h] = true
	}
	if len(seen) > 1 {
		return ErrActiveReignConflict
	}
	return nil
}
