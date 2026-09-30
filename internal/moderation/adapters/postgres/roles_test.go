package postgres_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func TestRepositoryResolvesActiveAndRevokedAssignments(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	holder := mustModerationAccount(t, ctx, q, "mod-roles-holder@arena.example.com")
	grantor := mustModerationAccount(t, ctx, q, "mod-roles-grantor@arena.example.com")
	holderID := domain.AccountID(uuidString(holder.ID))

	if _, err := pool.Exec(ctx, `INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1, 'moderator', $2)`, holder.ID, grantor.ID); err != nil {
		t.Fatalf("grant role: %v", err)
	}

	assignment, err := repo.AssignmentFor(ctx, holderID)
	if err != nil {
		t.Fatalf("AssignmentFor: %v", err)
	}
	if assignment == nil || assignment.Role != domain.RoleModerator || assignment.Revoked {
		t.Fatalf("assignment = %+v, want active moderator", assignment)
	}

	if _, err := pool.Exec(ctx, `UPDATE app.admin_roles SET revoked_at = now() WHERE account_id = $1`, holder.ID); err != nil {
		t.Fatalf("revoke role: %v", err)
	}
	revoked, err := repo.AssignmentFor(ctx, holderID)
	if err != nil {
		t.Fatalf("AssignmentFor revoked: %v", err)
	}
	if revoked == nil || !revoked.Revoked {
		t.Fatalf("revoked assignment = %+v, want Revoked true (returned, not hidden)", revoked)
	}

	stranger := domain.AccountID(uuidString(mustModerationAccount(t, ctx, q, "mod-roles-stranger@arena.example.com").ID))
	missing, err := repo.AssignmentFor(ctx, stranger)
	if err != nil || missing != nil {
		t.Fatalf("stranger = %+v, err %v; want nil, nil", missing, err)
	}
}

func mustModerationAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) platformpg.AppAccount {
	t.Helper()
	acc, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return acc
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
