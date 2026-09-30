package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

type fakeAppeals struct {
	actions map[string]*application.SanctionedAction
	appeals map[string]*application.AppealRecord
	byID    map[string]*application.AppealRecord
	seq     int
}

func (f *fakeAppeals) ActionForAppeal(_ context.Context, actionID string) (*application.SanctionedAction, error) {
	return f.actions[actionID], nil
}

func (f *fakeAppeals) AppealByAction(_ context.Context, actionID string) (*application.AppealRecord, error) {
	for _, appeal := range f.appeals {
		if appeal.ActionID == actionID {
			return appeal, nil
		}
	}
	return nil, nil
}

func (f *fakeAppeals) AppealByID(_ context.Context, appealID string) (*application.AppealRecord, error) {
	return f.byID[appealID], nil
}

func (f *fakeAppeals) InsertAppeal(_ context.Context, request application.InsertAppealRequest) (*application.AppealRecord, error) {
	f.seq++
	record := &application.AppealRecord{
		ID:        "appeal-test-1",
		ActionID:  request.ActionID,
		Appellant: request.Appellant,
		Status:    application.AppealOpen,
	}
	f.appeals[request.ActionID] = record
	f.byID[record.ID] = record
	return record, nil
}

func (f *fakeAppeals) ClaimAppeal(_ context.Context, request application.ClaimAppealRequest) (*application.AppealRecord, error) {
	record, ok := f.byID[request.AppealID]
	if !ok {
		return nil, application.ErrAppealNotFound
	}
	if record.Status != application.AppealOpen {
		return nil, application.ErrAppealAlreadyClaimed
	}
	record.Status = application.AppealUnderReview
	return record, nil
}

func (f *fakeAppeals) DecideAppeal(_ context.Context, request application.DecideAppealRequest) (*application.AppealRecord, error) {
	record, ok := f.byID[request.AppealID]
	if !ok {
		return nil, application.ErrAppealNotFound
	}
	if record.Status != application.AppealUnderReview {
		return nil, application.ErrInvalidAppealTransition
	}
	switch request.Outcome {
	case domain.OutcomeUpheld:
		record.Status = application.AppealUpheld
	case domain.OutcomeModified:
		record.Status = application.AppealModified
	case domain.OutcomeReversed:
		record.Status = application.AppealReversed
	}
	now := time.Now().UTC()
	record.DecidedAt = &now
	return record, nil
}

const (
	appealOwner    = domain.AccountID("018f6b2a-0000-7000-8000-000000000081")
	appealStranger = domain.AccountID("018f6b2a-0000-7000-8000-000000000082")
	appealActor    = domain.AccountID("018f6b2a-0000-7000-8000-000000000083")
	appealReviewer = domain.AccountID("018f6b2a-0000-7000-8000-000000000084")
)

func appealActionFixture(when time.Time) *application.SanctionedAction {
	return &application.SanctionedAction{
		ActionID:    "action-appeal-1",
		Action:      domain.ActionSuspension,
		Actor:       appealActor,
		Rule:        "MOD-10:suspension",
		CreatedAt:   when,
		CaseID:      "case-appeal-1",
		Target:      domain.TargetProfile,
		TargetID:    string(appealOwner),
		TargetOwner: appealOwner,
	}
}

func appealAuthorizer() *application.Authorizer {
	roles := &fakeRoles{assignments: map[domain.AccountID]*application.RoleAssignment{
		appealReviewer: {AccountID: appealReviewer, Role: domain.RoleAdmin},
	}}
	authorizer, err := application.NewAuthorizer(roles, &fakeClock{})
	if err != nil {
		panic(err)
	}
	return authorizer
}

func TestFileAppealOwnerDeadlineAndDuplicate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	newFileUC := func(store *fakeAppeals) *application.FileAppealUseCase {
		uc, err := application.NewFileAppealUseCase(application.AppealDependencies{
			Appeals: store,
			Clock:   &fakeClock{now: now},
		})
		if err != nil {
			t.Fatalf("NewFileAppealUseCase: %v", err)
		}
		return uc
	}

	t.Run("owner files inside window", func(t *testing.T) {
		t.Parallel()
		store := &fakeAppeals{
			actions: map[string]*application.SanctionedAction{"action-appeal-1": appealActionFixture(now.Add(-time.Hour))},
			appeals: map[string]*application.AppealRecord{},
			byID:    map[string]*application.AppealRecord{},
		}
		result, err := newFileUC(store).Execute(context.Background(), application.FileAppealCommand{
			ActionID: "action-appeal-1", Appellant: string(appealOwner), Context: "Contesting the suspension with fresh context",
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if result.Replayed || result.AppealID == "" {
			t.Fatalf("result = %+v, want fresh appeal", result)
		}
	})

	t.Run("stranger denied", func(t *testing.T) {
		t.Parallel()
		store := &fakeAppeals{
			actions: map[string]*application.SanctionedAction{"action-appeal-1": appealActionFixture(now.Add(-time.Hour))},
			appeals: map[string]*application.AppealRecord{},
			byID:    map[string]*application.AppealRecord{},
		}
		_, err := newFileUC(store).Execute(context.Background(), application.FileAppealCommand{
			ActionID: "action-appeal-1", Appellant: string(appealStranger), Context: "Third-party contest",
		})
		if !errors.Is(err, application.ErrNotAppealOwner) {
			t.Fatalf("error = %v, want ErrNotAppealOwner", err)
		}
	})

	t.Run("late appeal denied", func(t *testing.T) {
		t.Parallel()
		store := &fakeAppeals{
			actions: map[string]*application.SanctionedAction{"action-appeal-1": appealActionFixture(now.Add(-31 * 24 * time.Hour))},
			appeals: map[string]*application.AppealRecord{},
			byID:    map[string]*application.AppealRecord{},
		}
		_, err := newFileUC(store).Execute(context.Background(), application.FileAppealCommand{
			ActionID: "action-appeal-1", Appellant: string(appealOwner), Context: "Late contest",
		})
		if !errors.Is(err, application.ErrAppealExpired) {
			t.Fatalf("error = %v, want ErrAppealExpired", err)
		}
	})

	t.Run("duplicate replays original", func(t *testing.T) {
		t.Parallel()
		store := &fakeAppeals{
			actions: map[string]*application.SanctionedAction{"action-appeal-1": appealActionFixture(now.Add(-time.Hour))},
			appeals: map[string]*application.AppealRecord{},
			byID:    map[string]*application.AppealRecord{},
		}
		uc := newFileUC(store)
		first, err := uc.Execute(context.Background(), application.FileAppealCommand{
			ActionID: "action-appeal-1", Appellant: string(appealOwner), Context: "First contest",
		})
		if err != nil {
			t.Fatalf("first: %v", err)
		}
		second, err := uc.Execute(context.Background(), application.FileAppealCommand{
			ActionID: "action-appeal-1", Appellant: string(appealOwner), Context: "Second contest",
		})
		if err != nil {
			t.Fatalf("second: %v", err)
		}
		if !second.Replayed || second.AppealID != first.AppealID {
			t.Fatalf("second = %+v, want replay of %s", second, first.AppealID)
		}
	})
}

func TestClaimAppealRequiresDifferentReviewer(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	action := appealActionFixture(now.Add(-time.Hour))
	newClaimUC := func(store *fakeAppeals) *application.ClaimAppealUseCase {
		uc, err := application.NewClaimAppealUseCase(application.AppealDependencies{
			Appeals:    store,
			Authorizer: appealAuthorizer(),
			Clock:      &fakeClock{now: now},
		})
		if err != nil {
			t.Fatalf("NewClaimAppealUseCase: %v", err)
		}
		return uc
	}

	t.Run("deciding moderator cannot review", func(t *testing.T) {
		t.Parallel()
		deciderRoles := &fakeRoles{assignments: map[domain.AccountID]*application.RoleAssignment{
			appealActor: {AccountID: appealActor, Role: domain.RoleAdmin},
		}}
		deciderAuthorizer, err := application.NewAuthorizer(deciderRoles, &fakeClock{})
		if err != nil {
			t.Fatalf("NewAuthorizer: %v", err)
		}
		store := &fakeAppeals{
			actions: map[string]*application.SanctionedAction{"action-appeal-1": action},
			appeals: map[string]*application.AppealRecord{},
			byID: map[string]*application.AppealRecord{
				"appeal-test-1": {ID: "appeal-test-1", ActionID: "action-appeal-1", Appellant: appealOwner, Status: application.AppealOpen},
			},
		}
		uc, err := application.NewClaimAppealUseCase(application.AppealDependencies{
			Appeals:    store,
			Authorizer: deciderAuthorizer,
			Clock:      &fakeClock{now: now},
		})
		if err != nil {
			t.Fatalf("NewClaimAppealUseCase: %v", err)
		}
		_, err = uc.Execute(context.Background(), application.ClaimAppealCommand{
			AppealID: "appeal-test-1", Reviewer: string(appealActor),
		})
		if !errors.Is(err, application.ErrSameReviewer) {
			t.Fatalf("error = %v, want ErrSameReviewer", err)
		}
	})

	t.Run("different reviewer claims", func(t *testing.T) {
		t.Parallel()
		store := &fakeAppeals{
			actions: map[string]*application.SanctionedAction{"action-appeal-1": action},
			appeals: map[string]*application.AppealRecord{},
			byID: map[string]*application.AppealRecord{
				"appeal-test-1": {ID: "appeal-test-1", ActionID: "action-appeal-1", Appellant: appealOwner, Status: application.AppealOpen},
			},
		}
		record, err := newClaimUC(store).Execute(context.Background(), application.ClaimAppealCommand{
			AppealID: "appeal-test-1", Reviewer: string(appealReviewer),
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if string(record.Status) != string(application.AppealUnderReview) {
			t.Fatalf("record = %+v, want under review", record)
		}
	})
}

func TestDecideAppealRestoresWithoutDeletingAction(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	action := appealActionFixture(now.Add(-time.Hour))
	store := &fakeAppeals{
		actions: map[string]*application.SanctionedAction{"action-appeal-1": action},
		appeals: map[string]*application.AppealRecord{},
		byID: map[string]*application.AppealRecord{
			"appeal-test-1": {ID: "appeal-test-1", ActionID: "action-appeal-1", Appellant: appealOwner, Status: application.AppealUnderReview, Reviewer: appealReviewer},
		},
	}
	uc, err := application.NewDecideAppealUseCase(application.AppealDependencies{
		Appeals:    store,
		Authorizer: appealAuthorizer(),
		Clock:      &fakeClock{now: now},
	})
	if err != nil {
		t.Fatalf("NewDecideAppealUseCase: %v", err)
	}

	result, err := uc.Execute(context.Background(), application.DecideAppealCommand{
		AppealID: "appeal-test-1", Reviewer: string(appealReviewer),
		Outcome: "reversed", Reason: "Fresh evidence clears the account",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Outcome != domain.OutcomeReversed {
		t.Fatalf("outcome = %q, want reversed", result.Outcome)
	}
	// The original action stands: reversal restores projections, never
	// deletes the sanction record.
	if _, ok := store.actions["action-appeal-1"]; !ok {
		t.Fatal("original action must survive reversal")
	}

	// A stranger to the claim cannot decide it.
	_, err = uc.Execute(context.Background(), application.DecideAppealCommand{
		AppealID: "appeal-test-1", Reviewer: string(appealStranger),
		Outcome: "upheld", Reason: "Intruding decision",
	})
	if !errors.Is(err, application.ErrNotAuthorized) {
		t.Fatalf("error = %v, want ErrNotAuthorized", err)
	}
}
