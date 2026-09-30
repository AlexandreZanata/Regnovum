package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/search/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/search/application"
)

func TestPublicSearchIndexedLanguageAndModerationExclusion(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	pool := db.Pool.Pool()
	repo := postgres.NewRepository(pool)
	var account, arenaPT, arenaEN pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO app.accounts (email, status) VALUES ($1, 'active') RETURNING id`, "search-owner@arena.example.com").Scan(&account); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO app.arenas (creator_id, slug, statement, context, category, language, status, published_at) VALUES ($1, 'search-pt', 'A fusão nuclear será energia limpa', 'tecnologia e energia', 'technology', 'pt-BR', 'published', now()) RETURNING id`, account).Scan(&arenaPT); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO app.arenas (creator_id, slug, statement, context, category, language, status, published_at) VALUES ($1, 'search-en', 'Fusion energy will become affordable', 'science and technology', 'science', 'en-US', 'published', now()) RETURNING id`, account).Scan(&arenaEN); err != nil {
		t.Fatal(err)
	}
	var removedArena pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO app.arenas (creator_id, slug, statement, category, language, status, published_at) VALUES ($1, 'search-removed', 'fusão nuclear secreta', 'technology', 'pt-BR', 'removed', now()) RETURNING id`, account).Scan(&removedArena); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.arenas (creator_id, slug, statement, category, language, status, published_at) SELECT $1, 'search-extra-' || n, 'Conteúdo sintético para o índice de busca', 'technology', 'pt-BR', 'published', now() FROM generate_series(1, 1000) AS n`, account); err != nil {
		t.Fatal(err)
	}
	insertArgument := func(arena pgtype.UUID, content, status, key string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost, status, idempotency_key) VALUES ($1, $2, 'support', $3, $4, 10, $5, $6)`, arena, account, content, "search-hash-"+key, status, key); err != nil {
			t.Fatal(err)
		}
	}
	insertArgument(arenaPT, "A fusão é uma fonte de energia", "published", "published-pt")
	insertArgument(arenaPT, "A fusão retirada não deve aparecer", "withdrawn", "withdrawn-pt")
	insertArgument(arenaPT, "A fusão removida não deve aparecer", "removed", "removed-pt")
	insertArgument(arenaEN, "Fusion power is promising", "published", "published-en")

	codec, err := application.NewCursorCodec([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	arenaUseCase, err := application.NewArenaSearchUseCase(repo, codec)
	if err != nil {
		t.Fatal(err)
	}
	argumentUseCase, err := application.NewArgumentSearchUseCase(repo, codec)
	if err != nil {
		t.Fatal(err)
	}

	pt, err := arenaUseCase.Execute(ctx, "fusão", "pt-BR", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pt.Items) != 1 || pt.Items[0].ID != arenaPT.String() {
		t.Fatalf("Portuguese arenas = %+v, want only the published Portuguese Arena", pt.Items)
	}
	en, err := arenaUseCase.Execute(ctx, "fusion", "en-US", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(en.Items) != 1 || en.Items[0].ID != arenaEN.String() {
		t.Fatalf("English arenas = %+v, want only the English Arena", en.Items)
	}
	args, err := argumentUseCase.Execute(ctx, "fusão", "pt-BR", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(args.Items) != 1 || !strings.Contains(args.Items[0].Content, "fonte") {
		t.Fatalf("Portuguese arguments = %+v, want only the published argument", args.Items)
	}

	// PostgreSQL receives the user term as a parameter, not interpolated SQL;
	// websearch syntax is deliberately harmless and returns no rows.
	injection, err := arenaUseCase.Execute(ctx, `fusão'); DROP TABLE app.arenas; --`, "pt-BR", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(injection.Items) != 0 {
		t.Fatalf("injection-shaped query returned %+v", injection.Items)
	}
	var tableExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('app.arenas') IS NOT NULL`).Scan(&tableExists); err != nil || !tableExists {
		t.Fatalf("arenas table existence = %v, err = %v", tableExists, err)
	}

	var indexExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = 'app' AND indexname = 'arenas_public_search_pt_fts_idx')`).Scan(&indexExists); err != nil || !indexExists {
		t.Fatalf("arenas GIN index exists = %v, err = %v", indexExists, err)
	}
	var indexDefinition string
	if err := pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = 'app' AND indexname = 'arenas_public_search_pt_fts_idx'`).Scan(&indexDefinition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(indexDefinition), "using gin") {
		t.Fatalf("index definition=%q, want the public Portuguese GIN index", indexDefinition)
	}

	// The cursor is keyset-bound to one search kind and cannot be reused for
	// the other result set.
	if pt.NextCursor != "" {
		if _, err := arenaUseCase.Execute(ctx, "fusão", "pt-BR", pt.NextCursor, 10); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublicSearchArgumentStatusAndIndexMetadata(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	var indexes int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'app' AND indexname IN ('arguments_public_search_pt_fts_idx', 'arguments_public_search_en_fts_idx')`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 2 {
		t.Fatalf("argument search indexes = %d, want 2", indexes)
	}
	var version string
	if err := db.QueryRow(ctx, `SELECT version()`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(version, "PostgreSQL") {
		t.Fatalf("database version = %q, want PostgreSQL", version)
	}
}
