package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	crownadapter "github.com/AlexandreZanata/Regnovum/internal/crown/adapters/postgres"
	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

func TestSuccessionEvaluatorTwoCandidatesAndDeterminism(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-sucessao-1"
	anaID, bobID, _ := setupSeasonAndAccounts(t, ctx, db, seasonKey)

	pool := db.Pool.Pool()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)
	wealthAdapter := crownadapter.NewWealthProjectionAdapter()

	// Initial monarch is rainha-1, initialized at revision 1 (C = 5000):
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	if err := evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, "rainha-1", 1); err != nil {
		t.Fatalf("init evaluator: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit init: %v", err)
	}

	// 1. Ana earns 6000 milli (> C=5000) in event revision 2:
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)
	rev2, err := wealthAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       anaID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     6000000000,
	})
	if err != nil {
		t.Fatalf("record ana wealth: %v", err)
	}
	if rev2 != 2 {
		t.Fatalf("expected revision 2, got %d", rev2)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit rev2: %v", err)
	}

	// Worker acquires lease:
	acquired, err := evalAdapter.AcquireLease(ctx, pool, seasonKey, "worker-1", 10*time.Second)
	if err != nil || !acquired {
		t.Fatalf("expected worker-1 to acquire lease, got %v, err: %v", acquired, err)
	}

	// Evaluate revision 2 (Ana should conquer from rainha-1):
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	res2, hasWork, err := evalAdapter.EvaluateNextRevisionTx(ctx, tx, seasonKey, "worker-1")
	if err != nil {
		t.Fatalf("evaluate rev 2: %v", err)
	}
	if !hasWork {
		t.Fatalf("expected work on rev 2")
	}
	if res2.HolderSubject != anaID {
		t.Fatalf("expected ana to become holder, got %s", res2.HolderSubject)
	}
	if res2.ReignVersion != 2 {
		t.Fatalf("expected reign version 2, got %d", res2.ReignVersion)
	}
	if res2.Predecessor != "rainha-1" {
		t.Fatalf("expected predecessor rainha-1, got %s", res2.Predecessor)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit transition rev 2: %v", err)
	}

	// 2. Bob earns 8000000000 milli (> Ana=6000000000) in event revision 3:
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)
	rev3, err := wealthAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       bobID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     8000000000,
	})
	if err != nil {
		t.Fatalf("record bob wealth: %v", err)
	}
	if rev3 != 3 {
		t.Fatalf("expected revision 3, got %d", rev3)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit rev3: %v", err)
	}

	// Evaluate revision 3 (Bob should conquer from Ana):
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	res3, hasWork, err := evalAdapter.EvaluateNextRevisionTx(ctx, tx, seasonKey, "worker-1")
	if err != nil {
		t.Fatalf("evaluate rev 3: %v", err)
	}
	if !hasWork {
		t.Fatalf("expected work on rev 3")
	}
	if res3.HolderSubject != bobID {
		t.Fatalf("expected bob to become holder, got %s", res3.HolderSubject)
	}
	if res3.ReignVersion != 3 {
		t.Fatalf("expected reign version 3, got %d", res3.ReignVersion)
	}
	if res3.Predecessor != anaID {
		t.Fatalf("expected predecessor ana, got %s", res3.Predecessor)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit transition rev 3: %v", err)
	}

	// Verify exactly one active reign in database:
	activeReign, found, err := evalAdapter.GetActiveReign(ctx, pool, seasonKey)
	if err != nil || !found {
		t.Fatalf("get active reign: %v", err)
	}
	if activeReign.HolderSubject != bobID {
		t.Fatalf("expected active monarch bob, got %s", activeReign.HolderSubject)
	}
	if activeReign.ReignVersion != 3 {
		t.Fatalf("expected active reign version 3, got %d", activeReign.ReignVersion)
	}

	// Verify outbox has exactly 2 recorded succession events:
	var outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.seasonal_succession_events WHERE season_id = $1`, seasonKey).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxCount != 2 {
		t.Fatalf("expected 2 succession outbox events, got %d", outboxCount)
	}
}

func TestSuccessionEvaluatorRollbackLeavesNoEventOrState(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-rollback-1"
	anaID, _, _ := setupSeasonAndAccounts(t, ctx, db, seasonKey)

	pool := db.Pool.Pool()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)
	wealthAdapter := crownadapter.NewWealthProjectionAdapter()

	tx, _ := pool.Begin(ctx)
	_ = evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, "rainha-1", 1)
	_ = tx.Commit(ctx)

	// Record wealth in transaction that will ROLLBACK:
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	_, err = wealthAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       anaID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     999999,
	})
	if err != nil {
		t.Fatalf("record wealth: %v", err)
	}
	// Explicit rollback:
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Verify checkpoint is still at 1:
	var lastRev int64
	if err := pool.QueryRow(ctx, `SELECT last_revision FROM app.seasonal_wealth_checkpoints WHERE season_id = $1`, seasonKey).Scan(&lastRev); err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if lastRev != 1 {
		t.Fatalf("expected checkpoint to stay at 1 after rollback, got %d", lastRev)
	}

	// Verify Ana has no projection:
	_, found, _ := wealthAdapter.GetProjection(ctx, pool, seasonKey, anaID)
	if found {
		t.Fatalf("uncommitted projection must not exist after rollback")
	}

	// Worker evaluation finds no work:
	_, _ = evalAdapter.AcquireLease(ctx, pool, seasonKey, "worker-1", 10*time.Second)
	tx, _ = pool.Begin(ctx)
	defer tx.Rollback(ctx)
	_, hasWork, err := evalAdapter.EvaluateNextRevisionTx(ctx, tx, seasonKey, "worker-1")
	if err != nil {
		t.Fatalf("evaluate after rollback: %v", err)
	}
	if hasWork {
		t.Fatalf("evaluator must not find work from uncommitted rollback")
	}
}

func TestSuccessionEvaluatorUniqueReignsConstraint(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-constraints-1"
	setupSeasonAndAccounts(t, ctx, db, seasonKey)

	pool := db.Pool.Pool()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)

	tx, _ := pool.Begin(ctx)
	_ = evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, "rainha-1", 1)
	_ = tx.Commit(ctx)

	t.Run("violates single active reign constraint", func(t *testing.T) {
		// Inserting a second reign with is_active = true must fail seasonal_reigns_active_unique:
		_, err := pool.Exec(ctx, `
			INSERT INTO app.seasonal_reigns (
				season_id, reign_version, holder_subject, is_regent, reason,
				institutional_c, winning_wealth, attained_revision, transition_revision,
				predecessor, is_active
			) VALUES ($1, 2, 'rival', false, 'conquest', 0, 0, 1, 1, 'rainha-1', true)
		`, seasonKey)
		if err == nil {
			t.Fatalf("expected unique constraint violation for second active reign")
		}
	})

	t.Run("violates reign version uniqueness", func(t *testing.T) {
		// Inserting duplicate reign_version (1) must fail seasonal_reigns_version_unique:
		_, err := pool.Exec(ctx, `
			INSERT INTO app.seasonal_reigns (
				season_id, reign_version, holder_subject, is_regent, reason,
				institutional_c, winning_wealth, attained_revision, transition_revision,
				predecessor, is_active
			) VALUES ($1, 1, 'rival', false, 'conquest', 0, 0, 1, 1, 'rainha-1', false)
		`, seasonKey)
		if err == nil {
			t.Fatalf("expected unique constraint violation for duplicate reign_version")
		}
	})
}

func TestSuccessionEvaluatorOutboxUniquenessAndOrdering(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-ordering-1"
	setupSeasonAndAccounts(t, ctx, db, seasonKey)

	pool := db.Pool.Pool()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)

	tx, _ := pool.Begin(ctx)
	_ = evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, "rainha-1", 1)
	_ = tx.Commit(ctx)

	t.Run("duplicate outbox revision is refused", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO app.seasonal_wealth_events
				(season_id, revision, subject_id, beneficiary_kind, event_kind, delta_assets, delta_liabilities)
			VALUES ($1, 1, 'subject-1', 'participante', 'transfer', 10, 0)
		`, seasonKey)
		if err == nil {
			t.Fatalf("expected unique constraint violation for duplicate outbox revision 1")
		}
	})

	t.Run("revision gap refuses evaluation", func(t *testing.T) {
		// Manually insert revision 3 (skipping revision 2) and advance checkpoint to 3:
		mustExec(t, ctx, pool, `
			INSERT INTO app.seasonal_wealth_events
				(season_id, revision, subject_id, beneficiary_kind, event_kind, delta_assets, delta_liabilities)
			VALUES ($1, 3, 'subject-1', 'participante', 'transfer', 10, 0)
		`, seasonKey)
		mustExec(t, ctx, pool, `
			UPDATE app.seasonal_wealth_checkpoints SET last_revision = 3 WHERE season_id = $1
		`, seasonKey)

		_, _ = evalAdapter.AcquireLease(ctx, pool, seasonKey, "worker-1", 10*time.Second)

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		defer tx.Rollback(ctx)

		// Evaluating targetRev=2 when only 3 exists must fail with ErrRevisionGap:
		_, _, err = evalAdapter.EvaluateNextRevisionTx(ctx, tx, seasonKey, "worker-1")
		if !errors.Is(err, crowndomain.ErrRevisionGap) {
			t.Fatalf("expected ErrRevisionGap on missing revision 2, got %v", err)
		}
	})
}

func TestSuccessionEvaluatorWorkerLeaseAcquisitionAndExpiry(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-leases-1"
	setupSeasonAndAccounts(t, ctx, db, seasonKey)

	pool := db.Pool.Pool()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)

	tx, _ := pool.Begin(ctx)
	_ = evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, "rainha-1", 1)
	_ = tx.Commit(ctx)

	// Worker 1 acquires lease for 5 seconds:
	ok, err := evalAdapter.AcquireLease(ctx, pool, seasonKey, "worker-1", 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("worker-1 failed to acquire lease: ok=%v, err=%v", ok, err)
	}

	// Worker 2 attempts to acquire lease while worker-1's lease is active:
	ok, err = evalAdapter.AcquireLease(ctx, pool, seasonKey, "worker-2", 5*time.Second)
	if !errors.Is(err, crowndomain.ErrWorkerLeaseBusy) {
		t.Fatalf("expected ErrWorkerLeaseBusy for worker-2, got ok=%v, err=%v", ok, err)
	}

	// Simulate lease expiry by backdating lease_expires_at:
	mustExec(t, ctx, pool, `
		UPDATE app.seasonal_succession_evaluators
		SET lease_expires_at = now() - interval '10 seconds'
		WHERE season_id = $1
	`, seasonKey)

	// Worker 1 tries to evaluate with expired lease:
	tx, _ = pool.Begin(ctx)
	defer tx.Rollback(ctx)
	_, _, err = evalAdapter.EvaluateNextRevisionTx(ctx, tx, seasonKey, "worker-1")
	if !errors.Is(err, crowndomain.ErrStaleWorkerLease) {
		t.Fatalf("expected ErrStaleWorkerLease, got %v", err)
	}
	_ = tx.Rollback(ctx)

	// Worker 2 can now acquire the expired lease:
	ok, err = evalAdapter.AcquireLease(ctx, pool, seasonKey, "worker-2", 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("worker-2 failed to acquire expired lease: ok=%v, err=%v", ok, err)
	}
}

func TestSuccessionEvaluatorConcurrentEvaluationWithRace(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-concurrency-1"
	anaID, _, _ := setupSeasonAndAccounts(t, ctx, db, seasonKey)

	pool := db.Pool.Pool()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)
	wealthAdapter := crownadapter.NewWealthProjectionAdapter()

	tx, _ := pool.Begin(ctx)
	_ = evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, "rainha-1", 1)
	_ = tx.Commit(ctx)

	// Record wealth event revision 2:
	tx, _ = pool.Begin(ctx)
	_, _ = wealthAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       anaID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     6000000000,
	})
	_ = tx.Commit(ctx)

	var wg sync.WaitGroup
	results := make(chan error, 2)

	for _, workerID := range []string{"worker-alpha", "worker-beta"} {
		wg.Add(1)
		go func(wID string) {
			defer wg.Done()
			acquired, err := evalAdapter.AcquireLease(ctx, pool, seasonKey, wID, 5*time.Second)
			if err != nil {
				results <- err
				return
			}
			if !acquired {
				results <- crowndomain.ErrWorkerLeaseBusy
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
		}(workerID)
	}

	wg.Wait()
	close(results)

	successCount := 0
	busyCount := 0
	for res := range results {
		if res == nil {
			successCount++
		} else if errors.Is(res, crowndomain.ErrWorkerLeaseBusy) {
			busyCount++
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly 1 worker to succeed, got %d", successCount)
	}
	if busyCount != 1 {
		t.Fatalf("expected exactly 1 worker to encounter lease busy, got %d", busyCount)
	}

	// Verify that revision was evaluated exactly once:
	var lastEvaluated int64
	if err := pool.QueryRow(ctx, `SELECT last_evaluated_revision FROM app.seasonal_succession_evaluators WHERE season_id = $1`, seasonKey).Scan(&lastEvaluated); err != nil {
		t.Fatalf("read last evaluated: %v", err)
	}
	if lastEvaluated != 2 {
		t.Fatalf("expected last evaluated revision 2, got %d", lastEvaluated)
	}
}

func TestSuccessionEvaluatorCrashRollbackAndReplay(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-replay-1"
	anaID, _, _ := setupSeasonAndAccounts(t, ctx, db, seasonKey)

	pool := db.Pool.Pool()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)
	wealthAdapter := crownadapter.NewWealthProjectionAdapter()

	tx, _ := pool.Begin(ctx)
	_ = evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, "rainha-1", 1)
	_ = tx.Commit(ctx)

	// Record revision 2:
	tx, _ = pool.Begin(ctx)
	_, _ = wealthAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       anaID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     6000000000,
	})
	_ = tx.Commit(ctx)

	_, _ = evalAdapter.AcquireLease(ctx, pool, seasonKey, "worker-1", 10*time.Second)

	// Simulate crash during transaction evaluation:
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)
	res, hasWork, err := evalAdapter.EvaluateNextRevisionTx(ctx, tx, seasonKey, "worker-1")
	if err != nil || !hasWork {
		t.Fatalf("evaluate rev 2: %v", err)
	}
	if res.HolderSubject != anaID {
		t.Fatalf("expected ana, got %s", res.HolderSubject)
	}
	// "Crash": Rollback instead of commit:
	_ = tx.Rollback(ctx)

	// Check that state was not partially applied:
	activeReign, _, _ := evalAdapter.GetActiveReign(ctx, pool, seasonKey)
	if activeReign.HolderSubject != "rainha-1" {
		t.Fatalf("expected monarch to still be rainha-1 after rollback, got %s", activeReign.HolderSubject)
	}

	// Replay evaluation:
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	resReplay, hasWork, err := evalAdapter.EvaluateNextRevisionTx(ctx, tx, seasonKey, "worker-1")
	if err != nil || !hasWork {
		t.Fatalf("replay evaluation failed: %v", err)
	}
	if resReplay.HolderSubject != anaID {
		t.Fatalf("expected ana on replay, got %s", resReplay.HolderSubject)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit replay: %v", err)
	}

	// Now active monarch is Ana:
	activeReign, _, _ = evalAdapter.GetActiveReign(ctx, pool, seasonKey)
	if activeReign.HolderSubject != anaID {
		t.Fatalf("expected active monarch ana, got %s", activeReign.HolderSubject)
	}
}

func TestSuccessionEvaluatorBacklogHaltsRoyalActs(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-backlog-1"
	anaID, _, _ := setupSeasonAndAccounts(t, ctx, db, seasonKey)

	pool := db.Pool.Pool()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)
	wealthAdapter := crownadapter.NewWealthProjectionAdapter()

	tx, _ := pool.Begin(ctx)
	_ = evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, "rainha-1", 1)
	_ = tx.Commit(ctx)

	// At start: revision 1 evaluated, last_revision 1 -> backlog is 0:
	if err := evalAdapter.CheckBacklog(ctx, pool, seasonKey); err != nil {
		t.Fatalf("expected no backlog at revision 1, got %v", err)
	}

	current, err := evalAdapter.Current(ctx, crowndomain.SeasonID(seasonKey))
	if err != nil {
		t.Fatalf("expected Current to succeed, got %v", err)
	}
	if current.Holder != "rainha-1" {
		t.Fatalf("expected rainha-1, got %s", current.Holder)
	}

	// Now record event revision 2 (economic journal advances to 2):
	tx, _ = pool.Begin(ctx)
	_, _ = wealthAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       anaID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     6000000000,
	})
	_ = tx.Commit(ctx)

	// Now checkpoint last_revision = 2, but evaluator is still at 1:
	err = evalAdapter.CheckBacklog(ctx, pool, seasonKey)
	if !errors.Is(err, crowndomain.ErrBacklogUnevaluated) {
		t.Fatalf("expected ErrBacklogUnevaluated, got %v", err)
	}

	// Calling ReignResolver.Current must be blocked by unevaluated backlog:
	_, err = evalAdapter.Current(ctx, crowndomain.SeasonID(seasonKey))
	if !errors.Is(err, crowndomain.ErrBacklogUnevaluated) {
		t.Fatalf("expected Current to fail with ErrBacklogUnevaluated, got %v", err)
	}

	// Worker processes revision 2:
	_, _ = evalAdapter.AcquireLease(ctx, pool, seasonKey, "worker-1", 10*time.Second)
	tx, _ = pool.Begin(ctx)
	_, _, _ = evalAdapter.EvaluateNextRevisionTx(ctx, tx, seasonKey, "worker-1")
	_ = tx.Commit(ctx)

	// Backlog is cleared! CheckBacklog returns nil:
	if err := evalAdapter.CheckBacklog(ctx, pool, seasonKey); err != nil {
		t.Fatalf("expected no backlog after evaluation, got %v", err)
	}

	// Current now succeeds and returns Ana!
	current, err = evalAdapter.Current(ctx, crowndomain.SeasonID(seasonKey))
	if err != nil {
		t.Fatalf("expected Current to succeed after clearing backlog, got %v", err)
	}
	if current.Holder != crowndomain.HolderSubject(anaID) {
		t.Fatalf("expected ana as current holder, got %s", current.Holder)
	}
	if current.Reign != 2 {
		t.Fatalf("expected reign 2, got %d", current.Reign)
	}
}

func recordAliceAssetsAndDebt(
	t *testing.T,
	ctx context.Context,
	pool crownadapter.DBQuerier,
	wealthAdapter *crownadapter.WealthProjectionAdapter,
	seasonKey, aliceID string,
) {
	t.Helper()
	p, ok := pool.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		t.Fatalf("expected tx pool")
	}

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin alice assets tx: %v", err)
	}
	defer tx.Rollback(ctx)
	_, err = wealthAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       aliceID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     7000000000,
	})
	if err != nil {
		t.Fatalf("record alice assets: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit alice assets: %v", err)
	}

	tx2, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin alice debt tx: %v", err)
	}
	defer tx2.Rollback(ctx)
	_, err = wealthAdapter.RegisterObligationTx(ctx, tx2, crownadapter.RegisterObligationParams{
		SeasonID:        seasonKey,
		Debtor:          aliceID,
		Kind:            string(crowndomain.ObligationKindRegisteredLoan),
		Amount:          500000000,
		AlreadyDeducted: false,
		SourceRef:       "notarial-alice-debt-1",
	})
	if err != nil {
		t.Fatalf("record alice debt: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit alice debt: %v", err)
	}
}

func recordBobWealth(
	t *testing.T,
	ctx context.Context,
	pool crownadapter.DBQuerier,
	wealthAdapter *crownadapter.WealthProjectionAdapter,
	seasonKey, bobID string,
) {
	t.Helper()
	p, ok := pool.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		t.Fatalf("expected tx pool")
	}

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin bob tx: %v", err)
	}
	defer tx.Rollback(ctx)
	_, err = wealthAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       bobID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     8000000000,
	})
	if err != nil {
		t.Fatalf("record bob wealth: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit bob wealth: %v", err)
	}
}

func evalStep(
	t *testing.T,
	ctx context.Context,
	pool crownadapter.DBQuerier,
	evalAdapter *crownadapter.SuccessionEvaluatorAdapter,
	seasonKey, workerID, wantHolder string,
	wantReign int,
) {
	t.Helper()
	p, ok := pool.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		t.Fatalf("expected tx pool")
	}

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin eval tx: %v", err)
	}
	defer tx.Rollback(ctx)

	res, hasWork, err := evalAdapter.EvaluateNextRevisionTx(ctx, tx, seasonKey, workerID)
	if err != nil || !hasWork {
		t.Fatalf("eval step: hasWork=%v, err=%v", hasWork, err)
	}
	if res.HolderSubject != wantHolder || res.ReignVersion != wantReign {
		t.Fatalf("eval result = %+v, want holder=%s reign=%d", res, wantHolder, wantReign)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit eval step: %v", err)
	}
}

func verifyAlicePreserved(
	t *testing.T,
	ctx context.Context,
	pool crownadapter.DBQuerier,
	wealthAdapter *crownadapter.WealthProjectionAdapter,
	seasonKey, aliceID string,
) {
	t.Helper()
	proj, found, err := wealthAdapter.GetProjection(ctx, pool, seasonKey, aliceID)
	if err != nil || !found {
		t.Fatalf("get alice projection: %v", err)
	}
	if proj.AssetsMilli != 7000000000 || proj.LiabilitiesMilli != 500000000 || proj.Wealth != 6500000000 {
		t.Fatalf("alice wealth not preserved: %+v", proj)
	}

	var oblCount int
	var oblSum int64
	err = pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(amount_milli), 0)
		FROM app.seasonal_wealth_obligations
		WHERE season_id = $1 AND debtor_subject = $2
	`, seasonKey, aliceID).Scan(&oblCount, &oblSum)
	if err != nil || oblCount != 1 || oblSum != 500000000 {
		t.Fatalf("alice obligations: err=%v, count=%d, sum=%d", err, oblCount, oblSum)
	}
}

func verifyReignTransitionAndFencing(
	t *testing.T,
	ctx context.Context,
	pool crownadapter.DBQuerier,
	evalAdapter *crownadapter.SuccessionEvaluatorAdapter,
	seasonKey, aliceID, bobID string,
) {
	t.Helper()
	reignRec, found, err := evalAdapter.GetActiveReign(ctx, pool, seasonKey)
	if err != nil || !found || reignRec.HolderSubject != bobID || reignRec.ReignVersion != 2 || !reignRec.IsActive {
		t.Fatalf("active reign record: %+v, found=%v, err=%v", reignRec, found, err)
	}

	var aliceActive bool
	var aliceEnded *time.Time
	err = pool.QueryRow(ctx, `
		SELECT is_active, ended_at
		FROM app.seasonal_reigns
		WHERE season_id = $1 AND reign_version = 1
	`, seasonKey).Scan(&aliceActive, &aliceEnded)
	if err != nil || aliceActive || aliceEnded == nil {
		t.Fatalf("reign 1 active=%v, ended=%v, err=%v", aliceActive, aliceEnded, err)
	}

	current, err := evalAdapter.Current(ctx, crowndomain.SeasonID(seasonKey))
	if err != nil || current.Holder != crowndomain.HolderSubject(bobID) || current.Reign != 2 {
		t.Fatalf("current reign: %+v, err=%v", current, err)
	}

	openCurrent := current
	openCurrent.Open = true
	openCurrent.StartsAt = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	openCurrent.EndsAt = openCurrent.StartsAt.Add(7776000 * time.Second)
	testNow := openCurrent.StartsAt.Add(time.Hour)

	fenceAlice := crowndomain.EffectFence{
		Season:               crowndomain.SeasonID(seasonKey),
		Reign:                2,
		Competence:           "patrimonial",
		Author:               crowndomain.HolderSubject(aliceID),
		CurrentReign:         openCurrent,
		EconomicBacklogClean: true,
		Now:                  testNow,
	}
	if err := crowndomain.ValidateEffectFence(fenceAlice); !errors.Is(err, crowndomain.ErrNotHolder) {
		t.Fatalf("fence alice err = %v, want ErrNotHolder", err)
	}

	fenceStale := crowndomain.EffectFence{
		Season:               crowndomain.SeasonID(seasonKey),
		Reign:                1,
		Competence:           "patrimonial",
		Author:               crowndomain.HolderSubject(aliceID),
		CurrentReign:         openCurrent,
		EconomicBacklogClean: true,
		Now:                  testNow,
	}
	if err := crowndomain.ValidateEffectFence(fenceStale); !errors.Is(err, crowndomain.ErrStaleReign) {
		t.Fatalf("fence stale err = %v, want ErrStaleReign", err)
	}
}

func TestExKingWealthAndObligationsPreservedAfterSuccession(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-sucessao-preserve"
	aliceID, bobID, _ := setupSeasonAndAccounts(t, ctx, db, seasonKey)
	pool := db.Pool.Pool()
	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)
	wealthAdapter := crownadapter.NewWealthProjectionAdapter()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := evalAdapter.InitSeasonEvaluatorTx(ctx, tx, seasonKey, aliceID, 1); err != nil {
		t.Fatalf("init evaluator: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit init: %v", err)
	}

	if acq, err := evalAdapter.AcquireLease(ctx, pool, seasonKey, "worker-preserve", 10*time.Second); err != nil || !acq {
		t.Fatalf("acquire lease: %v", err)
	}

	// 1. Alice earns assets (rev 2) and incurs debt (rev 3):
	recordAliceAssetsAndDebt(t, ctx, pool, wealthAdapter, seasonKey, aliceID)
	evalStep(t, ctx, pool, evalAdapter, seasonKey, "worker-preserve", aliceID, 1)
	evalStep(t, ctx, pool, evalAdapter, seasonKey, "worker-preserve", aliceID, 1)

	// 2. Bob earns assets (rev 4) and conquers throne:
	recordBobWealth(t, ctx, pool, wealthAdapter, seasonKey, bobID)
	evalStep(t, ctx, pool, evalAdapter, seasonKey, "worker-preserve", bobID, 2)

	// 3. Verify Alice's balance/debts preserved and royal effects fenced:
	verifyAlicePreserved(t, ctx, pool, wealthAdapter, seasonKey, aliceID)
	verifyReignTransitionAndFencing(t, ctx, pool, evalAdapter, seasonKey, aliceID, bobID)
}
