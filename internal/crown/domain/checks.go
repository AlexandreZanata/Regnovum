package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// ApprovalID identifies one independent check. Checks bind an act
// digest so a recorded approval cannot authorize another payload.
type ApprovalID string

// ParseApprovalID validates one approval token.
func ParseApprovalID(raw string) (ApprovalID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return ApprovalID(token), nil
}

// String returns the stored approval value.
func (a ApprovalID) String() string { return string(a) }

// Approval is one independent technical check of an irreversible
// act: which act, by which distinct checker, in which book and
// reign, over which payload digest, when checked and until when,
// and whether it claims urgency. Succession needs no predecessor
// discretion: checks name the current reign, and a pending act from
// another reign fails currency and needs fresh validation.
type Approval struct {
	ID        ApprovalID
	Act       ActID
	Checker   HolderSubject
	Season    SeasonID
	Reign     ReignVersion
	Digest    string
	CheckedAt time.Time
	ExpiresAt time.Time
	Emergency bool
	ReviewDue time.Time
}

// Clearance is one authorized final execution: the act it allows,
// in which book and reign, over which digest, until when. It moves
// nothing itself: later tasks execute against it and revalidate
// authority at the effect.
type Clearance struct {
	Act       ActID
	Season    SeasonID
	Reign     ReignVersion
	Digest    string
	ExpiresAt time.Time
}

// isDigest reports whether raw is 64 lowercase hex digits: the act
// payload seal shape. Hashes never carry PII, only the binding.
func isDigest(raw string) bool {
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

// actDigestOf seals the effect-relevant fields of one validated act:
// identity, authority, kind, reason, target, effect, dates, charter,
// correction link and economic payload. Any change breaks the seal
// first. Urgency review lives on the approvals, never inside the
// act digest.
func actDigestOf(act RoyalAct) string {
	end := ""
	if !act.EndsAt.IsZero() {
		end = act.EndsAt.UTC().Format(time.RFC3339Nano)
	}
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d",
		act.ID, act.Author, act.Season, int(act.Reign), act.Competence,
		act.Kind, act.Reason, act.Target, act.Effect,
		act.DecreedAt.UTC().Format(time.RFC3339Nano),
		act.Effective.UTC().Format(time.RFC3339Nano),
		end,
		act.Charter, act.Corrects, act.Origin, act.Amount,
	)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// ActDigest validates one act and seals its payload digest.
func ActDigest(act RoyalAct) (string, error) {
	sealed, err := DefineAct(act)
	if err != nil {
		return "", err
	}
	return actDigestOf(sealed), nil
}

// checkApprovalShape checks one approval shape: whole tokens, reign
// from 1, digest shape, ordered live window, review due ordered
// when present.
func checkApprovalShape(a Approval) error {
	if _, err := ParseApprovalID(string(a.ID)); err != nil {
		return err
	}
	if _, err := ParseActID(string(a.Act)); err != nil {
		return err
	}
	if _, err := ParseHolderSubject(string(a.Checker)); err != nil {
		return err
	}
	if _, err := ParseSeasonID(string(a.Season)); err != nil {
		return err
	}
	if _, err := ParseReignVersion(int(a.Reign)); err != nil {
		return err
	}
	if !isDigest(a.Digest) {
		return ErrInvalidAuthority
	}
	if a.CheckedAt.IsZero() || a.ExpiresAt.IsZero() {
		return ErrInvalidAuthority
	}
	checked, expires := a.CheckedAt.UTC(), a.ExpiresAt.UTC()
	if !expires.After(checked) {
		return ErrInvalidAuthority
	}
	if !a.ReviewDue.IsZero() && !a.ReviewDue.UTC().After(checked) {
		return ErrInvalidAuthority
	}
	return nil
}

// checkApprovalBinding binds one approval to one sealed act: same
// act, book and reign, same digest, checker distinct from author.
// A predecessor approval never suffices by discretion: the reign
// must match the act, and the act must match the current reign.
func checkApprovalBinding(sealed RoyalAct, digest string, a Approval) error {
	if a.Act != sealed.ID {
		return ErrTamperedAct
	}
	if a.Season != sealed.Season || a.Reign != sealed.Reign {
		return ErrTamperedAct
	}
	if a.Digest != digest {
		return ErrTamperedAct
	}
	if a.Checker == sealed.Author {
		return ErrSelfApproval
	}
	return nil
}

// checkApprovalWindow judges one approval against the call clock:
// live now authorizes, spent or time-travelled refuses.
func checkApprovalWindow(a Approval, now time.Time) error {
	moment := now.UTC()
	checked, expires := a.CheckedAt.UTC(), a.ExpiresAt.UTC()
	if moment.Before(checked) {
		return ErrInvalidAuthority
	}
	if !moment.Before(expires) {
		return ErrCheckExpired
	}
	return nil
}

// checkApprovalPair judges independence: identifiers differ and
// checkers differ. Same identifier replayed or same checker twice
// is not a second opinion.
func checkApprovalPair(first, second Approval) error {
	if first.ID == second.ID {
		return ErrDuplicateApproval
	}
	if first.Checker == second.Checker {
		return ErrDuplicateApproval
	}
	return nil
}

// checkApprovalCurrency judges the act against the invested
// snapshot: same book, same reign still invested. A pending act
// from another reign stops here and needs fresh validation after
// the exchange; no predecessor discretion bypasses it.
func checkApprovalCurrency(sealed RoyalAct, current CurrentReign) error {
	if sealed.Season != current.Season {
		return ErrSeasonMismatch
	}
	switch {
	case sealed.Reign == current.Reign:
		return nil
	case sealed.Reign < current.Reign:
		return ErrStaleReign
	default:
		return ErrFutureReign
	}
}

// checkApprovalEmergency judges urgency: emergency only contains
// temporarily with scheduled review. An emergency check without
// review due, or over an act without temporary vigour, blocks final
// execution.
func checkApprovalEmergency(sealed RoyalAct, first, second Approval) error {
	if !first.Emergency && !second.Emergency {
		return nil
	}
	if sealed.EndsAt.IsZero() {
		return ErrEmergencyWithoutReview
	}
	if first.Emergency && first.ReviewDue.IsZero() {
		return ErrEmergencyWithoutReview
	}
	if second.Emergency && second.ReviewDue.IsZero() {
		return ErrEmergencyWithoutReview
	}
	return nil
}

// minExpiry answers the earlier of two expiries in UTC.
func minExpiry(first, second time.Time) time.Time {
	one, other := first.UTC(), second.UTC()
	if one.Before(other) {
		return one
	}
	return other
}

// RequireIndependentCheck authorizes final execution of one
// irreversible act after two independent checks. Every refusal
// arrives before any effect: self-approval, identical or replayed
// approvals, altered payloads, expired checks, reign exchanges and
// urgency without review all stop here. Amounts stay bounded by
// strconv: the digest binds the exact economic payload T02 sealed.
func RequireIndependentCheck(act RoyalAct, first, second Approval, current CurrentReign, now time.Time) (Clearance, error) {
	if now.IsZero() {
		return Clearance{}, ErrInvalidAuthority
	}
	sealed, err := DefineAct(act)
	if err != nil {
		return Clearance{}, err
	}
	digest := actDigestOf(sealed)
	if err := current.validShape(); err != nil {
		return Clearance{}, err
	}
	if err := checkApprovalCurrency(sealed, current); err != nil {
		return Clearance{}, err
	}
	if err := checkApprovalShape(first); err != nil {
		return Clearance{}, err
	}
	if err := checkApprovalShape(second); err != nil {
		return Clearance{}, err
	}
	if err := checkApprovalPair(first, second); err != nil {
		return Clearance{}, err
	}
	if err := checkApprovalBinding(sealed, digest, first); err != nil {
		return Clearance{}, err
	}
	if err := checkApprovalBinding(sealed, digest, second); err != nil {
		return Clearance{}, err
	}
	if err := checkApprovalWindow(first, now); err != nil {
		return Clearance{}, err
	}
	if err := checkApprovalWindow(second, now); err != nil {
		return Clearance{}, err
	}
	if err := checkApprovalEmergency(sealed, first, second); err != nil {
		return Clearance{}, err
	}
	return Clearance{
		Act: sealed.ID, Season: sealed.Season, Reign: sealed.Reign,
		Digest: digest, ExpiresAt: minExpiry(first.ExpiresAt, second.ExpiresAt),
	}, nil
}
