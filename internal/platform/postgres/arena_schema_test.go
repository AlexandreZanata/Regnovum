package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func mustArenaCreator(t *testing.T, ctx context.Context, q *postgres.Queries, email string) pgtype.UUID {
	t.Helper()
	acc := mustCreateAccount(t, ctx, q, email)
	return acc.ID
}

// insertDraftArena creates a minimal valid draft through raw SQL; the arenas
// query layer arrives with P08-T03.
func insertDraftArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator pgtype.UUID, statement, category, language string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, $2, $3, $4, 'draft')
		RETURNING id`, creator, statement, category, language).Scan(&id); err != nil {
		t.Fatalf("insert draft arena: %v", err)
	}
	return id
}

func publishArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID pgtype.UUID, slug string, publishedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE app.arenas
		SET status = 'published', slug = $2, published_at = $3, version = version + 1
		WHERE id = $1`, arenaID, slug, publishedAt); err != nil {
		t.Fatalf("publish arena: %v", err)
	}
}

func TestArenaSchemaConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "arena-schema@arena.example.com")
	now := time.Now().UTC()

	var categories int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app.categories").Scan(&categories); err != nil {
		t.Fatalf("count categories: %v", err)
	}
	if categories < 8 {
		t.Fatalf("seeded categories = %d, want at least 8", categories)
	}

	_, err := pool.Exec(ctx, "INSERT INTO app.categories (slug) VALUES ('Bad Slug')")
	assertPgCode(t, err, "23514")

	// Drafts need no slug and no publication instant.
	insertDraftArena(t, ctx, pool, creator, "A afirmação de teste do schema", "technology", "pt-BR")

	validSlug := "valid-arena-slug"
	future := now.Add(24 * time.Hour)
	probes := []struct {
		name      string
		statement string
		category  string
		language  string
		status    string
		slug      *string
		published *time.Time
		closes    *time.Time
		version   int
		wantCode  string
	}{
		{name: "unknown language", statement: "Uma afirmação", category: "technology", language: "es-ES", status: "draft", version: 1, wantCode: "23514"},
		{name: "non canonical language", statement: "Uma afirmação", category: "technology", language: "pt-br", status: "draft", version: 1, wantCode: "23514"},
		{name: "unknown category", statement: "Uma afirmação", category: "unknown", language: "pt-BR", status: "draft", version: 1, wantCode: "23503"},
		{name: "unknown status", statement: "Uma afirmação", category: "technology", language: "pt-BR", status: "archived", version: 1, wantCode: "23514"},
		{name: "blank statement", statement: "   ", category: "technology", language: "pt-BR", status: "draft", version: 1, wantCode: "23514"},
		{name: "oversized statement", statement: strings.Repeat("a", 2001), category: "technology", language: "pt-BR", status: "draft", version: 1, wantCode: "23514"},
		{name: "zero version", statement: "Uma afirmação", category: "technology", language: "pt-BR", status: "draft", version: 0, wantCode: "23514"},
		{name: "published without date", statement: "Uma afirmação", category: "technology", language: "pt-BR", status: "published", slug: &validSlug, version: 1, wantCode: "23514"},
		{name: "draft with publication date", statement: "Uma afirmação", category: "technology", language: "pt-BR", status: "draft", published: &now, version: 1, wantCode: "23514"},
		{name: "draft with slug", statement: "Uma afirmação", category: "technology", language: "pt-BR", status: "draft", slug: &validSlug, version: 1, wantCode: "23514"},
		{name: "closes without publication", statement: "Uma afirmação", category: "technology", language: "pt-BR", status: "draft", closes: &future, version: 1, wantCode: "23514"},
		{name: "closes before publication", statement: "Uma afirmação", category: "technology", language: "pt-BR", status: "published", slug: &validSlug, published: &now, closes: &now, version: 1, wantCode: "23514"},
		{name: "invalid slug format", statement: "Uma afirmação", category: "technology", language: "pt-BR", status: "published", slug: strPtr("Invalid Slug"), published: &now, version: 1, wantCode: "23514"},
		{name: "short slug", statement: "Uma afirmação", category: "technology", language: "pt-BR", status: "published", slug: strPtr("ab"), published: &now, version: 1, wantCode: "23514"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at, closes_at, version)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				creator, probe.statement, probe.category, probe.language, probe.status,
				textOrNil(probe.slug), timeOrNil(probe.published), timeOrNil(probe.closes), probe.version)
			assertPgCode(t, err, probe.wantCode)
		})
	}

	// Orphan creators are refused.
	orphan := pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c}, Valid: true}
	_, err = pool.Exec(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Uma afirmação', 'technology', 'pt-BR', 'draft')`, orphan)
	assertPgCode(t, err, "23503")

	// Slugs are unique once published.
	first := insertDraftArena(t, ctx, pool, creator, "Primeira afirmação publicada", "technology", "pt-BR")
	publishArena(t, ctx, pool, first, "unique-arena-slug", now)
	second := insertDraftArena(t, ctx, pool, creator, "Segunda afirmação publicada", "technology", "pt-BR")
	_, err = pool.Exec(ctx, `
		UPDATE app.arenas SET status = 'published', slug = 'unique-arena-slug', published_at = $2
		WHERE id = $1`, second, now)
	assertPgCode(t, err, "23505")
	_, err = pool.Exec(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		VALUES ($1, 'Terceira afirmação', 'technology', 'pt-BR', 'published', 'unique-arena-slug', $2)`, creator, now)
	assertPgCode(t, err, "23505")
}

func TestArenaDraftEditingAndPublishedImmutability(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "arena-immutable@arena.example.com")
	now := time.Now().UTC()

	arenaID := insertDraftArena(t, ctx, pool, creator, "Afirmação inicial do rascunho", "technology", "pt-BR")

	// Drafts are editable.
	if _, err := pool.Exec(ctx,
		"UPDATE app.arenas SET statement = $2, language = 'en-US' WHERE id = $1", arenaID,
		"Afirmação revisada do rascunho"); err != nil {
		t.Fatalf("draft edit rejected: %v", err)
	}

	publishArena(t, ctx, pool, arenaID, "immutable-arena", now)

	probes := []struct {
		name string
		sql  string
	}{
		{name: "statement", sql: "UPDATE app.arenas SET statement = 'Outra afirmação' WHERE id = $1"},
		{name: "language", sql: "UPDATE app.arenas SET language = 'pt-BR' WHERE id = $1"},
		{name: "slug", sql: "UPDATE app.arenas SET slug = 'other-slug' WHERE id = $1"},
		{name: "published_at", sql: "UPDATE app.arenas SET published_at = now() WHERE id = $1"},
		{name: "creator", sql: "UPDATE app.arenas SET creator_id = gen_random_uuid() WHERE id = $1"},
		{name: "created_at", sql: "UPDATE app.arenas SET created_at = now() WHERE id = $1"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, probe.sql, arenaID)
			assertPgCode(t, err, "23514")
		})
	}

	// Lifecycle fields remain mutable after publication.
	closes := now.Add(48 * time.Hour)
	if _, err := pool.Exec(ctx, `
		UPDATE app.arenas
		SET context = 'Contexto revisado com data pública', category = 'science',
		    closes_at = $2, status = 'closed', version = version + 1
		WHERE id = $1`, arenaID, closes); err != nil {
		t.Fatalf("lifecycle update rejected: %v", err)
	}

	// Published arenas are never deleted; drafts are.
	_, err := pool.Exec(ctx, "DELETE FROM app.arenas WHERE id = $1", arenaID)
	assertPgCode(t, err, "23514")

	draftID := insertDraftArena(t, ctx, pool, creator, "Rascunho descartável", "culture", "pt-BR")
	if _, err := pool.Exec(ctx, "DELETE FROM app.arenas WHERE id = $1", draftID); err != nil {
		t.Fatalf("draft deletion rejected: %v", err)
	}
}

func TestArenaRelationsConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "arena-relations@arena.example.com")
	now := time.Now().UTC()

	previousID := insertDraftArena(t, ctx, pool, creator, "Arena anterior encerrada", "philosophy", "pt-BR")
	publishArena(t, ctx, pool, previousID, "previous-arena", now)
	nextID := insertDraftArena(t, ctx, pool, creator, "Arena nova que continua o debate", "philosophy", "pt-BR")
	publishArena(t, ctx, pool, nextID, "next-arena", now)

	var relationID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arena_relations (arena_id, related_arena_id, relation)
		VALUES ($1, $2, 'SUPERSEDES')
		RETURNING id`, nextID, previousID).Scan(&relationID); err != nil {
		t.Fatalf("valid relation rejected: %v", err)
	}

	probes := []struct {
		name      string
		arenaID   pgtype.UUID
		relatedID pgtype.UUID
		relation  string
		wantCode  string
	}{
		{name: "self relation", arenaID: nextID, relatedID: nextID, relation: "CONTINUES", wantCode: "23514"},
		{name: "duplicate relation", arenaID: nextID, relatedID: previousID, relation: "SUPERSEDES", wantCode: "23505"},
		{name: "unknown kind", arenaID: nextID, relatedID: previousID, relation: "RELATES", wantCode: "23514"},
		{name: "unknown related arena", arenaID: nextID, relatedID: pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c}, Valid: true}, relation: "CONTINUES", wantCode: "23503"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO app.arena_relations (arena_id, related_arena_id, relation)
				VALUES ($1, $2, $3)`, probe.arenaID, probe.relatedID, probe.relation)
			assertPgCode(t, err, probe.wantCode)
		})
	}

	if _, err := pool.Exec(ctx, "DELETE FROM app.arena_relations WHERE id = $1", relationID); err != nil {
		t.Fatalf("relation deletion rejected: %v", err)
	}
}

func TestArenaRuntimeGrants(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()

	checks := []struct {
		table string
		priv  string
		want  bool
	}{
		{"app.categories", "SELECT", true},
		{"app.categories", "INSERT", false},
		{"app.categories", "UPDATE", false},
		{"app.categories", "DELETE", false},
		{"app.arenas", "SELECT", true},
		{"app.arenas", "INSERT", true},
		{"app.arenas", "UPDATE", true},
		{"app.arenas", "DELETE", true},
		{"app.arena_relations", "SELECT", true},
		{"app.arena_relations", "INSERT", true},
		{"app.arena_relations", "UPDATE", false},
		{"app.arena_relations", "DELETE", true},
	}
	for _, check := range checks {
		var allowed bool
		if err := pool.QueryRow(ctx,
			"SELECT has_table_privilege('arena_app', $1, $2)", check.table, check.priv,
		).Scan(&allowed); err != nil {
			t.Fatalf("has_table_privilege(%s, %s): %v", check.table, check.priv, err)
		}
		if allowed != check.want {
			t.Errorf("arena_app %s on %s = %v, want %v", check.priv, check.table, allowed, check.want)
		}
	}

	// The publication immutability trigger also holds for the runtime role.
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "arena-grants@arena.example.com")
	now := time.Now().UTC()
	arenaID := insertDraftArena(t, ctx, pool, creator, "Arena para probe de runtime", "society", "pt-BR")
	publishArena(t, ctx, pool, arenaID, "runtime-probe-arena", now)

	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, "UPDATE app.arenas SET statement = 'Silent rewrite' WHERE id = $1", arenaID)
		assertPgCode(t, err, "23514")
	})
}

func strPtr(value string) *string { return &value }

func textOrNil(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func timeOrNil(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}
