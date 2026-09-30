package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	statpostgres "github.com/AlexandreZanata/Regnovum/internal/statprojections/adapters/postgres"
)

func TestRebuildArenaProjectionCanBeDeletedAndRebuilt(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{
		Email:  "stat-projection-rebuild@arena.example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := q.SetEmailVerified(ctx, account.ID); err != nil {
		t.Fatalf("verify account: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.arenas
				(creator_id, slug, statement, category, language, status, published_at)
			VALUES ($1, $2, $3, 'technology', 'pt-BR', 'published', $4)`,
			account.ID,
			"stat-projection-rebuild-"+string(rune('a'+i)),
			"Synthetic projection rebuild Arena",
			time.Now().UTC()); err != nil {
			t.Fatalf("seed Arena %d: %v", i, err)
		}
	}

	repository := statpostgres.NewRepository(pool)
	first, err := repository.RebuildArenaBatch(ctx, "", 1)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if first.Processed != 1 || first.NextCursor == "" {
		t.Fatalf("first batch = %+v, want one processed row and a cursor", first)
	}
	second, err := repository.RebuildArenaBatch(ctx, first.NextCursor, 1)
	if err != nil {
		t.Fatalf("resumed batch: %v", err)
	}
	if second.Processed != 1 || second.NextCursor == "" {
		t.Fatalf("resumed batch = %+v, want the second row", second)
	}
	terminal, err := repository.RebuildArenaBatch(ctx, second.NextCursor, 1)
	if err != nil {
		t.Fatalf("terminal batch: %v", err)
	}
	if terminal.Processed != 0 || terminal.NextCursor != "" {
		t.Fatalf("terminal batch = %+v, want no remaining rows", terminal)
	}

	var before []byte
	if err := pool.QueryRow(ctx, `
		SELECT stats FROM app.arena_public_stat_projections
		ORDER BY arena_id ASC LIMIT 1`).Scan(&before); err != nil {
		t.Fatalf("read initial projection: %v", err)
	}
	var decoded map[string]int64
	if err := json.Unmarshal(before, &decoded); err != nil {
		t.Fatalf("projection is not JSON stats: %v", err)
	}
	if decoded["eligible_participants"] != 0 || decoded["published_arguments"] != 0 {
		t.Fatalf("projection stats = %+v, want source-derived counts", decoded)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM app.arena_public_stat_projections"); err != nil {
		t.Fatalf("delete projection rows: %v", err)
	}
	if _, err := repository.RebuildArenaBatch(ctx, "", 100); err != nil {
		t.Fatalf("rebuild after deletion: %v", err)
	}
	var after []byte
	if err := pool.QueryRow(ctx, `
		SELECT stats FROM app.arena_public_stat_projections
		ORDER BY arena_id ASC LIMIT 1`).Scan(&after); err != nil {
		t.Fatalf("read rebuilt projection: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("rebuild changed source-derived values:\n before=%s\n after=%s", before, after)
	}
}
