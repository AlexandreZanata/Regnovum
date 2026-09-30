package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// P46-T05 — passagem de temporada não renova passe/grant nem apaga
// período pago.
//
// Passes keep their contracted idempotency on (account, origin,
// reference) and their immutable expiration: the same reference in a
// new season replays the original lot untouched, and a paid period is
// never rewritten by the calendar.

type replayPassLotRepo struct {
	lots map[string]*application.GrantPassLotResult
}

func (r *replayPassLotRepo) GrantPassLot(_ context.Context, request application.GrantPassLotRequest) (*application.GrantPassLotResult, error) {
	key := request.AccountID.String() + "|" + request.Origin.String() + "|" + request.Reference.String()
	if stored, ok := r.lots[key]; ok {
		replayed := *stored
		replayed.Replayed = true
		return &replayed, nil
	}
	lot, err := domain.ReconstitutePassLot(domain.LotID("lot-"+request.Reference.String()), request.AccountID, request.Origin, request.Quantity, request.Quantity.Int32(), request.ExpiresAt, request.Reference, request.GrantedAt)
	if err != nil {
		return nil, err
	}
	stored := &application.GrantPassLotResult{Lot: *lot}
	if r.lots == nil {
		r.lots = map[string]*application.GrantPassLotResult{}
	}
	r.lots[key] = stored
	return stored, nil
}

func TestSeasonPassageDoesNotRenewPassOrErasePaidPeriod(t *testing.T) {
	t.Parallel()

	repo := &replayPassLotRepo{}
	clock := &fakeClock{now: testClockInstant}
	useCase := application.NewGrantArenaPassesUseCase(repo, clock)

	periodEnd := testClockInstant.Add(30 * 24 * time.Hour)
	member := application.GrantArenaPassesCommand{
		AccountID: testAccountID, Origin: "MEMBER", Quantity: 1,
		Reference: "member:sub_1:1750000000", ExpiresAt: &periodEnd,
	}
	first, err := useCase.Execute(context.Background(), member)
	if err != nil {
		t.Fatalf("first member grant: %v", err)
	}
	if first.Replayed {
		t.Fatal("first grant replayed: it must settle")
	}

	// Same subscription period in the next season: same reference
	// replays the original lot with the original expiration, never a
	// second pass.
	second, err := useCase.Execute(context.Background(), member)
	if err != nil {
		t.Fatalf("second-season replay: %v", err)
	}
	if !second.Replayed {
		t.Fatal("second season did not replay: passage must not renew the grant")
	}
	if !second.Lot.ExpiresAt().Equal(periodEnd.UTC()) {
		t.Fatalf("replay expiration = %v, want %v: paid period never rewritten", second.Lot.ExpiresAt(), periodEnd.UTC())
	}

	// A purchased pass never expires and is never erased by the reset.
	purchase := application.GrantArenaPassesCommand{
		AccountID: testAccountID, Origin: "PURCHASE", Quantity: 2,
		Reference: "stripe:evt_purchase_season",
	}
	bought, err := useCase.Execute(context.Background(), purchase)
	if err != nil {
		t.Fatalf("purchase grant: %v", err)
	}
	if bought.Lot.ExpiresAt() != nil {
		t.Fatalf("purchase expiration = %v, want nil: paid credit stays valid without a new acceptance", bought.Lot.ExpiresAt())
	}
}
