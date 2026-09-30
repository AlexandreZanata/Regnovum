package application_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

const (
	checkoutSuccessURL = "https://arena.example/checkout/success"
	checkoutCancelURL  = "https://arena.example/checkout/cancel"
)

var testCheckoutInstant = time.Date(2026, 9, 18, 9, 30, 0, 0, time.UTC)

// fakeStripeCustomers models the stored account→provider customer mapping with
// the same replay rule as the database: the first mapping of an account wins.
type fakeStripeCustomers struct {
	stored    map[string]application.StripeCustomerRecord
	recorded  []application.RecordStripeCustomerRequest
	loadErr   error
	recordErr error
}

func (r *fakeStripeCustomers) StripeCustomer(_ context.Context, accountID domain.AccountID) (*application.StripeCustomerRecord, error) {
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	record, found := r.stored[accountID.String()]
	if !found {
		return nil, nil
	}
	return &record, nil
}

func (r *fakeStripeCustomers) RecordStripeCustomer(_ context.Context, request application.RecordStripeCustomerRequest) (*application.StripeCustomerRecord, error) {
	r.recorded = append(r.recorded, request)
	if r.recordErr != nil {
		return nil, r.recordErr
	}
	if r.stored == nil {
		r.stored = map[string]application.StripeCustomerRecord{}
	}
	if existing, found := r.stored[request.AccountID.String()]; found {
		return &existing, nil
	}
	record := application.StripeCustomerRecord{
		AccountID:  request.AccountID,
		CustomerID: request.CustomerID,
		Livemode:   request.Livemode,
		CreatedAt:  testCheckoutInstant,
	}
	r.stored[request.AccountID.String()] = record
	return &record, nil
}

func (r *fakeStripeCustomers) AccountIDByStripeCustomer(_ context.Context, customerID domain.StripeCustomerID) (domain.AccountID, error) {
	if r.loadErr != nil {
		return "", r.loadErr
	}
	for _, rec := range r.stored {
		if rec.CustomerID == customerID {
			return rec.AccountID, nil
		}
	}
	return "", application.ErrPurchaserNotFound
}

// fakeCheckoutIntents models the intent table: one row per provider session,
// a replay resolving the stored row (replayed=true) and nothing else written.
type fakeCheckoutIntents struct {
	bySession map[string]application.CheckoutIntentRecord
	recorded  []application.RecordCheckoutIntentRequest
	err       error
	nextID    int
}

func (r *fakeCheckoutIntents) RecordCheckoutIntent(_ context.Context, request application.RecordCheckoutIntentRequest) (*application.RecordCheckoutIntentResult, error) {
	r.recorded = append(r.recorded, request)
	if r.err != nil {
		return nil, r.err
	}
	if r.bySession == nil {
		r.bySession = map[string]application.CheckoutIntentRecord{}
	}
	if existing, found := r.bySession[request.SessionID.String()]; found {
		return &application.RecordCheckoutIntentResult{Intent: existing, Replayed: true}, nil
	}
	r.nextID++
	record := application.CheckoutIntentRecord{
		ID:             fmt.Sprintf("intent-%d", r.nextID),
		AccountID:      request.AccountID,
		Market:         request.Market,
		ProductID:      request.ProductID,
		CatalogVersion: request.CatalogVersion,
		Amount:         request.Amount,
		Livemode:       request.Livemode,
		Status:         request.Status,
		SessionID:      request.SessionID,
		CreatedAt:      testCheckoutInstant,
	}
	r.bySession[request.SessionID.String()] = record
	return &application.RecordCheckoutIntentResult{Intent: record}, nil
}

func (r *fakeCheckoutIntents) GetCheckoutIntentBySession(_ context.Context, sessionID domain.StripeCheckoutSessionID) (*application.CheckoutIntentRecord, error) {
	if r.bySession == nil {
		return nil, application.ErrCheckoutIntentNotFound
	}
	record, found := r.bySession[sessionID.String()]
	if !found {
		return nil, application.ErrCheckoutIntentNotFound
	}
	return &record, nil
}

func (r *fakeCheckoutIntents) MarkCheckoutIntentPaid(_ context.Context, sessionID domain.StripeCheckoutSessionID) error {
	if r.bySession == nil {
		return nil
	}
	record, found := r.bySession[sessionID.String()]
	if !found {
		return nil
	}
	record.Status = domain.CheckoutIntentPaid
	r.bySession[sessionID.String()] = record
	return nil
}

// fakePurchaserDirectory answers the eligibility question, defaulting to an
// eligible account so a test only states what it is about.
type fakePurchaserDirectory struct {
	purchaser application.Purchaser
	err       error
	asked     []domain.AccountID
}

func (d *fakePurchaserDirectory) PurchaserForCheckout(_ context.Context, accountID domain.AccountID) (application.Purchaser, error) {
	d.asked = append(d.asked, accountID)
	if d.err != nil {
		return application.Purchaser{}, d.err
	}
	if d.purchaser.AccountID.IsZero() {
		return application.Purchaser{AccountID: accountID, Eligible: true}, nil
	}
	return d.purchaser, nil
}

// checkoutAnswer builds the provider session of one price: open, unpaid and
// priced exactly as the catalog says.
func checkoutAnswer(id string, mode domain.CheckoutMode, minorUnits int64, currency domain.Currency) application.CheckoutSession {
	return application.CheckoutSession{
		ID:              domain.StripeCheckoutSessionID(id),
		Status:          domain.CheckoutStatusOpen,
		PaymentStatus:   domain.CheckoutPaymentUnpaid,
		Mode:            mode,
		AmountMinor:     minorUnits,
		Currency:        currency,
		ClientReference: "operation-1",
		URL:             "https://checkout.provider.example/session",
	}
}

// checkoutGrant helpers keep the catalog fixtures short and explicit.
func inkGrant(t *testing.T, quantity int64) domain.Grant {
	t.Helper()
	grant, err := domain.NewINKGrant(quantity)
	if err != nil {
		t.Fatalf("NewINKGrant(%d): %v", quantity, err)
	}
	return grant
}

func passGrant(t *testing.T, quantity int32) domain.Grant {
	t.Helper()
	grant, err := domain.NewArenaPassGrant(quantity)
	if err != nil {
		t.Fatalf("NewArenaPassGrant(%d): %v", quantity, err)
	}
	return grant
}

func checkoutProduct(t *testing.T, market domain.Market, product string, minorUnits int64, currency domain.Currency, grant domain.Grant, priceID string) domain.Product {
	t.Helper()
	id, err := domain.ParseProductID(product)
	if err != nil {
		t.Fatalf("ParseProductID(%q): %v", product, err)
	}
	amount, err := domain.NewMoney(minorUnits, currency)
	if err != nil {
		t.Fatalf("NewMoney(%d, %s): %v", minorUnits, currency, err)
	}
	price, err := domain.ParseStripePriceID(priceID)
	if err != nil {
		t.Fatalf("ParseStripePriceID(%q): %v", priceID, err)
	}
	entry, err := domain.NewProduct(market, id, amount, grant, price)
	if err != nil {
		t.Fatalf("NewProduct(%s/%s): %v", market, product, err)
	}
	return entry
}

func checkoutCatalog(t *testing.T, version int, products ...domain.Product) *domain.Catalog {
	t.Helper()
	catalog, err := domain.NewCatalog(version, products)
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return catalog
}

// testCatalog is the commercial fixture: a Brazilian price list with one INK
// pack, one pass pack and the Member subscription, plus one international
// product and one product with no price in this environment.
func testCatalog(t *testing.T) *domain.Catalog {
	t.Helper()
	return checkoutCatalog(t, 7,
		checkoutProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, inkGrant(t, 10000), "price_1QbrInk"),
		checkoutProduct(t, domain.MarketBrazil, "pass_5", 3990, domain.CurrencyBRL, passGrant(t, 5), "price_1QbrPass"),
		checkoutProduct(t, domain.MarketBrazil, "member_monthly", 1990, domain.CurrencyBRL, domain.NewMemberGrant(), "price_1QbrMember"),
		checkoutProduct(t, domain.MarketInternational, "ink_10000", 199, domain.CurrencyUSD, inkGrant(t, 10000), "price_1QintlInk"),
		checkoutProduct(t, domain.MarketBrazil, "pass_1", 990, domain.CurrencyBRL, passGrant(t, 1), ""),
	)
}

// checkoutFixture wires the use case with fakes and a fixed clock.
type checkoutFixture struct {
	useCase    *application.CreateCheckoutUseCase
	gateway    *fakeGateway
	purchasers *fakePurchaserDirectory
	customers  *fakeStripeCustomers
	intents    *fakeCheckoutIntents
	clock      *fakeClock
	accountID  domain.AccountID
}

func newCheckoutFixture(t *testing.T, mutate func(*application.CheckoutDependencies)) *checkoutFixture {
	t.Helper()

	// The fake provider answers, per price, exactly what the catalog fixture
	// charges: the amount of a session is a property of the price, which is why
	// a test that wants a mismatch must configure a session explicitly.
	gateway := &fakeGateway{
		createCustomer: application.Customer{ID: domain.StripeCustomerID("cus_fixture"), Livemode: false},
		sessionByPrice: map[string]application.CheckoutSession{
			"price_1QbrInk":    checkoutAnswer("cs_test_inkbr", domain.CheckoutModePayment, 990, domain.CurrencyBRL),
			"price_1QbrPass":   checkoutAnswer("cs_test_passbr", domain.CheckoutModePayment, 3990, domain.CurrencyBRL),
			"price_1QbrMember": checkoutAnswer("cs_test_memberbr", domain.CheckoutModeSubscription, 1990, domain.CurrencyBRL),
			"price_1QintlInk":  checkoutAnswer("cs_test_inkintl", domain.CheckoutModePayment, 199, domain.CurrencyUSD),
		},
	}
	fixture := &checkoutFixture{
		gateway:    gateway,
		purchasers: &fakePurchaserDirectory{},
		customers:  &fakeStripeCustomers{},
		intents:    &fakeCheckoutIntents{},
		clock:      &fakeClock{now: testCheckoutInstant},
		accountID:  domain.AccountID(testAccountID),
	}

	dependencies := application.CheckoutDependencies{
		Catalog:    testCatalog(t),
		Gateway:    fixture.gateway,
		Purchasers: fixture.purchasers,
		Customers:  fixture.customers,
		Intents:    fixture.intents,
		Clock:      fixture.clock,
		Returns:    application.CheckoutReturnURLs{Success: checkoutSuccessURL, Cancel: checkoutCancelURL},
	}
	if mutate != nil {
		mutate(&dependencies)
	}

	useCase, err := application.NewCreateCheckoutUseCase(dependencies)
	if err != nil {
		t.Fatalf("NewCreateCheckoutUseCase: %v", err)
	}
	fixture.useCase = useCase
	return fixture
}

func (f *checkoutFixture) command(product string) application.CreateCheckoutCommand {
	return application.CreateCheckoutCommand{
		AccountID:      f.accountID.String(),
		Market:         "BR",
		Product:        product,
		IdempotencyKey: "operation-1",
	}
}

// TestCreateCheckoutResolvesEveryCommercialFactOnTheServer is the core proof of
// P12-T04: the caller names a product and a region, and the amount, the
// currency, the price identifier, the mode and the return URLs all come from
// the server.
func TestCreateCheckoutResolvesEveryCommercialFactOnTheServer(t *testing.T) {
	t.Parallel()

	fixture := newCheckoutFixture(t, nil)

	result, err := fixture.useCase.Execute(context.Background(), fixture.command("ink_10000"))
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	// The buyer is provisioned once, under a key derived from the account.
	if len(fixture.gateway.customerRequests) != 1 {
		t.Fatalf("provider customer requests = %d, want 1", len(fixture.gateway.customerRequests))
	}
	wantCustomerKey := "customer:" + fixture.accountID.String()
	if got := fixture.gateway.customerRequests[0].IdempotencyKey; got != wantCustomerKey {
		t.Errorf("customer idempotency key = %q, want %q", got, wantCustomerKey)
	}

	if len(fixture.gateway.sessionRequests) != 1 {
		t.Fatalf("checkout session requests = %d, want 1", len(fixture.gateway.sessionRequests))
	}
	request := fixture.gateway.sessionRequests[0]
	switch {
	case request.PriceID.String() != "price_1QbrInk":
		t.Errorf("price = %q, want the catalog price of BR/ink_10000", request.PriceID.String())
	case request.Mode != domain.CheckoutModePayment:
		t.Errorf("mode = %q, want payment for a one-off INK pack", request.Mode)
	case request.CustomerID.String() != "cus_fixture":
		t.Errorf("customer = %q", request.CustomerID.String())
	case request.SuccessURL != checkoutSuccessURL || request.CancelURL != checkoutCancelURL:
		t.Errorf("return URLs = %q / %q, want the allowlisted ones", request.SuccessURL, request.CancelURL)
	case request.ClientReference != "operation-1":
		t.Errorf("client reference = %q, want the caller operation token", request.ClientReference)
	case request.IdempotencyKey != "checkout:"+fixture.accountID.String()+":operation-1":
		t.Errorf("idempotency key = %q, want the account-namespaced derivation", request.IdempotencyKey)
	}

	if len(fixture.intents.recorded) != 1 {
		t.Fatalf("recorded intents = %d, want 1", len(fixture.intents.recorded))
	}
	recorded := fixture.intents.recorded[0]
	if recorded.Market != domain.MarketBrazil || recorded.ProductID.String() != "ink_10000" || recorded.CatalogVersion != 7 {
		t.Errorf("recorded decision = %+v", recorded)
	}
	if recorded.Amount.MinorUnits() != 990 || recorded.Amount.Currency() != domain.CurrencyBRL {
		t.Errorf("recorded amount = %s, want the catalog price", recorded.Amount.String())
	}
	if recorded.Status != domain.CheckoutIntentOpen || recorded.ClosedAt != nil {
		t.Errorf("recorded status = %s closed_at = %v", recorded.Status, recorded.ClosedAt)
	}

	if result.IntentID == "" || result.Status != domain.CheckoutIntentOpen {
		t.Errorf("result = %+v", result)
	}
	if result.SessionStatus != domain.CheckoutStatusOpen || result.RedirectURL == "" {
		t.Errorf("result session = %s url = %q", result.SessionStatus, result.RedirectURL)
	}
	if result.Amount.MinorUnits() != 990 || result.CatalogVersion != 7 || result.Replayed {
		t.Errorf("result = %+v", result)
	}
}

// TestCreateCheckoutKeepsTheCommercialDecisionPerProductKind covers the three
// products of the plan: INK packs and Arena Passes are one-off payments, the
// Member subscription is a recurring checkout, and each region keeps its own
// price list.
func TestCreateCheckoutKeepsTheCommercialDecisionPerProductKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		market   string
		product  string
		wantMode domain.CheckoutMode
		wantPri  string
		wantUnit int64
		wantCur  domain.Currency
	}{
		{name: "ink pack in Brazil", market: "BR", product: "ink_10000", wantMode: domain.CheckoutModePayment, wantPri: "price_1QbrInk", wantUnit: 990, wantCur: domain.CurrencyBRL},
		{name: "pass pack in Brazil", market: "BR", product: "pass_5", wantMode: domain.CheckoutModePayment, wantPri: "price_1QbrPass", wantUnit: 3990, wantCur: domain.CurrencyBRL},
		{name: "member subscription in Brazil", market: "br", product: "member_monthly", wantMode: domain.CheckoutModeSubscription, wantPri: "price_1QbrMember", wantUnit: 1990, wantCur: domain.CurrencyBRL},
		{name: "ink pack internationally", market: "international", product: "ink_10000", wantMode: domain.CheckoutModePayment, wantPri: "price_1QintlInk", wantUnit: 199, wantCur: domain.CurrencyUSD},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newCheckoutFixture(t, nil)
			command := fixture.command(testCase.product)
			command.Market = testCase.market
			result, err := fixture.useCase.Execute(context.Background(), command)
			if err != nil {
				t.Fatalf("Execute error = %v", err)
			}

			request := fixture.gateway.sessionRequests[0]
			if request.PriceID.String() != testCase.wantPri || request.Mode != testCase.wantMode {
				t.Errorf("provider request = %s/%s, want %s/%s", request.PriceID, request.Mode, testCase.wantPri, testCase.wantMode)
			}
			if result.Market.String() != strings.ToUpper(testCase.market) {
				t.Errorf("market = %q", result.Market)
			}
			if result.Amount.MinorUnits() != testCase.wantUnit || result.Amount.Currency() != testCase.wantCur {
				t.Errorf("amount = %s, want %d %s", result.Amount.String(), testCase.wantUnit, testCase.wantCur)
			}
		})
	}
}

// TestCreateCheckoutRefusesProductsOutsideTheCatalog proves a caller cannot buy
// what the catalog does not sell, including a product that exists but has no
// price in this environment.
func TestCreateCheckoutRefusesProductsOutsideTheCatalog(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		market  string
		product string
		want    error
	}{
		{name: "unknown product", market: "BR", product: "ink_999999", want: domain.ErrUnknownProduct},
		{name: "product of another region", market: "BR", product: "ink_40000", want: domain.ErrUnknownProduct},
		{name: "product without a price here", market: "BR", product: "pass_1", want: domain.ErrProductNotPriced},
		{name: "unknown region", market: "MARS", product: "ink_10000", want: domain.ErrInvalidMarket},
		{name: "product identifier that is not one", market: "BR", product: "https://evil.example/pay", want: domain.ErrInvalidProductID},
		{name: "product identifier with a script", market: "BR", product: "javascript:alert(1)", want: domain.ErrInvalidProductID},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newCheckoutFixture(t, nil)
			command := fixture.command(testCase.product)
			command.Market = testCase.market

			_, err := fixture.useCase.Execute(context.Background(), command)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
			if len(fixture.gateway.sessionRequests) != 0 || len(fixture.gateway.customerRequests) != 0 {
				t.Error("a refused product must never reach the provider")
			}
			if len(fixture.intents.recorded) != 0 {
				t.Error("a refused product must never be recorded")
			}
		})
	}
}

// TestCreateCheckoutRefusesIneligibleAndUnknownAccounts covers the
// "conta não verificada" rule: only an active account with a verified email
// can start a checkout, and an unknown account is refused as such.
func TestCreateCheckoutRefusesIneligibleAndUnknownAccounts(t *testing.T) {
	t.Parallel()

	t.Run("ineligible account", func(t *testing.T) {
		t.Parallel()

		fixture := newCheckoutFixture(t, nil)
		fixture.purchasers.purchaser = application.Purchaser{AccountID: fixture.accountID, Eligible: false}

		_, err := fixture.useCase.Execute(context.Background(), fixture.command("ink_10000"))
		if !errors.Is(err, application.ErrPurchaserNotEligible) {
			t.Fatalf("error = %v, want ErrPurchaserNotEligible", err)
		}
		if len(fixture.gateway.sessionRequests) != 0 || len(fixture.intents.recorded) != 0 {
			t.Error("an ineligible account must not reach the provider or be recorded")
		}
	})

	t.Run("unknown account", func(t *testing.T) {
		t.Parallel()

		fixture := newCheckoutFixture(t, nil)
		fixture.purchasers.err = application.ErrPurchaserNotFound

		_, err := fixture.useCase.Execute(context.Background(), fixture.command("ink_10000"))
		if !errors.Is(err, application.ErrPurchaserNotFound) {
			t.Fatalf("error = %v, want ErrPurchaserNotFound", err)
		}
		if len(fixture.gateway.sessionRequests) != 0 {
			t.Error("an unknown account must not reach the provider")
		}
	})

	t.Run("empty account identifier", func(t *testing.T) {
		t.Parallel()

		fixture := newCheckoutFixture(t, nil)
		command := fixture.command("ink_10000")
		command.AccountID = "   "

		_, err := fixture.useCase.Execute(context.Background(), command)
		if !errors.Is(err, domain.ErrEmptyAccountID) {
			t.Fatalf("error = %v, want ErrEmptyAccountID", err)
		}
		if len(fixture.purchasers.asked) != 0 {
			t.Error("an empty account must never be looked up")
		}
	})

	t.Run("missing operation token", func(t *testing.T) {
		t.Parallel()

		fixture := newCheckoutFixture(t, nil)
		command := fixture.command("ink_10000")
		command.IdempotencyKey = ""

		_, err := fixture.useCase.Execute(context.Background(), command)
		if !errors.Is(err, domain.ErrEmptyIdempotencyKey) {
			t.Fatalf("error = %v, want ErrEmptyIdempotencyKey", err)
		}
		if len(fixture.gateway.sessionRequests) != 0 {
			t.Error("an operation without a retry key must not reach the provider")
		}
	})
}

// TestCreateCheckoutIsReplaySafe proves the plan's replay rule end to end: the
// same operation token resolves the same provider session and the same intent,
// and the account is never provisioned twice.
func TestCreateCheckoutIsReplaySafe(t *testing.T) {
	t.Parallel()

	fixture := newCheckoutFixture(t, nil)
	command := fixture.command("ink_10000")

	first, err := fixture.useCase.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("first Execute error = %v", err)
	}
	if first.Replayed {
		t.Fatal("the first attempt is not a replay")
	}

	second, err := fixture.useCase.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("second Execute error = %v", err)
	}
	if !second.Replayed {
		t.Error("the retry must resolve the stored intent")
	}
	if second.IntentID != first.IntentID {
		t.Errorf("intent = %q then %q, want the same one", first.IntentID, second.IntentID)
	}
	if second.SessionID != first.SessionID {
		t.Errorf("session = %q then %q, want the same one", first.SessionID, second.SessionID)
	}

	// The provider receives the same idempotency key both times, which is what
	// makes the provider return one session; locally the customer is resolved
	// from the stored mapping and one intent is recorded.
	if len(fixture.gateway.sessionRequests) != 2 {
		t.Fatalf("session requests = %d, want 2", len(fixture.gateway.sessionRequests))
	}
	if fixture.gateway.sessionRequests[0].IdempotencyKey != fixture.gateway.sessionRequests[1].IdempotencyKey {
		t.Error("a retry must reuse the same provider idempotency key")
	}
	if len(fixture.gateway.customerRequests) != 1 {
		t.Errorf("customer requests = %d, want exactly 1", len(fixture.gateway.customerRequests))
	}
	if len(fixture.intents.recorded) != 2 || len(fixture.intents.bySession) != 1 {
		t.Errorf("recorded intents = %d rows = %d, want 2 attempts on one row", len(fixture.intents.recorded), len(fixture.intents.bySession))
	}
}

// TestCreateCheckoutKeysAreNamespacedPerAccountAndToken keeps two accounts from
// ever colliding on a provider idempotency key, and proves the same token for
// the same account stays stable (which is what makes a retry harmless).
func TestCreateCheckoutKeysAreNamespacedPerAccountAndToken(t *testing.T) {
	t.Parallel()

	fixture := newCheckoutFixture(t, nil)
	sameToken := fixture.command("ink_10000")
	if _, err := fixture.useCase.Execute(context.Background(), sameToken); err != nil {
		t.Fatalf("first Execute error = %v", err)
	}

	otherToken := fixture.command("pass_5")
	otherToken.IdempotencyKey = "operation-2"
	second, err := fixture.useCase.Execute(context.Background(), otherToken)
	if err != nil {
		t.Fatalf("second Execute error = %v", err)
	}

	otherAccount := fixture.command("ink_10000")
	otherAccount.AccountID = "018f6b2a-0000-7000-8000-0000000000ff"
	if _, err := fixture.useCase.Execute(context.Background(), otherAccount); err != nil {
		t.Fatalf("other account Execute error = %v", err)
	}

	keys := map[string]bool{}
	for _, request := range fixture.gateway.sessionRequests {
		if keys[request.IdempotencyKey] {
			t.Fatalf("duplicate provider key %q", request.IdempotencyKey)
		}
		keys[request.IdempotencyKey] = true
	}
	want := []string{
		"checkout:" + fixture.accountID.String() + ":operation-1",
		"checkout:" + fixture.accountID.String() + ":operation-2",
		"checkout:018f6b2a-0000-7000-8000-0000000000ff:operation-1",
	}
	for _, key := range want {
		if !keys[key] {
			t.Errorf("provider key %q never reached the provider (got %v)", key, keys)
		}
	}
	if second.Amount.MinorUnits() != 3990 {
		t.Errorf("second amount = %s", second.Amount.String())
	}
}

// TestCreateCheckoutCommandCannotCarryAPriceOrADestination is the mechanical
// half of "não aceitar amount/price arbitrário do browser" and of "open
// redirect": the request type the browser can fill has no field able to carry
// an amount, a currency, a price, an email or a URL.
func TestCreateCheckoutCommandCannotCarryAPriceOrADestination(t *testing.T) {
	t.Parallel()

	commandType := reflect.TypeOf(application.CreateCheckoutCommand{})
	fields := make([]string, 0, commandType.NumField())
	for index := 0; index < commandType.NumField(); index++ {
		field := commandType.Field(index)
		if field.Type.Kind() != reflect.String {
			t.Errorf("field %s is %s: the browser may only name identifiers", field.Name, field.Type)
		}
		fields = append(fields, field.Name)
		lowered := strings.ToLower(field.Name)
		for _, forbidden := range []string{"amount", "price", "currency", "money", "minor", "url", "redirect", "email", "customer"} {
			if strings.Contains(lowered, forbidden) {
				t.Errorf("field %s carries %q: that value belongs to the server", field.Name, forbidden)
			}
		}
	}

	want := []string{"AccountID", "Market", "Product", "IdempotencyKey"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("command fields = %v, want %v", fields, want)
	}

	// The catalog price is the only amount that can reach the provider: two
	// accounts buying the same product are quoted the same amount, and a
	// caller that tried to name a different one has no way to say it.
	fixture := newCheckoutFixture(t, nil)
	second := fixture.command("pass_5")
	second.AccountID = "018f6b2a-0000-7000-8000-0000000000ff"
	if _, err := fixture.useCase.Execute(context.Background(), fixture.command("pass_5")); err != nil {
		t.Fatalf("first purchase error = %v", err)
	}
	if _, err := fixture.useCase.Execute(context.Background(), second); err != nil {
		t.Fatalf("second purchase error = %v", err)
	}
	for index, recorded := range fixture.intents.recorded {
		if recorded.Amount.MinorUnits() != 3990 || recorded.Amount.Currency() != domain.CurrencyBRL {
			t.Errorf("intent %d amount = %s, want the catalog price 3990 BRL", index, recorded.Amount.String())
		}
	}
}

// TestCreateCheckoutRefusesAProviderPriceMismatch refuses a provider session
// that would charge something other than the catalog price: a price edited in
// the provider dashboard must never make the buyer pay a different amount.
func TestCreateCheckoutRefusesAProviderPriceMismatch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mode    domain.CheckoutMode
		amount  int64
		cur     domain.Currency
		product string
		refused bool
	}{
		{name: "one-off with another amount", mode: domain.CheckoutModePayment, amount: 1234, cur: domain.CurrencyBRL, product: "ink_10000", refused: true},
		{name: "one-off with no amount at all", mode: domain.CheckoutModePayment, amount: 0, cur: domain.CurrencyBRL, product: "ink_10000", refused: true},
		{name: "one-off with another currency", mode: domain.CheckoutModePayment, amount: 990, cur: domain.CurrencyUSD, product: "ink_10000", refused: true},
		{name: "one-off with the catalog price", mode: domain.CheckoutModePayment, amount: 990, cur: domain.CurrencyBRL, product: "ink_10000", refused: false},
		{name: "subscription without a total", mode: domain.CheckoutModeSubscription, amount: 0, cur: domain.CurrencyBRL, product: "member_monthly", refused: false},
		{name: "subscription with the catalog total", mode: domain.CheckoutModeSubscription, amount: 1990, cur: domain.CurrencyBRL, product: "member_monthly", refused: false},
		{name: "subscription with another total", mode: domain.CheckoutModeSubscription, amount: 1500, cur: domain.CurrencyBRL, product: "member_monthly", refused: true},
		{name: "subscription with another currency", mode: domain.CheckoutModeSubscription, amount: 0, cur: domain.CurrencyUSD, product: "member_monthly", refused: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newCheckoutFixture(t, nil)
			fixture.gateway.sessionByPrice = nil
			fixture.gateway.checkoutSession = checkoutAnswer("cs_test_mismatch", testCase.mode, testCase.amount, testCase.cur)

			_, err := fixture.useCase.Execute(context.Background(), fixture.command(testCase.product))
			if testCase.refused {
				if !errors.Is(err, application.ErrProviderAmountMismatch) {
					t.Fatalf("error = %v, want ErrProviderAmountMismatch", err)
				}
				if len(fixture.intents.recorded) != 0 {
					t.Error("a mismatched price must never be recorded")
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute error = %v", err)
			}
			if len(fixture.intents.recorded) != 1 {
				t.Error("an accepted session must be recorded once")
			}
		})
	}
}

// TestCreateCheckoutRefusesAProviderModeSwitch stops the flow when the account
// mapping belongs to the other provider mode: test and live objects are never
// mixed.
func TestCreateCheckoutRefusesAProviderModeSwitch(t *testing.T) {
	t.Parallel()

	fixture := newCheckoutFixture(t, nil)
	fixture.customers.stored = map[string]application.StripeCustomerRecord{
		fixture.accountID.String(): {
			AccountID:  fixture.accountID,
			CustomerID: domain.StripeCustomerID("cus_test_fixture"),
			Livemode:   false,
		},
	}
	fixture.gateway.checkoutSession.ID = "cs_live_switch"
	fixture.gateway.checkoutSession.Livemode = true

	_, err := fixture.useCase.Execute(context.Background(), fixture.command("ink_10000"))
	if !errors.Is(err, application.ErrProviderModeChanged) {
		t.Fatalf("error = %v, want ErrProviderModeChanged", err)
	}
	if len(fixture.intents.recorded) != 0 {
		t.Error("a mode switch must never be recorded")
	}
	if len(fixture.gateway.customerRequests) != 0 {
		t.Error("an existing mapping must not provision another customer")
	}
}

// TestCreateCheckoutRecordsAnUnpayableSession shows that a session the provider
// already reports as no longer payable is recorded as an expired intent, with
// the instant it was learned, instead of silently pretending it is open.
func TestCreateCheckoutRecordsAnUnpayableSession(t *testing.T) {
	t.Parallel()

	fixture := newCheckoutFixture(t, nil)
	fixture.gateway.checkoutSession = checkoutAnswer("cs_test_expired", domain.CheckoutModePayment, 990, domain.CurrencyBRL)
	fixture.gateway.checkoutSession.Status = domain.CheckoutStatusExpired

	result, err := fixture.useCase.Execute(context.Background(), fixture.command("ink_10000"))
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.Status != domain.CheckoutIntentExpired || result.SessionStatus != domain.CheckoutStatusExpired {
		t.Fatalf("result = %+v", result)
	}
	recorded := fixture.intents.recorded[0]
	if recorded.Status != domain.CheckoutIntentExpired {
		t.Fatalf("recorded status = %s", recorded.Status)
	}
	if recorded.ClosedAt == nil || !recorded.ClosedAt.Equal(testCheckoutInstant) {
		t.Fatalf("closed at = %v, want the clock instant", recorded.ClosedAt)
	}
}

// TestCreateCheckoutPropagatesFailuresWithoutWritingAnything keeps a failed
// attempt from leaving a financial record behind: the intent is written only
// after the provider answered.
func TestCreateCheckoutPropagatesFailuresWithoutWritingAnything(t *testing.T) {
	t.Parallel()

	t.Run("provider refuses the session", func(t *testing.T) {
		t.Parallel()

		fixture := newCheckoutFixture(t, nil)
		fixture.gateway.checkoutErr = application.ErrPaymentGatewayRejected

		_, err := fixture.useCase.Execute(context.Background(), fixture.command("ink_10000"))
		if !errors.Is(err, application.ErrPaymentGatewayRejected) {
			t.Fatalf("error = %v", err)
		}
		if len(fixture.intents.recorded) != 0 {
			t.Error("a refused session must not be recorded")
		}
	})

	t.Run("provider is unavailable", func(t *testing.T) {
		t.Parallel()

		fixture := newCheckoutFixture(t, nil)
		fixture.gateway.customerErr = application.ErrPaymentGatewayUnavailable

		_, err := fixture.useCase.Execute(context.Background(), fixture.command("ink_10000"))
		if !errors.Is(err, application.ErrPaymentGatewayUnavailable) {
			t.Fatalf("error = %v", err)
		}
		if len(fixture.customers.recorded) != 0 || len(fixture.intents.recorded) != 0 {
			t.Error("a failed provisioning must not be recorded")
		}
	})

	t.Run("record fails", func(t *testing.T) {
		t.Parallel()

		fixture := newCheckoutFixture(t, nil)
		fixture.intents.err = errors.New("storage down")

		if _, err := fixture.useCase.Execute(context.Background(), fixture.command("ink_10000")); err == nil {
			t.Fatal("a storage failure must surface")
		}
	})
}

// TestNewCreateCheckoutUseCaseRequiresCoherentConfiguration refuses to build a
// checkout missing any dependency it drives, or missing the declared return
// URLs, so an attempt can never be created without a price to charge or a place
// to send the buyer back to.
//
// URL syntax and the single-origin rule are NOT asserted here on purpose: the
// use case treats the return URLs as opaque deploy-time configuration and the
// domain/application layers may not import net/url. They are asserted where a
// URL is handled — TestBillingReturnURLsAreAllowlisted (config, at boot) and the
// Stripe adapter's malformed-URL cases (before the provider socket opens).
func TestNewCreateCheckoutUseCaseRequiresCoherentConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*application.CheckoutDependencies)
	}{
		{name: "no catalog", mutate: func(d *application.CheckoutDependencies) { d.Catalog = nil }},
		{name: "no gateway", mutate: func(d *application.CheckoutDependencies) { d.Gateway = nil }},
		{name: "no purchaser directory", mutate: func(d *application.CheckoutDependencies) { d.Purchasers = nil }},
		{name: "no customer repository", mutate: func(d *application.CheckoutDependencies) { d.Customers = nil }},
		{name: "no intent repository", mutate: func(d *application.CheckoutDependencies) { d.Intents = nil }},
		{name: "no clock", mutate: func(d *application.CheckoutDependencies) { d.Clock = nil }},
		{name: "no success URL", mutate: func(d *application.CheckoutDependencies) { d.Returns.Success = "" }},
		{name: "no cancel URL", mutate: func(d *application.CheckoutDependencies) { d.Returns.Cancel = "" }},
		{
			name: "whitespace-only success URL",
			mutate: func(d *application.CheckoutDependencies) {
				// Not trimmed or repaired here: a value that is not the
				// configured URL would only ever reach the provider by
				// mistake, so the use case refuses to guess.
				d.Returns.Success = "   "
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			dependencies := application.CheckoutDependencies{
				Catalog:    testCatalog(t),
				Gateway:    &fakeGateway{},
				Purchasers: &fakePurchaserDirectory{},
				Customers:  &fakeStripeCustomers{},
				Intents:    &fakeCheckoutIntents{},
				Clock:      &fakeClock{now: testCheckoutInstant},
				Returns:    application.CheckoutReturnURLs{Success: checkoutSuccessURL, Cancel: checkoutCancelURL},
			}
			testCase.mutate(&dependencies)

			useCase, err := application.NewCreateCheckoutUseCase(dependencies)
			if !errors.Is(err, application.ErrInvalidCheckoutConfig) {
				t.Fatalf("error = %v, want ErrInvalidCheckoutConfig", err)
			}
			if useCase != nil {
				t.Fatal("a refused configuration must not build a use case")
			}
		})
	}

	if _, err := application.NewCreateCheckoutUseCase(application.CheckoutDependencies{
		Catalog:    testCatalog(t),
		Gateway:    &fakeGateway{},
		Purchasers: &fakePurchaserDirectory{},
		Customers:  &fakeStripeCustomers{},
		Intents:    &fakeCheckoutIntents{},
		Clock:      &fakeClock{now: testCheckoutInstant},
		Returns:    application.CheckoutReturnURLs{Success: checkoutSuccessURL, Cancel: checkoutCancelURL},
	}); err != nil {
		t.Fatalf("a coherent configuration must build: %v", err)
	}
}

func TestCreateCheckoutProviderKeyBoundaryLength(t *testing.T) {
	t.Parallel()

	// The derived provider key ("checkout:" + account + ":" + token) is
	// accepted at exactly the provider bound and refused past it
	// (mutation gate: create_checkout.go:326).
	fixture := newCheckoutFixture(t, nil)
	key200 := strings.Repeat("k", 200)
	account45 := domain.AccountID(strings.Repeat("a", 45))
	if len("checkout:"+account45.String()+":"+key200) != 255 {
		t.Fatalf("fixture key length = %d, want exactly 255", len("checkout:"+account45.String()+":"+key200))
	}
	boundary := fixture.command("ink_10000")
	boundary.AccountID = account45.String()
	boundary.IdempotencyKey = key200
	if _, err := fixture.useCase.Execute(context.Background(), boundary); err != nil {
		t.Fatalf("255-byte provider key rejected: %v", err)
	}
	past := boundary
	past.AccountID = strings.Repeat("a", 46)
	if _, err := fixture.useCase.Execute(context.Background(), past); !errors.Is(err, domain.ErrIdempotencyKeyTooLong) {
		t.Fatalf("256-byte provider key error = %v, want ErrIdempotencyKeyTooLong", err)
	}
}
