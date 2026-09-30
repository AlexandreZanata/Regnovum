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

const testCreatorID = "018f6b2a-0000-7000-8000-000000000010"

var testInstant = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

type fakeArenaRepo struct {
	arenas map[domain.ArenaID]*domain.Arena

	createRequests []application.CreateArenaRequest
	createErr      error

	getErr error

	updateRequests []application.DraftUpdate
	updateErr      error

	publishRequests []publishCall
	publishErr      error

	transitions   []transitionCall
	transitionErr error

	deleteCalls []domain.ArenaID
	deleteErr   error
}

func newFakeArenaRepo() *fakeArenaRepo {
	return &fakeArenaRepo{arenas: make(map[domain.ArenaID]*domain.Arena)}
}

func (r *fakeArenaRepo) CreateArena(_ context.Context, request application.CreateArenaRequest) (*domain.Arena, error) {
	r.createRequests = append(r.createRequests, request)
	if r.createErr != nil {
		return nil, r.createErr
	}
	arena, err := domain.ReconstituteArena(
		domain.ArenaID("018f6b2a-0000-7000-8000-000000000020"),
		request.CreatorID,
		request.Statement,
		request.Context,
		request.Category,
		request.Language,
		domain.ArenaStatusDraft,
		domain.Slug{},
		1,
		testInstant,
		nil,
		nil,
	)
	if err != nil {
		return nil, err
	}
	r.arenas[arena.ID()] = arena
	return arena, nil
}

func (r *fakeArenaRepo) GetArenaForCreator(_ context.Context, arenaID domain.ArenaID, _ domain.CreatorID) (*domain.Arena, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	arena, ok := r.arenas[arenaID]
	if !ok {
		return nil, application.ErrArenaNotFound
	}
	// Real adapters rebuild the entity on every read; returning a copy keeps
	// the stored state immune to in-memory domain mutations.
	copied := *arena
	return &copied, nil
}

func (r *fakeArenaRepo) ListArenaDraftsForCreator(_ context.Context, creatorID domain.CreatorID) ([]domain.Arena, error) {
	drafts := make([]domain.Arena, 0, len(r.arenas))
	for _, arena := range r.arenas {
		if arena.CreatorID() == creatorID {
			drafts = append(drafts, *arena)
		}
	}
	return drafts, nil
}

func (r *fakeArenaRepo) UpdateArenaDraft(_ context.Context, arenaID domain.ArenaID, _ domain.CreatorID, update application.DraftUpdate) (*domain.Arena, error) {
	r.updateRequests = append(r.updateRequests, update)
	if r.updateErr != nil {
		return nil, r.updateErr
	}
	arena, ok := r.arenas[arenaID]
	if !ok {
		return nil, application.ErrArenaNotFound
	}
	if err := arena.UpdateDraft(&update.Statement, &update.Context, &update.Category, &update.Language); err != nil {
		return nil, err
	}
	return arena, nil
}

func (r *fakeArenaRepo) PublishArenaDraft(_ context.Context, arenaID domain.ArenaID, _ domain.CreatorID, slug domain.Slug, publishedAt time.Time, expectedVersion int32) (*domain.Arena, error) {
	r.publishRequests = append(r.publishRequests, publishCall{slug: slug, publishedAt: publishedAt, expectedVersion: expectedVersion})
	if r.publishErr != nil {
		return nil, r.publishErr
	}
	arena, ok := r.arenas[arenaID]
	if !ok {
		return nil, application.ErrArenaNotFound
	}
	if arena.Version() != expectedVersion {
		return nil, application.ErrVersionConflict
	}
	publishedAtCopy := publishedAt
	published, err := domain.ReconstituteArena(
		arena.ID(), arena.CreatorID(), arena.Statement(), arena.Context(), arena.Category(), arena.Language(),
		domain.ArenaStatusPublished, slug, arena.Version()+1, arena.CreatedAt(), &publishedAtCopy, nil,
	)
	if err != nil {
		return nil, err
	}
	r.arenas[arenaID] = published
	return published, nil
}

type publishCall struct {
	slug            domain.Slug
	publishedAt     time.Time
	expectedVersion int32
}

type transitionCall struct {
	status          domain.ArenaStatus
	expectedVersion int32
}

func (r *fakeArenaRepo) transition(arenaID domain.ArenaID, status domain.ArenaStatus, expectedVersion int32) (*domain.Arena, error) {
	arena, ok := r.arenas[arenaID]
	if !ok {
		return nil, application.ErrArenaNotFound
	}
	if arena.Version() != expectedVersion {
		return nil, application.ErrVersionConflict
	}
	publishedAt := arena.PublishedAt()
	updated, err := domain.ReconstituteArena(
		arena.ID(), arena.CreatorID(), arena.Statement(), arena.Context(), arena.Category(), arena.Language(),
		status, arena.Slug(), arena.Version()+1, arena.CreatedAt(), publishedAt, arena.ClosesAt(),
	)
	if err != nil {
		return nil, err
	}
	r.arenas[arenaID] = updated
	return updated, nil
}

func (r *fakeArenaRepo) GetArenaByID(_ context.Context, arenaID domain.ArenaID) (*domain.Arena, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	arena, ok := r.arenas[arenaID]
	if !ok {
		return nil, application.ErrArenaNotFound
	}
	copied := *arena
	return &copied, nil
}

func (r *fakeArenaRepo) CloseArena(_ context.Context, arenaID domain.ArenaID, _ domain.CreatorID, expectedVersion int32) (*domain.Arena, error) {
	r.transitions = append(r.transitions, transitionCall{status: domain.ArenaStatusClosed, expectedVersion: expectedVersion})
	if r.transitionErr != nil {
		return nil, r.transitionErr
	}
	return r.transition(arenaID, domain.ArenaStatusClosed, expectedVersion)
}

func (r *fakeArenaRepo) RestrictArena(_ context.Context, arenaID domain.ArenaID, expectedVersion int32) (*domain.Arena, error) {
	r.transitions = append(r.transitions, transitionCall{status: domain.ArenaStatusRestricted, expectedVersion: expectedVersion})
	if r.transitionErr != nil {
		return nil, r.transitionErr
	}
	return r.transition(arenaID, domain.ArenaStatusRestricted, expectedVersion)
}

func (r *fakeArenaRepo) RemoveArena(_ context.Context, arenaID domain.ArenaID, expectedVersion int32) (*domain.Arena, error) {
	r.transitions = append(r.transitions, transitionCall{status: domain.ArenaStatusRemoved, expectedVersion: expectedVersion})
	if r.transitionErr != nil {
		return nil, r.transitionErr
	}
	return r.transition(arenaID, domain.ArenaStatusRemoved, expectedVersion)
}

func (r *fakeArenaRepo) DeleteArenaDraft(_ context.Context, arenaID domain.ArenaID, _ domain.CreatorID) error {
	r.deleteCalls = append(r.deleteCalls, arenaID)
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.arenas, arenaID)
	return nil
}

func seedDraft(t *testing.T, repo *fakeArenaRepo, version int32) *domain.Arena {
	t.Helper()
	statement, err := domain.ParseStatement("A AGI existirá até 2040", domain.DefaultStatementPolicy())
	if err != nil {
		t.Fatalf("ParseStatement: %v", err)
	}
	category, err := domain.ParseCategory("technology")
	if err != nil {
		t.Fatalf("ParseCategory: %v", err)
	}
	language, err := domain.ParseLanguage("pt-BR")
	if err != nil {
		t.Fatalf("ParseLanguage: %v", err)
	}
	arena, err := domain.ReconstituteArena(
		domain.ArenaID("018f6b2a-0000-7000-8000-000000000021"),
		domain.CreatorID(testCreatorID),
		statement,
		domain.Context{},
		category,
		language,
		domain.ArenaStatusDraft,
		domain.Slug{},
		version,
		testInstant,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ReconstituteArena: %v", err)
	}
	repo.arenas[arena.ID()] = arena
	return arena
}

func TestCreateArenaDraftUseCase(t *testing.T) {
	repo := newFakeArenaRepo()
	useCase := application.NewCreateArenaDraftUseCase(repo, domain.DefaultStatementPolicy())

	draft, err := useCase.Execute(context.Background(), application.CreateArenaDraftCommand{
		AccountID: testCreatorID,
		Statement: "A AGI existirá até 2040",
		Context:   "Contexto opcional\r\ncom duas linhas",
		Category:  "technology",
		Language:  "pt-br",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !draft.IsDraft() || draft.Version() != 1 {
		t.Fatalf("draft = status %s, version %d", draft.Status(), draft.Version())
	}
	if !draft.Slug().IsZero() || draft.PublishedAt() != nil {
		t.Fatal("a draft must not have a slug or publication instant")
	}
	if len(repo.createRequests) != 1 {
		t.Fatalf("create calls = %d, want 1", len(repo.createRequests))
	}
	request := repo.createRequests[0]
	if request.CreatorID.String() != testCreatorID || request.Category.String() != "technology" {
		t.Errorf("request = %s/%s", request.CreatorID, request.Category)
	}
	if request.Language.String() != "pt-BR" {
		t.Errorf("language = %q, want canonical pt-BR", request.Language)
	}
	if request.Context.String() != "Contexto opcional\ncom duas linhas" {
		t.Errorf("context = %q, want canonical text", request.Context)
	}
}

func TestCreateArenaDraftUseCaseValidations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*application.CreateArenaDraftCommand)
		wantErr error
	}{
		{name: "empty account", mutate: func(cmd *application.CreateArenaDraftCommand) { cmd.AccountID = "" }, wantErr: domain.ErrEmptyCreatorID},
		{name: "short statement", mutate: func(cmd *application.CreateArenaDraftCommand) { cmd.Statement = "curta" }, wantErr: domain.ErrStatementTooShort},
		{name: "long statement", mutate: func(cmd *application.CreateArenaDraftCommand) { cmd.Statement = strings.Repeat("a", 300) }, wantErr: domain.ErrStatementTooLong},
		{name: "invalid context", mutate: func(cmd *application.CreateArenaDraftCommand) { cmd.Context = "contexto\x00" }, wantErr: domain.ErrInvalidContext},
		{name: "invalid category", mutate: func(cmd *application.CreateArenaDraftCommand) { cmd.Category = "Technology" }, wantErr: domain.ErrInvalidCategory},
		{name: "unsupported language", mutate: func(cmd *application.CreateArenaDraftCommand) { cmd.Language = "es-ES" }, wantErr: domain.ErrUnsupportedLanguage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeArenaRepo()
			useCase := application.NewCreateArenaDraftUseCase(repo, domain.DefaultStatementPolicy())

			command := application.CreateArenaDraftCommand{
				AccountID: testCreatorID,
				Statement: "A AGI existirá até 2040",
				Category:  "technology",
				Language:  "pt-BR",
			}
			tc.mutate(&command)

			if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Execute() error = %v, want %v", err, tc.wantErr)
			}
			if len(repo.createRequests) != 0 {
				t.Fatal("validation failure must not reach the repository")
			}
		})
	}

	repo := newFakeArenaRepo()
	repo.createErr = errors.New("storage unavailable")
	useCase := application.NewCreateArenaDraftUseCase(repo, domain.DefaultStatementPolicy())
	if _, err := useCase.Execute(context.Background(), application.CreateArenaDraftCommand{
		AccountID: testCreatorID,
		Statement: "A AGI existirá até 2040",
		Category:  "technology",
		Language:  "pt-BR",
	}); !errors.Is(err, repo.createErr) {
		t.Fatalf("repository error not propagated: %v", err)
	}
}

func validUpdateCommand(arena *domain.Arena) application.UpdateArenaDraftCommand {
	return application.UpdateArenaDraftCommand{
		AccountID:       testCreatorID,
		ArenaID:         arena.ID().String(),
		Statement:       "A afirmação revisada do rascunho",
		Context:         "Novo contexto",
		Category:        "science",
		Language:        "en-US",
		ExpectedVersion: arena.Version(),
	}
}

func TestUpdateArenaDraftUseCase(t *testing.T) {
	repo := newFakeArenaRepo()
	arena := seedDraft(t, repo, 2)
	useCase := application.NewUpdateArenaDraftUseCase(repo, domain.DefaultStatementPolicy())

	updated, err := useCase.Execute(context.Background(), validUpdateCommand(arena))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if updated.Version() != 3 || updated.Statement().String() != "A afirmação revisada do rascunho" {
		t.Fatalf("updated draft = version %d, statement %q", updated.Version(), updated.Statement())
	}
	if updated.Category().String() != "science" || updated.Language().String() != "en-US" {
		t.Errorf("updated draft = %s/%s", updated.Category(), updated.Language())
	}
	if len(repo.updateRequests) != 1 || repo.updateRequests[0].ExpectedVersion != 2 {
		t.Fatalf("update requests = %+v", repo.updateRequests)
	}
}

func TestUpdateArenaDraftUseCaseClearsContext(t *testing.T) {
	repo := newFakeArenaRepo()
	arena := seedDraft(t, repo, 1)
	statement, _ := domain.ParseStatement("A AGI existirá até 2040", domain.DefaultStatementPolicy())
	contextValue, _ := domain.ParseContext("Contexto antigo", domain.DefaultStatementPolicy())
	category, _ := domain.ParseCategory("technology")
	language, _ := domain.ParseLanguage("pt-BR")
	withContext, err := domain.ReconstituteArena(
		arena.ID(), arena.CreatorID(), statement, contextValue, category, language,
		domain.ArenaStatusDraft, domain.Slug{}, 1, testInstant, nil, nil,
	)
	if err != nil {
		t.Fatalf("ReconstituteArena: %v", err)
	}
	repo.arenas[arena.ID()] = withContext

	useCase := application.NewUpdateArenaDraftUseCase(repo, domain.DefaultStatementPolicy())
	command := validUpdateCommand(withContext)
	command.Context = ""

	updated, err := useCase.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !updated.Context().IsZero() {
		t.Fatalf("context = %q, want unset", updated.Context())
	}
	if !repo.updateRequests[0].Context.IsZero() {
		t.Fatal("clearing the context must reach the repository as unset")
	}
}

func TestUpdateArenaDraftUseCaseValidationsAndConflicts(t *testing.T) {
	repo := newFakeArenaRepo()
	arena := seedDraft(t, repo, 2)
	useCase := application.NewUpdateArenaDraftUseCase(repo, domain.DefaultStatementPolicy())

	invalid := []struct {
		name    string
		mutate  func(*application.UpdateArenaDraftCommand)
		wantErr error
	}{
		{name: "empty account", mutate: func(cmd *application.UpdateArenaDraftCommand) { cmd.AccountID = "" }, wantErr: domain.ErrEmptyCreatorID},
		{name: "empty arena", mutate: func(cmd *application.UpdateArenaDraftCommand) { cmd.ArenaID = "" }, wantErr: domain.ErrEmptyArenaID},
		{name: "zero version", mutate: func(cmd *application.UpdateArenaDraftCommand) { cmd.ExpectedVersion = 0 }, wantErr: domain.ErrInvalidVersion},
		{name: "short statement", mutate: func(cmd *application.UpdateArenaDraftCommand) { cmd.Statement = "curta" }, wantErr: domain.ErrStatementTooShort},
		{name: "invalid category", mutate: func(cmd *application.UpdateArenaDraftCommand) { cmd.Category = "Technology" }, wantErr: domain.ErrInvalidCategory},
		{name: "unsupported language", mutate: func(cmd *application.UpdateArenaDraftCommand) { cmd.Language = "fr-FR" }, wantErr: domain.ErrUnsupportedLanguage},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			command := validUpdateCommand(arena)
			tc.mutate(&command)
			if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Execute() error = %v, want %v", err, tc.wantErr)
			}
			if len(repo.updateRequests) != 0 {
				t.Fatal("validation failure must not reach the repository")
			}
		})
	}

	// Stale version: the caller read version 1 but the draft is at 2.
	stale := validUpdateCommand(arena)
	stale.ExpectedVersion = 1
	if _, err := useCase.Execute(context.Background(), stale); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale version error = %v, want ErrVersionConflict", err)
	}
	if len(repo.updateRequests) != 0 {
		t.Fatal("version conflict must not reach the repository")
	}

	// Repository race: the draft changed between the read and the write.
	repo.updateErr = application.ErrVersionConflict
	if _, err := useCase.Execute(context.Background(), validUpdateCommand(arena)); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("race error = %v, want ErrVersionConflict", err)
	}

	// Missing draft.
	repo.updateErr = nil
	missing := validUpdateCommand(arena)
	missing.ArenaID = "018f6b2a-0000-7000-8000-0000000000ff"
	if _, err := useCase.Execute(context.Background(), missing); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("missing draft error = %v, want ErrArenaNotFound", err)
	}
}

func TestUpdateArenaDraftUseCaseRejectsNonDraft(t *testing.T) {
	repo := newFakeArenaRepo()
	arena := seedDraft(t, repo, 1)
	published := makePublished(t, arena)
	repo.arenas[arena.ID()] = published

	useCase := application.NewUpdateArenaDraftUseCase(repo, domain.DefaultStatementPolicy())
	command := validUpdateCommand(published)
	if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, domain.ErrArenaNotDraft) {
		t.Fatalf("published update error = %v, want ErrArenaNotDraft", err)
	}
	if len(repo.updateRequests) != 0 {
		t.Fatal("non-draft update must not reach the repository")
	}
}

func TestDeleteArenaDraftUseCase(t *testing.T) {
	repo := newFakeArenaRepo()
	arena := seedDraft(t, repo, 1)
	useCase := application.NewDeleteArenaDraftUseCase(repo)

	if err := useCase.Execute(context.Background(), application.DeleteArenaDraftCommand{
		AccountID: testCreatorID,
		ArenaID:   arena.ID().String(),
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(repo.deleteCalls) != 1 {
		t.Fatalf("delete calls = %d, want 1", len(repo.deleteCalls))
	}

	for _, tc := range []struct {
		name    string
		command application.DeleteArenaDraftCommand
		wantErr error
	}{
		{name: "empty account", command: application.DeleteArenaDraftCommand{ArenaID: arena.ID().String()}, wantErr: domain.ErrEmptyCreatorID},
		{name: "empty arena", command: application.DeleteArenaDraftCommand{AccountID: testCreatorID}, wantErr: domain.ErrEmptyArenaID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := useCase.Execute(context.Background(), tc.command); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Execute() error = %v, want %v", err, tc.wantErr)
			}
		})
	}

	repo.deleteErr = application.ErrArenaNotFound
	if err := useCase.Execute(context.Background(), application.DeleteArenaDraftCommand{
		AccountID: testCreatorID,
		ArenaID:   arena.ID().String(),
	}); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("foreign draft error = %v, want ErrArenaNotFound", err)
	}

	repo.deleteErr = domain.ErrArenaNotDraft
	if err := useCase.Execute(context.Background(), application.DeleteArenaDraftCommand{
		AccountID: testCreatorID,
		ArenaID:   arena.ID().String(),
	}); !errors.Is(err, domain.ErrArenaNotDraft) {
		t.Fatalf("non-draft error = %v, want ErrArenaNotDraft", err)
	}
}

func makePublished(t *testing.T, draft *domain.Arena) *domain.Arena {
	t.Helper()
	publishedAt := testInstant
	slug, err := domain.ParseSlug("a-agi-existira-ate-2040")
	if err != nil {
		t.Fatalf("ParseSlug: %v", err)
	}
	published, err := domain.ReconstituteArena(
		draft.ID(), draft.CreatorID(), draft.Statement(), draft.Context(), draft.Category(), draft.Language(),
		domain.ArenaStatusPublished, slug, draft.Version()+1, draft.CreatedAt(), &publishedAt, nil,
	)
	if err != nil {
		t.Fatalf("ReconstituteArena(published): %v", err)
	}
	return published
}
