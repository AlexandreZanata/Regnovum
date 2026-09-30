package wallet_test

import (
	"context"
	"testing"
	"time"

	billingadapter "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/wallet"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

type fakeWalletCredits struct {
	requests []walletapp.CreditRequest
	replay   bool
	err      error
}

func (f *fakeWalletCredits) ApplyCredit(_ context.Context, req walletapp.CreditRequest) (*walletapp.CreditResult, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	return &walletapp.CreditResult{Replayed: f.replay}, nil
}

type fakeWalletClock struct {
	now time.Time
}

func (c *fakeWalletClock) Now() time.Time {
	return c.now
}

func TestInker_CreditPurchasedInk(t *testing.T) {
	t.Parallel()

	credits := &fakeWalletCredits{}
	clock := &fakeWalletClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	inker := billingadapter.NewInker(credits, clock)

	res, err := inker.CreditPurchasedInk(context.Background(), application.InkerCreditRequest{
		AccountID:   "acc_123",
		Amount:      10000,
		Reference:   "purchase:cs_test_123",
		Idempotency: "settle:intent-123",
	})
	if err != nil {
		t.Fatalf("CreditPurchasedInk: %v", err)
	}
	if res.Replayed {
		t.Errorf("Replayed = true, want false")
	}

	if len(credits.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(credits.requests))
	}
	req := credits.requests[0]
	if req.Bucket != walletdomain.BucketPurchased {
		t.Errorf("Bucket = %v, want PURCHASED_INK", req.Bucket)
	}
	if req.OperationType != walletdomain.OperationCreditPurchase {
		t.Errorf("OperationType = %v, want credit_purchase", req.OperationType)
	}
	if req.Delta != 10000 {
		t.Errorf("Delta = %d, want 10000", req.Delta)
	}
}

func TestInker_CreditMemberInk(t *testing.T) {
	t.Parallel()

	credits := &fakeWalletCredits{}
	clock := &fakeWalletClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	inker := billingadapter.NewInker(credits, clock)

	res, err := inker.CreditMemberInk(context.Background(), application.InkerCreditRequest{
		AccountID:   "acc_456",
		Amount:      30000,
		Reference:   "member:sub_123:1788220800",
		Idempotency: "member-ink:sub_123:1788220800",
	})
	if err != nil {
		t.Fatalf("CreditMemberInk: %v", err)
	}
	if res.Replayed {
		t.Errorf("Replayed = true, want false")
	}

	if len(credits.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(credits.requests))
	}
	req := credits.requests[0]
	if req.Bucket != walletdomain.BucketFree {
		t.Errorf("Bucket = %v, want FREE_INK", req.Bucket)
	}
	if req.OperationType != walletdomain.OperationCreditMember {
		t.Errorf("OperationType = %v, want credit_member", req.OperationType)
	}
	if req.Delta != 30000 {
		t.Errorf("Delta = %d, want 30000 (30k member franchise)", req.Delta)
	}
}
