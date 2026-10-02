package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	CodeEmptyAccountID          ErrorCode = "PROFILE_EMPTY_ACCOUNT_ID"
	CodeEmptyUsername           ErrorCode = "PROFILE_EMPTY_USERNAME"
	CodeUsernameTooShort        ErrorCode = "PROFILE_USERNAME_TOO_SHORT"
	CodeUsernameTooLong         ErrorCode = "PROFILE_USERNAME_TOO_LONG"
	CodeUsernameNonASCII        ErrorCode = "PROFILE_USERNAME_NON_ASCII"
	CodeInvalidUsernameFormat   ErrorCode = "PROFILE_USERNAME_INVALID_FORMAT"
	CodeUsernameReserved        ErrorCode = "PROFILE_USERNAME_RESERVED"
	CodeUsernameUnchanged       ErrorCode = "PROFILE_USERNAME_UNCHANGED"
	CodeUsernameCooldown        ErrorCode = "PROFILE_USERNAME_COOLDOWN"
	CodeInvalidCooldown         ErrorCode = "PROFILE_INVALID_COOLDOWN"
	CodeInvalidReservedUsername ErrorCode = "PROFILE_INVALID_RESERVED_USERNAME"
	CodeEmptyLocale             ErrorCode = "PROFILE_EMPTY_LOCALE"
	CodeInvalidLocale           ErrorCode = "PROFILE_INVALID_LOCALE"
	CodeUnsupportedLocale       ErrorCode = "PROFILE_UNSUPPORTED_LOCALE"
	CodeInvalidTimezone         ErrorCode = "PROFILE_INVALID_TIMEZONE"
	CodeUnknownRetentionClass   ErrorCode = "PROFILE_UNKNOWN_RETENTION_CLASS"
	CodeInvalidRetentionPolicy  ErrorCode = "PROFILE_INVALID_RETENTION_POLICY"
	CodeInvalidRetentionHold    ErrorCode = "PROFILE_INVALID_RETENTION_HOLD"
	CodeUnknownKingdomClass     ErrorCode = "PROFILE_UNKNOWN_KINGDOM_CLASS"
	CodeMissingCollectionBasis  ErrorCode = "PROFILE_MISSING_COLLECTION_BASIS"
	CodeNoJurisdictionWindow    ErrorCode = "PROFILE_NO_JURISDICTION_WINDOW"
	CodeInvalidKingdomExport    ErrorCode = "PROFILE_INVALID_KINGDOM_EXPORT"
	CodeUnknownExportSection    ErrorCode = "PROFILE_UNKNOWN_EXPORT_SECTION"
	CodeConsentRefused          ErrorCode = "PROFILE_CONSENT_REFUSED"
	CodeCrossAccountExport      ErrorCode = "PROFILE_CROSS_ACCOUNT_EXPORT"
	CodeDeadAccountAccess       ErrorCode = "PROFILE_DEAD_ACCOUNT_ACCESS"
)

// DomainError represents an invariant or rule failure in the profiles domain.
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
	ErrEmptyAccountID           = DomainError{Code: CodeEmptyAccountID, Message: "account identifier cannot be empty"}
	ErrEmptyUsername            = DomainError{Code: CodeEmptyUsername, Message: "username cannot be empty"}
	ErrUsernameTooShort         = DomainError{Code: CodeUsernameTooShort, Message: "username is shorter than the minimum allowed length"}
	ErrUsernameTooLong          = DomainError{Code: CodeUsernameTooLong, Message: "username is longer than the maximum allowed length"}
	ErrUsernameNonASCII         = DomainError{Code: CodeUsernameNonASCII, Message: "username contains non-ASCII characters"}
	ErrInvalidUsernameFormat    = DomainError{Code: CodeInvalidUsernameFormat, Message: "username format is invalid"}
	ErrUsernameReserved         = DomainError{Code: CodeUsernameReserved, Message: "username is reserved"}
	ErrUsernameUnchanged        = DomainError{Code: CodeUsernameUnchanged, Message: "username is unchanged"}
	ErrUsernameCooldown         = DomainError{Code: CodeUsernameCooldown, Message: "username cannot be changed before the cooldown elapses"}
	ErrInvalidCooldown          = DomainError{Code: CodeInvalidCooldown, Message: "username change cooldown cannot be negative"}
	ErrInvalidReservedUsername  = DomainError{Code: CodeInvalidReservedUsername, Message: "reserved username list contains an invalid username"}
	ErrEmptyLocale              = DomainError{Code: CodeEmptyLocale, Message: "interface locale cannot be empty"}
	ErrInvalidLocale            = DomainError{Code: CodeInvalidLocale, Message: "interface locale is not a well-formed BCP 47 tag"}
	ErrUnsupportedLocale        = DomainError{Code: CodeUnsupportedLocale, Message: "interface locale is not supported by the product"}
	ErrInvalidTimezone          = DomainError{Code: CodeInvalidTimezone, Message: "timezone is not a valid IANA time zone name"}
	ErrUnknownRetentionClass    = DomainError{Code: CodeUnknownRetentionClass, Message: "retention class is not part of the executable policy"}
	ErrInvalidRetentionSchedule = DomainError{Code: CodeInvalidRetentionPolicy, Message: "retention schedule is incoherent"}
	ErrInvalidRetentionHold     = DomainError{Code: CodeInvalidRetentionHold, Message: "retention hold is incoherent"}
	ErrUnknownKingdomClass      = DomainError{Code: CodeUnknownKingdomClass, Message: "kingdom retention class is not governed"}
	ErrMissingCollectionBasis   = DomainError{Code: CodeMissingCollectionBasis, Message: "collection needs a stated legal basis"}
	ErrNoJurisdictionWindow     = DomainError{Code: CodeNoJurisdictionWindow, Message: "the jurisdiction carries no disposal horizon for this class"}
	ErrInvalidKingdomExport     = DomainError{Code: CodeInvalidKingdomExport, Message: "kingdom export request is incoherent"}
	ErrUnknownExportSection     = DomainError{Code: CodeUnknownExportSection, Message: "export section is outside the allowlist"}
	ErrConsentRefused           = DomainError{Code: CodeConsentRefused, Message: "the holder refused consent: nothing is exported"}
	ErrCrossAccountExport       = DomainError{Code: CodeCrossAccountExport, Message: "the export mixes holders or leaks foreign proof"}
	ErrDeadAccountAccess        = DomainError{Code: CodeDeadAccountAccess, Message: "a dead account answers only to its heir"}
)
