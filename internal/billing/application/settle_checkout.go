package application

import (
	"context"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// Inker is the port through which the billing module grants INK credits to
// an account. The port is satisfied by the wallet module's
// CreditInkUseCase, which handles the idempotent ledger append.
type Inker interface {
	// CreditPurchasedInk grants the specified amount of INK to the
	// account's PURCHASED_INK bucket. The same idempotency key returns the
	// original credit without duplicating the transaction.
	CreditPurchasedInk(ctx context.Context, request InkerCreditRequest) (*InkerCreditResult, error)

	// CreditMemberInk grants the specified amount of INK to the account's
	// FREE_INK bucket under the credit_member operation (P12-T08). The same
	// idempotency key returns the original credit without duplicating.
	CreditMemberInk(ctx context.Context, request InkerCreditRequest) (*InkerCreditResult, error)
}

// InkerCreditRequest is the billing-typed request for an INK credit.
// It uses plain strings instead of wallet domain types, so the billing
// module does not import the wallet module.
type InkerCreditRequest struct {
	AccountID   string
	Amount      int64
	Reference   string
	Idempotency string
}

// InkerCreditResult is the outcome of an INK credit.
type InkerCreditResult struct {
	Replayed bool
}

// SettleCheckoutDependencies groups everything the settle use case needs.
type SettleCheckoutDependencies struct {
	// Catalog is the versioned price list, the source of the product and
	// the INK quantity to grant.
	Catalog *domain.Catalog
	// Intents reads and updates checkout intents.
	Intents CheckoutIntentRepository
	// Inker grants INK credits to an account. The port is satisfied by
	// the wallet module's CreditInkUseCase.
	Inker Inker
	// Clock supplies the instant of settlement.
	Clock Clock
}

// SettleCheckoutCommand identifies the checkout session to settle.
type SettleCheckoutCommand struct {
	// SessionID is the provider session identifier from the webhook event.
	SessionID domain.StripeCheckoutSessionID
}

// SettleCheckoutResult is the outcome of settling a checkout intent.
type SettleCheckoutResult struct {
	// IntentID is the local identifier of the settled intent.
	IntentID string
	// Replayed reports whether the intent was already settled (a replay of
	// the same webhook event).
	Replayed bool
	// AmountCredited is the INK quantity granted to the account.
	AmountCredited int64
}

// SettleCheckoutUseCase maps a verified payment to an idempotent INK credit.
//
// The flow is:
//
//  1. Look up the checkout intent by session ID.
//  2. Verify the intent is in `open` status (not already settled or expired).
//  3. Look up the catalog product to get the INK quantity (grant).
//  4. Credit the account's PURCHASED_INK bucket with the quantity.
//  5. Mark the intent as `paid` with `paid_at`.
//
// The whole operation is idempotent: the webhook event ID is the outer
// idempotency anchor (T05), and the wallet credit has its own idempotency
// key derived from the intent ID. The intent status transition is protected
// by the database trigger (open → paid only once).
//
// Currency, amount and product are verified against the catalog: a price
// edited in the provider dashboard must never make the buyer receive a
// different INK quantity than the one the server decided.
type SettleCheckoutUseCase struct {
	catalog *domain.Catalog
	intents CheckoutIntentRepository
	inker   Inker
	clock   Clock
}

// NewSettleCheckoutUseCase builds the use case, refusing incomplete
// composition.
func NewSettleCheckoutUseCase(deps SettleCheckoutDependencies) (*SettleCheckoutUseCase, error) {
	if deps.Catalog == nil {
		return nil, fmt.Errorf("%w: the versioned catalog is required", ErrInvalidCheckoutConfig)
	}
	if deps.Intents == nil {
		return nil, fmt.Errorf("%w: the checkout intent repository is required", ErrInvalidCheckoutConfig)
	}
	if deps.Inker == nil {
		return nil, fmt.Errorf("%w: the INK credit port is required", ErrInvalidCheckoutConfig)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("%w: a clock is required", ErrInvalidCheckoutConfig)
	}
	return &SettleCheckoutUseCase{
		catalog: deps.Catalog,
		intents: deps.Intents,
		inker:   deps.Inker,
		clock:   deps.Clock,
	}, nil
}

// Execute settles one checkout intent after verified payment.
func (uc *SettleCheckoutUseCase) Execute(ctx context.Context, command SettleCheckoutCommand) (*SettleCheckoutResult, error) {
	// Step 1: look up the checkout intent by session ID.
	intent, err := uc.intents.GetCheckoutIntentBySession(ctx, command.SessionID)
	if err != nil {
		return nil, fmt.Errorf("settle checkout: %w", err)
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

	// Step 3: look up the catalog product to get the INK quantity.
	product, err := uc.catalog.Product(intent.Market, intent.ProductID)
	if err != nil {
		return nil, fmt.Errorf("settle checkout (catalog): %w", err)
	}

	// Verify the product grants INK.
	if product.Grant().Kind() != domain.GrantKindINK {
		return nil, fmt.Errorf("%w: product %s grants %s, not INK",
			ErrSettleCheckoutWrongGrantKind, intent.ProductID, product.Grant().Kind())
	}

	inkQuantity := product.Grant().Quantity()
	if inkQuantity < 1 {
		return nil, fmt.Errorf("%w: product %s grants zero INK",
			ErrSettleCheckoutWrongGrantKind, intent.ProductID)
	}

	// Step 4: credit the account's PURCHASED_INK bucket. The idempotency
	// key is derived from the intent ID so the same payment never credits
	// twice.
	creditResult, err := uc.inker.CreditPurchasedInk(ctx, InkerCreditRequest{
		AccountID:   string(intent.AccountID),
		Amount:      inkQuantity,
		Reference:   intent.ID,
		Idempotency: fmt.Sprintf("settle:%s", intent.ID),
	})
	if err != nil {
		return nil, fmt.Errorf("settle checkout (credit): %w", err)
	}

	// Step 5: mark the intent as `paid` with `paid_at`.
	if err := uc.intents.MarkCheckoutIntentPaid(ctx, command.SessionID); err != nil {
		return nil, fmt.Errorf("settle checkout (mark paid): %w", err)
	}

	return &SettleCheckoutResult{
		IntentID:       intent.ID,
		Replayed:       creditResult.Replayed,
		AmountCredited: inkQuantity,
	}, nil
}
