package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// GrantArenaPassesCommand holds the parameters of an Arena Pass grant. The
// expiration rule follows the origin: PURCHASE never expires, MEMBER
// requires the end of its period and ADMIN may optionally expire.
type GrantArenaPassesCommand struct {
	AccountID string
	Origin    string
	Quantity  int32
	Reference string
	ExpiresAt *time.Time
}

// GrantArenaPassesUseCase validates and persists an idempotent Arena Pass
// grant. The same (account, origin, reference) returns the original lot
// without duplicating the entitlement.
type GrantArenaPassesUseCase struct {
	lots  PassLotRepository
	clock Clock
}

// NewGrantArenaPassesUseCase creates an instance of
// GrantArenaPassesUseCase.
func NewGrantArenaPassesUseCase(lots PassLotRepository, clock Clock) *GrantArenaPassesUseCase {
	return &GrantArenaPassesUseCase{lots: lots, clock: clock}
}

// Execute validates the grant, resolves its expiration in UTC and delegates
// the idempotent write to the repository.
func (uc *GrantArenaPassesUseCase) Execute(ctx context.Context, cmd GrantArenaPassesCommand) (*GrantPassLotResult, error) {
	accountID := domain.AccountID(cmd.AccountID)
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	origin, err := domain.ParsePassOrigin(cmd.Origin)
	if err != nil {
		return nil, err
	}

	quantity, err := domain.NewQuantity(cmd.Quantity)
	if err != nil {
		return nil, err
	}

	reference, err := domain.ParseReference(cmd.Reference)
	if err != nil {
		return nil, err
	}

	expiresAt, err := resolveExpiration(origin, cmd.ExpiresAt)
	if err != nil {
		return nil, err
	}

	return uc.lots.GrantPassLot(ctx, GrantPassLotRequest{
		AccountID: accountID,
		Origin:    origin,
		Quantity:  quantity,
		Reference: reference,
		ExpiresAt: expiresAt,
		GrantedAt: uc.clock.Now(),
	})
}

// resolveExpiration enforces the origin rule and normalizes the instant to
// UTC. Expirations are immutable: the repository never updates the stored
// value, so a replay keeps the original instant even if the retry payload
// carries another one.
func resolveExpiration(origin domain.PassOrigin, raw *time.Time) (*time.Time, error) {
	switch origin {
	case domain.OriginPurchase:
		if raw != nil {
			return nil, domain.ErrExpirationForbidden
		}
		return nil, nil
	case domain.OriginMember:
		if raw == nil {
			return nil, domain.ErrExpirationRequired
		}
	case domain.OriginAdmin:
		if raw == nil {
			return nil, nil
		}
	default:
		return nil, domain.ErrInvalidPassOrigin
	}

	instant := raw.UTC()
	return &instant, nil
}
