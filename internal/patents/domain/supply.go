package domain

import (
	"time"
)

// Sale modes: the direct sale is the only path the domain
// executes. The auction mode exists so the refusal is explicit:
// it names the unavailable path instead of leaving it silent.
const (
	// SaleModeDirect is the ratified direct sale under a table.
	SaleModeDirect = "direta"
	// SaleModeAuction is the unavailable auction path: it always
	// refuses, until a separate approved rule exists.
	SaleModeAuction = "leilao"
)

// SupplyTable is one ratified price-and-seats decision: its
// version, price in minor units with ISO currency, eligible
// population, seat cap, duration, cosmetic grant and season
// book. Every value arrives in the approval: the domain holds
// no price, cap or population default, so a missing term blocks
// instead of inventing one. A published table is immutable,
// and a frozen book sells nothing.
type SupplyTable struct {
	Version         string
	PriceMinorUnits int64
	Currency        string
	Eligible        string
	Cap             int64
	Duration        time.Duration
	CosmeticGrant   string
	Season          SeasonKey
	Frozen          bool
	FreezeReason    string
}

// ValidateSupplyTable checks one table: whole version, currency,
// eligible population, cosmetic grant and season, with positive
// price, cap and duration. A missing price, cap or population
// refuses as a missing ratified term.
func ValidateSupplyTable(table SupplyTable) error {
	if _, err := parsePatentToken(table.Version); err != nil {
		return ErrTermsMissing
	}
	if _, err := parsePatentToken(table.Currency); err != nil {
		return ErrTermsMissing
	}
	if _, err := parsePatentToken(table.Eligible); err != nil {
		return ErrTermsMissing
	}
	if _, err := parsePatentDetail(table.CosmeticGrant); err != nil {
		return ErrTermsMissing
	}
	if _, err := ParseSeasonKey(string(table.Season)); err != nil {
		return ErrTermsMissing
	}
	if table.PriceMinorUnits <= 0 || table.Cap <= 0 || table.Duration <= 0 {
		return ErrTermsMissing
	}
	return nil
}

// TermsOf derives the sale terms of one valid table: the grant
// layer (T01) sells exactly what the table approves, so price
// and duration never come from a second source.
func TermsOf(table SupplyTable) (ApprovedTerms, error) {
	if err := ValidateSupplyTable(table); err != nil {
		return ApprovedTerms{}, err
	}
	return ApprovedTerms{
		Version: table.Version, PriceMinorUnits: table.PriceMinorUnits,
		Currency: table.Currency, Seats: table.Cap,
		Duration: table.Duration, CosmeticGrant: table.CosmeticGrant,
	}, nil
}

// IssuedSeat is one consumed seat: the purchase identity, the
// buyer, its position in the book and when it was issued.
// Positions start at 1 and never exceed the cap.
type IssuedSeat struct {
	PurchaseID string
	Buyer      string
	Position   int64
	IssuedAt   time.Time
}

// SupplyBook is the seat ledger of one season book: the current
// table and the append-only issued seats. Counts are positions,
// never money: the book says who holds which seat, not what INK
// moved. Concurrency control belongs to the future adapter
// transaction, not to this value.
type SupplyBook struct {
	Season SeasonKey
	Table  SupplyTable
	Seats  []IssuedSeat
}

// NewSupplyBook opens the empty seat ledger of one season book.
func NewSupplyBook(season SeasonKey) (SupplyBook, error) {
	if _, err := ParseSeasonKey(string(season)); err != nil {
		return SupplyBook{}, err
	}
	return SupplyBook{Season: season}, nil
}

// findSeat answers whether the purchase identity already holds a
// seat in this book.
func findSeat(book SupplyBook, purchaseID string) (IssuedSeat, bool) {
	for _, seat := range book.Seats {
		if seat.PurchaseID == purchaseID {
			return seat, true
		}
	}
	return IssuedSeat{}, false
}

// PublishTable installs one ratified table over a book. A virgin
// book takes the first valid table; an identical republication
// returns the book unchanged; the same version with different
// content refuses as a retroactive rewrite; a new version only
// prices future sales, never the issued seats. A frozen book
// takes no table: freezing is terminal, never thawed by side
// effect.
func PublishTable(book SupplyBook, table SupplyTable) (SupplyBook, error) {
	if err := ValidateSupplyTable(table); err != nil {
		return SupplyBook{}, err
	}
	if table.Season != book.Season {
		return SupplyBook{}, ErrInvalidGrant
	}
	if book.Table.Frozen {
		return SupplyBook{}, ErrFrozenSale
	}
	if book.Table.Version == "" {
		book.Table = table
		return book, nil
	}
	if table.Version == book.Table.Version {
		if table == book.Table {
			return book, nil
		}
		return SupplyBook{}, ErrRetroactiveChange
	}
	book.Table = table
	return book, nil
}

// FreezeRequest carries one sales freeze: the book, when, and
// the reason that ordered it.
type FreezeRequest struct {
	Book   SupplyBook
	At     time.Time
	Reason string
}

// FreezeSales freezes one book: from then on it sells nothing.
// An identical replay returns the same book; freezing an
// already frozen book under a different fact refuses, so the
// freeze reason stays single.
func FreezeSales(req FreezeRequest) (SupplyBook, error) {
	if req.At.IsZero() {
		return SupplyBook{}, ErrInvalidGrant
	}
	reason, err := parsePatentDetail(req.Reason)
	if err != nil {
		return SupplyBook{}, err
	}
	book := req.Book
	if book.Table.Frozen {
		if book.Table.FreezeReason == reason {
			return book, nil
		}
		return SupplyBook{}, ErrFrozenSale
	}
	if book.Table.Version == "" {
		return SupplyBook{}, ErrTermsMissing
	}
	book.Table.Frozen = true
	book.Table.FreezeReason = reason
	return book, nil
}

// IssueRequest carries one seat claim: who buys, in which
// population, by which sale mode, under which purchase identity
// and when.
type IssueRequest struct {
	PurchaseID string
	Buyer      string
	Population string
	SaleMode   string
	At         time.Time
}

// IssueSeat consumes one seat of the current table. The auction
// mode refuses without endpoint; an identical replay returns the
// same seat, so neither patent nor price duplicates; a frozen
// book, a stranger to the eligible population and a book past
// its cap all refuse with no new seat. A reused purchase
// identity by another buyer conflicts.
func IssueSeat(book SupplyBook, req IssueRequest) (SupplyBook, IssuedSeat, error) {
	if req.SaleMode == SaleModeAuction {
		return SupplyBook{}, IssuedSeat{}, ErrAuctionUnavailable
	}
	if req.SaleMode != SaleModeDirect {
		return SupplyBook{}, IssuedSeat{}, ErrInvalidGrant
	}
	purchaseID, err := parsePatentToken(req.PurchaseID)
	if err != nil {
		return SupplyBook{}, IssuedSeat{}, err
	}
	buyer, err := parsePatentToken(req.Buyer)
	if err != nil {
		return SupplyBook{}, IssuedSeat{}, err
	}
	population, err := parsePatentToken(req.Population)
	if err != nil {
		return SupplyBook{}, IssuedSeat{}, err
	}
	if req.At.IsZero() {
		return SupplyBook{}, IssuedSeat{}, ErrInvalidGrant
	}
	if err := ValidateSupplyTable(book.Table); err != nil {
		return SupplyBook{}, IssuedSeat{}, err
	}
	if book.Table.Season != book.Season {
		return SupplyBook{}, IssuedSeat{}, ErrInvalidGrant
	}
	if recorded, found := findSeat(book, purchaseID); found {
		if recorded.Buyer == buyer {
			return book, recorded, nil
		}
		return SupplyBook{}, IssuedSeat{}, ErrDuplicateHold
	}
	if book.Table.Frozen {
		return SupplyBook{}, IssuedSeat{}, ErrFrozenSale
	}
	if population != book.Table.Eligible {
		return SupplyBook{}, IssuedSeat{}, ErrIneligibleBuyer
	}
	if int64(len(book.Seats)) >= book.Table.Cap {
		return SupplyBook{}, IssuedSeat{}, ErrCapExceeded
	}
	seat := IssuedSeat{
		PurchaseID: purchaseID, Buyer: buyer,
		Position: int64(len(book.Seats)) + 1, IssuedAt: req.At.UTC(),
	}
	book.Seats = append(book.Seats, seat)
	return book, seat, nil
}

// RepriceGrant always refuses: a granted patent keeps its sale
// terms, and a newer table prices future sales only. The past
// never reprices, not even to a cheaper table.
func RepriceGrant(holding Holding, table SupplyTable) error {
	if holding.Grant.ID == "" {
		return ErrInvalidGrant
	}
	if err := ValidateSupplyTable(table); err != nil {
		return err
	}
	return ErrRetroactiveChange
}
