package application_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// fakeInker is a stub that records INK credit requests.
type fakeInker struct {
	credits []application.InkerCreditRequest
	results []application.InkerCreditResult
	errors  []error
	next    int
}

func (f *fakeInker) CreditPurchasedInk(_ context.Context, request application.InkerCreditRequest) (*application.InkerCreditResult, error) {
	f.credits = append(f.credits, request)
	if f.next < len(f.errors) {
		err := f.errors[f.next]
		f.next++
		return nil, err
	}
	if f.next < len(f.results) {
		result := f.results[f.next]
		f.next++
		return &result, nil
	}
	return &application.InkerCreditResult{Replayed: false}, nil
}

func (f *fakeInker) CreditMemberInk(_ context.Context, request application.InkerCreditRequest) (*application.InkerCreditResult, error) {
	f.credits = append(f.credits, request)
	if f.next < len(f.errors) {
		err := f.errors[f.next]
		f.next++
		return nil, err
	}
	if f.next < len(f.results) {
		result := f.results[f.next]
		f.next++
		return &result, nil
	}
	return &application.InkerCreditResult{Replayed: false}, nil
}

// fakeSettleCheckoutIntents is an in-memory implementation of the checkout
// intent repository for settle tests.
type fakeSettleCheckoutIntents struct {
	intents map[string]*application.CheckoutIntentRecord
}

func newFakeSettleCheckoutIntents() *fakeSettleCheckoutIntents {
	return &fakeSettleCheckoutIntents{
		intents: make(map[string]*application.CheckoutIntentRecord),
	}
}

func (f *fakeSettleCheckoutIntents) RecordCheckoutIntent(_ context.Context, request application.RecordCheckoutIntentRequest) (*application.RecordCheckoutIntentResult, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeSettleCheckoutIntents) GetCheckoutIntentBySession(_ context.Context, sessionID domain.StripeCheckoutSessionID) (*application.CheckoutIntentRecord, error) {
	for _, intent := range f.intents {
		if intent.SessionID == sessionID {
			return intent, nil
		}
	}
	return nil, application.ErrCheckoutIntentNotFound
}

func (f *fakeSettleCheckoutIntents) MarkCheckoutIntentPaid(_ context.Context, sessionID domain.StripeCheckoutSessionID) error {
	for id, intent := range f.intents {
		if intent.SessionID == sessionID {
			intent.Status = domain.CheckoutIntentPaid
			f.intents[id] = intent
			return nil
		}
	}
	return nil
}

// testSettleCatalog builds a minimal catalog with one INK product.
func testSettleCatalog(t *testing.T) *domain.Catalog {
	t.Helper()
	catalog, err := domain.NewCatalog(1, []domain.Product{
		mustNewProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, 10000, "price_1QbrInk"),
	})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return catalog
}

// mustNewProduct builds a product or fails the test.
func mustNewProduct(t *testing.T, market domain.Market, id string, amountMinor int64, currency domain.Currency, inkQuantity int64, priceID string) domain.Product {
	t.Helper()
	productID, err := domain.ParseProductID(id)
	if err != nil {
		t.Fatalf("ParseProductID: %v", err)
	}
	amount, err := domain.NewMoney(amountMinor, currency)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	grant, err := domain.NewINKGrant(inkQuantity)
	if err != nil {
		t.Fatalf("NewINKGrant: %v", err)
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

// mustNewCheckoutIntent creates a checkout intent for testing.
func mustNewCheckoutIntent(t *testing.T, sessionID string, status domain.CheckoutIntentStatus) *application.CheckoutIntentRecord {
	t.Helper()
	sid, err := domain.ParseStripeCheckoutSessionID(sessionID, false)
	if err != nil {
		t.Fatalf("ParseStripeCheckoutSessionID: %v", err)
	}
	amount, err := domain.NewMoney(990, domain.CurrencyBRL)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	return &application.CheckoutIntentRecord{
		ID:             "intent-1",
		AccountID:      "018f6b2a-0000-7000-8000-0000000000ff",
		Market:         domain.MarketBrazil,
		ProductID:      "ink_10000",
		CatalogVersion: 1,
		Amount:         amount,
		Livemode:       false,
		Status:         status,
		SessionID:      sid,
		CreatedAt:      time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	}
}

// TestSettleCheckoutGrantsINKOnSettledPayment proves the core flow: a valid
// open intent is settled, the account receives INK, and the intent is marked
// as paid.
func TestSettleCheckoutGrantsINKOnSettledPayment(t *testing.T) {
	t.Parallel()

	catalog := testSettleCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntent(t, "cs_test_session1", domain.CheckoutIntentOpen)
	intents.intents[intent.ID] = intent

	inker := &fakeInker{
		results: []application.InkerCreditResult{{Replayed: false}},
	}

	useCase, err := application.NewSettleCheckoutUseCase(application.SettleCheckoutDependencies{
		Catalog: catalog,
		Intents: intents,
		Inker:   inker,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleCheckoutUseCase: %v", err)
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
	if result.AmountCredited != 10000 {
		t.Errorf("amountCredited = %d, want 10000", result.AmountCredited)
	}

	// Verify the INK credit was requested with correct parameters.
	if len(inker.credits) != 1 {
		t.Fatalf("credits = %d, want 1", len(inker.credits))
	}
	credit := inker.credits[0]
	if credit.AccountID != "018f6b2a-0000-7000-8000-0000000000ff" {
		t.Errorf("accountID = %q", credit.AccountID)
	}
	if credit.Amount != 10000 {
		t.Errorf("amount = %d, want 10000", credit.Amount)
	}
	if credit.Idempotency != "settle:intent-1" {
		t.Errorf("idempotency = %q, want settle:intent-1", credit.Idempotency)
	}

	// Verify the intent was marked as paid.
	if intent.Status != domain.CheckoutIntentPaid {
		t.Errorf("intent status = %s, want paid", intent.Status)
	}
}

// TestSettleCheckoutIsIdempotentOnReplay proves that a replay of the same
// settlement does not grant INK twice.
func TestSettleCheckoutIsIdempotentOnReplay(t *testing.T) {
	t.Parallel()

	catalog := testSettleCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntent(t, "cs_test_session1", domain.CheckoutIntentOpen)
	intents.intents[intent.ID] = intent

	inker := &fakeInker{
		results: []application.InkerCreditResult{
			{Replayed: false},
			{Replayed: true},
		},
	}

	useCase, err := application.NewSettleCheckoutUseCase(application.SettleCheckoutDependencies{
		Catalog: catalog,
		Intents: intents,
		Inker:   inker,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleCheckoutUseCase: %v", err)
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

	// Only one INK credit should have been requested.
	if len(inker.credits) != 1 {
		t.Errorf("credits = %d, want 1", len(inker.credits))
	}
}

// TestSettleCheckoutRejectsAlreadySettledIntent proves that an already settled
// intent is treated as a replay.
func TestSettleCheckoutRejectsAlreadySettledIntent(t *testing.T) {
	t.Parallel()

	catalog := testSettleCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntent(t, "cs_test_session1", domain.CheckoutIntentPaid)
	intents.intents[intent.ID] = intent

	inker := &fakeInker{}

	useCase, err := application.NewSettleCheckoutUseCase(application.SettleCheckoutDependencies{
		Catalog: catalog,
		Intents: intents,
		Inker:   inker,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleCheckoutUseCase: %v", err)
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
	if len(inker.credits) != 0 {
		t.Errorf("credits = %d, want 0 (no INK granted)", len(inker.credits))
	}
}

// TestSettleCheckoutRejectsExpiredIntent proves that an expired intent cannot
// be settled.
func TestSettleCheckoutRejectsExpiredIntent(t *testing.T) {
	t.Parallel()

	catalog := testSettleCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntent(t, "cs_test_expired1", domain.CheckoutIntentExpired)
	intents.intents[intent.ID] = intent

	inker := &fakeInker{}

	useCase, err := application.NewSettleCheckoutUseCase(application.SettleCheckoutDependencies{
		Catalog: catalog,
		Intents: intents,
		Inker:   inker,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleCheckoutUseCase: %v", err)
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
	if len(inker.credits) != 0 {
		t.Errorf("credits = %d, want 0 (no INK granted)", len(inker.credits))
	}
}

// TestSettleCheckoutRejectsUnknownSession proves that a session not found
// locally is refused.
func TestSettleCheckoutRejectsUnknownSession(t *testing.T) {
	t.Parallel()

	catalog := testSettleCatalog(t)
	intents := newFakeSettleCheckoutIntents()
	inker := &fakeInker{}

	useCase, err := application.NewSettleCheckoutUseCase(application.SettleCheckoutDependencies{
		Catalog: catalog,
		Intents: intents,
		Inker:   inker,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleCheckoutUseCase: %v", err)
	}

	sessionID, _ := domain.ParseStripeCheckoutSessionID("cs_test_unknown1", false)
	_, err = useCase.Execute(context.Background(), application.SettleCheckoutCommand{
		SessionID: sessionID,
	})
	if !errors.Is(err, application.ErrCheckoutIntentNotFound) {
		t.Fatalf("error = %v, want ErrCheckoutIntentNotFound", err)
	}
	if len(inker.credits) != 0 {
		t.Errorf("credits = %d, want 0", len(inker.credits))
	}
}

// TestSettleCheckoutRequiresCoherentConfiguration proves the use case refuses
// incomplete composition.
func TestSettleCheckoutRequiresCoherentConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*application.SettleCheckoutDependencies)
	}{
		{name: "no catalog", mutate: func(d *application.SettleCheckoutDependencies) { d.Catalog = nil }},
		{name: "no intents", mutate: func(d *application.SettleCheckoutDependencies) { d.Intents = nil }},
		{name: "no inker", mutate: func(d *application.SettleCheckoutDependencies) { d.Inker = nil }},
		{name: "no clock", mutate: func(d *application.SettleCheckoutDependencies) { d.Clock = nil }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			deps := application.SettleCheckoutDependencies{
				Catalog: testSettleCatalog(t),
				Intents: newFakeSettleCheckoutIntents(),
				Inker:   &fakeInker{},
				Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
			}
			testCase.mutate(&deps)
			_, err := application.NewSettleCheckoutUseCase(deps)
			if err == nil {
				t.Fatal("a refused configuration must not build a use case")
			}
		})
	}
}

func TestSettleCheckoutAcceptsSingleUnitGrant(t *testing.T) {
	t.Parallel()

	// A grant of exactly one unit is valid: the fence refuses only below
	// one (mutation gate: settle_checkout.go:151).
	catalog, err := domain.NewCatalog(1, []domain.Product{
		mustNewProduct(t, domain.MarketBrazil, "ink_1", 990, domain.CurrencyBRL, 1, "price_1QbrInk"),
	})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	intents := newFakeSettleCheckoutIntents()
	intent := mustNewCheckoutIntent(t, "cs_test_session1", domain.CheckoutIntentOpen)
	intent.ProductID = "ink_1"
	intents.intents[intent.ID] = intent
	inker := &fakeInker{
		results: []application.InkerCreditResult{{Replayed: false}},
	}
	useCase, err := application.NewSettleCheckoutUseCase(application.SettleCheckoutDependencies{
		Catalog: catalog,
		Intents: intents,
		Inker:   inker,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewSettleCheckoutUseCase: %v", err)
	}
	result, err := useCase.Execute(context.Background(), application.SettleCheckoutCommand{
		SessionID: intent.SessionID,
	})
	if err != nil {
		t.Fatalf("single-unit settlement rejected: %v", err)
	}
	if result.AmountCredited != 1 {
		t.Errorf("amountCredited = %d, want 1", result.AmountCredited)
	}
}
