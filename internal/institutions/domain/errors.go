package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for institutional book violations.
type ErrorCode string

const (
	// CodeInvalidAct names a malformed office act: a blank or
	// abusive author, office, competence, rule, effect, season or
	// detail, or a missing instant. Shape refuses before any effect.
	CodeInvalidAct ErrorCode = "INSTITUTIONS_INVALID_ACT"
	// CodeUnauthorizedAct names an act without authority or audit:
	// no mandate reference, no independent audit, or the auditor
	// auditing its own authorship.
	CodeUnauthorizedAct ErrorCode = "INSTITUTIONS_UNAUTHORIZED_ACT"
	// CodeUnknownCorrection names a correction linked to no
	// recorded act: corrections arrive linked and the original
	// stays visible beside them.
	CodeUnknownCorrection ErrorCode = "INSTITUTIONS_UNKNOWN_CORRECTION"
	// CodeEntryConflict names a divergent replay under a recorded
	// act identity: the same entry returns the same book, a
	// different payload conflicts instead of duplicating.
	CodeEntryConflict ErrorCode = "INSTITUTIONS_ENTRY_CONFLICT"
	// CodeAccessDenied names a full-record read outside the access
	// list: the public reads the safe summary, never the integra.
	CodeAccessDenied ErrorCode = "INSTITUTIONS_ACCESS_DENIED"
)

// DomainError represents an invariant or rule failure in the institutions domain.
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
	// ErrInvalidAct refuses a malformed office act.
	ErrInvalidAct = DomainError{
		Code:    CodeInvalidAct,
		Message: "the office act is malformed: author, office, competence, rule, season, effect and instants arrive whole",
	}
	// ErrUnauthorizedAct refuses an act without authority or audit.
	ErrUnauthorizedAct = DomainError{
		Code:    CodeUnauthorizedAct,
		Message: "the act lacks authority or audit: a mandate reference and an independent auditor, never the author itself",
	}
	// ErrUnknownCorrection refuses a correction without its original.
	ErrUnknownCorrection = DomainError{
		Code:    CodeUnknownCorrection,
		Message: "the correction links no recorded act: corrections arrive linked and originals stay visible",
	}
	// ErrEntryConflict refuses a divergent replay under a recorded identity.
	ErrEntryConflict = DomainError{
		Code:    CodeEntryConflict,
		Message: "the identity already records a different act: replay returns the same book, divergence conflicts",
	}
	// ErrAccessDenied refuses a full-record read outside the access list.
	ErrAccessDenied = DomainError{
		Code:    CodeAccessDenied,
		Message: "the reader is not listed: the public reads the safe summary, never the integra",
	}
)
