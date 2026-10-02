package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for patent rule violations.
type ErrorCode string

const (
	// CodeInvalidGrant names a malformed patent grant: a blank or
	// abusive identity, holder, season or cosmetic title, or a
	// missing instant or season window. Shape refuses before any
	// effect.
	CodeInvalidGrant ErrorCode = "PATENT_INVALID_GRANT"
	// CodeTermsMissing names a sale without ratified terms: price,
	// seats, duration and version arrive in an approved decision,
	// never in an invented constant, so a missing term blocks the
	// sale instead of falling back to a default.
	CodeTermsMissing ErrorCode = "PATENT_TERMS_MISSING"
	// CodeExpired names a grant outside its season window: before
	// the season starts, at or after its expiry, or past the
	// season end. Intervals are [start, end).
	CodeExpired ErrorCode = "PATENT_EXPIRED"
	// CodeNonTransferable names any attempt to move a patent to
	// another holder: the status is bound to one account and one
	// season, so transfer always refuses.
	CodeNonTransferable ErrorCode = "PATENT_NON_TRANSFERABLE"
	// CodeNewGrantRequired names any automatic renewal, discount
	// or power carried from a past season: the old honor stays a
	// historic fact, and the new cycle needs a new grant with new
	// acceptance.
	CodeNewGrantRequired ErrorCode = "PATENT_NEW_GRANT_REQUIRED"
)

// DomainError represents an invariant or rule failure in the patents domain.
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
	// ErrInvalidGrant refuses a malformed patent grant.
	ErrInvalidGrant = DomainError{
		Code:    CodeInvalidGrant,
		Message: "the patent grant is malformed: identity, holder, season and cosmetic title arrive whole with live instants and season window",
	}
	// ErrTermsMissing refuses a sale without ratified terms.
	ErrTermsMissing = DomainError{
		Code:    CodeTermsMissing,
		Message: "the ratified terms are missing: price, seats, duration and version arrive in an approved decision, never in a default",
	}
	// ErrExpired refuses a grant outside its season window.
	ErrExpired = DomainError{
		Code:    CodeExpired,
		Message: "the patent is not live: grants live inside [season start, expiry capped at season end)",
	}
	// ErrNonTransferable refuses any transfer of a patent.
	ErrNonTransferable = DomainError{
		Code:    CodeNonTransferable,
		Message: "the patent does not transfer: one account and one season hold it, no handover exists",
	}
	// ErrNewGrantRequired refuses automatic renewal from a past season.
	ErrNewGrantRequired = DomainError{
		Code:    CodeNewGrantRequired,
		Message: "the new season needs a new grant: past honor is history, never a discount or an automatic power",
	}
)
