package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// TestPositionSchemaConstraints proves the P09-T01 invariants: one initial
// position per account and Arena, the closed position vocabulary, positive
// versions and the foreign keys to Arenas and accounts.
func TestPositionSchemaConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "position-schema@arena.example.com")
	arenaID := insertDraftArena(t, ctx, pool, creator, "Afirmação do schema de posições", "technology", "pt-BR")
	other := mustCreateAccount(t, ctx, q, "position-schema-other@arena.example.com")

	if _, err := pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position)
		VALUES ($1, $2, 'agree', 'agree')`, arenaID, creator); err != nil {
		t.Fatalf("valid position rejected: %v", err)
	}

	// One initial position per account and Arena.
	_, err := pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position)
		VALUES ($1, $2, 'disagree', 'disagree')`, arenaID, creator)
	assertPgCode(t, err, "23505")

	probes := []struct {
		name    string
		initial string
		current string
		version int
		want    string
	}{
		{name: "unknown initial position", initial: "maybe", current: "agree", version: 1, want: "23514"},
		{name: "unknown current position", initial: "agree", current: "maybe", version: 1, want: "23514"},
		{name: "zero version", initial: "agree", current: "agree", version: 0, want: "23514"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position, version)
				VALUES ($1, $2, $3, $4, $5)`,
				arenaID, other.ID, probe.initial, probe.current, probe.version)
			assertPgCode(t, err, probe.want)
		})
	}

	// Orphan Arenas and accounts are refused.
	orphan := pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0x4a, 0x4b, 0x4c}, Valid: true}
	_, err = pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position)
		VALUES ($1, $2, 'agree', 'agree')`, orphan, creator)
	assertPgCode(t, err, "23503")
	_, err = pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position)
		VALUES ($1, $2, 'agree', 'agree')`, arenaID, orphan)
	assertPgCode(t, err, "23503")
}

func TestPositionChangesAppendOnlyChain(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "position-chain@arena.example.com")
	arenaID := insertDraftArena(t, ctx, pool, creator, "Afirmação da cadeia de posições", "philosophy", "pt-BR")
	other := mustCreateAccount(t, ctx, q, "position-chain-other@arena.example.com")

	if _, err := pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position)
		VALUES ($1, $2, 'agree', 'agree')`, arenaID, creator); err != nil {
		t.Fatalf("insert initial position: %v", err)
	}

	// A valid change advances the chain: agree -> disagree, version 2.
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.position_changes (arena_id, account_id, from_position, to_position, version)
		VALUES ($1, $2, 'agree', 'disagree', 2)`, arenaID, creator); err != nil {
		t.Fatalf("valid change rejected: %v", err)
	}

	probes := []struct {
		name    string
		from    string
		to      string
		version int
		want    string
	}{
		{name: "same position", from: "disagree", to: "disagree", version: 3, want: "23514"},
		{name: "version one", from: "disagree", to: "undecided", version: 1, want: "23514"},
		{name: "unknown from position", from: "maybe", to: "agree", version: 3, want: "23514"},
		{name: "unknown to position", from: "disagree", to: "maybe", version: 3, want: "23514"},
		{name: "reused chain version", from: "disagree", to: "undecided", version: 2, want: "23505"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO app.position_changes (arena_id, account_id, from_position, to_position, version)
				VALUES ($1, $2, $3, $4, $5)`,
				arenaID, creator, probe.from, probe.to, probe.version)
			assertPgCode(t, err, probe.want)
		})
	}

	// A change without its position row violates the composite foreign key.
	_, err := pool.Exec(ctx, `
		INSERT INTO app.position_changes (arena_id, account_id, from_position, to_position, version)
		VALUES ($1, $2, 'agree', 'disagree', 2)`, arenaID, other.ID)
	assertPgCode(t, err, "23503")

	// The initial position and its provenance are immutable.
	initialProbes := []struct {
		name string
		sql  string
	}{
		{name: "initial position", sql: "UPDATE app.debate_positions SET initial_position = 'disagree' WHERE arena_id = $1 AND account_id = $2"},
		{name: "arena", sql: "UPDATE app.debate_positions SET arena_id = gen_random_uuid() WHERE arena_id = $1 AND account_id = $2"},
		{name: "account", sql: "UPDATE app.debate_positions SET account_id = gen_random_uuid() WHERE arena_id = $1 AND account_id = $2"},
		{name: "created_at", sql: "UPDATE app.debate_positions SET created_at = now() WHERE arena_id = $1 AND account_id = $2"},
	}
	for _, probe := range initialProbes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, probe.sql, arenaID, creator)
			assertPgCode(t, err, "23514")
		})
	}

	// The projection moves with the accepted change.
	if _, err := pool.Exec(ctx, `
		UPDATE app.debate_positions
		SET current_position = 'disagree', version = 2, updated_at = now()
		WHERE arena_id = $1 AND account_id = $2`, arenaID, creator); err != nil {
		t.Fatalf("projection update rejected: %v", err)
	}

	// Changes are append-only for the owner role too.
	_, err = pool.Exec(ctx, `
		UPDATE app.position_changes SET to_position = 'undecided'
		WHERE arena_id = $1 AND account_id = $2`, arenaID, creator)
	assertPgCode(t, err, "23514")
	_, err = pool.Exec(ctx, `
		DELETE FROM app.position_changes
		WHERE arena_id = $1 AND account_id = $2`, arenaID, creator)
	assertPgCode(t, err, "23514")

	// And the runtime role cannot rewrite or delete them either: the
	// privilege itself is absent (42501), a stronger denial than the
	// defensive trigger proven above for roles holding UPDATE/DELETE.
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, `
			UPDATE app.position_changes SET to_position = 'undecided'
			WHERE arena_id = $1 AND account_id = $2`, arenaID, creator)
		assertPgCode(t, err, "42501")
	})
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, `
			DELETE FROM app.position_changes
			WHERE arena_id = $1 AND account_id = $2`, arenaID, creator)
		assertPgCode(t, err, "42501")
	})
}

func TestPositionRuntimeGrants(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool := db.Pool.Pool()

	checks := []struct {
		table string
		priv  string
		want  bool
	}{
		{"app.debate_positions", "SELECT", true},
		{"app.debate_positions", "INSERT", true},
		{"app.debate_positions", "UPDATE", true},
		{"app.debate_positions", "DELETE", false},
		{"app.position_changes", "SELECT", true},
		{"app.position_changes", "INSERT", true},
		{"app.position_changes", "UPDATE", false},
		{"app.position_changes", "DELETE", false},
	}
	for _, check := range checks {
		var allowed bool
		if err := pool.QueryRow(ctx,
			"SELECT has_table_privilege('arena_app', $1, $2)", check.table, check.priv,
		).Scan(&allowed); err != nil {
			t.Fatalf("has_table_privilege(%s, %s): %v", check.table, check.priv, err)
		}
		if allowed != check.want {
			t.Errorf("arena_app %s on %s = %v, want %v", check.priv, check.table, allowed, check.want)
		}
	}

	// The runtime may confirm a position and move the projection, but never
	// rewrite the initial choice or delete the row.
	q := postgres.New(db.Pool)
	creator := mustArenaCreator(t, ctx, q, "position-grants@arena.example.com")
	arenaID := insertDraftArena(t, ctx, pool, creator, "Afirmação para probe de runtime", "society", "pt-BR")

	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position)
			VALUES ($1, $2, 'agree', 'agree')`, arenaID, creator); err != nil {
			t.Fatalf("runtime insert rejected: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE app.debate_positions
			SET current_position = 'disagree', version = 2, updated_at = now()
			WHERE arena_id = $1 AND account_id = $2`, arenaID, creator); err != nil {
			t.Fatalf("runtime projection update rejected: %v", err)
		}
		_, err := tx.Exec(ctx, `
			UPDATE app.debate_positions SET initial_position = 'undecided'
			WHERE arena_id = $1 AND account_id = $2`, arenaID, creator)
		assertPgCode(t, err, "23514")
	})
	withAppRole(t, pool, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, `
			DELETE FROM app.debate_positions
			WHERE arena_id = $1 AND account_id = $2`, arenaID, creator)
		assertPgCode(t, err, "42501")
	})
}
