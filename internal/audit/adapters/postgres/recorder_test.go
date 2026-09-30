package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	auditpostgres "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	auditdomain "github.com/AlexandreZanata/Regnovum/internal/audit/domain"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	persuasiondomain "github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

func mustAuditAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) platformpg.AppAccount {
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

func mustUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		t.Fatalf("scan uuid %q: %v", raw, err)
	}
	return id
}

func TestRecorderRoundTripAndReplay(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := auditpostgres.NewRepository(pool)
	q := platformpg.New(pool)

	actor := mustAuditAccount(t, ctx, q, "audit-recorder@arena.example.com")

	event := auditdomain.AuditEvent{
		Actor:      uuidString(actor.ID),
		Action:     "moderation.decide",
		TargetType: "case",
		TargetID:   "case-probe-1",
		ReasonCode: "MOD-3:spam",
		Metadata: map[string]string{
			"case_id": "case-probe-1",
			"rule":    "MOD-3:spam",
		},
		IdempotencyKey: "audit-probe-key-1",
		OccurredAt:     time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	}

	first, err := repo.Record(ctx, event)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if first.ID == "" || first.Replayed {
		t.Fatalf("first record = %+v, want fresh id", first)
	}

	second, err := repo.Record(ctx, event)
	if err != nil {
		t.Fatalf("Record replay: %v", err)
	}
	if !second.Replayed || second.ID != first.ID {
		t.Fatalf("replay = %+v, want same id with replayed", second)
	}

	var metadataJSON []byte
	var action, reason string
	if err := pool.QueryRow(ctx, `SELECT action, reason_code, metadata FROM app.audit_events WHERE id = $1`, mustUUID(t, first.ID)).Scan(&action, &reason, &metadataJSON); err != nil {
		t.Fatalf("reload audit event: %v", err)
	}
	if action != "moderation.decide" || reason != "MOD-3:spam" {
		t.Fatalf("stored = %s/%s, want moderation.decide/MOD-3:spam", action, reason)
	}
	var metadata map[string]string
	if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata["rule"] != "MOD-3:spam" {
		t.Fatalf("metadata = %v, want rule preserved", metadata)
	}
}

func TestRecorderRejectsSensitiveMetadata(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := auditpostgres.NewRepository(pool)
	q := platformpg.New(pool)

	actor := mustAuditAccount(t, ctx, q, "audit-sensitive@arena.example.com")

	for _, key := range []string{"email", "token", "session", "password", "payload", "ip"} {
		event := auditdomain.AuditEvent{
			Actor:      uuidString(actor.ID),
			Action:     "wallet.adjust",
			TargetType: "operation",
			TargetID:   "op-1",
			ReasonCode: "admin_adjustment",
			Metadata:   map[string]string{key: "secret-value"},
			OccurredAt: time.Now().UTC(),
		}
		if _, err := repo.Record(ctx, event); err == nil {
			t.Fatalf("metadata key %q must be rejected before any write", key)
		}
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.audit_events WHERE actor_id = $1`, actor.ID).Scan(&count); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if count != 0 {
		t.Fatalf("audit events = %d, want 0 (rejected before write)", count)
	}
}

func TestShimsIntegrateExistingRecorders(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := auditpostgres.NewRepository(pool)
	q := platformpg.New(pool)
	now := time.Now().UTC()

	actor := mustAuditAccount(t, ctx, q, "audit-shim-actor@arena.example.com")
	target := mustAuditAccount(t, ctx, q, "audit-shim-target@arena.example.com")
	actorID := uuidString(actor.ID)

	// Wallet adjustment shim.
	reason, err := walletdomain.ParseReason("Quarterly franchise correction approved")
	if err != nil {
		t.Fatalf("ParseReason: %v", err)
	}
	reference, err := walletdomain.ParseReference("audit:probe-1")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	key, err := walletdomain.ParseIdempotencyKey("audit-wallet-key-1")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey: %v", err)
	}
	amount, err := walletdomain.NewInk(100)
	if err != nil {
		t.Fatalf("NewInk: %v", err)
	}
	if err := repo.RecordAdminAdjustment(ctx, walletapp.AdminAdjustmentEvent{
		ActorAccountID: walletdomain.AccountID(actorID),
		AccountID:      walletdomain.AccountID(uuidString(target.ID)),
		OperationID:    "operation-probe-1",
		OperationType:  walletdomain.OperationCreditAdmin,
		Allocation:     walletdomain.NewAllocation(amount, walletdomain.Ink{}),
		Reason:         reason,
		Reference:      reference,
		IdempotencyKey: key,
		OccurredAt:     now,
	}); err != nil {
		t.Fatalf("RecordAdminAdjustment: %v", err)
	}

	// Arenas moderation shim.
	arenaReason, err := arenasdomain.ParseReason("Coordinated spam flood with links")
	if err != nil {
		t.Fatalf("ParseReason: %v", err)
	}
	if err := repo.RecordArenaModeration(ctx, arenasapp.ModerationEvent{
		ActorAccountID: arenasdomain.ModeratorID(actorID),
		ArenaID:        arenasdomain.ArenaID("018f6b2a-0000-7000-8000-0000000000a1"),
		Action:         arenasapp.ModerationRestrict,
		Reason:         arenaReason,
		OccurredAt:     now,
	}); err != nil {
		t.Fatalf("RecordArenaModeration: %v", err)
	}

	// Persuasion attribution shim.
	attributionID, err := persuasiondomain.ParseAttributionID("018f6b2a-0000-7000-8000-0000000000b1")
	if err != nil {
		t.Fatalf("ParseAttributionID: %v", err)
	}
	argumentID, err := persuasiondomain.ParseArgumentID("018f6b2a-0000-7000-8000-0000000000b2")
	if err != nil {
		t.Fatalf("ParseArgumentID: %v", err)
	}
	moderatorID, err := persuasiondomain.ParseModeratorID(actorID)
	if err != nil {
		t.Fatalf("ParseModeratorID: %v", err)
	}
	persuasionReason, err := persuasiondomain.ParseReason("Attribution farm with reciprocal credits")
	if err != nil {
		t.Fatalf("ParseReason: %v", err)
	}
	if err := repo.RecordAttributionModeration(ctx, persuasionapp.AttributionModerationEvent{
		ActorAccountID: moderatorID,
		AttributionID:  attributionID,
		ArgumentID:     argumentID,
		Action:         persuasiondomain.ModerationActionInvalidate,
		Reason:         persuasionReason,
		OccurredAt:     now,
	}); err != nil {
		t.Fatalf("RecordAttributionModeration: %v", err)
	}

	var actions []string
	rows, err := pool.Query(ctx, `SELECT action FROM app.audit_events WHERE actor_id = $1`, actor.ID)
	if err != nil {
		t.Fatalf("list audit actions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatalf("scan action: %v", err)
		}
		actions = append(actions, action)
	}
	// Same instant inserts have no defined order: compare as a set.
	got := map[string]bool{}
	for _, action := range actions {
		got[action] = true
	}
	for _, want := range []string{"wallet.adjust", "arena.restrict", "attribution.invalidate"} {
		if !got[want] {
			t.Fatalf("audit actions = %v, want the three integrated sources", actions)
		}
	}
	if len(actions) != 3 {
		t.Fatalf("audit actions = %v, want exactly 3", actions)
	}
}

func TestBillingAndAdminEventsFitTheTrail(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := auditpostgres.NewRepository(pool)
	q := platformpg.New(pool)
	now := time.Now().UTC()

	actor := mustAuditAccount(t, ctx, q, "audit-billing-admin@arena.example.com")
	actorID := uuidString(actor.ID)

	// Billing-flavored fact: a reconciliation run divergence, recorded
	// through the generic trail with allowlisted metadata.
	var runRow pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.billing_reconciliation_runs (livemode, window_start, window_end, status, finished_at)
		VALUES (false, now() - interval '1 day', now(), 'completed', now())
		RETURNING id`).Scan(&runRow); err != nil {
		t.Fatalf("seed reconciliation run: %v", err)
	}
	runID := uuidString(runRow)

	if _, err := repo.Record(ctx, auditdomain.AuditEvent{
		Actor:      actorID,
		Action:     "billing.reconcile",
		TargetType: "run",
		TargetID:   runID,
		ReasonCode: "amount_mismatch",
		Metadata: map[string]string{
			"outcome": "finding_recorded",
			"rule":    "catalog_price_is_truth",
		},
		OccurredAt: now,
	}); err != nil {
		t.Fatalf("record billing event: %v", err)
	}

	// Admin-flavored fact: a role assignment, recorded through the generic
	// trail with allowlisted metadata.
	grantee := mustAuditAccount(t, ctx, q, "audit-grantee@arena.example.com")
	if _, err := pool.Exec(ctx, `INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1, 'moderator', $2)`, grantee.ID, actor.ID); err != nil {
		t.Fatalf("grant role: %v", err)
	}
	if _, err := repo.Record(ctx, auditdomain.AuditEvent{
		Actor:      actorID,
		Action:     "admin.grant",
		TargetType: "account",
		TargetID:   uuidString(grantee.ID),
		ReasonCode: "role_assignment",
		Metadata: map[string]string{
			"target_account_id": uuidString(grantee.ID),
			"rule":              "moderator",
		},
		OccurredAt: now,
	}); err != nil {
		t.Fatalf("record admin event: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.audit_events WHERE actor_id = $1`, actor.ID).Scan(&count); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if count != 2 {
		t.Fatalf("audit events = %d, want billing + admin facts", count)
	}
}

func TestRecorderJoinsSharedTransaction(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := auditpostgres.NewRepository(pool)
	q := platformpg.New(pool)

	actor := mustAuditAccount(t, ctx, q, "audit-shared-tx@arena.example.com")

	newEvent := func() auditdomain.AuditEvent {
		return auditdomain.AuditEvent{
			Actor:      uuidString(actor.ID),
			Action:     "attribution.invalidate",
			TargetType: "attribution",
			TargetID:   "attribution-probe-1",
			ReasonCode: "attribution_moderation",
			OccurredAt: time.Now().UTC(),
		}
	}

	// Rollback: the record joins the caller transaction and vanishes with it.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if _, err := repo.Record(platformpg.WithTx(ctx, tx), newEvent()); err != nil {
		t.Fatalf("record in transaction: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var rolledBack int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.audit_events WHERE actor_id = $1`, actor.ID).Scan(&rolledBack); err != nil {
		t.Fatalf("count after rollback: %v", err)
	}
	if rolledBack != 0 {
		t.Fatalf("audit events after rollback = %d, want 0", rolledBack)
	}

	// Commit: the same record persists.
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if _, err := repo.Record(platformpg.WithTx(ctx, tx), newEvent()); err != nil {
		t.Fatalf("record in transaction: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var committed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.audit_events WHERE actor_id = $1`, actor.ID).Scan(&committed); err != nil {
		t.Fatalf("count after commit: %v", err)
	}
	if committed != 1 {
		t.Fatalf("audit events after commit = %d, want 1", committed)
	}
}
