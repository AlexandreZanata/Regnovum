package domain

import (
	"time"
)

// TreasuryDestination is the only lawful destination of patent
// price: the treasury. It names a rule, never an amount: every
// value still arrives in ratified terms and in the receipt.
const TreasuryDestination = "tesouro"

// ServiceFailureReason is the only lawful reason for restitution:
// a failure of the service itself. Any other reason refuses,
// because the domain returns unusable price, never damages or
// earnings.
const ServiceFailureReason = "falha-do-servico"

// Holding is one acquired patent with its lifecycle facts: the
// grant, whether a rule revoked it and why, whether the account
// died with its use, and whether a service failure restituted
// its price with a treasury receipt. The zero value is an empty
// shell: nothing is usable before AcquireHolding.
type Holding struct {
	Grant         PatentGrant
	Revoked       bool
	RevokeReason  string
	RevokeRule    string
	RevokedAt     time.Time
	DeadClosed    bool
	ClosedAt      time.Time
	Refunded      bool
	RefundReceipt RefundReceipt
	RefundedAt    time.Time
}

// RefundReceipt is the treasury receipt of one restitution: its
// identity, the restituted minor-unit amount with ISO currency,
// the origin book it returns to, and the treasury destination.
// It records a fact: no value moves here, the product stays
// disabled, and adapters move nothing without P44 and release.
type RefundReceipt struct {
	ID               string
	AmountMinorUnits int64
	Currency         string
	Book             SeasonKey
	Treasury         string
}

// AcquireRequest carries one voluntary acquisition: the sale to
// grant and whether the holder explicitly accepted the patent
// contract. Silence never binds.
type AcquireRequest struct {
	Grant    GrantRequest
	Accepted bool
}

// AcquireHolding sells one patent to a holder that explicitly
// accepted its contract. Without acceptance the sale refuses
// with the holding untouched.
func AcquireHolding(req AcquireRequest) (Holding, error) {
	if !req.Accepted {
		return Holding{}, ErrConsentRequired
	}
	grant, err := GrantPatent(req.Grant)
	if err != nil {
		return Holding{}, err
	}
	return Holding{Grant: grant}, nil
}

// samePurchase answers whether the request replays the holding:
// same identity, holder, book, title, terms and instant. Only a
// whole replay is idempotent; any difference is a new purchase.
func samePurchase(holding Holding, req GrantRequest) bool {
	grant := holding.Grant
	termsVersion := ""
	if req.Terms != nil {
		termsVersion = req.Terms.Version
	}
	return grant.ID == req.ID &&
		grant.Holder == req.Holder &&
		string(grant.Season) == req.Season &&
		grant.Title == req.Title &&
		grant.TermsVersion == termsVersion &&
		grant.GrantedAt.UTC().Equal(req.GrantedAt.UTC())
}

// AcquireSecond replays or refuses a second purchase by the same
// holder in the same book. An identical replay returns the same
// holding, so neither patent nor price duplicates; a different
// purchase conflicts, which also settles seat fights between two
// buyers of one place: the second distinct claim never lands.
// Seats across holders are a T03 supply table, not this check.
func AcquireSecond(holding Holding, req GrantRequest) (Holding, error) {
	if samePurchase(holding, req) {
		return holding, nil
	}
	return Holding{}, ErrDuplicateHold
}

// UseRequest carries one use check: the holding, the instant,
// and whether the account returned blessed. Blessing is carried
// so the rule can say it plainly: it restores participation,
// never the patent.
type UseRequest struct {
	Holding Holding
	At      time.Time
	Blessed bool
}

// CheckUse answers whether the holding is usable at the instant.
// Terminal facts dominate the window: a revoked, dead-closed or
// refunded holding refuses even inside its dates, and a blessed
// return changes nothing about the patent.
func CheckUse(req UseRequest) error {
	holding := req.Holding
	switch {
	case holding.Revoked:
		return ErrRevoked
	case holding.DeadClosed:
		return ErrDeathClosed
	case holding.Refunded:
		return ErrRefundClosed
	}
	if req.At.IsZero() {
		return ErrInvalidGrant
	}
	if !holding.Grant.IsActive(req.At) {
		return ErrExpired
	}
	return nil
}

// IsUsable is the boolean view of CheckUse for gates that branch
// on liveness instead of naming the refusal.
func (h Holding) IsUsable(at time.Time) bool {
	return CheckUse(UseRequest{Holding: h, At: at}) == nil
}

// RevokeRequest carries one rule revocation: the holding, when,
// and the reason with the rule that ordered it.
type RevokeRequest struct {
	Holding Holding
	At      time.Time
	Reason  string
	Rule    string
}

// RevokeHolding ends the use of a live holding by rule. The fact
// stays visible beside its reason and rule; identical replays of
// the same revocation return the same holding, a holding that
// already ended refuses, and revoking outside the live window
// refuses with the holding untouched.
func RevokeHolding(req RevokeRequest) (Holding, error) {
	if req.At.IsZero() {
		return Holding{}, ErrInvalidGrant
	}
	reason, err := parsePatentDetail(req.Reason)
	if err != nil {
		return Holding{}, err
	}
	rule, err := parsePatentToken(req.Rule)
	if err != nil {
		return Holding{}, err
	}
	holding := req.Holding
	if holding.Revoked {
		if holding.RevokeReason == reason && holding.RevokeRule == rule &&
			holding.RevokedAt.UTC().Equal(req.At.UTC()) {
			return holding, nil
		}
		return Holding{}, ErrRevoked
	}
	if err := CheckUse(UseRequest{Holding: holding, At: req.At}); err != nil {
		return Holding{}, err
	}
	holding.Revoked = true
	holding.RevokeReason = reason
	holding.RevokeRule = rule
	holding.RevokedAt = req.At.UTC()
	return holding, nil
}

// CloseForDeath ends the use of a holding with its dead account.
// Closing twice is idempotent: the first instant wins and the
// fact stays single.
func CloseForDeath(holding Holding, at time.Time) (Holding, error) {
	if at.IsZero() {
		return Holding{}, ErrInvalidGrant
	}
	if holding.DeadClosed {
		return holding, nil
	}
	holding.DeadClosed = true
	holding.ClosedAt = at.UTC()
	return holding, nil
}

// RefundRequest carries one service-failure restitution: the
// holding, when, the lawful reason, and the treasury receipt
// fields. The book must be the origin book of the grant: a
// refund never lands in another season.
type RefundRequest struct {
	Holding          Holding
	At               time.Time
	Reason           string
	ReceiptID        string
	AmountMinorUnits int64
	Currency         string
	Treasury         string
	Book             string
}

// RefundHolding restitutes the price of a usable holding after a
// service failure, once, in the origin book, with a treasury
// receipt. The sale unwinds: the holding stops being usable, an
// identical replay returns the same holding, and any second,
// divergent or foreign-book refund refuses with no new effect.
func RefundHolding(req RefundRequest) (Holding, error) {
	if req.At.IsZero() {
		return Holding{}, ErrInvalidGrant
	}
	if req.Reason != ServiceFailureReason {
		return Holding{}, ErrInvalidGrant
	}
	receiptID, err := parsePatentToken(req.ReceiptID)
	if err != nil {
		return Holding{}, err
	}
	currency, err := parsePatentToken(req.Currency)
	if err != nil {
		return Holding{}, err
	}
	treasury, err := parsePatentToken(req.Treasury)
	if err != nil {
		return Holding{}, err
	}
	if treasury != TreasuryDestination {
		return Holding{}, ErrInvalidGrant
	}
	if req.AmountMinorUnits <= 0 {
		return Holding{}, ErrInvalidGrant
	}
	holding := req.Holding
	if holding.Refunded {
		receipt := holding.RefundReceipt
		if receipt.ID == receiptID &&
			receipt.AmountMinorUnits == req.AmountMinorUnits &&
			receipt.Currency == currency &&
			string(receipt.Book) == req.Book &&
			holding.RefundedAt.UTC().Equal(req.At.UTC()) {
			return holding, nil
		}
		return Holding{}, ErrRefundClosed
	}
	if err := CheckUse(UseRequest{Holding: holding, At: req.At}); err != nil {
		return Holding{}, err
	}
	if req.Book != string(holding.Grant.Season) {
		return Holding{}, ErrRefundClosed
	}
	holding.Refunded = true
	holding.RefundReceipt = RefundReceipt{
		ID: receiptID, AmountMinorUnits: req.AmountMinorUnits,
		Currency: currency, Book: holding.Grant.Season, Treasury: treasury,
	}
	holding.RefundedAt = req.At.UTC()
	return holding, nil
}
