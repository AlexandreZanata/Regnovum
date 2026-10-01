package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// TransferCommand moves existing INK between two custodies. Labels name
// the custodies inside their kinds; the amount travels in milliINK.
// Both books are mandatory and must match: origin and destination
// resolve inside one book, and cross-book moves never happen.
type TransferCommand struct {
	FromSeason string
	FromKind   string
	FromLabel  string
	ToSeason   string
	ToKind     string
	ToLabel    string
	Millis     int64
}

// TransferRequest is a validated transfer for the repository port.
type TransferRequest struct {
	FromSeason domain.SeasonKey
	FromKind   domain.CustodyKind
	FromLabel  string
	ToSeason   domain.SeasonKey
	ToKind     domain.CustodyKind
	ToLabel    string
	Amount     domain.MilliInk
}

// TransferResult is the outcome of a transfer: the business intention
// carried by both legs, and the exact debited and credited amounts.
type TransferResult struct {
	TransferID string
	Debited    domain.MilliInk
	Credited   domain.MilliInk
}

// TransferRepository moves value between custodies. Implementations lock
// both custodies in canonical order, refuse negative balances and locked
// sources, and commit the debit/credit pair in one transaction.
type TransferRepository interface {
	// Transfer debits the source and credits the destination atomically.
	// Unknown custodies, locked sources, same-custody pairs and
	// insufficient balances fail without writing any leg.
	Transfer(ctx context.Context, request TransferRequest) (*TransferResult, error)
}

// TransferUseCase validates and moves existing INK between custodies
// of one season book. It is an internal operation: no public surface
// calls it.
type TransferUseCase struct {
	transfers TransferRepository
	books     SeasonBooks
}

// NewTransferUseCase creates an instance of TransferUseCase.
func NewTransferUseCase(transfers TransferRepository, books SeasonBooks) *TransferUseCase {
	return &TransferUseCase{transfers: transfers, books: books}
}

// Execute validates the transfer and moves the amount atomically
// inside one book, refusing cross-book moves before anything is read.
func (uc *TransferUseCase) Execute(ctx context.Context, cmd TransferCommand) (*TransferResult, error) {
	fromSeason, err := domain.ParseSeasonKey(cmd.FromSeason)
	if err != nil {
		return nil, err
	}
	toSeason, err := domain.ParseSeasonKey(cmd.ToSeason)
	if err != nil {
		return nil, err
	}
	if fromSeason != toSeason {
		return nil, domain.ErrCrossSeason
	}
	ends, err := validateTransferEnds(cmd.FromKind, cmd.FromLabel, cmd.ToKind, cmd.ToLabel, cmd.Millis)
	if err != nil {
		return nil, err
	}
	if err := uc.books.RequireActive(ctx, fromSeason); err != nil {
		return nil, err
	}
	return uc.transfers.Transfer(ctx, TransferRequest{
		FromSeason: fromSeason,
		FromKind:   ends.fromKind,
		FromLabel:  ends.fromLabel,
		ToSeason:   toSeason,
		ToKind:     ends.toKind,
		ToLabel:    ends.toLabel,
		Amount:     ends.amount,
	})
}
