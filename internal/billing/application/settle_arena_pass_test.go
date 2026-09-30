package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// fakeSettlePassLots records pass lot grant requests for testing.
type fakeSettlePassLots struct {
	grants  []application.GrantPassLotRequest
	results []application.GrantPassLotResult
	errors  []error
	next    int
}

func (f *fakeSettlePassLots) GrantPassLot(_ context.Context, request application.GrantPassLotRequest) (*application.GrantPassLotResult, error) {
	f.grants = append(f.grants, request)
	if f.next < len(f.errors) && f.errors[f.next] != nil {
		err := f.errors[f.next]
		f.next++
		return nil, err
	}
	if f.next < len(f.results) {
		result := f.results[f.next]
		f.next++
		return &result, nil
	}
	return &application.GrantPassLotResult{Replayed: false}, nil
}

// mustNewPassProduct builds an Arena Pass product or fails the test.
func mustNewPassProduct(t *testing.T, market domain.Market, id string, amountMinor int64, currency domain.Currency, passQuantity int32, priceID string) domain.Product {
	t.Helper()
	productID, err := domain.ParseProductID(id)
	if err != nil {
		t.Fatalf("ParseProductID: %v", err)
	}
	amount, err := domain.NewMoney(amountMinor, currency)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	grant, err := domain.NewArenaPassGrant(passQuantity)
	if err != nil {
		t.Fatalf("NewArenaPassGrant: %v", err)
	}
	var price domain.StripePriceID
	if priceID != "" {
		price, err = domain.ParseStripePriceID(priceID)
		if err != nil {
			t.Fatalf("ParseStripePriceID: %v", err)
		}
	}
	product, err := domain.NewProduct(market, productID, amount, grant, price)
	if err != nil {
		t.Fatalf("NewProduct: %v", err)
	}
	return product
}

// testSettlePassCatalog builds a minimal catalog with an Arena Pass product and an INK product.
func testSettlePassCatalog(t *testing.T) *domain.Catalog {
	t.Helper()
	catalog, err := domain.NewCatalog(1, []domain.Product{
		mustNewPassProduct(t, domain.MarketBrazil, "pass_5", 3990, domain.CurrencyBRL, 5, "price_1QbrPass"),
		mustNewProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, 10000, "price_1QbrInk"),
	})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return catalog
}

// TestSettleArenaPassGrantsPassesOnSettledPayment proves the core flow: a valid
// open intent is settled, the account receives the exact pass quantity with OriginPurchase,
// purchased passes do not expire (ExpiresAt == nil), and the intent is marked as paid.
func TestSettleArenaPassGrantsPassesOnSettledPayment(t *testing.T) {
	t.Parallel()

	catalog := testSettlePassCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntentWithProduct(t, "cs_test_session1", domain.CheckoutIntentOpen, "pass_5")
	intents.intents[intent.ID] = intent

	lots := &fakeSettlePassLots{
		results: []application.GrantPassLotResult{{Replayed: false}},
	}

	useCase, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{
		Catalog: catalog,
		Intents: intents,
		Lots:    lots,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleArenaPassUseCase: %v", err)
	}

	sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_session1", false)
	result, err := useCase.Execute(context.Background(), application.SettleCheckoutCommand{
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.IntentID != "intent-1" {
		t.Errorf("intentID = %q, want intent-1", result.IntentID)
	}
	if result.Replayed {
		t.Error("first settlement must not be a replay")
	}
	if result.AmountCredited != 5 {
		t.Errorf("amountCredited = %d, want 5", result.AmountCredited)
	}

	// Verify the pass grant was requested with exact quantity and no expiration.
	if len(lots.grants) != 1 {
		t.Fatalf("grants = %d, want 1", len(lots.grants))
	}
	grant := lots.grants[0]
	if grant.AccountID != "018f6b2a-0000-7000-8000-0000000000ff" {
		t.Errorf("accountID = %q", grant.AccountID)
	}
	if grant.Origin != domain.OriginPurchase {
		t.Errorf("origin = %s, want %s", grant.Origin, domain.OriginPurchase)
	}
	if grant.Quantity.Int32() != 5 {
		t.Errorf("quantity = %d, want 5", grant.Quantity.Int32())
	}
	if grant.ExpiresAt != nil {
		t.Errorf("purchased passes must never expire, got ExpiresAt = %v", grant.ExpiresAt)
	}
	if grant.Reference.String() != "intent-1" {
		t.Errorf("reference = %q, want intent-1", grant.Reference.String())
	}

	// Verify the intent was marked as paid.
	if intent.Status != domain.CheckoutIntentPaid {
		t.Errorf("intent status = %s, want paid", intent.Status)
	}
}

// TestSettleArenaPassIsIdempotentOnReplay proves that a replay of the same
// settlement does not grant passes twice.
func TestSettleArenaPassIsIdempotentOnReplay(t *testing.T) {
	t.Parallel()

	catalog := testSettlePassCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntentWithProduct(t, "cs_test_session1", domain.CheckoutIntentOpen, "pass_5")
	intents.intents[intent.ID] = intent

	lots := &fakeSettlePassLots{
		results: []application.GrantPassLotResult{
			{Replayed: false},
			{Replayed: true},
		},
	}

	useCase, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{
		Catalog: catalog,
		Intents: intents,
		Lots:    lots,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleArenaPassUseCase: %v", err)
	}

	sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_session1", false)
	cmd := application.SettleCheckoutCommand{SessionID: sessionID}

	// First settlement.
	result1, err := useCase.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if result1.Replayed {
		t.Error("first settlement must not be a replay")
	}

	// Mark intent as paid (simulating the first settlement).
	intent.Status = domain.CheckoutIntentPaid
	intents.intents[intent.ID] = intent

	// Second settlement: replay.
	result2, err := useCase.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if !result2.Replayed {
		t.Error("second settlement must be a replay")
	}

	// Only one pass grant should have been requested.
	if len(lots.grants) != 1 {
		t.Errorf("grants = %d, want 1", len(lots.grants))
	}
}

// TestSettleArenaPassRejectsAlreadySettledIntent proves that an already settled
// intent is treated as a replay.
func TestSettleArenaPassRejectsAlreadySettledIntent(t *testing.T) {
	t.Parallel()

	catalog := testSettlePassCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntentWithProduct(t, "cs_test_session1", domain.CheckoutIntentPaid, "pass_5")
	intents.intents[intent.ID] = intent

	lots := &fakeSettlePassLots{}

	useCase, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{
		Catalog: catalog,
		Intents: intents,
		Lots:    lots,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleArenaPassUseCase: %v", err)
	}

	sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_session1", false)
	result, err := useCase.Execute(context.Background(), application.SettleCheckoutCommand{
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Replayed {
		t.Error("already settled intent must be treated as replay")
	}
	if len(lots.grants) != 0 {
		t.Errorf("grants = %d, want 0 (no passes granted)", len(lots.grants))
	}
}

// TestSettleArenaPassRejectsExpiredIntent proves that an expired intent cannot
// be settled.
func TestSettleArenaPassRejectsExpiredIntent(t *testing.T) {
	t.Parallel()

	catalog := testSettlePassCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntentWithProduct(t, "cs_test_expired1", domain.CheckoutIntentExpired, "pass_5")
	intents.intents[intent.ID] = intent

	lots := &fakeSettlePassLots{}

	useCase, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{
		Catalog: catalog,
		Intents: intents,
		Lots:    lots,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleArenaPassUseCase: %v", err)
	}

	sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_expired1", false)
	result, err := useCase.Execute(context.Background(), application.SettleCheckoutCommand{
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Replayed {
		t.Error("expired intent must be treated as replay")
	}
	if len(lots.grants) != 0 {
		t.Errorf("grants = %d, want 0 (no passes granted)", len(lots.grants))
	}
}

// TestSettleArenaPassRejectsUnknownSession proves that a session not found
// locally is refused.
func TestSettleArenaPassRejectsUnknownSession(t *testing.T) {
	t.Parallel()

	catalog := testSettlePassCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	lots := &fakeSettlePassLots{}

	useCase, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{
		Catalog: catalog,
		Intents: intents,
		Lots:    lots,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleArenaPassUseCase: %v", err)
	}

	sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_unknown1", false)
	_, err = useCase.Execute(context.Background(), application.SettleCheckoutCommand{
		SessionID: sessionID,
	})
	if !errors.Is(err, application.ErrCheckoutIntentNotFound) {
		t.Fatalf("error = %v, want ErrCheckoutIntentNotFound", err)
	}
	if len(lots.grants) != 0 {
		t.Errorf("grants = %d, want 0", len(lots.grants))
	}
}

// TestSettleArenaPassCatálogoDivergente proves that catalog divergence
// (wrong grant kind, missing product) is rejected properly.
func TestSettleArenaPassCatálogoDivergente(t *testing.T) {
	t.Parallel()

	catalog := testSettlePassCatalog(t)

	t.Run("product grants INK instead of ARENA_PASS", func(t *testing.T) {
		t.Parallel()
		intents := newFakeSettleCheckoutIntents()
		intent := mustNewCheckoutIntentWithProduct(t, "cs_test_inksession", domain.CheckoutIntentOpen, "ink_10000")
		intents.intents[intent.ID] = intent
		lots := &fakeSettlePassLots{}

		useCase, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{
			Catalog: catalog,
			Intents: intents,
			Lots:    lots,
			Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
		})
		if err != nil {
			t.Fatalf("NewSettleArenaPassUseCase: %v", err)
		}

		sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_inksession", false)
		_, err = useCase.Execute(context.Background(), application.SettleCheckoutCommand{
			SessionID: sessionID,
		})
		if !errors.Is(err, application.ErrSettleCheckoutWrongGrantKind) {
			t.Fatalf("error = %v, want ErrSettleCheckoutWrongGrantKind", err)
		}
		if len(lots.grants) != 0 {
			t.Errorf("grants = %d, want 0", len(lots.grants))
		}
	})

	t.Run("product not found in catalog", func(t *testing.T) {
		t.Parallel()
		intents := newFakeSettleCheckoutIntents()
		intent := mustNewCheckoutIntentWithProduct(t, "cs_test_unknprod", domain.CheckoutIntentOpen, "pass_unknown")
		intents.intents[intent.ID] = intent
		lots := &fakeSettlePassLots{}

		useCase, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{
			Catalog: catalog,
			Intents: intents,
			Lots:    lots,
			Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
		})
		if err != nil {
			t.Fatalf("NewSettleArenaPassUseCase: %v", err)
		}

		sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_unknprod", false)
		_, err = useCase.Execute(context.Background(), application.SettleCheckoutCommand{
			SessionID: sessionID,
		})
		if err == nil {
			t.Fatal("expected error for product not in catalog")
		}
		if len(lots.grants) != 0 {
			t.Errorf("grants = %d, want 0", len(lots.grants))
		}
	})
}

// TestSettleArenaPassRollbackWhenGrantFails proves that if granting the pass lot fails,
// the intent is NOT marked as paid (rollback / failure protection).
func TestSettleArenaPassRollbackWhenGrantFails(t *testing.T) {
	t.Parallel()

	catalog := testSettlePassCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntentWithProduct(t, "cs_test_failgrant", domain.CheckoutIntentOpen, "pass_5")
	intents.intents[intent.ID] = intent

	expectedErr := errors.New("db connection failure")
	lots := &fakeSettlePassLots{
		errors: []error{expectedErr},
	}

	useCase, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{
		Catalog: catalog,
		Intents: intents,
		Lots:    lots,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleArenaPassUseCase: %v", err)
	}

	sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_failgrant", false)
	_, err = useCase.Execute(context.Background(), application.SettleCheckoutCommand{
		SessionID: sessionID,
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("error = %v, want %v", err, expectedErr)
	}

	// CRITICAL ROLLBACK CHECK: Intent MUST NOT be marked as paid!
	if intent.Status != domain.CheckoutIntentOpen {
		t.Errorf("intent status = %s, want open (must not mark paid on grant failure)", intent.Status)
	}
}

// TestSettleArenaPassRequiresCoherentConfiguration proves the use case refuses
// incomplete composition.
func TestSettleArenaPassRequiresCoherentConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*application.SettleArenaPassDependencies)
	}{
		{name: "no catalog", mutate: func(d *application.SettleArenaPassDependencies) { d.Catalog = nil }},
		{name: "no intents", mutate: func(d *application.SettleArenaPassDependencies) { d.Intents = nil }},
		{name: "no lots", mutate: func(d *application.SettleArenaPassDependencies) { d.Lots = nil }},
		{name: "no clock", mutate: func(d *application.SettleArenaPassDependencies) { d.Clock = nil }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			deps := application.SettleArenaPassDependencies{
				Catalog: testSettlePassCatalog(t),
				Intents: newFakeSettleCheckoutIntents(),
				Lots:    &fakeSettlePassLots{},
				Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
			}
			testCase.mutate(&deps)
			_, err := application.NewSettleArenaPassUseCase(deps)
			if err == nil {
				t.Fatal("a refused configuration must not build a use case")
			}
		})
	}
}

// TestProcessWebhookHandlesPassCheckoutSessionCompleted proves that webhook processing
// correctly settles an Arena Pass purchase through PassSettler.
func TestProcessWebhookHandlesPassCheckoutSessionCompleted(t *testing.T) {
	t.Parallel()

	catalog := testSettlePassCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntentWithProduct(t, "cs_test_session1", domain.CheckoutIntentOpen, "pass_5")
	intents.intents[intent.ID] = intent
	lots := &fakeSettlePassLots{
		results: []application.GrantPassLotResult{{Replayed: false}},
	}
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	passSettler, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{
		Catalog: catalog,
		Intents: intents,
		Lots:    lots,
		Clock:   clock,
	})
	if err != nil {
		t.Fatalf("NewSettleArenaPassUseCase: %v", err)
	}

	repo := newFakeWebhookEventRepository()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier:    &fakeWebhookVerifier{verifyResult: nil},
		Events:      repo,
		PassSettler: passSettler,
		Clock:       clock,
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase: %v", err)
	}

	body := testCheckoutSessionBody("paid")
	if err := useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: "123",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Verify the pass was granted and intent marked paid.
	if len(lots.grants) != 1 {
		t.Fatalf("grants = %d, want 1", len(lots.grants))
	}
	if lots.grants[0].Quantity.Int32() != 5 {
		t.Errorf("granted quantity = %d, want 5", lots.grants[0].Quantity.Int32())
	}
	if intent.Status != domain.CheckoutIntentPaid {
		t.Errorf("intent status = %s, want paid", intent.Status)
	}
}

// mustNewCheckoutIntentWithProduct creates a checkout intent for testing with
// a specific product ID.
func mustNewCheckoutIntentWithProduct(t *testing.T, sessionID string, status domain.CheckoutIntentStatus, productID string) *application.CheckoutIntentRecord {
	t.Helper()
	sid, err := domain.ParseStripeCheckoutSessionID(sessionID, false)
	if err != nil {
		t.Fatalf("ParseStripeCheckoutSessionID(%q): %v", sessionID, err)
	}
	amount, err := domain.NewMoney(3990, domain.CurrencyBRL)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	return &application.CheckoutIntentRecord{
		ID:             "intent-1",
		AccountID:      "018f6b2a-0000-7000-8000-0000000000ff",
		Market:         domain.MarketBrazil,
		ProductID:      domain.ProductID(productID),
		CatalogVersion: 1,
		Amount:         amount,
		Livemode:       false,
		Status:         status,
		SessionID:      sid,
		CreatedAt:      time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	}
}

func TestSettleArenaPassAcceptsSinglePassGrant(t *testing.T) {
	t.Parallel()

	// A grant of exactly one pass is valid: the fence refuses only below
	// one (mutation gate: settle_arena_pass.go:106).
	catalog, err := domain.NewCatalog(1, []domain.Product{
		mustNewPassProduct(t, domain.MarketBrazil, "pass_1", 990, domain.CurrencyBRL, 1, "price_1QbrPass"),
	})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntentWithProduct(t, "cs_test_session1", domain.CheckoutIntentOpen, "pass_1")
	intents.intents[intent.ID] = intent
	lots := &fakeSettlePassLots{
		results: []application.GrantPassLotResult{{Replayed: false}},
	}
	useCase, err := application.NewSettleArenaPassUseCase(application.SettleArenaPassDependencies{
		Catalog: catalog,
		Intents: intents,
		Lots:    lots,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleArenaPassUseCase: %v", err)
	}
	sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_session1", false)
	result, err := useCase.Execute(context.Background(), application.SettleCheckoutCommand{
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("single-pass settlement rejected: %v", err)
	}
	if result.AmountCredited != 1 {
		t.Errorf("amountCredited = %d, want 1", result.AmountCredited)
	}
}
