package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	CodeNegativeMilliInk     ErrorCode = "ECONOMY_NEGATIVE_MILLIINK"
	CodeInvalidMilliInk      ErrorCode = "ECONOMY_INVALID_MILLIINK"
	CodeMilliInkOverflow     ErrorCode = "ECONOMY_MILLIINK_OVERFLOW"
	CodeInsufficientMilliInk ErrorCode = "ECONOMY_INSUFFICIENT_MILLIINK"
	CodeMilliInkPrecision    ErrorCode = "ECONOMY_MILLIINK_PRECISION"
	CodeUnknownLocale        ErrorCode = "ECONOMY_UNKNOWN_LOCALE"
	CodeInvalidGenesisKey    ErrorCode = "ECONOMY_INVALID_GENESIS_KEY"
	CodeGenesisAlreadyExists ErrorCode = "ECONOMY_GENESIS_ALREADY_EXISTS"
	CodeUnknownCustody       ErrorCode = "ECONOMY_UNKNOWN_CUSTODY"
	CodeUnauthorizedCustody  ErrorCode = "ECONOMY_UNAUTHORIZED_CUSTODY"
	CodeSameCustody          ErrorCode = "ECONOMY_SAME_CUSTODY"
	CodeInvalidIntention     ErrorCode = "ECONOMY_INVALID_INTENTION"
	CodeIntentionConflict    ErrorCode = "ECONOMY_INTENTION_CONFLICT"
	CodeInvalidStatement     ErrorCode = "ECONOMY_INVALID_STATEMENT"
	CodeStatementForbidden   ErrorCode = "ECONOMY_STATEMENT_FORBIDDEN"
	CodeStatementSuspended   ErrorCode = "ECONOMY_STATEMENT_SUSPENDED"
	CodeInvalidHold          ErrorCode = "ECONOMY_INVALID_HOLD"
	CodeHoldState            ErrorCode = "ECONOMY_HOLD_STATE"
	CodeHoldNotFound         ErrorCode = "ECONOMY_HOLD_NOT_FOUND"
	CodeHoldNotExpired       ErrorCode = "ECONOMY_HOLD_NOT_EXPIRED"
	CodeEconomyFrozen        ErrorCode = "ECONOMY_FROZEN"
	CodeIncidentNotFound     ErrorCode = "ECONOMY_INCIDENT_NOT_FOUND"
	CodeInvalidCharter       ErrorCode = "ECONOMY_INVALID_CHARTER"
	CodeConsentRequired      ErrorCode = "ECONOMY_CONSENT_REQUIRED"
	CodeConsentConflict      ErrorCode = "ECONOMY_CONSENT_CONFLICT"
	CodeRateMismatch         ErrorCode = "ECONOMY_RATE_MISMATCH"
	CodeOptInExpired         ErrorCode = "ECONOMY_OPTIN_EXPIRED"
	CodeOptInMissing         ErrorCode = "ECONOMY_OPTIN_MISSING"
	CodeInvalidGrant         ErrorCode = "ECONOMY_INVALID_GRANT"
	CodeInvalidRefund        ErrorCode = "ECONOMY_INVALID_REFUND"
	CodeInvalidDisbursement  ErrorCode = "ECONOMY_INVALID_DISBURSEMENT"
)

// DomainError represents an invariant or rule failure in the economy domain.
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
	ErrNegativeMilliInk = DomainError{
		Code:    CodeNegativeMilliInk,
		Message: "milliink quantity cannot be negative",
	}
	ErrInvalidMilliInk = DomainError{
		Code:    CodeInvalidMilliInk,
		Message: "milliink text is not an exact decimal quantity",
	}
	ErrMilliInkOverflow = DomainError{
		Code:    CodeMilliInkOverflow,
		Message: "milliink arithmetic would overflow the 64-bit range",
	}
	ErrInsufficientMilliInk = DomainError{
		Code:    CodeInsufficientMilliInk,
		Message: "milliink subtraction would produce a negative quantity",
	}
	ErrMilliInkPrecision = DomainError{
		Code:    CodeMilliInkPrecision,
		Message: "milliink fractions beyond three decimals are refused, never rounded",
	}
	ErrUnknownLocale = DomainError{
		Code:    CodeUnknownLocale,
		Message: "locale is outside the pt/en decimal vocabulary",
	}
	ErrInvalidGenesisKey = DomainError{
		Code:    CodeInvalidGenesisKey,
		Message: "genesis key is empty, too long or carries control characters",
	}
	ErrGenesisAlreadyExists = DomainError{
		Code:    CodeGenesisAlreadyExists,
		Message: "genesis already happened: a second creation event is refused",
	}
	ErrUnknownCustody = DomainError{
		Code:    CodeUnknownCustody,
		Message: "custody is outside the closed kind vocabulary or does not exist",
	}
	ErrUnauthorizedCustody = DomainError{
		Code:    CodeUnauthorizedCustody,
		Message: "source custody kind cannot spend yet: locked holds release only by their own conditions",
	}
	ErrSameCustody = DomainError{
		Code:    CodeSameCustody,
		Message: "transfer within one custody is refused: legs must move value between custodies",
	}
	ErrInvalidIntention = DomainError{
		Code:    CodeInvalidIntention,
		Message: "intention key, actor or operation is empty, too long or carries control characters",
	}
	ErrIntentionConflict = DomainError{
		Code:    CodeIntentionConflict,
		Message: "intention key already settled a different payload: reuse is refused, never merged",
	}
	ErrInvalidStatement = DomainError{
		Code:    CodeInvalidStatement,
		Message: "statement query carries a bad limit, cursor or identity",
	}
	ErrStatementForbidden = DomainError{
		Code:    CodeStatementForbidden,
		Message: "caller may not read this custody statement: system custodies and other holders are refused",
	}
	ErrStatementSuspended = DomainError{
		Code:    CodeStatementSuspended,
		Message: "owner account is not active: suspended holders read nothing",
	}
	ErrInvalidHold = DomainError{
		Code:    CodeInvalidHold,
		Message: "hold purpose, expiry or amount is missing, too long or carries control characters",
	}
	ErrHoldState = DomainError{
		Code:    CodeHoldState,
		Message: "hold is not in a state this settlement leaves from: settled holds never reopen",
	}
	ErrHoldNotFound = DomainError{
		Code:    CodeHoldNotFound,
		Message: "hold does not exist",
	}
	ErrHoldNotExpired = DomainError{
		Code:    CodeHoldNotExpired,
		Message: "hold deadline has not passed: expiry is observed, never anticipated",
	}
	ErrEconomyFrozen = DomainError{
		Code:    CodeEconomyFrozen,
		Message: "economy is frozen on a conservation break: reads continue, mutations wait for a compensated resolution",
	}
	ErrIncidentNotFound = DomainError{
		Code:    CodeIncidentNotFound,
		Message: "incident does not exist or is not an open break",
	}
	ErrInvalidCharter = DomainError{
		Code:    CodeInvalidCharter,
		Message: "charter version, decision, rate or validity is missing, malformed or out of range",
	}
	ErrConsentRequired = DomainError{
		Code:    CodeConsentRequired,
		Message: "no accepted charter for this account and version: conversion without acceptance is refused",
	}
	ErrConsentConflict = DomainError{
		Code:    CodeConsentConflict,
		Message: "a different verdict or terms already stand for this account and version",
	}
	ErrRateMismatch = DomainError{
		Code:    CodeRateMismatch,
		Message: "offered rate differs from the recorded opt-in terms: conversion matches exactly or not at all",
	}
	ErrOptInExpired = DomainError{
		Code:    CodeOptInExpired,
		Message: "opt-in validity lapsed: expired intents never convert",
	}
	ErrOptInMissing = DomainError{
		Code:    CodeOptInMissing,
		Message: "no recorded opt-in intent for this account and charter: nothing to convert",
	}
	ErrInvalidGrant = DomainError{
		Code:    CodeInvalidGrant,
		Message: "monetary grant needs a positive amount",
	}
	ErrInvalidRefund = DomainError{
		Code:    CodeInvalidRefund,
		Message: "refusal refund needs coherent legacy balances and a valid fiat correlation",
	}
	ErrInvalidDisbursement = DomainError{
		Code:    CodeInvalidDisbursement,
		Message: "disbursement needs an allowlisted purpose and two governors besides the beneficiary",
	}
)
