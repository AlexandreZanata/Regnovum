package domain

// CustodyKind is the closed vocabulary of Genesis custody classes. Kinds
// follow docs/reino/LEDGER_CONTRACT.md §1: every unit lives in exactly one
// custody, and locked holds (escrow, contract, title) release only by
// their own conditions, never by a plain transfer.
type CustodyKind string

const (
	// CustodyTreasury is the Genesis home: the single origin of supply.
	CustodyTreasury CustodyKind = "treasury"
	// CustodyUser is a holder account.
	CustodyUser CustodyKind = "user"
	// CustodyEscrow is a locked conditional hold.
	CustodyEscrow CustodyKind = "escrow"
	// CustodyContract is a locked agreement hold.
	CustodyContract CustodyKind = "contract"
	// CustodyTitle is a locked principal hold.
	CustodyTitle CustodyKind = "title"
)

// ParseCustodyKind validates a custody class against the schema CHECK
// vocabulary. Matching is exact: surrounding whitespace is not trimmed,
// so two spellings can never name one kind. Unknown kinds are refused.
func ParseCustodyKind(raw string) (CustodyKind, error) {
	kind := CustodyKind(raw)
	if !kind.IsValid() {
		return "", ErrUnknownCustody
	}
	return kind, nil
}

// IsValid reports whether the kind belongs to the closed vocabulary.
func (k CustodyKind) IsValid() bool {
	switch k {
	case CustodyTreasury, CustodyUser, CustodyEscrow, CustodyContract, CustodyTitle:
		return true
	default:
		return false
	}
}

// CanSpend reports whether the kind may originate a plain transfer.
// Treasury and user holds move freely; locked holds release only by
// their own release conditions (later phases), so spending from them
// here is refused before any balance is read.
func (k CustodyKind) CanSpend() bool {
	switch k {
	case CustodyTreasury, CustodyUser:
		return true
	default:
		return false
	}
}

// String returns the stored kind value.
func (k CustodyKind) String() string {
	return string(k)
}
