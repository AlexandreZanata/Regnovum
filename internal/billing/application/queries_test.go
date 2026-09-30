package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

type fakePassLotQueryRepo struct {
	lots         []domain.PassLot
	expired      []domain.PassLot
	consumptions []application.PassConsumptionRecord
	listErr      error
	expireErr    error
	historyErr   error

	lastAccount domain.AccountID
	lastAt      time.Time
	lastLimit   int
	lastAfter   *application.ConsumptionPosition
}

func (r *fakePassLotQueryRepo) ListAccountPassLots(_ context.Context, accountID domain.AccountID) ([]domain.PassLot, error) {
	r.lastAccount = accountID
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.lots, nil
}

func (r *fakePassLotQueryRepo) ListExpiredPassLots(_ context.Context, at time.Time, limit int) ([]domain.PassLot, error) {
	r.lastAt = at
	r.lastLimit = limit
	if r.expireErr != nil {
		return nil, r.expireErr
	}
	return r.expired, nil
}

func (r *fakePassLotQueryRepo) ListConsumptionsPage(_ context.Context, accountID domain.AccountID, after *application.ConsumptionPosition, limit int) ([]application.PassConsumptionRecord, error) {
	r.lastAccount = accountID
	r.lastAfter = after
	r.lastLimit = limit
	if r.historyErr != nil {
		return nil, r.historyErr
	}

	start := 0
	if after != nil {
		for i, entry := range r.consumptions {
			if entry.ConsumptionID == after.ConsumptionID {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(r.consumptions) {
		end = len(r.consumptions)
	}
	page := append([]application.PassConsumptionRecord(nil), r.consumptions[start:end]...)
	return page, nil
}

func mustLot(t *testing.T, id string, origin domain.PassOrigin, quantity int32, remaining int32, expiresAt *time.Time) domain.PassLot {
	t.Helper()
	parsedQuantity, err := domain.NewQuantity(quantity)
	if err != nil {
		t.Fatalf("NewQuantity(%d): %v", quantity, err)
	}
	reference, err := domain.ParseReference("ref:" + id)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	lot, err := domain.ReconstitutePassLot(
		domain.LotID(id),
		domain.AccountID(testAccountID),
		origin,
		parsedQuantity,
		remaining,
		expiresAt,
		reference,
		testClockInstant,
	)
	if err != nil {
		t.Fatalf("ReconstitutePassLot(%s): %v", id, err)
	}
	return *lot
}

func TestGetArenaPassSummaryUseCase(t *testing.T) {
	at := testClockInstant
	expired := at.Add(-time.Hour)
	boundary := at
	soon := at.Add(time.Hour)

	repo := &fakePassLotQueryRepo{lots: []domain.PassLot{
		mustLot(t, "lot-purchase", domain.OriginPurchase, 3, 2, nil),
		mustLot(t, "lot-expired", domain.OriginMember, 3, 3, &expired),
		mustLot(t, "lot-boundary", domain.OriginMember, 1, 1, &boundary),
		mustLot(t, "lot-soon", domain.OriginMember, 1, 1, &soon),
		mustLot(t, "lot-consumed", domain.OriginPurchase, 1, 0, nil),
	}}
	useCase := application.NewGetArenaPassSummaryUseCase(repo, &fakeClock{now: at})

	summary, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !summary.CheckedAt.Equal(at) {
		t.Errorf("CheckedAt = %v, want %v", summary.CheckedAt, at)
	}
	if summary.AvailableTotal != 3 {
		t.Fatalf("AvailableTotal = %d, want 3 (expired and boundary lots excluded)", summary.AvailableTotal)
	}
	if len(summary.Lots) != 5 {
		t.Fatalf("breakdown entries = %d, want 5", len(summary.Lots))
	}

	flagged := map[string]bool{}
	for _, entry := range summary.Lots {
		flagged[entry.Lot.ID().String()] = entry.Expired
	}
	if !flagged["lot-expired"] {
		t.Error("lot past its expiration must be flagged")
	}
	if !flagged["lot-boundary"] {
		t.Error("the expiration instant itself must already be expired")
	}
	if flagged["lot-soon"] || flagged["lot-purchase"] || flagged["lot-consumed"] {
		t.Error("non-expired lots must not be flagged")
	}

	if _, err := useCase.Execute(context.Background(), domain.AccountID("")); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}

	repo.listErr = errors.New("storage unavailable")
	if _, err := useCase.Execute(context.Background(), domain.AccountID(testAccountID)); !errors.Is(err, repo.listErr) {
		t.Fatalf("repository error not propagated: %v", err)
	}
}

func TestExpireArenaPassLotsUseCase(t *testing.T) {
	at := testClockInstant
	expired := at.Add(-24 * time.Hour)
	older := at.Add(-48 * time.Hour)

	repo := &fakePassLotQueryRepo{expired: []domain.PassLot{
		mustLot(t, "lot-expired-a", domain.OriginMember, 2, 1, &expired),
		mustLot(t, "lot-expired-b", domain.OriginMember, 5, 5, &older),
	}}
	useCase := application.NewExpireArenaPassLotsUseCase(repo, &fakeClock{now: at})

	first, err := useCase.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if first.ExpiredPasses != 6 {
		t.Fatalf("ExpiredPasses = %d, want 6", first.ExpiredPasses)
	}
	if len(first.ExpiredLots) != 2 || !first.CheckedAt.Equal(at) {
		t.Fatalf("report = %+v", first)
	}
	if repo.lastLimit != application.DefaultExpiredPassLotsPageSize || !repo.lastAt.Equal(at) {
		t.Fatalf("sweep parameters = limit %d at %v", repo.lastLimit, repo.lastAt)
	}

	// The sweep is a pure derivation: a repeated run produces the same report.
	second, err := useCase.Execute(context.Background())
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if second.ExpiredPasses != first.ExpiredPasses || len(second.ExpiredLots) != len(first.ExpiredLots) {
		t.Fatalf("repeated sweep diverged: %+v vs %+v", second, first)
	}

	repo.expireErr = errors.New("storage unavailable")
	if _, err := useCase.Execute(context.Background()); !errors.Is(err, repo.expireErr) {
		t.Fatalf("repository error not propagated: %v", err)
	}
}
