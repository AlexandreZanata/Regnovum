package application_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

func TestGetSubscriptionStatusProjectsWithoutProviderIDs(t *testing.T) {
	t.Parallel()

	subs := newFakeSubscriptionRepository()
	account := domain.AccountID("018f6b2a-0000-7000-8000-0000000000dd")
	periodEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	subs.active = map[string]*application.SubscriptionRecord{
		account.String(): {
			ID:                "sub-rec-1",
			AccountID:         account,
			Status:            domain.SubscriptionActive,
			Market:            domain.MarketBrazil,
			ProductID:         "member_monthly",
			CurrentPeriodEnd:  &periodEnd,
			CancelAtPeriodEnd: true,
		},
	}
	uc := application.NewGetSubscriptionStatusUseCase(subs)

	status, err := uc.Execute(context.Background(), account)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !status.HasSubscription || status.Status != domain.SubscriptionActive {
		t.Fatalf("status = %+v, want active", status)
	}

	// The projection must never carry provider identifiers: reflect over
	// every field and refuse stripe shapes.
	value := reflect.ValueOf(*status)
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.String {
			continue
		}
		text := field.String()
		for _, marker := range []string{"cus_", "cs_", "sub_", "pi_", "price_"} {
			if strings.Contains(text, marker) {
				t.Fatalf("subscription projection leaks provider identifier in %s: %q", value.Type().Field(i).Name, text)
			}
		}
	}

	empty, err := uc.Execute(context.Background(), domain.AccountID("018f6b2a-0000-7000-8000-0000000000ee"))
	if err != nil {
		t.Fatalf("Execute empty: %v", err)
	}
	if empty.HasSubscription {
		t.Fatalf("empty status = %+v, want no subscription", empty)
	}
}

func TestGetBillingPortalOpensAllowlistedSession(t *testing.T) {
	t.Parallel()

	customers := newFakeCustomerDirectory()
	account := domain.AccountID("018f6b2a-0000-7000-8000-0000000000dd")
	customerID, _ := domain.ParseStripeCustomerID("cus_portal123")
	customers.mapping = map[string]domain.AccountID{}
	storedCustomers := &fakePortalCustomers{
		record: &application.StripeCustomerRecord{AccountID: account, CustomerID: customerID},
	}
	_ = customers
	gateway := &fakeReconcileGateway{portalURL: "https://billing.example/portal/session-1"}

	uc, err := application.NewGetBillingPortalUseCase(application.PortalDependencies{
		Customers: storedCustomers,
		Gateway:   gateway,
		ReturnURL: "https://arena.example/billing/return",
	})
	if err != nil {
		t.Fatalf("NewGetBillingPortalUseCase: %v", err)
	}

	result, err := uc.Execute(context.Background(), account, "operation-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.PortalURL != "https://billing.example/portal/session-1" {
		t.Fatalf("portal URL = %q", result.PortalURL)
	}
	for _, marker := range []string{"cus_", "cs_", "sub_"} {
		if strings.Contains(result.PortalURL, marker) {
			t.Fatalf("portal URL leaks identifier %q: %q", marker, result.PortalURL)
		}
	}

	_, err = uc.Execute(context.Background(), domain.AccountID("018f6b2a-0000-7000-8000-0000000000ee"), "operation-1")
	if err == nil {
		t.Fatal("account without customer must fail")
	}
}

type fakePortalCustomers struct {
	record *application.StripeCustomerRecord
	err    error
}

func (f *fakePortalCustomers) StripeCustomer(_ context.Context, accountID domain.AccountID) (*application.StripeCustomerRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.record != nil && f.record.AccountID == accountID {
		return f.record, nil
	}
	return nil, nil
}

func (f *fakePortalCustomers) RecordStripeCustomer(_ context.Context, _ application.RecordStripeCustomerRequest) (*application.StripeCustomerRecord, error) {
	return f.record, nil
}

func (f *fakePortalCustomers) AccountIDByStripeCustomer(_ context.Context, _ domain.StripeCustomerID) (domain.AccountID, error) {
	return "", nil
}

func TestGetBillingPortalKeyBoundaryLength(t *testing.T) {
	t.Parallel()

	// The derived portal key ("portal:" + account + ":" + token) is
	// accepted at exactly the provider bound and refused past it
	// (mutation gate: get_billing_portal.go:65).
	account := domain.AccountID(strings.Repeat("a", 47))
	customerID, _ := domain.ParseStripeCustomerID("cus_portal123")
	storedCustomers := &fakePortalCustomers{
		record: &application.StripeCustomerRecord{AccountID: account, CustomerID: customerID},
	}
	gateway := &fakeReconcileGateway{portalURL: "https://billing.example/portal/session-1"}
	uc, err := application.NewGetBillingPortalUseCase(application.PortalDependencies{
		Customers: storedCustomers,
		Gateway:   gateway,
		ReturnURL: "https://arena.example/billing/return",
	})
	if err != nil {
		t.Fatalf("NewGetBillingPortalUseCase: %v", err)
	}
	key200 := strings.Repeat("k", 200)
	if len("portal:"+account.String()+":"+key200) != 255 {
		t.Fatalf("fixture key length = %d, want exactly 255", len("portal:"+account.String()+":"+key200))
	}
	if _, err := uc.Execute(context.Background(), account, key200); err != nil {
		t.Fatalf("255-byte portal key rejected: %v", err)
	}
	longAccount := domain.AccountID(strings.Repeat("a", 48))
	storedCustomers.record = &application.StripeCustomerRecord{AccountID: longAccount, CustomerID: customerID}
	if _, err := uc.Execute(context.Background(), longAccount, key200); !errors.Is(err, domain.ErrIdempotencyKeyTooLong) {
		t.Fatalf("256-byte portal key error = %v, want ErrIdempotencyKeyTooLong", err)
	}
}
