package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	crownadapter "github.com/AlexandreZanata/Regnovum/internal/crown/adapters/postgres"
	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func baseDSN() string {
	if dsn := os.Getenv("ARENA_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable"
}

func newTestDB(t testing.TB, opts ...dbtest.Option) *dbtest.TestDB {
	t.Helper()
	allOpts := append([]dbtest.Option{dbtest.WithBaseDSN(baseDSN())}, opts...)
	return dbtest.New(t, allOpts...)
}

func testContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func mustExec(t testing.TB, ctx context.Context, ex crownadapter.DBQuerier, sql string, args ...any) {
	t.Helper()
	if _, err := ex.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %s args %+v: %v", sql, args, err)
	}
}

func setupSeasonAndAccounts(t *testing.T, ctx context.Context, db *dbtest.TestDB, seasonKey string) (anaID, bobID, carlosID string) {
	t.Helper()
	pool := db.Pool.Pool()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	// Seed season row in app.seasons:
	mustExec(t, ctx, tx, `
		INSERT INTO app.seasons
			(season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		VALUES ($1, 1, '2026-04-01T00:00:00Z', '2026-04-01T00:00:00Z'::timestamptz + make_interval(secs => 7776000), 'v1', 'wealth-v1', 'rainha-1', 'regente-1', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')
		ON CONFLICT (season_key) DO NOTHING
	`, seasonKey)

	// Seed Treasury custody in app.economy_custodies:
	mustExec(t, ctx, tx, `
		INSERT INTO app.economy_custodies (id, kind, label, season_key)
		VALUES (uuidv7(), 'treasury', 'free_treasury', $1)
		ON CONFLICT DO NOTHING
	`, seasonKey)

	// Fund Treasury with initial genesis balance:
	var genesisTransferID string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&genesisTransferID); err != nil {
		t.Fatalf("gen genesis id: %v", err)
	}
	var freeTreasuryID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM app.economy_custodies WHERE season_key = $1 AND label = 'free_treasury'`, seasonKey).Scan(&freeTreasuryID); err != nil {
		t.Fatalf("get treasury id: %v", err)
	}
	mustExec(t, ctx, tx, `
		INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		VALUES ($1::uuid, $2::uuid, 'credit', 5000000000, $3)
	`, genesisTransferID, freeTreasuryID, seasonKey)

	initAdapter := crownadapter.NewWealthProjectionAdapter()
	if _, err := initAdapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       string(crowndomain.InstitutionalCrownSubject),
		BeneficiaryKind: string(crowndomain.BeneficiaryKindInstitutional),
		EventKind:       "genesis",
		DeltaAssets:     5000000000,
	}); err != nil {
		t.Fatalf("seed treasury outbox: %v", err)
	}

	// Seed accounts in app.accounts:
	var id1, id2, id3 string
	if err := tx.QueryRow(ctx, `INSERT INTO app.accounts (email, status) VALUES ('ana-' || gen_random_uuid()::text || '@test.example', 'active') RETURNING id::text`).Scan(&id1); err != nil {
		t.Fatalf("seed account ana: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO app.accounts (email, status) VALUES ('bob-' || gen_random_uuid()::text || '@test.example', 'active') RETURNING id::text`).Scan(&id2); err != nil {
		t.Fatalf("seed account bob: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO app.accounts (email, status) VALUES ('carlos-' || gen_random_uuid()::text || '@test.example', 'active') RETURNING id::text`).Scan(&id3); err != nil {
		t.Fatalf("seed account carlos: %v", err)
	}

	// Seed user custodies in app.economy_custodies:
	mustExec(t, ctx, tx, `INSERT INTO app.economy_custodies (id, kind, label, season_key, owner_account_id) VALUES (uuidv7(), 'user', 'wallet-ana', $1, $2::uuid)`, seasonKey, id1)
	mustExec(t, ctx, tx, `INSERT INTO app.economy_custodies (id, kind, label, season_key, owner_account_id) VALUES (uuidv7(), 'user', 'wallet-bob', $1, $2::uuid)`, seasonKey, id2)
	mustExec(t, ctx, tx, `INSERT INTO app.economy_custodies (id, kind, label, season_key, owner_account_id) VALUES (uuidv7(), 'user', 'wallet-carlos', $1, $2::uuid)`, seasonKey, id3)

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit setup: %v", err)
	}
	return id1, id2, id3
}

func getCustodyID(t *testing.T, ctx context.Context, tx pgx.Tx, seasonKey, label string) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM app.economy_custodies WHERE season_key = $1 AND label = $2`, seasonKey, label).Scan(&id); err != nil {
		t.Fatalf("get custody id for %s: %v", label, err)
	}
	return id
}

func recordTransferLegs(t *testing.T, ctx context.Context, tx pgx.Tx, seasonKey, fromLabel, toLabel string, amount int64) {
	t.Helper()
	fromCus := getCustodyID(t, ctx, tx, seasonKey, fromLabel)
	toCus := getCustodyID(t, ctx, tx, seasonKey, toLabel)

	var transferID string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&transferID); err != nil {
		t.Fatalf("gen transfer id: %v", err)
	}

	mustExec(t, ctx, tx, `
		INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		VALUES ($1::uuid, $2::uuid, 'debit', $3, $4)
	`, transferID, fromCus, amount, seasonKey)

	mustExec(t, ctx, tx, `
		INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		VALUES ($1::uuid, $2::uuid, 'credit', $3, $4)
	`, transferID, toCus, amount, seasonKey)
}

func TestWealthProjectionMatchesOracleAfterTransfers(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-1"
	anaID, bobID, carlosID := setupSeasonAndAccounts(t, ctx, db, seasonKey)
	adapter := crownadapter.NewWealthProjectionAdapter()
	pool := db.Pool.Pool()

	// Transfer 1: Treasury -> Ana (10,000 milliINK)
	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	recordTransferLegs(t, ctx, tx1, seasonKey, "free_treasury", "wallet-ana", 10000)
	_, err = adapter.RecordOutboxEventTx(ctx, tx1, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       string(crowndomain.InstitutionalCrownSubject),
		BeneficiaryKind: string(crowndomain.BeneficiaryKindInstitutional),
		EventKind:       "transfer",
		DeltaAssets:     -10000,
	})
	if err != nil {
		t.Fatalf("record outbox treasury tx1: %v", err)
	}
	_, err = adapter.RecordOutboxEventTx(ctx, tx1, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       anaID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     10000,
	})
	if err != nil {
		t.Fatalf("record outbox ana tx1: %v", err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit tx1: %v", err)
	}

	// Transfer 2: Ana -> Bob (4,000 milliINK)
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	recordTransferLegs(t, ctx, tx2, seasonKey, "wallet-ana", "wallet-bob", 4000)
	_, err = adapter.RecordOutboxEventTx(ctx, tx2, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       anaID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     -4000,
	})
	if err != nil {
		t.Fatalf("record outbox ana tx2: %v", err)
	}
	_, err = adapter.RecordOutboxEventTx(ctx, tx2, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       bobID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     4000,
	})
	if err != nil {
		t.Fatalf("record outbox bob tx2: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit tx2: %v", err)
	}

	// Transfer 3: Bob -> Carlos (1,500 milliINK)
	tx3, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx3: %v", err)
	}
	recordTransferLegs(t, ctx, tx3, seasonKey, "wallet-bob", "wallet-carlos", 1500)
	_, err = adapter.RecordOutboxEventTx(ctx, tx3, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       bobID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     -1500,
	})
	if err != nil {
		t.Fatalf("record outbox bob tx3: %v", err)
	}
	_, err = adapter.RecordOutboxEventTx(ctx, tx3, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       carlosID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     1500,
	})
	if err != nil {
		t.Fatalf("record outbox carlos tx3: %v", err)
	}
	if err := tx3.Commit(ctx); err != nil {
		t.Fatalf("commit tx3: %v", err)
	}

	// Verify incremental projection values:
	pAna, found, err := adapter.GetProjection(ctx, pool, seasonKey, anaID)
	if err != nil || !found {
		t.Fatalf("projection ana not found: %v", err)
	}
	if pAna.Wealth != 6000 || pAna.AssetsMilli != 6000 {
		t.Fatalf("ana projection = %+v, want wealth=6000", pAna)
	}

	pBob, found, err := adapter.GetProjection(ctx, pool, seasonKey, bobID)
	if err != nil || !found {
		t.Fatalf("projection bob not found: %v", err)
	}
	if pBob.Wealth != 2500 || pBob.AssetsMilli != 2500 {
		t.Fatalf("bob projection = %+v, want wealth=2500", pBob)
	}

	pCarlos, found, err := adapter.GetProjection(ctx, pool, seasonKey, carlosID)
	if err != nil || !found {
		t.Fatalf("projection carlos not found: %v", err)
	}
	if pCarlos.Wealth != 1500 || pCarlos.AssetsMilli != 1500 {
		t.Fatalf("carlos projection = %+v, want wealth=1500", pCarlos)
	}

	// Verify against independent rebuild oracle:
	txOracle, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txOracle: %v", err)
	}
	defer txOracle.Rollback(ctx)

	summary, err := adapter.RebuildAllProjections(ctx, txOracle, seasonKey)
	if err != nil {
		t.Fatalf("rebuild oracle failed (projection diverged): %v", err)
	}
	if summary.SubjectCount < 3 {
		t.Fatalf("oracle subject count = %d, want >= 3", summary.SubjectCount)
	}
	if summary.CheckpointHash == "" {
		t.Fatalf("oracle checkpoint hash is empty")
	}
}

func TestWealthProjectionAfterDebtReserveAndRefund(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-1"
	anaID, bobID, carlosID := setupSeasonAndAccounts(t, ctx, db, seasonKey)
	adapter := crownadapter.NewWealthProjectionAdapter()
	pool := db.Pool.Pool()

	// Initial transfer Treasury -> Carlos (1,500 milliINK)
	txInit, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txInit: %v", err)
	}
	recordTransferLegs(t, ctx, txInit, seasonKey, "free_treasury", "wallet-carlos", 1500)
	_, _ = adapter.RecordOutboxEventTx(ctx, txInit, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       string(crowndomain.InstitutionalCrownSubject),
		BeneficiaryKind: string(crowndomain.BeneficiaryKindInstitutional),
		EventKind:       "transfer",
		DeltaAssets:     -1500,
	})
	_, _ = adapter.RecordOutboxEventTx(ctx, txInit, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       carlosID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     1500,
	})
	if err := txInit.Commit(ctx); err != nil {
		t.Fatalf("commit txInit: %v", err)
	}

	// Debt: Register loan of 1,000 for Carlos
	txDebt, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txDebt: %v", err)
	}
	_, err = adapter.RegisterObligationTx(ctx, txDebt, crownadapter.RegisterObligationParams{
		SeasonID:        seasonKey,
		Debtor:          carlosID,
		Kind:            string(crowndomain.ObligationKindRegisteredLoan),
		Amount:          1000,
		AlreadyDeducted: false,
		SourceRef:       "loan-contract-1",
	})
	if err != nil {
		t.Fatalf("register debt: %v", err)
	}
	if err := txDebt.Commit(ctx); err != nil {
		t.Fatalf("commit txDebt: %v", err)
	}

	pCarlosAfterDebt, found, err := adapter.GetProjection(ctx, pool, seasonKey, carlosID)
	if err != nil || !found {
		t.Fatalf("carlos projection not found: %v", err)
	}
	// Assets=1500, Liabilities=1000 -> Wealth=500
	if pCarlosAfterDebt.Wealth != 500 || pCarlosAfterDebt.LiabilitiesMilli != 1000 {
		t.Fatalf("carlos after debt = %+v, want wealth=500", pCarlosAfterDebt)
	}

	// Reserve: Earmark 50,000 for Crown internal reserve
	txReserve, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txReserve: %v", err)
	}
	_, err = adapter.RegisterObligationTx(ctx, txReserve, crownadapter.RegisterObligationParams{
		SeasonID:        seasonKey,
		Debtor:          string(crowndomain.InstitutionalCrownSubject),
		Kind:            string(crowndomain.ObligationKindInternalReserve),
		Amount:          50000,
		AlreadyDeducted: false,
		SourceRef:       "reserve-earmark",
	})
	if err != nil {
		t.Fatalf("register reserve: %v", err)
	}
	if err := txReserve.Commit(ctx); err != nil {
		t.Fatalf("commit txReserve: %v", err)
	}

	pCrown, found, err := adapter.GetProjection(ctx, pool, seasonKey, string(crowndomain.InstitutionalCrownSubject))
	if err != nil || !found {
		t.Fatalf("crown projection not found: %v", err)
	}
	// Internal reserve is non-deductible; liabilities must remain 0:
	if pCrown.LiabilitiesMilli != 0 {
		t.Fatalf("crown liabilities = %d, want 0 (internal reserve is non-deductible)", pCrown.LiabilitiesMilli)
	}

	// Refund: Carlos transfers 500 to Bob
	txRefund, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txRefund: %v", err)
	}
	recordTransferLegs(t, ctx, txRefund, seasonKey, "wallet-carlos", "wallet-bob", 500)
	_, _ = adapter.RecordOutboxEventTx(ctx, txRefund, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       carlosID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "refund",
		DeltaAssets:     -500,
	})
	_, _ = adapter.RecordOutboxEventTx(ctx, txRefund, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       bobID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "refund",
		DeltaAssets:     500,
	})
	if err := txRefund.Commit(ctx); err != nil {
		t.Fatalf("commit txRefund: %v", err)
	}

	pCarlosAfterRefund, _, _ := adapter.GetProjection(ctx, pool, seasonKey, carlosID)
	// Assets=1000, Liabilities=1000 -> Wealth=0 (neutralized)
	if pCarlosAfterRefund.Wealth != 0 || pCarlosAfterRefund.AssetsMilli != 1000 {
		t.Fatalf("carlos after refund = %+v, want wealth=0", pCarlosAfterRefund)
	}

	// Run oracle rebuild verification:
	txOracle, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txOracle: %v", err)
	}
	defer txOracle.Rollback(ctx)

	if _, err := adapter.RebuildAllProjections(ctx, txOracle, seasonKey); err != nil {
		t.Fatalf("rebuild oracle failed (projection diverged after debt/reserve/refund): %v", err)
	}
	_ = anaID
}

func TestWealthUncommittedEventsDoNotAppear(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-1"
	anaID, _, _ := setupSeasonAndAccounts(t, ctx, db, seasonKey)
	adapter := crownadapter.NewWealthProjectionAdapter()
	pool := db.Pool.Pool()

	// Transaction that aborts / rolls back:
	txAbort, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txAbort: %v", err)
	}
	recordTransferLegs(t, ctx, txAbort, seasonKey, "free_treasury", "wallet-ana", 99999)
	_, err = adapter.RecordOutboxEventTx(ctx, txAbort, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       anaID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     99999,
	})
	if err != nil {
		t.Fatalf("record outbox in txAbort: %v", err)
	}

	// Rollback:
	if err := txAbort.Rollback(ctx); err != nil {
		t.Fatalf("rollback txAbort: %v", err)
	}

	// Verify uncommitted event does NOT appear in projection or outbox:
	pAna, found, err := adapter.GetProjection(ctx, pool, seasonKey, anaID)
	if err != nil {
		t.Fatalf("get projection: %v", err)
	}
	if found && pAna.Wealth == 99999 {
		t.Fatalf("uncommitted wealth appeared in projection: %+v", pAna)
	}

	var eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.seasonal_wealth_events WHERE season_id = $1 AND subject_id = $2`, seasonKey, anaID).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("uncommitted outbox event appeared in table, count = %d", eventCount)
	}
}

func TestWealthStaleOrGapRevisionBlocksAct(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-1"
	anaID, _, _ := setupSeasonAndAccounts(t, ctx, db, seasonKey)
	adapter := crownadapter.NewWealthProjectionAdapter()
	pool := db.Pool.Pool()

	// Record one event advancing revision to 1:
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	rev, err := adapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
		SeasonID:        seasonKey,
		SubjectID:       anaID,
		BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
		EventKind:       "transfer",
		DeltaAssets:     100,
	})
	if err != nil {
		t.Fatalf("record outbox: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Fresh revision matches:
	if err := adapter.CheckRevisionFreshness(ctx, pool, seasonKey, rev); err != nil {
		t.Fatalf("fresh revision failed: %v", err)
	}

	// Stale revision: expected < current
	if err := adapter.CheckRevisionFreshness(ctx, pool, seasonKey, rev-1); !errors.Is(err, crowndomain.ErrStaleRevision) {
		t.Fatalf("stale revision = %v, want ErrStaleRevision", err)
	}

	// Revision gap: expected > current
	if err := adapter.CheckRevisionFreshness(ctx, pool, seasonKey, rev+2); !errors.Is(err, crowndomain.ErrRevisionGap) {
		t.Fatalf("revision gap = %v, want ErrRevisionGap", err)
	}

	// Freeze check:
	txFreeze, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin freeze: %v", err)
	}
	if err := adapter.SetSeasonFreeze(ctx, txFreeze, seasonKey, true); err != nil {
		t.Fatalf("set freeze: %v", err)
	}
	if err := txFreeze.Commit(ctx); err != nil {
		t.Fatalf("commit freeze: %v", err)
	}

	if err := adapter.CheckRevisionFreshness(ctx, pool, seasonKey, rev); !errors.Is(err, crowndomain.ErrProjectionFrozen) {
		t.Fatalf("frozen projection = %v, want ErrProjectionFrozen", err)
	}
}

func TestTop1LeaderExplainUsesIndex(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := testContext()
	defer cancel()

	seasonKey := "temporada-1"
	anaID, bobID, carlosID := setupSeasonAndAccounts(t, ctx, db, seasonKey)
	adapter := crownadapter.NewWealthProjectionAdapter()
	pool := db.Pool.Pool()

	// Seed multiple projections:
	// Ana: 50,000
	// Bob: 75,000 (Highest)
	// Carlos: 25,000
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := adapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{SeasonID: seasonKey, SubjectID: anaID, EventKind: "transfer", DeltaAssets: 50000}); err != nil {
		t.Fatalf("record outbox ana: %v", err)
	}
	if _, err := adapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{SeasonID: seasonKey, SubjectID: bobID, EventKind: "transfer", DeltaAssets: 75000}); err != nil {
		t.Fatalf("record outbox bob: %v", err)
	}
	if _, err := adapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{SeasonID: seasonKey, SubjectID: carlosID, EventKind: "transfer", DeltaAssets: 25000}); err != nil {
		t.Fatalf("record outbox carlos: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Query top-1 leader:
	leader, found, err := adapter.GetTopLeader(ctx, pool, seasonKey)
	if err != nil || !found {
		t.Fatalf("get top leader: %v", err)
	}
	if leader.SubjectID != bobID || leader.Wealth != 75000 {
		t.Fatalf("top leader = %+v, want bob (75000)", leader)
	}

	// EXPLAIN query:
	query := `
		EXPLAIN (FORMAT JSON)
		SELECT subject_id, wealth, attained_revision, beneficiary_kind, assets_milli, liabilities_milli, frozen, eligible
		FROM app.seasonal_wealth_projections
		WHERE season_id = $1 AND frozen = false AND eligible = true
		ORDER BY wealth DESC, attained_revision ASC, subject_id ASC
		LIMIT 1;
	`
	var explainJSON string
	if err := pool.QueryRow(ctx, query, seasonKey).Scan(&explainJSON); err != nil {
		t.Fatalf("explain query: %v", err)
	}

	// Verify that EXPLAIN plan uses the index seasonal_wealth_ranking_idx:
	// In PostgreSQL, Index Scan over seasonal_wealth_ranking_idx will be mentioned.
	// When table is very small, PostgreSQL might consider Index Scan or Bitmap Scan.
	// We force enable_seqscan=off on this session to prove the index is valid and picked!
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Release()

	mustExec(t, ctx, conn, "SET enable_seqscan = off")
	var forceExplain string
	if err := conn.QueryRow(ctx, query, seasonKey).Scan(&forceExplain); err != nil {
		t.Fatalf("forced explain query: %v", err)
	}

	if !strings.Contains(forceExplain, "seasonal_wealth_ranking_idx") {
		t.Fatalf("explain plan does not use seasonal_wealth_ranking_idx: %s", forceExplain)
	}
	if !strings.Contains(forceExplain, "Index Scan") && !strings.Contains(forceExplain, "Index Only Scan") {
		t.Fatalf("explain plan is not Index Scan: %s", forceExplain)
	}
	t.Logf("EXPLAIN plan verified: %s", forceExplain)
}

func BenchmarkTop1LeaderQuery(b *testing.B) {
	db := newTestDB(b)
	ctx := context.Background()

	seasonKey := "temporada-1"
	pool := db.Pool.Pool()
	tx, err := pool.Begin(ctx)
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	mustExec(b, ctx, tx, `
		INSERT INTO app.seasons
			(season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		VALUES ($1, 1, '2026-04-01T00:00:00Z', '2026-04-01T00:00:00Z'::timestamptz + make_interval(secs => 7776000), 'v1', 'wealth-v1', 'rainha-1', 'regente-1', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')
		ON CONFLICT DO NOTHING
	`, seasonKey)
	adapter := crownadapter.NewWealthProjectionAdapter()

	// Seed 50 accounts:
	for i := 0; i < 50; i++ {
		subID := fmt.Sprintf("bench-user-%d", i)
		_, _ = adapter.RecordOutboxEventTx(ctx, tx, crownadapter.WealthEvent{
			SeasonID:        seasonKey,
			SubjectID:       subID,
			BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
			EventKind:       "transfer",
			DeltaAssets:     int64(i * 1000),
		})
	}
	if err := tx.Commit(ctx); err != nil {
		b.Fatalf("commit benchmark seed: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		leader, found, err := adapter.GetTopLeader(ctx, pool, seasonKey)
		if err != nil || !found {
			b.Fatalf("get top leader in bench: %v", err)
		}
		if leader.Wealth != 49000 {
			b.Fatalf("unexpected leader wealth: %d", leader.Wealth)
		}
	}
}

func BenchmarkIncrementalWealthUpdate(b *testing.B) {
	db := newTestDB(b)
	ctx := context.Background()

	seasonKey := "temporada-1"
	pool := db.Pool.Pool()
	tx, err := pool.Begin(ctx)
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	mustExec(b, ctx, tx, `
		INSERT INTO app.seasons
			(season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		VALUES ($1, 1, '2026-04-01T00:00:00Z', '2026-04-01T00:00:00Z'::timestamptz + make_interval(secs => 7776000), 'v1', 'wealth-v1', 'rainha-1', 'regente-1', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')
		ON CONFLICT DO NOTHING
	`, seasonKey)
	_ = tx.Commit(ctx)
	adapter := crownadapter.NewWealthProjectionAdapter()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		bTx, err := pool.Begin(ctx)
		if err != nil {
			b.Fatalf("begin: %v", err)
		}
		subID := fmt.Sprintf("bench-account-%d", i%10)
		_, err = adapter.RecordOutboxEventTx(ctx, bTx, crownadapter.WealthEvent{
			SeasonID:        seasonKey,
			SubjectID:       subID,
			BeneficiaryKind: string(crowndomain.BeneficiaryKindParticipant),
			EventKind:       "transfer",
			DeltaAssets:     10,
		})
		if err != nil {
			b.Fatalf("record outbox: %v", err)
		}
		if err := bTx.Commit(ctx); err != nil {
			b.Fatalf("commit: %v", err)
		}
	}
}
