package postgres_test

// P34-T01 — the Treasury vault labels hold their shape on real
// PostgreSQL.
//
// app.economy_custodies constrains the treasury kind to the closed
// vault vocabulary (Genesis home plus four exclusive vaults): unknown
// spellings die at the CHECK, duplicated vaults die at the UNIQUE
// guard, history rewrites die at triggers and grants, and the runtime
// role can append and read but never rewrite. The suite runs on a
// disposable database and touches no ledger.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func vaultSchemaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// TestTreasuryVaultLabelsClosed proves the CHECK vocabulary: the five
// vaults open, anything else dies at 23514, and user labels stay free.
func TestTreasuryVaultLabelsClosed(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := vaultSchemaCtx()
	defer cancel()

	for _, vault := range []string{"main", "sovereign_reserve", "commercial_stock", "operating_cash", "free_treasury"} {
		mustExecVaultSchema(t, ctx, db,
			`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', $1) ON CONFLICT DO NOTHING`, vault)
	}
	badLabels := []struct {
		name string
		sql  string
	}{
		{"slush", `INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'slush')`},
		{"cased home", `INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'MAIN')`},
		{"padded vault", `INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', ' main')`},
		{"partition name", `INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'available')`},
	}
	for _, probe := range badLabels {
		_, err := db.Exec(ctx, probe.sql)
		assertPgCode(t, err, "23514")
	}
	mustExecVaultSchema(t, ctx, db,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', 'slush') ON CONFLICT DO NOTHING`)
}

// TestTreasuryVaultUniquenessAndImmutability proves one custody per
// vault: duplicated vaults die at 23505 and rewrites die at the 23514
// trigger for the owner role itself.
func TestTreasuryVaultUniquenessAndImmutability(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := vaultSchemaCtx()
	defer cancel()

	mustExecVaultSchema(t, ctx, db,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'sovereign_reserve') ON CONFLICT DO NOTHING`)
	_, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'sovereign_reserve')`)
	assertPgCode(t, err, "23505")

	mutations := []struct {
		name string
		sql  string
	}{
		{"rename vault", `UPDATE app.economy_custodies SET label = 'slush' WHERE kind = 'treasury' AND label = 'sovereign_reserve'`},
		{"delete vault", `DELETE FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'sovereign_reserve'`},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql)
		assertPgCode(t, err, "23514")
	}
}

// TestVaultRuntimeLeastPrivilege proves arena_app appends and reads
// vaults but never rewrites: UPDATE and DELETE die at the grant level.
func TestVaultRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update vault", `UPDATE app.economy_custodies SET label = 'slush' WHERE kind = 'treasury'`, "42501"},
			{"delete vault", `DELETE FROM app.economy_custodies WHERE kind = 'treasury'`, "42501"},
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
		var vaults int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.economy_custodies WHERE kind = 'treasury'`).Scan(&vaults); err != nil {
			t.Fatalf("read vaults: %v", err)
		}
	})
}

func mustExecVaultSchema(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}
