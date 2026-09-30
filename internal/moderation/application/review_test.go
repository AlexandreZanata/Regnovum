package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

type fakeCases struct {
	mu      sync.Mutex
	records map[string]*application.CaseRecord
	claims  int
	decides int
}

func newFakeCases() *fakeCases {
	return &fakeCases{records: make(map[string]*application.CaseRecord)}
}

func (f *fakeCases) GetCase(_ context.Context, caseID string) (*application.CaseRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.records[caseID], nil
}

func (f *fakeCases) ClaimCase(_ context.Context, request application.ClaimCaseRequest) (*application.CaseRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.records[request.CaseID]
	if !ok {
		return nil, application.ErrCaseNotFound
	}
	now := request.ClaimedAt
	if record.Status == application.CaseOpen {
		f.claims++
		lease := now.Add(request.LeaseDays)
		updated := *record
		updated.Status = application.CaseUnderReview
		updated.ClaimedBy = request.Actor
		updated.LeaseExpiresAt = &lease
		f.records[request.CaseID] = &updated
		return &updated, nil
	}
	if record.Status == application.CaseUnderReview && record.LeaseExpiresAt != nil && !record.LeaseExpiresAt.After(now) {
		f.claims++
		lease := now.Add(request.LeaseDays)
		updated := *record
		updated.ClaimedBy = request.Actor
		updated.LeaseExpiresAt = &lease
		f.records[request.CaseID] = &updated
		return &updated, nil
	}
	if record.Status == application.CaseUnderReview && record.ClaimedBy == request.Actor {
		return record, nil
	}
	return nil, application.ErrCaseAlreadyClaimed
}

func (f *fakeCases) DecideCase(_ context.Context, request application.DecideCaseRequest) (*application.DecisionRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.records[request.CaseID]
	if !ok {
		return nil, application.ErrCaseNotFound
	}
	if record.Status != application.CaseUnderReview {
		return nil, application.ErrInvalidCaseTransition
	}
	if record.ClaimedBy != request.Actor {
		return nil, application.ErrCaseAlreadyClaimed
	}
	if record.LeaseExpiresAt == nil || !record.LeaseExpiresAt.After(request.DecidedAt) {
		return nil, application.ErrLeaseExpired
	}
	f.decides++
	updated := *record
	updated.Status = application.CaseDecided
	updated.ClaimedBy = ""
	updated.LeaseExpiresAt = nil
	f.records[request.CaseID] = &updated
	return &application.DecisionRecord{ActionID: "action-test-1", CaseID: request.CaseID, Actor: request.Actor, Action: request.Action}, nil
}

const (
	reviewModerator  = domain.AccountID("018f6b2a-0000-7000-8000-000000000061")
	reviewModerator2 = domain.AccountID("018f6b2a-0000-7000-8000-000000000062")
	reviewOwner      = domain.AccountID("018f6b2a-0000-7000-8000-000000000063")
)

func reviewAuthorizer() *application.Authorizer {
	roles := &fakeRoles{assignments: map[domain.AccountID]*application.RoleAssignment{
		reviewModerator:  {AccountID: reviewModerator, Role: domain.RoleModerator},
		reviewModerator2: {AccountID: reviewModerator2, Role: domain.RoleModerator},
	}}
	authorizer, err := application.NewAuthorizer(roles, &fakeClock{})
	if err != nil {
		panic(err)
	}
	return authorizer
}

func reviewCaseFixture(status application.CaseStatus) *application.CaseRecord {
	return &application.CaseRecord{
		ID:          "case-review-1",
		Target:      domain.TargetArena,
		TargetID:    "018f6b2a-0000-7000-8000-000000000071",
		TargetOwner: reviewOwner,
		Status:      status,
	}
}

func TestClaimConcurrentModerators(t *testing.T) {
	t.Parallel()

	cases := newFakeCases()
	cases.records["case-review-1"] = reviewCaseFixture(application.CaseOpen)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	claimUC, err := application.NewClaimCaseUseCase(application.ReviewDependencies{
		Cases:      cases,
		Authorizer: reviewAuthorizer(),
		Clock:      clock,
	})
	if err != nil {
		t.Fatalf("NewClaimCaseUseCase: %v", err)
	}

	const racers = 8
	wins := make([]*application.CaseRecord, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			actor := reviewModerator
			if index%2 == 1 {
				actor = reviewModerator2
			}
			record, err := claimUC.Execute(context.Background(), application.ClaimCaseCommand{
				CaseID: "case-review-1",
				Actor:  string(actor),
			})
			wins[index], errs[index] = record, err
		}(i)
	}
	wg.Wait()

	succeeded := 0
	denied := 0
	for i, err := range errs {
		if err == nil {
			succeeded++
			if wins[i].Status != application.CaseUnderReview || wins[i].ClaimedBy.IsZero() {
				t.Fatalf("winner %d record = %+v, want claimed review", i, wins[i])
			}
		} else if errors.Is(err, application.ErrCaseAlreadyClaimed) {
			denied++
		} else {
			t.Fatalf("racer %d error = %v, want nil or ErrCaseAlreadyClaimed", i, err)
		}
	}
	// The racers alternate two moderators: the four racers sharing the
	// winner's identity succeed (one fresh claim, three idempotent
	// replays), and the other four deny on the live lease.
	if succeeded != racers/2 || denied != racers/2 {
		t.Fatalf("succeeded = %d denied = %d, want %d/%d", succeeded, denied, racers/2, racers/2)
	}
	if cases.claims != 1 {
		t.Fatalf("claims = %d, want exactly 1 persisted claim", cases.claims)
	}
}

func TestDecideLeaseExpiredAndInvalidTransitions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	newDecideUC := func(cases *fakeCases) *application.DecideCaseUseCase {
		uc, err := application.NewDecideCaseUseCase(application.ReviewDependencies{
			Cases:      cases,
			Authorizer: reviewAuthorizer(),
			Clock:      &fakeClock{now: now},
		})
		if err != nil {
			t.Fatalf("NewDecideCaseUseCase: %v", err)
		}
		return uc
	}

	t.Run("expired lease denies", func(t *testing.T) {
		t.Parallel()
		expired := now.Add(-time.Minute)
		cases := newFakeCases()
		cases.records["case-review-1"] = &application.CaseRecord{
			ID:             "case-review-1",
			Target:         domain.TargetArena,
			TargetID:       "018f6b2a-0000-7000-8000-000000000071",
			TargetOwner:    reviewOwner,
			Status:         application.CaseUnderReview,
			ClaimedBy:      reviewModerator,
			LeaseExpiresAt: &expired,
		}
		_, err := newDecideUC(cases).Execute(context.Background(), application.DecideCaseCommand{
			CaseID: "case-review-1", Actor: string(reviewModerator),
			Action: "warning", Rule: "MOD-2:warning", Justification: "Measured decision with scope",
		})
		if !errors.Is(err, application.ErrLeaseExpired) {
			t.Fatalf("error = %v, want ErrLeaseExpired", err)
		}
	})

	t.Run("open case denies", func(t *testing.T) {
		t.Parallel()
		cases := newFakeCases()
		cases.records["case-review-1"] = reviewCaseFixture(application.CaseOpen)
		_, err := newDecideUC(cases).Execute(context.Background(), application.DecideCaseCommand{
			CaseID: "case-review-1", Actor: string(reviewModerator),
			Action: "warning", Rule: "MOD-2:warning", Justification: "Measured decision with scope",
		})
		if !errors.Is(err, application.ErrInvalidCaseTransition) {
			t.Fatalf("error = %v, want ErrInvalidCaseTransition", err)
		}
	})

	t.Run("explicit measure passes through", func(t *testing.T) {
		t.Parallel()
		lease := now.Add(time.Minute)
		cases := newFakeCases()
		cases.records["case-review-1"] = &application.CaseRecord{
			ID:             "case-review-1",
			Target:         domain.TargetArena,
			TargetID:       "018f6b2a-0000-7000-8000-000000000071",
			TargetOwner:    reviewOwner,
			Status:         application.CaseUnderReview,
			ClaimedBy:      reviewModerator,
			LeaseExpiresAt: &lease,
		}
		result, err := newDecideUC(cases).Execute(context.Background(), application.DecideCaseCommand{
			CaseID: "case-review-1", Actor: string(reviewModerator),
			Action: "warning", Rule: "MOD-2:warning", Justification: "Least restrictive measure for the risk",
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if result.Action != domain.ActionWarning || result.ActionID == "" {
			t.Fatalf("result = %+v, want the moderator's warning untouched", result)
		}
	})
}

func TestDecideConflictDeclared(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	lease := now.Add(time.Minute)
	cases := newFakeCases()
	cases.records["case-review-1"] = &application.CaseRecord{
		ID:             "case-review-1",
		Target:         domain.TargetArena,
		TargetID:       "018f6b2a-0000-7000-8000-000000000071",
		TargetOwner:    reviewModerator,
		Status:         application.CaseUnderReview,
		ClaimedBy:      reviewModerator,
		LeaseExpiresAt: &lease,
	}
	uc, err := application.NewDecideCaseUseCase(application.ReviewDependencies{
		Cases:      cases,
		Authorizer: reviewAuthorizer(),
		Clock:      &fakeClock{now: now},
	})
	if err != nil {
		t.Fatalf("NewDecideCaseUseCase: %v", err)
	}

	_, err = uc.Execute(context.Background(), application.DecideCaseCommand{
		CaseID: "case-review-1", Actor: string(reviewModerator),
		Action: "warning", Rule: "MOD-2:warning", Justification: "Self-review attempt",
	})
	if !errors.Is(err, application.ErrConflictOfInterest) {
		t.Fatalf("error = %v, want ErrConflictOfInterest", err)
	}
	if cases.decides != 0 {
		t.Fatalf("decides = %d, want 0 (conflict records nothing)", cases.decides)
	}
}

func TestDecideTargetActionMismatchDenies(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	lease := now.Add(time.Minute)

	// An admin may ban, but never an argument: the target matrix denies
	// without touching persistence.
	adminRoles := &fakeRoles{assignments: map[domain.AccountID]*application.RoleAssignment{
		reviewModerator: {AccountID: reviewModerator, Role: domain.RoleAdmin},
	}}
	adminAuthorizer, err := application.NewAuthorizer(adminRoles, &fakeClock{})
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	cases := newFakeCases()
	cases.records["case-review-1"] = &application.CaseRecord{
		ID:             "case-review-1",
		Target:         domain.TargetArgument,
		TargetID:       "018f6b2a-0000-7000-8000-000000000071",
		TargetOwner:    reviewOwner,
		Status:         application.CaseUnderReview,
		ClaimedBy:      reviewModerator,
		LeaseExpiresAt: &lease,
	}
	adminUC, err := application.NewDecideCaseUseCase(application.ReviewDependencies{
		Cases:      cases,
		Authorizer: adminAuthorizer,
		Clock:      &fakeClock{now: now},
	})
	if err != nil {
		t.Fatalf("NewDecideCaseUseCase: %v", err)
	}
	_, err = adminUC.Execute(context.Background(), application.DecideCaseCommand{
		CaseID: "case-review-1", Actor: string(reviewModerator),
		Action: "ban", Rule: "MOD-10:ban", Justification: "Ban on an argument",
	})
	if !errors.Is(err, domain.ErrTargetActionMismatch) {
		t.Fatalf("error = %v, want ErrTargetActionMismatch", err)
	}
	if cases.decides != 0 {
		t.Fatalf("decides = %d, want 0 (mismatch records nothing)", cases.decides)
	}

	// A suspension without a future expiry denies before any write, even
	// when role and target both allow it.
	expiryCases := newFakeCases()
	expiryCases.records["case-review-1"] = &application.CaseRecord{
		ID:             "case-review-1",
		Target:         domain.TargetProfile,
		TargetID:       string(reviewOwner),
		TargetOwner:    reviewOwner,
		Status:         application.CaseUnderReview,
		ClaimedBy:      reviewModerator,
		LeaseExpiresAt: &lease,
	}
	expiryUC, err := application.NewDecideCaseUseCase(application.ReviewDependencies{
		Cases:      expiryCases,
		Authorizer: adminAuthorizer,
		Clock:      &fakeClock{now: now},
	})
	if err != nil {
		t.Fatalf("NewDecideCaseUseCase: %v", err)
	}
	_, err = expiryUC.Execute(context.Background(), application.DecideCaseCommand{
		CaseID: "case-review-1", Actor: string(reviewModerator),
		Action: "suspension", Rule: "MOD-10:suspension", Justification: "Suspension without expiry",
	})
	if !errors.Is(err, domain.ErrInvalidExpiry) {
		t.Fatalf("error = %v, want ErrInvalidExpiry", err)
	}
	if len(expiryCases.records) != 1 || expiryCases.decides != 0 {
		t.Fatalf("decides = %d, want 0 (expiry failure records nothing)", expiryCases.decides)
	}
}
