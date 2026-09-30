package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NewcomerStatus names the lifecycle of one crumb grant: admitted,
// blocked, appealed, cancelled or erased. Admitting and appealing
// move no value: even an admitted newcomer only records the right
// to exactly one future distribution, decided by later tasks.
// Cancellation withdraws that right before distribution; erasure
// removes the account identifier per policy while keeping the
// uniqueness digest so one person never counts twice.
type NewcomerStatus string

const (
	// NewcomerAdmitted records one eligible newcomer: a new account,
	// an active standing, a clear proportional antifraud check and
	// a person digest never seen before. Exactly one per person.
	NewcomerAdmitted NewcomerStatus = "admitted"
	// NewcomerBlocked withholds a grant: a blocked account, a failed
	// antifraud check or a later account of an already admitted
	// person. Appealable by the holder, never an automatic grant.
	NewcomerBlocked NewcomerStatus = "blocked"
	// NewcomerAppealed records the holder challenge of a blocked
	// grant: the right to be heard, still with no grant.
	NewcomerAppealed NewcomerStatus = "appealed"
	// NewcomerCancelled withdraws an admitted, blocked or appealed
	// grant before any distribution: terminal, with no payment.
	NewcomerCancelled NewcomerStatus = "cancelled"
	// NewcomerErased removes the account identifier per retention
	// policy: the uniqueness digest and epoch stay so the person
	// never counts twice. Terminal.
	NewcomerErased NewcomerStatus = "erased"
)

// ParseNewcomerStatus validates a status against the closed machine.
func ParseNewcomerStatus(raw string) (NewcomerStatus, error) {
	status := NewcomerStatus(raw)
	switch status {
	case NewcomerAdmitted, NewcomerBlocked, NewcomerAppealed, NewcomerCancelled, NewcomerErased:
		return status, nil
	default:
		return "", ErrInvalidNewcomer
	}
}

// Terminal reports whether the grant settles nothing further:
// cancelled and erased grants never reopen.
func (s NewcomerStatus) Terminal() bool {
	return s == NewcomerCancelled || s == NewcomerErased
}

// String returns the stored status value.
func (s NewcomerStatus) String() string { return string(s) }

// maxNewcomerRunes bounds opaque newcomer tokens: long enough for
// operation tokens, short enough to stay out of log abuse.
const maxNewcomerRunes = 128

// AdmitRequest carries one newcomer claim. Every value arrives from
// the caller: no ratified standing, antifraud threshold or activity
// lives here. There is deliberately no activity field — posts, likes
// and work never decide admission — and no PII field: the person
// travels only as a hex64 digest, never as name, document or face.
type AdmitRequest struct {
	Account         string
	PersonProofHash string
	EpochKey        string
	AccountActive   bool
	AntifraudClear  bool
}

// Grant is one newcomer decision: the opaque account, the minimal
// uniqueness digest, the epoch it was claimed in and the lifecycle
// status, sealed by hash. The seal binds account, digest and epoch:
// any reclassification breaks the seal first. Status moves freely
// without breaking it.
type Grant struct {
	Account    string
	PersonHash string
	EpochKey   string
	Status     NewcomerStatus
	Hash       string
}

// parseNewcomerToken validates one opaque newcomer token: exact
// match, no control characters, bounded length.
func parseNewcomerToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidNewcomer
	}
	if utf8.RuneCountInString(raw) > maxNewcomerRunes {
		return "", ErrInvalidNewcomer
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidNewcomer
		}
	}
	return raw, nil
}

// isNewcomerHex64 reports whether raw is 64 lowercase hex digits:
// the minimized uniqueness proof shape. No PII ever passes here,
// only its digest.
func isNewcomerHex64(raw string) bool {
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

// sealNewcomer binds account, digest and epoch: the canonical bytes
// any holder recomputes to detect tampering or reclassification.
func sealNewcomer(account, personHash, epochKey string) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%s", account, personHash, epochKey)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// buildGrant seals one grant in the given status.
func buildGrant(account, personHash, epochKey string, status NewcomerStatus) Grant {
	return Grant{
		Account: account, PersonHash: personHash, EpochKey: epochKey,
		Status: status, Hash: sealNewcomer(account, personHash, epochKey),
	}
}

// validateAdmitRequest checks the claim shape: opaque account, digest
// proof and a sealable epoch. Q25 stays PENDENTE
// (docs/reino/DECISOES_VIGENTES.md): standing and antifraud arrive
// as booleans per call, never as ratified thresholds here.
func validateAdmitRequest(req AdmitRequest) (account, personHash, epochKey string, err error) {
	account, err = parseNewcomerToken(req.Account)
	if err != nil {
		return "", "", "", err
	}
	if !isNewcomerHex64(req.PersonProofHash) {
		return "", "", "", ErrInvalidNewcomer
	}
	if _, err = ParseEpochKey(req.EpochKey); err != nil {
		return "", "", "", ErrInvalidNewcomer
	}
	return account, req.PersonProofHash, req.EpochKey, nil
}

// admittedMatch reports how one claim relates to ever-admitted
// grants: an exact replay, a person already counted, or an account
// epoch pair claiming a different person.
func admittedMatch(admitted []Grant, account, personHash, epochKey string) (replay Grant, replayed, duplicate, conflict bool) {
	for _, g := range admitted {
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
	return Grant{}, false, duplicate, conflict
}

// AdmitNewcomer records one eligible newcomer exactly once per
// person: a new account with active standing, a clear proportional
// antifraud check and a person digest never admitted before. The
// admitted slice holds ever-admitted grants (including cancelled
// and erased: withdrawing never frees a second grant); blocked
// attempts never join it.
//
// An exact replay (same account, digest and epoch) returns the
// recorded grant idempotently. A later account of the same person —
// including two simultaneous accounts — blocks with
// ErrDuplicateNewcomer and appeal. A blocked standing or a failed
// antifraud check blocks with ErrAccountBlocked and appeal. A same
// account epoch pair claiming a different person refuses without a
// grant. No activity is read: admission never asks for posts, likes
// or work.
func AdmitNewcomer(req AdmitRequest, admitted []Grant) (Grant, error) {
	account, personHash, epochKey, err := validateAdmitRequest(req)
	if err != nil {
		return Grant{}, err
	}
	replay, replayed, duplicate, conflict := admittedMatch(admitted, account, personHash, epochKey)
	if replayed {
		return replay, nil
	}
	if duplicate {
		return buildGrant(account, personHash, epochKey, NewcomerBlocked), ErrDuplicateNewcomer
	}
	if conflict {
		return Grant{}, ErrInvalidNewcomer
	}
	if !req.AccountActive || !req.AntifraudClear {
		return buildGrant(account, personHash, epochKey, NewcomerBlocked), ErrAccountBlocked
	}
	return buildGrant(account, personHash, epochKey, NewcomerAdmitted), nil
}

// VerifyHash recomputes the seal and refuses a grant whose terms no
// longer agree.
func (g Grant) VerifyHash() error {
	if g.Hash == "" || sealNewcomer(g.Account, g.PersonHash, g.EpochKey) != g.Hash {
		return ErrInvalidNewcomer
	}
	return nil
}

// Appeal records the holder challenge of a blocked grant: blocked
// grants only, by the holder only. Strangers never speak for a
// grant, and terminal or admitted grants never appeal. Appealing
// moves no value and grants nothing.
func (g Grant) Appeal(by string) (Grant, error) {
	if err := g.VerifyHash(); err != nil {
		return Grant{}, err
	}
	if g.Status != NewcomerBlocked {
		return Grant{}, ErrNewcomerState
	}
	if by != g.Account || strings.TrimSpace(by) == "" {
		return Grant{}, ErrNewcomerNotParty
	}
	next := g
	next.Status = NewcomerAppealed
	next.Hash = sealNewcomer(next.Account, next.PersonHash, next.EpochKey)
	return next, nil
}

// Cancel withdraws an admitted, blocked or appealed grant before any
// distribution: terminal, with no payment. Cancelled and erased
// grants never cancel again and never reopen.
func (g Grant) Cancel() (Grant, error) {
	if err := g.VerifyHash(); err != nil {
		return Grant{}, err
	}
	if g.Status != NewcomerAdmitted && g.Status != NewcomerBlocked && g.Status != NewcomerAppealed {
		return Grant{}, ErrNewcomerState
	}
	next := g
	next.Status = NewcomerCancelled
	next.Hash = sealNewcomer(next.Account, next.PersonHash, next.EpochKey)
	return next, nil
}

// Erase removes the account identifier per retention policy: the
// uniqueness digest and epoch stay so the person never counts
// twice, and the seal is recomputed over the erased form. Any
// non-erased grant may erase; erased grants never erase again and
// never reopen. Erasure grants nothing and frees nothing.
func (g Grant) Erase() (Grant, error) {
	if err := g.VerifyHash(); err != nil {
		return Grant{}, err
	}
	if g.Status == NewcomerErased {
		return Grant{}, ErrNewcomerState
	}
	next := g
	next.Account = ""
	next.Status = NewcomerErased
	next.Hash = sealNewcomer(next.Account, next.PersonHash, next.EpochKey)
	return next, nil
}
