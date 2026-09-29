package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	CodeInvalidTransferKind ErrorCode = "COMMERCE_INVALID_TRANSFER_KIND"
	CodeInvalidIntention    ErrorCode = "COMMERCE_INVALID_INTENTION"
	CodeIntentionConflict   ErrorCode = "COMMERCE_INTENTION_CONFLICT"
	CodeInvalidTerms        ErrorCode = "COMMERCE_INVALID_TERMS"
	CodeUnknownAccount      ErrorCode = "COMMERCE_UNKNOWN_ACCOUNT"
	CodeAccountNotActive    ErrorCode = "COMMERCE_ACCOUNT_NOT_ACTIVE"
	CodeSanctionedAccount   ErrorCode = "COMMERCE_SANCTIONED_ACCOUNT"
	CodeSelfTransfer        ErrorCode = "COMMERCE_SELF_TRANSFER"
	CodeLimitExceeded       ErrorCode = "COMMERCE_LIMIT_EXCEEDED"
	CodeRateLimited         ErrorCode = "COMMERCE_RATE_LIMITED"
	CodeConsentRequired     ErrorCode = "COMMERCE_CONSENT_REQUIRED"
	CodeInvalidContract     ErrorCode = "COMMERCE_INVALID_CONTRACT"
	CodeContractState       ErrorCode = "COMMERCE_CONTRACT_STATE"
	CodeContractNotFound    ErrorCode = "COMMERCE_CONTRACT_NOT_FOUND"
	CodeUnauthorizedRelease ErrorCode = "COMMERCE_UNAUTHORIZED_RELEASE"
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
	ErrUnknownAccount = DomainError{
		Code:    CodeUnknownAccount,
		Message: "account does not exist: transfers settle only between known accounts",
	}
	ErrAccountNotActive = DomainError{
		Code:    CodeAccountNotActive,
		Message: "account is not active: suspended, deleted and pending accounts move nothing",
	}
	ErrSanctionedAccount = DomainError{
		Code:    CodeSanctionedAccount,
		Message: "account is sanctioned: blocked parties neither pay nor receive",
	}
	ErrSelfTransfer = DomainError{
		Code:    CodeSelfTransfer,
		Message: "payer and payee coincide: a transfer moves value between accounts",
	}
	ErrLimitExceeded = DomainError{
		Code:    CodeLimitExceeded,
		Message: "amount exceeds the approved per-transfer ceiling",
	}
	ErrRateLimited = DomainError{
		Code:    CodeRateLimited,
		Message: "payer exceeded the approved transfer count for the window",
	}
	ErrConsentRequired = DomainError{
		Code:    CodeConsentRequired,
		Message: "transfer needs an explicit consent reference: silent debits never settle",
	}
	ErrInvalidContract = DomainError{
		Code:    CodeInvalidContract,
		Message: "trade contract needs an object, two parties, a positive amount and a future expiry",
	}
	ErrContractState = DomainError{
		Code:    CodeContractState,
		Message: "contract is not in a state this settlement leaves from: terminal contracts never reopen",
	}
	ErrContractNotFound = DomainError{
		Code:    CodeContractNotFound,
		Message: "contract is unknown to this account: escrow settles only accepted intentions",
	}
	ErrUnauthorizedRelease = DomainError{
		Code:    CodeUnauthorizedRelease,
		Message: "release needs the buyer acceptance or a competent decision: unilateral releases never settle",
	}
)
