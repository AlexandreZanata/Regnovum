package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

type fakeReconcileGateway struct {
	sessions      map[string]application.CheckoutSession
	sessionErrs   map[string]error
	subscriptions map[string]application.Subscription
	subErrs       map[string]error
	portalURL     string
}

func (g *fakeReconcileGateway) CreateCustomer(_ context.Context, _ application.CreateCustomerRequest) (application.Customer, error) {
	return application.Customer{}, errors.New("not implemented")
}

func (g *fakeReconcileGateway) CreateCheckoutSession(_ context.Context, _ application.CreateCheckoutSessionRequest) (application.CheckoutSession, error) {
	return application.CheckoutSession{}, errors.New("not implemented")
}

func (g *fakeReconcileGateway) GetCheckoutSession(_ context.Context, id domain.StripeCheckoutSessionID) (application.CheckoutSession, error) {
	if err, ok := g.sessionErrs[id.String()]; ok {
		return application.CheckoutSession{}, err
	}
	if session, ok := g.sessions[id.String()]; ok {
		return session, nil
	}
	return application.CheckoutSession{}, application.ErrPaymentGatewayRejected
}

func (g *fakeReconcileGateway) GetSubscription(_ context.Context, id domain.StripeSubscriptionID) (application.Subscription, error) {
	if err, ok := g.subErrs[id.String()]; ok {
		return application.Subscription{}, err
	}
	if sub, ok := g.subscriptions[id.String()]; ok {
		return sub, nil
	}
	return application.Subscription{}, application.ErrPaymentGatewayRejected
}

func (g *fakeReconcileGateway) CreatePortalSession(_ context.Context, _ application.CreatePortalSessionRequest) (application.PortalSession, error) {
	return application.PortalSession{URL: g.portalURL}, nil
}

type fakeReconcileStore struct {
	intents  []application.CheckoutIntentRecord
	subs     []application.SubscriptionRecord
	events   []application.WebhookEventRecord
	runs     int
	runID    string
	findings []application.RecordFindingRequest
	finished []struct {
		scanned  int
		findings int
		failed   bool
	}
}

func (s *fakeReconcileStore) ListIntents(_ context.Context, _, _ time.Time, _ bool) ([]application.CheckoutIntentRecord, error) {
	return s.intents, nil
}

func (s *fakeReconcileStore) ListSubscriptions(_ context.Context, _, _ time.Time, _ bool) ([]application.SubscriptionRecord, error) {
	return s.subs, nil
}

func (s *fakeReconcileStore) ListUnprocessedEvents(_ context.Context, _, _ time.Time, _ bool) ([]application.WebhookEventRecord, error) {
	return s.events, nil
}

func (s *fakeReconcileStore) CreateRun(_ context.Context, _ bool, _, _ time.Time) (string, error) {
	s.runs++
	s.runID = "run-reconcile-1"
	return s.runID, nil
}

func (s *fakeReconcileStore) RecordFinding(_ context.Context, req application.RecordFindingRequest) error {
	s.findings = append(s.findings, req)
	return nil
}

func (s *fakeReconcileStore) FinishRun(_ context.Context, _ string, scanned, findings int, failed bool) error {
	s.finished = append(s.finished, struct {
		scanned  int
		findings int
		failed   bool
	}{scanned, findings, failed})
	return nil
}

func reconcileIntentFixture(t *testing.T, session, status string, amount int64, currency domain.Currency) application.CheckoutIntentRecord {
	t.Helper()
	sid, err := domain.ParseStripeCheckoutSessionID(session, false)
	if err != nil {
		t.Fatalf("ParseStripeCheckoutSessionID: %v", err)
	}
	money, err := domain.NewMoney(amount, currency)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	intentStatus, err := domain.ParseCheckoutIntentStatus(status)
	if err != nil {
		t.Fatalf("ParseCheckoutIntentStatus: %v", err)
	}
	productID, _ := domain.ParseProductID("ink_10000")
	return application.CheckoutIntentRecord{
		ID:             "intent-" + session,
		AccountID:      "018f6b2a-0000-7000-8000-0000000000cc",
		Market:         domain.MarketBrazil,
		ProductID:      productID,
		CatalogVersion: 1,
		Amount:         money,
		Livemode:       false,
		Status:         intentStatus,
		SessionID:      sid,
		CreatedAt:      time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}
}

func reconcileWindowFixture(t *testing.T) domain.ReconciliationWindow {
	t.Helper()
	window, err := domain.NewReconciliationWindow(
		time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("NewReconciliationWindow: %v", err)
	}
	return window
}

func newReconcileUseCase(gateway *fakeReconcileGateway, store *fakeReconcileStore) *application.ReconcileBillingWindowUseCase {
	uc, err := application.NewReconcileBillingWindowUseCase(application.ReconcileBillingDependencies{
		Gateway: gateway,
		Intents: &fakeRefundIntents{intents: map[string]*application.CheckoutIntentRecord{}},
		Subs:    &fakeSubscriptionRepository{subs: map[string]*application.SubscriptionRecord{}},
		Events:  newFakeWebhookEventRepository(),
		Runs:    store,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		panic(err)
	}
	return uc
}

func TestReconcileDetectsMissingRemote(t *testing.T) {
	t.Parallel()

	gateway := &fakeReconcileGateway{
		sessionErrs: map[string]error{"cs_test_missing1": application.ErrPaymentGatewayRejected},
	}
	store := &fakeReconcileStore{
		intents: []application.CheckoutIntentRecord{reconcileIntentFixture(t, "cs_test_missing1", "paid", 990, domain.CurrencyBRL)},
	}
	uc := newReconcileUseCase(gateway, store)

	result, err := uc.Execute(context.Background(), application.ReconcileBillingWindowCommand{Window: reconcileWindowFixture(t)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Scanned != 1 || result.Findings != 1 {
		t.Fatalf("result = %+v, want 1 scanned 1 finding", result)
	}
	if store.findings[0].Kind != domain.ReconciliationMissingRemote {
		t.Fatalf("kind = %q, want missing_remote", store.findings[0].Kind)
	}
	if strings.Contains(store.findings[0].Reference, "cus_") || strings.Contains(store.findings[0].Reference, "sk_") {
		t.Fatalf("finding reference leaks provider secret shape: %q", store.findings[0].Reference)
	}
}

func TestReconcileDetectsAmountAndCurrencyMismatch(t *testing.T) {
	t.Parallel()

	sessionID := "cs_test_mismatch1"
	sid, _ := domain.ParseStripeCheckoutSessionID(sessionID, false)
	gateway := &fakeReconcileGateway{
		sessions: map[string]application.CheckoutSession{
			sessionID: {
				ID:            sid,
				Status:        domain.CheckoutStatusComplete,
				PaymentStatus: domain.CheckoutPaymentPaid,
				Mode:          domain.CheckoutModePayment,
				AmountMinor:   1990,
				Currency:      domain.CurrencyUSD,
			},
		},
	}
	store := &fakeReconcileStore{
		intents: []application.CheckoutIntentRecord{reconcileIntentFixture(t, sessionID, "paid", 990, domain.CurrencyBRL)},
	}
	uc := newReconcileUseCase(gateway, store)

	result, err := uc.Execute(context.Background(), application.ReconcileBillingWindowCommand{Window: reconcileWindowFixture(t)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Findings != 2 {
		t.Fatalf("findings = %d, want amount + currency", result.Findings)
	}
	kinds := map[domain.ReconciliationKind]bool{}
	for _, finding := range store.findings {
		kinds[finding.Kind] = true
	}
	if !kinds[domain.ReconciliationAmountMismatch] || !kinds[domain.ReconciliationCurrencyMismatch] {
		t.Fatalf("kinds = %v, want amount and currency", kinds)
	}
}

func TestReconcileDetectsOutOfOrderStatus(t *testing.T) {
	t.Parallel()

	sessionID := "cs_test_outoforder1"
	sid, _ := domain.ParseStripeCheckoutSessionID(sessionID, false)
	gateway := &fakeReconcileGateway{
		sessions: map[string]application.CheckoutSession{
			sessionID: {
				ID:            sid,
				Status:        domain.CheckoutStatusComplete,
				PaymentStatus: domain.CheckoutPaymentUnpaid,
				Mode:          domain.CheckoutModePayment,
				AmountMinor:   990,
				Currency:      domain.CurrencyBRL,
			},
		},
	}
	store := &fakeReconcileStore{
		intents: []application.CheckoutIntentRecord{reconcileIntentFixture(t, sessionID, "paid", 990, domain.CurrencyBRL)},
	}
	uc := newReconcileUseCase(gateway, store)

	result, err := uc.Execute(context.Background(), application.ReconcileBillingWindowCommand{Window: reconcileWindowFixture(t)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Findings != 1 || store.findings[0].Kind != domain.ReconciliationStatusMismatch {
		t.Fatalf("result = %+v findings %+v, want one status_mismatch", result, store.findings)
	}
}

func TestReconcileRecordsUnprocessedEventsWithoutCorrecting(t *testing.T) {
	t.Parallel()

	eventID, _ := domain.ParseWebhookEventID("evt_unprocessed1")
	eventType, _ := domain.ParseWebhookEventType("checkout.session.completed")
	gateway := &fakeReconcileGateway{}
	store := &fakeReconcileStore{
		events: []application.WebhookEventRecord{{
			ID:        "local-1",
			EventID:   eventID,
			EventType: eventType,
			Status:    application.WebhookEventFailed,
		}},
	}
	uc := newReconcileUseCase(gateway, store)

	beforeIntents := len(store.intents)
	result, err := uc.Execute(context.Background(), application.ReconcileBillingWindowCommand{Window: reconcileWindowFixture(t)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Scanned != 1 || result.Findings != 1 {
		t.Fatalf("result = %+v, want 1 scanned 1 finding", result)
	}
	if store.findings[0].Kind != domain.ReconciliationUnprocessedEvent {
		t.Fatalf("kind = %q, want unprocessed_event", store.findings[0].Kind)
	}
	if len(store.intents) != beforeIntents {
		t.Fatal("reconciliation must never mutate local mirrors")
	}
	if len(store.finished) != 1 || store.finished[0].failed {
		t.Fatalf("run not finished cleanly: %+v", store.finished)
	}
}

func TestReconcileRequiresCoherentConfiguration(t *testing.T) {
	t.Parallel()

	base := application.ReconcileBillingDependencies{
		Gateway: &fakeReconcileGateway{},
		Intents: &fakeRefundIntents{intents: map[string]*application.CheckoutIntentRecord{}},
		Subs:    &fakeSubscriptionRepository{subs: map[string]*application.SubscriptionRecord{}},
		Events:  newFakeWebhookEventRepository(),
		Runs:    &fakeReconcileStore{},
		Clock:   &fakeClock{},
	}
	mutations := []func(*application.ReconcileBillingDependencies){
		func(d *application.ReconcileBillingDependencies) { d.Gateway = nil },
		func(d *application.ReconcileBillingDependencies) { d.Intents = nil },
		func(d *application.ReconcileBillingDependencies) { d.Subs = nil },
		func(d *application.ReconcileBillingDependencies) { d.Events = nil },
		func(d *application.ReconcileBillingDependencies) { d.Runs = nil },
		func(d *application.ReconcileBillingDependencies) { d.Clock = nil },
	}
	for i, mutate := range mutations {
		deps := base
		mutate(&deps)
		if _, err := application.NewReconcileBillingWindowUseCase(deps); err == nil {
			t.Fatalf("mutation %d must refuse composition", i)
		}
	}
}
