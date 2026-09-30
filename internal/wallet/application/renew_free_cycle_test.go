package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

type fakeFreeCycleRepo struct {
	anchor    time.Time
	anchorErr error

	requests []application.FreeCycleRenewalRequest
	renewErr error
}

func (r *fakeFreeCycleRepo) FreeCycleAnchor(_ context.Context, _ domain.AccountID) (time.Time, error) {
	if r.anchorErr != nil {
		return time.Time{}, r.anchorErr
	}
	return r.anchor, nil
}

func (r *fakeFreeCycleRepo) RenewFreePeriod(_ context.Context, request application.FreeCycleRenewalRequest) (*application.PeriodRenewal, error) {
	r.requests = append(r.requests, request)
	if r.renewErr != nil {
		return nil, r.renewErr
	}
	return &application.PeriodRenewal{
		PeriodStart: request.PeriodStart,
		Expired:     domain.Ink{},
		Granted:     request.Franchise,
	}, nil
}

func TestRenewFreeCycleUseCaseActivationPeriod(t *testing.T) {
	now := testClockInstant
	repo := &fakeFreeCycleRepo{anchor: now.AddDate(0, 0, -10)}
	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), &fakeClock{now: now})

	result, err := useCase.Execute(context.Background(), application.RenewFreeCycleCommand{AccountID: testAccountID})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.Renewals) != 0 {
		t.Fatalf("renewals = %+v, want none during the activation period", result.Renewals)
	}
	if !result.CurrentPeriodStart.Equal(repo.anchor) {
		t.Errorf("CurrentPeriodStart = %v, want the anchor %v", result.CurrentPeriodStart, repo.anchor)
	}
	if len(repo.requests) != 0 {
		t.Fatalf("repository called %d times during the activation period", len(repo.requests))
	}
}

func TestRenewFreeCycleUseCaseNormalTurnAndKeys(t *testing.T) {
	now := testClockInstant
	anchor := now.AddDate(0, -1, -5) // mid-way into the second period
	repo := &fakeFreeCycleRepo{anchor: anchor}
	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), &fakeClock{now: now})

	result, err := useCase.Execute(context.Background(), application.RenewFreeCycleCommand{AccountID: testAccountID})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.Renewals) != 1 {
		t.Fatalf("renewals = %d, want 1", len(result.Renewals))
	}
	if len(repo.requests) != 1 {
		t.Fatalf("repository calls = %d, want 1", len(repo.requests))
	}

	request := repo.requests[0]
	period := domain.PeriodAt(anchor, 1)
	if !request.PeriodStart.Equal(period.Start()) {
		t.Errorf("PeriodStart = %v, want %v", request.PeriodStart, period.Start())
	}
	if request.Franchise.Int64() != 5000 {
		t.Errorf("Franchise = %d, want 5000", request.Franchise.Int64())
	}
	if !request.ChangedAt.Equal(now) {
		t.Errorf("ChangedAt = %v, want %v", request.ChangedAt, now)
	}
	stamp := period.Start().UTC().Format(time.RFC3339)
	if want := "free:" + testAccountID + ":" + stamp; request.Reference.String() != want {
		t.Errorf("Reference = %q, want %q", request.Reference, want)
	}
	if want := "free-cycle:" + testAccountID + ":" + stamp + ":expire"; request.ExpireKey.String() != want {
		t.Errorf("ExpireKey = %q, want %q", request.ExpireKey, want)
	}
	if want := "free-cycle:" + testAccountID + ":" + stamp + ":grant"; request.GrantKey.String() != want {
		t.Errorf("GrantKey = %q, want %q", request.GrantKey, want)
	}
	if request.ExpireKey.Equals(request.GrantKey) {
		t.Error("expire and grant keys must differ")
	}
}

func TestRenewFreeCycleUseCaseMultiplePeriodsInOrder(t *testing.T) {
	now := testClockInstant
	anchor := now.AddDate(0, -3, -5) // three full turns elapsed
	repo := &fakeFreeCycleRepo{anchor: anchor}
	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), &fakeClock{now: now})

	result, err := useCase.Execute(context.Background(), application.RenewFreeCycleCommand{AccountID: testAccountID})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.Renewals) != 3 {
		t.Fatalf("renewals = %d, want 3", len(result.Renewals))
	}
	if len(repo.requests) != 3 {
		t.Fatalf("repository calls = %d, want 3", len(repo.requests))
	}
	for i := 1; i < len(repo.requests); i++ {
		if !repo.requests[i].PeriodStart.After(repo.requests[i-1].PeriodStart) {
			t.Fatalf("requests are not in ascending period order: %v then %v",
				repo.requests[i-1].PeriodStart, repo.requests[i].PeriodStart)
		}
	}
	for i, request := range repo.requests {
		if !request.PeriodStart.Equal(domain.PeriodAt(anchor, int64(i+1)).Start()) {
			t.Errorf("request %d period = %v, want period %d", i, request.PeriodStart, i+1)
		}
	}
}

func TestRenewFreeCycleUseCaseValidationsAndErrors(t *testing.T) {
	now := testClockInstant
	repo := &fakeFreeCycleRepo{anchor: now.AddDate(0, -1, -5)}
	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), &fakeClock{now: now})

	if _, err := useCase.Execute(context.Background(), application.RenewFreeCycleCommand{AccountID: ""}); !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Fatalf("empty account error = %v, want ErrEmptyAccountID", err)
	}

	repo.anchorErr = application.ErrWalletNotFound
	if _, err := useCase.Execute(context.Background(), application.RenewFreeCycleCommand{AccountID: testAccountID}); !errors.Is(err, application.ErrWalletNotFound) {
		t.Fatalf("anchor error not propagated: %v", err)
	}

	repo.anchorErr = nil
	repo.renewErr = errors.New("ledger unavailable")
	if _, err := useCase.Execute(context.Background(), application.RenewFreeCycleCommand{AccountID: testAccountID}); !errors.Is(err, repo.renewErr) {
		t.Fatalf("renewal error not propagated: %v", err)
	}
}

func TestRenewFreeCycleUseCasePropagatesReplay(t *testing.T) {
	now := testClockInstant
	repo := &fakeReplayFreeCycleRepo{anchor: now.AddDate(0, -1, -5)}
	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), &fakeClock{now: now})

	result, err := useCase.Execute(context.Background(), application.RenewFreeCycleCommand{AccountID: testAccountID})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.Renewals) != 1 || !result.Renewals[0].Replayed {
		t.Fatalf("renewals = %+v, want one replayed period", result.Renewals)
	}
}

type fakeReplayFreeCycleRepo struct {
	anchor time.Time
}

func (r *fakeReplayFreeCycleRepo) FreeCycleAnchor(_ context.Context, _ domain.AccountID) (time.Time, error) {
	return r.anchor, nil
}

func (r *fakeReplayFreeCycleRepo) RenewFreePeriod(_ context.Context, request application.FreeCycleRenewalRequest) (*application.PeriodRenewal, error) {
	return &application.PeriodRenewal{PeriodStart: request.PeriodStart, Replayed: true}, nil
}
