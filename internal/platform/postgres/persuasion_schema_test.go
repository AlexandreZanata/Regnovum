package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// uuidOrNil renders an optional identifier as its database parameter.
func uuidOrNil(value *pgtype.UUID) pgtype.UUID {
	if value == nil {
		return pgtype.UUID{}
	}
	return *value
}

// mustAttributionContext seeds one author with a position change and one
// argument authored by another account, returning the identifiers the
// schema tests link together.
func mustAttributionContext(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *postgres.Queries, email string) (author, other, arena, argumentID, changeID pgtype.UUID) {
	t.Helper()
	author = mustCreateAccount(t, ctx, q, email).ID
	other = mustCreateAccount(t, ctx, q, "attribution-other-"+email).ID

	slug := "attribution-" + strings.NewReplacer("@", "-", ".", "-").Replace(email)
	arena = insertDraftArena(t, ctx, pool, author, "Afirmação para atribuições de persuasão", "technology", "pt-BR")
	publishArena(t, ctx, pool, arena, slug, time.Now().UTC())

	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, 'support', 'Argumento elegível para atribuição', $3, 33)
		RETURNING id`, arena, other, validArgumentHash).Scan(&argumentID); err != nil {
		t.Fatalf("insert argument: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position, version)
		VALUES ($1, $2, 'agree', 'disagree', 2)`, arena, author); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.position_changes (arena_id, account_id, from_position, to_position, version, changed_at)
		VALUES ($1, $2, 'agree', 'disagree', 2, now())
		RETURNING id`, arena, author).Scan(&changeID); err != nil {
		t.Fatalf("insert position change: %v", err)
	}
	return author, other, arena, argumentID, changeID
}

func TestPersuasionAttributionSchemaConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	author, other, _, argumentID, changeID := mustAttributionContext(t, ctx, pool, q, "attribution-schema@arena.example.com")
	moderator := mustCreateAccount(t, ctx, q, "attribution-moderator@arena.example.com").ID

	// A valid attribution.
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id)
		VALUES ($1, $2, $3)`, changeID, author, argumentID); err != nil {
		t.Fatalf("valid attribution rejected: %v", err)
	}

	// The same argument cannot be credited twice by the same change.
	_, err := pool.Exec(ctx, `
		INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id)
		VALUES ($1, $2, $3)`, changeID, author, argumentID)
	assertPgCode(t, err, "23505")

	orphan := pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x91, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0x9b, 0x9c}, Valid: true}

	// A second argument keeps every probe on a fresh (change, argument)
	// pair, so the constraint under test is the one that fires.
	var secondArgument pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
		SELECT arena_id, author_id, 'oppose', 'Segundo argumento para atribuição', $2, 37
		FROM app.arguments WHERE id = $1
		RETURNING id`, argumentID, validArgumentHash).Scan(&secondArgument); err != nil {
		t.Fatalf("insert second argument: %v", err)
	}

	now := time.Now().UTC()
	reason := "atribuição fraudulenta confirmada"
	blankReason := "   "
	longReason := strings.Repeat("a", 501)

	// The decision record is all-or-nothing: validity only moves through a
	// complete decision, so no projection can observe an unrecorded change.
	probes := []struct {
		name        string
		changeID    pgtype.UUID
		attributor  pgtype.UUID
		argumentID  pgtype.UUID
		status      string
		invalidated *time.Time
		reason      *string
		moderator   *pgtype.UUID
		want        string
	}{
		{name: "unknown change", changeID: orphan, attributor: author, argumentID: secondArgument, status: "valid", want: "23503"},
		{name: "unknown argument", changeID: changeID, attributor: author, argumentID: orphan, status: "valid", want: "23503"},
		{name: "mismatched attributor", changeID: changeID, attributor: other, argumentID: secondArgument, status: "valid", want: "23503"},
		{name: "unknown status", changeID: changeID, attributor: author, argumentID: secondArgument, status: "maybe", want: "23514"},
		{name: "invalid without instant", changeID: changeID, attributor: author, argumentID: secondArgument, status: "invalid", reason: &reason, moderator: &moderator, want: "23514"},
		{name: "invalid without decision", changeID: changeID, attributor: author, argumentID: secondArgument, status: "invalid", invalidated: &now, want: "23514"},
		{name: "decision without reason", changeID: changeID, attributor: author, argumentID: secondArgument, status: "invalid", invalidated: &now, moderator: &moderator, want: "23514"},
		{name: "decision without moderator", changeID: changeID, attributor: author, argumentID: secondArgument, status: "invalid", invalidated: &now, reason: &reason, want: "23514"},
		{name: "decision without instant", changeID: changeID, attributor: author, argumentID: secondArgument, status: "invalid", reason: &reason, moderator: &moderator, want: "23514"},
		{name: "blank reason", changeID: changeID, attributor: author, argumentID: secondArgument, status: "invalid", invalidated: &now, reason: &blankReason, moderator: &moderator, want: "23514"},
		{name: "reason above the bound", changeID: changeID, attributor: author, argumentID: secondArgument, status: "invalid", invalidated: &now, reason: &longReason, moderator: &moderator, want: "23514"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id, status, invalidated_at, moderation_reason, moderated_by, moderated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $5)`,
				probe.changeID, probe.attributor, probe.argumentID, probe.status, timeOrNil(probe.invalidated),
				textOrNil(probe.reason), uuidOrNil(probe.moderator))
			assertPgCode(t, err, probe.want)
		})
	}

	// The inverse incoherence is refused too: a valid attribution cannot
	// carry an invalidation instant.
	_, err = pool.Exec(ctx, `
		INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id, status, invalidated_at)
		VALUES ($1, $2, $3, 'valid', $4)`, changeID, author, secondArgument, now)
	assertPgCode(t, err, "23514")

	// An invalid attribution carrying its complete decision is accepted.
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id, status, invalidated_at, moderation_reason, moderated_by, moderated_at)
		VALUES ($1, $2, $3, 'invalid', $4, $5, $6, $4)`, changeID, author, secondArgument, now, reason, moderator); err != nil {
		t.Fatalf("invalid attribution rejected: %v", err)
	}

	// A restored attribution is valid and keeps its decision record.
	var thirdArgument pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost)
		SELECT arena_id, author_id, 'oppose', 'Terceiro argumento para atribuição', $2, 41
		FROM app.arguments WHERE id = $1
		RETURNING id`, argumentID, validArgumentHash).Scan(&thirdArgument); err != nil {
		t.Fatalf("insert third argument: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id, status, moderation_reason, moderated_by, moderated_at)
		VALUES ($1, $2, $3, 'valid', $4, $5, $6)`, changeID, author, thirdArgument, reason, moderator, now); err != nil {
		t.Fatalf("restored attribution rejected: %v", err)
	}
}

func TestPersuasionAttributionImmutabilityAndRetention(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	author, _, _, argumentID, changeID := mustAttributionContext(t, ctx, pool, q, "attribution-immutable@arena.example.com")
	moderator := mustCreateAccount(t, ctx, q, "attribution-immutable-moderator@arena.example.com").ID

	var attributionID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id)
		VALUES ($1, $2, $3)
		RETURNING id`, changeID, author, argumentID).Scan(&attributionID); err != nil {
		t.Fatalf("insert attribution: %v", err)
	}

	probes := []struct {
		name string
		sql  string
	}{
		{name: "change", sql: "UPDATE app.persuasion_attributions SET position_change_id = gen_random_uuid() WHERE id = $1"},
		{name: "attributor", sql: "UPDATE app.persuasion_attributions SET attributor_id = gen_random_uuid() WHERE id = $1"},
		{name: "argument", sql: "UPDATE app.persuasion_attributions SET argument_id = gen_random_uuid() WHERE id = $1"},
		{name: "created_at", sql: "UPDATE app.persuasion_attributions SET created_at = now() WHERE id = $1"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, probe.sql, attributionID)
			assertPgCode(t, err, "23514")
		})
	}

	// Invalidation and restoration move the validity and the decision record
	// on the retained row.
	if _, err := pool.Exec(ctx, `
		UPDATE app.persuasion_attributions
		SET status = 'invalid', invalidated_at = now(),
		    moderation_reason = 'atribuição fraudulenta confirmada',
		    moderated_by = $2, moderated_at = now()
		WHERE id = $1`, attributionID, moderator); err != nil {
		t.Fatalf("invalidation rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE app.persuasion_attributions
		SET status = 'valid', invalidated_at = NULL,
		    moderation_reason = 'recurso aceito',
		    moderated_by = $2, moderated_at = now()
		WHERE id = $1`, attributionID, moderator); err != nil {
		t.Fatalf("restoration rejected: %v", err)
	}

	// The decision record is preserved: a restored attribution may not drop
	// its moderation history (only the trigger can reject this, since a valid
	// attribution without a decision is otherwise coherent).
	_, err := pool.Exec(ctx, `
		UPDATE app.persuasion_attributions
		SET moderation_reason = NULL, moderated_by = NULL, moderated_at = NULL
		WHERE id = $1`, attributionID)
	assertPgCode(t, err, "23514")

	// An unrecorded invalidation is refused even for the owner.
	_, err = pool.Exec(ctx, `
		UPDATE app.persuasion_attributions SET status = 'invalid', invalidated_at = now()
		WHERE id = $1`, attributionID)
	assertPgCode(t, err, "23514")

	// Attributions are retained for every role.
	_, err = pool.Exec(ctx, "DELETE FROM app.persuasion_attributions WHERE id = $1", attributionID)
	assertPgCode(t, err, "23514")
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, "DELETE FROM app.persuasion_attributions WHERE id = $1", attributionID)
		assertPgCode(t, err, "42501")
	})
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, "UPDATE app.persuasion_attributions SET argument_id = gen_random_uuid() WHERE id = $1", attributionID)
		assertPgCode(t, err, "23514")
	})
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			UPDATE app.persuasion_attributions
			SET status = 'invalid', invalidated_at = now(),
			    moderation_reason = 'invalidação pelo runtime',
			    moderated_by = $2, moderated_at = now()
			WHERE id = $1`, attributionID, moderator); err != nil {
			t.Fatalf("runtime invalidation rejected: %v", err)
		}
	})
	// The runtime cannot invalidate without recording the decision either.
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, `
			UPDATE app.persuasion_attributions SET status = 'invalid', invalidated_at = now()
			WHERE id = $1`, attributionID)
		assertPgCode(t, err, "23514")
	})
}

// TestPersuasionAttributionPrivacy proves the P11-T01 validation: no
// database view exposes the attribution table, so no public projection can
// carry the attributor account identifier.
func TestPersuasionAttributionPrivacy(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()

	var views int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_views
		WHERE schemaname = 'app'
		  AND (definition ILIKE '%persuasion_attributions%' OR definition ILIKE '%attributor%')`).Scan(&views); err != nil {
		t.Fatalf("scan pg_views: %v", err)
	}
	if views != 0 {
		t.Fatalf("views exposing attributions = %d, want 0", views)
	}

	var viewColumns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.view_column_usage
		WHERE table_schema = 'app' AND table_name = 'persuasion_attributions'`).Scan(&viewColumns); err != nil {
		t.Fatalf("scan view_column_usage: %v", err)
	}
	if viewColumns != 0 {
		t.Fatalf("views referencing attribution columns = %d, want 0", viewColumns)
	}

	// The identity column is exactly the private one the phase names.
	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'app' AND table_name = 'persuasion_attributions' AND column_name = 'attributor_id'`).Scan(&columns); err != nil {
		t.Fatalf("scan columns: %v", err)
	}
	if columns != 1 {
		t.Fatalf("attributor_id columns = %d, want exactly one", columns)
	}
}

func TestPersuasionAttributionRuntimeGrants(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()

	checks := []struct {
		priv string
		want bool
	}{
		{"SELECT", true},
		{"INSERT", true},
		{"UPDATE", true},
		{"DELETE", false},
	}
	for _, check := range checks {
		var allowed bool
		if err := pool.QueryRow(ctx,
			"SELECT has_table_privilege('arena_app', 'app.persuasion_attributions', $1)", check.priv,
		).Scan(&allowed); err != nil {
			t.Fatalf("has_table_privilege(%s): %v", check.priv, err)
		}
		if allowed != check.want {
			t.Errorf("arena_app %s on app.persuasion_attributions = %v, want %v", check.priv, allowed, check.want)
		}
	}
}
