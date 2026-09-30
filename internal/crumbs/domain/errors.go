package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	// CodeInvalidEpoch names a malformed or nonexistent weekly epoch:
	// bad shape, out-of-range week or W53 in a 52-week year.
	CodeInvalidEpoch ErrorCode = "CRUMBS_INVALID_EPOCH"
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
)
