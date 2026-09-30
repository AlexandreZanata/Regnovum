package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	// CodeInvalidTerms names malformed arbitration terms: blank or
	// abusive tokens, parties that are not two distinct accounts, a
	// negative value, a missing deadline or an unknown costs rule.
	CodeInvalidTerms ErrorCode = "DISPUTES_INVALID_TERMS"
	// CodeTermsExpired names an acceptance past the proposal
	// deadline: expired terms never bind, and dead terms are never
	// proposed.
	CodeTermsExpired ErrorCode = "DISPUTES_TERMS_EXPIRED"
	// CodeTermsNotParty names a stranger speaking for a proposal:
	// only the two named parties accept their own terms.
	CodeTermsNotParty ErrorCode = "DISPUTES_TERMS_NOT_PARTY"
)

// DomainError represents an invariant or rule failure in the disputes domain.
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
	// ErrInvalidTerms refuses arbitration terms that cannot name an
	// obligation: the proposal needs a key, a revision, an object,
	// two distinct parties, a rite, evidence rules, a costs rule, a
	// deadline, execution terms and a non-negative value.
	ErrInvalidTerms = DomainError{
		Code:    CodeInvalidTerms,
		Message: "arbitration terms need a key, a revision, an object, two distinct parties, a rite, evidence rules, a costs rule, a deadline and execution terms",
	}
	// ErrTermsExpired refuses acceptances past the deadline:
	// expired proposals never open a case.
	ErrTermsExpired = DomainError{
		Code:    CodeTermsExpired,
		Message: "expired proposal terms never bind: acceptance needs a live deadline",
	}
	// ErrTermsNotParty refuses a stranger accepting someone else's
	// proposal: only a named party accepts its own terms.
	ErrTermsNotParty = DomainError{
		Code:    CodeTermsNotParty,
		Message: "only a named party accepts its own proposal terms: strangers never bind a case",
	}
)
