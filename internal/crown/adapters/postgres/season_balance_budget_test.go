package postgres_test

// P47-T10 — budgets medidos do caminho da sucessão, test-only.
//
// Mede o custo da consulta top-1 pelo índice, o custo do update
// incremental por outbox, o atraso da outbox (revisões commitadas
// menos avaliadas ao fim da drenagem) e a contenção de workers, e
// fixa tetos de regressão generosos: eles pegam degradação
// patológica, nunca micro-otimização. Os valores medidos estão em
// docs/quality/SEASON_BALANCE.md; os tetos aqui não são parâmetros
// ratificados, e o lançamento exige budgets ratificados antes.
//
// Comandos:
//   go test ./internal/crown/adapters/postgres -run TestCrownBudget -race -count=1 -v

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	crownadapter "github.com/AlexandreZanata/Regnovum/internal/crown/adapters/postgres"
	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

// budgetTopAccounts is the synthetic leader population: large enough
// to exercise the ranking index, never a claim about millions.
const budgetTopAccounts = 200

// budgetRevisions is the synthetic outbox depth drained in one run.
const budgetRevisions = 20

func seedBudgetSeason(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seasonKey string, accounts int) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	mustExec(t, ctx, tx, `
		INSERT INTO app.seasons
		(season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		VALUES ($1, 1, '2026-04-01T00:00:00Z', '2026-04-01T00:00:00Z'::timestamptz + make_interval(secs => 7776000), 'v1', 'wealth-v1', 'rainha-1', 'regente-1', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')
		ON CONFLICT DO NOTHING
	`, seasonKey)
	adapter := crownadapter.NewWealthProjectionAdapter()
	for i := 0; i < accounts; i++ {
		if _, err := adapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
			SeasonID:        seasonKey,
			SubjectID:       fmt.Sprintf("budget-user-%d", i),
			BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
			EventKind:       "transfer",
			DeltaAssets:     int64((i + 1) * 1000),
		}); err != nil {
			t.Fatalf("seed wealth event: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit budget seed: %v", err)
	}
}

// TestCrownBudgetTopIndexCost measures the top-1 leader query over a
// synthetic population and pins a regression ceiling far above the
// measured cost.
func TestCrownBudgetTopIndexCost(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-budget-top-1"
	pool := db.Pool.Pool()
	seedBudgetSeason(t, ctx, pool, seasonKey, budgetTopAccounts)

	adapter := crownadapter.NewWealthProjectionAdapter()
	const reads = 50
	start := time.Now()
	for i := 0; i < reads; i++ {
		leader, found, err := adapter.GetTopLeader(ctx, pool, seasonKey)
		if err != nil || !found {
			t.Fatalf("get top leader: found=%v err=%v", found, err)
		}
		if leader.Wealth != int64(budgetTopAccounts*1000) {
			t.Fatalf("leader wealth = %d, want %d", leader.Wealth, budgetTopAccounts*1000)
		}
	}
	elapsed := time.Since(start)
	average := elapsed / reads
	t.Logf("top-1 over %d accounts: %d reads in %v (avg %v per read)", budgetTopAccounts, reads, elapsed, average)
	if average > 500*time.Millisecond {
		t.Fatalf("top-1 average %v above the 500ms regression budget", average)
	}
}

// TestCrownBudgetIncrementalUpdateCost measures sequential
// incremental outbox commits and pins a regression ceiling.
func TestCrownBudgetIncrementalUpdateCost(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-budget-incr-1"
	pool := db.Pool.Pool()
	seedBudgetSeason(t, ctx, pool, seasonKey, 10)

	adapter := crownadapter.NewWealthProjectionAdapter()
	const writes = 100
	start := time.Now()
	for i := 0; i < writes; i++ {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := adapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
			SeasonID:        seasonKey,
			SubjectID:       fmt.Sprintf("budget-writer-%d", i%10),
			BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
			EventKind:       "transfer",
			DeltaAssets:     10,
		}); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("record outbox: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	elapsed := time.Since(start)
	average := elapsed / writes
	t.Logf("incremental updates: %d commits in %v (avg %v per commit)", writes, elapsed, average)
	if average > 500*time.Millisecond {
		t.Fatalf("incremental average %v above the 500ms regression budget", average)
	}
}

// TestCrownBudgetOutboxDrainLag records a synthetic outbox depth,
// drains it through the evaluator and proves the lag returns to
// zero: every committed revision is evaluated exactly once.
func TestCrownBudgetOutboxDrainLag(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-budget-drain-1"
	pool := db.Pool.Pool()
	seedBudgetSeason(t, ctx, pool, seasonKey, 5)

	wealthAdapter := crownadapter.NewWealthProjectionAdapter()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, "rainha-1", 1); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("init evaluator: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit init: %v", err)
	}

	for i := 0; i < budgetRevisions; i++ {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := wealthAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
			SeasonID:        seasonKey,
			SubjectID:       fmt.Sprintf("budget-drain-%d", i%5),
			BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
			EventKind:       "transfer",
			DeltaAssets:     1000,
		}); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("record outbox: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	acquired, err := evalAdapter.AcquireLease(ctx, pool, seasonKey, "budget-worker", 30*time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: %v %v", acquired, err)
	}
	start := time.Now()
	evaluated := 0
	for {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin eval: %v", err)
		}
		_, hasWork, err := evalAdapter.EvaluateNextRevisionTx(ctx, tx, seasonKey, "budget-worker")
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("evaluate: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit eval: %v", err)
		}
		if !hasWork {
			break
		}
		evaluated++
		if evaluated > budgetRevisions+5 {
			t.Fatalf("drain did not converge after %d evaluations", evaluated)
		}
	}
	elapsed := time.Since(start)
	var committed, done int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(c.last_revision, 0), COALESCE(e.last_evaluated_revision, 0)
		FROM app.seasonal_wealth_checkpoints c
		LEFT JOIN app.seasonal_succession_evaluators e ON e.season_id = c.season_id
		WHERE c.season_id = $1`, seasonKey).Scan(&committed, &done); err != nil {
		t.Fatalf("read lag: %v", err)
	}
	lag := committed - done
	t.Logf("outbox drain: %d revisions evaluated in %v, lag after drain = %d", evaluated, elapsed, lag)
	if lag != 0 {
		t.Fatalf("outbox lag = %d after drain, want 0", lag)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("drain of %d revisions took %v, above the 30s regression budget", budgetRevisions, elapsed)
	}
}

// TestCrownBudgetLeaseContention races four workers on one lease:
// exactly one wins and the book evaluates exactly once per
// revision, so contention serializes instead of duplicating.
func TestCrownBudgetLeaseContention(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-budget-lease-1"
	pool := db.Pool.Pool()
	seedBudgetSeason(t, ctx, pool, seasonKey, 3)

	wealthAdapter := crownadapter.NewWealthProjectionAdapter()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, "rainha-1", 1); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("init evaluator: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit init: %v", err)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := wealthAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID: seasonKey, SubjectID: "budget-racer",
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer", DeltaAssets: 5000,
	}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("record: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	const workers = 4
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			wID := fmt.Sprintf("budget-racer-%d", n)
			acquired, err := evalAdapter.AcquireLease(ctx, pool, seasonKey, wID, 30*time.Second)
			if err != nil || !acquired {
				if err == nil {
					err = crowndomain.ErrWorkerLeaseBusy
				}
				results <- err
				return
			}
			wTx, err := pool.Begin(ctx)
			if err != nil {
				results <- err
				return
			}
			defer wTx.Rollback(ctx)
			_, _, err = evalAdapter.EvaluateNextRevisionTx(ctx, wTx, seasonKey, wID)
			if err != nil {
				results <- err
				return
			}
			results <- wTx.Commit(ctx)
		}(i)
	}
	wg.Wait()
	close(results)

	wins, busy := 0, 0
	for res := range results {
		switch {
		case res == nil:
			wins++
		case isBudgetLeaseBusy(res):
			busy++
		default:
			t.Fatalf("unexpected contention error: %v", res)
		}
	}
	t.Logf("lease contention: %d workers, %d wins, %d stood down", workers, wins, busy)
	if wins != 1 || busy != workers-1 {
		t.Fatalf("wins=%d busy=%d, want exactly 1 winner and %d standing down", wins, busy, workers-1)
	}
}

func isBudgetLeaseBusy(err error) bool {
	for err != nil {
		if err == crowndomain.ErrWorkerLeaseBusy {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
