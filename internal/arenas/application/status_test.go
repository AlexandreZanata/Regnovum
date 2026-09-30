package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

const testModeratorID = "018f6b2a-0000-7000-8000-000000000099"

type fakeModerationAuthorizer struct {
	allowed map[domain.ModeratorID]bool
	err     error
	calls   int
	actors  []domain.ModeratorID
}

func (a *fakeModerationAuthorizer) EnsureModerator(_ context.Context, actor domain.ModeratorID) error {
	a.calls++
	a.actors = append(a.actors, actor)
	if a.err != nil {
		return a.err
	}
	if !a.allowed[actor] {
		return application.ErrNotAuthorized
	}
	return nil
}

type fakeModerationAudit struct {
	events []application.ModerationEvent
	err    error
}

func (a *fakeModerationAudit) RecordArenaModeration(_ context.Context, event application.ModerationEvent) error {
	a.events = append(a.events, event)
	return a.err
}

func newModerationFixture(t *testing.T) (*fakeArenaRepo, *fakeModerationAuthorizer, *fakeModerationAudit, *fixedClock, *domain.Arena) {
	t.Helper()
	repo := newFakeArenaRepo()
	published := seedPublished(t, repo)
	authorizer := &fakeModerationAuthorizer{allowed: map[domain.ModeratorID]bool{testModeratorID: true}}
	audit := &fakeModerationAudit{}
	clock := &fixedClock{now: testInstant}
	return repo, authorizer, audit, clock, published
}

func seedPublished(t *testing.T, repo *fakeArenaRepo) *domain.Arena {
	t.Helper()
	draft := seedDraft(t, repo, 1)
	published := makePublished(t, draft)
	repo.arenas[published.ID()] = published
	return published
}

func validModerationCommand(arena *domain.Arena) application.ModerateArenaCommand {
	return application.ModerateArenaCommand{
		ActorAccountID: testModeratorID,
		ArenaID:        arena.ID().String(),
		Reason:         "decisão de moderação registrada no caso 42",
	}
}

func TestCloseArenaUseCase(t *testing.T) {
	repo := newFakeArenaRepo()
	published := seedPublished(t, repo)
	useCase := application.NewCloseArenaUseCase(repo)

	result, err := useCase.Execute(context.Background(), application.CloseArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   published.ID().String(),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Arena.Status() != domain.ArenaStatusClosed {
		t.Fatalf("result = status %s, replayed %v", result.Arena.Status(), result.Replayed)
	}
	if result.Arena.Version() != published.Version()+1 {
		t.Fatalf("version = %d, want %d", result.Arena.Version(), published.Version()+1)
	}
	if len(repo.transitions) != 1 || repo.transitions[0].expectedVersion != published.Version() {
		t.Fatalf("transitions = %+v", repo.transitions)
	}
	if err := result.Arena.EnsureAcceptsParticipation(); !errors.Is(err, domain.ErrArenaNotOpen) {
		t.Fatalf("closed arena participation error = %v, want ErrArenaNotOpen", err)
	}

	// Retry resolves the replay without a second transition.
	retry, err := useCase.Execute(context.Background(), application.CloseArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   published.ID().String(),
	})
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed || len(repo.transitions) != 1 {
		t.Fatalf("retry = %+v, transitions = %d", retry, len(repo.transitions))
	}
}

func TestCloseArenaUseCaseRejectsOtherStates(t *testing.T) {
	repo := newFakeArenaRepo()
	draft := seedDraft(t, repo, 1)
	useCase := application.NewCloseArenaUseCase(repo)

	if _, err := useCase.Execute(context.Background(), application.CloseArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   draft.ID().String(),
	}); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("draft close error = %v, want ErrInvalidStatusChange", err)
	}

	if _, err := useCase.Execute(context.Background(), application.CloseArenaCommand{
		ArenaID: draft.ID().String(),
	}); !errors.Is(err, domain.ErrEmptyCreatorID) {
		t.Fatalf("empty account error = %v, want ErrEmptyCreatorID", err)
	}
	if _, err := useCase.Execute(context.Background(), application.CloseArenaCommand{
		AccountID: testCreatorID,
	}); !errors.Is(err, domain.ErrEmptyArenaID) {
		t.Fatalf("empty arena error = %v, want ErrEmptyArenaID", err)
	}

	repo.transitionErr = application.ErrVersionConflict
	published := seedPublished(t, repo)
	if _, err := useCase.Execute(context.Background(), application.CloseArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   published.ID().String(),
	}); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("conflict error = %v, want ErrVersionConflict", err)
	}
}

func TestRestrictArenaUseCase(t *testing.T) {
	repo, authorizer, audit, clock, published := newModerationFixture(t)
	useCase := application.NewRestrictArenaUseCase(repo, authorizer, audit, clock)

	result, err := useCase.Execute(context.Background(), validModerationCommand(published))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Arena.Status() != domain.ArenaStatusRestricted {
		t.Fatalf("result = status %s, replayed %v", result.Arena.Status(), result.Replayed)
	}
	if authorizer.calls != 1 || authorizer.actors[0] != testModeratorID {
		t.Fatalf("authorizer calls = %+v", authorizer.actors)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(audit.events))
	}
	event := audit.events[0]
	if event.Action != application.ModerationRestrict || event.ActorAccountID != testModeratorID || event.Replayed {
		t.Fatalf("audit event = %+v", event)
	}
	if event.Reason.String() != "decisão de moderação registrada no caso 42" || !event.OccurredAt.Equal(testInstant) {
		t.Fatalf("audit reason/instant = %q/%v", event.Reason, event.OccurredAt)
	}
	if err := result.Arena.EnsureAcceptsParticipation(); !errors.Is(err, domain.ErrArenaNotOpen) {
		t.Fatalf("restricted arena participation error = %v, want ErrArenaNotOpen", err)
	}

	// Retry on the restricted Arena is an audited replay without writes.
	replay, err := useCase.Execute(context.Background(), validModerationCommand(published))
	if err != nil {
		t.Fatalf("replay Execute() error = %v", err)
	}
	if !replay.Replayed || len(repo.transitions) != 1 {
		t.Fatalf("replay = %+v, transitions = %d", replay, len(repo.transitions))
	}
	if len(audit.events) != 2 || !audit.events[1].Replayed {
		t.Fatalf("audit events = %+v, want the replay recorded", audit.events)
	}
}

func TestRestrictArenaUseCaseNegativeAuthorization(t *testing.T) {
	repo, authorizer, audit, clock, published := newModerationFixture(t)
	authorizer.allowed = map[domain.ModeratorID]bool{}
	useCase := application.NewRestrictArenaUseCase(repo, authorizer, audit, clock)

	if _, err := useCase.Execute(context.Background(), validModerationCommand(published)); !errors.Is(err, application.ErrNotAuthorized) {
		t.Fatalf("Execute() error = %v, want ErrNotAuthorized", err)
	}
	if len(repo.transitions) != 0 {
		t.Fatal("denied moderation must not write")
	}
	if len(audit.events) != 0 {
		t.Fatal("denied moderation must not audit")
	}

	authorizer.err = errors.New("identity unavailable")
	if _, err := useCase.Execute(context.Background(), validModerationCommand(published)); !errors.Is(err, authorizer.err) {
		t.Fatalf("authorizer error not propagated: %v", err)
	}
}

func TestModerationValidations(t *testing.T) {
	repo, authorizer, audit, clock, published := newModerationFixture(t)
	restrict := application.NewRestrictArenaUseCase(repo, authorizer, audit, clock)
	remove := application.NewRemoveArenaUseCase(repo, authorizer, audit, clock)

	tests := []struct {
		name    string
		mutate  func(*application.ModerateArenaCommand)
		wantErr error
	}{
		{name: "empty actor", mutate: func(cmd *application.ModerateArenaCommand) { cmd.ActorAccountID = "" }, wantErr: domain.ErrActorRequired},
		{name: "empty arena", mutate: func(cmd *application.ModerateArenaCommand) { cmd.ArenaID = "" }, wantErr: domain.ErrEmptyArenaID},
		{name: "empty reason", mutate: func(cmd *application.ModerateArenaCommand) { cmd.Reason = "   " }, wantErr: domain.ErrEmptyReason},
		{name: "invalid reason", mutate: func(cmd *application.ModerateArenaCommand) { cmd.Reason = "linha\nquebrada" }, wantErr: domain.ErrInvalidReason},
		{name: "reason too long", mutate: func(cmd *application.ModerateArenaCommand) { cmd.Reason = strings.Repeat("a", 501) }, wantErr: domain.ErrReasonTooLong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			command := validModerationCommand(published)
			tc.mutate(&command)

			if _, err := restrict.Execute(context.Background(), command); !errors.Is(err, tc.wantErr) {
				t.Fatalf("restrict error = %v, want %v", err, tc.wantErr)
			}
			if _, err := remove.Execute(context.Background(), command); !errors.Is(err, tc.wantErr) {
				t.Fatalf("remove error = %v, want %v", err, tc.wantErr)
			}
			if authorizer.calls != 0 {
				t.Error("validation failure must not reach the authorizer")
			}
			if len(repo.transitions) != 0 || len(audit.events) != 0 {
				t.Fatal("validation failure must not write or audit")
			}
		})
	}
}

func TestRemoveArenaUseCase(t *testing.T) {
	repo, authorizer, audit, clock, published := newModerationFixture(t)
	restrict := application.NewRestrictArenaUseCase(repo, authorizer, audit, clock)
	remove := application.NewRemoveArenaUseCase(repo, authorizer, audit, clock)

	// Published → removed.
	result, err := remove.Execute(context.Background(), validModerationCommand(published))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Arena.Status() != domain.ArenaStatusRemoved {
		t.Fatalf("result = status %s, replayed %v", result.Arena.Status(), result.Replayed)
	}
	if audit.events[0].Action != application.ModerationRemove {
		t.Fatalf("audit action = %q, want REMOVE", audit.events[0].Action)
	}

	// Removed is terminal: retry is a replay, not a transition.
	replay, err := remove.Execute(context.Background(), validModerationCommand(published))
	if err != nil {
		t.Fatalf("replay Execute() error = %v", err)
	}
	if !replay.Replayed || len(repo.transitions) != 1 {
		t.Fatalf("replay = %+v, transitions = %d", replay, len(repo.transitions))
	}

	// A draft can be neither restricted nor removed.
	draft := seedDraft(t, repo, 1)
	draftCommand := application.ModerateArenaCommand{
		ActorAccountID: testModeratorID,
		ArenaID:        draft.ID().String(),
		Reason:         "tentativa sobre rascunho",
	}
	if _, err := remove.Execute(context.Background(), draftCommand); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("draft remove error = %v, want ErrInvalidStatusChange", err)
	}
	if _, err := restrict.Execute(context.Background(), draftCommand); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("draft restrict error = %v, want ErrInvalidStatusChange", err)
	}

	// Reserved transition: restricted → removed.
	restrictedSeed := seedPublished(t, repo)
	if _, err := restrict.Execute(context.Background(), validModerationCommand(restrictedSeed)); err != nil {
		t.Fatalf("restrict seed: %v", err)
	}
	removed, err := remove.Execute(context.Background(), validModerationCommand(restrictedSeed))
	if err != nil {
		t.Fatalf("restricted remove error = %v", err)
	}
	if removed.Arena.Status() != domain.ArenaStatusRemoved {
		t.Fatalf("status = %s, want removed", removed.Arena.Status())
	}
}

func TestModerationAuditFailurePropagates(t *testing.T) {
	repo, authorizer, audit, clock, published := newModerationFixture(t)
	audit.err = errors.New("audit sink unavailable")
	useCase := application.NewRestrictArenaUseCase(repo, authorizer, audit, clock)

	if _, err := useCase.Execute(context.Background(), validModerationCommand(published)); !errors.Is(err, audit.err) {
		t.Fatalf("audit error not propagated: %v", err)
	}
}
