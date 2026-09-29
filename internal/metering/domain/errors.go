package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	CodeInvalidService     ErrorCode = "METERING_INVALID_SERVICE"
	CodeInvalidVersion     ErrorCode = "METERING_INVALID_VERSION"
	CodeInvalidPrice       ErrorCode = "METERING_INVALID_PRICE"
	CodeInvalidPriceWindow ErrorCode = "METERING_INVALID_PRICE_WINDOW"
	CodeOverlappingPrice   ErrorCode = "METERING_OVERLAPPING_PRICE"
	CodeDuplicatePrice     ErrorCode = "METERING_DUPLICATE_PRICE_VERSION"
	CodePriceNotFound      ErrorCode = "METERING_PRICE_NOT_FOUND"
	CodeUnknownUnit        ErrorCode = "METERING_UNKNOWN_UNIT"
	CodeInvalidAuthority   ErrorCode = "METERING_INVALID_AUTHORITY"
	CodeUnknownPriceLocale ErrorCode = "METERING_UNKNOWN_LOCALE"
)

// DomainError represents an invariant or rule failure in the metering domain.
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
	ErrInvalidService = DomainError{
		Code:    CodeInvalidService,
		Message: "metering service needs a short lowercase slug without control characters",
	}
	ErrInvalidVersion = DomainError{
		Code:    CodeInvalidVersion,
		Message: "metering price version needs a positive integer",
	}
	ErrInvalidPrice = DomainError{
		Code:    CodeInvalidPrice,
		Message: "metering price needs a positive integer in milliINK, never float",
	}
	ErrInvalidPriceWindow = DomainError{
		Code:    CodeInvalidPriceWindow,
		Message: "metering price window needs two UTC instants with start strictly before end",
	}
	ErrOverlappingPrice = DomainError{
		Code:    CodeOverlappingPrice,
		Message: "metering price window overlaps another window of the same service: abutting only",
	}
	ErrDuplicatePriceVersion = DomainError{
		Code:    CodeDuplicatePrice,
		Message: "metering price version already exists for this service: versions are never reused",
	}
	ErrPriceNotFound = DomainError{
		Code:    CodePriceNotFound,
		Message: "no metering price covers this service and instant: absent prices never default",
	}
	ErrUnknownUnit = DomainError{
		Code:    CodeUnknownUnit,
		Message: "metering unit is outside the canonical vocabulary",
	}
	ErrInvalidAuthority = DomainError{
		Code:    CodeInvalidAuthority,
		Message: "metering authority needs a short printable name without control characters",
	}
	ErrUnknownPriceLocale = DomainError{
		Code:    CodeUnknownPriceLocale,
		Message: "metering locale is outside the pt/en vocabulary",
	}
)
