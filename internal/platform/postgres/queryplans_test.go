package postgres_test

// P28-T03 — planos e budgets SQL das queries críticas sobre PostgreSQL real.
//
// Cobre o que os guardas de plano existentes ainda não olhavam: leitura de
// saldo e extrato da wallet, profile por conta, documento da Arena e claim
// de job. Cada plano roda como EXPLAIN (ANALYZE, BUFFERS) sobre dataset
// sintético pequeno e representativo, com parâmetros sem PII, e falha em
// sequential scan inesperado, contagem excessiva ou degradação. O baseline
// registra versão do PostgreSQL e estatísticas das tabelas; paginação keyset
// e contenção de lock têm prova própria de N+1 e fail-fast.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbbudget"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// explainQueryPlan runs one EXPLAIN statement and returns its plan lines.
func explainQueryPlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, statement string, args ...any) string {
	t.Helper()
	rows, err := pool.Query(ctx, statement, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan line: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}
	return plan.String()
}

// assertIndexedPlan fails on unexpected sequential scans and requires index
// evidence in the plan. Small tables may legitimately scan, so every call
// site documents why an index is expected at its volume.
func assertIndexedPlan(t *testing.T, name, plan string, markers ...string) {
	t.Helper()
	if strings.Contains(plan, "Seq Scan") {
		t.Fatalf("%s falls back to a sequential scan:\n%s", name, plan)
	}
	assertPlanUses(t, name, plan, markers...)
}

// assertPlanUses requires one exact index path in the plan without
// constraining the join strategy. It guards the selective access a query is
// designed around: dropping that index (or the filter that feeds it) fails,
// while a planner-correct strategy choice at small volume does not.
func assertPlanUses(t *testing.T, name, plan string, markers ...string) {
	t.Helper()
	for _, marker := range markers {
		if strings.Contains(plan, marker) {
			t.Logf("%s plan uses %s", name, marker)
			return
		}
	}
	t.Fatalf("%s plan shows none of %q:\n%s", name, markers, plan)
}

// assertPostgresBaseline pins the engine the plans were measured against and
// logs the table statistics behind them. A plan measured on another major
// version or on empty statistics is not this baseline.
func assertPostgresBaseline(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tables map[string]int64) {
	t.Helper()
	var versionNum string
	if err := pool.QueryRow(ctx, "SHOW server_version_num").Scan(&versionNum); err != nil {
		t.Fatalf("server_version_num: %v", err)
	}
	if !strings.HasPrefix(versionNum, "18") {
		t.Fatalf("server_version_num = %s, want PostgreSQL 18", versionNum)
	}
	var version string
	if err := pool.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatalf("version(): %v", err)
	}
	t.Logf("postgres baseline: %s", version)
	for table, want := range tables {
		var got int64
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s rows = %d, want %d (dataset shape changed)", table, got, want)
		}
		var reltuples float64
		if err := pool.QueryRow(ctx, "SELECT reltuples::float8 FROM pg_class WHERE oid = to_regclass($1)", table).Scan(&reltuples); err != nil {
			t.Fatalf("reltuples %s: %v", table, err)
		}
		t.Logf("baseline stats: %s rows=%d reltuples=%.0f", table, got, reltuples)
	}
}

// assertNoForeignPII fails if any seeded account carries a non-synthetic
// address. EXPLAIN shows placeholders, never values, and the seed below is
// synthetic-only; this guard keeps it that way.
func assertNoForeignPII(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var foreign int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app.accounts WHERE email NOT LIKE '%@arena.example.com'").Scan(&foreign); err != nil {
		t.Fatalf("count foreign emails: %v", err)
	}
	if foreign != 0 {
		t.Fatalf("%d seeded accounts carry non-synthetic email", foreign)
	}
}

// seedPlanVolume creates the probe account and the representative volume.
// Every literal is synthetic; cardinities are returned for the baseline.
func seedPlanVolume(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *platformpg.Queries) pgtype.UUID {
	t.Helper()
	probe := mustCreateAccount(t, ctx, q, "plan-probe@arena.example.com")
	mustCreateProfile(t, ctx, q, probe.ID, "PlanProbe", "planprobe", "pt-BR")
	if err := q.EnsureWalletAccount(ctx, probe.ID); err != nil {
		t.Fatalf("ensure probe wallet: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.accounts (email, status)
		SELECT 'plan-volume-' || g || '@arena.example.com', 'active'
		FROM generate_series(1, 2000) AS g`); err != nil {
		t.Fatalf("seed volume accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.profiles (account_id, username, username_normalized)
		SELECT a.id, 'planuser' || g, 'planuser' || g
		FROM (SELECT id, row_number() OVER () AS g FROM app.accounts WHERE email LIKE 'plan-volume-%') AS a`); err != nil {
		t.Fatalf("seed volume profiles: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.wallet_accounts (account_id)
		SELECT id FROM app.accounts WHERE email LIKE 'plan-volume-%'`); err != nil {
		t.Fatalf("seed volume wallets: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		SELECT $1, 'credit_free', 'plan-op-' || g, 'plan-volume'
		FROM generate_series(1, 200) AS g`, probe.ID); err != nil {
		t.Fatalf("seed probe operations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		SELECT a.id, 'credit_free', 'plan-vol-' || a.n || '-' || g, 'plan-volume'
		FROM (SELECT id, row_number() OVER () AS n FROM app.accounts WHERE email LIKE 'plan-volume-%') AS a
		CROSS JOIN generate_series(1, 3) AS g`); err != nil {
		t.Fatalf("seed volume operations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.wallet_transactions (operation_id, bucket, amount)
		SELECT o.id, 'FREE_INK', 10
		FROM app.wallet_operations o`); err != nil {
		t.Fatalf("seed transactions: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.jobs (type, version, parameters, idempotency_key, state, available_at, lease_owner, leased_until)
		SELECT 'session_cleanup', 1, '{}', 'plan-job-' || g,
		    (ARRAY['queued', 'leased', 'succeeded'])[1 + (g % 3)],
		    now() - (g || ' seconds')::interval,
		    CASE WHEN g % 3 = 1 THEN 'plan-seed' END,
		    CASE WHEN g % 3 = 1 THEN now() + interval '1 hour' END
		FROM generate_series(1, 1500) AS g`); err != nil {
		t.Fatalf("seed jobs volume: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at, version)
		SELECT $1, 'Synthetic plan arena ' || g, 'technology', 'pt-BR', 'published',
		    'plan-volume-' || g, now() - (g || ' minutes')::interval, 2
		FROM generate_series(1, 300) AS g`, probe.ID); err != nil {
		t.Fatalf("seed arenas volume: %v", err)
	}
	for _, table := range []string{"app.accounts", "app.profiles", "app.wallet_accounts", "app.wallet_operations", "app.wallet_transactions", "app.jobs", "app.arenas"} {
		if _, err := pool.Exec(ctx, "ANALYZE "+table); err != nil {
			t.Fatalf("analyze %s: %v", table, err)
		}
	}
	return probe.ID
}

// TestCriticalQueryPlansUseIndexes runs EXPLAIN (ANALYZE, BUFFERS) over the
// critical reads the earlier plan guards did not cover, on representative
// volume with analyzed statistics.
func TestCriticalQueryPlansUseIndexes(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)
	probeID := seedPlanVolume(t, ctx, pool, q)

	assertPostgresBaseline(t, ctx, pool, map[string]int64{
		"app.accounts":            2001,
		"app.profiles":            2001,
		"app.wallet_accounts":     2001,
		"app.wallet_operations":   6200,
		"app.wallet_transactions": 6200,
		"app.jobs":                1500,
		"app.arenas":              300,
	})
	assertNoForeignPII(t, ctx, pool)

	// Wallet balance mirrors GetWalletAccount: primary-key lookup.
	balancePlan := explainQueryPlan(t, ctx, pool, `
		EXPLAIN (ANALYZE, BUFFERS)
		SELECT account_id, balance_free, balance_purchased, created_at, updated_at, free_cycle_anchor_at
		FROM app.wallet_accounts
		WHERE account_id = $1`, probeID)
	assertIndexedPlan(t, "wallet balance", balancePlan, "Index")

	// Statement page mirrors ListWalletStatementPage: keyset join with limit.
	// At this volume the planner correctly hash-joins the fully cached
	// ledger instead of probing it row by row, so the guarded invariant is
	// the selective per-account path — not the join strategy. Dropping
	// wallet_operations_account_id_idx (or the account filter) fails here.
	statementPlan := explainQueryPlan(t, ctx, pool, `
		EXPLAIN (ANALYZE, BUFFERS)
		SELECT t.id, t.operation_id, t.bucket, t.amount, t.created_at, o.operation_type, o.reference
		FROM app.wallet_transactions t
		JOIN app.wallet_operations o ON o.id = t.operation_id
		WHERE o.account_id = $1
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT 20`, probeID)
	assertPlanUses(t, "wallet statement page", statementPlan, "wallet_operations_account_id_idx")

	// Profile mirrors GetProfileByAccountID: primary-key lookup.
	profilePlan := explainQueryPlan(t, ctx, pool, `
		EXPLAIN (ANALYZE, BUFFERS)
		SELECT account_id, username, username_normalized, interface_locale, created_at, updated_at
		FROM app.profiles
		WHERE account_id = $1`, probeID)
	assertIndexedPlan(t, "profile by account", profilePlan, "Index")

	// Arena document mirrors the public read by id.
	var arenaID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM app.arenas WHERE slug = 'plan-volume-1'`).Scan(&arenaID); err != nil {
		t.Fatalf("probe arena: %v", err)
	}
	arenaPlan := explainQueryPlan(t, ctx, pool, `
		EXPLAIN (ANALYZE, BUFFERS)
		SELECT id, creator_id, slug, statement, category, language, status, published_at
		FROM app.arenas
		WHERE id = $1`, arenaID)
	assertIndexedPlan(t, "arena document", arenaPlan, "Index")

	// Job claim mirrors LeaseJob: due ordering with SKIP LOCKED over volume.
	claimPlan := explainQueryPlan(t, ctx, pool, `
		EXPLAIN (ANALYZE, BUFFERS)
		WITH claimable AS (
		    SELECT id
		    FROM app.jobs
		    WHERE (state = 'queued' AND available_at <= now())
		       OR (state = 'leased' AND leased_until <= now())
		    ORDER BY available_at ASC, id ASC
		    FOR UPDATE SKIP LOCKED
		    LIMIT 1
		)
		UPDATE app.jobs AS j
		SET state = 'leased', lease_owner = 'plan-probe', leased_until = now() + interval '30 seconds', attempts = j.attempts + 1
		FROM claimable
		WHERE j.id = claimable.id
		RETURNING j.id`)
	assertIndexedPlan(t, "job claim", claimPlan, "Index")
	if !strings.Contains(claimPlan, "LockRows") {
		t.Fatalf("job claim plan lost its SKIP LOCKED lock node:\n%s", claimPlan)
	}
}

// TestKeysetPaginationWalksWithoutNPlusOne pages a 25-row statement through
// keyset cursors and proves each page costs exactly one query: no per-row
// round trip hides behind the page loop.
func TestKeysetPaginationWalksWithoutNPlusOne(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)

	probe := mustCreateAccount(t, ctx, q, "plan-pages@arena.example.com")
	if err := q.EnsureWalletAccount(ctx, probe.ID); err != nil {
		t.Fatalf("ensure probe wallet: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		SELECT $1, 'credit_free', 'plan-page-' || g, 'plan-pages'
		FROM generate_series(1, 25) AS g`, probe.ID); err != nil {
		t.Fatalf("seed page operations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.wallet_transactions (operation_id, bucket, amount)
		SELECT o.id, 'FREE_INK', 10
		FROM app.wallet_operations o
		WHERE o.account_id = $1`, probe.ID); err != nil {
		t.Fatalf("seed page transactions: %v", err)
	}

	tracker := dbbudget.NewTracker()
	tracked := dbbudget.WithTracker(ctx, tracker)
	seen := make(map[string]bool, 25)
	var afterCreatedAt pgtype.Timestamptz
	var afterID pgtype.UUID
	for page := 0; page < 3; page++ {
		rows, err := q.ListWalletStatementPage(tracked, platformpg.ListWalletStatementPageParams{
			AccountID:      probe.ID,
			AfterCreatedAt: afterCreatedAt,
			AfterID:        afterID,
			PageLimit:      10,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if page < 2 && len(rows) != 10 {
			t.Fatalf("page %d rows = %d, want 10", page, len(rows))
		}
		if page == 2 && len(rows) != 5 {
			t.Fatalf("page %d rows = %d, want 5", page, len(rows))
		}
		for _, row := range rows {
			id := row.ID.String()
			if seen[id] {
				t.Fatalf("row %s returned on two pages (keyset overlap)", id)
			}
			seen[id] = true
		}
		last := rows[len(rows)-1]
		afterCreatedAt = last.CreatedAt
		afterID = last.ID
	}
	if len(seen) != 25 {
		t.Fatalf("walked %d distinct rows, want 25 (keyset skip)", len(seen))
	}
	observation := tracker.Observation()
	if observation.Queries != 3 {
		t.Fatalf("pagination cost %d queries, want exactly 3 (one per page, no N+1)", observation.Queries)
	}
}

// TestRowLockFailsFastUnderContention proves a contested wallet row lock
// fails fast with lock_not_available instead of hanging: contention surfaces
// as an error the caller retries, never as a stuck request.
func TestRowLockFailsFastUnderContention(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)

	probe := mustCreateAccount(t, ctx, q, "plan-lock@arena.example.com")
	if err := q.EnsureWalletAccount(ctx, probe.ID); err != nil {
		t.Fatalf("ensure probe wallet: %v", err)
	}

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT account_id FROM app.wallet_accounts WHERE account_id = $1 FOR UPDATE`, probe.ID); err != nil {
		t.Fatalf("holder lock: %v", err)
	}

	contender, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin contender: %v", err)
	}
	defer func() { _ = contender.Rollback(ctx) }()
	if _, err := contender.Exec(ctx, `SET LOCAL lock_timeout = '200ms'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	_, err = contender.Exec(ctx, `SELECT account_id FROM app.wallet_accounts WHERE account_id = $1 FOR UPDATE`, probe.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("contended lock err = %v, want SQLSTATE 55P03 lock_not_available", err)
	}
	// The refused statement aborts the contender transaction by design; a
	// fresh transaction after the holder releases must lock cleanly.
	if err := contender.Rollback(ctx); err != nil {
		t.Fatalf("rollback contender: %v", err)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	retry, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin retry: %v", err)
	}
	defer func() { _ = retry.Rollback(ctx) }()
	if _, err := retry.Exec(ctx, `SELECT account_id FROM app.wallet_accounts WHERE account_id = $1 FOR UPDATE`, probe.ID); err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := retry.Commit(ctx); err != nil {
		t.Fatalf("commit retry: %v", err)
	}
	var balance int64
	if err := pool.QueryRow(ctx, `SELECT balance_free FROM app.wallet_accounts WHERE account_id = $1`, probe.ID).Scan(&balance); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("balance = %d, want untouched 0", balance)
	}
}
