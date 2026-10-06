// Tests of the guarded billing writes composed in the process (P49-T06):
// the hosted checkout and the customer portal run on the platform mux over
// disposable PostgreSQL, driven by real HTTP with the same pool and security
// boundary as the account journey — the session the JSON login opened is the
// session the writes require, and no second authentication exists.
//
// The provider is a deterministic synthetic gateway injected at composition:
// no network call leaves the test, and flipping it to fail proves an
// unavailable provider grants no credit. Settlement runs through the
// composed provider-only webhook pipeline with a synthetic signing secret,
// so checkout → verified event → projection is proven without ever giving a
// client UI to the webhook ingress.
package bootstrap_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	billingcatalog "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/catalog"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// billingTestGateway is the deterministic synthetic provider the P49-T06
// composition is proven against: every checkout and portal opening resolves
// locally with stable identifiers, and fail, once set, makes every provider
// call fail so the tests prove an unavailable provider grants no credit.
type billingTestGateway struct {
	mu       sync.Mutex
	sequence int
	fail     error
	// lastSession records the latest provisioned session, so the proof can
	// address the verified event to it without reading a provider
	// identifier back from any browser document.
	lastSession billingdomain.StripeCheckoutSessionID
	// prices mirrors the catalog the proof loads: what the synthetic
	// provider charges for each price, so verifyChargedPrice compares equal
	// amounts instead of passing by construction.
	prices map[string]struct {
		amount   int64
		currency billingdomain.Currency
	}
}

// newBillingTestGateway builds the synthetic provider with the catalog
// prices the proof loads, so the charged amounts compare equal.
func newBillingTestGateway() *billingTestGateway {
	return &billingTestGateway{prices: map[string]struct {
		amount   int64
		currency billingdomain.Currency
	}{
		"price_1Qt06pass1": {amount: 990, currency: billingdomain.CurrencyBRL},
		"price_1Qt06ink1":  {amount: 990, currency: billingdomain.CurrencyBRL},
	}}
}

func (g *billingTestGateway) next(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sequence++
	return fmt.Sprintf("%s%d", prefix, g.sequence)
}

func (g *billingTestGateway) CreateCustomer(_ context.Context, _ billingapp.CreateCustomerRequest) (billingapp.Customer, error) {
	if g.fail != nil {
		return billingapp.Customer{}, g.fail
	}
	id, err := billingdomain.ParseStripeCustomerID(g.next("cus_t06customer"))
	if err != nil {
		return billingapp.Customer{}, err
	}
	return billingapp.Customer{ID: id}, nil
}

func (g *billingTestGateway) CreateCheckoutSession(_ context.Context, request billingapp.CreateCheckoutSessionRequest) (billingapp.CheckoutSession, error) {
	if g.fail != nil {
		return billingapp.CheckoutSession{}, g.fail
	}
	charged, ok := g.prices[request.PriceID.String()]
	if !ok {
		return billingapp.CheckoutSession{}, billingdomain.ErrUnknownProduct
	}
	id, err := billingdomain.ParseStripeCheckoutSessionID(g.next("cs_test_t06session"), false)
	if err != nil {
		return billingapp.CheckoutSession{}, err
	}
	g.lastSession = id
	return billingapp.CheckoutSession{
		ID:            id,
		Status:        billingdomain.CheckoutStatusOpen,
		PaymentStatus: billingdomain.CheckoutPaymentUnpaid,
		Mode:          request.Mode,
		AmountMinor:   charged.amount,
		Currency:      charged.currency,
		URL:           "https://checkout.example/t06/hosted-checkout",
	}, nil
}

// lastProvisionedSession returns the latest session the synthetic provider
// provisioned.
func (g *billingTestGateway) lastProvisionedSession() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastSession.String()
}

func (g *billingTestGateway) GetCheckoutSession(_ context.Context, _ billingdomain.StripeCheckoutSessionID) (billingapp.CheckoutSession, error) {
	return billingapp.CheckoutSession{}, billingapp.ErrPaymentGatewayRejected
}

func (g *billingTestGateway) GetSubscription(_ context.Context, _ billingdomain.StripeSubscriptionID) (billingapp.Subscription, error) {
	return billingapp.Subscription{}, billingapp.ErrPaymentGatewayRejected
}

func (g *billingTestGateway) CreatePortalSession(_ context.Context, _ billingapp.CreatePortalSessionRequest) (billingapp.PortalSession, error) {
	if g.fail != nil {
		return billingapp.PortalSession{}, g.fail
	}
	return billingapp.PortalSession{URL: "https://billing.example/portal/t06"}, nil
}

// billingJourney is the account plus privacy, entitlement-read and billing
// surfaces on a real listener, sharing one pool and one security boundary
// like `arena server` does.
type billingJourney struct {
	account       *bootstrap.AccountSurface
	billing       *bootstrap.BillingSurface
	gateway       *billingTestGateway
	server        *httptest.Server
	pool          *pgxpool.Pool
	webhookSecret string
}

// testBillingCatalog loads the versioned catalog with the BR products the
// composition proof buys: one pass and one INK pack, priced in BRL.
func testBillingCatalog(t *testing.T) *billingdomain.Catalog {
	t.Helper()

	catalog, err := billingcatalog.Load(billingcatalog.Spec{
		Markets: []billingcatalog.EnabledMarket{{Market: "BR", Currency: "BRL"}},
		Prices: []billingcatalog.ConfiguredPrice{
			{Market: "BR", Product: "pass_1", PriceID: "price_1Qt06pass1"},
			{Market: "BR", Product: "ink_10000", PriceID: "price_1Qt06ink1"},
		},
	})
	if err != nil {
		t.Fatalf("catalog.Load() error = %v", err)
	}
	return catalog
}

// newBillingJourney composes the surfaces the way the process does, with the
// synthetic gateway and secret the T06 proof injects.
// billingWritesTestConfig is the synthetic payment composition the reach
// map uses: the same guarded surface the process composes over Stripe.
func billingWritesTestConfig(t *testing.T) bootstrap.BillingConfig {
	t.Helper()

	return bootstrap.BillingConfig{
		Gateway:         newBillingTestGateway(),
		Catalog:         testBillingCatalog(t),
		SuccessURL:      "https://arena.example/billing/success",
		CancelURL:       "https://arena.example/billing/cancel",
		PortalReturnURL: "https://arena.example/billing/success",
		WebhookSecret:   "whsec_t06_synthetic_secret",
	}
}

func newBillingJourney(t *testing.T, gateway *billingTestGateway) *billingJourney {
	t.Helper()

	database := dbtest.New(t)
	pool := database.Pool.Pool()
	clock := clockseed.NewClock()
	random := clockseed.NewRandom()
	manager, err := security.New(security.Options{Env: config.EnvTest, Clock: clock, Random: random})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}
	base := func() bootstrap.Options {
		return bootstrap.Options{
			Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
			Pool: pool, Clock: clock, Random: random, Assets: manifestFixture(t), Security: manager,
			CursorSecret: []byte(cursorSecret),
		}
	}
	account, err := bootstrap.ComposeAccount(base())
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}
	privacy, err := bootstrap.ComposeAccountPrivacy(base())
	if err != nil {
		t.Fatalf("ComposeAccountPrivacy() error = %v", err)
	}
	entitlements, err := bootstrap.ComposeEntitlementReads(base())
	if err != nil {
		t.Fatalf("ComposeEntitlementReads() error = %v", err)
	}
	secret := "whsec_t06_synthetic_secret"
	composed := billingWritesTestConfig(t)
	composed.Gateway = gateway
	billing, err := bootstrap.ComposeBillingWrites(base(), composed)
	if err != nil {
		t.Fatalf("ComposeBillingWrites() error = %v", err)
	}
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{},
		[]httpserver.Surface{account.Surface(), privacy.Surface(), entitlements.Surface(), billing.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &billingJourney{account: account, billing: billing, gateway: gateway, server: server, pool: pool, webhookSecret: secret}
}

// billingLogin registers, verifies and signs in one account over the JSON
// API, returning the client holding its session.
func billingLogin(t *testing.T, journey *billingJourney, email, password string) *http.Client {
	t.Helper()

	client := browser(t)
	status, _, _ := jsonPost(t, client, journey.server, "/api/v1/auth/register", `{"email":`+quoteJSON(email)+`,"password":`+quoteJSON(password)+`}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /api/v1/auth/register status = %d, want 201", status)
	}
	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	token, found := journey.account.LocalSink().LastTokenForEmail(address)
	if !found {
		t.Fatalf("registration of %s issued no token", email)
	}
	if status, _ := jsonGet(t, client, journey.server, "/api/v1/auth/verify?token="+token); status != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/verify status = %d, want 200", status)
	}
	status, raw, _ := jsonPost(t, client, journey.server, "/api/v1/auth/login", `{"email":`+quoteJSON(email)+`,"password":`+quoteJSON(password)+`}`)
	if status != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/login status = %d, want 200 (body: %.200s)", status, raw)
	}
	return client
}

// billingPost issues one authenticated JSON POST and returns status, body
// and headers.
func billingPost(t *testing.T, client *http.Client, server *httptest.Server, path, body string) (int, []byte, http.Header) {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read POST %s: %v", path, err)
	}
	return response.StatusCode, raw, response.Header
}

// billingPassTotal reads the owner's pass summary over HTTP.
func billingPassTotal(t *testing.T, client *http.Client, journey *billingJourney) int64 {
	t.Helper()

	status, raw, _ := entitlementGet(t, client, journey.server, "/api/v1/me/passes")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/passes status = %d, want 200 (body: %.300s)", status, raw)
	}
	var summary struct {
		AvailableTotal int64 `json:"available_total"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("summary is not JSON: %v", err)
	}
	return summary.AvailableTotal
}

// billingPurchasedBalance reads the owner's purchased INK bucket over HTTP.
func billingPurchasedBalance(t *testing.T, client *http.Client, journey *billingJourney) int64 {
	t.Helper()

	status, raw, _ := entitlementGet(t, client, journey.server, "/api/v1/me/wallet")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/me/wallet status = %d, want 200 (body: %.300s)", status, raw)
	}
	var balance struct {
		Purchased int64 `json:"balance_purchased"`
	}
	if err := json.Unmarshal(raw, &balance); err != nil {
		t.Fatalf("wallet is not JSON: %v", err)
	}
	return balance.Purchased
}

// signBillingEvent signs one webhook body with the synthetic secret, the way
// the provider signs its server-to-server deliveries.
func signBillingEvent(secret string, body []byte) (signature, timestamp string) {
	stamp := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.%s", stamp, body)))
	return fmt.Sprintf("t=%d,v1=%s", stamp, hex.EncodeToString(mac.Sum(nil))), fmt.Sprintf("%d", stamp)
}

// completedSessionEvent builds a checkout.session.completed body for one
// provider session.
func completedSessionEvent(eventID, sessionID string) []byte {
	return []byte(fmt.Sprintf(`{
		"id": %q,
		"type": "checkout.session.completed",
		"livemode": false,
		"created": %d,
		"data": {"object": {"id": %q, "status": "complete", "payment_status": "paid"}}
	}`, eventID, time.Now().Unix(), sessionID))
}

// deliverBillingEvent runs one provider delivery through the composed
// provider-only pipeline.
func deliverBillingEvent(t *testing.T, journey *billingJourney, body []byte, signature, timestamp string) error {
	t.Helper()

	return journey.billing.WebhookPipeline().Execute(context.Background(), billingapp.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: signature,
		TimestampHeader: timestamp,
	})
}

// checkoutPass buys one pass over HTTP and returns the provider session the
// synthetic gateway provisioned. No provider identifier may leak into the
// private checkout document.
func checkoutPass(t *testing.T, client *http.Client, journey *billingJourney, key string) string {
	t.Helper()

	status, raw, header := billingPost(t, client, journey.server, "/api/v1/me/billing/checkout",
		`{"market":"BR","product":"pass_1","idempotency_key":`+quoteJSON(key)+`}`)
	if status != http.StatusOK {
		t.Fatalf("POST checkout status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertEntitlementPrivate(t, header)
	var document struct {
		IntentID    string `json:"intent_id"`
		RedirectURL string `json:"redirect_url"`
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
		Replayed    bool   `json:"replayed"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("checkout is not JSON: %v", err)
	}
	if document.AmountMinor != 990 || document.Currency != "BRL" || document.Replayed {
		t.Fatalf("checkout document = %.300s, want a fresh 990 BRL intent", raw)
	}
	for _, leaked := range []string{"cus_", "cs_test_", "sk_", "price_"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("checkout body leaks a provider identifier %q: %.300s", leaked, raw)
		}
	}
	return document.RedirectURL
}

// TestBillingCheckoutSettlesPassOnVerifiedEvent proves the guarded write
// path end to end: checkout over HTTP, then the signed provider event
// through the composed pipeline, then the pass projection. Visiting the
// browser return in between grants nothing.
func TestBillingCheckoutSettlesPassOnVerifiedEvent(t *testing.T) {
	t.Parallel()

	journey := newBillingJourney(t, newBillingTestGateway())
	client := billingLogin(t, journey, "billing-pass-t06@arena.example.com", "Correct Horse 11!")
	before := billingPassTotal(t, client, journey)

	redirect := checkoutPass(t, client, journey, "t06-pass-settle-1")
	if redirect != "https://checkout.example/t06/hosted-checkout" {
		t.Fatalf("redirect = %q, want the synthetic hosted checkout", redirect)
	}
	if billingPassTotal(t, client, journey) != before {
		t.Fatal("the browser return granted a pass: only a verified webhook event may settle an intent")
	}

	body := completedSessionEvent("evt_t06pass1", journey.gateway.lastProvisionedSession())
	signature, timestamp := signBillingEvent(journey.webhookSecret, body)
	if err := deliverBillingEvent(t, journey, body, signature, timestamp); err != nil {
		t.Fatalf("verified event: %v", err)
	}
	if got := billingPassTotal(t, client, journey); got != before+1 {
		t.Fatalf("available_total = %d, want %d after the verified settlement", got, before+1)
	}
}

// TestBillingCheckoutSettlesInkOnVerifiedEvent proves the INK grant path
// settles into the legacy wallet through the same verified pipeline.
func TestBillingCheckoutSettlesInkOnVerifiedEvent(t *testing.T) {
	t.Parallel()

	journey := newBillingJourney(t, newBillingTestGateway())
	client := billingLogin(t, journey, "billing-ink-t06@arena.example.com", "Correct Horse 11!")
	if got := billingPurchasedBalance(t, client, journey); got != 0 {
		t.Fatalf("purchased balance = %d, want 0 before any settlement", got)
	}

	status, raw, _ := billingPost(t, client, journey.server, "/api/v1/me/billing/checkout",
		`{"market":"BR","product":"ink_10000","idempotency_key":"t06-ink-settle-1"}`)
	if status != http.StatusOK {
		t.Fatalf("POST checkout status = %d, want 200 (body: %.300s)", status, raw)
	}
	var document struct {
		RedirectURL string `json:"redirect_url"`
		AmountMinor int64  `json:"amount_minor"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("checkout is not JSON: %v", err)
	}
	if document.AmountMinor != 990 {
		t.Fatalf("amount_minor = %d, want 990", document.AmountMinor)
	}
	if document.RedirectURL != "https://checkout.example/t06/hosted-checkout" {
		t.Fatalf("redirect = %q, want the synthetic hosted checkout", document.RedirectURL)
	}

	body := completedSessionEvent("evt_t06ink1", journey.gateway.lastProvisionedSession())
	signature, timestamp := signBillingEvent(journey.webhookSecret, body)
	if err := deliverBillingEvent(t, journey, body, signature, timestamp); err != nil {
		t.Fatalf("verified event: %v", err)
	}
	if got := billingPurchasedBalance(t, client, journey); got != 10000 {
		t.Fatalf("purchased balance = %d, want 10000 after the verified settlement", got)
	}
}

// TestBillingWebhookRefusesForgedAndReplayedEvents proves the two guards of
// the provider-only ingress: a forged signature fails, and a replayed event
// is acknowledged without granting twice.
func TestBillingWebhookRefusesForgedAndReplayedEvents(t *testing.T) {
	t.Parallel()

	journey := newBillingJourney(t, newBillingTestGateway())
	client := billingLogin(t, journey, "billing-replay-t06@arena.example.com", "Correct Horse 11!")
	before := billingPassTotal(t, client, journey)
	checkoutPass(t, client, journey, "t06-pass-replay-1")

	body := completedSessionEvent("evt_t06replay1", journey.gateway.lastProvisionedSession())
	if err := deliverBillingEvent(t, journey, body, "t=1,v1=forged", "1"); err == nil {
		t.Fatal("forged signature accepted: the ingress must authenticate the provider")
	} else if !errors.Is(err, billingapp.ErrWebhookSignatureInvalid) {
		t.Fatalf("forged error = %v, want ErrWebhookSignatureInvalid", err)
	}
	if got := billingPassTotal(t, client, journey); got != before {
		t.Fatal("a forged event settled an intent")
	}

	signature, timestamp := signBillingEvent(journey.webhookSecret, body)
	if err := deliverBillingEvent(t, journey, body, signature, timestamp); err != nil {
		t.Fatalf("first verified delivery: %v", err)
	}
	if err := deliverBillingEvent(t, journey, body, signature, timestamp); err != nil {
		t.Fatalf("replayed delivery: %v (a replay must be acknowledged, not failed)", err)
	}
	if got := billingPassTotal(t, client, journey); got != before+1 {
		t.Fatalf("available_total = %d, want %d: a replay must not grant twice", got, before+1)
	}
}

// TestBillingPortalOpensForCustomer proves the portal write opens the hosted
// session for the stored customer, stays private, and answers 404 for an
// account the provider never saw.
func TestBillingPortalOpensForCustomer(t *testing.T) {
	t.Parallel()

	journey := newBillingJourney(t, newBillingTestGateway())
	client := billingLogin(t, journey, "billing-portal-t06@arena.example.com", "Correct Horse 11!")
	other := billingLogin(t, journey, "billing-portal-other-t06@arena.example.com", "Correct Horse 11!")
	checkoutPass(t, client, journey, "t06-portal-buy-1")

	status, raw, header := billingPost(t, client, journey.server, "/api/v1/me/billing/portal", `{"idempotency_key":"t06-portal-1"}`)
	if status != http.StatusOK {
		t.Fatalf("POST portal status = %d, want 200 (body: %.300s)", status, raw)
	}
	assertEntitlementPrivate(t, header)
	var portal struct {
		PortalURL string `json:"portal_url"`
	}
	if err := json.Unmarshal(raw, &portal); err != nil {
		t.Fatalf("portal is not JSON: %v", err)
	}
	if portal.PortalURL != "https://billing.example/portal/t06" {
		t.Fatalf("portal_url = %q, want the synthetic hosted portal", portal.PortalURL)
	}

	status, raw, _ = billingPost(t, other, journey.server, "/api/v1/me/billing/portal", `{"idempotency_key":"t06-portal-other-1"}`)
	if status != http.StatusNotFound {
		t.Fatalf("POST portal without a customer status = %d, want 404 (body: %.300s)", status, raw)
	}

	anonymous := browser(t)
	for _, path := range []string{"/api/v1/me/billing/checkout", "/api/v1/me/billing/portal"} {
		status, _, header := billingPost(t, anonymous, journey.server, path, `{"market":"BR","product":"pass_1","idempotency_key":"t06-anon-1"}`)
		if status != http.StatusUnauthorized {
			t.Fatalf("POST %s anonymous status = %d, want 401", path, status)
		}
		assertEntitlementPrivate(t, header)
	}
}

// TestBillingCheckoutFailsClosedWhenProviderIsDown proves an unavailable
// provider fails the write instead of granting credit.
func TestBillingCheckoutFailsClosedWhenProviderIsDown(t *testing.T) {
	t.Parallel()

	gateway := &billingTestGateway{fail: billingapp.ErrPaymentGatewayRejected}
	journey := newBillingJourney(t, gateway)
	client := billingLogin(t, journey, "billing-down-t06@arena.example.com", "Correct Horse 11!")
	before := billingPassTotal(t, client, journey)

	status, raw, _ := billingPost(t, client, journey.server, "/api/v1/me/billing/checkout",
		`{"market":"BR","product":"pass_1","idempotency_key":"t06-down-1"}`)
	if status == http.StatusOK {
		t.Fatalf("POST checkout with the provider down status = 200: the failure must refuse, not grant (body: %.300s)", raw)
	}
	if got := billingPassTotal(t, client, journey); got != before {
		t.Fatal("a failed checkout granted a pass")
	}
}

// TestComposeBillingWritesRefusesIncompleteComposition proves the payment
// edges are as required as the process ones: without the gateway, the
// catalog or the webhook secret the writes stay unmounted.
func TestComposeBillingWritesRefusesIncompleteComposition(t *testing.T) {
	t.Parallel()

	database := dbtest.New(t)
	complete := func() (bootstrap.Options, bootstrap.BillingConfig) {
		return bootstrap.Options{
				Env: config.EnvTest, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
				Pool: database.Pool.Pool(), Clock: clockseed.NewClock(),
				Random: clockseed.NewRandom(), Assets: manifestFixture(t),
			}, bootstrap.BillingConfig{
				Gateway:         newBillingTestGateway(),
				Catalog:         testBillingCatalog(t),
				SuccessURL:      "https://arena.example/billing/success",
				CancelURL:       "https://arena.example/billing/cancel",
				PortalReturnURL: "https://arena.example/billing/success",
				WebhookSecret:   "whsec_t06_synthetic_secret",
			}
	}

	options, billing := complete()
	billing.Gateway = nil
	if _, err := bootstrap.ComposeBillingWrites(options, billing); !errors.Is(err, bootstrap.ErrIncompleteComposition) {
		t.Fatalf("nil gateway error = %v, want ErrIncompleteComposition", err)
	}
	options, billing = complete()
	billing.Catalog = nil
	if _, err := bootstrap.ComposeBillingWrites(options, billing); !errors.Is(err, bootstrap.ErrIncompleteComposition) {
		t.Fatalf("nil catalog error = %v, want ErrIncompleteComposition", err)
	}
	options, billing = complete()
	billing.WebhookSecret = ""
	if _, err := bootstrap.ComposeBillingWrites(options, billing); !errors.Is(err, bootstrap.ErrIncompleteComposition) {
		t.Fatalf("empty webhook secret error = %v, want ErrIncompleteComposition", err)
	}
}
