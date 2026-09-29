package postgres_test

// P33-T07 — dry-run de upgrade e rollback da transição legada.
//
// O roteiro expand/contract (docs/reino/DIREITOS_LEGADOS.md §4, estratégia
// planejada) exige: migrations só aditivas, leitura dupla com o livro
// legado como verdade até o corte, ensaio em cópias sintéticas com
// checksums e app anterior operável na janela, rollback pela estratégia
// documentada (transação por migration + retry, sem down destrutivo) e
// bloqueio em saldo ambíguo/órfão. Nada aqui converte, cunha ou move
// saldo: o ensaio roda sobre PostgreSQL descartável com dados
// inteiramente sintéticos e o produto econômico segue desativado.

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func rehearsalCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 120*time.Second)
}

func rehearsalBaseDSN() string {
	if dsn := os.Getenv("ARENA_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return dbtest.DefaultAdminDSN
}

func openRehearsalSQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		t.Fatalf("open rehearsal connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// legacySnapshot is the rights fingerprint before/after the upgrade:
// counts and sums per legacy surface, all synthetic.
type legacySnapshot struct {
	walletFree       int64
	walletPurchased  int64
	operations       int64
	purchasedSum     int64
	passRemaining    int64
	consumptions     int64
	subscriptions    int64
	economyEntries   int64
	economyCustodies int64
}

func captureLegacy(t *testing.T, ctx context.Context, pool *pgxpool.Pool) legacySnapshot {
	t.Helper()
	var snap legacySnapshot
	queries := []struct {
		sql string
		dst *int64
	}{
		{`SELECT COALESCE(SUM(balance_free), 0) FROM app.wallet_accounts`, &snap.walletFree},
		{`SELECT COALESCE(SUM(balance_purchased), 0) FROM app.wallet_accounts`, &snap.walletPurchased},
		{`SELECT count(*) FROM app.wallet_operations`, &snap.operations},
		{`SELECT COALESCE(SUM(amount), 0) FROM app.wallet_transactions WHERE bucket = 'PURCHASED_INK'`, &snap.purchasedSum},
		{`SELECT COALESCE(SUM(remaining_quantity), 0) FROM app.arena_pass_lots`, &snap.passRemaining},
		{`SELECT count(*) FROM app.arena_pass_consumptions`, &snap.consumptions},
		{`SELECT count(*) FROM app.subscriptions`, &snap.subscriptions},
		{`SELECT count(*) FROM app.economy_entries`, &snap.economyEntries},
		{`SELECT count(*) FROM app.economy_custodies`, &snap.economyCustodies},
	}
	for _, q := range queries {
		if err := pool.QueryRow(ctx, q.sql).Scan(q.dst); err != nil {
			// Pre-economy snapshots have no economy tables yet: zero is
			// the honest reading, not an error to hide.
			if strings.Contains(q.sql, "economy_") {
				*q.dst = 0
				continue
			}
			t.Fatalf("capture %s: %v", q.sql, err)
		}
	}
	return snap
}

func seedRehearsalLegacy(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var acc1, acc2 string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('rehearsal-1@invalid.example', 'active') RETURNING id::text`).Scan(&acc1); err != nil {
		t.Fatalf("seed account 1: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('rehearsal-2@invalid.example', 'active') RETURNING id::text`).Scan(&acc2); err != nil {
		t.Fatalf("seed account 2: %v", err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v (%s)", err, sql)
		}
	}
	exec(`INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased) VALUES ($1::uuid, 6000, 0)`, acc1)
	exec(`INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased) VALUES ($1::uuid, 0, 40000)`, acc2)
	exec(`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference) VALUES ($1::uuid, 'credit_free', 'rehearsal-key-1', 'rehearsal-ref-1')`, acc1)
	exec(`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference) VALUES ($1::uuid, 'credit_purchase', 'rehearsal-key-2', 'rehearsal-ref-2')`, acc2)
	exec(`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference) VALUES ($1::uuid, 'debit_argument', 'rehearsal-key-3', 'rehearsal-ref-3')`, acc1)
	exec(`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) SELECT id, 'FREE_INK', 8000 FROM app.wallet_operations WHERE idempotency_key = 'rehearsal-key-1'`)
	exec(`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) SELECT id, 'PURCHASED_INK', 40000 FROM app.wallet_operations WHERE idempotency_key = 'rehearsal-key-2'`)
	exec(`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) SELECT id, 'FREE_INK', -2000 FROM app.wallet_operations WHERE idempotency_key = 'rehearsal-key-3'`)
	exec(`INSERT INTO app.arena_pass_lots (account_id, origin, quantity, remaining_quantity, reference) VALUES ($1::uuid, 'PURCHASE', 5, 4, 'rehearsal-pass-1')`, acc1)
	exec(`INSERT INTO app.arena_pass_lots (account_id, origin, quantity, remaining_quantity, expires_at, reference) VALUES ($1::uuid, 'MEMBER', 1, 1, now() + interval '30 days', 'rehearsal-pass-2')`, acc2)
	exec(`INSERT INTO app.arena_pass_consumptions (lot_id, arena_id) SELECT id, gen_random_uuid() FROM app.arena_pass_lots WHERE reference = 'rehearsal-pass-1'`)
	exec(`INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode) VALUES ($1::uuid, 'cus_REHEARSAL1', false)`, acc2)
	exec(`INSERT INTO app.subscriptions (account_id, stripe_subscription_id, status, livemode, market, product_id, catalog_version, stripe_price_id) VALUES ($1::uuid, 'sub_REHEARSAL1', 'active', false, 'BR', 'member_monthly', 1, 'price_REHEARSAL1')`, acc2)
}

// TestLegacyTransitionUpgradePreservesRights proves the expand half: a
// synthetic legacy copy at 00032 upgrades to the head with zero loss of
// saldo/pass/contrato, the old application surface keeps writing and
// reading, and neither a silent conversion nor a new Genesis appears.
func TestLegacyTransitionUpgradePreservesRights(t *testing.T) {
	tdb := newTestDB(t, dbtest.WithoutMigrations(), dbtest.WithBaseDSN(rehearsalBaseDSN()))
	ctx, cancel := rehearsalCtx()
	defer cancel()

	versions, err := dbmigrate.Versions()
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	head := versions[len(versions)-1]
	pre := int64(32)

	if applied, err := dbmigrate.UpTo(ctx, openRehearsalSQL(t, tdb.DSN), pre, io.Discard); err != nil {
		t.Fatalf("UpTo(%d): %v", pre, err)
	} else if applied == 0 {
		t.Fatalf("UpTo(%d) applied = 0, want progress", pre)
	}

	pool := tdb.Pool.Pool()
	seedRehearsalLegacy(t, ctx, pool)
	before := captureLegacy(t, ctx, pool)
	if before.walletFree != 6000 || before.walletPurchased != 40000 {
		t.Fatalf("seeded balances = %d/%d, want 6000/40000", before.walletFree, before.walletPurchased)
	}
	if before.passRemaining != 5 || before.consumptions != 1 || before.subscriptions != 1 {
		t.Fatalf("seeded rights = %+v, want 5 passes, 1 consumption, 1 subscription", before)
	}

	if applied, err := dbmigrate.Up(ctx, openRehearsalSQL(t, tdb.DSN), io.Discard); err != nil {
		t.Fatalf("Up to head: %v", err)
	} else if applied == 0 {
		t.Fatalf("Up applied = 0, want the economy expand")
	}
	if version, err := dbmigrate.CurrentVersion(ctx, openRehearsalSQL(t, tdb.DSN)); err != nil || version != head {
		t.Fatalf("CurrentVersion = %d, %v; want head %d", version, err, head)
	}

	after := captureLegacy(t, ctx, pool)
	if after.walletFree != before.walletFree || after.walletPurchased != before.walletPurchased {
		t.Fatalf("balances drifted: before %+v, after %+v", before, after)
	}
	if after.operations != before.operations || after.purchasedSum != before.purchasedSum {
		t.Fatalf("ledger drifted: before %+v, after %+v", before, after)
	}
	if after.passRemaining != before.passRemaining || after.consumptions != before.consumptions {
		t.Fatalf("passes drifted: before %+v, after %+v", before, after)
	}
	if after.subscriptions != before.subscriptions {
		t.Fatalf("contracts drifted: before %+v, after %+v", before, after)
	}
	if after.economyEntries != 0 {
		t.Fatalf("economy entries = %d, want 0 (no silent conversion, no new Genesis)", after.economyEntries)
	}

	// Janela de coexistência: a superfície antiga segue operável no
	// schema novo (tipos legados, leitura de saldo, lote de passe).
	var acc string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('rehearsal-old-app@invalid.example', 'active') RETURNING id::text`).Scan(&acc); err != nil {
		t.Fatalf("old-app account: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased) VALUES ($1::uuid, 100, 200)`, acc); err != nil {
		t.Fatalf("old-app wallet: %v", err)
	}
	var op string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference) VALUES ($1::uuid, 'credit_purchase', 'rehearsal-old-key', 'rehearsal-old-ref') RETURNING id::text`,
		acc).Scan(&op); err != nil {
		t.Fatalf("old-app operation: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) VALUES ($1::uuid, 'PURCHASED_INK', 200)`, op); err != nil {
		t.Fatalf("old-app leg: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.arena_pass_lots (account_id, origin, quantity, remaining_quantity, reference) VALUES ($1::uuid, 'PURCHASE', 2, 2, 'rehearsal-old-pass')`, acc); err != nil {
		t.Fatalf("old-app pass: %v", err)
	}
}

// TestLegacyTransitionFailureRestoresWithoutDestructiveDown proves the
// contract half: qualquer falha restaura pela estratégia documentada
// (uma transação por migration, retry converge, sem down). Uma escrita
// interrompida some por rollback, a versão não avança parcial e o banco
// segue rolável ao head; o runner expõe só Up/UpTo, nunca Down.
func TestLegacyTransitionFailureRestoresWithoutDestructiveDown(t *testing.T) {
	tdb := newTestDB(t, dbtest.WithoutMigrations(), dbtest.WithBaseDSN(rehearsalBaseDSN()))
	ctx, cancel := rehearsalCtx()
	defer cancel()

	versions, err := dbmigrate.Versions()
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	head := versions[len(versions)-1]
	if _, err := dbmigrate.Up(ctx, openRehearsalSQL(t, tdb.DSN), io.Discard); err != nil {
		t.Fatalf("Up to head: %v", err)
	}

	pool := tdb.Pool.Pool()
	before := captureLegacy(t, ctx, pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var acc string
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('rehearsal-torn@invalid.example', 'active') RETURNING id::text`).Scan(&acc); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("torn account: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased) VALUES ($1::uuid, 1, 1)`, acc); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("torn wallet: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var torn int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM app.accounts WHERE email = 'rehearsal-torn@invalid.example'`).Scan(&torn); err != nil {
		t.Fatalf("probe torn: %v", err)
	}
	if torn != 0 {
		t.Fatalf("torn write survived rollback: %d rows", torn)
	}
	if after := captureLegacy(t, ctx, pool); after != before {
		t.Fatalf("checksums drifted on torn write: before %+v, after %+v", before, after)
	}
	if version, err := dbmigrate.CurrentVersion(ctx, openRehearsalSQL(t, tdb.DSN)); err != nil || version != head {
		t.Fatalf("CurrentVersion = %d, %v; want head %d after torn write", version, err, head)
	}
	if applied, err := dbmigrate.Up(ctx, openRehearsalSQL(t, tdb.DSN), io.Discard); err != nil || applied != 0 {
		t.Fatalf("Up after torn write = (%d, %v), want (0, nil)", applied, err)
	}

	assertNoDestructiveDown(t)
}

// assertNoDestructiveDown proves the runner never offers a destructive
// way back: dbmigrate.go defines Up/UpTo but no Down, so a volta é o
// processo (rollback + retry + restore da cópia), nunca um down que
// apague Dado legado.
func assertNoDestructiveDown(t *testing.T) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
	raw, err := os.ReadFile(filepath.Join(root, "internal", "platform", "dbmigrate", "dbmigrate.go"))
	if err != nil {
		t.Fatalf("read runner: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func Down(") || strings.HasPrefix(trimmed, "func (") && strings.Contains(trimmed, "Down(") {
			t.Fatalf("runner offers %q: volta destrutiva proibida", trimmed)
		}
	}
}
