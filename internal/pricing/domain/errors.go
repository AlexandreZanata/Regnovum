package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	CodeInvalidSource       ErrorCode = "PRICING_INVALID_SOURCE"
	CodeInvalidPrice        ErrorCode = "PRICING_INVALID_PRICE"
	CodeInvalidObservation  ErrorCode = "PRICING_INVALID_OBSERVATION"
	CodeStaleObservation    ErrorCode = "PRICING_STALE_OBSERVATION"
	CodeDuplicateSource     ErrorCode = "PRICING_DUPLICATE_SOURCE"
	CodeSourceUnavailable   ErrorCode = "PRICING_SOURCE_UNAVAILABLE"
	CodeInsufficientSources ErrorCode = "PRICING_INSUFFICIENT_SOURCES"
	CodeDivergentSources    ErrorCode = "PRICING_DIVERGENT_SOURCES"
	CodeInvalidQuote        ErrorCode = "PRICING_INVALID_QUOTE"
)

// DomainError represents an invariant or rule failure in the pricing domain.
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
	ErrInvalidSource = DomainError{
		Code:    CodeInvalidSource,
		Message: "rate source needs a short lowercase slug without control characters",
	}
	ErrInvalidPrice = DomainError{
		Code:    CodeInvalidPrice,
		Message: "rate price needs a positive integer in minor units, never float",
	}
	ErrInvalidObservation = DomainError{
		Code:    CodeInvalidObservation,
		Message: "rate observation needs a source, a positive price, an instant and its payload",
	}
	ErrStaleObservation = DomainError{
		Code:    CodeStaleObservation,
		Message: "rate observation falls outside the freshness window: future or aged quotes never price",
	}
	ErrDuplicateSource = DomainError{
		Code:    CodeDuplicateSource,
		Message: "rate source is already approved: one identity collects once per round",
	}
	ErrSourceUnavailable = DomainError{
		Code:    CodeSourceUnavailable,
		Message: "rate source is unreachable or its breaker is open: the round continues without it",
	}
	ErrInsufficientSources = DomainError{
		Code:    CodeInsufficientSources,
		Message: "rate round is below the minimum source count: no quotation is issued",
	}
	ErrDivergentSources = DomainError{
		Code:    CodeDivergentSources,
		Message: "rate sightings diverge beyond the approved spread: no quotation is issued",
	}
	ErrInvalidQuote = DomainError{
		Code:    CodeInvalidQuote,
		Message: "purchase quote needs a positive price, distinct sources, ordered instants and a positive lifetime",
	}
)
