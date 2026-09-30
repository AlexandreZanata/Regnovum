package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func TestAuditEventsRejectUpdatesAndDeletes(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	actor := mustCreateAccount(t, ctx, q, "audit-freeze@arena.example.com")

	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.audit_events (actor_id, action, target_type, target_id, reason_code)
		VALUES ($1, 'moderation.decide', 'case', 'case-probe-1', 'MOD-3:spam')
		RETURNING id`, actor.ID).Scan(&id); err != nil {
		t.Fatalf("seed audit event: %v", err)
	}

	_, err := pool.Exec(ctx, `UPDATE app.audit_events SET reason_code = 'rewritten' WHERE id = $1`, id)
	assertPgCode(t, err, "23514")

	_, err = pool.Exec(ctx, `DELETE FROM app.audit_events WHERE id = $1`, id)
	assertPgCode(t, err, "23514")

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.audit_events WHERE id = $1`, id).Scan(&count); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if count != 1 {
		t.Fatalf("audit events = %d, want the single retained row", count)
	}
}

func TestAuditEventsConstrainMetadataAndReferences(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)

	actor := mustCreateAccount(t, ctx, q, "audit-constraints@arena.example.com")

	// Forbidden metadata keys never reach storage.
	_, err := pool.Exec(ctx, `
		INSERT INTO app.audit_events (actor_id, action, target_type, target_id, reason_code, metadata)
		VALUES ($1, 'wallet.adjust', 'operation', 'op-1', 'admin_adjustment', '{"email": "someone@example.com"}')`, actor.ID)
	assertPgCode(t, err, "23514")

	// Non-object metadata never reaches storage.
	_, err = pool.Exec(ctx, `
		INSERT INTO app.audit_events (actor_id, action, target_type, target_id, reason_code, metadata)
		VALUES ($1, 'wallet.adjust', 'operation', 'op-1', 'admin_adjustment', '["operation_id"]')`, actor.ID)
	assertPgCode(t, err, "23514")

	// Unknown actions, targets and orphan actors never reach storage.
	_, err = pool.Exec(ctx, `
		INSERT INTO app.audit_events (actor_id, action, target_type, target_id, reason_code)
		VALUES ($1, 'decide', 'case', 'case-1', 'MOD-3:spam')`, actor.ID)
	assertPgCode(t, err, "23514")

	_, err = pool.Exec(ctx, `
		INSERT INTO app.audit_events (actor_id, action, target_type, target_id, reason_code)
		VALUES ($1, 'moderation.decide', 'comment', 'comment-1', 'MOD-3:spam')`, actor.ID)
	assertPgCode(t, err, "23514")

	_, err = pool.Exec(ctx, `
		INSERT INTO app.audit_events (actor_id, action, target_type, target_id, reason_code)
		VALUES ($1, 'moderation.decide', 'case', 'case-1', 'MOD-3:spam')`, billingOrphanID)
	assertPgCode(t, err, "23503")

	// A duplicate idempotency key collides instead of duplicating.
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.audit_events (actor_id, action, target_type, target_id, reason_code, idempotency_key)
		VALUES ($1, 'wallet.adjust', 'operation', 'op-1', 'admin_adjustment', 'audit-probe-key-1')`, actor.ID); err != nil {
		t.Fatalf("first keyed insert: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO app.audit_events (actor_id, action, target_type, target_id, reason_code, idempotency_key)
		VALUES ($1, 'wallet.adjust', 'operation', 'op-1', 'admin_adjustment', 'audit-probe-key-1')`, actor.ID)
	assertPgCode(t, err, "23505")
}
