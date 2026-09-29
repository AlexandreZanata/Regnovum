package application

import (
	"context"
)

// SettleChargebackCommand carries one inbound provider dispute
// delivery: the raw body with the provider signature and timestamp
// headers. The body names intent key, account, disputed fiat amount,
// currency, status and event identifier; every compensating fact is
// conferred inside the repository against the sealed settlement,
// never from these bytes alone.
type SettleChargebackCommand struct {
	Payload         []byte
	SignatureHeader string
	TimestampHeader string
}

// SettleChargebackRequest is the validated delivery for the
// repository port.
type SettleChargebackRequest struct {
	Payload         []byte
	SignatureHeader string
	TimestampHeader string
}

// SettleChargebackResult is the settled dispute: the chargeback
// record, the INK revoked from the holder, the INK the operator
// covered and whether the call replayed an earlier delivery.
type SettleChargebackResult struct {
	ChargebackID  string
	InkRevoked    int64
	TreasuryCover int64
	Replayed      bool
}

// ChargebackRepository reverses paid provider events with linked
// compensation in one transaction: the holder answers up to their
// balance, the operator covers the remainder from commercial stock,
// and the chargeback row names the dispute beside the settlement, or
// nothing moves at all.
type ChargebackRepository interface {
	// SettleChargeback reverses one disputed liquidation, keyed
	// idempotently by the provider event and bounded by one dispute
	// per liquidation. Redeliveries resolve without effect; unknown,
	// divergent, stale and uncovered disputes refuse.
	SettleChargeback(ctx context.Context, request SettleChargebackRequest) (*SettleChargebackResult, error)
}

// SettleChargebackUseCase reverses one disputed liquidation without
// minting INK, without implying a negative balance and without
// debiting good-faith third parties. It is an internal operation: no
// public surface calls it.
type SettleChargebackUseCase struct {
	chargebacks ChargebackRepository
}

// NewSettleChargebackUseCase creates an instance of
// SettleChargebackUseCase, refusing incomplete composition.
func NewSettleChargebackUseCase(chargebacks ChargebackRepository) (*SettleChargebackUseCase, error) {
	if chargebacks == nil {
		return nil, ErrInvalidPurchaseIntentConfig
	}
	return &SettleChargebackUseCase{chargebacks: chargebacks}, nil
}

// Execute validates the delivery envelope and settles it. Empty
// bodies and missing headers stop before any store is touched; the
// dispute authenticates inside the repository, where a forgery must
// never be persisted.
func (uc *SettleChargebackUseCase) Execute(ctx context.Context, cmd SettleChargebackCommand) (*SettleChargebackResult, error) {
	if len(cmd.Payload) == 0 {
		return nil, ErrWebhookPayloadMalformed
	}
	if cmd.SignatureHeader == "" || cmd.TimestampHeader == "" {
		return nil, ErrWebhookSignatureInvalid
	}
	return uc.chargebacks.SettleChargeback(ctx, SettleChargebackRequest(cmd))
}
