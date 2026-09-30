package postgres_test

// P32-T02 — o schema de custódia da economia sobre PostgreSQL 18 real.
//
// A migration 00033 cria app.economy_custodies, app.economy_partitions e
// app.economy_entries: dupla entrada por transfer_id, soma em milliINK
// bigint, UNIQUE contra custódia duplicada e triggers que recusam UPDATE e
// DELETE para qualquer papel. Os testes abaixo provam sobre banco
// descartável: escrita parcial revertida, história imutável, unicidade,
// faixa de valor, privilégio mínimo do arena_app e upgrade que preserva o
// ledger legado. Um único milliINK divergente bloquearia.

import (
	"context"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
)

func economyCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func mustExecEconomy(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func mustCreateCustody(t *testing.T, ctx context.Context, db *dbtest.TestDB, kind, label string) string {
	t.Helper()
	var id string
	err := db.QueryRow(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ($1, $2) RETURNING id::text`,
		kind, label).Scan(&id)
	if err != nil {
		t.Fatalf("create custody %s/%s: %v", kind, label, err)
	}
	return id
}

func mustCreatePartition(t *testing.T, ctx context.Context, db *dbtest.TestDB, custodyID, name string) string {
	t.Helper()
	var id string
	err := db.QueryRow(ctx,
		`INSERT INTO app.economy_partitions (custody_id, name) VALUES ($1, $2) RETURNING id::text`,
		custodyID, name).Scan(&id)
	if err != nil {
		t.Fatalf("create partition %s/%s: %v", custodyID, name, err)
	}
	return id
}

func countEntries(t *testing.T, ctx context.Context, db *dbtest.TestDB, transferID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_entries WHERE transfer_id = $1::uuid`,
		transferID).Scan(&count); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	return count
}

// TestEconomyCustodyRejectsPartialWrite proves a transfer is all or
// nothing at the schema level: the second leg references a custody that
// does not exist, the statement fails with a foreign-key violation and
// the rolled-back transaction leaves zero legs behind.
func TestEconomyCustodyRejectsPartialWrite(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := economyCtx()
	defer cancel()

	treasury := mustCreateCustody(t, ctx, db, "treasury", "partial-treasury")
	mustCreatePartition(t, ctx, db, treasury, "available")

	transfer := "11111111-1111-7111-8111-111111111111"
	tx, err := db.Pool.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1, $2, 'debit', 1000)`, transfer, treasury); err != nil {
		t.Fatalf("first leg: %v", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1, '00000000-0000-7000-8000-000000000000', 'credit', 1000)`, transfer)
	assertPgCode(t, err, "23503")
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := countEntries(t, ctx, db, transfer); got != 0 {
		t.Fatalf("entries for aborted transfer = %d, want 0", got)
	}
}

// TestEconomyCustodyImmutableHistory proves triggers refuse UPDATE and
// DELETE on custodies, partitions and entries for the owner role itself:
// the ledger is append-only below the grant level.
func TestEconomyCustodyImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := economyCtx()
	defer cancel()

	treasury := mustCreateCustody(t, ctx, db, "treasury", "frozen-treasury")
	partition := mustCreatePartition(t, ctx, db, treasury, "available")
	holder := mustCreateCustody(t, ctx, db, "user", "frozen-holder")
	transfer := "22222222-2222-7222-8222-222222222222"
	mustExecEconomy(t, ctx, db,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1, $2, 'debit', 1000)`, transfer, treasury)
	mustExecEconomy(t, ctx, db,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1, $2, 'credit', 1000)`, transfer, holder)

	mutations := []struct {
		name string
		sql  string
		args []any
	}{
		{"update entry amount", `UPDATE app.economy_entries SET amount_milli = 999 WHERE transfer_id = $1::uuid`, []any{transfer}},
		{"delete entry", `DELETE FROM app.economy_entries WHERE transfer_id = $1::uuid`, []any{transfer}},
		{"rename custody", `UPDATE app.economy_custodies SET label = 'renamed' WHERE id = $1::uuid`, []any{treasury}},
		{"delete custody", `DELETE FROM app.economy_custodies WHERE id = $1::uuid`, []any{holder}},
		{"delete partition", `DELETE FROM app.economy_partitions WHERE id = $1::uuid`, []any{partition}},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql, mutation.args...)
		assertPgCode(t, err, "23514")
	}
}

// TestEconomyCustodyUniqueness proves one home per unit: duplicate
// custodies, duplicate partitions and a reused transfer leg are all
// refused with unique violations.
func TestEconomyCustodyUniqueness(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := economyCtx()
	defer cancel()

	mustCreateCustody(t, ctx, db, "escrow", "deal-7")
	_, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('escrow', 'deal-7')`)
	assertPgCode(t, err, "23505")

	treasury := mustCreateCustody(t, ctx, db, "treasury", "unique-treasury")
	mustCreatePartition(t, ctx, db, treasury, "available")
	_, err = db.Exec(ctx,
		`INSERT INTO app.economy_partitions (custody_id, name) VALUES ($1, 'available')`, treasury)
	assertPgCode(t, err, "23505")

	transfer := "33333333-3333-7333-8333-333333333333"
	mustExecEconomy(t, ctx, db,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1, $2, 'debit', 100)`, transfer, treasury)
	_, err = db.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1, $2, 'debit', 100)`, transfer, treasury)
	assertPgCode(t, err, "23505")
}

// TestEconomyEntryAmountRange proves legs carry 1..S milliINK: zero,
// negative and beyond-supply amounts fail the range check, while the
// edges 1 and S land.
func TestEconomyEntryAmountRange(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := economyCtx()
	defer cancel()

	treasury := mustCreateCustody(t, ctx, db, "treasury", "range-treasury")
	for _, amount := range []int64{0, -5, 2100000000001} {
		_, err := db.Exec(ctx,
			`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
			 VALUES (gen_random_uuid(), $1, 'debit', $2)`, treasury, amount)
		assertPgCode(t, err, "23514")
	}
	for _, amount := range []int64{1, 2100000000000} {
		mustExecEconomy(t, ctx, db,
			`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
			 VALUES (gen_random_uuid(), $1, 'debit', $2)`, treasury, amount)
	}
}

// TestEconomyRuntimeLeastPrivilege proves arena_app holds the minimum:
// it can read and append, but UPDATE and DELETE are denied at the grant
// level before any trigger could even fire.
func TestEconomyRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		var custody string
		if err := tx.QueryRow(ctx,
			`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', 'app-holder') RETURNING id::text`,
		).Scan(&custody); err != nil {
			t.Fatalf("runtime insert custody: %v", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.economy_partitions (custody_id, name) VALUES ($1, 'available')`, custody); err != nil {
			t.Fatalf("runtime insert partition: %v", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
			 VALUES (gen_random_uuid(), $1, 'debit', 10)`, custody); err != nil {
			t.Fatalf("runtime insert entry: %v", err)
		}
		denied := []struct {
			name string
			sql  string
		}{
			{"update entry", `UPDATE app.economy_entries SET amount_milli = 11 WHERE custody_id = $1::uuid`},
			{"delete entry", `DELETE FROM app.economy_entries WHERE custody_id = $1::uuid`},
			{"update custody", `UPDATE app.economy_custodies SET label = 'renamed' WHERE id = $1::uuid`},
		}
		for _, probe := range denied {
			if _, err := tx.Exec(ctx, `SAVEPOINT probe`); err != nil {
				t.Fatalf("savepoint: %v", err)
			}
			_, err := tx.Exec(ctx, probe.sql, custody)
			assertPgCode(t, err, "42501")
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT probe`); err != nil {
				t.Fatalf("rollback to savepoint: %v", err)
			}
		}
	})
}

// TestEconomyUpgradePreservesLegacyLedger proves migration 00033 lands on
// a history it does not disturb: the wallet tables accept new rows and
// their checks still bite, and the new tables exist beside them.
func TestEconomyUpgradePreservesLegacyLedger(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := economyCtx()
	defer cancel()

	for _, table := range []string{"app.wallet_accounts", "app.wallet_operations", "app.wallet_transactions", "app.economy_entries"} {
		var present bool
		if err := db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'app' AND c.relname = $1)`,
			table[len("app."):]).Scan(&present); err != nil {
			t.Fatalf("probe %s: %v", table, err)
		}
		if !present {
			t.Fatalf("table %s is missing after upgrade", table)
		}
	}

	q := postgres.New(db.Pool)
	acc := mustCreateAccount(t, ctx, q, "economy-upgrade@arena.example.com")
	createWallet(t, ctx, q, acc.ID)
	operation, err := q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      acc.ID,
		OperationType:  "credit_purchase",
		IdempotencyKey: "economy-upgrade-probe",
		Reference:      "economy-upgrade",
	})
	if err != nil {
		t.Fatalf("create legacy operation: %v", err)
	}
	_, err = db.Exec(ctx,
		`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) VALUES ($1, 'FREE_INK', 0)`,
		operation.ID)
	assertPgCode(t, err, "23514")
}
