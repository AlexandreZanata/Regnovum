package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	arenaspg "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

var testFeedSecret = []byte("arena-feed-cursor-secret-0123456789")

func mustPublicArena(
	t *testing.T,
	ctx context.Context,
	repo *arenaspg.Repository,
	creator domain.CreatorID,
	statement, slug, language, category string,
	publishedAt time.Time,
) *domain.Arena {
	t.Helper()
	draft, err := repo.CreateArena(ctx, mustCreateRequest(t, creator, statement, "", category, language))
	if err != nil {
		t.Fatalf("create seed arena %s: %v", slug, err)
	}
	parsedSlug, err := domain.ParseSlug(slug)
	if err != nil {
		t.Fatalf("ParseSlug(%q): %v", slug, err)
	}
	published, err := repo.PublishArenaDraft(ctx, draft.ID(), creator, parsedSlug, publishedAt.UTC(), draft.Version())
	if err != nil {
		t.Fatalf("publish seed arena %s: %v", slug, err)
	}
	return published
}

func newFeedUseCase(t *testing.T, repo arenasapp.ArenaFeedRepository) *arenasapp.GetArenaFeedUseCase {
	t.Helper()
	codec, err := arenasapp.NewFeedCursorCodec(testFeedSecret)
	if err != nil {
		t.Fatalf("build feed codec: %v", err)
	}
	return arenasapp.NewGetArenaFeedUseCase(repo, codec)
}

func TestRepository_PublicFeedKeysetWithIdenticalTimestamps(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := arenaspg.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "arena-feed@arena.example.com")
	instant := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	const published = 7
	for i := 0; i < published; i++ {
		// Every Arena shares the exact same publication instant: only the
		// (published_at, id) tuple keeps the page stable.
		mustPublicArena(t, ctx, repo, creator,
			fmt.Sprintf("Afirmação sintética número %d", i),
			fmt.Sprintf("feed-tied-%02d", i), "pt-BR", "technology", instant)
	}

	// Drafts and removed Arenas must never appear.
	if _, err := repo.CreateArena(ctx, mustCreateRequest(t, creator, "Rascunho invisível no feed", "", "technology", "pt-BR")); err != nil {
		t.Fatalf("create draft: %v", err)
	}
	removedSeed := mustPublicArena(t, ctx, repo, creator, "Arena removida do feed", "feed-removed", "pt-BR", "technology", instant)
	if _, err := repo.RemoveArena(ctx, removedSeed.ID(), removedSeed.Version()); err != nil {
		t.Fatalf("remove seed: %v", err)
	}

	useCase := newFeedUseCase(t, repo)
	seen := make(map[string]bool, published)
	cursor := ""
	pages := 0
	for {
		feed, err := useCase.Execute(ctx, arenasapp.GetArenaFeedCommand{Cursor: cursor, Limit: 3})
		if err != nil {
			t.Fatalf("page %d error = %v", pages, err)
		}
		pages++
		for _, arena := range feed.Arenas {
			if arena.Status() == domain.ArenaStatusDraft {
				t.Fatalf("SECURITY VIOLATION: draft %q appeared in the public feed", arena.ID())
			}
			if seen[arena.ID().String()] {
				t.Fatalf("duplicate entry %q across pages", arena.ID())
			}
			seen[arena.ID().String()] = true
		}
		if feed.NextCursor == "" {
			break
		}
		cursor = feed.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}

	if len(seen) != published {
		t.Fatalf("entries delivered = %d, want %d (no duplicates, no gaps, no hidden statuses)", len(seen), published)
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3 for limit 3 and %d entries", pages, published)
	}
}

func TestRepository_PublicFeedFiltersAndVisibility(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := arenaspg.NewRepository(pool)
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "arena-feed-filters@arena.example.com")
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	mustPublicArena(t, ctx, repo, creator, "Arena publicada em português", "filter-pt-published", "pt-BR", "technology", base)
	mustPublicArena(t, ctx, repo, creator, "Arena published in english", "filter-en-published", "en-US", "technology", base.Add(-time.Minute))
	closed := mustPublicArena(t, ctx, repo, creator, "Arena fechada de ciência", "filter-pt-closed", "pt-BR", "science", base.Add(-2*time.Minute))
	if _, err := repo.CloseArena(ctx, closed.ID(), creator, closed.Version()); err != nil {
		t.Fatalf("close seed: %v", err)
	}
	restricted := mustPublicArena(t, ctx, repo, creator, "Arena restrita de cultura", "filter-en-restricted", "en-US", "culture", base.Add(-3*time.Minute))
	if _, err := repo.RestrictArena(ctx, restricted.ID(), restricted.Version()); err != nil {
		t.Fatalf("restrict seed: %v", err)
	}
	removed := mustPublicArena(t, ctx, repo, creator, "Arena removida de tecnologia", "filter-pt-removed", "pt-BR", "technology", base.Add(-4*time.Minute))
	if _, err := repo.RemoveArena(ctx, removed.ID(), removed.Version()); err != nil {
		t.Fatalf("remove seed: %v", err)
	}
	if _, err := repo.CreateArena(ctx, mustCreateRequest(t, creator, "Rascunho de tecnologia em português", "", "technology", "pt-BR")); err != nil {
		t.Fatalf("create draft: %v", err)
	}

	useCase := newFeedUseCase(t, repo)
	fetch := func(t *testing.T, cmd arenasapp.GetArenaFeedCommand) []domain.Arena {
		t.Helper()
		feed, err := useCase.Execute(ctx, cmd)
		if err != nil {
			t.Fatalf("Execute(%+v) error = %v", cmd, err)
		}
		return feed.Arenas
	}

	all := fetch(t, arenasapp.GetArenaFeedCommand{Limit: 100})
	if len(all) != 4 {
		t.Fatalf("unfiltered feed = %d entries, want 4 (published, closed and restricted only)", len(all))
	}
	for _, arena := range all {
		if arena.Status() == domain.ArenaStatusRemoved || arena.Status() == domain.ArenaStatusDraft {
			t.Fatalf("SECURITY VIOLATION: %s arena in the public feed", arena.Status())
		}
	}

	portuguese := fetch(t, arenasapp.GetArenaFeedCommand{Language: "pt-BR", Limit: 100})
	if len(portuguese) != 2 {
		t.Fatalf("pt-BR feed = %d entries, want 2 (published + closed)", len(portuguese))
	}
	for _, arena := range portuguese {
		if arena.Language().String() != "pt-BR" {
			t.Fatalf("language filter leaked %q", arena.Language())
		}
	}

	science := fetch(t, arenasapp.GetArenaFeedCommand{Category: "science", Limit: 100})
	if len(science) != 1 || science[0].Category().String() != "science" {
		t.Fatalf("science feed = %+v, want the closed science arena", science)
	}

	closedOnly := fetch(t, arenasapp.GetArenaFeedCommand{Status: "closed", Limit: 100})
	if len(closedOnly) != 1 || closedOnly[0].Status() != domain.ArenaStatusClosed {
		t.Fatalf("closed feed = %+v, want exactly the closed arena", closedOnly)
	}

	restrictedOnly := fetch(t, arenasapp.GetArenaFeedCommand{Status: "restricted", Limit: 100})
	if len(restrictedOnly) != 1 || restrictedOnly[0].Status() != domain.ArenaStatusRestricted {
		t.Fatalf("restricted feed = %+v, want exactly the restricted arena", restrictedOnly)
	}

	combined := fetch(t, arenasapp.GetArenaFeedCommand{Language: "pt-BR", Category: "technology", Limit: 100})
	if len(combined) != 1 || combined[0].Slug().String() != "filter-pt-published" {
		t.Fatalf("combined filter = %+v, want only the published pt-BR technology arena", combined)
	}
}

// TestRepository_PublicFeedUsesIndexOnSyntheticVolume proves the keyset feed
// is served by an index at volume instead of a full scan plus sort.
func TestRepository_PublicFeedUsesIndexOnSyntheticVolume(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)

	creator := mustArenaCreator(t, ctx, q, "arena-feed-volume@arena.example.com")
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at, version)
		SELECT $1,
		       'Synthetic arena ' || g,
		       CASE WHEN g % 3 = 0 THEN 'science' ELSE 'technology' END,
		       CASE WHEN g % 2 = 0 THEN 'pt-BR' ELSE 'en-US' END,
		       'published',
		       'synthetic-volume-' || g,
		       now() - (g || ' minutes')::interval,
		       2
		FROM generate_series(1, 5000) AS g`, creator.String()); err != nil {
		t.Fatalf("seed synthetic volume: %v", err)
	}
	if _, err := pool.Exec(ctx, "ANALYZE app.arenas"); err != nil {
		t.Fatalf("analyze arenas: %v", err)
	}

	unfilteredPlan := explainPlan(t, ctx, pool, `
		EXPLAIN
		SELECT id, creator_id, slug, statement, context, category, language, status, version, created_at, published_at, closes_at
		FROM app.arenas
		WHERE status IN ('published', 'closed', 'restricted')
		  AND (NULL::text IS NULL OR language = NULL::text)
		  AND (NULL::text IS NULL OR category = NULL::text)
		  AND (NULL::text IS NULL OR status = NULL::text)
		  AND (NULL::timestamptz IS NULL OR (published_at, id) < (NULL::timestamptz, NULL::uuid))
		ORDER BY published_at DESC, id DESC
		LIMIT 21`)
	if !strings.Contains(unfilteredPlan, "arenas_public_feed_idx") {
		t.Fatalf("unfiltered feed plan does not use its partial index:\n%s", unfilteredPlan)
	}
	if strings.Contains(unfilteredPlan, "Seq Scan") {
		t.Fatalf("unfiltered feed plan falls back to a sequential scan:\n%s", unfilteredPlan)
	}

	filteredPlan := explainPlan(t, ctx, pool, `
		EXPLAIN
		SELECT id, creator_id, slug, statement, context, category, language, status, version, created_at, published_at, closes_at
		FROM app.arenas
		WHERE status IN ('published', 'closed', 'restricted')
		  AND (language = 'pt-BR')
		ORDER BY published_at DESC, id DESC
		LIMIT 21`)
	if strings.Contains(filteredPlan, "Seq Scan") {
		t.Fatalf("language-filtered feed plan falls back to a sequential scan:\n%s", filteredPlan)
	}
	if !strings.Contains(filteredPlan, "Index") {
		t.Fatalf("language-filtered feed plan does not use an index:\n%s", filteredPlan)
	}
}

func explainPlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, statement string) string {
	t.Helper()
	rows, err := pool.Query(ctx, statement)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var builder strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}
	return builder.String()
}
