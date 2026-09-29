package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	CodeInvalidService       ErrorCode = "METERING_INVALID_SERVICE"
	CodeInvalidVersion       ErrorCode = "METERING_INVALID_VERSION"
	CodeInvalidPrice         ErrorCode = "METERING_INVALID_PRICE"
	CodeInvalidPriceWindow   ErrorCode = "METERING_INVALID_PRICE_WINDOW"
	CodeOverlappingPrice     ErrorCode = "METERING_OVERLAPPING_PRICE"
	CodeDuplicatePrice       ErrorCode = "METERING_DUPLICATE_PRICE_VERSION"
	CodePriceNotFound        ErrorCode = "METERING_PRICE_NOT_FOUND"
	CodeUnknownUnit          ErrorCode = "METERING_UNKNOWN_UNIT"
	CodeInvalidAuthority     ErrorCode = "METERING_INVALID_AUTHORITY"
	CodeUnknownPriceLocale   ErrorCode = "METERING_UNKNOWN_LOCALE"
	CodeEmptyMeasuredContent ErrorCode = "METERING_EMPTY_CONTENT"
	CodeInvalidMeasured      ErrorCode = "METERING_INVALID_CONTENT"
	CodeMeasuredTooLong      ErrorCode = "METERING_CONTENT_TOO_LONG"
	CodeMissingMeterCounter  ErrorCode = "METERING_MISSING_GRAPHEME_COUNTER"
	CodeInvalidMeasuredHash  ErrorCode = "METERING_INVALID_CONTENT_HASH"
	CodeMeasuredHashMismatch ErrorCode = "METERING_CONTENT_HASH_MISMATCH"
	CodeInvalidMeasuredLimit ErrorCode = "METERING_INVALID_MEASURE_LIMIT"
	CodeInvalidQuote         ErrorCode = "METERING_INVALID_QUOTE"
	CodeQuoteExpired         ErrorCode = "METERING_QUOTE_EXPIRED"
	CodeQuoteMismatch        ErrorCode = "METERING_QUOTE_MISMATCH"
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
	ErrEmptyMeasuredContent = DomainError{
		Code:    CodeEmptyMeasuredContent,
		Message: "metering content cannot be empty or whitespace only",
	}
	ErrInvalidMeasuredContent = DomainError{
		Code:    CodeInvalidMeasured,
		Message: "metering content contains unsupported characters",
	}
	ErrMeasuredContentTooLong = DomainError{
		Code:    CodeMeasuredTooLong,
		Message: "metering content exceeds the caller-supplied grapheme limit",
	}
	ErrMissingMeterCounter = DomainError{
		Code:    CodeMissingMeterCounter,
		Message: "a grapheme counter is required",
	}
	ErrInvalidMeasuredHash = DomainError{
		Code:    CodeInvalidMeasuredHash,
		Message: "content hash format is invalid",
	}
	ErrMeasuredContentHashMismatch = DomainError{
		Code:    CodeMeasuredHashMismatch,
		Message: "content does not match its canonical hash",
	}
	ErrInvalidMeasuredLimit = DomainError{
		Code:    CodeInvalidMeasuredLimit,
		Message: "metering measure limit needs a positive grapheme budget",
	}
	ErrInvalidQuote = DomainError{
		Code:    CodeInvalidQuote,
		Message: "metering quote needs an account, measured content, covering price and positive lifetime",
	}
	ErrQuoteExpired = DomainError{
		Code:    CodeQuoteExpired,
		Message: "metering quote expired: altered or lapsed terms need a new acceptance",
	}
	ErrQuoteMismatch = DomainError{
		Code:    CodeQuoteMismatch,
		Message: "metering quote binds one account to one content hash: another account or text needs a new acceptance",
	}
)
