package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for reputation rule violations.
type ErrorCode string

const (
	// CodeInvalidRecord names a malformed standing fact: a blank or
	// abusive subject, role, season, link or detail, or a missing
	// instant. Shape refuses before any effect.
	CodeInvalidRecord ErrorCode = "REPUTATION_INVALID_RECORD"
	// CodeUnknownEvent names a kind outside the closed auditable
	// vocabulary: payment, purchase and any other string never
	// become reputation, so money cannot buy standing.
	CodeUnknownEvent ErrorCode = "REPUTATION_UNKNOWN_EVENT"
	// CodeEventConflict names a divergent replay under a recorded
	// event or correction identity: the same key returns the same
	// record, a different payload conflicts instead of duplicating.
	CodeEventConflict ErrorCode = "REPUTATION_EVENT_CONFLICT"
	// CodeUnknownFact names a correction or deletion bound to no
	// recorded event: corrections link to facts, never float alone.
	CodeUnknownFact ErrorCode = "REPUTATION_UNKNOWN_FACT"
	// CodeForgetBlocked names a deletion the policy forbids: legal
	// hold, retention not due, or a fact with a linked correction.
	CodeForgetBlocked ErrorCode = "REPUTATION_FORGET_BLOCKED"
)

// DomainError represents an invariant or rule failure in the reputation domain.
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
	// ErrInvalidRecord refuses a malformed standing fact.
	ErrInvalidRecord = DomainError{
		Code:    CodeInvalidRecord,
		Message: "the standing fact is malformed: subject, role, season, link and detail arrive whole with a live instant",
	}
	// ErrUnknownEvent refuses payment and any other kind outside
	// the closed auditable vocabulary.
	ErrUnknownEvent = DomainError{
		Code:    CodeUnknownEvent,
		Message: "the kind is not an auditable event: deadlines, reversals, declared conflicts and upheld decisions only, never payment",
	}
	// ErrEventConflict refuses a divergent replay under a recorded identity.
	ErrEventConflict = DomainError{
		Code:    CodeEventConflict,
		Message: "the identity already records a different fact: replay returns the same record, divergence conflicts",
	}
	// ErrUnknownFact refuses a correction or deletion without its fact.
	ErrUnknownFact = DomainError{
		Code:    CodeUnknownFact,
		Message: "the fact is not recorded: corrections and deletions bind to recorded events, never float alone",
	}
	// ErrForgetBlocked refuses a deletion the policy forbids.
	ErrForgetBlocked = DomainError{
		Code:    CodeForgetBlocked,
		Message: "the policy keeps this fact: legal hold, retention not due, or a linked correction preserves it",
	}
)
