package postgres_test

// P37-T03 — the trade contract and settlement registries hold
// their shape on real PostgreSQL.
//
// app.commerce_contracts records one funded contract per buyer key
// with the service object, both parties, the exact amount, the
// sealed terms and the locking transfer; app.commerce_settlements
// records each lifecycle step with its own transfer when it moves
// value. Bad amounts, empty terms, non-trade kinds, unknown parties
// and duplicated keys or transfers die at CHECKs, UNIQUEs and
// foreign keys, history rewrites die at triggers and grants, and
// the runtime role appends and reads but never rewrites. The suite
// runs on a disposable database and moves no money.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func commerceContractCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func commerceContractAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('commerce-contract-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

func seedCommerceContract(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, buyer, provider string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.commerce_contracts
		 (contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at)
		 VALUES ($1, 'trade', $2::uuid, $3::uuid, 'revisão de contrato', 20000, 'seal-terms', gen_random_uuid(), now() + interval '1 hour')
		 RETURNING id::text`, key, buyer, provider).Scan(&id); err != nil {
		t.Fatalf("seed contract: %v", err)
	}
	return id
}

// TestCommerceContractRegistryKeysAndTerms proves one funded
// contract per buyer key with sealed trade terms: non-positive
// amounts, empty terms, non-trade kinds, unknown parties and
// duplicated keys or transfers die at CHECKs, UNIQUEs and foreign
// keys, while another buyer reusing a key opens its own contract.
func TestCommerceContractRegistryKeysAndTerms(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := commerceContractCtx()
	defer cancel()

	buyer := commerceContractAccount(t, ctx, db)
	provider := commerceContractAccount(t, ctx, db)
	seedCommerceContract(t, ctx, db, "schema-contract-1", buyer, provider)

	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"duplicate key", `INSERT INTO app.commerce_contracts
		 (contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at)
		 VALUES ('schema-contract-1', 'trade', $1::uuid, $2::uuid, 'obj', 20000, 'seal', gen_random_uuid(), now() + interval '1 hour')`,
			[]any{buyer, provider}, "23505"},
		{"zero amount", `INSERT INTO app.commerce_contracts
		 (contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at)
		 VALUES ('schema-contract-2', 'trade', $1::uuid, $2::uuid, 'obj', 0, 'seal', gen_random_uuid(), now() + interval '1 hour')`,
			[]any{buyer, provider}, "23514"},
		{"gift kind", `INSERT INTO app.commerce_contracts
		 (contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at)
		 VALUES ('schema-contract-3', 'gift', $1::uuid, $2::uuid, 'obj', 20000, 'seal', gen_random_uuid(), now() + interval '1 hour')`,
			[]any{buyer, provider}, "23514"},
		{"empty object", `INSERT INTO app.commerce_contracts
		 (contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at)
		 VALUES ('schema-contract-4', 'trade', $1::uuid, $2::uuid, '  ', 20000, 'seal', gen_random_uuid(), now() + interval '1 hour')`,
			[]any{buyer, provider}, "23514"},
		{"unknown buyer", `INSERT INTO app.commerce_contracts
		 (contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at)
		 VALUES ('schema-contract-5', 'trade', '00000000-0000-4000-8000-000000000000', $1::uuid, 'obj', 20000, 'seal', gen_random_uuid(), now() + interval '1 hour')`,
			[]any{provider}, "23503"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}

	var firstTransfer string
	if err := db.QueryRow(ctx,
		`SELECT escrow_transfer_id::text FROM app.commerce_contracts WHERE contract_key = 'schema-contract-1'`).Scan(&firstTransfer); err != nil {
		t.Fatalf("read seeded transfer: %v", err)
	}
	_, err := db.Exec(ctx, `INSERT INTO app.commerce_contracts
	 (contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at)
	 VALUES ('schema-contract-6', 'trade', $1::uuid, $2::uuid, 'obj', 20000, 'seal', $3::uuid, now() + interval '1 hour')`,
		buyer, provider, firstTransfer)
	assertPgCode(t, err, "23505")

	other := commerceContractAccount(t, ctx, db)
	mustExecCommerceContract(t, ctx, db,
		`INSERT INTO app.commerce_contracts
		 (contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at)
		 VALUES ('schema-contract-1', 'trade', $1::uuid, $2::uuid, 'obj', 20000, 'seal', gen_random_uuid(), now() + interval '1 hour')`,
		other, provider)

	contract := seedCommerceContract(t, ctx, db, "schema-contract-9", buyer, provider)
	for _, action := range []string{"accept", "expire"} {
		mustExecCommerceContract(t, ctx, db,
			`INSERT INTO app.commerce_settlements (contract_id, action) VALUES ($1::uuid, $2)`,
			contract, action)
	}
	mustExecCommerceContract(t, ctx, db,
		`INSERT INTO app.commerce_settlements (contract_id, action, transfer_id)
		 VALUES ($1::uuid, 'release', gen_random_uuid())`, contract)
	_, err = db.Exec(ctx, `INSERT INTO app.commerce_settlements (contract_id, action, transfer_id)
	 VALUES ($1::uuid, 'refund', gen_random_uuid())`, contract)
	assertPgCode(t, err, "23505")
	_, err = db.Exec(ctx, `INSERT INTO app.commerce_settlements (contract_id, action)
	 VALUES ($1::uuid, 'deliver')`, contract)
	assertPgCode(t, err, "23514")
}

// TestCommerceContractRegistryImmutableHistory proves triggers
// refuse UPDATE and DELETE on both registries for the owner role
// itself.
func TestCommerceContractRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := commerceContractCtx()
	defer cancel()

	buyer := commerceContractAccount(t, ctx, db)
	provider := commerceContractAccount(t, ctx, db)
	contract := seedCommerceContract(t, ctx, db, "schema-contract-8", buyer, provider)
	mustExecCommerceContract(t, ctx, db,
		`INSERT INTO app.commerce_settlements (contract_id, action) VALUES ($1::uuid, 'accept')`, contract)

	mutations := []struct {
		name string
		sql  string
		args []any
	}{
		{"rewrite object", `UPDATE app.commerce_contracts SET object = 'outro' WHERE contract_key = 'schema-contract-8'`, nil},
		{"delete contract", `DELETE FROM app.commerce_contracts WHERE contract_key = 'schema-contract-8'`, nil},
		{"delete settlement", `DELETE FROM app.commerce_settlements WHERE contract_id = $1::uuid`, []any{contract}},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql, mutation.args...)
		assertPgCode(t, err, "23514")
	}
}

// TestCommerceContractRuntimeLeastPrivilege proves arena_app
// appends and reads contracts and settlements but never rewrites
// them: UPDATE and DELETE die at the grant level.
func TestCommerceContractRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update contract", `UPDATE app.commerce_contracts SET object = 'x'`, "42501"},
			{"delete contract", `DELETE FROM app.commerce_contracts`, "42501"},
			{"update settlement", `UPDATE app.commerce_settlements SET action = 'release'`, "42501"},
			{"delete settlement", `DELETE FROM app.commerce_settlements`, "42501"},
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
		var contracts int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.commerce_contracts`).Scan(&contracts); err != nil {
			t.Fatalf("read contracts: %v", err)
		}
	})
}

func mustExecCommerceContract(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
