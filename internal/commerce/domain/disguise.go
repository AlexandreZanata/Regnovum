package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// DisguiseReason names the minimized signal that a personal gift
// may carry consideration: a contract, an announcement, a delivery
// or a recurrence. The four come from RESPOSTAS §D item 24: they
// may prove disguised trade, but a contestable classification never
// authorizes automatic seizure. The reason travels sealed in the
// flag: it routes review, never prices.
type DisguiseReason string

const (
	// DisguiseContract signals a service object behind the gift:
	// a written or agreed exchange.
	DisguiseContract DisguiseReason = "contract"
	// DisguiseAnnouncement signals a public offer behind the
	// gift: the transfer answers an ad.
	DisguiseAnnouncement DisguiseReason = "announcement"
	// DisguiseDelivery signals a delivered service behind the
	// gift: value already changed hands.
	DisguiseDelivery DisguiseReason = "delivery"
	// DisguiseRecurrence signals a repeated pattern behind the
	// gift: cadence suggesting price, not present.
	DisguiseRecurrence DisguiseReason = "recurrence"
)

// AllDisguiseReasons returns the closed vocabulary in canonical order.
func AllDisguiseReasons() []DisguiseReason {
	return []DisguiseReason{DisguiseContract, DisguiseAnnouncement, DisguiseDelivery, DisguiseRecurrence}
}

// ParseDisguiseReason validates a reason against the closed vocabulary.
// Matching is exact: no trimming, no case folding, no combined values.
func ParseDisguiseReason(raw string) (DisguiseReason, error) {
	reason := DisguiseReason(raw)
	switch reason {
	case DisguiseContract, DisguiseAnnouncement, DisguiseDelivery, DisguiseRecurrence:
		return reason, nil
	default:
		return "", ErrInvalidDisguise
	}
}

// String returns the stored reason value.
func (r DisguiseReason) String() string { return string(r) }

// DisguiseStatus names the review lifecycle of one flagged gift.
// Flagging and contesting move no value: even a confirmed fraud
// records an act with an explicit charge and an appeal window, never
// an automatic debit. Dismissal is the false-positive path and moves
// nothing at all.
type DisguiseStatus string

const (
	// DisguiseFlagged is a signalled gift awaiting review: no value
	// has moved and no tithe has been charged.
	DisguiseFlagged DisguiseStatus = "flagged"
	// DisguiseContested is a flagged gift the payer or payee
	// challenged: the right to be heard, recorded without moving.
	DisguiseContested DisguiseStatus = "contested"
	// DisguiseDismissed clears the flag as a false positive:
	// terminal, with no transfer of INK.
	DisguiseDismissed DisguiseStatus = "dismissed"
	// DisguiseConfirmed upholds the flag as disguised trade:
	// terminal, recording the contractual basis, the trail and the
	// appeal window with the explicit charge owed, never a seizure.
	DisguiseConfirmed DisguiseStatus = "confirmed"
)

// ParseDisguiseStatus validates a status against the closed machine.
func ParseDisguiseStatus(raw string) (DisguiseStatus, error) {
	status := DisguiseStatus(raw)
	switch status {
	case DisguiseFlagged, DisguiseContested, DisguiseDismissed, DisguiseConfirmed:
		return status, nil
	default:
		return "", ErrInvalidDisguise
	}
}

// Terminal reports whether the review settles nothing further:
// dismissed and confirmed reviews never reopen.
func (s DisguiseStatus) Terminal() bool {
	return s == DisguiseDismissed || s == DisguiseConfirmed
}

// String returns the stored status value.
func (s DisguiseStatus) String() string { return string(s) }

// DisguiseDecision names the competent outcome for a flagged or
// contested review: clear it or uphold it.
type DisguiseDecision string

const (
	// DisguiseDismiss clears the flag as a false positive.
	DisguiseDismiss DisguiseDecision = "dismiss"
	// DisguiseConfirm upholds the flag as disguised trade with an
	// explicit act.
	DisguiseConfirm DisguiseDecision = "confirm"
)

// ParseDisguiseDecision validates a review outcome.
func ParseDisguiseDecision(raw string) (DisguiseDecision, error) {
	decision := DisguiseDecision(raw)
	switch decision {
	case DisguiseDismiss, DisguiseConfirm:
		return decision, nil
	default:
		return "", ErrInvalidDisguise
	}
}

// String returns the stored decision value.
func (d DisguiseDecision) String() string { return string(d) }

// maxDisguiseBasisRunes bounds the contractual basis of a confirmed
// act: long enough to name the contract, short enough to stay out
// of log abuse. Evidence itself never travels here: only its hash.
const maxDisguiseBasisRunes = 280

// DisguiseFlag is one flagged gift: the transfer it names, the
// closed reason, the minimized evidence hash, the reporter and the
// lifecycle status. The hash seals review key, transfer, reason,
// evidence and reporter: any reclassification breaks the seal first.
type DisguiseFlag struct {
	ReviewKey    string
	TransferKey  string
	Payer        string
	Payee        string
	AmountMill   int64
	Reason       DisguiseReason
	EvidenceHash string
	Reporter     string
	Status       DisguiseStatus
	Hash         string
}

// FlagRequest carries the fields of one signal. Every value arrives
// from the caller: no ratified reason, amount or reporter lives here.
type FlagRequest struct {
	ReviewKey    string
	TransferKey  string
	Payer        string
	Payee        string
	AmountMill   int64
	Reason       DisguiseReason
	EvidenceHash string
	Reporter     string
}

// isHex64 reports whether raw is 64 lowercase hex digits: the
// minimized proof shape. No PII ever passes here, only its digest.
func isHex64(raw string) bool {
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

// parseDisguiseToken validates one opaque review token: exact match,
// no control characters, bounded length.
func parseDisguiseToken(raw string) (string, error) {
	return parseToken(raw, ErrInvalidDisguise)
}

// parseDisguiseBasis validates the contractual basis of a confirmed
// act: exact match, bounded like a contract object, never blank.
func parseDisguiseBasis(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidDisguise
	}
	if utf8.RuneCountInString(raw) > maxDisguiseBasisRunes {
		return "", ErrInvalidDisguise
	}
	for _, r := range raw {
		if r < 32 || r == 127 {
			return "", ErrInvalidDisguise
		}
	}
	return raw, nil
}

// FlagDisguise seals one disguise signal in flagged state. Blank
// tokens, unknown reasons, non-hash evidence and non-positive
// amounts refuse before anything is stored. Flagging never prices
// and never moves value: the gift stays a gift until a competent
// act says otherwise, and even then by explicit charge, never by
// seizure.
func FlagDisguise(req FlagRequest) (DisguiseFlag, error) {
	reviewKey, err := parseDisguiseToken(req.ReviewKey)
	if err != nil {
		return DisguiseFlag{}, err
	}
	transferKey, err := parseDisguiseToken(req.TransferKey)
	if err != nil {
		return DisguiseFlag{}, err
	}
	payer, err := parseDisguiseToken(req.Payer)
	if err != nil {
		return DisguiseFlag{}, err
	}
	payee, err := parseDisguiseToken(req.Payee)
	if err != nil {
		return DisguiseFlag{}, err
	}
	if payer == payee {
		return DisguiseFlag{}, ErrInvalidDisguise
	}
	if req.AmountMill <= 0 {
		return DisguiseFlag{}, ErrInvalidDisguise
	}
	if _, err := ParseDisguiseReason(string(req.Reason)); err != nil {
		return DisguiseFlag{}, err
	}
	if !isHex64(req.EvidenceHash) {
		return DisguiseFlag{}, ErrInvalidDisguise
	}
	reporter, err := parseDisguiseToken(req.Reporter)
	if err != nil {
		return DisguiseFlag{}, err
	}
	flag := DisguiseFlag{
		ReviewKey: reviewKey, TransferKey: transferKey,
		Payer: payer, Payee: payee, AmountMill: req.AmountMill,
		Reason: req.Reason, EvidenceHash: req.EvidenceHash,
		Reporter: reporter, Status: DisguiseFlagged,
	}
	flag.Hash = sealDisguise(flag)
	return flag, nil
}

// sealDisguise binds review key, transfer, parties, amount, reason,
// evidence and reporter: the canonical bytes any holder recomputes
// to detect tampering or reclassification.
func sealDisguise(flag DisguiseFlag) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s",
		flag.ReviewKey, flag.TransferKey, flag.Payer, flag.Payee,
		flag.AmountMill, flag.Reason.String(), flag.EvidenceHash, flag.Reporter)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// VerifyHash recomputes the seal and refuses a flag whose terms no
// longer agree.
func (f DisguiseFlag) VerifyHash() error {
	if f.Hash == "" || sealDisguise(f) != f.Hash {
		return ErrInvalidDisguise
	}
	return nil
}

// Contest records the payer or payee challenge: flagged reviews
// only. Strangers never speak for a transfer, and terminal reviews
// never reopen. Contesting moves no value.
func (f DisguiseFlag) Contest(by string) (DisguiseFlag, error) {
	if err := f.VerifyHash(); err != nil {
		return DisguiseFlag{}, err
	}
	if f.Status != DisguiseFlagged {
		return DisguiseFlag{}, ErrDisguiseState
	}
	if by != f.Payer && by != f.Payee {
		return DisguiseFlag{}, ErrDisguiseNotParty
	}
	if strings.TrimSpace(by) == "" {
		return DisguiseFlag{}, ErrDisguiseNotParty
	}
	next := f
	next.Status = DisguiseContested
	next.Hash = sealDisguise(next)
	return next, nil
}

// ResolveRequest carries one competent outcome: the decision, the
// contractual basis and trail for confirmations, and the appeal
// window both outcomes owe the parties.
type ResolveRequest struct {
	Decision    DisguiseDecision
	Basis       string
	TrailHash   string
	AppealUntil time.Time
	Now         time.Time
}

// Resolve settles a flagged or contested review exactly once:
// dismiss clears a false positive with no movement, confirm upholds
// disguised trade with an explicit act. Confirmations need the
// contractual basis and the trail hash; both outcomes need a future
// appeal instant so the parties keep their right to resource.
// Resolving never debits by itself: the charge is recorded owed,
// never seized.
func (f DisguiseFlag) Resolve(req ResolveRequest) (DisguiseFlag, error) {
	if err := f.VerifyHash(); err != nil {
		return DisguiseFlag{}, err
	}
	if f.Status != DisguiseFlagged && f.Status != DisguiseContested {
		return DisguiseFlag{}, ErrDisguiseState
	}
	if req.Decision != DisguiseDismiss && req.Decision != DisguiseConfirm {
		return DisguiseFlag{}, ErrInvalidDisguise
	}
	if req.Now.IsZero() || req.AppealUntil.IsZero() || !req.AppealUntil.After(req.Now) {
		return DisguiseFlag{}, ErrInvalidDisguise
	}
	if req.Decision == DisguiseDismiss {
		if strings.TrimSpace(req.Basis) != "" || strings.TrimSpace(req.TrailHash) != "" {
			return DisguiseFlag{}, ErrInvalidDisguise
		}
		next := f
		next.Status = DisguiseDismissed
		next.Hash = sealDisguise(next)
		return next, nil
	}
	if _, err := parseDisguiseBasis(req.Basis); err != nil {
		return DisguiseFlag{}, err
	}
	if !isHex64(req.TrailHash) {
		return DisguiseFlag{}, ErrInvalidDisguise
	}
	next := f
	next.Status = DisguiseConfirmed
	next.Hash = sealDisguise(next)
	return next, nil
}

// DisguiseCharge computes the explicit tithe owed when disguised
// trade is confirmed: `floor(amount × 10 / 100)` in milliINK, the
// same function as the settlement tithe. For integer milliINK this
// equals `amount / 10` with truncation, computed by division only so
// amounts near MaxInt64 never overflow through `×10`. The charge is
// recorded owed with basis, trail and appeal: it is never seized
// here.
func DisguiseCharge(amountMill int64) (int64, error) {
	if amountMill <= 0 {
		return 0, ErrInvalidDisguise
	}
	return amountMill / 10, nil
}
