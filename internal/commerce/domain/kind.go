package domain

// TransferKind names the business nature of one voluntary movement:
// a personal gift carries no consideration, a formal trade settles a
// commercial service, a refund reverses a previous settlement and a
// treasury movement reallocates sovereign stock. The kind travels
// inside the sealed intention: a settled transfer never changes
// kind, and tithe, holds and escrow attach by kind in later tasks,
// never by inference from amount or parties.
type TransferKind string

const (
	// TransferGift is a personal present without consideration: it
	// never bears tithe and never requires a commercial contract.
	TransferGift TransferKind = "gift"
	// TransferTrade is a formal commercial payment for a service:
	// it settles only against an accepted commercial intent and
	// bears tithe at settlement.
	TransferTrade TransferKind = "trade"
	// TransferRefund reverses a previous settlement in whole or in
	// part, linked to its cause.
	TransferRefund TransferKind = "refund"
	// TransferTreasury reallocates sovereign stock between vaults:
	// it is never a payment to a holder.
	TransferTreasury TransferKind = "treasury"
)

// AllTransferKinds returns the closed vocabulary in canonical order.
func AllTransferKinds() []TransferKind {
	return []TransferKind{TransferGift, TransferTrade, TransferRefund, TransferTreasury}
}

// ParseTransferKind validates a kind against the closed vocabulary.
// Matching is exact: surrounding whitespace is not trimmed, case is
// not folded and combined or empty values refuse. An absent or
// ambiguous kind never defaults to another.
func ParseTransferKind(raw string) (TransferKind, error) {
	kind := TransferKind(raw)
	if !kind.IsValid() {
		return "", ErrInvalidTransferKind
	}
	return kind, nil
}

// IsValid reports whether the kind belongs to the closed vocabulary.
func (k TransferKind) IsValid() bool {
	switch k {
	case TransferGift, TransferTrade, TransferRefund, TransferTreasury:
		return true
	default:
		return false
	}
}

// String returns the stored kind value.
func (k TransferKind) String() string { return string(k) }

// BearsTithe reports whether the kind settles with tithe: only
// formal trade does. Gifts never bear tithe, refunds compensate
// through their own linked entries and treasury movements are not
// payments. The rate itself arrives per settlement in a later task;
// this predicate only routes, never prices.
func (k TransferKind) BearsTithe() bool {
	return k == TransferTrade
}
