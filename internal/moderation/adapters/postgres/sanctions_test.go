package postgres_test

import (
	"context"
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

func mustPublishedArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator pgtype.UUID, slug string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	now := time.Now().UTC()
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		VALUES ($1, 'Sanction probe statement', 'technology', 'pt-BR', 'published', $2, $3)
		RETURNING id`, creator, slug, now).Scan(&id); err != nil {
		t.Fatalf("seed published arena: %v", err)
	}
	return id
}

func mustPublishedArgument(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arena, author pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, 'support', 'Sanction probe argument content', 'sanction-hash-1', 10)
		RETURNING id`, arena, author).Scan(&id); err != nil {
		t.Fatalf("seed published argument: %v", err)
	}
	return id
}

func mustSanctionCase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repo *postgres.Repository, targetType, targetID string, actor domain.AccountID, now time.Time) string {
	t.Helper()
	var id pgtype.UUID
	var arena, argument, account any
	switch targetType {
	case "arena":
		arena = mustUUID(targetID)
	case "argument":
		argument = mustUUID(targetID)
	case "profile":
		account = mustUUID(targetID)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.moderation_cases (target_type, target_arena_id, target_argument_id, target_account_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id`, targetType, arena, argument, account).Scan(&id); err != nil {
		t.Fatalf("seed sanction case: %v", err)
	}
	caseID := uuidString(id)
	if _, err := repo.ClaimCase(ctx, application.ClaimCaseRequest{
		CaseID:    caseID,
		Actor:     actor,
		ClaimedAt: now,
		LeaseDays: domain.ClaimLease,
	}); err != nil {
		t.Fatalf("claim sanction case: %v", err)
	}
	return caseID
}

func TestSanctionsMatrixPerTarget(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustModerationAccount(t, ctx, q, "sanction-owner@arena.example.com")
	moderator := mustModerationAccount(t, ctx, q, "sanction-actor@arena.example.com")
	actor := domain.AccountID(uuidString(moderator.ID))
	now := time.Now().UTC()

	arenaID := mustPublishedArena(t, ctx, pool, owner.ID, "sanction-probe-arena")
	argumentID := mustPublishedArgument(t, ctx, pool, arenaID, owner.ID)
	ownerID := uuidString(owner.ID)

	decide := func(caseID string, action domain.Action, expiresAt *time.Time) (*application.DecisionRecord, error) {
		return repo.DecideCase(ctx, application.DecideCaseRequest{
			CaseID:        caseID,
			Actor:         actor,
			Action:        action,
			Rule:          "MOD-9:probe",
			Justification: "Probe sanction with measured scope",
			ExpiresAt:     expiresAt,
			DecidedAt:     now.Add(time.Minute),
		})
	}

	// Arena close moves the public projection to closed in the same
	// transaction as the audit event.
	closeCase := mustSanctionCase(t, ctx, pool, repo, "arena", uuidString(arenaID), actor, now)
	if _, err := decide(closeCase, domain.ActionArenaClose, nil); err != nil {
		t.Fatalf("arena close: %v", err)
	}
	var arenaStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.arenas WHERE id = $1`, arenaID).Scan(&arenaStatus); err != nil {
		t.Fatalf("reload arena: %v", err)
	}
	if arenaStatus != "closed" {
		t.Fatalf("arena status = %s, want closed", arenaStatus)
	}

	// Argument removal hides the content from the published-only public
	// list in the same transaction as the audit event.
	removeCase := mustSanctionCase(t, ctx, pool, repo, "argument", uuidString(argumentID), actor, now)
	if _, err := decide(removeCase, domain.ActionArgumentRemove, nil); err != nil {
		t.Fatalf("argument remove: %v", err)
	}
	var publishedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.arguments WHERE arena_id = $1 AND status = 'published'`, arenaID).Scan(&publishedCount); err != nil {
		t.Fatalf("count published arguments: %v", err)
	}
	if publishedCount != 0 {
		t.Fatalf("published arguments = %d, want 0 (removed leaves the public list)", publishedCount)
	}

	// Suspension carries the future expiry onto the audit event and moves
	// the account out of active in the same transaction.
	suspendCase := mustSanctionCase(t, ctx, pool, repo, "profile", ownerID, actor, now)
	suspensionExpiry := now.Add(72 * time.Hour)
	if _, err := decide(suspendCase, domain.ActionSuspension, &suspensionExpiry); err != nil {
		t.Fatalf("suspension: %v", err)
	}
	var accountStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.accounts WHERE id = $1`, owner.ID).Scan(&accountStatus); err != nil {
		t.Fatalf("reload account: %v", err)
	}
	if accountStatus != "suspended" {
		t.Fatalf("account status = %s, want suspended", accountStatus)
	}
	var recordedExpiry time.Time
	if err := pool.QueryRow(ctx, `SELECT expires_at FROM app.moderation_actions WHERE case_id = $1`, mustUUID(suspendCase)).Scan(&recordedExpiry); err != nil {
		t.Fatalf("reload suspension expiry: %v", err)
	}
	// timestamptz stores microsecond precision: truncate before comparing.
	if !recordedExpiry.Truncate(time.Millisecond).Equal(suspensionExpiry.Truncate(time.Millisecond)) {
		t.Fatalf("suspension expiry = %v, want %v", recordedExpiry, suspensionExpiry)
	}

	// A ban is the same projection without an expiry: indefinite until a
	// reversal restores it (P13-T06).
	other := mustModerationAccount(t, ctx, q, "sanction-ban@arena.example.com")
	banCase := mustSanctionCase(t, ctx, pool, repo, "profile", uuidString(other.ID), actor, now)
	if _, err := decide(banCase, domain.ActionBan, nil); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM app.accounts WHERE id = $1`, other.ID).Scan(&accountStatus); err != nil {
		t.Fatalf("reload banned account: %v", err)
	}
	if accountStatus != "suspended" {
		t.Fatalf("banned account status = %s, want suspended", accountStatus)
	}
}

func TestSanctionsRollbackOnStaleTarget(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustModerationAccount(t, ctx, q, "rollback-owner@arena.example.com")
	moderator := mustModerationAccount(t, ctx, q, "rollback-actor@arena.example.com")
	actor := domain.AccountID(uuidString(moderator.ID))
	now := time.Now().UTC()

	// A draft arena left the sanctionable state for closing (only
	// published arenas close): the decision fails and nothing persists.
	var draftID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Rollback probe statement', 'technology', 'pt-BR', 'draft')
		RETURNING id`, owner.ID).Scan(&draftID); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	caseID := mustSanctionCase(t, ctx, pool, repo, "arena", uuidString(draftID), actor, now)
	_, err := repo.DecideCase(ctx, application.DecideCaseRequest{
		CaseID:        caseID,
		Actor:         actor,
		Action:        domain.ActionArenaClose,
		Rule:          "MOD-9:probe",
		Justification: "Close attempt on a draft",
		DecidedAt:     now.Add(time.Minute),
	})
	if err == nil {
		t.Fatal("closing a draft must fail")
	}

	var actions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.moderation_actions WHERE case_id = $1`, mustUUID(caseID)).Scan(&actions); err != nil {
		t.Fatalf("count actions: %v", err)
	}
	if actions != 0 {
		t.Fatalf("actions = %d, want 0 (rolled back with the effect)", actions)
	}
	stored, err := repo.GetCase(ctx, caseID)
	if err != nil || stored.Status != application.CaseUnderReview {
		t.Fatalf("case = %+v, err %v; want still under_review", stored, err)
	}
}

func TestSanctionsInvalidateAttributions(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustModerationAccount(t, ctx, q, "invalidate-owner@arena.example.com")
	attributor := mustModerationAccount(t, ctx, q, "invalidate-attributor@arena.example.com")
	moderator := mustModerationAccount(t, ctx, q, "invalidate-actor@arena.example.com")
	actor := domain.AccountID(uuidString(moderator.ID))
	now := time.Now().UTC()

	arenaID := mustPublishedArena(t, ctx, pool, owner.ID, "sanction-invalidate-arena")
	argumentID := mustPublishedArgument(t, ctx, pool, arenaID, owner.ID)

	if _, err := pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position, version)
		VALUES ($1, $2, 'agree', 'disagree', 2)`, arenaID, attributor.ID); err != nil {
		t.Fatalf("seed debate position: %v", err)
	}
	var changeID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.position_changes (arena_id, account_id, from_position, to_position, version)
		VALUES ($1, $2, 'agree', 'disagree', 2)
		RETURNING id`, arenaID, attributor.ID).Scan(&changeID); err != nil {
		t.Fatalf("seed position change: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id)
		VALUES ($1, $2, $3)`, changeID, attributor.ID, argumentID); err != nil {
		t.Fatalf("seed attribution: %v", err)
	}

	caseID := mustSanctionCase(t, ctx, pool, repo, "argument", uuidString(argumentID), actor, now)
	if _, err := repo.DecideCase(ctx, application.DecideCaseRequest{
		CaseID:        caseID,
		Actor:         actor,
		Action:        domain.ActionAttributionInvalidate,
		Rule:          "MOD-5:fraud",
		Justification: "Coordinated attribution farm crediting this argument",
		DecidedAt:     now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("decide attribution invalidate: %v", err)
	}

	var status, reason string
	var moderatedBy pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT status, moderation_reason, moderated_by FROM app.persuasion_attributions WHERE argument_id = $1`, argumentID).Scan(&status, &reason, &moderatedBy); err != nil {
		t.Fatalf("reload attribution: %v", err)
	}
	if status != "invalid" || reason != "MOD-5:fraud" || uuidString(moderatedBy) != actor.String() {
		t.Fatalf("attribution = %s/%s/%s, want invalid/MOD-5:fraud/%s", status, reason, uuidString(moderatedBy), actor)
	}
	var argumentStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.arguments WHERE id = $1`, argumentID).Scan(&argumentStatus); err != nil {
		t.Fatalf("reload argument: %v", err)
	}
	if argumentStatus != "published" {
		t.Fatalf("argument status = %s, want published untouched (invalidation targets attributions, not content)", argumentStatus)
	}
}
