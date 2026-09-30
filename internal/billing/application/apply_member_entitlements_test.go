package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

type fakeSubscriptionRepository struct {
	subs    map[string]*application.SubscriptionRecord
	active  map[string]*application.SubscriptionRecord
	upserts []application.UpsertSubscriptionRequest
	getErr  error
	upErr   error
}

func newFakeSubscriptionRepository() *fakeSubscriptionRepository {
	return &fakeSubscriptionRepository{
		subs:   make(map[string]*application.SubscriptionRecord),
		active: make(map[string]*application.SubscriptionRecord),
	}
}

func (f *fakeSubscriptionRepository) GetSubscriptionByStripeID(_ context.Context, subID domain.StripeSubscriptionID) (*application.SubscriptionRecord, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if rec, found := f.subs[subID.String()]; found {
		return rec, nil
	}
	return nil, nil
}

func (f *fakeSubscriptionRepository) UpsertSubscription(_ context.Context, req application.UpsertSubscriptionRequest) (*application.SubscriptionRecord, error) {
	f.upserts = append(f.upserts, req)
	if f.upErr != nil {
		return nil, f.upErr
	}
	rec := &application.SubscriptionRecord{
		ID:                   "sub-rec-1",
		AccountID:            req.AccountID,
		StripeSubscriptionID: req.StripeSubscriptionID,
		Status:               req.Status,
		Livemode:             req.Livemode,
		Market:               req.Market,
		ProductID:            req.ProductID,
		CatalogVersion:       req.CatalogVersion,
		StripePriceID:        req.StripePriceID,
		CurrentPeriodStart:   req.CurrentPeriodStart,
		CurrentPeriodEnd:     req.CurrentPeriodEnd,
		CancelAtPeriodEnd:    req.CancelAtPeriodEnd,
		CanceledAt:           req.CanceledAt,
		CreatedAt:            time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:            time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	f.subs[req.StripeSubscriptionID.String()] = rec
	return rec, nil
}

func (f *fakeSubscriptionRepository) GetActiveSubscriptionByAccount(_ context.Context, accountID domain.AccountID) (*application.SubscriptionRecord, error) {
	if f.active == nil {
		return nil, nil
	}
	return f.active[accountID.String()], nil
}

type fakeCustomerDirectory struct {
	mapping map[string]domain.AccountID
	err     error
}

func newFakeCustomerDirectory() *fakeCustomerDirectory {
	return &fakeCustomerDirectory{
		mapping: make(map[string]domain.AccountID),
	}
}

func (f *fakeCustomerDirectory) StripeCustomer(_ context.Context, _ domain.AccountID) (*application.StripeCustomerRecord, error) {
	return nil, nil
}

func (f *fakeCustomerDirectory) RecordStripeCustomer(_ context.Context, _ application.RecordStripeCustomerRequest) (*application.StripeCustomerRecord, error) {
	return nil, nil
}

func (f *fakeCustomerDirectory) AccountIDByStripeCustomer(_ context.Context, customerID domain.StripeCustomerID) (domain.AccountID, error) {
	if f.err != nil {
		return "", f.err
	}
	if acc, found := f.mapping[customerID.String()]; found {
		return acc, nil
	}
	return "", application.ErrPurchaserNotFound
}

type fakeMemberInker struct {
	credits []application.InkerCreditRequest
	replay  bool
	err     error
}

func (f *fakeMemberInker) CreditPurchasedInk(_ context.Context, req application.InkerCreditRequest) (*application.InkerCreditResult, error) {
	f.credits = append(f.credits, req)
	if f.err != nil {
		return nil, f.err
	}
	return &application.InkerCreditResult{Replayed: f.replay}, nil
}

func (f *fakeMemberInker) CreditMemberInk(_ context.Context, req application.InkerCreditRequest) (*application.InkerCreditResult, error) {
	f.credits = append(f.credits, req)
	if f.err != nil {
		return nil, f.err
	}
	return &application.InkerCreditResult{Replayed: f.replay}, nil
}

type fakeMemberPassLots struct {
	grants []application.GrantPassLotRequest
	replay bool
	err    error
}

func (f *fakeMemberPassLots) GrantPassLot(_ context.Context, req application.GrantPassLotRequest) (*application.GrantPassLotResult, error) {
	f.grants = append(f.grants, req)
	if f.err != nil {
		return nil, f.err
	}
	return &application.GrantPassLotResult{Replayed: f.replay}, nil
}

func mustNewMemberProduct(t *testing.T, market domain.Market, id string, amountMinor int64, currency domain.Currency, priceID string) domain.Product {
	t.Helper()
	productID, err := domain.ParseProductID(id)
	if err != nil {
		t.Fatalf("ParseProductID: %v", err)
	}
	amount, err := domain.NewMoney(amountMinor, currency)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	grant := domain.NewMemberGrant()
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

func testMemberCatalog(t *testing.T) *domain.Catalog {
	t.Helper()
	catalog, err := domain.NewCatalog(1, []domain.Product{
		mustNewMemberProduct(t, domain.MarketBrazil, "member_monthly", 1990, domain.CurrencyBRL, "price_1QbrMember"),
		mustNewProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, 10000, "price_1QbrInk"),
	})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return catalog
}

// TestApplyMemberEntitlements_ActiveGrantsPassAnd30kInk verifies that when a
// subscription is active, exactly 1 expiring Arena Pass and 30,000 INK total
// are granted for the period (P12-T08; MONETIZATION §2.2, §3).
func TestApplyMemberEntitlements_ActiveGrantsPassAnd30kInk(t *testing.T) {
	t.Parallel()

	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user1"] = domain.AccountID("usr_test_account")
	passLots := &fakeMemberPassLots{}
	inker := &fakeMemberInker{}
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	useCase, err := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})
	if err != nil {
		t.Fatalf("NewApplyMemberEntitlementsUseCase: %v", err)
	}

	periodStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	subID, _ := domain.ParseStripeSubscriptionID("sub_member1")
	cusID, _ := domain.ParseStripeCustomerID("cus_user1")
	priceID, _ := domain.ParseStripePriceID("price_1QbrMember")

	cmd := application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              priceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
		Livemode:             false,
	}

	result, err := useCase.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !result.PassGranted || result.PassReplayed {
		t.Errorf("PassGranted = %v, PassReplayed = %v; want true, false", result.PassGranted, result.PassReplayed)
	}
	if !result.InkGranted || result.InkReplayed {
		t.Errorf("InkGranted = %v, InkReplayed = %v; want true, false", result.InkGranted, result.InkReplayed)
	}
	if result.Status != domain.SubscriptionActive {
		t.Errorf("Status = %v, want %v", result.Status, domain.SubscriptionActive)
	}

	// Verify PassLot grant request
	if len(passLots.grants) != 1 {
		t.Fatalf("expected 1 pass grant, got %d", len(passLots.grants))
	}
	grant := passLots.grants[0]
	if grant.AccountID.String() != "usr_test_account" {
		t.Errorf("grant AccountID = %q, want usr_test_account", grant.AccountID)
	}
	if grant.Origin != domain.OriginMember {
		t.Errorf("grant Origin = %v, want %v", grant.Origin, domain.OriginMember)
	}
	if grant.Quantity.Int32() != 1 {
		t.Errorf("grant Quantity = %d, want 1", grant.Quantity.Int32())
	}
	if grant.ExpiresAt == nil || !grant.ExpiresAt.Equal(periodEnd) {
		t.Errorf("grant ExpiresAt = %v, want %v", grant.ExpiresAt, periodEnd)
	}

	// Verify INK credit request: 30,000 INK total for period
	if len(inker.credits) != 1 {
		t.Fatalf("expected 1 ink credit, got %d", len(inker.credits))
	}
	inkCredit := inker.credits[0]
	if inkCredit.AccountID != "usr_test_account" {
		t.Errorf("inkCredit AccountID = %q, want usr_test_account", inkCredit.AccountID)
	}
	if inkCredit.Amount != 30000 {
		t.Errorf("inkCredit Amount = %d, want 30000 (30k total)", inkCredit.Amount)
	}
}

// TestApplyMemberEntitlements_PeriodChange verifies that when the subscription period
// advances, distinct benefits (30k INK and a pass expiring at the new period end)
// are granted for the new period.
func TestApplyMemberEntitlements_PeriodChange(t *testing.T) {
	t.Parallel()

	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user1"] = domain.AccountID("usr_test_account")
	passLots := &fakeMemberPassLots{}
	inker := &fakeMemberInker{}
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	useCase, err := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})
	if err != nil {
		t.Fatalf("NewApplyMemberEntitlementsUseCase: %v", err)
	}

	subID, _ := domain.ParseStripeSubscriptionID("sub_member1")
	cusID, _ := domain.ParseStripeCustomerID("cus_user1")
	priceID, _ := domain.ParseStripePriceID("price_1QbrMember")

	// Period 1: Sep 1 -> Oct 1
	p1Start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	p1End := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	res1, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              priceID,
		CurrentPeriodStart:   &p1Start,
		CurrentPeriodEnd:     &p1End,
	})
	if err != nil {
		t.Fatalf("period 1 Execute: %v", err)
	}
	if !res1.PassGranted || !res1.InkGranted {
		t.Fatalf("expected period 1 benefits granted")
	}

	// Period 2: Oct 1 -> Nov 1
	p2Start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p2End := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	res2, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              priceID,
		CurrentPeriodStart:   &p2Start,
		CurrentPeriodEnd:     &p2End,
	})
	if err != nil {
		t.Fatalf("period 2 Execute: %v", err)
	}
	if !res2.PassGranted || !res2.InkGranted {
		t.Fatalf("expected period 2 benefits granted")
	}

	if len(passLots.grants) != 2 {
		t.Fatalf("expected 2 pass grants, got %d", len(passLots.grants))
	}
	if passLots.grants[0].Reference.String() == passLots.grants[1].Reference.String() {
		t.Errorf("pass grant references should differ between periods: %q vs %q",
			passLots.grants[0].Reference, passLots.grants[1].Reference)
	}
	if !passLots.grants[1].ExpiresAt.Equal(p2End) {
		t.Errorf("period 2 pass expires_at = %v, want %v", passLots.grants[1].ExpiresAt, p2End)
	}

	if len(inker.credits) != 2 {
		t.Fatalf("expected 2 ink credits, got %d", len(inker.credits))
	}
	if inker.credits[0].Idempotency == inker.credits[1].Idempotency {
		t.Errorf("ink credit idempotency keys should differ: %q vs %q",
			inker.credits[0].Idempotency, inker.credits[1].Idempotency)
	}
}

// TestApplyMemberEntitlements_OutOfOrder_TerminalStatusCannotRevive verifies that
// an out-of-order non-terminal event (e.g. active) cannot revive a subscription
// that is already terminal (canceled).
func TestApplyMemberEntitlements_OutOfOrder_TerminalStatusCannotRevive(t *testing.T) {
	t.Parallel()

	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user1"] = domain.AccountID("usr_test_account")
	passLots := &fakeMemberPassLots{}
	inker := &fakeMemberInker{}
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	subID, _ := domain.ParseStripeSubscriptionID("sub_member1")
	cusID, _ := domain.ParseStripeCustomerID("cus_user1")
	priceID, _ := domain.ParseStripePriceID("price_1QbrMember")

	// Pre-populate subscription as canceled (terminal)
	canceledAt := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	subs.subs[subID.String()] = &application.SubscriptionRecord{
		ID:                   "sub-rec-canceled",
		AccountID:            domain.AccountID("usr_test_account"),
		StripeSubscriptionID: subID,
		Status:               domain.SubscriptionCanceled,
		CanceledAt:           &canceledAt,
	}

	useCase, err := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})
	if err != nil {
		t.Fatalf("NewApplyMemberEntitlementsUseCase: %v", err)
	}

	periodStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// An out-of-order active event arrives
	result, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              priceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !result.IgnoredOutOfOrder {
		t.Errorf("expected IgnoredOutOfOrder = true, got false")
	}
	if result.PassGranted || result.InkGranted {
		t.Errorf("benefits must not be granted for out-of-order event")
	}
	if subs.subs[subID.String()].Status != domain.SubscriptionCanceled {
		t.Errorf("stored status = %v, want canceled (cannot revive)", subs.subs[subID.String()].Status)
	}
	if len(passLots.grants) != 0 || len(inker.credits) != 0 {
		t.Errorf("expected 0 grants and 0 credits, got %d and %d", len(passLots.grants), len(inker.credits))
	}
}

// TestApplyMemberEntitlements_OutOfOrder_OlderPeriodDoesNotRegress verifies that
// an out-of-order event for an older period does not regress stored period
// and does not grant stale benefits.
func TestApplyMemberEntitlements_OutOfOrder_OlderPeriodDoesNotRegress(t *testing.T) {
	t.Parallel()

	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user1"] = domain.AccountID("usr_test_account")
	passLots := &fakeMemberPassLots{}
	inker := &fakeMemberInker{}
	clock := &fakeClock{now: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)}

	subID, _ := domain.ParseStripeSubscriptionID("sub_member1")
	cusID, _ := domain.ParseStripeCustomerID("cus_user1")
	priceID, _ := domain.ParseStripePriceID("price_1QbrMember")

	// Stored subscription is already in Period 2 (Oct 1 -> Nov 1)
	p2Start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p2End := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	subs.subs[subID.String()] = &application.SubscriptionRecord{
		ID:                   "sub-rec-p2",
		AccountID:            domain.AccountID("usr_test_account"),
		StripeSubscriptionID: subID,
		Status:               domain.SubscriptionActive,
		CurrentPeriodStart:   &p2Start,
		CurrentPeriodEnd:     &p2End,
	}

	useCase, _ := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})

	// An out-of-order event for Period 1 (Sep 1 -> Oct 1) arrives
	p1Start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	p1End := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	result, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              priceID,
		CurrentPeriodStart:   &p1Start,
		CurrentPeriodEnd:     &p1End,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !result.IgnoredOutOfOrder {
		t.Errorf("expected IgnoredOutOfOrder = true, got false")
	}
	if result.PassGranted || result.InkGranted {
		t.Errorf("benefits must not be granted for older period event")
	}
	if len(passLots.grants) != 0 || len(inker.credits) != 0 {
		t.Errorf("expected 0 grants, got %d passes and %d ink", len(passLots.grants), len(inker.credits))
	}
}

// TestApplyMemberEntitlements_Cancellation verifies that canceling a subscription
// updates its status to canceled, records canceled_at, and grants no benefits.
func TestApplyMemberEntitlements_Cancellation(t *testing.T) {
	t.Parallel()

	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user1"] = domain.AccountID("usr_test_account")
	passLots := &fakeMemberPassLots{}
	inker := &fakeMemberInker{}
	now := time.Date(2026, 9, 20, 15, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}

	useCase, _ := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})

	subID, _ := domain.ParseStripeSubscriptionID("sub_member1")
	cusID, _ := domain.ParseStripeCustomerID("cus_user1")
	priceID, _ := domain.ParseStripePriceID("price_1QbrMember")

	result, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionCanceled,
		PriceID:              priceID,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Status != domain.SubscriptionCanceled {
		t.Errorf("Status = %v, want canceled", result.Status)
	}
	if result.PassGranted || result.InkGranted {
		t.Errorf("canceled subscription must not grant benefits")
	}
	if len(subs.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(subs.upserts))
	}
	if subs.upserts[0].CanceledAt == nil || !subs.upserts[0].CanceledAt.Equal(now) {
		t.Errorf("CanceledAt = %v, want %v", subs.upserts[0].CanceledAt, now)
	}
}

// TestApplyMemberEntitlements_Retry verifies that replaying the exact same event
// reports Replayed = true and adds zero new benefits.
func TestApplyMemberEntitlements_Retry(t *testing.T) {
	t.Parallel()

	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user1"] = domain.AccountID("usr_test_account")
	passLots := &fakeMemberPassLots{replay: true}
	inker := &fakeMemberInker{replay: true}
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	useCase, _ := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})

	subID, _ := domain.ParseStripeSubscriptionID("sub_member1")
	cusID, _ := domain.ParseStripeCustomerID("cus_user1")
	priceID, _ := domain.ParseStripePriceID("price_1QbrMember")
	periodStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	result, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              priceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.PassGranted || !result.PassReplayed {
		t.Errorf("PassGranted = %v, PassReplayed = %v; want false, true", result.PassGranted, result.PassReplayed)
	}
	if result.InkGranted || !result.InkReplayed {
		t.Errorf("InkGranted = %v, InkReplayed = %v; want false, true", result.InkGranted, result.InkReplayed)
	}
}

// TestApplyMemberEntitlements_PastDue verifies that a past_due status updates
// the subscription state but grants zero benefits for the unpaid period.
func TestApplyMemberEntitlements_PastDue(t *testing.T) {
	t.Parallel()

	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user1"] = domain.AccountID("usr_test_account")
	passLots := &fakeMemberPassLots{}
	inker := &fakeMemberInker{}
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	useCase, _ := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})

	subID, _ := domain.ParseStripeSubscriptionID("sub_member1")
	cusID, _ := domain.ParseStripeCustomerID("cus_user1")
	priceID, _ := domain.ParseStripePriceID("price_1QbrMember")

	result, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionPastDue,
		PriceID:              priceID,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Status != domain.SubscriptionPastDue {
		t.Errorf("Status = %v, want past_due", result.Status)
	}
	if result.PassGranted || result.InkGranted {
		t.Errorf("past_due subscription must not grant benefits")
	}
	if len(passLots.grants) != 0 || len(inker.credits) != 0 {
		t.Errorf("expected 0 grants and 0 credits for past_due")
	}
}

// TestApplyMemberEntitlements_ReactivationWithoutDuplication verifies that
// when a subscription goes active -> past_due -> active within the same period,
// the reactivation does NOT grant duplicate benefits (idempotency key matches).
func TestApplyMemberEntitlements_ReactivationWithoutDuplication(t *testing.T) {
	t.Parallel()

	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user1"] = domain.AccountID("usr_test_account")

	// Store pass grants and INK credits in memory with idempotency
	grantedPasses := make(map[string]bool)
	passLots := &fakeMemberPassLots{}
	inker := &fakeMemberInker{}
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	useCase, _ := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})

	subID, _ := domain.ParseStripeSubscriptionID("sub_member1")
	cusID, _ := domain.ParseStripeCustomerID("cus_user1")
	priceID, _ := domain.ParseStripePriceID("price_1QbrMember")
	periodStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// Step 1: Active in Period 1 -> Grants benefits
	res1, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              priceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	})
	if err != nil {
		t.Fatalf("step 1 Execute: %v", err)
	}
	if !res1.PassGranted || !res1.InkGranted {
		t.Fatalf("expected benefits granted in step 1")
	}
	grantedPasses[passLots.grants[0].Reference.String()] = true

	// Step 2: Transitions to past_due in Period 1 -> No benefits
	res2, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionPastDue,
		PriceID:              priceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	})
	if err != nil {
		t.Fatalf("step 2 Execute: %v", err)
	}
	if res2.PassGranted || res2.InkGranted {
		t.Fatalf("past_due must not grant benefits in step 2")
	}

	// Configure fakes to return Replayed = true since same reference/idempotency key is used
	passLots.replay = true
	inker.replay = true

	// Step 3: Reactivates to active in the SAME Period 1 -> Replayed, no duplicate
	res3, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              priceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	})
	if err != nil {
		t.Fatalf("step 3 Execute: %v", err)
	}
	if res3.PassGranted || !res3.PassReplayed {
		t.Errorf("step 3: PassGranted = %v, PassReplayed = %v; want false, true", res3.PassGranted, res3.PassReplayed)
	}
	if res3.InkGranted || !res3.InkReplayed {
		t.Errorf("step 3: InkGranted = %v, InkReplayed = %v; want false, true", res3.InkGranted, res3.InkReplayed)
	}
}

// TestApplyMemberEntitlements_TrialingGrantsBenefits verifies that a trialing
// subscription also receives period entitlements.
func TestApplyMemberEntitlements_TrialingGrantsBenefits(t *testing.T) {
	t.Parallel()

	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user1"] = domain.AccountID("usr_test_account")
	passLots := &fakeMemberPassLots{}
	inker := &fakeMemberInker{}
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	useCase, _ := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})

	subID, _ := domain.ParseStripeSubscriptionID("sub_member1")
	cusID, _ := domain.ParseStripeCustomerID("cus_user1")
	priceID, _ := domain.ParseStripePriceID("price_1QbrMember")
	periodStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	result, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionTrialing,
		PriceID:              priceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !result.PassGranted || !result.InkGranted {
		t.Errorf("trialing subscription should receive period benefits")
	}
}

// TestApplyMemberEntitlements_ValidationsAndErrors covers failure cases:
// invalid product kind, unknown price, missing customer, invalid period, empty inputs.
func TestApplyMemberEntitlements_ValidationsAndErrors(t *testing.T) {
	t.Parallel()

	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user1"] = domain.AccountID("usr_test_account")
	passLots := &fakeMemberPassLots{}
	inker := &fakeMemberInker{}
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	useCase, _ := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})

	subID, _ := domain.ParseStripeSubscriptionID("sub_member1")
	cusID, _ := domain.ParseStripeCustomerID("cus_user1")
	memberPriceID, _ := domain.ParseStripePriceID("price_1QbrMember")
	inkPriceID, _ := domain.ParseStripePriceID("price_1QbrInk")
	unknownPriceID, _ := domain.ParseStripePriceID("price_1Qunknown")

	periodStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// Case 1: Wrong grant kind (INK instead of Member)
	_, err := useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              inkPriceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	})
	if !errors.Is(err, application.ErrMemberWrongGrantKind) {
		t.Errorf("wrong grant kind: got %v, want %v", err, application.ErrMemberWrongGrantKind)
	}

	// Case 2: Unknown price ID
	_, err = useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              unknownPriceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	})
	if !errors.Is(err, domain.ErrUnknownProduct) {
		t.Errorf("unknown price: got %v, want %v", err, domain.ErrUnknownProduct)
	}

	// Case 3: Customer not found
	unknownCus, _ := domain.ParseStripeCustomerID("cus_unknown")
	_, err = useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           unknownCus,
		Status:               domain.SubscriptionActive,
		PriceID:              memberPriceID,
		CurrentPeriodStart:   &periodStart,
		CurrentPeriodEnd:     &periodEnd,
	})
	if !errors.Is(err, application.ErrPurchaserNotFound) {
		t.Errorf("unknown customer: got %v, want %v", err, application.ErrPurchaserNotFound)
	}

	// Case 4: Inverted period (end before start)
	_, err = useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		StripeSubscriptionID: subID,
		CustomerID:           cusID,
		Status:               domain.SubscriptionActive,
		PriceID:              memberPriceID,
		CurrentPeriodStart:   &periodEnd,
		CurrentPeriodEnd:     &periodStart,
	})
	if !errors.Is(err, domain.ErrInvalidBillingPeriod) {
		t.Errorf("inverted period: got %v, want %v", err, domain.ErrInvalidBillingPeriod)
	}

	// Case 5: Empty subscription ID
	_, err = useCase.Execute(context.Background(), application.ApplyMemberEntitlementsCommand{
		CustomerID: cusID,
		Status:     domain.SubscriptionActive,
		PriceID:    memberPriceID,
	})
	if !errors.Is(err, domain.ErrInvalidStripeSubscriptionID) {
		t.Errorf("empty sub id: got %v, want %v", err, domain.ErrInvalidStripeSubscriptionID)
	}

	// Case 6: Constructor checks
	_, err = application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{})
	if !errors.Is(err, application.ErrInvalidMemberConfig) {
		t.Errorf("incomplete config: got %v, want %v", err, application.ErrInvalidMemberConfig)
	}
}
