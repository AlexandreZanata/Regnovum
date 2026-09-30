package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func TestQueuePageProjectsRoutingOnly(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustModerationAccount(t, ctx, q, "queue-owner@arena.example.com")
	var arenaID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Queue probe statement', 'technology', 'pt-BR', 'draft')
		RETURNING id`, owner.ID).Scan(&arenaID); err != nil {
		t.Fatalf("seed arena: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.moderation_cases (target_type, target_arena_id, priority)
			VALUES ('arena', $1, 'normal')`, mustUUID(arenaID)); err != nil {
			t.Fatalf("seed case %d: %v", i, err)
		}
	}

	first, err := repo.ListQueuePage(ctx, "", nil, 2)
	if err != nil {
		t.Fatalf("ListQueuePage: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first page items = %d, want 2", len(first))
	}
	for _, item := range first {
		if item.CaseID == "" || item.TargetID == "" || item.CreatedAt.IsZero() {
			t.Fatalf("queue item = %+v, want routing fields populated", item)
		}
	}

	after := &application.QueuePosition{CreatedAt: first[1].CreatedAt, CaseID: first[1].CaseID}
	second, err := repo.ListQueuePage(ctx, "", after, 2)
	if err != nil {
		t.Fatalf("ListQueuePage second: %v", err)
	}
	if len(second) != 1 || second[0].CaseID == first[0].CaseID || second[0].CaseID == first[1].CaseID {
		t.Fatalf("second page = %+v, want the remaining case without duplicates", second)
	}

	filtered, err := repo.ListQueuePage(ctx, "decided", nil, 10)
	if err != nil {
		t.Fatalf("ListQueuePage filtered: %v", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("decided filter items = %d, want 0", len(filtered))
	}
}

func TestSessionAgeResolvesAndDeniesUnknown(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustModerationAccount(t, ctx, q, "session-age@arena.example.com")
	now := time.Now().UTC()

	var sessionID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.sessions (account_id, token_hash, expires_at)
		VALUES ($1, '\x01', $2)
		RETURNING id`, owner.ID, now.Add(time.Hour)).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	age, err := repo.SessionAgeAt(ctx, sessionID, now)
	if err != nil {
		t.Fatalf("SessionAgeAt: %v", err)
	}
	if age < 0 || age > time.Minute {
		t.Fatalf("session age = %v, want a fresh non-negative age", age)
	}

	if _, err := repo.SessionAgeAt(ctx, "018f6b2a-0000-7000-8000-000000000099", now); err == nil {
		t.Fatal("unknown session must deny")
	}
	if _, err := repo.SessionAgeAt(ctx, "not-a-uuid", now); err == nil {
		t.Fatal("malformed session must deny without leaking")
	}
}
