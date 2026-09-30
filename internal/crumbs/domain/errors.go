package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	// CodeInvalidEpoch names a malformed or nonexistent weekly epoch:
	// bad shape, out-of-range week or W53 in a 52-week year.
	CodeInvalidEpoch ErrorCode = "CRUMBS_INVALID_EPOCH"
	// CodeInvalidReflux names a malformed reflux query: an empty or
	// inverted window settles nothing.
	CodeInvalidReflux ErrorCode = "CRUMBS_INVALID_REFLUX"
	// CodeInvalidBudget names a malformed weekly budget query: more
	// than four sealed nets or a negative free Treasury.
	CodeInvalidBudget ErrorCode = "CRUMBS_INVALID_BUDGET"
	// CodeNegativeR4Blocked names a negative R4 median: refunds
	// outran revenue, so weekly distribution blocks until a rule is
	// ratified, never moving a negative amount.
	CodeNegativeR4Blocked ErrorCode = "CRUMBS_NEGATIVE_R4_BLOCKED"
	// CodeInvalidNewcomer names a malformed newcomer claim: blank or
	// abusive tokens, a non-hash uniqueness proof or an epoch that
	// cannot seal.
	CodeInvalidNewcomer ErrorCode = "CRUMBS_INVALID_NEWCOMER"
	// CodeDuplicateNewcomer names the second grant of one person:
	// one concession per person, the later account blocks appealable.
	CodeDuplicateNewcomer ErrorCode = "CRUMBS_DUPLICATE_NEWCOMER"
	// CodeAccountBlocked names a withheld grant: a blocked account
	// or a failed proportional antifraud check, appealable, never
	// an automatic grant.
	CodeAccountBlocked ErrorCode = "CRUMBS_ACCOUNT_BLOCKED"
	// CodeNewcomerState names an illegal lifecycle move: a terminal
	// grant reopening or an appeal outside the blocked state.
	CodeNewcomerState ErrorCode = "CRUMBS_NEWCOMER_STATE"
	// CodeNewcomerNotParty names a stranger speaking for a grant:
	// only the holder appeals its own blocked grant.
	CodeNewcomerNotParty ErrorCode = "CRUMBS_NEWCOMER_NOT_PARTY"
)

// DomainError represents an invariant or rule failure in the crumbs domain.
type DomainError struct {
	Code    ErrorCode
	Message string
}

func (e DomainError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e DomainError) Is(target error) bool {
	t, ok := target.(DomainError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

var (
	// ErrInvalidEpoch refuses a weekly epoch that cannot exist: the
	// seal barrier rejects it before any week is read or closed.
	ErrInvalidEpoch = DomainError{
		Code:    CodeInvalidEpoch,
		Message: "weekly epoch needs an ISO year and a week the year holds: W53 in a 52-week year never seals",
	}
	// ErrInvalidReflux refuses a reflux window that cannot hold a
	// sum: the query needs two ordered instants.
	ErrInvalidReflux = DomainError{
		Code:    CodeInvalidReflux,
		Message: "regular reflux needs an ordered [start, end) window: empty or inverted windows sum nothing",
	}
	// ErrInvalidBudget refuses a weekly budget that cannot exist:
	// more than four sealed nets or a negative free Treasury.
	ErrInvalidBudget = DomainError{
		Code:    CodeInvalidBudget,
		Message: "weekly budget needs up to four sealed nets and a non-negative free Treasury: unsealed weeks never count",
	}
	// ErrNegativeR4Blocked refuses to fund a week from a negative
	// R4 median: the deficit stays visible and distribution waits
	// for a ratified rule, never transferring a negative amount.
	ErrNegativeR4Blocked = DomainError{
		Code:    CodeNegativeR4Blocked,
		Message: "negative R4 blocks weekly distribution until a rule is ratified: no negative transfer leaves the Treasury",
	}
	// ErrInvalidNewcomer refuses a newcomer claim that cannot name
	// a grant: abusive tokens, a non-digest proof or an epoch that
	// never seals.
	ErrInvalidNewcomer = DomainError{
		Code:    CodeInvalidNewcomer,
		Message: "newcomer grant needs an opaque account, a hex64 uniqueness digest and a sealable epoch: PII and activity never travel here",
	}
	// ErrDuplicateNewcomer withholds the second grant of one
	// person: the later account blocks with appeal, never a second
	// payment.
	ErrDuplicateNewcomer = DomainError{
		Code:    CodeDuplicateNewcomer,
		Message: "one concession per person: the later account blocks with contestation, never a second grant",
	}
	// ErrAccountBlocked withholds a grant from a blocked account or
	// a failed antifraud check: appealable, never automatic.
	ErrAccountBlocked = DomainError{
		Code:    CodeAccountBlocked,
		Message: "blocked account or failed antifraud check blocks the grant with appeal: strangers never decide",
	}
	// ErrNewcomerState refuses an illegal lifecycle move: terminal
	// grants never reopen and only blocked grants appeal.
	ErrNewcomerState = DomainError{
		Code:    CodeNewcomerState,
		Message: "newcomer lifecycle moves once: terminal grants never reopen and only blocked grants appeal",
	}
	// ErrNewcomerNotParty refuses a stranger speaking for a grant:
	// only the holder appeals its own blocked grant.
	ErrNewcomerNotParty = DomainError{
		Code:    CodeNewcomerNotParty,
		Message: "only the holder appeals its own blocked grant: strangers never speak for a newcomer",
	}
)
