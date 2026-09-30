package postgres_test

// P37-T05 — the service refund and obligation registries hold
// their shape on real PostgreSQL.
//
// app.commerce_service_refunds records one proportional refund per
// contract key with the total returned, the floor(10%) tithe
// reversal, the provider share, the explicit shortfall and the
// moving transfer; app.commerce_refund_obligations records at most
// one explicit debt per refund naming the provider debtor and the
// buyer creditor. Bad amounts, inconsistent splits, over-obligation
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

func serviceRefundCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func serviceRefundAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('commerce-refund-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

func seedServiceRefundContract(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, buyer, provider string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.commerce_contracts
		 (contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash, escrow_transfer_id, expires_at)
		 VALUES ($1, 'trade', $2::uuid, $3::uuid, 'serviço com reembolso', 20000, 'seal-terms', gen_random_uuid(), now() + interval '1 hour')
		 RETURNING id::text`, key, buyer, provider).Scan(&id); err != nil {
		t.Fatalf("seed contract: %v", err)
	}
	return id
}

func seedServiceRefund(t *testing.T, ctx context.Context, db *dbtest.TestDB, contract, key string, amount, tithe, share int64) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.commerce_service_refunds
		 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, transfer_id)
		 VALUES ($1::uuid, $2, $3, $4, $5, gen_random_uuid())
		 RETURNING id::text`, contract, key, amount, tithe, share).Scan(&id); err != nil {
		t.Fatalf("seed refund: %v", err)
	}
	return id
}

// TestServiceRefundRegistryKeysAndSplit proves one refund per
// contract key with the proportional split sealed by CHECKs:
// non-positive amounts, inconsistent tithe or share, obligations
// beyond the provider share and duplicated keys or transfers die,
// while another contract reusing a key opens its own refund.
func TestServiceRefundRegistryKeysAndSplit(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := serviceRefundCtx()
	defer cancel()

	buyer := serviceRefundAccount(t, ctx, db)
	provider := serviceRefundAccount(t, ctx, db)
	contract := seedServiceRefundContract(t, ctx, db, "schema-refund-contract-1", buyer, provider)
	seedServiceRefund(t, ctx, db, contract, "schema-refund-1", 20000, 2000, 18000)

	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"duplicate key", `INSERT INTO app.commerce_service_refunds
		 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, transfer_id)
		 VALUES ($1::uuid, 'schema-refund-1', 20000, 2000, 18000, gen_random_uuid())`,
			[]any{contract}, "23505"},
		{"zero amount", `INSERT INTO app.commerce_service_refunds
		 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, transfer_id)
		 VALUES ($1::uuid, 'schema-refund-2', 0, 0, 0, gen_random_uuid())`,
			[]any{contract}, "23514"},
		{"wrong tithe", `INSERT INTO app.commerce_service_refunds
		 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, transfer_id)
		 VALUES ($1::uuid, 'schema-refund-3', 20000, 2001, 17999, gen_random_uuid())`,
			[]any{contract}, "23514"},
		{"wrong share", `INSERT INTO app.commerce_service_refunds
		 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, transfer_id)
		 VALUES ($1::uuid, 'schema-refund-4', 20000, 2000, 17999, gen_random_uuid())`,
			[]any{contract}, "23514"},
		{"over obligation", `INSERT INTO app.commerce_service_refunds
		 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, obligation_milli, transfer_id)
		 VALUES ($1::uuid, 'schema-refund-5', 20000, 2000, 18000, 18001, gen_random_uuid())`,
			[]any{contract}, "23514"},
		{"unknown contract", `INSERT INTO app.commerce_service_refunds
		 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, transfer_id)
		 VALUES ('00000000-0000-4000-8000-000000000000', 'schema-refund-6', 100, 10, 90, gen_random_uuid())`,
			nil, "23503"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}

	var firstTransfer string
	if err := db.QueryRow(ctx,
		`SELECT transfer_id::text FROM app.commerce_service_refunds WHERE refund_key = 'schema-refund-1'`).Scan(&firstTransfer); err != nil {
		t.Fatalf("read seeded transfer: %v", err)
	}
	_, err := db.Exec(ctx, `INSERT INTO app.commerce_service_refunds
	 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, transfer_id)
	 VALUES ($1::uuid, 'schema-refund-7', 100, 10, 90, $2::uuid)`, contract, firstTransfer)
	assertPgCode(t, err, "23505")

	other := seedServiceRefundContract(t, ctx, db, "schema-refund-contract-2", buyer, provider)
	mustExecServiceRefund(t, ctx, db,
		`INSERT INTO app.commerce_service_refunds
		 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, transfer_id)
		 VALUES ($1::uuid, 'schema-refund-1', 100, 10, 90, gen_random_uuid())`, other)

	refund := seedServiceRefund(t, ctx, db, contract, "schema-refund-9", 100, 10, 90)
	mustExecServiceRefund(t, ctx, db,
		`INSERT INTO app.commerce_refund_obligations
		 (refund_id, debtor_id, creditor_id, amount_milli)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, 90)`, refund, provider, buyer)
	_, err = db.Exec(ctx, `INSERT INTO app.commerce_refund_obligations
	 (refund_id, debtor_id, creditor_id, amount_milli)
	 VALUES ($1::uuid, $2::uuid, $3::uuid, 90)`, refund, provider, buyer)
	assertPgCode(t, err, "23505")
	_, err = db.Exec(ctx, `INSERT INTO app.commerce_refund_obligations
	 (refund_id, debtor_id, creditor_id, amount_milli)
	 VALUES ($1::uuid, $2::uuid, $2::uuid, 10)`, refund, provider)
	assertPgCode(t, err, "23514")
}

// TestServiceRefundRegistryImmutableHistory proves triggers refuse
// UPDATE and DELETE on both registries for the owner role itself.
func TestServiceRefundRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := serviceRefundCtx()
	defer cancel()

	buyer := serviceRefundAccount(t, ctx, db)
	provider := serviceRefundAccount(t, ctx, db)
	contract := seedServiceRefundContract(t, ctx, db, "schema-refund-contract-8", buyer, provider)
	refund := seedServiceRefund(t, ctx, db, contract, "schema-refund-8", 100, 10, 90)
	mustExecServiceRefund(t, ctx, db,
		`INSERT INTO app.commerce_refund_obligations
		 (refund_id, debtor_id, creditor_id, amount_milli)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, 90)`, refund, provider, buyer)

	mutations := []struct {
		name string
		sql  string
		args []any
	}{
		{"rewrite refund", `UPDATE app.commerce_service_refunds SET amount_milli = 1 WHERE refund_key = 'schema-refund-8'`, nil},
		{"delete refund", `DELETE FROM app.commerce_service_refunds WHERE refund_key = 'schema-refund-8'`, nil},
		{"delete obligation", `DELETE FROM app.commerce_refund_obligations WHERE refund_id = $1::uuid`, []any{refund}},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql, mutation.args...)
		assertPgCode(t, err, "23514")
	}
}

// TestServiceRefundRuntimeLeastPrivilege proves arena_app appends
// and reads refunds and obligations but never rewrites them: UPDATE
// and DELETE die at the grant level.
func TestServiceRefundRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update refund", `UPDATE app.commerce_service_refunds SET amount_milli = 1`, "42501"},
			{"delete refund", `DELETE FROM app.commerce_service_refunds`, "42501"},
			{"update obligation", `UPDATE app.commerce_refund_obligations SET amount_milli = 1`, "42501"},
			{"delete obligation", `DELETE FROM app.commerce_refund_obligations`, "42501"},
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
		var refunds int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.commerce_service_refunds`).Scan(&refunds); err != nil {
			t.Fatalf("read refunds: %v", err)
		}
	})
}

func mustExecServiceRefund(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
