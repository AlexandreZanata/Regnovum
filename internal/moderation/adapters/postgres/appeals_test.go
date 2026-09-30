package postgres_test

import (
	"context"
	"errors"
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

// mustDecidedSuspension sanctions the owner with a suspension through the
// real review path and returns the action and case identifiers.
func mustDecidedSuspension(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repo *postgres.Repository, owner, moderator pgtype.UUID, now time.Time) (actionID, caseID string) {
	t.Helper()
	actor := domain.AccountID(uuidString(moderator))

	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.moderation_cases (target_type, target_account_id)
		VALUES ('profile', $1)
		RETURNING id`, owner).Scan(&id); err != nil {
		t.Fatalf("seed appeal case: %v", err)
	}
	caseID = uuidString(id)
	if _, err := repo.ClaimCase(ctx, application.ClaimCaseRequest{
		CaseID:    caseID,
		Actor:     actor,
		ClaimedAt: now,
		LeaseDays: domain.ClaimLease,
	}); err != nil {
		t.Fatalf("claim appeal case: %v", err)
	}
	expiry := now.Add(72 * time.Hour)
	decided, err := repo.DecideCase(ctx, application.DecideCaseRequest{
		CaseID:        caseID,
		Actor:         actor,
		Action:        domain.ActionSuspension,
		Rule:          "MOD-10:suspension",
		Justification: "Probe suspension under appeal",
		ExpiresAt:     &expiry,
		DecidedAt:     now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("decide suspension: %v", err)
	}
	return decided.ActionID, caseID
}

func TestAppealsReverseRestoresWithoutDeletingAction(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)
	now := time.Now().UTC()

	owner := mustModerationAccount(t, ctx, q, "appeal-owner@arena.example.com")
	moderator := mustModerationAccount(t, ctx, q, "appeal-actor@arena.example.com")
	reviewer := mustModerationAccount(t, ctx, q, "appeal-reviewer@arena.example.com")
	reviewerID := domain.AccountID(uuidString(reviewer.ID))

	actionID, _ := mustDecidedSuspension(t, ctx, pool, repo, owner.ID, moderator.ID, now)

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.accounts WHERE id = $1`, owner.ID).Scan(&status); err != nil {
		t.Fatalf("reload suspended account: %v", err)
	}
	if status != "suspended" {
		t.Fatalf("account status = %s, want suspended before reversal", status)
	}

	// Only the owner files.
	ownerAppeal, err := repo.InsertAppeal(ctx, application.InsertAppealRequest{
		ActionID:  actionID,
		Appellant: domain.AccountID(uuidString(owner.ID)),
		Context:   "Contesting the suspension with fresh context",
	})
	if err != nil {
		t.Fatalf("InsertAppeal: %v", err)
	}
	stranger := mustModerationAccount(t, ctx, q, "appeal-stranger@arena.example.com")
	if _, err := repo.InsertAppeal(ctx, application.InsertAppealRequest{
		ActionID:  actionID,
		Appellant: domain.AccountID(uuidString(stranger.ID)),
		Context:   "Third-party contest attempt",
	}); !errors.Is(err, application.ErrAppealDuplicate) {
		t.Fatalf("second contest error = %v, want ErrAppealDuplicate", err)
	}

	// A different reviewer claims and reverses with reason.
	if _, err := repo.ClaimAppeal(ctx, application.ClaimAppealRequest{
		AppealID: ownerAppeal.ID,
	}); err != nil {
		t.Fatalf("ClaimAppeal: %v", err)
	}
	decided, err := repo.DecideAppeal(ctx, application.DecideAppealRequest{
		AppealID:  ownerAppeal.ID,
		Reviewer:  reviewerID,
		Outcome:   domain.OutcomeReversed,
		Reason:    "Fresh evidence clears the account",
		DecidedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("DecideAppeal: %v", err)
	}
	if string(decided.Status) != string(application.AppealReversed) {
		t.Fatalf("appeal status = %q, want reversed", decided.Status)
	}

	// The sanctioned projection is restored...
	if err := pool.QueryRow(ctx, `SELECT status FROM app.accounts WHERE id = $1`, owner.ID).Scan(&status); err != nil {
		t.Fatalf("reload restored account: %v", err)
	}
	if status != "active" {
		t.Fatalf("account status = %s, want active after reversal", status)
	}
	// ...while the original action row stands untouched.
	var actions int
	var rule string
	if err := pool.QueryRow(ctx, `SELECT count(*), max(rule_applied) FROM app.moderation_actions WHERE id = $1`, mustUUID(actionID)).Scan(&actions, &rule); err != nil {
		t.Fatalf("reload original action: %v", err)
	}
	if actions != 1 || rule != "MOD-10:suspension" {
		t.Fatalf("original action = %d/%q, want 1/MOD-10:suspension retained", actions, rule)
	}
}

func TestAppealsUpholdKeepsSanction(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)
	now := time.Now().UTC()

	owner := mustModerationAccount(t, ctx, q, "uphold-owner@arena.example.com")
	moderator := mustModerationAccount(t, ctx, q, "uphold-actor@arena.example.com")
	reviewer := mustModerationAccount(t, ctx, q, "uphold-reviewer@arena.example.com")
	reviewerID := domain.AccountID(uuidString(reviewer.ID))

	actionID, _ := mustDecidedSuspension(t, ctx, pool, repo, owner.ID, moderator.ID, now)
	appeal, err := repo.InsertAppeal(ctx, application.InsertAppealRequest{
		ActionID:  actionID,
		Appellant: domain.AccountID(uuidString(owner.ID)),
		Context:   "Contesting the suspension",
	})
	if err != nil {
		t.Fatalf("InsertAppeal: %v", err)
	}
	if _, err := repo.ClaimAppeal(ctx, application.ClaimAppealRequest{
		AppealID: appeal.ID,
	}); err != nil {
		t.Fatalf("ClaimAppeal: %v", err)
	}
	if _, err := repo.DecideAppeal(ctx, application.DecideAppealRequest{
		AppealID: appeal.ID, Reviewer: reviewerID,
		Outcome: domain.OutcomeUpheld, Reason: "Sanction stands on the evidence",
		DecidedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("DecideAppeal uphold: %v", err)
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.accounts WHERE id = $1`, owner.ID).Scan(&status); err != nil {
		t.Fatalf("reload account: %v", err)
	}
	if status != "suspended" {
		t.Fatalf("account status = %s, want suspended kept on uphold", status)
	}
}
