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
// owner, purpose, amount and the deadline after which the hold may be
// marked expired.
type ReserveCommand struct {
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
	// dedicated hold custody. Reserved funds leave the spendable
	// balance until an explicit release or capture.
	Reserve(ctx context.Context, ownerKind domain.CustodyKind, ownerLabel string, purpose domain.HoldPurpose, amount domain.MilliInk, expiresAt time.Time) (*HoldView, error)
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

// ReserveUseCase validates and opens one value hold. It is an internal
// operation: no public surface calls it.
type ReserveUseCase struct {
	holds HoldsRepository
	clock Clock
}

// NewReserveUseCase creates an instance of ReserveUseCase.
func NewReserveUseCase(holds HoldsRepository, clock Clock) *ReserveUseCase {
	return &ReserveUseCase{holds: holds, clock: clock}
}

// Execute validates the reservation and locks the amount.
func (uc *ReserveUseCase) Execute(ctx context.Context, cmd ReserveCommand) (*HoldView, error) {
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
	return uc.holds.Reserve(ctx, ownerKind, cmd.OwnerLabel, purpose, amount, cmd.ExpiresAt.UTC())
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
