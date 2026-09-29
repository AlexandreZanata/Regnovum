package application

import (
	"context"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

// ContractView is the stored escrow contract: the sealed terms with
// the lifecycle status. Funds move only through the adapter
// transaction behind these transitions.
type ContractView struct {
	ID         string
	Key        string
	Object     string
	Buyer      string
	Provider   string
	AmountMill int64
	ExpiresAt  time.Time
	Status     domain.ContractStatus
	Hash       string
	PostedAt   time.Time
}

// EscrowRepository funds trade contracts and settles their escrows:
// every transition commits with the legs it moves, or nothing is
// stored at all.
type EscrowRepository interface {
	// FundContract seals one trade contract locking the buyer
	// amount in exclusive escrow, keyed idempotently by buyer and
	// token. Replays resolve the original contract untouched;
	// divergent terms under one key conflict.
	FundContract(ctx context.Context, request FundRequest) (*ContractView, error)
	// AcceptDelivery records the buyer delivery acceptance on a
	// funded contract.
	AcceptDelivery(ctx context.Context, key, buyer string) (*ContractView, error)
	// ReleaseContract pays an accepted escrow to the provider.
	ReleaseContract(ctx context.Context, key, buyer string) (*ContractView, error)
	// CancelContract refunds a funded contract to the buyer.
	CancelContract(ctx context.Context, key, buyer string) (*ContractView, error)
	// ExpireContract marks a lapsed contract without moving any
	// leg.
	ExpireContract(ctx context.Context, key, buyer string, at time.Time) (*ContractView, error)
	// ResolveContract settles an expired contract by competent
	// decision.
	ResolveContract(ctx context.Context, key, buyer, decider string, decision domain.ResolveDecision) (*ContractView, error)
}

// FundRequest is the validated funding for the repository port.
type FundRequest struct {
	Key        string
	Object     string
	Buyer      string
	Provider   string
	AmountMill int64
	ExpiresAt  time.Time
	Now        time.Time
}

// FundContractUseCase funds one formal trade in exclusive escrow.
// It is an internal operation: no public surface calls it before
// activation.
type FundContractUseCase struct {
	escrows EscrowRepository
}

// NewFundContractUseCase creates an instance of FundContractUseCase,
// refusing incomplete composition.
func NewFundContractUseCase(escrows EscrowRepository) (*FundContractUseCase, error) {
	if escrows == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &FundContractUseCase{escrows: escrows}, nil
}

// Execute validates the funding envelope and locks the amount. The
// object, both parties, a positive amount and a future expiry stop
// malformed calls before any store is touched.
func (uc *FundContractUseCase) Execute(ctx context.Context, cmd FundCommand) (*ContractView, error) {
	if _, err := domain.FundContract(domain.ContractRequest(cmd)); err != nil {
		return nil, err
	}
	request := FundRequest(cmd)
	request.Now = cmd.Now.UTC()
	return uc.escrows.FundContract(ctx, request)
}

// FundCommand names one trade funding: the caller operation token,
// the service object, both parties, the exact amount and the
// expiry. Only formal trade enters escrow.
type FundCommand domain.ContractRequest

// AcceptDeliveryUseCase records one buyer delivery acceptance.
type AcceptDeliveryUseCase struct {
	escrows EscrowRepository
}

// NewAcceptDeliveryUseCase creates an instance of
// AcceptDeliveryUseCase, refusing incomplete composition.
func NewAcceptDeliveryUseCase(escrows EscrowRepository) (*AcceptDeliveryUseCase, error) {
	if escrows == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &AcceptDeliveryUseCase{escrows: escrows}, nil
}

// Execute validates the acceptance envelope and records it.
func (uc *AcceptDeliveryUseCase) Execute(ctx context.Context, key, buyer string) (*ContractView, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(buyer) == "" {
		return nil, domain.ErrInvalidContract
	}
	return uc.escrows.AcceptDelivery(ctx, key, buyer)
}

// ReleaseContractUseCase pays one accepted escrow to the provider.
type ReleaseContractUseCase struct {
	escrows EscrowRepository
}

// NewReleaseContractUseCase creates an instance of
// ReleaseContractUseCase, refusing incomplete composition.
func NewReleaseContractUseCase(escrows EscrowRepository) (*ReleaseContractUseCase, error) {
	if escrows == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &ReleaseContractUseCase{escrows: escrows}, nil
}

// Execute validates the release envelope and pays the provider.
func (uc *ReleaseContractUseCase) Execute(ctx context.Context, key, buyer string) (*ContractView, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(buyer) == "" {
		return nil, domain.ErrInvalidContract
	}
	return uc.escrows.ReleaseContract(ctx, key, buyer)
}

// CancelContractUseCase refunds one funded contract to the buyer.
type CancelContractUseCase struct {
	escrows EscrowRepository
}

// NewCancelContractUseCase creates an instance of
// CancelContractUseCase, refusing incomplete composition.
func NewCancelContractUseCase(escrows EscrowRepository) (*CancelContractUseCase, error) {
	if escrows == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &CancelContractUseCase{escrows: escrows}, nil
}

// Execute validates the cancellation envelope and refunds the buyer.
func (uc *CancelContractUseCase) Execute(ctx context.Context, key, buyer string) (*ContractView, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(buyer) == "" {
		return nil, domain.ErrInvalidContract
	}
	return uc.escrows.CancelContract(ctx, key, buyer)
}

// ExpireContractUseCase marks one lapsed contract without moving
// any leg.
type ExpireContractUseCase struct {
	escrows EscrowRepository
}

// NewExpireContractUseCase creates an instance of
// ExpireContractUseCase, refusing incomplete composition.
func NewExpireContractUseCase(escrows EscrowRepository) (*ExpireContractUseCase, error) {
	if escrows == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &ExpireContractUseCase{escrows: escrows}, nil
}

// Execute validates the expiry envelope and marks the contract.
func (uc *ExpireContractUseCase) Execute(ctx context.Context, key, buyer string, at time.Time) (*ContractView, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(buyer) == "" || at.IsZero() {
		return nil, domain.ErrInvalidContract
	}
	return uc.escrows.ExpireContract(ctx, key, buyer, at)
}

// ResolveContractUseCase settles one expired contract by competent
// decision. Who decides is governance beyond this phase: the
// decider travels opaque and recorded, never authorized here.
type ResolveContractUseCase struct {
	escrows EscrowRepository
}

// NewResolveContractUseCase creates an instance of
// ResolveContractUseCase, refusing incomplete composition.
func NewResolveContractUseCase(escrows EscrowRepository) (*ResolveContractUseCase, error) {
	if escrows == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &ResolveContractUseCase{escrows: escrows}, nil
}

// Execute validates the resolution envelope and settles the
// expired escrow by decision.
func (uc *ResolveContractUseCase) Execute(ctx context.Context, key, buyer, decider, decision string) (*ContractView, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(buyer) == "" || strings.TrimSpace(decider) == "" {
		return nil, domain.ErrInvalidContract
	}
	parsed, err := domain.ParseResolveDecision(decision)
	if err != nil {
		return nil, err
	}
	return uc.escrows.ResolveContract(ctx, key, buyer, strings.TrimSpace(decider), parsed)
}
