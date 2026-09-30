package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

type stubUnitOfWork struct {
	committed  int
	rolledBack int
}

func (u *stubUnitOfWork) WithinTransaction(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		u.rolledBack++
		return err
	}
	u.committed++
	return nil
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

type stubPassConsumer struct {
	calls []application.PassConsumption
	err   error
}

func (c *stubPassConsumer) ConsumeArenaPass(_ context.Context, request application.PassConsumption) (*application.ConsumedPass, error) {
	c.calls = append(c.calls, request)
	if c.err != nil {
		return nil, c.err
	}
	return &application.ConsumedPass{LotID: "lot-1", Remaining: 0}, nil
}

func newPublishFixture(t *testing.T) (*fakeArenaRepo, *stubPassConsumer, *stubUnitOfWork, *fixedClock, *domain.Arena) {
	t.Helper()
	repo := newFakeArenaRepo()
	draft := seedDraft(t, repo, 1)
	consumer := &stubPassConsumer{}
	uow := &stubUnitOfWork{}
	clock := &fixedClock{now: testInstant}
	return repo, consumer, uow, clock, draft
}

func TestPublishArenaUseCaseSuccess(t *testing.T) {
	repo, consumer, uow, clock, draft := newPublishFixture(t)
	useCase := application.NewPublishArenaUseCase(repo, consumer, uow, clock)

	result, err := useCase.Execute(context.Background(), application.PublishArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   draft.ID().String(),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed {
		t.Fatal("first publication must not be a replay")
	}
	if result.Arena.Status() != domain.ArenaStatusPublished || result.Arena.Version() != 2 {
		t.Fatalf("published arena = status %s version %d", result.Arena.Status(), result.Arena.Version())
	}
	if !result.Arena.PublishedAt().Equal(testInstant) {
		t.Fatalf("publishedAt = %v, want %v", result.Arena.PublishedAt(), testInstant)
	}
	if len(consumer.calls) != 1 || consumer.calls[0].AccountID != testCreatorID || consumer.calls[0].ArenaID != draft.ID().String() {
		t.Fatalf("consumption calls = %+v", consumer.calls)
	}
	if len(repo.publishRequests) != 1 || repo.publishRequests[0].expectedVersion != 1 {
		t.Fatalf("publish requests = %+v", repo.publishRequests)
	}
	if uow.committed != 1 || uow.rolledBack != 0 {
		t.Fatalf("unit of work = committed %d, rolled back %d", uow.committed, uow.rolledBack)
	}
}

func TestPublishArenaUseCaseWithoutPassRollsBack(t *testing.T) {
	repo, consumer, uow, clock, draft := newPublishFixture(t)
	consumer.err = application.ErrNoPassAvailable
	useCase := application.NewPublishArenaUseCase(repo, consumer, uow, clock)

	if _, err := useCase.Execute(context.Background(), application.PublishArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   draft.ID().String(),
	}); !errors.Is(err, application.ErrNoPassAvailable) {
		t.Fatalf("Execute() error = %v, want ErrNoPassAvailable", err)
	}
	if len(repo.publishRequests) != 0 {
		t.Fatal("a failed consumption must not publish the Arena")
	}
	if uow.rolledBack != 1 || uow.committed != 0 {
		t.Fatalf("unit of work = committed %d, rolled back %d", uow.committed, uow.rolledBack)
	}
	stored, err := repo.GetArenaForCreator(context.Background(), draft.ID(), domain.CreatorID(testCreatorID))
	if err != nil {
		t.Fatalf("reload draft: %v", err)
	}
	if stored.Status() != domain.ArenaStatusDraft {
		t.Fatal("a failed publication must keep the Arena as a draft")
	}
}

// TestPublishArenaUseCaseFailureAfterConsumptionRollsBack is the atomicity
// probe of P08-T04: the pass is consumed first, the publication then fails,
// and the unit of work discards both writes.
func TestPublishArenaUseCaseFailureAfterConsumptionRollsBack(t *testing.T) {
	repo, consumer, uow, clock, draft := newPublishFixture(t)
	repo.publishErr = application.ErrSlugConflict
	useCase := application.NewPublishArenaUseCase(repo, consumer, uow, clock)

	if _, err := useCase.Execute(context.Background(), application.PublishArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   draft.ID().String(),
	}); !errors.Is(err, application.ErrSlugConflict) {
		t.Fatalf("Execute() error = %v, want ErrSlugConflict", err)
	}
	if len(consumer.calls) != 1 {
		t.Fatal("the consumption must have been attempted before the publication")
	}
	if uow.rolledBack != 1 || uow.committed != 0 {
		t.Fatalf("unit of work = committed %d, rolled back %d", uow.committed, uow.rolledBack)
	}
}

func TestPublishArenaUseCaseReplay(t *testing.T) {
	repo, consumer, uow, clock, draft := newPublishFixture(t)
	useCase := application.NewPublishArenaUseCase(repo, consumer, uow, clock)

	if _, err := useCase.Execute(context.Background(), application.PublishArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   draft.ID().String(),
	}); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}

	replay, err := useCase.Execute(context.Background(), application.PublishArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   draft.ID().String(),
	})
	if err != nil {
		t.Fatalf("replay Execute() error = %v", err)
	}
	if !replay.Replayed || replay.Arena.Version() != 2 {
		t.Fatalf("replay = %+v, want the published Arena without changes", replay)
	}
	if len(consumer.calls) != 1 {
		t.Fatalf("consumption calls = %d, want 1 (retries never consume again)", len(consumer.calls))
	}
}

func TestPublishArenaUseCaseValidationsAndNonDraftStates(t *testing.T) {
	repo, consumer, uow, clock, draft := newPublishFixture(t)
	useCase := application.NewPublishArenaUseCase(repo, consumer, uow, clock)

	if _, err := useCase.Execute(context.Background(), application.PublishArenaCommand{ArenaID: draft.ID().String()}); !errors.Is(err, domain.ErrEmptyCreatorID) {
		t.Fatalf("empty account error = %v, want ErrEmptyCreatorID", err)
	}
	if _, err := useCase.Execute(context.Background(), application.PublishArenaCommand{AccountID: testCreatorID}); !errors.Is(err, domain.ErrEmptyArenaID) {
		t.Fatalf("empty arena error = %v, want ErrEmptyArenaID", err)
	}

	// A closed Arena cannot be published again.
	closed := makePublished(t, draft)
	if err := closed.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	repo.arenas[draft.ID()] = closed
	if _, err := useCase.Execute(context.Background(), application.PublishArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   draft.ID().String(),
	}); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("closed publish error = %v, want ErrInvalidStatusChange", err)
	}
	if len(consumer.calls) != 0 {
		t.Fatal("a non-draft publication must not consume a pass")
	}

	// Missing or foreign drafts surface as not found.
	if _, err := useCase.Execute(context.Background(), application.PublishArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   "018f6b2a-0000-7000-8000-0000000000ff",
	}); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("missing arena error = %v, want ErrArenaNotFound", err)
	}

	// Repository version conflict propagates.
	fresh := newFakeArenaRepo()
	freshDraft := seedDraft(t, fresh, 1)
	fresh.publishErr = application.ErrVersionConflict
	conflictUseCase := application.NewPublishArenaUseCase(fresh, consumer, uow, clock)
	if _, err := conflictUseCase.Execute(context.Background(), application.PublishArenaCommand{
		AccountID: testCreatorID,
		ArenaID:   freshDraft.ID().String(),
	}); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("conflict error = %v, want ErrVersionConflict", err)
	}
}

func TestDeriveSlugIsStableAndBounded(t *testing.T) {
	repo := newFakeArenaRepo()
	draft := seedDraft(t, repo, 1)

	slug, err := application.DeriveSlug(*draft)
	if err != nil {
		t.Fatalf("DeriveSlug() error = %v", err)
	}
	if slug.String() != "a-agi-existira-ate-2040-018f6b2a" {
		t.Fatalf("slug = %q, want the statement base plus the arena id prefix", slug.String())
	}

	again, err := application.DeriveSlug(*draft)
	if err != nil || !again.Equals(slug) {
		t.Fatalf("DeriveSlug is not deterministic: %q vs %q", again.String(), slug.String())
	}

	// A very long statement is truncated so the id suffix always fits.
	policy := domain.DefaultStatementPolicy()
	statement, err := domain.ParseStatement(strings.Repeat("a", policy.MaxLength), policy)
	if err != nil {
		t.Fatalf("ParseStatement(long): %v", err)
	}
	category, _ := domain.ParseCategory("technology")
	language, _ := domain.ParseLanguage("pt-BR")
	longArena, err := domain.ReconstituteArena(
		domain.ArenaID("018f6b2a-0000-7000-8000-000000000021"),
		domain.CreatorID(testCreatorID),
		statement,
		domain.Context{},
		category,
		language,
		domain.ArenaStatusDraft,
		domain.Slug{},
		1,
		testInstant,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ReconstituteArena(long): %v", err)
	}
	longSlug, err := application.DeriveSlug(*longArena)
	if err != nil {
		t.Fatalf("DeriveSlug(long) error = %v", err)
	}
	if len(longSlug.String()) > domain.SlugMaxLength {
		t.Fatalf("long slug length = %d, want <= %d", len(longSlug.String()), domain.SlugMaxLength)
	}
	if len(longSlug.String()) < domain.SlugMinLength+1 || longSlug.String()[len(longSlug.String())-9] != '-' {
		t.Fatalf("long slug %q must end with the id suffix", longSlug.String())
	}
}

func TestDeriveSlugKeepsMinimalBase(t *testing.T) {
	// A statement slugifying to exactly SlugMinLength keeps its base: only
	// shorter bases fall back to the placeholder (mutation gate:
	// publish_arena.go:118).
	statement, err := domain.ParseStatement("abc!!!!!!!", domain.DefaultStatementPolicy())
	if err != nil {
		t.Fatalf("ParseStatement: %v", err)
	}
	category, _ := domain.ParseCategory("technology")
	language, _ := domain.ParseLanguage("pt-BR")
	arena, err := domain.ReconstituteArena(
		domain.ArenaID("018f6b2a-0000-7000-8000-000000000022"),
		domain.CreatorID(testCreatorID),
		statement,
		domain.Context{},
		category,
		language,
		domain.ArenaStatusDraft,
		domain.Slug{},
		1,
		testInstant,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ReconstituteArena: %v", err)
	}
	slug, err := application.DeriveSlug(*arena)
	if err != nil {
		t.Fatalf("DeriveSlug() error = %v", err)
	}
	if slug.String() != "abc-018f6b2a" {
		t.Fatalf("slug = %q, want the minimal base plus the id prefix", slug.String())
	}
}
