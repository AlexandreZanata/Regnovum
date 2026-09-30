package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

var retentionSchemaInstant = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func mustRetentionHold(t *testing.T, ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, class string, account, placedBy pgtype.UUID, reason string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.retention_holds (data_class, account_id, reason_code, placed_by, placed_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`, class, nullableUUID(account), reason, placedBy, retentionSchemaInstant).Scan(&id); err != nil {
		t.Fatalf("insert retention hold: %v", err)
	}
	return id
}

func mustRetentionRun(t *testing.T, ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, class string, executedAt time.Time, cutoff any, purged, anonymized, retained, held int32) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.retention_runs (
			data_class, executed_at, cutoff_at, purged_count, anonymized_count, retained_count, held_count
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`, class, executedAt, cutoff, purged, anonymized, retained, held).Scan(&id); err != nil {
		t.Fatalf("insert retention run: %v", err)
	}
	return id
}

// TestRetentionSchemaConstraints proves the ledger and the holds refuse an
// incoherent row instead of storing one: the class vocabulary is closed, the
// counts are non-negative, the cutoff only exists for classes with a purge
// horizon, and one class records one outcome per instant.
func TestRetentionSchemaConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)
	admin := mustCreateAccount(t, ctx, q, "retention-admin@arena.example.com")
	holder := mustCreateAccount(t, ctx, q, "retention-holder@arena.example.com")
	cutoff := pgtype.Timestamptz{Time: retentionSchemaInstant.Add(-30 * 24 * time.Hour), Valid: true}

	// The whole governed vocabulary is accepted by both tables.
	for _, class := range []string{"tokens", "sessions", "referential_logs", "exports", "abuse_signals", "billing"} {
		hasCutoff := class != "referential_logs" && class != "billing"
		cutoffValue := any(nil)
		if hasCutoff {
			cutoffValue = cutoff
		}
		mustRetentionRun(t, ctx, db.Pool, class, retentionSchemaInstant, cutoffValue, 1, 0, 0, 0)
		mustRetentionHold(t, ctx, db.Pool, class, holder.ID, admin.ID, "litigation")
	}

	// Unknown classes are refused by both tables.
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO app.retention_runs (data_class, executed_at, cutoff_at)
		VALUES ('wallet', $1, $2)`, retentionSchemaInstant, cutoff)
	assertPgErrorCode(t, err, "23514")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO app.retention_holds (data_class, reason_code, placed_by)
		VALUES ('wallet', 'litigation', $1)`, admin.ID)
	assertPgErrorCode(t, err, "23514")

	// Counts are never negative.
	for _, column := range []string{"purged_count", "anonymized_count", "retained_count", "held_count"} {
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO app.retention_runs (data_class, executed_at, cutoff_at, `+column+`)
			VALUES ('tokens', $1, $2, -1)`, retentionSchemaInstant.Add(time.Minute), cutoff)
		assertPgErrorCode(t, err, "23514")
	}

	// The cutoff rule: a retained class never dates a boundary, and a class
	// with a horizon always does.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO app.retention_runs (data_class, executed_at, cutoff_at)
		VALUES ('billing', $1, $2)`, retentionSchemaInstant.Add(time.Minute), cutoff)
	assertPgErrorCode(t, err, "23514")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO app.retention_runs (data_class, executed_at, cutoff_at)
		VALUES ('tokens', $1, NULL)`, retentionSchemaInstant.Add(time.Minute))
	assertPgErrorCode(t, err, "23514")

	// One class records one outcome per instant.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO app.retention_runs (data_class, executed_at, cutoff_at)
		VALUES ('tokens', $1, $2)`, retentionSchemaInstant, cutoff)
	assertPgErrorCode(t, err, "23505")
	// A different instant is a different outcome.
	mustRetentionRun(t, ctx, db.Pool, "tokens", retentionSchemaInstant.Add(time.Minute), cutoff, 0, 0, 0, 0)

	// The reason code of a hold is a bounded stable code.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO app.retention_holds (data_class, reason_code, placed_by)
		VALUES ('tokens', '   ', $1)`, admin.ID)
	assertPgErrorCode(t, err, "23514")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO app.retention_holds (data_class, reason_code, placed_by)
		VALUES ('tokens', repeat('x', 101), $1)`, admin.ID)
	assertPgErrorCode(t, err, "23514")

	// A hold is released completely or not at all.
	activeHold := mustRetentionHold(t, ctx, db.Pool, "exports", pgtype.UUID{}, admin.ID, "regulator_request")
	_, err = db.Pool.Exec(ctx, `UPDATE app.retention_holds SET released_at = $1 WHERE id = $2`,
		retentionSchemaInstant.Add(time.Hour), activeHold)
	assertPgErrorCode(t, err, "23514")
	// The release cannot predate the placement.
	_, err = db.Pool.Exec(ctx, `
		UPDATE app.retention_holds
		SET released_at = $1, release_reason_code = 'matter_closed'
		WHERE id = $2`, retentionSchemaInstant.Add(-time.Hour), activeHold)
	assertPgErrorCode(t, err, "23514")

	// An orphan holder is refused: holds are provenance.
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO app.retention_holds (data_class, reason_code, placed_by)
		VALUES ('sessions', 'litigation', '00000000-0000-0000-0000-0000000000ff')`)
	assertPgErrorCode(t, err, "23503")
}

// TestRetentionHoldUniquenessAndImmutability proves the two uniqueness rules
// (one active hold per class and account, one per whole class) and the
// provenance rules that make a hold evidence.
func TestRetentionHoldUniquenessAndImmutability(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)
	admin := mustCreateAccount(t, ctx, q, "retention-admin2@arena.example.com")
	holder := mustCreateAccount(t, ctx, q, "retention-holder2@arena.example.com")

	// One active hold per class and account.
	hold := mustRetentionHold(t, ctx, db.Pool, "sessions", holder.ID, admin.ID, "litigation")
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO app.retention_holds (data_class, account_id, reason_code, placed_by)
		VALUES ('sessions', $1, 'litigation', $2)`, holder.ID, admin.ID)
	assertPgErrorCode(t, err, "23505")

	// The same account can be held in another class.
	mustRetentionHold(t, ctx, db.Pool, "tokens", holder.ID, admin.ID, "litigation")

	// One active whole-class hold, and only one.
	whole := mustRetentionHold(t, ctx, db.Pool, "exports", pgtype.UUID{}, admin.ID, "regulator_request")
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO app.retention_holds (data_class, account_id, reason_code, placed_by)
		VALUES ('exports', NULL, 'regulator_request', $1)`, admin.ID)
	assertPgErrorCode(t, err, "23505")

	// Releasing the whole-class hold frees the slot for a new one.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE app.retention_holds
		SET released_at = $1, release_reason_code = 'matter_closed'
		WHERE id = $2`, retentionSchemaInstant.Add(time.Hour), whole); err != nil {
		t.Fatalf("release whole-class hold: %v", err)
	}
	mustRetentionHold(t, ctx, db.Pool, "exports", pgtype.UUID{}, admin.ID, "regulator_request")

	// Provenance is immutable.
	provenance := []struct {
		name string
		sql  string
		args []any
	}{
		{"class", `UPDATE app.retention_holds SET data_class = 'billing' WHERE id = $1`, []any{hold}},
		{"account", `UPDATE app.retention_holds SET account_id = NULL WHERE id = $1`, []any{hold}},
		{"reason", `UPDATE app.retention_holds SET reason_code = 'other_reason' WHERE id = $1`, []any{hold}},
		{"grantor", `UPDATE app.retention_holds SET placed_by = $2 WHERE id = $1`, []any{hold, holder.ID}},
		{"placement", `UPDATE app.retention_holds SET placed_at = $2 WHERE id = $1`, []any{hold, retentionSchemaInstant.Add(-time.Hour)}},
	}
	for _, testCase := range provenance {
		t.Run("immutable "+testCase.name, func(t *testing.T) {
			_, err := db.Pool.Exec(ctx, testCase.sql, testCase.args...)
			assertPgErrorCode(t, err, "23514")
		})
	}

	// Releasing happens once: a released hold cannot be re-released or
	// re-pointed.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE app.retention_holds
		SET released_at = $1, release_reason_code = 'matter_closed'
		WHERE id = $2`, retentionSchemaInstant.Add(time.Hour), hold); err != nil {
		t.Fatalf("release hold: %v", err)
	}
	_, err = db.Pool.Exec(ctx, `
		UPDATE app.retention_holds
		SET released_at = $1, release_reason_code = 'other'
		WHERE id = $2`, retentionSchemaInstant.Add(2*time.Hour), hold)
	assertPgErrorCode(t, err, "23514")

	// Holds and ledger rows are never deleted.
	_, err = db.Pool.Exec(ctx, `DELETE FROM app.retention_holds WHERE id = $1`, hold)
	assertPgErrorCode(t, err, "23514")
	run := mustRetentionRun(t, ctx, db.Pool, "tokens", retentionSchemaInstant.Add(2*time.Hour), pgtype.Timestamptz{Time: retentionSchemaInstant.Add(-time.Hour), Valid: true}, 1, 0, 0, 0)
	_, err = db.Pool.Exec(ctx, `DELETE FROM app.retention_runs WHERE id = $1`, run)
	assertPgErrorCode(t, err, "23514")
	_, err = db.Pool.Exec(ctx, `UPDATE app.retention_runs SET purged_count = 99 WHERE id = $1`, run)
	assertPgErrorCode(t, err, "23514")
}

// TestRetentionLeastPrivilege proves the runtime surface: holds are read,
// placed and released but never deleted, and the ledger is append-only by
// privilege as well as by trigger.
func TestRetentionLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)
	admin := mustCreateAccount(t, ctx, q, "retention-admin3@arena.example.com")
	holder := mustCreateAccount(t, ctx, q, "retention-holder3@arena.example.com")

	checks := []struct {
		table string
		priv  string
		want  bool
	}{
		{"app.retention_holds", "SELECT", true},
		{"app.retention_holds", "INSERT", true},
		{"app.retention_holds", "UPDATE", true},
		{"app.retention_holds", "DELETE", false},
		{"app.retention_runs", "SELECT", true},
		{"app.retention_runs", "INSERT", true},
		{"app.retention_runs", "UPDATE", false},
		{"app.retention_runs", "DELETE", false},
	}
	for _, check := range checks {
		var allowed bool
		if err := db.Pool.QueryRow(ctx,
			"SELECT has_table_privilege('arena_app', $1, $2)", check.table, check.priv,
		).Scan(&allowed); err != nil {
			t.Fatalf("has_table_privilege(%s, %s): %v", check.table, check.priv, err)
		}
		if allowed != check.want {
			t.Errorf("arena_app %s on %s = %v, want %v", check.priv, check.table, allowed, check.want)
		}
	}

	// The runtime places a hold and releases it through the real role.
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		var holdID pgtype.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO app.retention_holds (data_class, account_id, reason_code, placed_by)
			VALUES ('sessions', $1, 'litigation', $2)
			RETURNING id`, holder.ID, admin.ID).Scan(&holdID); err != nil {
			t.Fatalf("runtime insert hold: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE app.retention_holds
			SET released_at = now(), release_reason_code = 'matter_closed'
			WHERE id = $1`, holdID); err != nil {
			t.Fatalf("runtime release hold: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO app.retention_runs (data_class, executed_at, cutoff_at, purged_count)
			VALUES ('tokens', $1, $2, 3)`, retentionSchemaInstant, retentionSchemaInstant.Add(-30*24*time.Hour)); err != nil {
			t.Fatalf("runtime insert run: %v", err)
		}
	})

	probes := []struct {
		name string
		sql  string
	}{
		{"delete hold", "DELETE FROM app.retention_holds"},
		{"update run", "UPDATE app.retention_runs SET purged_count = 0"},
		{"delete run", "DELETE FROM app.retention_runs"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
				if _, err := tx.Exec(ctx, probe.sql); err == nil {
					t.Fatalf("%s must be rejected for the runtime role", probe.name)
				}
			})
		})
	}
}
