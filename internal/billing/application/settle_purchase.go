package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// SettlePurchaseCommand carries one inbound provider event delivery:
// the raw body with the provider signature and timestamp headers.
// The body names intent key, account, amount, currency, status and
// event identifier; every commercial fact is conferred inside the
// repository against the sealed intent, never from these bytes alone.
type SettlePurchaseCommand struct {
	Payload         []byte
	SignatureHeader string
	TimestampHeader string
}

// SettlePurchaseRequest is the validated delivery for the repository
// port.
type SettlePurchaseRequest struct {
	Payload         []byte
	SignatureHeader string
	TimestampHeader string
}

// SettlePurchaseResult is the settled delivery: the intent, the
// settlement record, the INK the buyer received and whether the call
// replayed an earlier delivery. Season names the purchase book the
// buyer was paid in.
type SettlePurchaseResult struct {
	IntentID     string
	SettlementID string
	InkMilli     int64
	Replayed     bool
	Season       domain.SeasonKey
}

// PurchaseSettlementRepository settles paid provider events with the
// committed INK in one transaction: the hold pays the buyer, the
// settlement row names the event beside the intent, or nothing moves
// at all.
type PurchaseSettlementRepository interface {
	// SettlePurchase settles one paid event, keyed idempotently by
	// the provider event and bounded by one liquidation per intent.
	// Redeliveries and out-of-order events resolve without effect;
	// non-paying, unknown, divergent and stale deliveries refuse.
	SettlePurchase(ctx context.Context, request SettlePurchaseRequest) (*SettlePurchaseResult, error)
}

// SettlePurchaseUseCase settles one paid provider event without
// minting INK and without touching third parties. It is an internal
// operation: no public surface calls it.
type SettlePurchaseUseCase struct {
	settlements PurchaseSettlementRepository
}

// NewSettlePurchaseUseCase creates an instance of SettlePurchaseUseCase,
// refusing incomplete composition.
func NewSettlePurchaseUseCase(settlements PurchaseSettlementRepository) (*SettlePurchaseUseCase, error) {
	if settlements == nil {
		return nil, ErrInvalidPurchaseIntentConfig
	}
	return &SettlePurchaseUseCase{settlements: settlements}, nil
}

// Execute validates the delivery envelope and settles it. Empty
// bodies and missing headers stop before any store is touched; the
// event authenticates inside the repository, where a forgery must
// never be persisted.
func (uc *SettlePurchaseUseCase) Execute(ctx context.Context, cmd SettlePurchaseCommand) (*SettlePurchaseResult, error) {
	if len(cmd.Payload) == 0 {
		return nil, ErrWebhookPayloadMalformed
	}
	if cmd.SignatureHeader == "" || cmd.TimestampHeader == "" {
		return nil, ErrWebhookSignatureInvalid
	}
	return uc.settlements.SettlePurchase(ctx, SettlePurchaseRequest(cmd))
}
