package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

const validArgumentHash = "v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestArgumentSchemaConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "argument-schema@arena.example.com")
	arenaID := insertDraftArena(t, ctx, pool, creator, "Afirmação do schema de argumentos", "technology", "pt-BR")
	publishArena(t, ctx, pool, arenaID, "argument-schema-arena", time.Now().UTC())
	otherArena := insertDraftArena(t, ctx, pool, creator, "Segunda afirmação do schema", "science", "pt-BR")
	publishArena(t, ctx, pool, otherArena, "argument-schema-arena-2", time.Now().UTC())

	// A valid top-level argument.
	var argumentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, 'support', $3, $4, $5)
		RETURNING id`,
		arenaID, creator, "A AGI existirá até 2040", validArgumentHash, 23,
	).Scan(&argumentID); err != nil {
		t.Fatalf("valid argument rejected: %v", err)
	}

	orphan := pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5a, 0x5b, 0x5c}, Valid: true}

	probes := []struct {
		name     string
		arenaID  pgtype.UUID
		authorID pgtype.UUID
		parentID pgtype.UUID
		relation string
		content  string
		hash     string
		cost     int
		want     string
	}{
		{name: "unknown relation", arenaID: arenaID, authorID: creator, relation: "maybe", content: "Conteúdo válido", hash: validArgumentHash, cost: 10, want: "23514"},
		{name: "blank content", arenaID: arenaID, authorID: creator, relation: "support", content: "   ", hash: validArgumentHash, cost: 10, want: "23514"},
		{name: "short hash", arenaID: arenaID, authorID: creator, relation: "support", content: "Conteúdo válido", hash: "abc", cost: 10, want: "23514"},
		{name: "zero cost", arenaID: arenaID, authorID: creator, relation: "support", content: "Conteúdo válido", hash: validArgumentHash, cost: 0, want: "23514"},
		{name: "cost above the limit", arenaID: arenaID, authorID: creator, relation: "support", content: "Conteúdo válido", hash: validArgumentHash, cost: 3001, want: "23514"},
		{name: "unknown arena", arenaID: orphan, authorID: creator, relation: "support", content: "Conteúdo válido", hash: validArgumentHash, cost: 10, want: "23503"},
		{name: "unknown author", arenaID: arenaID, authorID: orphan, relation: "support", content: "Conteúdo válido", hash: validArgumentHash, cost: 10, want: "23503"},
		{name: "cross arena parent", arenaID: otherArena, authorID: creator, parentID: argumentID, relation: "oppose", content: "Resposta em outra Arena", hash: validArgumentHash, cost: 10, want: "23503"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO app.arguments (arena_id, author_id, parent_id, relation, content, content_hash, grapheme_cost)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				probe.arenaID, probe.authorID, probe.parentID, probe.relation, probe.content, probe.hash, probe.cost)
			assertPgCode(t, err, probe.want)
		})
	}

	// The 3.000-cluster boundary is accepted; replies in the same Arena are
	// accepted too.
	var boundaryID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, 'context', 'Conteúdo no limite', $3, 3000)
		RETURNING id`, arenaID, creator, validArgumentHash).Scan(&boundaryID); err != nil {
		t.Fatalf("boundary cost rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, parent_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, $3, 'oppose', 'Resposta na mesma Arena', $4, 12)`,
		arenaID, creator, argumentID, validArgumentHash); err != nil {
		t.Fatalf("same-arena reply rejected: %v", err)
	}

	// An argument can never be its own parent.
	selfParent := pgtype.UUID{Bytes: [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}, Valid: true}
	_, err := pool.Exec(ctx, `
		INSERT INTO app.arguments (id, arena_id, author_id, parent_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, $3, $1, 'support', 'Autorresposta impossível', $4, 10)`,
		selfParent, arenaID, creator, validArgumentHash)
	assertPgCode(t, err, "23514")

	// The declared vocabulary is closed.
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, 'pro', 'Relação fora do vocabulário', $3, 10)`,
		arenaID, creator, validArgumentHash); err == nil {
		t.Fatal("relation outside the vocabulary was accepted")
	}
}

func TestArgumentImmutabilityAndRetention(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "argument-immutable@arena.example.com")
	arenaID := insertDraftArena(t, ctx, pool, creator, "Afirmação imutável do argumento", "philosophy", "pt-BR")
	publishArena(t, ctx, pool, arenaID, "argument-immutable-arena", time.Now().UTC())

	var argumentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, 'support', 'Conteúdo imutável do argumento', $3, 32)
		RETURNING id`, arenaID, creator, validArgumentHash).Scan(&argumentID); err != nil {
		t.Fatalf("insert argument: %v", err)
	}

	probes := []struct {
		name string
		sql  string
	}{
		{name: "content", sql: "UPDATE app.arguments SET content = 'Edição silenciosa' WHERE id = $1"},
		{name: "content hash", sql: "UPDATE app.arguments SET content_hash = 'v1:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff' WHERE id = $1"},
		{name: "grapheme cost", sql: "UPDATE app.arguments SET grapheme_cost = 1 WHERE id = $1"},
		{name: "relation", sql: "UPDATE app.arguments SET relation = 'oppose' WHERE id = $1"},
		{name: "author", sql: "UPDATE app.arguments SET author_id = gen_random_uuid() WHERE id = $1"},
		{name: "arena", sql: "UPDATE app.arguments SET arena_id = gen_random_uuid() WHERE id = $1"},
		{name: "created_at", sql: "UPDATE app.arguments SET created_at = now() WHERE id = $1"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, probe.sql, argumentID)
			assertPgCode(t, err, "23514")
		})
	}

	// Withdrawal and moderation move only the status and the updated
	// instant; the historical fact stays.
	if _, err := pool.Exec(ctx, `
		UPDATE app.arguments SET status = 'withdrawn', updated_at = now() WHERE id = $1`, argumentID); err != nil {
		t.Fatalf("status update rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE app.arguments SET status = 'removed', updated_at = now() WHERE id = $1`, argumentID); err != nil {
		t.Fatalf("moderation status update rejected: %v", err)
	}
	_, err := pool.Exec(ctx, "UPDATE app.arguments SET status = 'archived' WHERE id = $1", argumentID)
	assertPgCode(t, err, "23514")

	// Arguments are retained for every role.
	_, err = pool.Exec(ctx, "DELETE FROM app.arguments WHERE id = $1", argumentID)
	assertPgCode(t, err, "23514")
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, "DELETE FROM app.arguments WHERE id = $1", argumentID)
		assertPgCode(t, err, "42501")
	})
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, "UPDATE app.arguments SET content = 'Runtime edit' WHERE id = $1", argumentID)
		assertPgCode(t, err, "23514")
	})
}

func TestArgumentSourcesConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "argument-sources@arena.example.com")
	arenaID := insertDraftArena(t, ctx, pool, creator, "Afirmação com fontes do argumento", "science", "pt-BR")
	publishArena(t, ctx, pool, arenaID, "argument-sources-arena", time.Now().UTC())

	var argumentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, 'support', 'Conteúdo com fontes declaradas', $3, 30)
		RETURNING id`, arenaID, creator, validArgumentHash).Scan(&argumentID); err != nil {
		t.Fatalf("insert argument: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO app.argument_sources (argument_id, url, description)
		VALUES ($1, 'https://example.com/estudo', 'Estudo revisado por pares')`, argumentID); err != nil {
		t.Fatalf("valid source rejected: %v", err)
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO app.argument_sources (argument_id, url)
		VALUES ($1, 'https://example.com/estudo')`, argumentID)
	assertPgCode(t, err, "23505")

	orphan := pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c}, Valid: true}
	probes := []struct {
		name        string
		argumentID  pgtype.UUID
		url         string
		description *string
		want        string
	}{
		{name: "missing scheme", argumentID: argumentID, url: "example.com/estudo", want: "23514"},
		{name: "whitespace in url", argumentID: argumentID, url: "https://ex ample.com/x", want: "23514"},
		{name: "url without host", argumentID: argumentID, url: "http://", want: "23514"},
		{name: "oversized url", argumentID: argumentID, url: "https://example.com/" + strings.Repeat("a", 2048), want: "23514"},
		{name: "oversized description", argumentID: argumentID, url: "https://example.com/outra", description: strPtr(strings.Repeat("d", 501)), want: "23514"},
		{name: "unknown argument", argumentID: orphan, url: "https://example.com/orfa", want: "23503"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO app.argument_sources (argument_id, url, description)
				VALUES ($1, $2, $3)`, probe.argumentID, probe.url, textOrNil(probe.description))
			assertPgCode(t, err, probe.want)
		})
	}

	// Sources are inserted and read whole: the runtime never rewrites them.
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, `
			UPDATE app.argument_sources SET url = 'https://example.com/editada'
			WHERE argument_id = $1`, argumentID)
		assertPgCode(t, err, "42501")
	})
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, "DELETE FROM app.argument_sources WHERE argument_id = $1", argumentID)
		assertPgCode(t, err, "42501")
	})
}

func TestArgumentRuntimeGrants(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()

	checks := []struct {
		table string
		priv  string
		want  bool
	}{
		{"app.arguments", "SELECT", true},
		{"app.arguments", "INSERT", true},
		{"app.arguments", "UPDATE", true},
		{"app.arguments", "DELETE", false},
		{"app.argument_sources", "SELECT", true},
		{"app.argument_sources", "INSERT", true},
		{"app.argument_sources", "UPDATE", false},
		{"app.argument_sources", "DELETE", false},
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
}

// TestArgumentWithdrawnAtConstraints covers migration 00017 (P10-T06): the
// withdrawal instant only exists for withdrawn or removed arguments, the
// runtime may record it, and the content stays immutable.
func TestArgumentWithdrawnAtConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "argument-withdrawn@arena.example.com")
	arenaID := insertDraftArena(t, ctx, pool, creator, "Afirmação do instante de retirada", "culture", "pt-BR")
	publishArena(t, ctx, pool, arenaID, "argument-withdrawn-arena", time.Now().UTC())

	insertArgument := func(statement string) pgtype.UUID {
		t.Helper()
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
			VALUES ($1, $2, 'context', $3, $4, 30)
			RETURNING id`, arenaID, creator, statement, validArgumentHash).Scan(&id); err != nil {
			t.Fatalf("insert argument: %v", err)
		}
		return id
	}

	// A published argument cannot carry a withdrawal instant.
	published := insertArgument("Argumento ainda publicado")
	_, err := pool.Exec(ctx, "UPDATE app.arguments SET withdrawn_at = now() WHERE id = $1", published)
	assertPgCode(t, err, "23514")

	// The owner records the withdrawal: status and instant move together.
	if _, err := pool.Exec(ctx, `
		UPDATE app.arguments SET status = 'withdrawn', withdrawn_at = now(), updated_at = now()
		WHERE id = $1`, published); err != nil {
		t.Fatalf("withdrawal update rejected: %v", err)
	}

	// The runtime records the withdrawal too, and still cannot rewrite the
	// historical content.
	second := insertArgument("Argumento retirado pelo runtime")
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			UPDATE app.arguments SET status = 'withdrawn', withdrawn_at = now(), updated_at = now()
			WHERE id = $1`, second); err != nil {
			t.Fatalf("runtime withdrawal rejected: %v", err)
		}
		_, err := tx.Exec(ctx, "UPDATE app.arguments SET content = 'Edição silenciosa' WHERE id = $1", second)
		assertPgCode(t, err, "23514")
	})

	// The moderation path may turn a withdrawn argument into removed: the
	// withdrawal instant stays as the audit fact.
	if _, err := pool.Exec(ctx, `
		UPDATE app.arguments SET status = 'removed', updated_at = now() WHERE id = $1`, published); err != nil {
		t.Fatalf("moderation removal after withdrawal rejected: %v", err)
	}
	var withdrawnAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT withdrawn_at FROM app.arguments WHERE id = $1`, published).Scan(&withdrawnAt); err != nil {
		t.Fatalf("read withdrawn_at: %v", err)
	}
	if withdrawnAt == nil {
		t.Fatal("the withdrawal instant must survive a later moderation removal")
	}
}
