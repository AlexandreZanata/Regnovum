package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func TestRepository_AccountIDByStripeCustomer(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)
	repo := postgres.NewRepository(pool)

	account := mustBillingAccount(t, ctx, q, "stripe-customer-lookup@arena.example.com")
	accountID := domain.AccountID(uuidString(account.ID))
	customerID, err := domain.ParseStripeCustomerID("cus_lookup123")
	if err != nil {
		t.Fatalf("ParseStripeCustomerID: %v", err)
	}

	// Not found initially
	_, err = repo.AccountIDByStripeCustomer(ctx, customerID)
	if !errors.Is(err, application.ErrPurchaserNotFound) {
		t.Fatalf("expected ErrPurchaserNotFound, got %v", err)
	}

	// Record mapping
	_, err = repo.RecordStripeCustomer(ctx, application.RecordStripeCustomerRequest{
		AccountID:  accountID,
		CustomerID: customerID,
		Livemode:   false,
	})
	if err != nil {
		t.Fatalf("RecordStripeCustomer: %v", err)
	}

	// Now found
	resolved, err := repo.AccountIDByStripeCustomer(ctx, customerID)
	if err != nil {
		t.Fatalf("AccountIDByStripeCustomer: %v", err)
	}
	if resolved != accountID {
		t.Errorf("resolved = %q, want %q", resolved, accountID)
	}
}

func TestRepository_SubscriptionLifecycleAndIdempotency(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)
	repo := postgres.NewRepository(pool)

	account := mustBillingAccount(t, ctx, q, "sub-lifecycle@arena.example.com")
	accountID := domain.AccountID(uuidString(account.ID))
	customerID, _ := domain.ParseStripeCustomerID("cus_lifecycle1")
	subID, _ := domain.ParseStripeSubscriptionID("sub_lifecycle1")
	priceID, _ := domain.ParseStripePriceID("price_1QbrMember")
	productID, _ := domain.ParseProductID("member_monthly")

	// Pre-requisite: stripe_customers FK
	_, err := repo.RecordStripeCustomer(ctx, application.RecordStripeCustomerRequest{
		AccountID:  accountID,
		CustomerID: customerID,
		Livemode:   false,
	})
	if err != nil {
		t.Fatalf("RecordStripeCustomer: %v", err)
	}

	// 1. Initial insert: active with Period 1
	p1Start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	p1End := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	inserted, err := repo.UpsertSubscription(ctx, application.UpsertSubscriptionRequest{
		AccountID:            accountID,
		StripeSubscriptionID: subID,
		Status:               domain.SubscriptionActive,
		Livemode:             false,
		Market:               domain.MarketBrazil,
		ProductID:            productID,
		CatalogVersion:       1,
		StripePriceID:        priceID,
		CurrentPeriodStart:   &p1Start,
		CurrentPeriodEnd:     &p1End,
	})
	if err != nil {
		t.Fatalf("initial UpsertSubscription: %v", err)
	}
	if inserted.Status != domain.SubscriptionActive {
		t.Errorf("inserted status = %v, want active", inserted.Status)
	}
	if inserted.AccountID != accountID {
		t.Errorf("inserted account = %v, want %v", inserted.AccountID, accountID)
	}

	// 2. Read back by Stripe ID
	readBack, err := repo.GetSubscriptionByStripeID(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubscriptionByStripeID: %v", err)
	}
	if readBack == nil {
		t.Fatal("expected subscription to exist")
	}
	if readBack.ID != inserted.ID {
		t.Errorf("readBack ID = %q, want %q", readBack.ID, inserted.ID)
	}
	if !readBack.CurrentPeriodStart.Equal(p1Start) {
		t.Errorf("period start = %v, want %v", readBack.CurrentPeriodStart, p1Start)
	}

	// 3. Active subscription lookup by account
	activeSub, err := repo.GetActiveSubscriptionByAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("GetActiveSubscriptionByAccount: %v", err)
	}
	if activeSub == nil || activeSub.ID != inserted.ID {
		t.Fatalf("expected active subscription %q, got %v", inserted.ID, activeSub)
	}

	// 4. Update: Period change to Period 2
	p2Start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p2End := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	updatedP2, err := repo.UpsertSubscription(ctx, application.UpsertSubscriptionRequest{
		AccountID:            accountID,
		StripeSubscriptionID: subID,
		Status:               domain.SubscriptionActive,
		Livemode:             false,
		Market:               domain.MarketBrazil,
		ProductID:            productID,
		CatalogVersion:       1,
		StripePriceID:        priceID,
		CurrentPeriodStart:   &p2Start,
		CurrentPeriodEnd:     &p2End,
	})
	if err != nil {
		t.Fatalf("update to period 2: %v", err)
	}
	if !updatedP2.CurrentPeriodStart.Equal(p2Start) {
		t.Errorf("updated period start = %v, want %v", updatedP2.CurrentPeriodStart, p2Start)
	}

	// 5. Update: Status transition to past_due
	updatedPastDue, err := repo.UpsertSubscription(ctx, application.UpsertSubscriptionRequest{
		AccountID:            accountID,
		StripeSubscriptionID: subID,
		Status:               domain.SubscriptionPastDue,
		Livemode:             false,
		Market:               domain.MarketBrazil,
		ProductID:            productID,
		CatalogVersion:       1,
		StripePriceID:        priceID,
		CurrentPeriodStart:   &p2Start,
		CurrentPeriodEnd:     &p2End,
	})
	if err != nil {
		t.Fatalf("update to past_due: %v", err)
	}
	if updatedPastDue.Status != domain.SubscriptionPastDue {
		t.Errorf("status = %v, want past_due", updatedPastDue.Status)
	}

	// 6. Update: Status transition to canceled
	canceledAt := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	updatedCanceled, err := repo.UpsertSubscription(ctx, application.UpsertSubscriptionRequest{
		AccountID:            accountID,
		StripeSubscriptionID: subID,
		Status:               domain.SubscriptionCanceled,
		Livemode:             false,
		Market:               domain.MarketBrazil,
		ProductID:            productID,
		CatalogVersion:       1,
		StripePriceID:        priceID,
		CurrentPeriodStart:   &p2Start,
		CurrentPeriodEnd:     &p2End,
		CanceledAt:           &canceledAt,
	})
	if err != nil {
		t.Fatalf("update to canceled: %v", err)
	}
	if updatedCanceled.Status != domain.SubscriptionCanceled {
		t.Errorf("status = %v, want canceled", updatedCanceled.Status)
	}
	if updatedCanceled.CanceledAt == nil || !updatedCanceled.CanceledAt.Equal(canceledAt) {
		t.Errorf("canceled_at = %v, want %v", updatedCanceled.CanceledAt, canceledAt)
	}

	// 7. Active subscription lookup now returns nil
	activeAfterCancel, err := repo.GetActiveSubscriptionByAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("GetActiveSubscriptionByAccount after cancel: %v", err)
	}
	if activeAfterCancel != nil {
		t.Errorf("expected nil active subscription after cancel, got %v", activeAfterCancel)
	}

	// Exactly 1 row in app.subscriptions for this subID
	count := countRows(t, ctx, pool, "app.subscriptions", account.ID.String())
	if count != 1 {
		t.Errorf("subscriptions count for account = %d, want 1", count)
	}
}
