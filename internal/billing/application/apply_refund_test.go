package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

type fakeRefundIntents struct {
	intents map[string]*application.CheckoutIntentRecord
}

func newFakeRefundIntents() *fakeRefundIntents {
	return &fakeRefundIntents{intents: make(map[string]*application.CheckoutIntentRecord)}
}

func (f *fakeRefundIntents) RecordCheckoutIntent(_ context.Context, _ application.RecordCheckoutIntentRequest) (*application.RecordCheckoutIntentResult, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeRefundIntents) GetCheckoutIntentBySession(_ context.Context, sessionID domain.StripeCheckoutSessionID) (*application.CheckoutIntentRecord, error) {
	for _, intent := range f.intents {
		if intent.SessionID == sessionID {
			return intent, nil
		}
	}
	return nil, application.ErrCheckoutIntentNotFound
}

func (f *fakeRefundIntents) MarkCheckoutIntentPaid(_ context.Context, _ domain.StripeCheckoutSessionID) error {
	return nil
}

type fakeRefundLedger struct {
	balance int64
	balErr  error
	debits  []application.RefundDebitRequest
	replay  bool
	debErr  error
}

func (f *fakeRefundLedger) PurchasedBalance(_ context.Context, _ domain.AccountID) (int64, error) {
	if f.balErr != nil {
		return 0, f.balErr
	}
	return f.balance, nil
}

func (f *fakeRefundLedger) DebitPurchasedInk(_ context.Context, req application.RefundDebitRequest) (*application.RefundDebitResult, error) {
	f.debits = append(f.debits, req)
	if f.debErr != nil {
		return nil, f.debErr
	}
	return &application.RefundDebitResult{Replayed: f.replay}, nil
}

type fakeRefundPasses struct {
	lots    map[string]*application.RefundPassLot
	revokes []string
	revErr  error
}

func newFakeRefundPasses() *fakeRefundPasses {
	return &fakeRefundPasses{lots: make(map[string]*application.RefundPassLot)}
}

func (f *fakeRefundPasses) LotForRefund(_ context.Context, accountID domain.AccountID, ref domain.Reference) (*application.RefundPassLot, error) {
	key := accountID.String() + "|" + ref.String()
	if lot, ok := f.lots[key]; ok {
		return lot, nil
	}
	return &application.RefundPassLot{Found: false}, nil
}

func (f *fakeRefundPasses) RevokeRemaining(_ context.Context, lotID string) (int32, error) {
	f.revokes = append(f.revokes, lotID)
	if f.revErr != nil {
		return 0, f.revErr
	}
	for _, lot := range f.lots {
		if lot.LotID == lotID {
			revoked := lot.Remaining
			lot.Remaining = 0
			return revoked, nil
		}
	}
	return 0, nil
}

type fakeRefundJournal struct {
	byProvider map[string]*application.RefundRecord
	byIntent   map[string][]application.RefundRecord
	records    []application.RecordRefundRequest
	seq        int
}

func newFakeRefundJournal() *fakeRefundJournal {
	return &fakeRefundJournal{
		byProvider: make(map[string]*application.RefundRecord),
		byIntent:   make(map[string][]application.RefundRecord),
	}
}

func (f *fakeRefundJournal) GetRefundByProviderID(_ context.Context, id string) (*application.RefundRecord, error) {
	if rec, ok := f.byProvider[id]; ok {
		return rec, nil
	}
	return nil, nil
}

func (f *fakeRefundJournal) RecordRefund(_ context.Context, req application.RecordRefundRequest) (*application.RefundRecord, bool, error) {
	if existing, ok := f.byProvider[req.ProviderRefund]; ok {
		return existing, true, nil
	}
	f.seq++
	rec := &application.RefundRecord{
		ID:             "refund-rec-" + string(rune('0'+f.seq)),
		IntentID:       req.IntentID,
		AccountID:      req.AccountID,
		ProviderRefund: req.ProviderRefund,
		Source:         req.Source,
		Status:         req.Status,
		ChargedMinor:   req.ChargedMinor,
		RefundedMinor:  req.RefundedMinor,
		InkRevoked:     req.InkRevoked,
		PassesRevoked:  req.PassesRevoked,
		NeedsReview:    req.NeedsReview,
		ReviewReason:   req.ReviewReason,
	}
	f.byProvider[req.ProviderRefund] = rec
	f.byIntent[req.IntentID] = append(f.byIntent[req.IntentID], *rec)
	f.records = append(f.records, req)
	return rec, false, nil
}

func (f *fakeRefundJournal) ListRefundsByIntent(_ context.Context, intentID string) ([]application.RefundRecord, error) {
	return f.byIntent[intentID], nil
}

func mustRefundCatalog(t *testing.T) *domain.Catalog {
	t.Helper()
	inkGrant, err := domain.NewINKGrant(10000)
	if err != nil {
		t.Fatalf("NewINKGrant: %v", err)
	}
	passGrant, err := domain.NewArenaPassGrant(1)
	if err != nil {
		t.Fatalf("NewArenaPassGrant: %v", err)
	}
	inkProductID, _ := domain.ParseProductID("ink_10000")
	passProductID, _ := domain.ParseProductID("pass_1")
	inkAmount, _ := domain.NewMoney(990, domain.CurrencyBRL)
	passAmount, _ := domain.NewMoney(990, domain.CurrencyBRL)
	inkPrice, _ := domain.ParseStripePriceID("price_1QbrInk")
	passPrice, _ := domain.ParseStripePriceID("price_1QbrPass")
	inkProduct, err := domain.NewProduct(domain.MarketBrazil, inkProductID, inkAmount, inkGrant, inkPrice)
	if err != nil {
		t.Fatalf("NewProduct ink: %v", err)
	}
	passProduct, err := domain.NewProduct(domain.MarketBrazil, passProductID, passAmount, passGrant, passPrice)
	if err != nil {
		t.Fatalf("NewProduct pass: %v", err)
	}
	catalog, err := domain.NewCatalog(1, []domain.Product{inkProduct, passProduct})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return catalog
}

func mustRefundIntent(t *testing.T, session, product string) *application.CheckoutIntentRecord {
	t.Helper()
	sid, err := domain.ParseStripeCheckoutSessionID(session, false)
	if err != nil {
		t.Fatalf("ParseStripeCheckoutSessionID: %v", err)
	}
	amount, err := domain.NewMoney(990, domain.CurrencyBRL)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	productID, err := domain.ParseProductID(product)
	if err != nil {
		t.Fatalf("ParseProductID: %v", err)
	}
	return &application.CheckoutIntentRecord{
		ID:             "intent-" + product,
		AccountID:      "018f6b2a-0000-7000-8000-0000000000aa",
		Market:         domain.MarketBrazil,
		ProductID:      productID,
		CatalogVersion: 1,
		Amount:         amount,
		Livemode:       false,
		Status:         domain.CheckoutIntentPaid,
		SessionID:      sid,
		CreatedAt:      time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	}
}

func newRefundUseCase(t *testing.T, catalog *domain.Catalog, intents *fakeRefundIntents, ledger *fakeRefundLedger, passes *fakeRefundPasses, journal *fakeRefundJournal) *application.ApplyRefundUseCase {
	t.Helper()
	uc, err := application.NewApplyRefundUseCase(application.RefundDependencies{
		Catalog: catalog,
		Intents: intents,
		Ledger:  ledger,
		Passes:  passes,
		Journal: journal,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewApplyRefundUseCase: %v", err)
	}
	return uc
}

func refundSessionID(t *testing.T, raw string) domain.StripeCheckoutSessionID {
	t.Helper()
	sid, err := domain.ParseStripeCheckoutSessionID(raw, false)
	if err != nil {
		t.Fatalf("ParseStripeCheckoutSessionID: %v", err)
	}
	return sid
}

func TestApplyRefundFullINKUnused(t *testing.T) {
	t.Parallel()

	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_refundfull1", "ink_10000")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{balance: 10000}
	passes := newFakeRefundPasses()
	journal := newFakeRefundJournal()
	uc := newRefundUseCase(t, catalog, intents, ledger, passes, journal)

	result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_refundfull1"),
		ProviderRefundID:    "re_fullunused1",
		Source:              domain.RefundSourceRefund,
		RefundedAmountMinor: 990,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Replayed || result.NeedsReview || result.Status != domain.RefundStatusApplied {
		t.Errorf("result = %+v, want applied without review", result)
	}
	if result.InkRevoked != 10000 {
		t.Errorf("inkRevoked = %d, want 10000", result.InkRevoked)
	}
	if len(ledger.debits) != 1 || ledger.debits[0].Amount != 10000 {
		t.Fatalf("debits = %+v, want one debit of 10000", ledger.debits)
	}
	if ledger.debits[0].Idempotency.String() != "refund:re_fullunused1" {
		t.Errorf("idempotency = %q, want refund:re_fullunused1", ledger.debits[0].Idempotency)
	}
	trail, err := journal.ListRefundsByIntent(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("ListRefundsByIntent: %v", err)
	}
	if len(trail) != 1 || trail[0].InkRevoked != 10000 {
		t.Fatalf("audit trail = %+v, want one row with 10000", trail)
	}
}

func TestApplyRefundPartialINK(t *testing.T) {
	t.Parallel()

	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_refundpart1", "ink_10000")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{balance: 10000}
	passes := newFakeRefundPasses()
	journal := newFakeRefundJournal()
	uc := newRefundUseCase(t, catalog, intents, ledger, passes, journal)

	result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_refundpart1"),
		ProviderRefundID:    "re_partialink1",
		Source:              domain.RefundSourceRefund,
		RefundedAmountMinor: 495,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.InkRevoked != 5000 || result.NeedsReview {
		t.Errorf("partial result = %+v, want 5000 without review", result)
	}
	if result.ReviewReason != "" {
		t.Errorf("partial reason = %q, want empty", result.ReviewReason)
	}
	if len(ledger.debits) != 1 || ledger.debits[0].Amount != 5000 {
		t.Fatalf("debits = %+v, want 5000", ledger.debits)
	}
}

func TestApplyRefundChargebackAlwaysNeedsReview(t *testing.T) {
	t.Parallel()

	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_disputeink1", "ink_10000")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{balance: 10000}
	passes := newFakeRefundPasses()
	journal := newFakeRefundJournal()
	uc := newRefundUseCase(t, catalog, intents, ledger, passes, journal)

	result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_disputeink1"),
		ProviderRefundID:    "dp_chargeback1",
		Source:              domain.RefundSourceDispute,
		RefundedAmountMinor: 990,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.InkRevoked != 10000 || !result.NeedsReview || result.Status != domain.RefundStatusNeedsReview {
		t.Errorf("chargeback result = %+v, want 10000 with review", result)
	}
	if result.ReviewReason != application.RefundReviewChargeback {
		t.Errorf("review reason = %q, want chargeback", result.ReviewReason)
	}
}

func TestApplyRefundCapsWhenConsumedWithoutNegative(t *testing.T) {
	t.Parallel()

	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_consumed1", "ink_10000")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{balance: 7000}
	passes := newFakeRefundPasses()
	journal := newFakeRefundJournal()
	uc := newRefundUseCase(t, catalog, intents, ledger, passes, journal)

	result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_consumed1"),
		ProviderRefundID:    "re_consumedink1",
		Source:              domain.RefundSourceRefund,
		RefundedAmountMinor: 990,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.InkRevoked != 7000 || !result.NeedsReview {
		t.Errorf("consumed result = %+v, want 7000 with review", result)
	}
	if result.ReviewReason != application.RefundReviewAlreadyConsumed {
		t.Errorf("review reason = %q, want already_consumed", result.ReviewReason)
	}
	if len(ledger.debits) != 1 || ledger.debits[0].Amount != 7000 {
		t.Fatalf("debits = %+v, want capped 7000, never negative", ledger.debits)
	}
}

func TestApplyRefundIsIdempotentOnReplay(t *testing.T) {
	t.Parallel()

	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_replayref1", "ink_10000")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{balance: 10000}
	passes := newFakeRefundPasses()
	journal := newFakeRefundJournal()
	uc := newRefundUseCase(t, catalog, intents, ledger, passes, journal)

	cmd := application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_replayref1"),
		ProviderRefundID:    "re_replayrefund1",
		Source:              domain.RefundSourceRefund,
		RefundedAmountMinor: 990,
	}
	first, err := uc.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if first.Replayed {
		t.Fatal("first execution must not replay")
	}
	second, err := uc.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if !second.Replayed {
		t.Fatal("second execution must replay")
	}
	if second.InkRevoked != 10000 {
		t.Errorf("replayed ink = %d, want 10000", second.InkRevoked)
	}
	if len(ledger.debits) != 1 {
		t.Errorf("debits = %d, want 1 (no duplicate compensation)", len(ledger.debits))
	}
	if len(journal.records) != 1 {
		t.Errorf("journal records = %d, want 1", len(journal.records))
	}
}

func TestApplyRefundPassesUnusedAndConsumed(t *testing.T) {
	t.Parallel()

	t.Run("unused lot revoked without review", func(t *testing.T) {
		t.Parallel()
		catalog := mustRefundCatalog(t)
		intents := newFakeRefundIntents()
		intent := mustRefundIntent(t, "cs_test_passfull1", "pass_1")
		intents.intents[intent.ID] = intent
		ledger := &fakeRefundLedger{}
		passes := newFakeRefundPasses()
		account := domain.AccountID("018f6b2a-0000-7000-8000-0000000000aa")
		ref, _ := domain.ParseReference(intent.ID)
		passes.lots[account.String()+"|"+ref.String()] = &application.RefundPassLot{LotID: "lot-unused-1", Quantity: 1, Remaining: 1, Found: true}
		journal := newFakeRefundJournal()
		uc := newRefundUseCase(t, catalog, intents, ledger, passes, journal)

		result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
			SessionID:           refundSessionID(t, "cs_test_passfull1"),
			ProviderRefundID:    "re_passunused1",
			Source:              domain.RefundSourceRefund,
			RefundedAmountMinor: 990,
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if result.PassesRevoked != 1 || result.NeedsReview {
			t.Errorf("unused pass result = %+v, want 1 without review", result)
		}
		if result.ReviewReason != "" {
			t.Errorf("unused pass reason = %q, want empty", result.ReviewReason)
		}
		if len(passes.revokes) != 1 {
			t.Fatalf("revokes = %v, want one", passes.revokes)
		}
	})

	t.Run("consumed lot revoked with review", func(t *testing.T) {
		t.Parallel()
		catalog := mustRefundCatalog(t)
		intents := newFakeRefundIntents()
		intent := mustRefundIntent(t, "cs_test_passused1", "pass_1")
		intents.intents[intent.ID] = intent
		ledger := &fakeRefundLedger{}
		passes := newFakeRefundPasses()
		account := domain.AccountID("018f6b2a-0000-7000-8000-0000000000aa")
		ref, _ := domain.ParseReference(intent.ID)
		passes.lots[account.String()+"|"+ref.String()] = &application.RefundPassLot{LotID: "lot-used-1", Quantity: 1, Remaining: 0, Found: true}
		journal := newFakeRefundJournal()
		uc := newRefundUseCase(t, catalog, intents, ledger, passes, journal)

		result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
			SessionID:           refundSessionID(t, "cs_test_passused1"),
			ProviderRefundID:    "re_passconsumed1",
			Source:              domain.RefundSourceRefund,
			RefundedAmountMinor: 990,
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if result.PassesRevoked != 0 || !result.NeedsReview {
			t.Errorf("consumed pass result = %+v, want 0 with review", result)
		}
		if len(passes.revokes) != 0 {
			t.Errorf("revokes = %v, want none when nothing remains", passes.revokes)
		}
	})
}

func TestApplyRefundRequiresCoherentConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*application.RefundDependencies)
	}{
		{name: "no catalog", mutate: func(d *application.RefundDependencies) { d.Catalog = nil }},
		{name: "no intents", mutate: func(d *application.RefundDependencies) { d.Intents = nil }},
		{name: "no ledger", mutate: func(d *application.RefundDependencies) { d.Ledger = nil }},
		{name: "no passes", mutate: func(d *application.RefundDependencies) { d.Passes = nil }},
		{name: "no journal", mutate: func(d *application.RefundDependencies) { d.Journal = nil }},
		{name: "no clock", mutate: func(d *application.RefundDependencies) { d.Clock = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := application.RefundDependencies{
				Catalog: mustRefundCatalog(t),
				Intents: newFakeRefundIntents(),
				Ledger:  &fakeRefundLedger{},
				Passes:  newFakeRefundPasses(),
				Journal: newFakeRefundJournal(),
				Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
			}
			tc.mutate(&deps)
			if _, err := application.NewApplyRefundUseCase(deps); err == nil {
				t.Fatal("refused configuration must not build a use case")
			}
		})
	}
}

func TestApplyRefundRejectsUnsettledIntent(t *testing.T) {
	t.Parallel()

	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_openrefund1", "ink_10000")
	intent.Status = domain.CheckoutIntentOpen
	intents.intents[intent.ID] = intent
	uc := newRefundUseCase(t, catalog, intents, &fakeRefundLedger{}, newFakeRefundPasses(), newFakeRefundJournal())

	_, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_openrefund1"),
		ProviderRefundID:    "re_opentest1",
		Source:              domain.RefundSourceRefund,
		RefundedAmountMinor: 990,
	})
	if !errors.Is(err, application.ErrRefundIntentNotSettled) {
		t.Fatalf("error = %v, want ErrRefundIntentNotSettled", err)
	}
}

func TestProcessWebhookAppliesRefundEndToEnd(t *testing.T) {
	t.Parallel()

	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_webhookref1", "ink_10000")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{balance: 10000}
	passes := newFakeRefundPasses()
	journal := newFakeRefundJournal()
	refundUC, err := application.NewApplyRefundUseCase(application.RefundDependencies{
		Catalog: catalog,
		Intents: intents,
		Ledger:  ledger,
		Passes:  passes,
		Journal: journal,
		Clock:   &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewApplyRefundUseCase: %v", err)
	}

	repo := newFakeWebhookEventRepository()
	webhook, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier:      &fakeWebhookVerifier{verifyResult: nil},
		Events:        repo,
		RefundSettler: refundUC,
		Clock:         &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase: %v", err)
	}

	body := []byte(`{
		"id": "evt_refundwebhook1",
		"type": "charge.refunded",
		"livemode": false,
		"created": 1789740000,
		"data": {
			"object": {
				"id": "re_webhookrefund1",
				"checkout_session": "cs_test_webhookref1",
				"amount_refunded": 990,
				"amount": 990
			}
		}
	}`)
	cmd := application.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: "123",
	}
	if err := webhook.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("webhook Execute: %v", err)
	}
	if len(ledger.debits) != 1 || ledger.debits[0].Amount != 10000 {
		t.Fatalf("webhook debits = %+v, want one 10000 debit", ledger.debits)
	}

	if err := webhook.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("webhook replay Execute: %v", err)
	}
	if len(ledger.debits) != 1 {
		t.Errorf("webhook replay debits = %d, want 1 (event replay harmless)", len(ledger.debits))
	}
	trail, _ := journal.ListRefundsByIntent(context.Background(), intent.ID)
	if len(trail) != 1 {
		t.Fatalf("audit trail = %d, want 1", len(trail))
	}
}

func TestApplyRefundFullRefundCarriesNoReviewReason(t *testing.T) {
	t.Parallel()

	// A fully reversible refund sets no attribution: the review reason
	// stays empty when nothing needs review (mutation gate:
	// apply_refund.go:252,393).
	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_fullreason1", "ink_10000")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{balance: 10000}
	uc := newRefundUseCase(t, catalog, intents, ledger, newFakeRefundPasses(), newFakeRefundJournal())

	result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_fullreason1"),
		ProviderRefundID:    "re_fullreason1",
		Source:              domain.RefundSourceRefund,
		RefundedAmountMinor: 990,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.NeedsReview || result.ReviewReason != "" {
		t.Errorf("full refund result = %+v, want applied with empty reason", result)
	}
}

func TestApplyRefundSmallDebitAttributesConsumption(t *testing.T) {
	t.Parallel()

	// A debit below the reversible quantity attributes the consumption
	// even when the mirror arithmetic would round it away (mutation gate:
	// apply_refund.go:393).
	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_smalldebit1", "ink_10000")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{balance: 1}
	uc := newRefundUseCase(t, catalog, intents, ledger, newFakeRefundPasses(), newFakeRefundJournal())

	result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_smalldebit1"),
		ProviderRefundID:    "re_smalldebit1",
		Source:              domain.RefundSourceRefund,
		RefundedAmountMinor: 495,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.InkRevoked != 1 || !result.NeedsReview {
		t.Errorf("small debit result = %+v, want 1 with review", result)
	}
	if result.ReviewReason != application.RefundReviewAlreadyConsumed {
		t.Errorf("review reason = %q, want already_consumed", result.ReviewReason)
	}
}

func TestApplyRefundZeroDebitSkipsLedgerWrite(t *testing.T) {
	t.Parallel()

	// A zero debit writes nothing: the ledger call is guarded, not merely
	// a zero-amount write (mutation gate: apply_refund.go:255).
	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_zerodebit1", "ink_10000")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{balance: 0}
	uc := newRefundUseCase(t, catalog, intents, ledger, newFakeRefundPasses(), newFakeRefundJournal())

	result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_zerodebit1"),
		ProviderRefundID:    "re_zerodebit1",
		Source:              domain.RefundSourceRefund,
		RefundedAmountMinor: 990,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.InkRevoked != 0 {
		t.Errorf("ink revoked = %d, want 0", result.InkRevoked)
	}
	if len(ledger.debits) != 0 {
		t.Fatalf("debits = %+v, want no ledger write", ledger.debits)
	}
}

func TestApplyRefundPassDisputeKeepsChargebackReason(t *testing.T) {
	t.Parallel()

	// A pass dispute keeps the chargeback attribution through the
	// fallback: the fallback only fills an empty reason (mutation gate:
	// apply_refund.go:306,316).
	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_passdispute1", "pass_1")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{}
	passes := newFakeRefundPasses()
	account := domain.AccountID("018f6b2a-0000-7000-8000-0000000000aa")
	ref, _ := domain.ParseReference(intent.ID)
	passes.lots[account.String()+"|"+ref.String()] = &application.RefundPassLot{LotID: "lot-dispute-1", Quantity: 1, Remaining: 1, Found: true}
	uc := newRefundUseCase(t, catalog, intents, ledger, passes, newFakeRefundJournal())

	result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_passdispute1"),
		ProviderRefundID:    "dp_passdispute1",
		Source:              domain.RefundSourceDispute,
		RefundedAmountMinor: 990,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.NeedsReview || result.ReviewReason != application.RefundReviewChargeback {
		t.Errorf("dispute result = %+v, want review with chargeback", result)
	}
}

func TestApplyRefundShrunkLotAttributesConsumption(t *testing.T) {
	t.Parallel()

	// A lot holding fewer passes than its original quantity is partial
	// consumption even when the current grant covers the remainder: the
	// lot's own history decides, not the catalog (mutation gate:
	// apply_refund.go:306).
	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_shrunklot1", "pass_1")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{}
	passes := newFakeRefundPasses()
	account := domain.AccountID("018f6b2a-0000-7000-8000-0000000000aa")
	ref, _ := domain.ParseReference(intent.ID)
	passes.lots[account.String()+"|"+ref.String()] = &application.RefundPassLot{LotID: "lot-shrunk-1", Quantity: 5, Remaining: 1, Found: true}
	uc := newRefundUseCase(t, catalog, intents, ledger, passes, newFakeRefundJournal())

	result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_shrunklot1"),
		ProviderRefundID:    "re_shrunklot1",
		Source:              domain.RefundSourceRefund,
		RefundedAmountMinor: 990,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.ReviewReason != application.RefundReviewAlreadyConsumed {
		t.Errorf("shrunk lot result = %+v, want already_consumed attribution", result)
	}
	if result.PassesRevoked != 1 {
		t.Errorf("passes revoked = %d, want 1", result.PassesRevoked)
	}
}

func TestApplyRefundDisputeWithoutBalanceKeepsChargeback(t *testing.T) {
	t.Parallel()

	// A dispute with nothing to revoke still attributes the chargeback:
	// the empty-debit fallback only fills an empty reason (mutation gate:
	// apply_refund.go:275).
	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intent := mustRefundIntent(t, "cs_test_disputeempty1", "ink_10000")
	intents.intents[intent.ID] = intent
	ledger := &fakeRefundLedger{balance: 0}
	uc := newRefundUseCase(t, catalog, intents, ledger, newFakeRefundPasses(), newFakeRefundJournal())

	result, err := uc.Execute(context.Background(), application.ApplyRefundCommand{
		SessionID:           refundSessionID(t, "cs_test_disputeempty1"),
		ProviderRefundID:    "dp_emptybalance1",
		Source:              domain.RefundSourceDispute,
		RefundedAmountMinor: 990,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.NeedsReview || result.ReviewReason != application.RefundReviewChargeback {
		t.Errorf("empty dispute result = %+v, want review with chargeback", result)
	}
	if len(ledger.debits) != 0 {
		t.Fatalf("debits = %+v, want no ledger write", ledger.debits)
	}
}
