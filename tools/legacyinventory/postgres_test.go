package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// TestAuditMatchesSyntheticLedger seeds a disposable database with
// synthetic rights only, then proves quantity and sum match the legacy
// ledger exactly, with zero findings and zero PII in the report.
func TestAuditMatchesSyntheticLedger(t *testing.T) {
	testDB := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := testDB.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	var acc1, acc2 string
	if err := testDB.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('synthetic-holder-1@invalid.example', 'active') RETURNING id::text`).Scan(&acc1); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := testDB.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('synthetic-holder-2@invalid.example', 'active') RETURNING id::text`).Scan(&acc2); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	exec(`INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased) VALUES ($1::uuid, 6000, 0)`, acc1)
	exec(`INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased) VALUES ($1::uuid, 0, 40000)`, acc2)
	exec(`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference) VALUES ($1::uuid, 'credit_free', 'synthetic-key-1', 'synthetic-ref-1')`, acc1)
	exec(`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference) VALUES ($1::uuid, 'credit_purchase', 'synthetic-key-2', 'synthetic-ref-2')`, acc2)
	exec(`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference) VALUES ($1::uuid, 'debit_argument', 'synthetic-key-3', 'synthetic-ref-3')`, acc1)
	exec(`INSERT INTO app.wallet_transactions (operation_id, bucket, amount)
		SELECT id, 'FREE_INK', 8000 FROM app.wallet_operations WHERE idempotency_key = 'synthetic-key-1'`)
	exec(`INSERT INTO app.wallet_transactions (operation_id, bucket, amount)
		SELECT id, 'PURCHASED_INK', 40000 FROM app.wallet_operations WHERE idempotency_key = 'synthetic-key-2'`)
	exec(`INSERT INTO app.wallet_transactions (operation_id, bucket, amount)
		SELECT id, 'FREE_INK', -2000 FROM app.wallet_operations WHERE idempotency_key = 'synthetic-key-3'`)
	exec(`INSERT INTO app.arena_pass_lots (account_id, origin, quantity, remaining_quantity, reference) VALUES ($1::uuid, 'PURCHASE', 5, 4, 'synthetic-pass-1')`, acc1)
	exec(`INSERT INTO app.arena_pass_lots (account_id, origin, quantity, remaining_quantity, expires_at, reference) VALUES ($1::uuid, 'MEMBER', 1, 1, now() + interval '30 days', 'synthetic-pass-2')`, acc2)
	exec(`INSERT INTO app.arena_pass_consumptions (lot_id, arena_id)
		SELECT id, gen_random_uuid() FROM app.arena_pass_lots WHERE reference = 'synthetic-pass-1'`)
	exec(`INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode) VALUES ($1::uuid, 'cus_SYNTHETIC1', false)`, acc2)
	exec(`INSERT INTO app.subscriptions (account_id, stripe_subscription_id, status, livemode, market, product_id, catalog_version, stripe_price_id)
		VALUES ($1::uuid, 'sub_SYNTHETIC1', 'active', false, 'BR', 'member_monthly', 1, 'price_SYNTHETIC1')`, acc2)

	report, err := Audit(ctx, testDB.Pool.Pool())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("synthetic findings = %v", report.Findings)
	}
	byContract := map[string]int64{}
	for _, contract := range report.Contracts {
		byContract[contract.Contract+"/"+contract.Origin] = contract.Total
	}
	for contract, want := range map[string]int64{
		"franchise/plan": 6000, "purchased-ink/stripe": 40000,
		"arena-pass/purchase": 4, "arena-pass/member": 1, "member-subscription/stripe": 1,
	} {
		if byContract[contract] != want {
			t.Errorf("contract %s total = %d, want %d (contracts: %+v)", contract, byContract[contract], want, report.Contracts)
		}
	}

	encoded, err := Render(report)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, forbidden := range []string{"@", "invalid.example", "cus_", "sub_SYNTHETIC", "synthetic-holder"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("report contains %q: synthetic PII leaked into the encoding", forbidden)
		}
	}
}

// TestCLIExitCodes pins the command contract on a disposable database:
// clean books hold, unreachable databases fail closed, usage errors
// name themselves.
func TestCLIExitCodes(t *testing.T) {
	testDB := dbtest.New(t)

	var okOut strings.Builder
	if code := run([]string{"-dsn", testDB.DSN}, &okOut, io.Discard); code != exitOK {
		t.Errorf("clean exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(okOut.String(), "franchise") {
		t.Errorf("clean output names no contract")
	}
	if code := run([]string{"-dsn", "postgres://arena:arena-local-dev@127.0.0.1:1/arena?sslmode=disable"}, io.Discard, io.Discard); code != exitViolation {
		t.Errorf("unreachable exit = %d, want %d", code, exitViolation)
	}
	if code := run([]string{"-extra"}, io.Discard, io.Discard); code != exitUsage {
		t.Errorf("usage exit = %d, want %d", code, exitUsage)
	}
}
