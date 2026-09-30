package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// PortalDependencies groups everything the portal use case needs.
type PortalDependencies struct {
	Customers StripeCustomerRepository
	Gateway   PaymentGateway
	// ReturnURL is where the provider sends the buyer after the portal. It
	// is allowlisted configuration, never browser input.
	ReturnURL string
}

// GetBillingPortalUseCase opens the hosted customer portal for the owner's
// stored customer. The response carries only the portal URL: customer,
// session and price identifiers never serialize.
type GetBillingPortalUseCase struct {
	customers StripeCustomerRepository
	gateway   PaymentGateway
	returnURL string
}

// NewGetBillingPortalUseCase builds the use case, refusing incomplete or
// incoherent composition.
func NewGetBillingPortalUseCase(deps PortalDependencies) (*GetBillingPortalUseCase, error) {
	if deps.Customers == nil || deps.Gateway == nil {
		return nil, fmt.Errorf("%w: the portal customer and gateway ports are required", ErrInvalidCheckoutConfig)
	}
	if strings.TrimSpace(deps.ReturnURL) == "" {
		return nil, fmt.Errorf("%w: the portal return URL is required", ErrInvalidCheckoutConfig)
	}
	return &GetBillingPortalUseCase{customers: deps.Customers, gateway: deps.Gateway, returnURL: deps.ReturnURL}, nil
}

// PortalResult is the hosted portal the buyer is sent to.
type PortalResult struct {
	PortalURL string
}

// Execute resolves the stored customer and opens one portal session. An
// account without a stored customer has nothing to manage, which is a
// not-found rather than an empty URL.
func (uc *GetBillingPortalUseCase) Execute(ctx context.Context, accountID domain.AccountID, idempotencyKey string) (*PortalResult, error) {
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	key, err := domain.ParseIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	stored, err := uc.customers.StripeCustomer(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, ErrPortalCustomerNotFound
	}
	portalKey := "portal:" + accountID.String() + ":" + key.String()
	if len(portalKey) > maxProviderIdempotencyKeyLength {
		return nil, domain.ErrIdempotencyKeyTooLong
	}
	session, err := uc.gateway.CreatePortalSession(ctx, CreatePortalSessionRequest{
		CustomerID:     stored.CustomerID,
		ReturnURL:      uc.returnURL,
		IdempotencyKey: portalKey,
	})
	if err != nil {
		return nil, err
	}
	return &PortalResult{PortalURL: session.URL}, nil
}
