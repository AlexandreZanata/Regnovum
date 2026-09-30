package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		t.Fatalf("scan uuid %q: %v", raw, err)
	}
	return id
}

func mustArenaCreator(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) domain.CreatorID {
	t.Helper()
	acc, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create creator %s: %v", email, err)
	}
	return domain.CreatorID(uuidString(acc.ID))
}

func mustCreateRequest(t *testing.T, creatorID domain.CreatorID, statement, contextText, category, language string) application.CreateArenaRequest {
	t.Helper()
	policy := domain.DefaultStatementPolicy()
	parsedStatement, err := domain.ParseStatement(statement, policy)
	if err != nil {
		t.Fatalf("ParseStatement(%q): %v", statement, err)
	}
	parsedContext, err := domain.ParseContext(contextText, policy)
	if err != nil {
		t.Fatalf("ParseContext(%q): %v", contextText, err)
	}
	parsedCategory, err := domain.ParseCategory(category)
	if err != nil {
		t.Fatalf("ParseCategory(%q): %v", category, err)
	}
	parsedLanguage, err := domain.ParseLanguage(language)
	if err != nil {
		t.Fatalf("ParseLanguage(%q): %v", language, err)
	}
	return application.CreateArenaRequest{
		CreatorID: creatorID,
		Statement: parsedStatement,
		Context:   parsedContext,
		Category:  parsedCategory,
		Language:  parsedLanguage,
	}
}

func mustDraftUpdate(t *testing.T, statement, contextText, category, language string, expectedVersion int32) application.DraftUpdate {
	t.Helper()
	request := mustCreateRequest(t, domain.CreatorID("ignored"), statement, contextText, category, language)
	return application.DraftUpdate{
		Statement:       request.Statement,
		Context:         request.Context,
		Category:        request.Category,
		Language:        request.Language,
		ExpectedVersion: expectedVersion,
	}
}

func countArenaRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func TestRepository_CreateAndGetDraft(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "arena-drafts@arena.example.com")

	draft, err := repo.CreateArena(ctx, mustCreateRequest(t, creator, "A AGI existirá até 2040", "Contexto inicial", "technology", "pt-BR"))
	if err != nil {
		t.Fatalf("CreateArena() error = %v", err)
	}
	if !draft.IsDraft() || draft.Version() != 1 {
		t.Fatalf("draft = status %s version %d", draft.Status(), draft.Version())
	}
	if !draft.Slug().IsZero() || draft.PublishedAt() != nil {
		t.Fatal("a draft must have no slug and no publication instant")
	}
	if draft.Context().String() != "Contexto inicial" {
		t.Errorf("context = %q", draft.Context())
	}
	if draft.CreatorID() != creator {
		t.Errorf("creator = %q, want %q", draft.CreatorID(), creator)
	}

	loaded, err := repo.GetArenaForCreator(ctx, draft.ID(), creator)
	if err != nil {
		t.Fatalf("GetArenaForCreator() error = %v", err)
	}
	if !loaded.Statement().Equals(draft.Statement()) || loaded.Version() != 1 {
		t.Fatalf("loaded draft = %+v", loaded)
	}

	drafts, err := repo.ListArenaDraftsForCreator(ctx, creator)
	if err != nil {
		t.Fatalf("ListArenaDraftsForCreator() error = %v", err)
	}
	if len(drafts) != 1 || drafts[0].ID() != draft.ID() {
		t.Fatalf("drafts = %+v, want the created draft", drafts)
	}

	var slugNull, publishedNull bool
	if err := pool.QueryRow(ctx,
		"SELECT slug IS NULL, published_at IS NULL FROM app.arenas WHERE id = $1",
		mustUUID(t, draft.ID().String())).Scan(&slugNull, &publishedNull); err != nil {
		t.Fatalf("inspect stored draft: %v", err)
	}
	if !slugNull || !publishedNull {
		t.Fatal("stored draft must keep slug and published_at NULL")
	}
}

func TestRepository_DraftOwnershipIsolation(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustArenaCreator(t, ctx, q, "arena-owner@arena.example.com")
	intruder := mustArenaCreator(t, ctx, q, "arena-intruder@arena.example.com")

	draft, err := repo.CreateArena(ctx, mustCreateRequest(t, owner, "A AGI existirá até 2040", "", "technology", "pt-BR"))
	if err != nil {
		t.Fatalf("CreateArena() error = %v", err)
	}

	if _, err := repo.GetArenaForCreator(ctx, draft.ID(), intruder); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("intruder get error = %v, want ErrArenaNotFound", err)
	}
	if _, err := repo.UpdateArenaDraft(ctx, draft.ID(), intruder, mustDraftUpdate(t, "Outra afirmação do intruso", "", "technology", "pt-BR", 1)); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("intruder update error = %v, want ErrArenaNotFound", err)
	}
	if err := repo.DeleteArenaDraft(ctx, draft.ID(), intruder); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("intruder delete error = %v, want ErrArenaNotFound", err)
	}

	intruderDrafts, err := repo.ListArenaDraftsForCreator(ctx, intruder)
	if err != nil {
		t.Fatalf("intruder list error = %v", err)
	}
	if len(intruderDrafts) != 0 {
		t.Fatalf("intruder sees %d drafts, want 0", len(intruderDrafts))
	}

	stored, err := repo.GetArenaForCreator(ctx, draft.ID(), owner)
	if err != nil {
		t.Fatalf("owner get error = %v", err)
	}
	if stored.Version() != 1 || !stored.Statement().Equals(draft.Statement()) {
		t.Fatal("intruder writes changed the owner draft")
	}
}

func TestRepository_UpdateDraftOptimisticConcurrency(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "arena-optimistic@arena.example.com")
	draft, err := repo.CreateArena(ctx, mustCreateRequest(t, creator, "A AGI existirá até 2040", "Contexto inicial", "technology", "pt-BR"))
	if err != nil {
		t.Fatalf("CreateArena() error = %v", err)
	}

	updated, err := repo.UpdateArenaDraft(ctx, draft.ID(), creator, mustDraftUpdate(t, "A afirmação revisada do rascunho", "", "science", "en-US", 1))
	if err != nil {
		t.Fatalf("UpdateArenaDraft() error = %v", err)
	}
	if updated.Version() != 2 {
		t.Fatalf("updated = version %d, want 2", updated.Version())
	}
	if updated.Statement().String() != "A afirmação revisada do rascunho" {
		t.Fatalf("updated statement = %q", updated.Statement())
	}
	if updated.Context().IsZero() != true {
		t.Fatal("clearing the context must store NULL")
	}
	if updated.Category().String() != "science" || updated.Language().String() != "en-US" {
		t.Fatalf("updated = %s/%s", updated.Category(), updated.Language())
	}

	// Stale version: rejected and untouched.
	if _, err := repo.UpdateArenaDraft(ctx, draft.ID(), creator, mustDraftUpdate(t, "Outra afirmação revisada", "", "technology", "pt-BR", 1)); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale update error = %v, want ErrVersionConflict", err)
	}
	stored, err := repo.GetArenaForCreator(ctx, draft.ID(), creator)
	if err != nil {
		t.Fatalf("reload draft: %v", err)
	}
	if stored.Version() != 2 || stored.Category().String() != "science" {
		t.Fatal("stale update changed the stored draft")
	}

	// Concurrent updates with the same expected version: exactly one wins.
	const workers = 10
	var wg sync.WaitGroup
	var successes, conflicts atomic.Int32
	unexpected := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := repo.UpdateArenaDraft(ctx, draft.ID(), creator, mustDraftUpdate(
				t, fmt.Sprintf("Afirmação concorrente número %d", index), "", "technology", "pt-BR", 2,
			))
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, application.ErrVersionConflict):
				conflicts.Add(1)
			default:
				unexpected[index] = err
			}
		}(i)
	}
	wg.Wait()

	for i, err := range unexpected {
		if err != nil {
			t.Fatalf("worker %d unexpected error = %v", i, err)
		}
	}
	if successes.Load() != 1 || conflicts.Load() != workers-1 {
		t.Fatalf("successes=%d conflicts=%d, want 1/%d", successes.Load(), conflicts.Load(), workers-1)
	}
	final, err := repo.GetArenaForCreator(ctx, draft.ID(), creator)
	if err != nil {
		t.Fatalf("final reload: %v", err)
	}
	if final.Version() != 3 {
		t.Fatalf("final version = %d, want 3 (one accepted update)", final.Version())
	}
}

func TestRepository_DeleteDraftAndNonDraftGuard(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "arena-delete@arena.example.com")
	draft, err := repo.CreateArena(ctx, mustCreateRequest(t, creator, "Rascunho descartável da Arena", "", "culture", "pt-BR"))
	if err != nil {
		t.Fatalf("CreateArena() error = %v", err)
	}
	if err := repo.DeleteArenaDraft(ctx, draft.ID(), creator); err != nil {
		t.Fatalf("DeleteArenaDraft() error = %v", err)
	}
	if err := repo.DeleteArenaDraft(ctx, draft.ID(), creator); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("second delete error = %v, want ErrArenaNotFound", err)
	}

	// A published Arena is retained: the guard reports the state instead.
	published, err := repo.CreateArena(ctx, mustCreateRequest(t, creator, "Arena publicada para guarda", "", "society", "pt-BR"))
	if err != nil {
		t.Fatalf("CreateArena(published seed) error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE app.arenas SET status = 'published', slug = 'guard-arena', published_at = now(), version = version + 1
		WHERE id = $1`, mustUUID(t, published.ID().String())); err != nil {
		t.Fatalf("publish seed: %v", err)
	}

	if err := repo.DeleteArenaDraft(ctx, published.ID(), creator); !errors.Is(err, domain.ErrArenaNotDraft) {
		t.Fatalf("published delete error = %v, want ErrArenaNotDraft", err)
	}
	if _, err := repo.UpdateArenaDraft(ctx, published.ID(), creator, mustDraftUpdate(t, "Reescrita proibida da Arena", "", "society", "pt-BR", published.Version())); !errors.Is(err, domain.ErrArenaNotDraft) {
		t.Fatalf("published update error = %v, want ErrArenaNotDraft", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app.arenas WHERE id = $1", mustUUID(t, published.ID().String())).Scan(&count); err != nil {
		t.Fatalf("count published: %v", err)
	}
	if count != 1 {
		t.Fatal("published Arena must never be deleted")
	}
}

// TestRepository_DraftsArePrivateAndConsumeNoPasses covers the P08-T03
// invariants: drafts never reach public projections and never touch the
// Arena Pass ledger.
func TestRepository_DraftsArePrivateAndConsumeNoPasses(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "arena-private@arena.example.com")
	createUseCase := application.NewCreateArenaDraftUseCase(repo, domain.DefaultStatementPolicy())
	updateUseCase := application.NewUpdateArenaDraftUseCase(repo, domain.DefaultStatementPolicy())
	deleteUseCase := application.NewDeleteArenaDraftUseCase(repo)

	draft, err := createUseCase.Execute(ctx, application.CreateArenaDraftCommand{
		AccountID: creator.String(),
		Statement: "A AGI existirá até 2040",
		Category:  "technology",
		Language:  "pt-BR",
	})
	if err != nil {
		t.Fatalf("create Execute() error = %v", err)
	}
	if _, err := updateUseCase.Execute(ctx, application.UpdateArenaDraftCommand{
		AccountID:       creator.String(),
		ArenaID:         draft.ID().String(),
		Statement:       "A AGI existirá até 2045",
		Category:        "science",
		Language:        "en-US",
		ExpectedVersion: draft.Version(),
	}); err != nil {
		t.Fatalf("update Execute() error = %v", err)
	}

	// No public projection can see the draft: nothing has left the draft
	// state and no public address was assigned.
	if got := countArenaRows(t, ctx, pool,
		"SELECT count(*) FROM app.arenas WHERE status <> 'draft'"); got != 0 {
		t.Fatalf("non-draft arenas = %d, want 0", got)
	}
	if got := countArenaRows(t, ctx, pool,
		"SELECT count(*) FROM app.arenas WHERE slug IS NOT NULL OR published_at IS NOT NULL"); got != 0 {
		t.Fatalf("drafts with a public address = %d, want 0", got)
	}

	// The ledger stays untouched: no operations, transactions or lots.
	if got := countArenaRows(t, ctx, pool, "SELECT count(*) FROM app.arena_pass_consumptions"); got != 0 {
		t.Fatalf("pass consumptions = %d, want 0", got)
	}
	if got := countArenaRows(t, ctx, pool, "SELECT count(*) FROM app.arena_pass_lots"); got != 0 {
		t.Fatalf("pass lots = %d, want 0", got)
	}
	if got := countArenaRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations"); got != 0 {
		t.Fatalf("wallet operations = %d, want 0", got)
	}

	// Deleting the draft is not a public mutation either.
	if err := deleteUseCase.Execute(ctx, application.DeleteArenaDraftCommand{
		AccountID: creator.String(),
		ArenaID:   draft.ID().String(),
	}); err != nil {
		t.Fatalf("delete Execute() error = %v", err)
	}
	if got := countArenaRows(t, ctx, pool, "SELECT count(*) FROM app.arenas"); got != 0 {
		t.Fatalf("arenas after delete = %d, want 0", got)
	}
}

func mustPublishSeed(t *testing.T, ctx context.Context, repo *postgres.Repository, creator domain.CreatorID, statement, slug string) *domain.Arena {
	t.Helper()
	draft, err := repo.CreateArena(ctx, mustCreateRequest(t, creator, statement, "", "technology", "pt-BR"))
	if err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	parsedSlug, err := domain.ParseSlug(slug)
	if err != nil {
		t.Fatalf("ParseSlug(%q): %v", slug, err)
	}
	published, err := repo.PublishArenaDraft(ctx, draft.ID(), creator, parsedSlug, time.Now().UTC(), draft.Version())
	if err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	return published
}

func TestRepository_CloseRestrictRemoveTransitions(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "arena-transitions@arena.example.com")
	intruder := mustArenaCreator(t, ctx, q, "arena-transitions-intruder@arena.example.com")

	arena := mustPublishSeed(t, ctx, repo, creator, "Arena publicada para fechamento", "close-transition-arena")

	closed, err := repo.CloseArena(ctx, arena.ID(), creator, arena.Version())
	if err != nil {
		t.Fatalf("CloseArena() error = %v", err)
	}
	if closed.Status() != domain.ArenaStatusClosed || closed.Version() != arena.Version()+1 {
		t.Fatalf("closed = status %s version %d", closed.Status(), closed.Version())
	}
	if closed.EnsureAcceptsParticipation(); !errors.Is(closed.EnsureAcceptsParticipation(), domain.ErrArenaNotOpen) {
		t.Fatal("a closed Arena must reject participation")
	}

	// Closing a closed Arena is not a transition.
	if _, err := repo.CloseArena(ctx, arena.ID(), creator, closed.Version()); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("second close error = %v, want ErrInvalidStatusChange", err)
	}

	// Stale version.
	stale := mustPublishSeed(t, ctx, repo, creator, "Arena para conflito de versão", "stale-version-arena")
	if _, err := repo.CloseArena(ctx, stale.ID(), creator, stale.Version()+1); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale close error = %v, want ErrVersionConflict", err)
	}

	// Foreign Arena is not found.
	if _, err := repo.CloseArena(ctx, stale.ID(), intruder, stale.Version()); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("foreign close error = %v, want ErrArenaNotFound", err)
	}

	// Restrict a closed Arena; retry on the restricted state is a conflict
	// at the repository level (the use case resolves it as a replay).
	restricted, err := repo.RestrictArena(ctx, arena.ID(), closed.Version())
	if err != nil {
		t.Fatalf("RestrictArena() error = %v", err)
	}
	if restricted.Status() != domain.ArenaStatusRestricted {
		t.Fatalf("status = %s, want restricted", restricted.Status())
	}
	if _, err := repo.RestrictArena(ctx, arena.ID(), restricted.Version()); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("second restrict error = %v, want ErrVersionConflict", err)
	}

	// Remove the restricted Arena; removed is terminal at the repository.
	removed, err := repo.RemoveArena(ctx, arena.ID(), restricted.Version())
	if err != nil {
		t.Fatalf("RemoveArena() error = %v", err)
	}
	if removed.Status() != domain.ArenaStatusRemoved {
		t.Fatalf("status = %s, want removed", removed.Status())
	}
	if _, err := repo.RemoveArena(ctx, arena.ID(), removed.Version()); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("second remove error = %v, want ErrVersionConflict", err)
	}

	// Drafts cannot be moderated or closed.
	draft, err := repo.CreateArena(ctx, mustCreateRequest(t, creator, "Rascunho fora da moderação", "", "culture", "pt-BR"))
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if _, err := repo.RestrictArena(ctx, draft.ID(), draft.Version()); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("draft restrict error = %v, want ErrInvalidStatusChange", err)
	}
	if _, err := repo.RemoveArena(ctx, draft.ID(), draft.Version()); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("draft remove error = %v, want ErrInvalidStatusChange", err)
	}
	if _, err := repo.CloseArena(ctx, draft.ID(), creator, draft.Version()); !errors.Is(err, domain.ErrInvalidStatusChange) {
		t.Fatalf("draft close error = %v, want ErrInvalidStatusChange", err)
	}

	// Unscoped lookup serves moderation; unknown ids are not found.
	if _, err := repo.GetArenaByID(ctx, arena.ID()); err != nil {
		t.Fatalf("GetArenaByID() error = %v", err)
	}
	if _, err := repo.GetArenaByID(ctx, "018f6b2a-0000-7000-8000-0000000000ff"); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("unknown GetArenaByID error = %v, want ErrArenaNotFound", err)
	}
}

type allowModerationAuthorizer struct {
	calls int
}

func (a *allowModerationAuthorizer) EnsureModerator(_ context.Context, _ domain.ModeratorID) error {
	a.calls++
	return nil
}

type denyModerationAuthorizer struct{}

func (denyModerationAuthorizer) EnsureModerator(_ context.Context, _ domain.ModeratorID) error {
	return application.ErrNotAuthorized
}

type captureModerationAudit struct {
	events []application.ModerationEvent
}

func (a *captureModerationAudit) RecordArenaModeration(_ context.Context, event application.ModerationEvent) error {
	a.events = append(a.events, event)
	return nil
}

func TestRepository_ModerationUseCasesEndToEnd(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "arena-moderation@arena.example.com")
	arena := mustPublishSeed(t, ctx, repo, creator, "Arena sob decisão de moderação", "moderation-arena")

	authorizer := &allowModerationAuthorizer{}
	audit := &captureModerationAudit{}
	clock := clockseed.NewClock()

	restrict := application.NewRestrictArenaUseCase(repo, authorizer, audit, clock)
	remove := application.NewRemoveArenaUseCase(repo, authorizer, audit, clock)

	command := application.ModerateArenaCommand{
		ActorAccountID: "018f6b2a-0000-7000-8000-000000000099",
		ArenaID:        arena.ID().String(),
		Reason:         "decisão registrada no caso 42",
	}

	restricted, err := restrict.Execute(ctx, command)
	if err != nil {
		t.Fatalf("restrict Execute() error = %v", err)
	}
	if restricted.Arena.Status() != domain.ArenaStatusRestricted {
		t.Fatalf("status = %s, want restricted", restricted.Arena.Status())
	}
	stored, err := repo.GetArenaByID(ctx, arena.ID())
	if err != nil {
		t.Fatalf("reload moderated arena: %v", err)
	}
	if stored.Status() != domain.ArenaStatusRestricted {
		t.Fatalf("stored status = %s, want restricted", stored.Status())
	}
	if len(audit.events) != 1 || audit.events[0].Action != application.ModerationRestrict {
		t.Fatalf("audit events = %+v", audit.events)
	}

	removed, err := remove.Execute(ctx, command)
	if err != nil {
		t.Fatalf("remove Execute() error = %v", err)
	}
	if removed.Arena.Status() != domain.ArenaStatusRemoved {
		t.Fatalf("status = %s, want removed", removed.Arena.Status())
	}
	if len(audit.events) != 2 || audit.events[1].Action != application.ModerationRemove {
		t.Fatalf("audit events = %+v", audit.events)
	}

	// Negative authorization: nothing changes and nothing is audited.
	other := mustPublishSeed(t, ctx, repo, creator, "Arena protegida da moderação", "protected-moderation-arena")
	denied := application.NewRestrictArenaUseCase(repo, denyModerationAuthorizer{}, audit, clock)
	if _, err := denied.Execute(ctx, application.ModerateArenaCommand{
		ActorAccountID: "018f6b2a-0000-7000-8000-000000000098",
		ArenaID:        other.ID().String(),
		Reason:         "tentativa sem autorização",
	}); !errors.Is(err, application.ErrNotAuthorized) {
		t.Fatalf("denied execute error = %v, want ErrNotAuthorized", err)
	}
	unchanged, err := repo.GetArenaByID(ctx, other.ID())
	if err != nil {
		t.Fatalf("reload protected arena: %v", err)
	}
	if unchanged.Status() != domain.ArenaStatusPublished {
		t.Fatalf("status after denial = %s, want published", unchanged.Status())
	}
	if len(audit.events) != 2 {
		t.Fatalf("audit events after denial = %d, want 2", len(audit.events))
	}
}
