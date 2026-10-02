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
	// CodeConsentRequired names an acquisition without explicit
	// voluntary acceptance: entry into the patent contract is
	// voluntary, so a missing acceptance blocks the sale.
	CodeConsentRequired ErrorCode = "PATENT_CONSENT_REQUIRED"
	// CodeDuplicateHold names a second distinct purchase by the
	// same holder in the same season book: one holder holds one
	// patent per book, so a divergent replay conflicts instead of
	// duplicating the patent or its price.
	CodeDuplicateHold ErrorCode = "PATENT_DUPLICATE_HOLD"
	// CodeRevoked names life after revocation: a revoked patent
	// stays visible with its rule and reason, but its use ends.
	CodeRevoked ErrorCode = "PATENT_REVOKED"
	// CodeDeathClosed names life after the account died: the use
	// ends with the account, and a blessing restores
	// participation without recreating the patent.
	CodeDeathClosed ErrorCode = "PATENT_DEATH_CLOSED"
	// CodeRefundClosed names life after a service-failure refund
	// and any second or foreign-book refund: restitution happens
	// once, in the origin book, with a treasury receipt, and never
	// duplicates the patent or its price.
	CodeRefundClosed ErrorCode = "PATENT_REFUND_CLOSED"
	// CodeCapExceeded names a sale past the ratified seat cap: the
	// oversubscribed claim never lands, so neither patent nor
	// price duplicates beyond the approved supply.
	CodeCapExceeded ErrorCode = "PATENT_CAP_EXCEEDED"
	// CodeFrozenSale names a sale while the book is frozen: the
	// frozen book sells nothing, and publishing a new table over
	// it never thaws it by side effect.
	CodeFrozenSale ErrorCode = "PATENT_FROZEN_SALE"
	// CodeRetroactiveChange names any rewrite of the past: a
	// published version never changes content, and a granted
	// patent never reprices under a newer table.
	CodeRetroactiveChange ErrorCode = "PATENT_RETROACTIVE_CHANGE"
	// CodeAuctionUnavailable names any auction path: the auction
	// stays without endpoint, rule or execution, so an auction
	// bid refuses instead of opening a sale.
	CodeAuctionUnavailable ErrorCode = "PATENT_AUCTION_UNAVAILABLE"
	// CodeIneligibleBuyer names a buyer outside the ratified
	// eligible population: the table sells only to whom the
	// approval names, never by inference.
	CodeIneligibleBuyer ErrorCode = "PATENT_INELIGIBLE_BUYER"
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
	// ErrConsentRequired refuses an acquisition without voluntary acceptance.
	ErrConsentRequired = DomainError{
		Code:    CodeConsentRequired,
		Message: "the acceptance is missing: entry into the patent contract is voluntary, silence never binds",
	}
	// ErrDuplicateHold refuses a second distinct purchase in the same book.
	ErrDuplicateHold = DomainError{
		Code:    CodeDuplicateHold,
		Message: "the holder already holds this book: an identical replay returns the same holding, a different purchase conflicts",
	}
	// ErrRevoked refuses life after revocation.
	ErrRevoked = DomainError{
		Code:    CodeRevoked,
		Message: "the patent was revoked: the fact stays visible with its rule and reason, but its use ended",
	}
	// ErrDeathClosed refuses life after the account died.
	ErrDeathClosed = DomainError{
		Code:    CodeDeathClosed,
		Message: "the account died with its use: a blessing restores participation without recreating the patent",
	}
	// ErrRefundClosed refuses life after restitution and any second refund.
	ErrRefundClosed = DomainError{
		Code:    CodeRefundClosed,
		Message: "the price was restituted: restitution happens once, in the origin book, with a treasury receipt, and never duplicates",
	}
	// ErrCapExceeded refuses a sale past the ratified seat cap.
	ErrCapExceeded = DomainError{
		Code:    CodeCapExceeded,
		Message: "the seat cap is reached: the approved supply ends here, an oversubscribed claim never lands",
	}
	// ErrFrozenSale refuses a sale while the book is frozen.
	ErrFrozenSale = DomainError{
		Code:    CodeFrozenSale,
		Message: "the book is frozen: a frozen supply sells nothing, and a new table never thaws it by side effect",
	}
	// ErrRetroactiveChange refuses any rewrite of the past.
	ErrRetroactiveChange = DomainError{
		Code:    CodeRetroactiveChange,
		Message: "the past does not reprice: a published version never changes content, and a grant keeps its sale terms",
	}
	// ErrAuctionUnavailable refuses any auction path.
	ErrAuctionUnavailable = DomainError{
		Code:    CodeAuctionUnavailable,
		Message: "the auction has no endpoint: bidding refuses instead of opening a sale, until a separate rule exists",
	}
	// ErrIneligibleBuyer refuses a buyer outside the eligible population.
	ErrIneligibleBuyer = DomainError{
		Code:    CodeIneligibleBuyer,
		Message: "the buyer is not named by the approval: the table sells only to its eligible population, never by inference",
	}
)
