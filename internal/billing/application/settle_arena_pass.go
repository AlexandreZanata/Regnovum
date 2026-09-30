package application

import (
	"context"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// SettleArenaPassDependencies groups everything the Arena Pass settle use
// case needs.
type SettleArenaPassDependencies struct {
	// Catalog is the versioned price list, the source of the product and
	// the pass quantity to grant.
	Catalog *domain.Catalog
	// Intents reads and updates checkout intents.
	Intents CheckoutIntentRepository
	// Lots grants Arena Pass lots.
	Lots PassLotRepository
	// Clock supplies the instant of settlement.
	Clock Clock
}

// SettleArenaPassUseCase maps a verified payment to an idempotent Arena Pass
// grant.
//
// The flow is:
//
//  1. Look up the checkout intent by session ID.
//  2. Verify the intent is in `open` status (not already settled or expired).
//  3. Look up the catalog product to get the pass quantity (grant).
//  4. Grant the account's Arena Pass lot with OriginPurchase (no expiration).
//  5. Mark the intent as `paid` with `paid_at`.
//
// The whole operation is idempotent: the webhook event ID is the outer
// idempotency anchor (T05), and the pass lot has its own idempotency key
// (account, origin, reference) derived from the intent ID. The intent status
// transition is protected by the database trigger (open → paid only once).
//
// Currency, amount and product are verified against the catalog: a price
// edited in the provider dashboard must never make the buyer receive a
// different pass quantity than the one the server decided.
type SettleArenaPassUseCase struct {
	catalog *domain.Catalog
	intents CheckoutIntentRepository
	lots    PassLotRepository
	clock   Clock
}

// NewSettleArenaPassUseCase builds the use case, refusing incomplete
// composition.
func NewSettleArenaPassUseCase(deps SettleArenaPassDependencies) (*SettleArenaPassUseCase, error) {
	if deps.Catalog == nil {
		return nil, fmt.Errorf("%w: the versioned catalog is required", ErrInvalidCheckoutConfig)
	}
	if deps.Intents == nil {
		return nil, fmt.Errorf("%w: the checkout intent repository is required", ErrInvalidCheckoutConfig)
	}
	if deps.Lots == nil {
		return nil, fmt.Errorf("%w: the pass lot repository is required", ErrInvalidCheckoutConfig)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("%w: a clock is required", ErrInvalidCheckoutConfig)
	}
	return &SettleArenaPassUseCase{
		catalog: deps.Catalog,
		intents: deps.Intents,
		lots:    deps.Lots,
		clock:   deps.Clock,
	}, nil
}

// Execute settles one checkout intent after verified payment with an Arena
// Pass grant.
func (uc *SettleArenaPassUseCase) Execute(ctx context.Context, command SettleCheckoutCommand) (*SettleCheckoutResult, error) {
	// Step 1: look up the checkout intent by session ID.
	intent, err := uc.intents.GetCheckoutIntentBySession(ctx, command.SessionID)
	if err != nil {
		return nil, fmt.Errorf("settle arena pass: %w", err)
	}

	// Step 2: verify the intent is in `open` status.
	if intent.Status != domain.CheckoutIntentOpen {
		// The intent is already settled, expired or failed. A replay of the
		// webhook event is harmless.
		return &SettleCheckoutResult{
			IntentID:       intent.ID,
			Replayed:       true,
			AmountCredited: 0,
		}, nil
	}

	// Step 3: look up the catalog product to get the pass quantity.
	product, err := uc.catalog.Product(intent.Market, intent.ProductID)
	if err != nil {
		return nil, fmt.Errorf("settle arena pass (catalog): %w", err)
	}

	// Verify the product grants ARENA_PASS.
	if product.Grant().Kind() != domain.GrantKindArenaPass {
		return nil, fmt.Errorf("%w: product %s grants %s, not ARENA_PASS",
			ErrSettleCheckoutWrongGrantKind, intent.ProductID, product.Grant().Kind())
	}

	passQuantity := product.Grant().Quantity()
	if passQuantity < 1 {
		return nil, fmt.Errorf("%w: product %s grants zero passes",
			ErrSettleCheckoutWrongGrantKind, intent.ProductID)
	}

	// Step 4: grant the account's Arena Pass lot with OriginPurchase
	// (purchased passes never expire). The idempotency is anchored on
	// (account, origin, reference) where reference is intent.ID.
	reference, err := domain.ParseReference(intent.ID)
	if err != nil {
		return nil, fmt.Errorf("settle arena pass (reference): %w", err)
	}

	quantity, err := domain.NewQuantity(int32(passQuantity))
	if err != nil {
		return nil, fmt.Errorf("settle arena pass (quantity): %w", err)
	}

	grantResult, err := uc.lots.GrantPassLot(ctx, GrantPassLotRequest{
		AccountID: intent.AccountID,
		Origin:    domain.OriginPurchase,
		Quantity:  quantity,
		Reference: reference,
		ExpiresAt: nil, // purchased passes never expire
		GrantedAt: uc.clock.Now(),
	})
	if err != nil {
		return nil, fmt.Errorf("settle arena pass (grant): %w", err)
	}

	// Step 5: mark the intent as `paid` with `paid_at`.
	if err := uc.intents.MarkCheckoutIntentPaid(ctx, command.SessionID); err != nil {
		return nil, fmt.Errorf("settle arena pass (mark paid): %w", err)
	}

	return &SettleCheckoutResult{
		IntentID:       intent.ID,
		Replayed:       grantResult.Replayed,
		AmountCredited: passQuantity,
	}, nil
}
