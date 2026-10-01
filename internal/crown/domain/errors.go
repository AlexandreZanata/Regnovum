package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for crown rule violations.
type ErrorCode string

const (
	// CodeInvalidAuthority names a malformed or partial credential:
	// a blank or abusive token, a zero reign, a missing instant or
	// a session window no policy could have issued.
	CodeInvalidAuthority ErrorCode = "CROWN_INVALID_AUTHORITY"
	// CodeOperatorNotSovereign names an operator credential offered
	// as a game office: the technical administrator never becomes
	// King by alias.
	CodeOperatorNotSovereign ErrorCode = "CROWN_OPERATOR_NOT_SOVEREIGN"
	// CodeSeasonMismatch names a claim outside the current book: the
	// authority of one season never decides in another.
	CodeSeasonMismatch ErrorCode = "CROWN_SEASON_MISMATCH"
	// CodeSeasonClosed names a claim outside the open window: before
	// the start, at or past the exclusive end, or while suspended.
	CodeSeasonClosed ErrorCode = "CROWN_SEASON_CLOSED"
	// CodeStaleReign names a claim from a superseded reign: a new
	// investiture revokes the previous holder without touching any
	// account.
	CodeStaleReign ErrorCode = "CROWN_STALE_REIGN"
	// CodeFutureReign names a claim from a reign nobody invested:
	// authority only moves forward through investiture.
	CodeFutureReign ErrorCode = "CROWN_FUTURE_REIGN"
	// CodeNotHolder names a claimant the current reign does not
	// invest: strangers and superseded holders decide nothing,
	// with or without a delegation they cannot chain.
	CodeNotHolder ErrorCode = "CROWN_NOT_HOLDER"
	// CodeInvalidSession names a session that cannot authenticate:
	// missing step-up, time travel, or a window longer than the
	// short session the call allows.
	CodeInvalidSession ErrorCode = "CROWN_INVALID_SESSION"
	// CodeSessionExpired names a live-shaped session whose window
	// already passed: replaying it authorizes nothing new.
	CodeSessionExpired ErrorCode = "CROWN_SESSION_EXPIRED"
	// CodeInvalidDelegation names a delegation that cannot chain:
	// wrong book, wrong reign, widened competence, self-theater, or
	// a delegator who no longer holds the office.
	CodeInvalidDelegation ErrorCode = "CROWN_INVALID_DELEGATION"
	// CodeDelegationExpired names a well-chained delegation past its
	// expiry: operational power ends where its window ends.
	CodeDelegationExpired ErrorCode = "CROWN_DELEGATION_EXPIRED"
	// CodeGrantExpired names a reused grant past its session: a
	// recorded authorization never outlives the session that made it.
	CodeGrantExpired ErrorCode = "CROWN_GRANT_EXPIRED"
	// CodeUnknownAct names a decree kind outside the closed royal
	// vocabulary: season alterations of deadline, Genesis or wealth
	// criteria never become a kind by inference.
	CodeUnknownAct ErrorCode = "CROWN_UNKNOWN_ACT"
	// CodeIncompleteAct names a decree missing a required field or
	// carrying an economic payload outside the economic kind: the
	// shape refuses before any effect.
	CodeIncompleteAct ErrorCode = "CROWN_INCOMPLETE_ACT"
	// CodeRetroactiveAct names a decree whose vigour starts before
	// its date: harmful backdating refuses, even when the text
	// claims a correction (corrections arrive as new prospective
	// acts linked to the original).
	CodeRetroactiveAct ErrorCode = "CROWN_RETROACTIVE_ACT"
	// CodeUnknownOrigin names an economic decree without a known
	// funding origin: value never moves from nowhere.
	CodeUnknownOrigin ErrorCode = "CROWN_UNKNOWN_ORIGIN"
	// CodeAmbiguousCharter names a decree without an explicit
	// charter version: facts bind to one published vN, never to
	// "current" or "latest" by inference.
	CodeAmbiguousCharter ErrorCode = "CROWN_AMBIGUOUS_CHARTER"
	// CodeSelfApproval names a check by its own author: the author
	// never reviews its own irreversible act.
	CodeSelfApproval ErrorCode = "CROWN_SELF_APPROVAL"
	// CodeDuplicateApproval names two checks that are not
	// independent: same identifier or same checker replayed.
	CodeDuplicateApproval ErrorCode = "CROWN_DUPLICATE_APPROVAL"
	// CodeTamperedAct names a check bound to another payload: any
	// change of identity, vigour, target, effect, value or charter
	// breaks both checks first.
	CodeTamperedAct ErrorCode = "CROWN_TAMPERED_ACT"
	// CodeCheckExpired names a check outside its live window:
	// spent approvals authorize nothing new.
	CodeCheckExpired ErrorCode = "CROWN_CHECK_EXPIRED"
	// CodeEmergencyWithoutReview names an urgent containment
	// without scheduled review: urgency only contains temporarily
	// with review, never executes finally alone.
	CodeEmergencyWithoutReview ErrorCode = "CROWN_EMERGENCY_WITHOUT_REVIEW"
)

// DomainError represents an invariant or rule failure in the crown domain.
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
	// ErrInvalidAuthority refuses a malformed or partial credential
	// before anything is read: every token, reign and instant must
	// arrive whole.
	ErrInvalidAuthority = DomainError{
		Code:    CodeInvalidAuthority,
		Message: "sovereign authority needs a season, a holder, a reign from 1, a competence and whole instants",
	}
	// ErrOperatorNotSovereign refuses the operator as King: the
	// technical credential never converts into the game office.
	ErrOperatorNotSovereign = DomainError{
		Code:    CodeOperatorNotSovereign,
		Message: "the operator credential is separate: administrators are never invested as King by alias",
	}
	// ErrSeasonMismatch refuses a claim outside the current book.
	ErrSeasonMismatch = DomainError{
		Code:    CodeSeasonMismatch,
		Message: "one season never decides in another: the claim names the current book or stops",
	}
	// ErrSeasonClosed refuses a claim outside the open window.
	ErrSeasonClosed = DomainError{
		Code:    CodeSeasonClosed,
		Message: "the season is not open: before the start, at or past the exclusive end, or suspended",
	}
	// ErrStaleReign refuses a superseded reign: investiture moves
	// power without deleting accounts.
	ErrStaleReign = DomainError{
		Code:    CodeStaleReign,
		Message: "the reign moved on: a new investiture revokes the previous holder",
	}
	// ErrFutureReign refuses a reign nobody invested.
	ErrFutureReign = DomainError{
		Code:    CodeFutureReign,
		Message: "no such reign was invested: authority only moves forward through investiture",
	}
	// ErrNotHolder refuses a claimant the reign does not invest.
	ErrNotHolder = DomainError{
		Code:    CodeNotHolder,
		Message: "the claimant does not hold the office in this reign: strangers decide nothing",
	}
	// ErrInvalidSession refuses a session that cannot authenticate.
	ErrInvalidSession = DomainError{
		Code:    CodeInvalidSession,
		Message: "the session cannot authenticate: step-up, ordered instants and a short window are all required",
	}
	// ErrSessionExpired refuses a spent session: replay authorizes
	// nothing new.
	ErrSessionExpired = DomainError{
		Code:    CodeSessionExpired,
		Message: "the session window passed: replaying it authorizes nothing new",
	}
	// ErrInvalidDelegation refuses a delegation that cannot chain to
	// the current holder.
	ErrInvalidDelegation = DomainError{
		Code:    CodeInvalidDelegation,
		Message: "the delegation cannot chain: same book, same reign, same competence, current holder as delegator",
	}
	// ErrDelegationExpired refuses a spent delegation.
	ErrDelegationExpired = DomainError{
		Code:    CodeDelegationExpired,
		Message: "the delegation window passed: operational power ends where its window ends",
	}
	// ErrGrantExpired refuses a reused grant past its session.
	ErrGrantExpired = DomainError{
		Code:    CodeGrantExpired,
		Message: "the grant outlived its session: recorded authorization never outlives the session that made it",
	}
	// ErrUnknownAct refuses a kind outside the closed vocabulary.
	ErrUnknownAct = DomainError{
		Code:    CodeUnknownAct,
		Message: "unknown royal act: the kind is not normative, office, process, economic, pardon or blessing, and season alterations never qualify",
	}
	// ErrIncompleteAct refuses a decree missing a required field.
	ErrIncompleteAct = DomainError{
		Code:    CodeIncompleteAct,
		Message: "the decree is incomplete: identity, authority, reason, target, effect, vigencia and charter version all arrive whole, and only the economic kind carries value",
	}
	// ErrRetroactiveAct refuses harmful backdating.
	ErrRetroactiveAct = DomainError{
		Code:    CodeRetroactiveAct,
		Message: "the decree backdates its vigour: effects start at or after the decree date, corrections arrive as new prospective acts",
	}
	// ErrUnknownOrigin refuses an economic decree without origin.
	ErrUnknownOrigin = DomainError{
		Code:    CodeUnknownOrigin,
		Message: "the economic decree names no known origin: value never moves from nowhere",
	}
	// ErrAmbiguousCharter refuses a decree without explicit version.
	ErrAmbiguousCharter = DomainError{
		Code:    CodeAmbiguousCharter,
		Message: "the charter version is ambiguous: decrees bind to one published vN, never to current or latest by inference",
	}
	// ErrSelfApproval refuses the author reviewing its own act.
	ErrSelfApproval = DomainError{
		Code:    CodeSelfApproval,
		Message: "the author cannot check its own act: irreversible acts need a distinct independent checker",
	}
	// ErrDuplicateApproval refuses checks that are not independent.
	ErrDuplicateApproval = DomainError{
		Code:    CodeDuplicateApproval,
		Message: "the checks are not independent: identifiers and checkers both differ, replay authorizes nothing new",
	}
	// ErrTamperedAct refuses a check bound to another payload.
	ErrTamperedAct = DomainError{
		Code:    CodeTamperedAct,
		Message: "the payload changed after the check: identity, vigour, target, effect, value and charter all bind the digest",
	}
	// ErrCheckExpired refuses a spent check.
	ErrCheckExpired = DomainError{
		Code:    CodeCheckExpired,
		Message: "the check window passed: spent approvals authorize nothing new",
	}
	// ErrEmergencyWithoutReview refuses urgency without review.
	ErrEmergencyWithoutReview = DomainError{
		Code:    CodeEmergencyWithoutReview,
		Message: "urgency without review executes nothing finally: emergency only contains temporarily with scheduled review",
	}
)
