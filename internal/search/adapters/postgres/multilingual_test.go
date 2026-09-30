package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/search/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/search/application"
)

// TestMultilingualSearchGoldenCorpus is the P29-T04 proof over real
// PostgreSQL: the golden corpus pins per-language matching (accents,
// stemming), deterministic ranking, stable keyset pagination, the
// explicit unknown-language fallback and the exclusion of removed and
// withdrawn content. Order and membership are pinned, never float
// scores: ranking values are engine internals, result order is the
// product promise.
func TestMultilingualSearchGoldenCorpus(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	pool := db.Pool.Pool()
	repo := postgres.NewRepository(pool)

	var account pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO app.accounts (email, status) VALUES ($1, 'active') RETURNING id`, "search-i18n@arena.example.com").Scan(&account); err != nil {
		t.Fatal(err)
	}
	insertArena := func(slug, statement, language, status string, published time.Time) pgtype.UUID {
		t.Helper()
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO app.arenas (creator_id, slug, statement, category, language, status, published_at) VALUES ($1, $2, $3, 'technology', $4, $5, $6) RETURNING id`, account, slug, statement, language, status, published).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	day := func(n int) time.Time { return time.Date(2026, time.January, n, 12, 0, 0, 0, time.UTC) }

	arenaFusao := insertArena("i18n-pt-fusao", "A fusão nuclear será energia limpa", "pt-BR", "published", day(3))
	arenaFusoes := insertArena("i18n-pt-fusoes", "Fusões nucleares e o futuro da energia", "pt-BR", "published", day(2))
	arenaSolar := insertArena("i18n-pt-solar", "Energia solar para todos", "pt-BR", "published", day(1))
	arenaFusion := insertArena("i18n-en-fusion", "Fusion energy will become affordable", "en-US", "published", day(3))
	arenaNuclear := insertArena("i18n-en-nuclear", "Nuclear fusion powers the grid", "en-US", "published", day(2))
	arenaPanels := insertArena("i18n-en-panels", "Solar panels for every home", "en-US", "published", day(1))
	insertArena("i18n-pt-removed", "Fusão secreta que ninguém deve achar", "pt-BR", "removed", day(4))

	insertArgument := func(arena pgtype.UUID, content, status, key string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost, status, idempotency_key) VALUES ($1, $2, 'support', $3, $4, 10, $5, $6)`, arena, account, content, "i18n-hash-"+key, status, key); err != nil {
			t.Fatal(err)
		}
	}
	insertArgument(arenaFusao, "A fusão é uma fonte de energia limpa", "published", "i18n-published")
	insertArgument(arenaFusao, "A fusão retirada não deve aparecer", "withdrawn", "i18n-withdrawn")

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

	ids := func(page *application.ArenaPage) []string {
		got := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			got = append(got, item.ID)
		}
		return got
	}
	wantIDs := func(t *testing.T, query, language string, want ...pgtype.UUID) {
		t.Helper()
		page, err := arenaUseCase.Execute(ctx, query, language, "", 10)
		if err != nil {
			t.Fatalf("query %q/%s error = %v", query, language, err)
		}
		got := ids(page)
		if len(got) != len(want) {
			t.Fatalf("query %q/%s = %v, want %d items", query, language, got, len(want))
		}
		for index, id := range want {
			if got[index] != id.String() {
				t.Fatalf("query %q/%s item %d = %s, want %s (full order %v)", query, language, index, got[index], id.String(), got)
			}
		}
	}

	// Accents: the accented query matches accented content. Measured on
	// PostgreSQL 18: singular and plural -ão nouns are distinct lexemes
	// ('fusã' vs 'fusõ'), so each form matches exactly its own document
	// instead of conflating approximately.
	wantIDs(t, "fusão", "pt-BR", arenaFusao)
	wantIDs(t, "fusões", "pt-BR", arenaFusoes)
	// English stemming: "energies" reaches "energy" ('energi' both ways).
	wantIDs(t, "energies", "en-US", arenaFusion)
	wantIDs(t, "fusion", "en-US", arenaFusion, arenaNuclear)
	wantIDs(t, "panels", "en-US", arenaPanels)
	// Unaccented queries do not fold accents: the engine has no unaccent
	// step, so "fusao" matches nothing instead of matching approximately.
	empty, err := arenaUseCase.Execute(ctx, "fusao", "pt-BR", "", 10)
	if err != nil {
		t.Fatalf("unaccented query error = %v", err)
	}
	if len(empty.Items) != 0 {
		t.Fatalf("unaccented query matched %+v, want no approximate matches", empty.Items)
	}

	// Explicit fallback: an empty language searches every language without
	// error; an unsupported language is refused at the use case, never
	// silently reinterpreted.
	unfiltered, err := arenaUseCase.Execute(ctx, "solar", "", "", 10)
	if err != nil {
		t.Fatalf("unfiltered query error = %v", err)
	}
	if len(unfiltered.Items) != 2 {
		t.Fatalf("unfiltered solar = %d items, want the pt and en documents", len(unfiltered.Items))
	}
	if _, err := arenaUseCase.Execute(ctx, "solar", "de-DE", "", 10); err == nil {
		t.Fatal("unsupported language must fail instead of falling back silently")
	}

	// Stable pagination: single-item pages over "energia" converge to the
	// full ordered set with no duplicates and no loss.
	// Shared stems rank by score, then recency: the golden order is part
	// of the contract.
	wantIDs(t, "energia", "pt-BR", arenaFusao, arenaFusoes, arenaSolar)
	full, err := arenaUseCase.Execute(ctx, "energia", "pt-BR", "", 10)
	if err != nil {
		t.Fatalf("full query error = %v", err)
	}
	var converged []string
	cursor := ""
	for page := 0; page < len(full.Items); page++ {
		result, err := arenaUseCase.Execute(ctx, "energia", "pt-BR", cursor, 1)
		if err != nil {
			t.Fatalf("page %d error = %v", page, err)
		}
		if len(result.Items) != 1 {
			t.Fatalf("page %d has %d items, want exactly 1", page, len(result.Items))
		}
		converged = append(converged, result.Items[0].ID)
		cursor = result.NextCursor
		if page < len(full.Items)-1 && cursor == "" {
			t.Fatalf("page %d produced no cursor", page)
		}
	}
	if len(converged) != len(full.Items) {
		t.Fatalf("paginated %d items but full query has %d", len(converged), len(full.Items))
	}
	seen := map[string]bool{}
	for index, id := range converged {
		if seen[id] {
			t.Fatalf("paginated item %d duplicates %s", index, id)
		}
		seen[id] = true
		if id != full.Items[index].ID {
			t.Fatalf("paginated order %v diverges from full order", converged)
		}
	}

	// Exclusion: removed arenas and withdrawn arguments never surface,
	// even when the query matches their terms exactly.
	removed, err := arenaUseCase.Execute(ctx, "secreta", "pt-BR", "", 10)
	if err != nil {
		t.Fatalf("removed query error = %v", err)
	}
	if len(removed.Items) != 0 {
		t.Fatalf("removed arena surfaced: %+v", removed.Items)
	}
	args, err := argumentUseCase.Execute(ctx, "fusão", "pt-BR", "", 10)
	if err != nil {
		t.Fatalf("argument query error = %v", err)
	}
	if len(args.Items) != 1 || args.Items[0].Content != "A fusão é uma fonte de energia limpa" {
		t.Fatalf("arguments = %+v, want only the published one", args.Items)
	}
}
