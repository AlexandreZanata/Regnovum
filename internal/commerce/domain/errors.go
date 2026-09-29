package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	CodeInvalidTransferKind ErrorCode = "COMMERCE_INVALID_TRANSFER_KIND"
	CodeInvalidIntention    ErrorCode = "COMMERCE_INVALID_INTENTION"
	CodeIntentionConflict   ErrorCode = "COMMERCE_INTENTION_CONFLICT"
	CodeInvalidTerms        ErrorCode = "COMMERCE_INVALID_TERMS"
)

// DomainError represents an invariant or rule failure in the commerce domain.
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
	ErrInvalidTransferKind = DomainError{
		Code:    CodeInvalidTransferKind,
		Message: "transfer kind needs one closed value: gift, trade, refund or treasury",
	}
	ErrInvalidIntention = DomainError{
		Code:    CodeInvalidIntention,
		Message: "transfer intention needs a key, a kind, two parties and a positive amount",
	}
	ErrIntentionConflict = DomainError{
		Code:    CodeIntentionConflict,
		Message: "intention key already settled different terms: reuse is refused, never merged",
	}
	ErrInvalidTerms = DomainError{
		Code:    CodeInvalidTerms,
		Message: "transfer terms need coherent parties, kind and amount",
	}
)
