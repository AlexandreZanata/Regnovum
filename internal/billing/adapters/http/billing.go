// Package http extension (P12-T10): the private billing API.
//
// Three authenticated routes, all private and no-store (THR-CACHE-01):
//   - POST /api/v1/me/billing/checkout creates one server-authoritative
//     checkout and returns the redirect URL with the priced amount;
//   - GET /api/v1/me/billing/subscription returns the Member projection
//     without provider identifiers;
//   - POST /api/v1/me/billing/portal opens the hosted customer portal.
//
// Provider identifiers (customer, session, subscription, price, payment
// intent) never serialize and never enter logs: responses carry only the
// local intent identifier, priced money in minor units with ISO currency,
// lifecycle values and the redirect/portal URLs.
package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// checkoutRequest is the minimal purchase intent the browser may name. Amount,
// currency, price and return URLs are deliberately absent: the server
// resolves them from the versioned catalog and allowlisted configuration.
type checkoutRequest struct {
	Market         string `json:"market"`
	Product        string `json:"product"`
	IdempotencyKey string `json:"idempotency_key"`
}

// checkoutResponse is the private checkout document: the local intent, the
// priced amount and the redirect URL. No provider identifier serializes.
type checkoutResponse struct {
	IntentID    string `json:"intent_id"`
	Status      string `json:"status"`
	RedirectURL string `json:"redirect_url"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Market      string `json:"market"`
	Product     string `json:"product"`
	Replayed    bool   `json:"replayed"`
}

// subscriptionResponse is the private Member projection. Absence is explicit:
// has_subscription false carries no lifecycle. No provider identifier
// serializes.
type subscriptionResponse struct {
	HasSubscription   bool    `json:"has_subscription"`
	Status            *string `json:"status,omitempty"`
	Product           *string `json:"product,omitempty"`
	Market            *string `json:"market,omitempty"`
	CurrentPeriodEnd  *string `json:"current_period_end,omitempty"`
	CancelAtPeriodEnd bool    `json:"cancel_at_period_end,omitempty"`
}

// portalRequest optionally carries the caller's operation token for
// idempotent portal openings.
type portalRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

// portalResponse carries only the hosted portal URL.
type portalResponse struct {
	PortalURL string `json:"portal_url"`
}

// BillingHandlerConfig aggregates the private billing use cases.
type BillingHandlerConfig struct {
	CreateCheckout        *application.CreateCheckoutUseCase
	GetSubscriptionStatus *application.GetSubscriptionStatusUseCase
	GetBillingPortal      *application.GetBillingPortalUseCase
	SecurityManager       *security.Manager
	RateLimit             ratelimit.Protector
}

// BillingHandler serves the private billing API.
type BillingHandler struct {
	checkout     *application.CreateCheckoutUseCase
	subscription *application.GetSubscriptionStatusUseCase
	portal       *application.GetBillingPortalUseCase
	security     *security.Manager
	rateLimit    ratelimit.Protector
}

// NewBillingHandler constructs the handler.
func NewBillingHandler(cfg BillingHandlerConfig) *BillingHandler {
	return &BillingHandler{
		checkout:     cfg.CreateCheckout,
		subscription: cfg.GetSubscriptionStatus,
		portal:       cfg.GetBillingPortal,
		security:     cfg.SecurityManager,
		rateLimit:    cfg.RateLimit,
	}
}

// CreateCheckout handles POST /api/v1/me/billing/checkout.
func (h *BillingHandler) CreateCheckout(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.checkout == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "checkout unavailable"))
		return
	}

	var body checkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_body", "request body is invalid"))
		return
	}

	result, err := h.checkout.Execute(r.Context(), application.CreateCheckoutCommand{
		AccountID:      identity.AccountID,
		Market:         body.Market,
		Product:        body.Product,
		IdempotencyKey: body.IdempotencyKey,
	})
	if err != nil {
		writeBillingProblem(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, checkoutResponse{
		IntentID:    result.IntentID,
		Status:      result.Status.String(),
		RedirectURL: result.RedirectURL,
		AmountMinor: result.Amount.MinorUnits(),
		Currency:    result.Amount.Currency().String(),
		Market:      result.Market.String(),
		Product:     result.ProductID.String(),
		Replayed:    result.Replayed,
	})
}

// GetSubscription handles GET /api/v1/me/billing/subscription.
func (h *BillingHandler) GetSubscription(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.subscription == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "subscription query unavailable"))
		return
	}

	status, err := h.subscription.Execute(r.Context(), domain.AccountID(identity.AccountID))
	if err != nil {
		writeBillingProblem(w, r, err)
		return
	}
	if !status.HasSubscription {
		writeJSON(w, http.StatusOK, subscriptionResponse{HasSubscription: false})
		return
	}

	statusValue := status.Status.String()
	productValue := status.Product.String()
	marketValue := status.Market.String()
	var periodEnd *string
	if status.CurrentPeriodEnd != nil {
		formatted := status.CurrentPeriodEnd.UTC().Format("2006-01-02T15:04:05Z07:00")
		periodEnd = &formatted
	}
	writeJSON(w, http.StatusOK, subscriptionResponse{
		HasSubscription:   true,
		Status:            &statusValue,
		Product:           &productValue,
		Market:            &marketValue,
		CurrentPeriodEnd:  periodEnd,
		CancelAtPeriodEnd: status.CancelAtPeriodEnd,
	})
}

// CreatePortal handles POST /api/v1/me/billing/portal.
func (h *BillingHandler) CreatePortal(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.portal == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "portal unavailable"))
		return
	}

	var body portalRequest
	_ = json.NewDecoder(r.Body).Decode(&body)

	result, err := h.portal.Execute(r.Context(), domain.AccountID(identity.AccountID), body.IdempotencyKey)
	if err != nil {
		writeBillingProblem(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, portalResponse{PortalURL: result.PortalURL})
}

// writeBillingProblem maps billing use-case errors to RFC 9457 Problem
// Details with stable codes. Provider identifiers are never reflected.
func writeBillingProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrEmptyAccountID):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
	case errors.Is(err, application.ErrPurchaserNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
	case errors.Is(err, application.ErrPurchaserNotEligible):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "not_eligible", "account is not eligible to purchase"))
	case errors.Is(err, application.ErrPortalCustomerNotFound):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "no_billing_customer", "no billing customer"))
	case errors.Is(err, domain.ErrInvalidMarket),
		errors.Is(err, domain.ErrInvalidProductID),
		errors.Is(err, domain.ErrUnknownProduct),
		errors.Is(err, domain.ErrUnknownMarket),
		errors.Is(err, domain.ErrMarketNotEnabled),
		errors.Is(err, domain.ErrEmptyIdempotencyKey),
		errors.Is(err, domain.ErrInvalidIdempotencyKey),
		errors.Is(err, domain.ErrIdempotencyKeyTooLong):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_checkout", "checkout request is invalid"))
	default:
		_ = httperror.WriteProblem(w, r, err)
	}
}

// RegisterBillingRoutes wires the private billing endpoints into the mux.
func (h *BillingHandler) RegisterBillingRoutes(mux *http.ServeMux) {
	// Both writes call Stripe, so both carry a policy; the read does not, and
	// the store's own idempotency is what makes a repeated checkout safe to
	// serve rather than what makes it free to request.
	mux.Handle("POST /api/v1/me/billing/checkout", withPrivateNoStore(h.privateBillingRoute(h.protect(ratelimit.ActionCheckoutCreate, http.HandlerFunc(h.CreateCheckout)))))
	mux.Handle("GET /api/v1/me/billing/subscription", withPrivateNoStore(h.privateBillingRoute(http.HandlerFunc(h.GetSubscription))))
	mux.Handle("POST /api/v1/me/billing/portal", withPrivateNoStore(h.privateBillingRoute(h.protect(ratelimit.ActionBillingPortal, http.HandlerFunc(h.CreatePortal)))))
}

func (h *BillingHandler) privateBillingRoute(next http.Handler) http.Handler {
	if h.security == nil {
		return next
	}
	return h.security.RequireAuthMiddleware()(next)
}

// protect applies the rate limit policy of one action, inside the
// authentication middleware so the account dimension is available.
func (h *BillingHandler) protect(action ratelimit.Action, next http.Handler) http.Handler {
	if h.rateLimit == nil {
		return next
	}
	return h.rateLimit.Protect(action, next)
}
