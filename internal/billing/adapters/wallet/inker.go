// Package wallet is the wallet outbound adapter of the billing module
// (P12-T06). It implements the billing application port (application.Inker)
// over the wallet application's CreditInkUseCase, and it is the only package
// in the billing module allowed to import the wallet module.
//
// The adapter translates between billing domain types and wallet domain types:
// the billing module uses plain strings for account IDs, references and
// idempotency keys, while the wallet module uses its own typed value objects.
// This translation keeps the billing module independent of the wallet module's
// internal vocabulary.
package wallet

import (
	"context"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Inker adapts the wallet CreditInkUseCase to the billing Inker port.
type Inker struct {
	credits walletapp.CreditRepository
	clock   walletapp.Clock
}

// NewInker builds the adapter. The clock is the wallet module's clock
// interface, which is satisfied by the same platform/clockseed.System that
// the billing module uses.
func NewInker(credits walletapp.CreditRepository, clock walletapp.Clock) *Inker {
	return &Inker{credits: credits, clock: clock}
}

// CreditPurchasedInk grants the specified amount of INK to the account's
// PURCHASED_INK bucket. It translates between billing and wallet domain types.
func (i *Inker) CreditPurchasedInk(ctx context.Context, request application.InkerCreditRequest) (*application.InkerCreditResult, error) {
	// Translate account ID (wallet domain AccountID is just a string type).
	accountID := walletdomain.AccountID(request.AccountID)

	// Parse the INK amount.
	amount, err := walletdomain.NewInk(request.Amount)
	if err != nil {
		return nil, fmt.Errorf("parse INK amount: %w", err)
	}

	// Parse the reference.
	reference, err := walletdomain.ParseReference(request.Reference)
	if err != nil {
		return nil, fmt.Errorf("parse reference: %w", err)
	}

	// Parse the idempotency key.
	idempotencyKey, err := walletdomain.ParseIdempotencyKey(request.Idempotency)
	if err != nil {
		return nil, fmt.Errorf("parse idempotency key: %w", err)
	}

	// Apply the credit through the wallet repository.
	result, err := i.credits.ApplyCredit(ctx, walletapp.CreditRequest{
		AccountID:      accountID,
		Bucket:         walletdomain.BucketPurchased,
		OperationType:  walletdomain.OperationCreditPurchase,
		IdempotencyKey: idempotencyKey,
		Reference:      reference,
		Delta:          amount.Int64(),
		ChangedAt:      i.clock.Now(),
	})
	if err != nil {
		return nil, fmt.Errorf("apply credit: %w", err)
	}

	return &application.InkerCreditResult{
		Replayed: result.Replayed,
	}, nil
}

// CreditMemberInk grants the specified amount of INK to the account's
// FREE_INK bucket under the credit_member operation (P12-T08). It translates
// between billing and wallet domain types.
func (i *Inker) CreditMemberInk(ctx context.Context, request application.InkerCreditRequest) (*application.InkerCreditResult, error) {
	accountID := walletdomain.AccountID(request.AccountID)

	amount, err := walletdomain.NewInk(request.Amount)
	if err != nil {
		return nil, fmt.Errorf("parse INK amount: %w", err)
	}

	reference, err := walletdomain.ParseReference(request.Reference)
	if err != nil {
		return nil, fmt.Errorf("parse reference: %w", err)
	}

	idempotencyKey, err := walletdomain.ParseIdempotencyKey(request.Idempotency)
	if err != nil {
		return nil, fmt.Errorf("parse idempotency key: %w", err)
	}

	result, err := i.credits.ApplyCredit(ctx, walletapp.CreditRequest{
		AccountID:      accountID,
		Bucket:         walletdomain.BucketFree,
		OperationType:  walletdomain.OperationCreditMember,
		IdempotencyKey: idempotencyKey,
		Reference:      reference,
		Delta:          amount.Int64(),
		ChangedAt:      i.clock.Now(),
	})
	if err != nil {
		return nil, fmt.Errorf("apply member credit: %w", err)
	}

	return &application.InkerCreditResult{
		Replayed: result.Replayed,
	}, nil
}
