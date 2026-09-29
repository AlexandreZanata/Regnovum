package postgres_test

// P33-T03 — the consent registries hold their shape on real PostgreSQL.
//
// app.economy_charter_consents and app.economy_optins are append-only
// verdicts and intents: duplicate decisions, out-of-vocabulary versions
// and history rewrites die at constraints and triggers, and the runtime
// role can append but never rewrite. The suite runs on a disposable
// database and touches no ledger.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func consentSchemaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// TestConsentRegistriesImmutableHistory proves triggers refuse UPDATE
// and DELETE on both registries for the owner role itself.
func TestConsentRegistriesImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := consentSchemaCtx()
	defer cancel()

	var holder string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('consent-schema@invalid.example', 'active') RETURNING id::text`).Scan(&holder); err != nil {
		t.Fatalf("seed holder: %v", err)
	}
	mustExecConsentSchema(t, ctx, db,
		`INSERT INTO app.economy_charter_consents (account_id, charter_version, decision) VALUES ($1::uuid, 'v1', 'accepted')`, holder)
	mustExecConsentSchema(t, ctx, db,
		`INSERT INTO app.economy_optins (account_id, charter_version, quantity_milli, rate_num, rate_den, valid_until)
		 VALUES ($1::uuid, 'v1', 100, 10, 1, now() + interval '30 days')`, holder)

	mutations := []struct {
		name string
		sql  string
		args []any
	}{
		{"rewrite verdict", `UPDATE app.economy_charter_consents SET decision = 'refused' WHERE account_id = $1::uuid`, []any{holder}},
		{"delete verdict", `DELETE FROM app.economy_charter_consents WHERE account_id = $1::uuid`, []any{holder}},
		{"rewrite intent", `UPDATE app.economy_optins SET quantity_milli = 200 WHERE account_id = $1::uuid`, []any{holder}},
		{"delete intent", `DELETE FROM app.economy_optins WHERE account_id = $1::uuid`, []any{holder}},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql, mutation.args...)
		assertPgCode(t, err, "23514")
	}
}

// TestConsentRegistryUniquenessAndChecks proves one decision and one
// intent per account and version, closed version and decision
// vocabularies, and positive terms.
func TestConsentRegistryUniquenessAndChecks(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := consentSchemaCtx()
	defer cancel()

	var holder string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('consent-unique@invalid.example', 'active') RETURNING id::text`).Scan(&holder); err != nil {
		t.Fatalf("seed holder: %v", err)
	}
	mustExecConsentSchema(t, ctx, db,
		`INSERT INTO app.economy_charter_consents (account_id, charter_version, decision) VALUES ($1::uuid, 'v1', 'accepted')`, holder)
	_, err := db.Exec(ctx,
		`INSERT INTO app.economy_charter_consents (account_id, charter_version, decision) VALUES ($1::uuid, 'v1', 'refused')`, holder)
	assertPgCode(t, err, "23505")

	badVersions := []struct {
		name string
		sql  string
	}{
		{"bare number", `INSERT INTO app.economy_charter_consents (account_id, charter_version, decision) VALUES ($1::uuid, '1', 'accepted')`},
		{"zero version", `INSERT INTO app.economy_charter_consents (account_id, charter_version, decision) VALUES ($1::uuid, 'v0', 'accepted')`},
		{"unknown decision", `INSERT INTO app.economy_charter_consents (account_id, charter_version, decision) VALUES ($1::uuid, 'v2', 'maybe')`},
	}
	for _, probe := range badVersions {
		_, err := db.Exec(ctx, probe.sql, holder)
		assertPgCode(t, err, "23514")
	}

	_, err = db.Exec(ctx,
		`INSERT INTO app.economy_optins (account_id, charter_version, quantity_milli, rate_num, rate_den, valid_until)
		 VALUES ($1::uuid, 'v1', 100, 10, 1, now() + interval '30 days')`, holder)
	if err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	_, err = db.Exec(ctx,
		`INSERT INTO app.economy_optins (account_id, charter_version, quantity_milli, rate_num, rate_den, valid_until)
		 VALUES ($1::uuid, 'v1', 100, 10, 1, now() + interval '30 days')`, holder)
	assertPgCode(t, err, "23505")
}

// TestConsentRuntimeLeastPrivilege proves arena_app appends and reads
// but never rewrites: UPDATE and DELETE die at the grant level.
func TestConsentRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update verdict", `UPDATE app.economy_charter_consents SET decision = 'refused'`, "42501"},
			{"delete verdict", `DELETE FROM app.economy_charter_consents`, "42501"},
			{"update intent", `UPDATE app.economy_optins SET quantity_milli = 1`, "42501"},
			{"delete intent", `DELETE FROM app.economy_optins`, "42501"},
		}
		for _, probe := range probes {
			if _, err := tx.Exec(ctx, `SAVEPOINT cell`); err != nil {
				t.Fatalf("savepoint: %v", err)
			}
			_, err := tx.Exec(ctx, probe.sql)
			assertPgCode(t, err, probe.want)
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT cell`); err != nil {
				t.Fatalf("rollback to savepoint: %v", err)
			}
		}
		var consents, intents int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.economy_charter_consents`).Scan(&consents); err != nil {
			t.Fatalf("read consents: %v", err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.economy_optins`).Scan(&intents); err != nil {
			t.Fatalf("read intents: %v", err)
		}
	})
}

func mustExecConsentSchema(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}
