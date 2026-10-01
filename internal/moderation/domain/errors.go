package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	CodeEmptyAccountID       ErrorCode = "MODERATION_EMPTY_ACCOUNT_ID"
	CodeInvalidRole          ErrorCode = "MODERATION_INVALID_ROLE"
	CodeInvalidAction        ErrorCode = "MODERATION_INVALID_ACTION"
	CodeRoleNotAuthorized    ErrorCode = "MODERATION_ROLE_NOT_AUTHORIZED"
	CodeConflictOfInterest   ErrorCode = "MODERATION_CONFLICT_OF_INTEREST"
	CodeStepUpRequired       ErrorCode = "MODERATION_STEP_UP_REQUIRED"
	CodeRoleRevoked          ErrorCode = "MODERATION_ROLE_REVOKED"
	CodeInvalidSessionAge    ErrorCode = "MODERATION_INVALID_SESSION_AGE"
	CodeInvalidTargetType    ErrorCode = "MODERATION_INVALID_TARGET_TYPE"
	CodeInvalidReason        ErrorCode = "MODERATION_INVALID_REASON"
	CodeInvalidContext       ErrorCode = "MODERATION_INVALID_CONTEXT"
	CodeEmptyTargetID        ErrorCode = "MODERATION_EMPTY_TARGET_ID"
	CodeInvalidRule          ErrorCode = "MODERATION_INVALID_RULE"
	CodeInvalidJustification ErrorCode = "MODERATION_INVALID_JUSTIFICATION"
	CodeInvalidExpiry        ErrorCode = "MODERATION_INVALID_EXPIRY"
	CodeTargetActionMismatch ErrorCode = "MODERATION_TARGET_ACTION_MISMATCH"
	CodeInvalidOutcome       ErrorCode = "MODERATION_INVALID_OUTCOME"
	CodeThirdPartyTarget     ErrorCode = "MODERATION_THIRD_PARTY_TARGET"
	CodeReviewTimeout        ErrorCode = "MODERATION_REVIEW_TIMEOUT"
	CodeMissingConviction    ErrorCode = "MODERATION_MISSING_CONVICTION"
	CodeExcessiveQuarantine  ErrorCode = "MODERATION_EXCESSIVE_QUARANTINE"
)

// DomainError represents an invariant or rule failure in the moderation domain.
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
	ErrEmptyAccountID       = DomainError{Code: CodeEmptyAccountID, Message: "account identifier cannot be empty"}
	ErrInvalidRole          = DomainError{Code: CodeInvalidRole, Message: "administrative role is unrecognized"}
	ErrInvalidAction        = DomainError{Code: CodeInvalidAction, Message: "moderation action is unrecognized"}
	ErrRoleNotAuthorized    = DomainError{Code: CodeRoleNotAuthorized, Message: "role may not take this moderation action"}
	ErrConflictOfInterest   = DomainError{Code: CodeConflictOfInterest, Message: "actor must declare conflict and pass the case to another reviewer"}
	ErrStepUpRequired       = DomainError{Code: CodeStepUpRequired, Message: "sensitive action requires recent authentication"}
	ErrRoleRevoked          = DomainError{Code: CodeRoleRevoked, Message: "administrative assignment is revoked"}
	ErrInvalidSessionAge    = DomainError{Code: CodeInvalidSessionAge, Message: "session age is incoherent"}
	ErrInvalidTargetType    = DomainError{Code: CodeInvalidTargetType, Message: "report target type is unrecognized"}
	ErrInvalidReason        = DomainError{Code: CodeInvalidReason, Message: "report reason is unrecognized"}
	ErrInvalidContext       = DomainError{Code: CodeInvalidContext, Message: "report context is too long or blank"}
	ErrEmptyTargetID        = DomainError{Code: CodeEmptyTargetID, Message: "report target identifier cannot be empty"}
	ErrInvalidRule          = DomainError{Code: CodeInvalidRule, Message: "applied rule reference is blank or too long"}
	ErrInvalidJustification = DomainError{Code: CodeInvalidJustification, Message: "decision justification is blank or too long"}
	ErrInvalidExpiry        = DomainError{Code: CodeInvalidExpiry, Message: "sanction expiry is required only for time-boxed measures and must be future"}
	ErrTargetActionMismatch = DomainError{Code: CodeTargetActionMismatch, Message: "moderation action cannot sanction this target type"}
	ErrInvalidOutcome       = DomainError{Code: CodeInvalidOutcome, Message: "appeal outcome is outside the supported vocabulary"}
	ErrThirdPartyTarget     = DomainError{Code: CodeThirdPartyTarget, Message: "severe measures target the accused account or piece only, never third-party content"}
	ErrReviewTimeout        = DomainError{Code: CodeReviewTimeout, Message: "the independent review arrived past its deadline: the preventive measure already lapsed"}
	ErrMissingConviction    = DomainError{Code: CodeMissingConviction, Message: "quarantine follows a conviction only: no sentence, no quarantine"}
	ErrExcessiveQuarantine  = DomainError{Code: CodeExcessiveQuarantine, Message: "quarantine exceeds the ratified cap"}
)
