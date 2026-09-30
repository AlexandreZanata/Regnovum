package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func mustQueryCodec(t *testing.T) *application.ArgumentCursorCodec {
	t.Helper()
	codec, err := application.NewArgumentCursorCodec([]byte("arguments-query-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("NewArgumentCursorCodec: %v", err)
	}
	return codec
}

// insertArgumentRow seeds one argument through raw SQL with a valid
// canonical hash; the write use cases already have their own coverage.
func insertArgumentRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, authorID pgtype.UUID, parentID pgtype.UUID, relation, content, status string, createdAt time.Time) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, parent_id, relation, content, content_hash, grapheme_cost, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		RETURNING id`,
		arenaID, authorID, parentID, relation, content, domain.HashContent(content).String(), len([]rune(content)), status, createdAt,
	).Scan(&id); err != nil {
		t.Fatalf("insert argument %q: %v", content, err)
	}
	return id
}

func TestPublicArgumentListsPaginateWithTiesAndCounts(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)
	repo := postgres.NewRepository(pool)
	codec := mustQueryCodec(t)

	author := mustArgumentAuthor(t, ctx, q, "arguments-query@arena.example.com")
	arena := mustArgumentArena(t, ctx, pool, author, "arguments-query-arena")

	// Five top-level arguments with the SAME created_at (ties) plus one
	// withdrawn and one removed argument that must never appear.
	tie := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	seeded := make([]pgtype.UUID, 0, 5)
	for index := 0; index < 5; index++ {
		seeded = append(seeded, insertArgumentRow(t, ctx, pool, arena, author, pgtype.UUID{},
			domain.RelationSupport, fmt.Sprintf("Argumento publicado número %d", index), "published", tie))
	}
	insertArgumentRow(t, ctx, pool, arena, author, pgtype.UUID{},
		domain.RelationSupport, "Argumento retirado pelo autor", "withdrawn", tie)
	insertArgumentRow(t, ctx, pool, arena, author, pgtype.UUID{},
		domain.RelationSupport, "Argumento removido pela moderação", "removed", tie)
	// Another relation must not leak into the support list.
	insertArgumentRow(t, ctx, pool, arena, author, pgtype.UUID{},
		domain.RelationOppose, "Argumento da relação oposta", "published", tie)

	// Two published replies and one withdrawn reply on the first argument.
	parent := seeded[0]
	insertArgumentRow(t, ctx, pool, arena, author, parent, domain.RelationOppose, "Primeira resposta publicada", "published", tie.Add(time.Minute))
	insertArgumentRow(t, ctx, pool, arena, author, parent, domain.RelationContext, "Segunda resposta publicada", "published", tie.Add(2*time.Minute))
	insertArgumentRow(t, ctx, pool, arena, author, parent, domain.RelationContext, "Resposta retirada", "withdrawn", tie.Add(3*time.Minute))

	list := application.NewListArenaArgumentsUseCase(repo, codec)
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	replyCounts := map[string]int64{}
	for {
		page, err := list.Execute(ctx, application.ListArenaArgumentsQuery{
			ArenaID:  uuidText(arena),
			Relation: domain.RelationSupport,
			Cursor:   cursor,
			Limit:    2,
		})
		if err != nil {
			t.Fatalf("page %d Execute() error = %v", pages, err)
		}
		pages++
		for _, argument := range page.Arguments {
			if seen[argument.ID.String()] {
				t.Fatalf("duplicate argument %s across pages", argument.ID)
			}
			seen[argument.ID.String()] = true
			if argument.Status != "published" || argument.Content == nil {
				t.Fatalf("listed argument = %+v, want published content", argument)
			}
			if argument.Relation.String() != domain.RelationSupport {
				t.Fatalf("relation = %q, want support only", argument.Relation.String())
			}
			replyCounts[argument.ID.String()] = argument.ReplyCount
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 5 || pages != 3 {
		t.Fatalf("delivered %d arguments in %d pages, want 5 in 3", len(seen), pages)
	}
	if replyCounts[parent.String()] != 2 {
		t.Fatalf("parent reply count = %d, want the two published replies", replyCounts[parent.String()])
	}

	// The replies list returns published replies only, newest first.
	replies := application.NewListRepliesUseCase(repo, codec)
	page, err := replies.Execute(ctx, application.ListRepliesQuery{ParentID: parent.String(), Limit: 10})
	if err != nil {
		t.Fatalf("replies Execute() error = %v", err)
	}
	if len(page.Arguments) != 2 {
		t.Fatalf("replies = %d, want the two published ones", len(page.Arguments))
	}
	if page.Arguments[0].Content.String() != "Segunda resposta publicada" {
		t.Fatalf("first reply = %q, want newest first", page.Arguments[0].Content.String())
	}
	for _, reply := range page.Arguments {
		if reply.ParentID.String() != parent.String() || reply.ReplyCount != 0 {
			t.Fatalf("reply = %+v, want the parent link and no derived count", reply)
		}
	}
}

func TestPublicArgumentVisibilityPolicy(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)
	repo := postgres.NewRepository(pool)

	author := mustArgumentAuthor(t, ctx, q, "arguments-visibility@arena.example.com")
	arena := mustArgumentArena(t, ctx, pool, author, "arguments-visibility-arena")

	published := insertArgumentRow(t, ctx, pool, arena, author, pgtype.UUID{}, domain.RelationSupport, "Argumento publicado e visível", "published", time.Now().UTC())
	withdrawn := insertArgumentRow(t, ctx, pool, arena, author, pgtype.UUID{}, domain.RelationSupport, "Argumento retirado do público", "withdrawn", time.Now().UTC())
	removed := insertArgumentRow(t, ctx, pool, arena, author, pgtype.UUID{}, domain.RelationSupport, "Argumento removido do público", "removed", time.Now().UTC())

	get := application.NewGetPublicArgumentUseCase(repo)
	visible, err := get.Execute(ctx, application.GetPublicArgumentQuery{ArgumentID: published.String()})
	if err != nil {
		t.Fatalf("published get error = %v", err)
	}
	if visible.Content == nil || visible.Content.String() != "Argumento publicado e visível" {
		t.Fatalf("published projection = %+v, want its content", visible)
	}

	retracted, err := get.Execute(ctx, application.GetPublicArgumentQuery{ArgumentID: withdrawn.String()})
	if err != nil {
		t.Fatalf("withdrawn get error = %v", err)
	}
	if retracted.Content != nil || retracted.Status != "withdrawn" {
		t.Fatalf("withdrawn projection = %+v, want the retracted placeholder", retracted)
	}

	if _, err := get.Execute(ctx, application.GetPublicArgumentQuery{ArgumentID: removed.String()}); !errors.Is(err, application.ErrArgumentNotFound) {
		t.Fatalf("removed get error = %v, want ErrArgumentNotFound", err)
	}
}

// TestPublicArgumentQueriesUseIndexes mirrors the repository statements and
// proves with EXPLAIN that the keyset lists walk their indexes instead of
// scanning the table, over a synthetic volume of 5.000 arguments.
func TestPublicArgumentQueriesUseIndexes(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	author := mustArgumentAuthor(t, ctx, q, "arguments-explain@arena.example.com")
	arena := mustArgumentArena(t, ctx, pool, author, "arguments-explain-arena")
	parent := insertArgumentRow(t, ctx, pool, arena, author, pgtype.UUID{}, domain.RelationSupport, "Argumento raiz do volume sintético", "published", time.Now().UTC())

	const syntheticHash = "v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, parent_id, relation, content, content_hash, grapheme_cost, status, created_at, updated_at)
		SELECT
		    $1, $2,
		    CASE WHEN series % 4 = 0 THEN $3::uuid ELSE NULL::uuid END,
		    (ARRAY['support', 'oppose', 'context'])[1 + (series % 3)],
		    'Conteúdo sintético número ' || series,
		    $4, 20,
		    CASE WHEN series % 10 = 0 THEN 'withdrawn' ELSE 'published' END,
		    now() - (series || ' seconds')::interval,
		    now() - (series || ' seconds')::interval
		FROM generate_series(1, 5000) AS series`,
		arena, author, parent, syntheticHash); err != nil {
		t.Fatalf("seed synthetic volume: %v", err)
	}

	arenaListQuery := `
		EXPLAIN (ANALYZE, BUFFERS)
		SELECT
		    a.id, a.arena_id, a.author_id, a.parent_id, a.relation, a.content,
		    a.content_hash, a.grapheme_cost, a.status, a.created_at, a.updated_at, a.withdrawn_at,
		    (SELECT count(*) FROM app.arguments reply WHERE reply.parent_id = a.id AND reply.status = 'published')::bigint AS reply_count
		FROM app.arguments a
		WHERE a.arena_id = $1
		  AND a.relation = 'support'
		  AND a.parent_id IS NULL
		  AND a.status = 'published'
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT 20`
	plan := explainPlan(t, ctx, pool, arenaListQuery, arena)
	if strings.Contains(plan, "Seq Scan on app.arguments a") {
		t.Fatalf("arena list falls back to a sequential scan:\n%s", plan)
	}
	if !strings.Contains(plan, "Index") {
		t.Fatalf("arena list plan shows no index usage:\n%s", plan)
	}
	t.Logf("arena list plan:\n%s", plan)

	repliesQuery := `
		EXPLAIN (ANALYZE, BUFFERS)
		SELECT
		    a.id, a.arena_id, a.author_id, a.parent_id, a.relation, a.content,
		    a.content_hash, a.grapheme_cost, a.status, a.created_at, a.updated_at, a.withdrawn_at
		FROM app.arguments a
		WHERE a.parent_id = $1
		  AND a.status = 'published'
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT 20`
	plan = explainPlan(t, ctx, pool, repliesQuery, parent)
	if strings.Contains(plan, "Seq Scan on app.arguments a") {
		t.Fatalf("replies list falls back to a sequential scan:\n%s", plan)
	}
	if !strings.Contains(plan, "Index") {
		t.Fatalf("replies list plan shows no index usage:\n%s", plan)
	}
	t.Logf("replies list plan:\n%s", plan)
}

// explainPlan runs one EXPLAIN statement and returns its plan lines.
func explainPlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, statement string, argument any) string {
	t.Helper()
	rows, err := pool.Query(ctx, statement, argument)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan line: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}
	return plan.String()
}
