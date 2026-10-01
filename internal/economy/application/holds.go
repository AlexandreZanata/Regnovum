package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// Clock exposes wall-clock time to economy use cases, keeping expiry
// decisions deterministic under test. Production wires the real clock;
// tests inject a fixed instant.
type Clock interface {
	Now() time.Time
}

// ReserveCommand locks value of one owner custody inside a new hold:
// book, owner, purpose, amount and the deadline after which the hold
// may be marked expired. The season is mandatory: the hold lives in
// its book.
type ReserveCommand struct {
	Season     string
	OwnerKind  string
	OwnerLabel string
	Purpose    string
	Millis     int64
	ExpiresAt  time.Time
}

// SettleCommand names one hold for release, capture or expiry.
type SettleCommand struct {
	HoldID string
}

// CaptureCommand settles one hold by paying its full amount to a
// beneficiary custody.
type CaptureCommand struct {
	HoldID  string
	ToKind  string
	ToLabel string
}

// HoldView is the settled or recorded state of one hold.
type HoldView struct {
	HoldID         string
	OwnerCustodyID string
	HoldCustodyID  string
	Amount         domain.MilliInk
	Purpose        string
	Status         string
	ExpiresAt      time.Time
}

// HoldsRepository locks and settles generic value holds. Every mutation
// runs in one transaction with the legs it moves: holds never diverge
// from the journal.
type HoldsRepository interface {
	// Reserve locks the amount out of the owner custody into a new
	// dedicated hold custody in one season book. Reserved funds
	// leave the spendable balance until an explicit release or
	// capture.
	Reserve(ctx context.Context, reservation HoldReservation) (*HoldView, error)
	// Release settles a hold back to its owner in full. Settled holds
	// never reopen.
	Release(ctx context.Context, holdID string) (*HoldView, error)
	// Capture pays a hold in full to a beneficiary custody. Two
	// capturers never both win.
	Capture(ctx context.Context, holdID, toKind, toLabel string) (*HoldView, error)
	// Expire marks a lapsed hold without moving any leg. Expiry never
	// burns or transfers value by itself.
	Expire(ctx context.Context, holdID string) (*HoldView, error)
}

// HoldReservation is a validated hold opening for the repository
// port: the book, the owner, the purpose, the amount and the
// deadline after which the hold may be marked expired.
type HoldReservation struct {
	Season     domain.SeasonKey
	OwnerKind  domain.CustodyKind
	OwnerLabel string
	Purpose    domain.HoldPurpose
	Amount     domain.MilliInk
	ExpiresAt  time.Time
}

// ReserveUseCase validates and opens one value hold in one season
// book. It is an internal operation: no public surface calls it.
type ReserveUseCase struct {
	holds HoldsRepository
	clock Clock
	books SeasonBooks
}

// NewReserveUseCase creates an instance of ReserveUseCase.
func NewReserveUseCase(holds HoldsRepository, clock Clock, books SeasonBooks) *ReserveUseCase {
	return &ReserveUseCase{holds: holds, clock: clock, books: books}
}

// Execute validates the reservation and locks the amount.
func (uc *ReserveUseCase) Execute(ctx context.Context, cmd ReserveCommand) (*HoldView, error) {
	season, err := domain.ParseSeasonKey(cmd.Season)
	if err != nil {
		return nil, err
	}
	ownerKind, err := domain.ParseCustodyKind(cmd.OwnerKind)
	if err != nil {
		return nil, err
	}
	if cmd.OwnerLabel == "" {
		return nil, domain.ErrUnknownCustody
	}
	if !ownerKind.CanSpend() {
		return nil, domain.ErrUnauthorizedCustody
	}
	purpose, err := domain.ParseHoldPurpose(cmd.Purpose)
	if err != nil {
		return nil, err
	}
	amount, err := domain.NewMilliInk(cmd.Millis)
	if err != nil {
		return nil, err
	}
	if amount.IsZero() {
		return nil, domain.ErrInvalidHold
	}
	if cmd.ExpiresAt.IsZero() || !cmd.ExpiresAt.After(uc.clock.Now()) {
		return nil, domain.ErrInvalidHold
	}
	if err := uc.books.RequireActive(ctx, season); err != nil {
		return nil, err
	}
	return uc.holds.Reserve(ctx, HoldReservation{
		Season: season, OwnerKind: ownerKind, OwnerLabel: cmd.OwnerLabel,
		Purpose: purpose, Amount: amount, ExpiresAt: cmd.ExpiresAt.UTC(),
	})
}

// ReleaseUseCase settles one hold back to its owner in full.
type ReleaseUseCase struct {
	holds HoldsRepository
}

// NewReleaseUseCase creates an instance of ReleaseUseCase.
func NewReleaseUseCase(holds HoldsRepository) *ReleaseUseCase {
	return &ReleaseUseCase{holds: holds}
}

// Execute settles the named hold back to its owner.
func (uc *ReleaseUseCase) Execute(ctx context.Context, cmd SettleCommand) (*HoldView, error) {
	if cmd.HoldID == "" {
		return nil, domain.ErrHoldNotFound
	}
	return uc.holds.Release(ctx, cmd.HoldID)
}

// CaptureUseCase pays one hold in full to a beneficiary custody.
type CaptureUseCase struct {
	holds HoldsRepository
}

// NewCaptureUseCase creates an instance of CaptureUseCase.
func NewCaptureUseCase(holds HoldsRepository) *CaptureUseCase {
	return &CaptureUseCase{holds: holds}
}

// Execute pays the named hold to the beneficiary custody.
func (uc *CaptureUseCase) Execute(ctx context.Context, cmd CaptureCommand) (*HoldView, error) {
	if cmd.HoldID == "" {
		return nil, domain.ErrHoldNotFound
	}
	if _, err := domain.ParseCustodyKind(cmd.ToKind); err != nil {
		return nil, err
	}
	if cmd.ToLabel == "" {
		return nil, domain.ErrUnknownCustody
	}
	return uc.holds.Capture(ctx, cmd.HoldID, cmd.ToKind, cmd.ToLabel)
}

// ExpireUseCase marks one lapsed hold expired without moving value.
type ExpireUseCase struct {
	holds HoldsRepository
}

// NewExpireUseCase creates an instance of ExpireUseCase.
func NewExpireUseCase(holds HoldsRepository) *ExpireUseCase {
	return &ExpireUseCase{holds: holds}
}

// Execute marks the named hold expired once its deadline passed.
func (uc *ExpireUseCase) Execute(ctx context.Context, cmd SettleCommand) (*HoldView, error) {
	if cmd.HoldID == "" {
		return nil, domain.ErrHoldNotFound
	}
	return uc.holds.Expire(ctx, cmd.HoldID)
}
