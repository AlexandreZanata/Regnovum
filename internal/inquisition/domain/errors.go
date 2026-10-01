package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for
// inquisition rule violations.
type ErrorCode string

const (
	// CodeInvalidReport names a malformed filing: a blank or
	// abusive token, a self-report, a missing reason or a missing
	// instant. Shape refuses before jurisdiction is judged.
	CodeInvalidReport ErrorCode = "INQUISITION_INVALID_REPORT"
	// CodeUnknownViolation names a violation outside the closed
	// safety vocabulary: only threat, fraud, harassment, data
	// abuse and malware authorize institutional action, never by
	// inference.
	CodeUnknownViolation ErrorCode = "INQUISITION_UNKNOWN_VIOLATION"
	// CodePrivateMatter names a private debt or private challenge
	// offered as a safety case: bilateral matters belong to
	// private dispute, never to institutional sanction.
	CodePrivateMatter ErrorCode = "INQUISITION_PRIVATE_MATTER"
	// CodeUnprovenAccusation names a containment over an
	// unevidenced report: a private accusation without proof
	// activates no sanction.
	CodeUnprovenAccusation ErrorCode = "INQUISITION_UNPROVEN_ACCUSATION"
	// CodeUngroundedContainment names a containment without
	// recorded authority, without motive, or without a bounded
	// temporary window: risk reports contain only with all three
	// on record.
	CodeUngroundedContainment ErrorCode = "INQUISITION_UNGROUNDED_CONTAINMENT"
	// CodeAmbiguousCharter names a report without an explicit
	// charter version: facts bind to one published vN, never to
	// "current" or "latest" by inference.
	CodeAmbiguousCharter ErrorCode = "INQUISITION_AMBIGUOUS_CHARTER"
)

// DomainError represents an invariant or rule failure in the
// inquisition domain.
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
	// ErrInvalidReport refuses a malformed filing before anything
	// is read.
	ErrInvalidReport = DomainError{
		Code:    CodeInvalidReport,
		Message: "the filing is malformed: whole identifiers, two distinct subjects, a stated reason and a live instant are all required",
	}
	// ErrUnknownViolation refuses a kind outside the closed safety
	// vocabulary.
	ErrUnknownViolation = DomainError{
		Code:    CodeUnknownViolation,
		Message: "unknown safety violation: only ameaca, fraude, assedio, dados and malware authorize institutional action",
	}
	// ErrPrivateMatter refuses a private debt or challenge as a
	// safety case.
	ErrPrivateMatter = DomainError{
		Code:    CodePrivateMatter,
		Message: "the matter is private: debt and private challenge belong to bilateral dispute, never to institutional sanction",
	}
	// ErrUnprovenAccusation refuses to contain on an unevidenced
	// report.
	ErrUnprovenAccusation = DomainError{
		Code:    CodeUnprovenAccusation,
		Message: "the accusation carries no proof: reports without sealed evidence activate no sanction",
	}
	// ErrUngroundedContainment refuses a containment without
	// recorded authority, motive or bounded window.
	ErrUngroundedContainment = DomainError{
		Code:    CodeUngroundedContainment,
		Message: "the containment is ungrounded: recorded authority, motive and a bounded temporary window are all required",
	}
	// ErrAmbiguousCharter refuses a report without an explicit
	// version.
	ErrAmbiguousCharter = DomainError{
		Code:    CodeAmbiguousCharter,
		Message: "the charter version is ambiguous: reports bind to one published vN, never to current or latest by inference",
	}
)
