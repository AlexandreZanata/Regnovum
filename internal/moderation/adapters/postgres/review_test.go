package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func mustReviewCase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, targetArena pgtype.UUID) string {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.moderation_cases (target_type, target_arena_id)
		VALUES ('arena', $1)
		RETURNING id`, targetArena).Scan(&id); err != nil {
		t.Fatalf("seed review case: %v", err)
	}
	return uuidString(id)
}

func TestReviewClaimConcurrentAndExpiredLease(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustModerationAccount(t, ctx, q, "mod-review-owner@arena.example.com")
	modA := mustModerationAccount(t, ctx, q, "mod-review-a@arena.example.com")
	modB := mustModerationAccount(t, ctx, q, "mod-review-b@arena.example.com")
	actorA := domain.AccountID(uuidString(modA.ID))
	actorB := domain.AccountID(uuidString(modB.ID))

	var arenaID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Review probe statement', 'technology', 'pt-BR', 'draft')
		RETURNING id`, owner.ID).Scan(&arenaID); err != nil {
		t.Fatalf("seed arena: %v", err)
	}
	caseID := mustReviewCase(t, ctx, pool, arenaID)
	now := time.Now().UTC()

	const racers = 20
	var winsA, winsB, denied int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			actor := actorA
			if index%2 == 1 {
				actor = actorB
			}
			_, err := repo.ClaimCase(ctx, application.ClaimCaseRequest{
				CaseID:    caseID,
				Actor:     actor,
				ClaimedAt: now,
				LeaseDays: domain.ClaimLease,
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				if actor == actorA {
					winsA++
				} else {
					winsB++
				}
			} else if errors.Is(err, application.ErrCaseAlreadyClaimed) {
				denied++
			} else {
				t.Errorf("racer %d error = %v", index, err)
			}
		}(i)
	}
	wg.Wait()

	// Exactly one moderator owns the live lease: its racers succeed (one
	// fresh claim plus idempotent replays), the other's deny.
	if (winsA == 0) == (winsB == 0) {
		t.Fatalf("winsA = %d winsB = %d, want exactly one winner", winsA, winsB)
	}
	if winsA+winsB+denied != racers {
		t.Fatalf("winsA = %d winsB = %d denied = %d, want %d total", winsA, winsB, denied, racers)
	}

	stored, err := repo.GetCase(ctx, caseID)
	if err != nil || stored == nil || stored.Status != application.CaseUnderReview {
		t.Fatalf("stored case = %+v, err %v; want claimed review", stored, err)
	}

	// An expired lease may be reclaimed by the other moderator. Age the
	// whole claim triple into the past so the coherence CHECK still holds.
	if _, err := pool.Exec(ctx, `UPDATE app.moderation_cases SET claimed_at = $2, lease_expires_at = $3 WHERE id = $1`,
		mustUUID(caseID), now.Add(-30*time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	loser := actorB
	if winsB > 0 {
		loser = actorA
	}
	reclaimed, err := repo.ClaimCase(ctx, application.ClaimCaseRequest{
		CaseID:    caseID,
		Actor:     loser,
		ClaimedAt: now,
		LeaseDays: domain.ClaimLease,
	})
	if err != nil {
		t.Fatalf("reclaim expired lease: %v", err)
	}
	if reclaimed.ClaimedBy != loser {
		t.Fatalf("reclaimed by = %q, want %q", reclaimed.ClaimedBy, loser)
	}
}

func TestReviewDecideAtomicAndInvalid(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustModerationAccount(t, ctx, q, "mod-decide-owner@arena.example.com")
	moderator := mustModerationAccount(t, ctx, q, "mod-decide-actor@arena.example.com")
	actor := domain.AccountID(uuidString(moderator.ID))

	var arenaID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Decide probe statement', 'technology', 'pt-BR', 'draft')
		RETURNING id`, owner.ID).Scan(&arenaID); err != nil {
		t.Fatalf("seed arena: %v", err)
	}
	caseID := mustReviewCase(t, ctx, pool, arenaID)
	now := time.Now().UTC()

	// Deciding an open case refuses without writing an action.
	_, err := repo.DecideCase(ctx, application.DecideCaseRequest{
		CaseID:        caseID,
		Actor:         actor,
		Action:        domain.ActionWarning,
		Rule:          "MOD-2:warning",
		Justification: "Probe decision on open case",
		DecidedAt:     now,
	})
	if !errors.Is(err, application.ErrInvalidCaseTransition) {
		t.Fatalf("open decide error = %v, want ErrInvalidCaseTransition", err)
	}

	if _, err := repo.ClaimCase(ctx, application.ClaimCaseRequest{
		CaseID:    caseID,
		Actor:     actor,
		ClaimedAt: now,
		LeaseDays: domain.ClaimLease,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	record, err := repo.DecideCase(ctx, application.DecideCaseRequest{
		CaseID:        caseID,
		Actor:         actor,
		Action:        domain.ActionWarning,
		Rule:          "MOD-2:warning",
		Justification: "Least restrictive measure for the risk",
		DecidedAt:     now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if record.Action != domain.ActionWarning || record.CaseID != caseID {
		t.Fatalf("decision = %+v", record)
	}

	// Case and action commit together: the case is decided with a cleared
	// claim and exactly one action exists.
	stored, err := repo.GetCase(ctx, caseID)
	if err != nil || stored.Status != application.CaseDecided || !stored.ClaimedBy.IsZero() {
		t.Fatalf("stored case = %+v, err %v; want decided with cleared claim", stored, err)
	}
	var actions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.moderation_actions WHERE case_id = $1`, mustUUID(caseID)).Scan(&actions); err != nil {
		t.Fatalf("count actions: %v", err)
	}
	if actions != 1 {
		t.Fatalf("actions = %d, want exactly 1", actions)
	}

	// Deciding twice refuses: the second decision writes nothing.
	_, err = repo.DecideCase(ctx, application.DecideCaseRequest{
		CaseID:        caseID,
		Actor:         actor,
		Action:        domain.ActionWarning,
		Rule:          "MOD-2:warning",
		Justification: "Second decision attempt",
		DecidedAt:     now.Add(2 * time.Minute),
	})
	if !errors.Is(err, application.ErrInvalidCaseTransition) {
		t.Fatalf("second decide error = %v, want ErrInvalidCaseTransition", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.moderation_actions WHERE case_id = $1`, mustUUID(caseID)).Scan(&actions); err != nil {
		t.Fatalf("recount actions: %v", err)
	}
	if actions != 1 {
		t.Fatalf("actions after retry = %d, want still 1", actions)
	}
}

func mustUUID(raw string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		panic(err)
	}
	return id
}
