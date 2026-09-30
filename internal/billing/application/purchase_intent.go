package application

import (
	"context"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// AcceptPurchaseCommand names one INK purchase acceptance: the caller
// operation token, the buying account, the sealed quotation and the
// fiat ticket in minor units. Price, INK quantity, stock and schedule
// are deliberately absent: the server resolves them from the stored
// quotation and its own fee schedule, so the buyer can never propose
// a price, a quantity or a vault.
type AcceptPurchaseCommand struct {
	IntentKey string
	AccountID string
	QuoteID   string
	FiatMinor int64
}

// AcceptPurchaseRequest is the validated acceptance for the
// repository port: the token, the account, the quotation and the BRL
// ticket, all resolved server-side from here on.
type AcceptPurchaseRequest struct {
	Key     domain.IdempotencyKey
	Account domain.AccountID
	QuoteID string
	Fiat    domain.Money
}

// PurchaseIntentResult is the settled acceptance: the stored intent,
// the server-derived amounts, the backing hold and whether the call
// replayed the original acceptance.
type PurchaseIntentResult struct {
	IntentID string
	QuoteID  string
	Fiat     domain.Money
	InkMilli int64
	HoldID   string
	Replayed bool
}

// PurchaseIntentRepository accepts INK purchases with the commercial
// hold in one transaction: the intent row and the stock commitment
// commit together, or nothing is stored at all.
type PurchaseIntentRepository interface {
	// AcceptPurchase seals one acceptance keyed idempotently by
	// account and token. Replays resolve the original acceptance
	// untouched; divergent terms under one key conflict instead of
	// provisioning twice; uncovered stock refuses without writing.
	AcceptPurchase(ctx context.Context, request AcceptPurchaseRequest) (*PurchaseIntentResult, error)
}

// AcceptPurchaseUseCase accepts one INK purchase without moving fiat
// and without minting INK. It is an internal operation: no public
// surface calls it.
type AcceptPurchaseUseCase struct {
	intents PurchaseIntentRepository
}

// NewAcceptPurchaseUseCase creates an instance of AcceptPurchaseUseCase,
// refusing incomplete composition.
func NewAcceptPurchaseUseCase(intents PurchaseIntentRepository) (*AcceptPurchaseUseCase, error) {
	if intents == nil {
		return nil, ErrInvalidPurchaseIntentConfig
	}
	return &AcceptPurchaseUseCase{intents: intents}, nil
}

// Execute validates the acceptance envelope and seals it. The token,
// the account, the quotation reference and a positive BRL ticket stop
// malformed calls before any store is touched; every commercial fact
// resolves inside the repository.
func (uc *AcceptPurchaseUseCase) Execute(ctx context.Context, cmd AcceptPurchaseCommand) (*PurchaseIntentResult, error) {
	key, err := domain.ParseIdempotencyKey(cmd.IntentKey)
	if err != nil {
		return nil, err
	}
	account := domain.AccountID(cmd.AccountID)
	if account.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	if strings.TrimSpace(cmd.QuoteID) == "" {
		return nil, ErrPurchaseQuoteNotFound
	}
	fiat, err := domain.NewMoney(cmd.FiatMinor, domain.CurrencyBRL)
	if err != nil || fiat.IsZero() {
		return nil, domain.ErrInvalidMoney
	}
	return uc.intents.AcceptPurchase(ctx, AcceptPurchaseRequest{
		Key: key, Account: account, QuoteID: strings.TrimSpace(cmd.QuoteID), Fiat: fiat,
	})
}
