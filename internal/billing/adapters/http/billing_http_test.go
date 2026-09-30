package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

func setupBillingHarness(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)
	repo := postgres.NewRepository(pool)

	owner := mustAccount(t, ctx, q, "billing-http-owner@arena.example.com")
	other := mustAccount(t, ctx, q, "billing-http-other@arena.example.com")
	ownerID := uuidString(owner.ID)
	otherID := uuidString(other.ID)

	verifiedOwner, err := q.SetEmailVerified(ctx, owner.ID)
	if err != nil {
		t.Fatalf("verify owner: %v", err)
	}
	_ = verifiedOwner
	verifiedOther, err := q.SetEmailVerified(ctx, other.ID)
	if err != nil {
		t.Fatalf("verify other: %v", err)
	}
	_ = verifiedOther

	catalog := testBillingCatalog(t)
	gateway := &stubBillingGateway{}
	checkoutUC, err := application.NewCreateCheckoutUseCase(application.CheckoutDependencies{
		Catalog:    catalog,
		Gateway:    gateway,
		Purchasers: repo,
		Customers:  repo,
		Intents:    repo,
		Clock:      clockseed.NewClock(),
		Returns: application.CheckoutReturnURLs{
			Success: "https://arena.example/checkout/success",
			Cancel:  "https://arena.example/checkout/cancel",
		},
	})
	if err != nil {
		t.Fatalf("NewCreateCheckoutUseCase: %v", err)
	}
	subscriptionUC := application.NewGetSubscriptionStatusUseCase(repo)
	portalUC, err := application.NewGetBillingPortalUseCase(application.PortalDependencies{
		Customers: repo,
		Gateway:   gateway,
		ReturnURL: "https://arena.example/billing/return",
	})
	if err != nil {
		t.Fatalf("NewGetBillingPortalUseCase: %v", err)
	}

	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clockseed.NewClock(),
		Random:         clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("security manager: %v", err)
	}

	handler := adapterhttp.NewBillingHandler(adapterhttp.BillingHandlerConfig{
		CreateCheckout:        checkoutUC,
		GetSubscriptionStatus: subscriptionUC,
		GetBillingPortal:      portalUC,
		SecurityManager:       secMgr,
	})
	mux := http.NewServeMux()
	handler.RegisterBillingRoutes(mux)

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case ownerSessionToken:
			return security.AuthIdentity{AccountID: ownerID, SessionID: "session-owner"}, nil
		case otherSessionToken:
			return security.AuthIdentity{AccountID: otherID, SessionID: "session-other"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})
	return secMgr.AuthenticateMiddleware(validator)(mux), ownerID, otherID
}

func testBillingCatalog(t *testing.T) *domain.Catalog {
	t.Helper()
	grant, err := domain.NewINKGrant(10000)
	if err != nil {
		t.Fatalf("NewINKGrant: %v", err)
	}
	productID, _ := domain.ParseProductID("ink_10000")
	amount, _ := domain.NewMoney(990, domain.CurrencyBRL)
	price, _ := domain.ParseStripePriceID("price_1QbrInk")
	product, err := domain.NewProduct(domain.MarketBrazil, productID, amount, grant, price)
	if err != nil {
		t.Fatalf("NewProduct: %v", err)
	}
	catalog, err := domain.NewCatalog(1, []domain.Product{product})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return catalog
}

type stubBillingGateway struct {
	portalURL string
}

func (g *stubBillingGateway) CreateCustomer(_ context.Context, _ application.CreateCustomerRequest) (application.Customer, error) {
	customerID, _ := domain.ParseStripeCustomerID("cus_stubhttp1")
	return application.Customer{ID: customerID}, nil
}

func (g *stubBillingGateway) CreateCheckoutSession(_ context.Context, request application.CreateCheckoutSessionRequest) (application.CheckoutSession, error) {
	sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_stubhttp1", false)
	amount, _ := domain.ParseCurrency("BRL")
	_ = amount
	return application.CheckoutSession{
		ID:            sessionID,
		Status:        domain.CheckoutStatusOpen,
		PaymentStatus: domain.CheckoutPaymentUnpaid,
		Mode:          domain.CheckoutModePayment,
		AmountMinor:   990,
		Currency:      domain.CurrencyBRL,
		URL:           "https://checkout.example/session-1",
	}, nil
}

func (g *stubBillingGateway) GetCheckoutSession(_ context.Context, id domain.StripeCheckoutSessionID) (application.CheckoutSession, error) {
	return application.CheckoutSession{}, application.ErrPaymentGatewayRejected
}

func (g *stubBillingGateway) GetSubscription(_ context.Context, id domain.StripeSubscriptionID) (application.Subscription, error) {
	return application.Subscription{}, application.ErrPaymentGatewayRejected
}

func (g *stubBillingGateway) CreatePortalSession(_ context.Context, _ application.CreatePortalSessionRequest) (application.PortalSession, error) {
	if g.portalURL != "" {
		return application.PortalSession{URL: g.portalURL}, nil
	}
	return application.PortalSession{URL: "https://billing.example/portal/session-1"}, nil
}

func TestBillingPrivateAPIRequiresAuthentication(t *testing.T) {
	mux, _, _ := setupBillingHarness(t)

	targets := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/me/billing/checkout"},
		{http.MethodGet, "/api/v1/me/billing/subscription"},
		{http.MethodPost, "/api/v1/me/billing/portal"},
	}
	for _, target := range targets {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(target.method, target.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", target.method, target.path, recorder.Code)
		}
		assertPrivateCacheHeaders(t, recorder)
	}
}

func TestBillingCheckoutCreatesWithoutStripeIDs(t *testing.T) {
	mux, _, _ := setupBillingHarness(t)

	body := `{"market":"BR","product":"ink_10000","idempotency_key":"operation-1"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/me/billing/checkout", strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: "arena_session", Value: ownerSessionToken})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertPrivateCacheHeaders(t, recorder)

	document := decodeObject(t, recorder.Body.Bytes())
	assertExactKeys(t, document, "intent_id", "status", "redirect_url", "amount_minor", "currency", "market", "product", "replayed")
	serialized, _ := json.Marshal(document)
	for _, marker := range []string{"cus_", "cs_", "sub_", "pi_", "price_"} {
		if strings.Contains(string(serialized), marker) {
			t.Fatalf("checkout response leaks provider identifier %q: %s", marker, serialized)
		}
	}
	if document["amount_minor"] != float64(990) || document["currency"] != "BRL" {
		t.Fatalf("priced amount = %v %v, want 990 BRL", document["amount_minor"], document["currency"])
	}
}

func TestBillingSubscriptionProjectsWithoutStripeIDs(t *testing.T) {
	mux, _, _ := setupBillingHarness(t)

	recorder := httptest.NewRecorder()
	request := authenticatedRequest(http.MethodGet, "/api/v1/me/billing/subscription", ownerSessionToken)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	assertPrivateCacheHeaders(t, recorder)

	document := decodeObject(t, recorder.Body.Bytes())
	if _, ok := document["has_subscription"]; !ok {
		t.Fatalf("subscription response keys = %v, want has_subscription", document)
	}
	serialized, _ := json.Marshal(document)
	for _, marker := range []string{"cus_", "cs_", "sub_", "pi_", "price_", "stripe"} {
		if strings.Contains(strings.ToLower(string(serialized)), marker) {
			t.Fatalf("subscription response leaks provider data %q: %s", marker, serialized)
		}
	}
	_ = time.Now
}
