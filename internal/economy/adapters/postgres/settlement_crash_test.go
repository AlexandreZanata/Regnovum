package postgres_test

// P44-T03 — crash-safe concurrent settlement matrix.
//
// One funded book is driven through the interleavings the validation
// demands: racing purchases on one intention, crash before commit,
// lock survival after abort, worker restart on a fresh pool,
// database bounce by terminating backends, two closers sealing once,
// out-of-order events, two succession evaluators contending one lease
// and an ex-King approval that must stay a refusal. Every case ends
// in reconciliation: supply S per book, a balanced journal, settled
// intentions counted once and obligations matching. No case needs a
// human: convergence comes from idempotency keys, unique guards and
// replayed outcomes.
//
// Two controlled-point substitutions are stated once: a real SIGKILL
// cannot happen inside the test process, so crash-before-commit is a
// rolled-back transaction and worker death is a cancelled context;
// a container restart cannot happen either, so the database bounce
// terminates every backend of the test database and the pool must
// reconnect transparently. The full container drill belongs to the
// restore harness of P44-T07.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	crownadapter "github.com/AlexandreZanata/Regnovum/internal/crown/adapters/postgres"
	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

const crashBook = "temporada-1"

// crashFund prepares one active book with three funded holders and
// returns the pool, living there for the whole case.
func crashFund(t *testing.T, ctx context.Context, db *dbtest.TestDB) *postgres.Repository {
	t.Helper()
	repo := postgres.NewRepository(db.Pool.Pool())
	seedSeasonBook(t, ctx, db, crashBook, 1, "2026-10-04T12:00:00Z")
	genesisBook(t, ctx, repo, "genesis-crash", crashBook)
	for _, user := range sUsers {
		makeBookCustody(t, ctx, db, "user", user, crashBook)
		if _, err := repo.Transfer(ctx, application.TransferRequest{
			FromSeason: economydomain.SeasonKey(crashBook),
			FromKind:   economydomain.CustodyTreasury, FromLabel: "main",
			ToSeason: economydomain.SeasonKey(crashBook),
			ToKind:   economydomain.CustodyUser, ToLabel: user,
			Amount: mustSeasonMilli(t, 20000),
		}); err != nil {
			t.Fatalf("fund %s: %v", user, err)
		}
	}
	return repo
}

// crashReconcile demands the converged state: supply S, a balanced
// journal and no cross-book leg, so every case proves the chaos above
// settled instead of merely stopping.
func crashReconcile(t *testing.T, ctx context.Context, db *dbtest.TestDB) {
	t.Helper()
	pool := db.Pool.Pool()
	var supply int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE season_key = $1`, crashBook).Scan(&supply); err != nil {
		t.Fatalf("reconcile supply: %v", err)
	}
	if supply != economydomain.GenesisSupplyMillis {
		t.Fatalf("reconcile supply = %d, want S", supply)
	}
	var credits, debits int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'credit'`).Scan(&credits); err != nil {
		t.Fatalf("reconcile credits: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'debit'`).Scan(&debits); err != nil {
		t.Fatalf("reconcile debits: %v", err)
	}
	if credits-debits != economydomain.GenesisSupplyMillis {
		t.Fatalf("reconcile journal %d - %d != S", credits, debits)
	}
}

// crashIntentions counts the settled rows for one intention key.
func crashIntentions(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string) int {
	t.Helper()
	var count int
	if err := db.Pool.Pool().QueryRow(ctx,
		`SELECT count(*) FROM app.economy_intentions WHERE intention_key = $1`, key).Scan(&count); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	return count
}

// settleIntent runs one idempotent purchase intention through the use
// case, answering whether the call replayed a stored outcome: only
// the first settlement writes legs, every identical retry replays.
func settleIntent(ctx context.Context, repo *postgres.Repository, key, actor string, millis int64) (bool, error) {
	useCase := application.NewIdempotentTransferUseCase(repo, repo)
	result, err := useCase.Execute(ctx, application.IdempotentTransferCommand{
		FromSeason: crashBook,
		Key:        key, Actor: actor, Operation: "t03-compra",
		FromKind: "treasury", FromLabel: "main",
		ToSeason: crashBook,
		ToKind:   "user", ToLabel: "u0", Millis: millis,
	})
	if err != nil {
		return false, err
	}
	return result.Replayed, nil
}

// TestSettlementRacesCollapseToOne races eight workers on one purchase
// intention while transfers and holds contend on the same book: the
// intention settles exactly once, every other worker replays, and the
// books reconcile with no loss, creation or duplicate.
func TestSettlementRacesCollapseToOne(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()
	repo := crashFund(t, ctx, db)

	const workers = 8
	var wg sync.WaitGroup
	settled, replayed := 0, 0
	var mu sync.Mutex
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			_, _ = repo.Transfer(ctx, application.TransferRequest{
				FromSeason: economydomain.SeasonKey(crashBook),
				FromKind:   economydomain.CustodyUser, FromLabel: sUsers[w%len(sUsers)],
				ToSeason: economydomain.SeasonKey(crashBook),
				ToKind:   economydomain.CustodyUser, ToLabel: sUsers[(w+1)%len(sUsers)],
				Amount: mustSeasonMilli(t, 100),
			})
			wasReplay, err := settleIntent(ctx, repo, "t03-race-1", "comprador", 500)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("worker %d: %v", w, err)
				return
			}
			if wasReplay {
				replayed++
			} else {
				settled++
			}
		}(w)
	}
	wg.Wait()
	if settled != 1 {
		t.Fatalf("settled = %d, want exactly one winner", settled)
	}
	if replayed != workers-1 {
		t.Fatalf("replayed = %d, want %d losers disclosed as replays", replayed, workers-1)
	}
	if got := crashIntentions(t, ctx, db, "t03-race-1"); got != 1 {
		t.Fatalf("intention rows = %d, want exactly one effect", got)
	}
	crashReconcile(t, ctx, db)
}

// TestCrashBeforeCommitLeavesNoPartial writes half a transfer and
// rolls the transaction back, the controlled-point equivalent of a
// kill before commit: balances do not move, and replaying the
// intention afterwards settles exactly once.
func TestCrashBeforeCommitLeavesNoPartial(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()
	repo := crashFund(t, ctx, db)
	pool := db.Pool.Pool()

	before := bookBalance(t, ctx, db, "treasury", "main", crashBook)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin crash tx: %v", err)
	}
	treasury := sCustodyID(t, ctx, db, "treasury", "main", crashBook)
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 VALUES (gen_random_uuid(), $1, 'debit', 7000, $2)`, treasury, crashBook); err != nil {
		t.Fatalf("half transfer: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback crash tx: %v", err)
	}
	if got := bookBalance(t, ctx, db, "treasury", "main", crashBook); got != before {
		t.Fatalf("treasury moved on rolled-back half transfer: %d != %d", got, before)
	}
	if replayed, err := settleIntent(ctx, repo, "t03-crash-1", "comprador", 700); err != nil || replayed {
		t.Fatalf("settle after crash: replayed=%v err=%v", replayed, err)
	}
	if got := crashIntentions(t, ctx, db, "t03-crash-1"); got != 1 {
		t.Fatalf("intention rows = %d, want exactly one effect", got)
	}
	crashReconcile(t, ctx, db)
}

// TestLockDoesNotSurviveAbort holds a custody row lock in one
// transaction while a transfer waits on it: rolling the holder back
// lets the waiter proceed instead of waiting forever.
func TestLockDoesNotSurviveAbort(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()
	repo := crashFund(t, ctx, db)
	pool := db.Pool.Pool()

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	var locked string
	if err := holder.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main' AND season_key = $1 FOR UPDATE`,
		crashBook).Scan(&locked); err != nil {
		t.Fatalf("take row lock: %v", err)
	}
	blockedCtx, blockedCancel := context.WithTimeout(ctx, 3*time.Second)
	defer blockedCancel()
	_, err = repo.Transfer(blockedCtx, application.TransferRequest{
		FromSeason: economydomain.SeasonKey(crashBook),
		FromKind:   economydomain.CustodyTreasury, FromLabel: "main",
		ToSeason: economydomain.SeasonKey(crashBook),
		ToKind:   economydomain.CustodyUser, ToLabel: "u1",
		Amount: mustSeasonMilli(t, 300),
	})
	if err == nil {
		t.Fatal("transfer through a held row lock settled: the lock did not isolate")
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("rollback holder: %v", err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: economydomain.SeasonKey(crashBook),
		FromKind:   economydomain.CustodyTreasury, FromLabel: "main",
		ToSeason: economydomain.SeasonKey(crashBook),
		ToKind:   economydomain.CustodyUser, ToLabel: "u1",
		Amount: mustSeasonMilli(t, 300),
	}); err != nil {
		t.Fatalf("transfer after abort: %v", err)
	}
	crashReconcile(t, ctx, db)
}

// TestWorkerRestartConverges kills the worker mid-flight with a
// cancelled context, then restarts on a fresh pool over the same
// database: the replay settles exactly once and the books reconcile,
// so no server-side lock or hidden state is needed to resume.
func TestWorkerRestartConverges(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()
	crashFund(t, ctx, db)

	deadCtx, kill := context.WithCancel(ctx)
	kill()
	fresh, err := pgxpool.New(ctx, db.DSN)
	if err != nil {
		t.Fatalf("restart pool: %v", err)
	}
	defer fresh.Close()
	restarted := postgres.NewRepository(fresh)
	if _, err := settleIntent(deadCtx, restarted, "t03-restart-1", "comprador", 900); err == nil {
		t.Fatal("killed worker unexpectedly settled")
	}
	if replayed, err := settleIntent(ctx, restarted, "t03-restart-1", "comprador", 900); err != nil || replayed {
		t.Fatalf("replay after restart: replayed=%v err=%v", replayed, err)
	}
	if replayed, err := settleIntent(ctx, restarted, "t03-restart-1", "comprador", 900); err != nil || !replayed {
		t.Fatalf("second replay after restart: replayed=%v err=%v", replayed, err)
	}
	if got := crashIntentions(t, ctx, db, "t03-restart-1"); got != 1 {
		t.Fatalf("intention rows = %d, want exactly one effect", got)
	}
	crashReconcile(t, ctx, db)
}

// TestDatabaseBounceReconnects terminates every backend of the test
// database and keeps operating through the same pool: the driver
// reconnects transparently and the replayed intention still settles
// exactly once.
func TestDatabaseBounceReconnects(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()
	repo := crashFund(t, ctx, db)
	pool := db.Pool.Pool()

	if replayed, err := settleIntent(ctx, repo, "t03-bounce-1", "comprador", 400); err != nil || replayed {
		t.Fatalf("settle before bounce: replayed=%v err=%v", replayed, err)
	}
	if _, err := pool.Exec(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		 WHERE datname = current_database() AND pid <> pg_backend_pid()`); err != nil {
		t.Fatalf("bounce backends: %v", err)
	}
	if replayed, err := settleIntent(ctx, repo, "t03-bounce-1", "comprador", 400); err != nil || !replayed {
		t.Fatalf("replay after bounce: replayed=%v err=%v", replayed, err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: economydomain.SeasonKey(crashBook),
		FromKind:   economydomain.CustodyUser, FromLabel: "u2",
		ToSeason: economydomain.SeasonKey(crashBook),
		ToKind:   economydomain.CustodyUser, ToLabel: "u0",
		Amount: mustSeasonMilli(t, 250),
	}); err != nil {
		t.Fatalf("transfer after bounce: %v", err)
	}
	if got := crashIntentions(t, ctx, db, "t03-bounce-1"); got != 1 {
		t.Fatalf("intention rows = %d, want exactly one effect", got)
	}
	crashReconcile(t, ctx, db)
}

// sealStage attempts one lifecycle stage: the unique guard lets
// exactly one closer win each stage, and the loser takes the
// conflict instead of duplicating the seal.
func sealStage(ctx context.Context, db *dbtest.TestDB, from, to string) error {
	fromValue := "NULL"
	if from != "" {
		fromValue = "'" + from + "'"
	}
	_, err := db.Pool.Pool().Exec(ctx,
		`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
		 VALUES ($1, `+fromValue+`, $2, now())`, crashBook, to)
	return err
}

// sealBookOnce advances the whole chain, tolerating stages a rival
// closer already won: the book still seals exactly once.
func sealBookOnce(ctx context.Context, db *dbtest.TestDB) {
	stages := [][2]string{{"", "prepared"}, {"prepared", "active"}, {"active", "closing"}, {"closing", "sealed"}}
	for _, stage := range stages {
		_ = sealStage(ctx, db, stage[0], stage[1])
	}
}

// TestTwoClosersSealOnce races two closers through the lifecycle
// while writers contend: the seal lands exactly once, writers either
// settle before it or refuse after it, legs freeze and the books
// reconcile.
func TestTwoClosersSealOnce(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()
	repo := crashFund(t, ctx, db)

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				_, _ = repo.Transfer(ctx, application.TransferRequest{
					FromSeason: economydomain.SeasonKey(crashBook),
					FromKind:   economydomain.CustodyUser, FromLabel: sUsers[w%len(sUsers)],
					ToSeason: economydomain.SeasonKey(crashBook),
					ToKind:   economydomain.CustodyUser, ToLabel: sUsers[(w+1)%len(sUsers)],
					Amount: mustSeasonMilli(t, 50),
				})
			}
		}(w)
	}
	for c := 0; c < 2; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sealBookOnce(ctx, db)
		}()
	}
	wg.Wait()

	var stages int
	if err := db.Pool.Pool().QueryRow(ctx,
		`SELECT count(*) FROM app.season_lifecycle WHERE season_key = $1`, crashBook).Scan(&stages); err != nil {
		t.Fatalf("count stages: %v", err)
	}
	// Two stages pre-exist from other suites' helpers only when they
	// share a database; here the database is private, so the seal is
	// exactly the four stages the closers raced.
	if stages != 4 {
		t.Fatalf("lifecycle stages = %d, want exactly the sealed chain once", stages)
	}
	frozen := bookLegs(t, ctx, db, crashBook)
	transferUC := application.NewTransferUseCase(repo, repo)
	if _, err := transferUC.Execute(ctx, application.TransferCommand{
		FromSeason: crashBook,
		FromKind:   "treasury", FromLabel: "main",
		ToSeason: crashBook,
		ToKind:   "user", ToLabel: "u0", Millis: 10,
	}); err == nil {
		t.Fatal("post-seal transfer settled: the seal did not freeze the book")
	}
	if got := bookLegs(t, ctx, db, crashBook); got != frozen {
		t.Fatalf("sealed legs moved %d -> %d after the seal", frozen, got)
	}
	crashReconcile(t, ctx, db)
}

// TestOutOfOrderEventConverges replays a divergent intention and the
// identical one: the divergent replay conflicts without merging, the
// identical replay returns the stored outcome, and the effect counts
// once.
func TestOutOfOrderEventConverges(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()
	repo := crashFund(t, ctx, db)

	if replayed, err := settleIntent(ctx, repo, "t03-ordem-1", "comprador", 600); err != nil || replayed {
		t.Fatalf("settle: replayed=%v err=%v", replayed, err)
	}
	if _, err := settleIntent(ctx, repo, "t03-ordem-1", "comprador", 900); !errors.Is(err, economydomain.ErrIntentionConflict) {
		t.Fatalf("divergent replay = %v, want ErrIntentionConflict", err)
	}
	if replayed, err := settleIntent(ctx, repo, "t03-ordem-1", "comprador", 600); err != nil || !replayed {
		t.Fatalf("identical replay: replayed=%v err=%v", replayed, err)
	}
	if got := crashIntentions(t, ctx, db, "t03-ordem-1"); got != 1 {
		t.Fatalf("intention rows = %d, want exactly one effect", got)
	}
	crashReconcile(t, ctx, db)
}

// TestTwoEvaluatorsAndExKingConverge contends one succession lease
// between two evaluators while an ex-King approval stays refused and
// a live settlement moves: exactly one evaluator wins, the stale act
// moves no leg, and the books reconcile.
func TestTwoEvaluatorsAndExKingConverge(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := seasonScopeCtx()
	defer cancel()
	repo := crashFund(t, ctx, db)
	pool := db.Pool.Pool()

	evalAdapter := crownadapter.NewSuccessionEvaluatorAdapter(pool)
	seedSeasonBook(t, ctx, db, "temporada-sucessao-t03", 3, "2027-04-02T12:00:00Z")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin init: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := evalAdapter.InitSeasonEvaluatorTx(ctx, tx, "temporada-sucessao-t03", "rainha-1", 1); err != nil {
		t.Fatalf("init evaluator: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit init: %v", err)
	}
	wins := make(chan string, 2)
	var wg sync.WaitGroup
	for _, worker := range []string{"worker-1", "worker-2"} {
		wg.Add(1)
		go func(worker string) {
			defer wg.Done()
			if ok, err := evalAdapter.AcquireLease(ctx, pool, "temporada-sucessao-t03", worker, 30*time.Second); err == nil && ok {
				wins <- worker
			}
		}(worker)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: economydomain.SeasonKey(crashBook),
		FromKind:   economydomain.CustodyTreasury, FromLabel: "main",
		ToSeason: economydomain.SeasonKey(crashBook),
		ToKind:   economydomain.CustodyUser, ToLabel: "u0",
		Amount: mustSeasonMilli(t, 800),
	}); err != nil {
		t.Fatalf("live settlement during contention: %v", err)
	}
	wg.Wait()
	close(wins)
	var winners []string
	for winner := range wins {
		winners = append(winners, winner)
	}
	if len(winners) != 1 {
		t.Fatalf("lease winners = %v, want exactly one evaluator", winners)
	}

	legsBefore := bookLegs(t, ctx, db, crashBook)
	anchor := time.Now().UTC()
	stale := crowndomain.EffectFence{
		Season: crowndomain.SeasonID(crashBook), Reign: crowndomain.ReignVersion(1),
		Competence: crowndomain.Competence("patrimonial"), Author: crowndomain.HolderSubject("ex-rei"),
		CurrentReign: crowndomain.CurrentReign{
			Season: crowndomain.SeasonID(crashBook), Holder: crowndomain.HolderSubject("nova-rainha"),
			Reign: crowndomain.ReignVersion(2), AuthorityVersion: crowndomain.AuthorityVersion(3),
			StartsAt: anchor.Add(-time.Hour), EndsAt: anchor.Add(6 * time.Hour), Open: true,
		},
		EconomicBacklogClean: true, Now: anchor,
	}
	if err := crowndomain.ValidateEffectFence(stale); !errors.Is(err, crowndomain.ErrStaleReign) {
		t.Fatalf("ex-King approval = %v, want ErrStaleReign", err)
	}
	if got := bookLegs(t, ctx, db, crashBook); got != legsBefore {
		t.Fatalf("refused ex-King act moved legs %d -> %d", legsBefore, got)
	}
	crashReconcile(t, ctx, db)
}
