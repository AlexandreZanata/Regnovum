package application

import (
	"context"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

// ServiceRefundResult is one proportional service refund: the
// stored row with the total returned to the buyer, the tithe
// reversal, the provider share, the explicit shortfall obligation
// and whether the call replayed the original compensation.
type ServiceRefundResult struct {
	RefundID       string
	TransferID     string
	AmountMill     int64
	TitheReversal  int64
	ProviderShare  int64
	ObligationMill int64
	Replayed       bool
}

// ServiceRefundRequest is the validated refund for the repository
// port: the liquidated contract key scoped by buyer, the caller
// refund token and the exact total returned to the buyer. The
// provider, the split and the obligation resolve server-side, so a
// forged request can neither invent a share nor hide a shortfall.
type ServiceRefundRequest struct {
	ContractKey string
	Buyer       string
	RefundKey   string
	AmountMill  int64
}

// ServiceRefundRepository compensates liquidated formal payments
// with their ledger legs in one transaction: the refund row, the
// optional obligation row and the legs commit together, or nothing
// is stored at all.
type ServiceRefundRepository interface {
	// RefundService compensates one liquidated contract in whole
	// or in part, keyed idempotently by contract and token.
	// Replays resolve the original compensation untouched,
	// divergent terms under one key conflict, and refunds that
	// would exceed the unrefunded remainder refuse without
	// writing.
	RefundService(ctx context.Context, request ServiceRefundRequest) (*ServiceRefundResult, error)
}

// RefundServiceUseCase compensates one liquidated formal trade in
// whole or in part. It is an internal operation: no public surface
// calls it before activation.
type RefundServiceUseCase struct {
	refunds ServiceRefundRepository
}

// NewRefundServiceUseCase creates an instance of
// RefundServiceUseCase, refusing incomplete composition.
func NewRefundServiceUseCase(refunds ServiceRefundRepository) (*RefundServiceUseCase, error) {
	if refunds == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &RefundServiceUseCase{refunds: refunds}, nil
}

// ServiceRefundCommand names one service refund: the liquidated
// contract key scoped by buyer, the caller refund token and the
// exact total returned to the buyer.
type ServiceRefundCommand struct {
	ContractKey string
	Buyer       string
	RefundKey   string
	AmountMill  int64
}

// Execute validates the refund envelope and compensates it. Blank
// keys and non-positive amounts stop malformed calls before any
// store is touched; the proportional split and the remainder guard
// resolve inside the repository transaction.
func (uc *RefundServiceUseCase) Execute(ctx context.Context, cmd ServiceRefundCommand) (*ServiceRefundResult, error) {
	if strings.TrimSpace(cmd.ContractKey) == "" || strings.TrimSpace(cmd.ContractKey) != cmd.ContractKey {
		return nil, domain.ErrInvalidContract
	}
	if strings.TrimSpace(cmd.Buyer) == "" || strings.TrimSpace(cmd.Buyer) != cmd.Buyer {
		return nil, domain.ErrInvalidContract
	}
	if strings.TrimSpace(cmd.RefundKey) == "" || strings.TrimSpace(cmd.RefundKey) != cmd.RefundKey {
		return nil, domain.ErrInvalidContract
	}
	if _, _, err := domain.SplitServiceRefund(cmd.AmountMill); err != nil {
		return nil, err
	}
	return uc.refunds.RefundService(ctx, ServiceRefundRequest(cmd))
}
