package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func mustModerationArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Moderation probe statement', 'technology', 'pt-BR', 'draft')
		RETURNING id`, creator).Scan(&id); err != nil {
		t.Fatalf("insert moderation arena: %v", err)
	}
	return id
}

func mustModerationArgument(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arena, author pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, 'support', 'Moderation probe argument content', 'moderation-hash-1', 10)
		RETURNING id`, arena, author).Scan(&id); err != nil {
		t.Fatalf("insert moderation argument: %v", err)
	}
	return id
}

func mustAdminRole(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account, grantor pgtype.UUID, role string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.admin_roles (account_id, role, granted_by)
		VALUES ($1, $2, $3)`, account, role, grantor); err != nil {
		t.Fatalf("insert admin role %s: %v", role, err)
	}
}

func mustModerationReport(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reporter pgtype.UUID, targetType string, arena, argument, account pgtype.UUID, reason string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.moderation_reports (reporter_id, target_type, target_arena_id, target_argument_id, target_account_id, reason)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`, reporter, targetType, nullableUUID(arena), nullableUUID(argument), nullableUUID(account), reason).Scan(&id); err != nil {
		t.Fatalf("insert moderation report: %v", err)
	}
	return id
}

func mustModerationCase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, targetType string, arena, argument, account pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.moderation_cases (target_type, target_arena_id, target_argument_id, target_account_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id`, targetType, nullableUUID(arena), nullableUUID(argument), nullableUUID(account)).Scan(&id); err != nil {
		t.Fatalf("insert moderation case: %v", err)
	}
	return id
}

func mustModerationAction(t *testing.T, ctx context.Context, pool *pgxpool.Pool, caseID, actor pgtype.UUID, actionType string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.moderation_actions (case_id, action_type, actor_id, rule_applied, justification)
		VALUES ($1, $2, $3, 'MOD-3:spam', 'Probe justification with rule and scope')
		RETURNING id`, caseID, actionType, actor).Scan(&id); err != nil {
		t.Fatalf("insert moderation action %s: %v", actionType, err)
	}
	return id
}

func mustModerationAppeal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, action, appellant pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.moderation_appeals (action_id, appellant_id, context)
		VALUES ($1, $2, 'Probe appeal context stating the contested decision')
		RETURNING id`, action, appellant).Scan(&id); err != nil {
		t.Fatalf("insert moderation appeal: %v", err)
	}
	return id
}

func nullableUUID(id pgtype.UUID) any {
	if !id.Valid {
		return nil
	}
	return id
}

func TestModerationReportTargetAndReasonConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	reporter := mustCreateAccount(t, ctx, q, "moderation-reporter@arena.example.com")
	target := mustCreateAccount(t, ctx, q, "moderation-target@arena.example.com")
	arena := mustModerationArena(t, ctx, pool, target.ID)
	argument := mustModerationArgument(t, ctx, pool, arena, target.ID)

	valid := mustModerationReport(t, ctx, pool, reporter.ID, "arena", arena, pgtype.UUID{}, pgtype.UUID{}, "spam")
	var storedType, storedReason string
	if err := pool.QueryRow(ctx, `SELECT target_type, reason FROM app.moderation_reports WHERE id = $1`, valid).Scan(&storedType, &storedReason); err != nil {
		t.Fatalf("reload report: %v", err)
	}
	if storedType != "arena" || storedReason != "spam" {
		t.Fatalf("report = %s/%s, want arena/spam", storedType, storedReason)
	}

	cases := []struct {
		name       string
		targetType string
		arena      pgtype.UUID
		argument   pgtype.UUID
		account    pgtype.UUID
		reason     string
		context    any
		wantCode   string
	}{
		{name: "unknown target type", targetType: "comment", arena: arena, reason: "spam", wantCode: "23514"},
		{name: "arena without reference", targetType: "arena", reason: "spam", wantCode: "23514"},
		{name: "arena with two references", targetType: "arena", arena: arena, argument: argument, reason: "spam", wantCode: "23514"},
		{name: "argument with account reference", targetType: "argument", argument: argument, account: target.ID, reason: "spam", wantCode: "23514"},
		{name: "profile without reference", targetType: "profile", reason: "spam", wantCode: "23514"},
		{name: "unknown reason", targetType: "arena", arena: arena, reason: "politics", wantCode: "23514"},
		{name: "blank context", targetType: "arena", arena: arena, reason: "spam", context: "   ", wantCode: "23514"},
		{name: "oversized context", targetType: "arena", arena: arena, reason: "spam", context: strings.Repeat("x", 2001), wantCode: "23514"},
		{name: "orphan reporter", targetType: "arena", arena: arena, reason: "spam", wantCode: "23503"},
		{name: "orphan arena", targetType: "arena", reason: "spam", wantCode: "23503"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reporterID := reporter.ID
			arenaID := tc.arena
			if tc.name == "orphan reporter" {
				reporterID = billingOrphanID
			}
			if tc.name == "orphan arena" {
				arenaID = billingOrphanID
			}
			_, err := pool.Exec(ctx, `
				INSERT INTO app.moderation_reports (reporter_id, target_type, target_arena_id, target_argument_id, target_account_id, reason, context)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				reporterID, tc.targetType, nullableUUID(arenaID), nullableUUID(tc.argument), nullableUUID(tc.account), tc.reason, tc.context)
			assertPgCode(t, err, tc.wantCode)
		})
	}
}

func TestModerationCaseLifecycleTransitions(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	creator := mustCreateAccount(t, ctx, q, "moderation-case@arena.example.com")
	arena := mustModerationArena(t, ctx, pool, creator.ID)

	legal := []struct{ from, to string }{
		{from: "open", to: "under_review"},
		{from: "under_review", to: "decided"},
		{from: "decided", to: "closed"},
	}
	// Reviews carry a bounded claim lease (migration 00024): driving a case
	// to under_review always sets the claim triple, and deciding clears it.
	claimCase := func(t *testing.T, caseID pgtype.UUID) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE app.moderation_cases
			SET status = 'under_review', claimed_by = $2, claimed_at = now(), lease_expires_at = now() + interval '15 minutes', updated_at = now()
			WHERE id = $1`, caseID, creator.ID); err != nil {
			t.Fatalf("claim case: %v", err)
		}
	}
	decideCase := func(t *testing.T, caseID pgtype.UUID) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE app.moderation_cases
			SET status = 'decided', claimed_by = NULL, claimed_at = NULL, lease_expires_at = NULL, updated_at = now()
			WHERE id = $1`, caseID); err != nil {
			t.Fatalf("decide case: %v", err)
		}
	}
	for i, transition := range legal {
		caseID := mustModerationCase(t, ctx, pool, "arena", arena, pgtype.UUID{}, pgtype.UUID{})
		if transition.from != "open" {
			steps := map[string][]string{"under_review": {"under_review"}, "decided": {"under_review", "decided"}}[transition.from]
			for _, step := range steps {
				if step == "under_review" {
					claimCase(t, caseID)
				} else {
					decideCase(t, caseID)
				}
			}
		}
		switch transition.to {
		case "under_review":
			claimCase(t, caseID)
		case "decided":
			decideCase(t, caseID)
		case "closed":
			if _, err := pool.Exec(ctx, `UPDATE app.moderation_cases SET status = 'closed', closed_at = $2, updated_at = now() WHERE id = $1`, caseID, time.Now().UTC()); err != nil {
				t.Fatalf("legal transition %s -> %s rejected: %v", transition.from, transition.to, err)
			}
		default:
			t.Fatalf("case %d has an unexpected target state %q", i, transition.to)
		}
	}

	illegal := []struct{ from, to string }{
		{from: "open", to: "decided"},
		{from: "open", to: "closed"},
		{from: "under_review", to: "open"},
		{from: "under_review", to: "closed"},
		{from: "decided", to: "under_review"},
		{from: "decided", to: "open"},
		{from: "closed", to: "open"},
	}
	for _, transition := range illegal {
		caseID := mustModerationCase(t, ctx, pool, "arena", arena, pgtype.UUID{}, pgtype.UUID{})
		if transition.from != "open" {
			steps := map[string][]string{"under_review": {"under_review"}, "decided": {"under_review", "decided"}, "closed": {"under_review", "decided"}}[transition.from]
			for _, step := range steps {
				if step == "under_review" {
					claimCase(t, caseID)
				} else {
					decideCase(t, caseID)
				}
			}
			if transition.from == "closed" {
				if _, err := pool.Exec(ctx, `UPDATE app.moderation_cases SET status = 'closed', closed_at = now(), updated_at = now() WHERE id = $1`, caseID); err != nil {
					t.Fatalf("close case: %v", err)
				}
			}
		}
		_, err := pool.Exec(ctx, `UPDATE app.moderation_cases SET status = $2, updated_at = now() WHERE id = $1`, caseID, transition.to)
		assertPgCode(t, err, "23514")
	}

	// Closed coherence: closed requires closed_at and vice versa.
	openCase := mustModerationCase(t, ctx, pool, "arena", arena, pgtype.UUID{}, pgtype.UUID{})
	_, err := pool.Exec(ctx, `UPDATE app.moderation_cases
		SET status = 'under_review', claimed_by = $2, claimed_at = now(), lease_expires_at = now() + interval '15 minutes', closed_at = now()
		WHERE id = $1`, openCase, creator.ID)
	assertPgCode(t, err, "23514")
}

func TestModerationActionExpiryAndImmutability(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	creator := mustCreateAccount(t, ctx, q, "moderation-action@arena.example.com")
	moderator := mustCreateAccount(t, ctx, q, "moderation-actor@arena.example.com")
	mustAdminRole(t, ctx, pool, moderator.ID, creator.ID, "moderator")
	arena := mustModerationArena(t, ctx, pool, creator.ID)
	caseID := mustModerationCase(t, ctx, pool, "arena", arena, pgtype.UUID{}, pgtype.UUID{})

	suspension := mustModerationAction(t, ctx, pool, caseID, moderator.ID, "warning")
	var actionType string
	if err := pool.QueryRow(ctx, `SELECT action_type FROM app.moderation_actions WHERE id = $1`, suspension).Scan(&actionType); err != nil {
		t.Fatalf("reload action: %v", err)
	}
	if actionType != "warning" {
		t.Fatalf("action = %s, want warning", actionType)
	}

	// Suspension without expiry is rejected; warning with expiry is rejected.
	_, err := pool.Exec(ctx, `
		INSERT INTO app.moderation_actions (case_id, action_type, actor_id, rule_applied, justification)
		VALUES ($1, 'suspension', $2, 'MOD-10:suspension', 'Probe suspension without expiry')`, caseID, moderator.ID)
	assertPgCode(t, err, "23514")

	_, err = pool.Exec(ctx, `
		INSERT INTO app.moderation_actions (case_id, action_type, actor_id, rule_applied, justification, expires_at)
		VALUES ($1, 'warning', $2, 'MOD-2:warning', 'Probe warning with expiry', now() + interval '1 day')`, caseID, moderator.ID)
	assertPgCode(t, err, "23514")

	// Past expiry is rejected.
	_, err = pool.Exec(ctx, `
		INSERT INTO app.moderation_actions (case_id, action_type, actor_id, rule_applied, justification, expires_at)
		VALUES ($1, 'suspension', $2, 'MOD-10:suspension', 'Probe suspension in the past', now() - interval '1 day')`, caseID, moderator.ID)
	assertPgCode(t, err, "23514")

	// Unknown action type is rejected.
	_, err = pool.Exec(ctx, `
		INSERT INTO app.moderation_actions (case_id, action_type, actor_id, rule_applied, justification)
		VALUES ($1, 'shadow_ban', $2, 'MOD-0:unknown', 'Probe unknown action')`, caseID, moderator.ID)
	assertPgCode(t, err, "23514")

	// Actions are facts: any UPDATE is rejected.
	_, err = pool.Exec(ctx, `UPDATE app.moderation_actions SET justification = 'rewritten' WHERE id = $1`, suspension)
	assertPgCode(t, err, "23514")
}

func TestModerationAppealSingularAndLifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	creator := mustCreateAccount(t, ctx, q, "moderation-appeal-target@arena.example.com")
	moderator := mustCreateAccount(t, ctx, q, "moderation-appeal-actor@arena.example.com")
	mustAdminRole(t, ctx, pool, moderator.ID, creator.ID, "moderator")
	arena := mustModerationArena(t, ctx, pool, creator.ID)
	caseID := mustModerationCase(t, ctx, pool, "arena", arena, pgtype.UUID{}, pgtype.UUID{})
	actionID := mustModerationAction(t, ctx, pool, caseID, moderator.ID, "warning")

	appealID := mustModerationAppeal(t, ctx, pool, actionID, creator.ID)

	// One appeal per action: the second contest of the same action fails
	// unique, not silently.
	_, err := pool.Exec(ctx, `
		INSERT INTO app.moderation_appeals (action_id, appellant_id, context)
		VALUES ($1, $2, 'Second contest of the same action')`, actionID, creator.ID)
	assertPgCode(t, err, "23505")

	// Decided without the review triple is rejected.
	_, err = pool.Exec(ctx, `UPDATE app.moderation_appeals SET status = 'reversed' WHERE id = $1`, appealID)
	assertPgCode(t, err, "23514")

	// Legal path: open -> under_review -> reversed with the triple.
	if _, err := pool.Exec(ctx, `UPDATE app.moderation_appeals SET status = 'under_review' WHERE id = $1`, appealID); err != nil {
		t.Fatalf("claim appeal: %v", err)
	}
	reviewer := mustCreateAccount(t, ctx, q, "moderation-appeal-reviewer@arena.example.com")
	if _, err := pool.Exec(ctx, `
		UPDATE app.moderation_appeals
		SET status = 'reversed', reviewer_id = $2, decision_reason = 'Probe reversal with fresh context', decided_at = now()
		WHERE id = $1`, appealID, reviewer.ID); err != nil {
		t.Fatalf("decide appeal: %v", err)
	}

	// Resolution is final: rewriting it fails.
	_, err = pool.Exec(ctx, `UPDATE app.moderation_appeals SET decision_reason = 'rewritten' WHERE id = $1`, appealID)
	assertPgCode(t, err, "23514")

	// Illegal jump open -> reversed on a fresh appeal fails.
	otherAction := mustModerationAction(t, ctx, pool, caseID, moderator.ID, "warning")
	otherAppeal := mustModerationAppeal(t, ctx, pool, otherAction, creator.ID)
	_, err = pool.Exec(ctx, `
		UPDATE app.moderation_appeals
		SET status = 'reversed', reviewer_id = $2, decision_reason = 'Probe jump', decided_at = now()
		WHERE id = $1`, otherAppeal, reviewer.ID)
	assertPgCode(t, err, "23514")
}

func TestModerationRolesAndEvidenceImmutability(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	holder := mustCreateAccount(t, ctx, q, "moderation-role-holder@arena.example.com")
	grantor := mustCreateAccount(t, ctx, q, "moderation-role-grantor@arena.example.com")
	other := mustCreateAccount(t, ctx, q, "moderation-role-other@arena.example.com")
	mustAdminRole(t, ctx, pool, holder.ID, grantor.ID, "moderator")

	// Unknown role is rejected.
	_, err := pool.Exec(ctx, `INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1, 'owner', $2)`, other.ID, grantor.ID)
	assertPgCode(t, err, "23514")

	// Duplicate assignment is rejected: one row per account.
	_, err = pool.Exec(ctx, `INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1, 'admin', $2)`, holder.ID, grantor.ID)
	assertPgCode(t, err, "23505")

	// Identity is provenance: rewriting the holder fails.
	_, err = pool.Exec(ctx, `UPDATE app.admin_roles SET account_id = $2 WHERE account_id = $1`, holder.ID, other.ID)
	assertPgCode(t, err, "23514")

	// Capability and revocation move: promotion and revocation are accepted.
	if _, err := pool.Exec(ctx, `UPDATE app.admin_roles SET role = 'admin' WHERE account_id = $1`, holder.ID); err != nil {
		t.Fatalf("promote role: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.admin_roles SET revoked_at = now() WHERE account_id = $1`, holder.ID); err != nil {
		t.Fatalf("revoke role: %v", err)
	}

	// Report evidence never moves.
	arena := mustModerationArena(t, ctx, pool, holder.ID)
	reportID := mustModerationReport(t, ctx, pool, other.ID, "arena", arena, pgtype.UUID{}, pgtype.UUID{}, "spam")
	_, err = pool.Exec(ctx, `UPDATE app.moderation_reports SET reason = 'fraud' WHERE id = $1`, reportID)
	assertPgCode(t, err, "23514")

	// Case targets never move; priority does.
	caseID := mustModerationCase(t, ctx, pool, "arena", arena, pgtype.UUID{}, pgtype.UUID{})
	otherArena := mustModerationArena(t, ctx, pool, holder.ID)
	_, err = pool.Exec(ctx, `UPDATE app.moderation_cases SET target_arena_id = $2 WHERE id = $1`, caseID, otherArena)
	assertPgCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE app.moderation_cases SET priority = 'urgent', updated_at = now() WHERE id = $1`, caseID); err != nil {
		t.Fatalf("reprioritize case: %v", err)
	}
}

func TestModerationRetentionWithoutCascades(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	creator := mustCreateAccount(t, ctx, q, "moderation-retention@arena.example.com")
	moderator := mustCreateAccount(t, ctx, q, "moderation-retention-mod@arena.example.com")
	mustAdminRole(t, ctx, pool, moderator.ID, creator.ID, "moderator")
	arena := mustModerationArena(t, ctx, pool, creator.ID)
	reportID := mustModerationReport(t, ctx, pool, creator.ID, "arena", arena, pgtype.UUID{}, pgtype.UUID{}, "spam")
	caseID := mustModerationCase(t, ctx, pool, "arena", arena, pgtype.UUID{}, pgtype.UUID{})
	actionID := mustModerationAction(t, ctx, pool, caseID, moderator.ID, "warning")
	appealID := mustModerationAppeal(t, ctx, pool, actionID, creator.ID)

	for _, table := range []string{
		"app.admin_roles", "app.moderation_reports", "app.moderation_cases",
		"app.moderation_actions", "app.moderation_appeals",
	} {
		_, err := pool.Exec(ctx, "DELETE FROM "+table)
		assertPgCode(t, err, "23514")
	}

	// Content and accounts referenced by the trail cannot be removed either:
	// RESTRICT guards the trail against cascade erasure.
	_, err := pool.Exec(ctx, `DELETE FROM app.arenas WHERE id = $1`, arena)
	if err == nil {
		t.Fatal("deleting a reported arena must fail")
	}
	_, err = pool.Exec(ctx, `DELETE FROM app.accounts WHERE id = $1`, creator.ID)
	if err == nil {
		t.Fatal("deleting an account named by the trail must fail")
	}

	var reports, cases, actions, appeals int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM app.moderation_reports WHERE id = $1),
		(SELECT count(*) FROM app.moderation_cases WHERE id = $2),
		(SELECT count(*) FROM app.moderation_actions WHERE id = $3),
		(SELECT count(*) FROM app.moderation_appeals WHERE id = $4)`,
		reportID, caseID, actionID, appealID).Scan(&reports, &cases, &actions, &appeals); err != nil {
		t.Fatalf("count trail: %v", err)
	}
	if reports != 1 || cases != 1 || actions != 1 || appeals != 1 {
		t.Fatalf("trail = %d/%d/%d/%d, want 1/1/1/1 retained", reports, cases, actions, appeals)
	}
}
