package billingpass_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/billingpass"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

const (
	testAccountID = "018f6b2a-0000-7000-8000-000000000001"
	testArenaID   = "018f6b2a-0000-7000-8000-000000000002"
)

var testInstant = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

type fakeClock struct {
	now time.Time
}

func (c fakeClock) Now() time.Time { return c.now }

type fakeBillingConsumer struct {
	requests []billingapp.ConsumePassRequest
	result   *billingapp.ConsumePassResult
	err      error
}

func (c *fakeBillingConsumer) ConsumeArenaPass(_ context.Context, request billingapp.ConsumePassRequest) (*billingapp.ConsumePassResult, error) {
	c.requests = append(c.requests, request)
	if c.err != nil {
		return nil, c.err
	}
	return c.result, nil
}

func mustBillingLot(t *testing.T) billingdomain.PassLot {
	t.Helper()
	quantity, err := billingdomain.NewQuantity(1)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	reference, err := billingdomain.ParseReference("stripe:evt_bridge")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	lot, err := billingdomain.ReconstitutePassLot(
		billingdomain.LotID("018f6b2a-0000-7000-8000-0000000000aa"),
		billingdomain.AccountID(testAccountID),
		billingdomain.OriginPurchase,
		quantity,
		0,
		nil,
		reference,
		testInstant,
	)
	if err != nil {
		t.Fatalf("ReconstitutePassLot: %v", err)
	}
	return *lot
}

func TestBridgeConsumeArenaPass(t *testing.T) {
	consumer := &fakeBillingConsumer{result: &billingapp.ConsumePassResult{
		Lot:       mustBillingLot(t),
		Remaining: 0,
	}}
	bridge := billingpass.New(consumer, fakeClock{now: testInstant})

	result, err := bridge.ConsumeArenaPass(context.Background(), application.PassConsumption{
		AccountID: testAccountID,
		ArenaID:   testArenaID,
	})
	if err != nil {
		t.Fatalf("ConsumeArenaPass() error = %v", err)
	}
	if result.LotID != "018f6b2a-0000-7000-8000-0000000000aa" || result.Remaining != 0 || result.Replayed {
		t.Fatalf("result = %+v", result)
	}
	if len(consumer.requests) != 1 {
		t.Fatalf("billing calls = %d, want 1", len(consumer.requests))
	}
	request := consumer.requests[0]
	if request.AccountID.String() != testAccountID || request.ArenaID.String() != testArenaID {
		t.Errorf("request = %s/%s", request.AccountID, request.ArenaID)
	}
	if !request.ConsumedAt.Equal(testInstant) {
		t.Errorf("ConsumedAt = %v, want %v", request.ConsumedAt, testInstant)
	}
}

func TestBridgeTranslatesBillingErrors(t *testing.T) {
	tests := []struct {
		name    string
		billing error
		want    error
	}{
		{name: "no pass", billing: billingdomain.ErrNoPassAvailable, want: application.ErrNoPassAvailable},
		{name: "foreign consumption", billing: billingapp.ErrArenaAlreadyConsumed, want: application.ErrArenaAlreadyConsumed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			consumer := &fakeBillingConsumer{err: tc.billing}
			bridge := billingpass.New(consumer, fakeClock{now: testInstant})

			_, err := bridge.ConsumeArenaPass(context.Background(), application.PassConsumption{
				AccountID: testAccountID,
				ArenaID:   testArenaID,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	sentinel := errors.New("ledger unavailable")
	bridge := billingpass.New(&fakeBillingConsumer{err: sentinel}, fakeClock{now: testInstant})
	if _, err := bridge.ConsumeArenaPass(context.Background(), application.PassConsumption{
		AccountID: testAccountID,
		ArenaID:   testArenaID,
	}); !errors.Is(err, sentinel) {
		t.Fatalf("unexpected error not propagated: %v", err)
	}

	if _, err := bridge.ConsumeArenaPass(context.Background(), application.PassConsumption{
		AccountID: testAccountID,
		ArenaID:   "not-a-uuid",
	}); err == nil {
		t.Fatal("invalid arena id must fail")
	}
}
