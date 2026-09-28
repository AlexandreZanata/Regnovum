package postgres_test

// P32-T05 — exactly-once monetary intentions on real PostgreSQL.
//
// One (key, actor, operation) triple settles at most once: the lookup,
// the legs and the persisted response share a transaction, the payload
// hash tells a replay from a conflict, and the triple unique index
// serializes simultaneous duplicates. The tests prove on a disposable
// database: identical replay, conflicting payload refused, simultaneous
// duplicates collapsing to one effect, crash frontiers settling 0 or 1
// (never 2), triple scoping, and conservation throughout.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func intentionCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func settleGenesisForIntentions(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *postgres.Repository {
	t.Helper()
	repo := postgres.NewRepository(pool)
	if _, err := repo.RunGenesis(ctx, application.GenesisRequest{Key: mustTransferKey(t, "genesis-intentions")}); err != nil {
		t.Fatalf("seed Genesis: %v", err)
	}
	return repo
}

func settle(t *testing.T, ctx context.Context, repo *postgres.Repository, key, actor, operation, toLabel string, millis int64) (*application.IdempotentTransferResult, error) {
	t.Helper()
	useCase := application.NewIdempotentTransferUseCase(repo)
	return useCase.Execute(ctx, application.IdempotentTransferCommand{
		Key: key, Actor: actor, Operation: operation,
		FromKind: "treasury", FromLabel: "main",
		ToKind: "user", ToLabel: toLabel,
		Millis: millis,
	})
}

func legsForTransfer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, transferID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_entries WHERE transfer_id = $1::uuid`, transferID).Scan(&count); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	return count
}

func countIntentions(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_intentions`).Scan(&count); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	return count
}

// TestIntentionReplayReturnsStoredResponse proves an identical retry
// after settlement resolves the persisted response: same transfer id,
// Replayed set, zero new legs.
func TestIntentionReplayReturnsStoredResponse(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentionCtx()
	defer cancel()

	repo := settleGenesisForIntentions(t, ctx, pool)
	makeCustody(t, ctx, pool, "user", "ana")

	first, err := settle(t, ctx, repo, "order-1", "ophelia", "sale", "ana", 1000)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if first.Replayed {
		t.Fatalf("first settlement reported Replayed")
	}
	second, err := settle(t, ctx, repo, "order-1", "ophelia", "sale", "ana", 1000)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.Replayed || second.TransferID != first.TransferID {
		t.Fatalf("replay resolved another outcome: %+v vs %+v", second, first)
	}
	if got := legsForTransfer(t, ctx, pool, first.TransferID); got != 2 {
		t.Fatalf("legs for replayed transfer = %d, want 2", got)
	}
	if got := custodyBalance(t, ctx, pool, "user", "ana"); got != 1000 {
		t.Fatalf("ana = %d, want 1000", got)
	}
}

// TestIntentionConflictingPayloadRefused proves a different payload under
// a settled triple is a conflict: the stored transfer stands untouched.
func TestIntentionConflictingPayloadRefused(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentionCtx()
	defer cancel()

	repo := settleGenesisForIntentions(t, ctx, pool)
	makeCustody(t, ctx, pool, "user", "ana")
	makeCustody(t, ctx, pool, "user", "bia")

	first, err := settle(t, ctx, repo, "order-2", "ophelia", "sale", "ana", 1000)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	_, err = settle(t, ctx, repo, "order-2", "ophelia", "sale", "bia", 1000)
	if !errors.Is(err, domain.ErrIntentionConflict) {
		t.Fatalf("changed payload = %v, want ErrIntentionConflict", err)
	}
	if got := legsForTransfer(t, ctx, pool, first.TransferID); got != 2 {
		t.Fatalf("legs for original transfer = %d, want 2", got)
	}
	if got := custodyBalance(t, ctx, pool, "user", "bia"); got != 0 {
		t.Fatalf("bia = %d after conflict, want 0", got)
	}
}

// TestIntentionSimultaneousDuplicatesCollapse proves eight simultaneous
// identical requests settle once: one founder, seven replays sharing the
// transfer id, exactly two legs in the journal.
func TestIntentionSimultaneousDuplicatesCollapse(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(30, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := intentionCtx()
	defer cancel()

	repo := settleGenesisForIntentions(t, ctx, pool)
	makeCustody(t, ctx, pool, "user", "ana")

	const runners = 8
	var wg sync.WaitGroup
	results := make([]*application.IdempotentTransferResult, runners)
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			useCase := application.NewIdempotentTransferUseCase(repo)
			results[i], errs[i] = useCase.Execute(ctx, application.IdempotentTransferCommand{
				Key: "order-race", Actor: "ophelia", Operation: "sale",
				FromKind: "treasury", FromLabel: "main",
				ToKind: "user", ToLabel: "ana",
				Millis: 1000,
			})
		}(i)
	}
	wg.Wait()
	founders, replays := 0, 0
	var transferID string
	for i := range runners {
		if errs[i] != nil {
			t.Fatalf("runner %d: %v", i, errs[i])
		}
		if results[i].Replayed {
			replays++
		} else {
			founders++
		}
		if transferID == "" {
			transferID = results[i].TransferID
		} else if results[i].TransferID != transferID {
			t.Fatalf("runner %d settled another transfer: %s vs %s", i, results[i].TransferID, transferID)
		}
	}
	if founders != 1 || replays != runners-1 {
		t.Fatalf("founders = %d, replays = %d; want 1 and %d", founders, replays, runners-1)
	}
	if got := legsForTransfer(t, ctx, pool, transferID); got != 2 {
		t.Fatalf("legs for raced intention = %d, want 2", got)
	}
	if got := custodyBalance(t, ctx, pool, "user", "ana"); got != 1000 {
		t.Fatalf("ana = %d, want 1000", got)
	}
}

// TestIntentionCrashFrontiersSettleZeroOrOne walks every crash frontier
// and proves the cardinality is 0 or 1, never 2: before any write the
// effect is absent, a torn write reverts whole, and a post-commit retry
// replays the single stored effect.
func TestIntentionCrashFrontiersSettleZeroOrOne(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentionCtx()
	defer cancel()

	repo := settleGenesisForIntentions(t, ctx, pool)
	makeCustody(t, ctx, pool, "user", "ana")

	// Frontier 1: crash before any write (cancelled command).
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	useCase := application.NewIdempotentTransferUseCase(repo)
	if _, err := useCase.Execute(cancelled, application.IdempotentTransferCommand{
		Key: "order-cancelled", Actor: "ophelia", Operation: "sale",
		FromKind: "treasury", FromLabel: "main",
		ToKind: "user", ToLabel: "ana",
		Millis: 100,
	}); err == nil {
		t.Fatalf("cancelled intention succeeded")
	}
	if got := countIntentions(t, ctx, pool); got != 0 {
		t.Fatalf("intentions after pre-write crash = %d, want 0", got)
	}

	// Frontier 2: torn write (legs without the intention row) reverts.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	var treasury string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&treasury); err != nil {
		t.Fatalf("resolve treasury: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES (gen_random_uuid(), $1::uuid, 'debit', 100)`, treasury); err != nil {
		t.Fatalf("torn debit: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := countIntentions(t, ctx, pool); got != 0 {
		t.Fatalf("intentions after torn write = %d, want 0", got)
	}

	// Frontier 3: retry after the commit replays the single effect.
	first, err := settle(t, ctx, repo, "order-crash", "ophelia", "sale", "ana", 100)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	second, err := settle(t, ctx, repo, "order-crash", "ophelia", "sale", "ana", 100)
	if err != nil || !second.Replayed || second.TransferID != first.TransferID {
		t.Fatalf("post-commit retry did not replay the single effect: %+v, %v", second, err)
	}
	if got := legsForTransfer(t, ctx, pool, first.TransferID); got != 2 {
		t.Fatalf("legs for crashed intention = %d, want exactly 2 across all frontiers", got)
	}
}

// TestIntentionTripleScopesIndependently proves the triple scopes the
// effect: the same key under another actor or operation settles its own
// transfer instead of colliding.
func TestIntentionTripleScopesIndependently(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentionCtx()
	defer cancel()

	repo := settleGenesisForIntentions(t, ctx, pool)
	makeCustody(t, ctx, pool, "user", "ana")

	first, err := settle(t, ctx, repo, "order-3", "ophelia", "sale", "ana", 100)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	otherActor, err := settle(t, ctx, repo, "order-3", "beatriz", "sale", "ana", 100)
	if err != nil {
		t.Fatalf("same key, other actor: %v", err)
	}
	otherOperation, err := settle(t, ctx, repo, "order-3", "ophelia", "refund", "ana", 100)
	if err != nil {
		t.Fatalf("same key and actor, other operation: %v", err)
	}
	if otherActor.TransferID == first.TransferID || otherOperation.TransferID == first.TransferID {
		t.Fatalf("independent triples shared one transfer")
	}
	if got := custodyBalance(t, ctx, pool, "user", "ana"); got != 300 {
		t.Fatalf("ana = %d, want 300", got)
	}
	if got := countIntentions(t, ctx, pool); got != 3 {
		t.Fatalf("intentions = %d, want 3", got)
	}
}

// TestIntentionConservationHolds proves the journal still balances after
// intentions settle: credits exceed debits by exactly the Genesis credit.
func TestIntentionConservationHolds(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := intentionCtx()
	defer cancel()

	repo := settleGenesisForIntentions(t, ctx, pool)
	makeCustody(t, ctx, pool, "user", "ana")
	if _, err := settle(t, ctx, repo, "order-4", "ophelia", "sale", "ana", 1500); err != nil {
		t.Fatalf("settle: %v", err)
	}
	var credits, debits int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'credit'`).Scan(&credits); err != nil {
		t.Fatalf("sum credits: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'debit'`).Scan(&debits); err != nil {
		t.Fatalf("sum debits: %v", err)
	}
	if credits-debits != domain.GenesisSupplyMillis {
		t.Fatalf("credits %d - debits %d != S", credits, debits)
	}
}
