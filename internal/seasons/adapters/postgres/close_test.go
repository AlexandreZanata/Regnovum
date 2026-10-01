package postgres_test

// P46-T09 — barreira transacional e fechamento recuperável.
//
// O Closer registra a barreira no relógio do banco exatamente uma
// vez sob concorrência, drena em lotes idempotentes com checkpoint
// (holds liberados uma vez via ReleaseForClose fenced, escrows
// classificados sem efeito ou bloqueados), recusa o selo com
// custódia preservada diante de obrigação aberta e conserva o
// snapshot. Sem wiring: o Worker compõe store e ports injetados, e o
// produto econômico segue desativado.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	commercepg "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/postgres"
	commerceapp "github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	economyapp "github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	closepg "github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/postgres"
	closeapp "github.com/AlexandreZanata/Regnovum/internal/seasons/application"
	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

const (
	closePolicyHash   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	closeManifestHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func closeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 90*time.Second)
}

type fixedCloseClock struct{ now time.Time }

func (c fixedCloseClock) Now() time.Time { return c.now }

// seedCloseSeason inserts one season row starting at the given
// instant with the exact ninety-day window, returning its bounds.
func seedCloseSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int, starts time.Time) (time.Time, time.Time) {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO app.seasons
		 (season_key, ordinal, starts_at, ends_at, charter_version, policy_ref, initial_monarch, regent, manifest_hash)
		 VALUES ($1, $2, $3::timestamptz, $3::timestamptz + make_interval(secs => 7776000), 'v3', 'politica-inicial', 'fundadora', 'regente-tecnica', $4)`,
		key, ordinal, starts.UTC().Format(time.RFC3339), closeManifestHash); err != nil {
		t.Fatalf("seed season %s: %v", key, err)
	}
	var from, to time.Time
	if err := db.QueryRow(ctx, `SELECT starts_at, ends_at FROM app.seasons WHERE season_key = $1`, key).Scan(&from, &to); err != nil {
		t.Fatalf("read season window: %v", err)
	}
	return from.UTC(), to.UTC()
}

// endedCloseSeason seeds a book whose end already passed on the
// wall clock: the cutoff is reached, only the barrier is missing.
func endedCloseSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string, ordinal int) (time.Time, time.Time) {
	t.Helper()
	return seedCloseSeason(t, ctx, db, key, ordinal, time.Now().UTC().Add(-91*24*time.Hour).Truncate(time.Second))
}

func activateCloseSeason(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string) {
	t.Helper()
	for _, stage := range [][2]string{{"NULL", "prepared"}, {"prepared", "active"}} {
		from := "NULL"
		if stage[0] != "NULL" {
			from = "'" + stage[0] + "'"
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO app.season_lifecycle (season_key, from_state, to_state, decided_at)
			 VALUES ($1, `+from+`, $2, now())`, key, stage[1]); err != nil {
			t.Fatalf("stage %s of %s: %v", stage[1], key, err)
		}
	}
}

func closeAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('fecho-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return id
}

// fundCloseHolder runs Genesis once per book and funds one user
// custody: the admitted pre-barrier effect the drain must include
// exactly once.
func fundCloseHolder(t *testing.T, ctx context.Context, db *dbtest.TestDB, account, season string, funds int64) {
	t.Helper()
	repo := economypg.NewRepository(db.Pool.Pool())
	genKey, err := economydomain.ParseGenesisKey("fecho-genesis-" + season)
	if err != nil {
		t.Fatalf("ParseGenesisKey: %v", err)
	}
	if _, err := repo.RunGenesis(ctx, economyapp.GenesisRequest{Key: genKey, Season: economydomain.SeasonKey(season)}); err != nil {
		if !errors.Is(err, economydomain.ErrGenesisAlreadyExists) {
			t.Fatalf("genesis %s: %v", season, err)
		}
	}
	amount, err := economydomain.NewMilliInk(funds)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('treasury', 'main', $1) ON CONFLICT DO NOTHING`, season); err != nil {
		t.Fatalf("ensure treasury: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', $1, $2) ON CONFLICT DO NOTHING`, account, season); err != nil {
		t.Fatalf("ensure holder custody: %v", err)
	}
	fromKind, _ := economydomain.ParseCustodyKind("treasury")
	toKind, _ := economydomain.ParseCustodyKind("user")
	if _, err := repo.Transfer(ctx, economyapp.TransferRequest{
		FromSeason: economydomain.SeasonKey(season), FromKind: fromKind, FromLabel: "main",
		ToSeason: economydomain.SeasonKey(season), ToKind: toKind, ToLabel: account, Amount: amount,
	}); err != nil {
		t.Fatalf("fund %s in %s: %v", account, season, err)
	}
}

// reserveCloseHold locks holder funds through the real path while
// the book is active.
func reserveCloseHold(t *testing.T, ctx context.Context, db *dbtest.TestDB, owner, season string, millis int64) string {
	t.Helper()
	repo := economypg.NewRepository(db.Pool.Pool())
	expires := time.Now().UTC().Add(30 * 24 * time.Hour)
	useCase := economyapp.NewReserveUseCase(repo, fixedCloseClock{now: expires.Add(-time.Hour)}, repo)
	view, err := useCase.Execute(ctx, economyapp.ReserveCommand{
		Season: season, OwnerKind: "user", OwnerLabel: owner, Purpose: "hold for test",
		Millis: millis, ExpiresAt: expires,
	})
	if err != nil {
		t.Fatalf("reserve hold: %v", err)
	}
	return view.HoldID
}

// escrowFundOrder bundles one direct escrow funding: commerce fund
// use cases refuse past wall windows, so ended books seed the
// contract and its fund legs directly with conserved amounts.
type escrowFundOrder struct {
	key      string
	buyer    string
	provider string
	amount   int64
	posted   time.Time
	season   string
	ends     time.Time
}

// seedFundedEscrow inserts one funded escrow with terminal terms and
// conserved fund legs: buyer debit, escrow credit, same transfer.
func seedFundedEscrow(t *testing.T, ctx context.Context, db *dbtest.TestDB, order escrowFundOrder) {
	t.Helper()
	escrowTransfer := newCloseUUID(t, ctx, db)
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('escrow', $1, $2) ON CONFLICT DO NOTHING`,
		"fecho-"+order.key, order.season); err != nil {
		t.Fatalf("provision escrow custody: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO app.commerce_contracts
		 (contract_key, kind, buyer_id, provider_id, object, amount_milli, terms_hash,
		  escrow_transfer_id, expires_at, posted_at, season_key,
		  terminal_policy_ref, terminal_policy_hash, buyer_accept_ref, provider_accept_ref)
		 VALUES ($1, 'trade', $2::uuid, $3::uuid, 'revisao de fecho', $4, 'terms-seal',
		  $5::uuid, $6, $7, $8, 'terminal-v1', $9, 'aceite-comprador-1', 'aceite-prestador-1')`,
		order.key, order.buyer, order.provider, order.amount, escrowTransfer,
		order.ends.Add(-time.Hour), order.posted, order.season, closePolicyHash); err != nil {
		t.Fatalf("seed contract: %v", err)
	}
	closeLeg(t, ctx, db, escrowTransfer, closeCustodyID(t, ctx, db, "user", order.buyer, order.season), "debit", order.amount, order.season)
	closeLeg(t, ctx, db, escrowTransfer, closeCustodyID(t, ctx, db, "escrow", "fecho-"+order.key, order.season), "credit", order.amount, order.season)
}

// seedReleasedEscrow appends acceptance and provider release to one
// funded escrow with the floor tithe split, mirroring the family
// payout math without reusing its gate.
func seedReleasedEscrow(t *testing.T, ctx context.Context, db *dbtest.TestDB, order escrowFundOrder) {
	t.Helper()
	seedFundedEscrow(t, ctx, db, order)
	var contract string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM app.commerce_contracts WHERE contract_key = $1 AND buyer_id = $2::uuid`,
		order.key, order.buyer).Scan(&contract); err != nil {
		t.Fatalf("read contract: %v", err)
	}
	transfer := newCloseUUID(t, ctx, db)
	if _, err := db.Exec(ctx,
		`INSERT INTO app.commerce_settlements (contract_id, action, transfer_id, posted_at)
		 VALUES ($1::uuid, 'accept', NULL, $2), ($1::uuid, 'release', $3::uuid, $4)`,
		contract, order.posted.Add(time.Minute), transfer, order.posted.Add(2*time.Minute)); err != nil {
		t.Fatalf("seed settlements: %v", err)
	}
	tithe := order.amount / 10
	closeLeg(t, ctx, db, transfer, closeCustodyID(t, ctx, db, "escrow", "fecho-"+order.key, order.season), "debit", order.amount, order.season)
	closeLeg(t, ctx, db, transfer, closeCustodyID(t, ctx, db, "user", order.provider, order.season), "credit", order.amount-tithe, order.season)
	closeLeg(t, ctx, db, transfer, closeCustodyID(t, ctx, db, "treasury", "main", order.season), "credit", tithe, order.season)
}

func newCloseUUID(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("mint uuid: %v", err)
	}
	return id
}

func closeCustodyID(t *testing.T, ctx context.Context, db *dbtest.TestDB, kind, label, season string) string {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		kind, label, season); err != nil {
		t.Fatalf("provision custody: %v", err)
	}
	var id string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2 AND season_key = $3`,
		kind, label, season).Scan(&id); err != nil {
		t.Fatalf("read custody: %v", err)
	}
	return id
}

func closeLeg(t *testing.T, ctx context.Context, db *dbtest.TestDB, transfer, custody, direction string, amount int64, season string) {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5)`, transfer, custody, direction, amount, season); err != nil {
		t.Fatalf("record leg: %v", err)
	}
}

// closeSums reads the conservation sentinels of one book: the
// Genesis S, the signed leg total (S by single-sided mint, stable by
// double entry after) and the row counts the seal compares.
func closeSums(t *testing.T, ctx context.Context, db *dbtest.TestDB, season string) (genesis, signed, legs, intentions int64) {
	t.Helper()
	if err := db.QueryRow(ctx,
		`SELECT COALESCE((SELECT amount_milli FROM app.economy_genesis WHERE season_key = $1), 0)`, season).Scan(&genesis); err != nil {
		t.Fatalf("read genesis: %v", err)
	}
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE season_key = $1`, season).Scan(&signed); err != nil {
		t.Fatalf("read signed total: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries WHERE season_key = $1`, season).Scan(&legs); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_intentions WHERE season_key = $1`, season).Scan(&intentions); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	return genesis, signed, legs, intentions
}

func closeWorker(db *dbtest.TestDB, limit int) (*closeapp.Worker, *economypg.Repository) {
	economyRepo := economypg.NewRepository(db.Pool.Pool())
	closer, err := closepg.NewCloser(db.Pool.Pool())
	if err != nil {
		panic(err)
	}
	release := func(ctx context.Context, holdID string, drain seasondomain.CloseDrain) (bool, error) {
		return economyRepo.ReleaseForClose(ctx, holdID, drain)
	}
	return &closeapp.Worker{Store: closer, ReleaseHold: release, BatchLimit: limit}, economyRepo
}

// TestCloseBarrierAdmitsOneRun proves two closers open exactly one
// barrier in generation 1 with the cutoff at or past the book end.
func TestCloseBarrierAdmitsOneRun(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := closeCtx()
	defer cancel()

	_, ends := endedCloseSeason(t, ctx, db, "temporada-fecho-a", 91)
	holder := closeAccount(t, ctx, db)
	fundCloseHolder(t, ctx, db, holder, "temporada-fecho-a", 100000)
	activateCloseSeason(t, ctx, db, "temporada-fecho-a")

	worker, _ := closeWorker(db, 10)
	first, second := make(chan error, 1), make(chan error, 1)
	var one, two closeapp.CloseRunView
	go func() {
		var err error
		one, err = worker.Begin(ctx, "temporada-fecho-a", "fechador-1", time.Minute)
		first <- err
	}()
	go func() {
		var err error
		two, err = worker.Begin(ctx, "temporada-fecho-a", "fechador-1", time.Minute)
		second <- err
	}()
	if err := <-first; err != nil {
		t.Fatalf("first closer: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second closer: %v", err)
	}
	if one.Generation != 1 || two.Generation != 1 || one.CutoffAt.IsZero() {
		t.Fatalf("barrier = %+v / %+v, want one generation-1 run", one, two)
	}
	if one.CutoffAt.Before(ends) {
		t.Fatalf("cutoff %v before book end %v: the barrier observes the end", one.CutoffAt, ends)
	}
	var closings, runs int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.season_lifecycle WHERE season_key = $1 AND to_state = 'closing'`, "temporada-fecho-a").Scan(&closings); err != nil {
		t.Fatalf("count closing: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.season_close_runs WHERE season_key = $1`, "temporada-fecho-a").Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if closings != 1 || runs != 1 {
		t.Fatalf("closing rows = %d, runs = %d, want exactly one barrier", closings, runs)
	}
	if one.SnapshotMilli != 2100000000000 {
		t.Fatalf("snapshot S = %d, want the book Genesis 2100000000000", one.SnapshotMilli)
	}
}

// TestCloseRefusesEarlyAndNormalEntry proves the cutoff is
// observed and normal entry stops at the barrier even with the
// worker off: no drain ran here.
func TestCloseRefusesEarlyAndNormalEntry(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := closeCtx()
	defer cancel()

	liveStarts := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	seedCloseSeason(t, ctx, db, "temporada-fecho-viva", 92, liveStarts)
	worker, _ := closeWorker(db, 10)
	if _, err := worker.Begin(ctx, "temporada-fecho-viva", "fechador-1", time.Minute); !errors.Is(err, seasondomain.ErrCloseNotDue) {
		t.Fatalf("early barrier = %v, want ErrCloseNotDue", err)
	}

	endedCloseSeason(t, ctx, db, "temporada-fecho-b", 93)
	buyer := closeAccount(t, ctx, db)
	provider := closeAccount(t, ctx, db)
	fundCloseHolder(t, ctx, db, buyer, "temporada-fecho-b", 100000)
	activateCloseSeason(t, ctx, db, "temporada-fecho-b")
	if _, err := worker.Begin(ctx, "temporada-fecho-b", "fechador-1", time.Minute); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// Normal entry stops at the barrier even with the worker off: no
	// drain ran here. The economy transfer carries no wall-window of
	// its own, so its refusal names the barrier gate exactly.
	economyRepo := economypg.NewRepository(db.Pool.Pool())
	amount, err := economydomain.NewMilliInk(1000)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	fromKind, _ := economydomain.ParseCustodyKind("user")
	toKind, _ := economydomain.ParseCustodyKind("user")
	if _, err := economyRepo.Transfer(ctx, economyapp.TransferRequest{
		FromSeason: economydomain.SeasonKey("temporada-fecho-b"), FromKind: fromKind, FromLabel: buyer,
		ToSeason: economydomain.SeasonKey("temporada-fecho-b"), ToKind: toKind, ToLabel: provider, Amount: amount,
	}); !errors.Is(err, economydomain.ErrBookSealed) {
		t.Fatalf("post-barrier transfer = %v, want ErrBookSealed", err)
	}
	// The commerce fund path refuses too (its wall window already
	// lapsed alongside the barrier): zero contracts either way.
	escrows, err := commercepg.NewEscrowRepository(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewEscrowRepository: %v", err)
	}
	fundUC, _ := commerceapp.NewFundContractUseCase(escrows)
	var seasonEnds time.Time
	if err := db.QueryRow(ctx, `SELECT ends_at FROM app.seasons WHERE season_key = $1`, "temporada-fecho-b").Scan(&seasonEnds); err != nil {
		t.Fatalf("read ends: %v", err)
	}
	_, fundErr := fundUC.Execute(ctx, commerceapp.FundCommand{
		Key: "fecho-tardio", Object: "revisao de fecho", Buyer: buyer, Provider: provider,
		AmountMill: 5000, ExpiresAt: seasonEnds.Add(-time.Hour), Now: seasonEnds.Add(-2 * time.Hour),
		Season: "temporada-fecho-b", PolicyRef: "terminal-v1", PolicyHash: closePolicyHash,
		BuyerAccept: "aceite-comprador-1", ProviderAccept: "aceite-prestador-1",
		SeasonEndsAt: seasonEnds, ResetAcknowledged: true,
	})
	if fundErr == nil {
		t.Fatal("post-barrier fund passed: the barrier stops normal entry")
	}
	var contracts int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.commerce_contracts WHERE season_key = $1`, "temporada-fecho-b").Scan(&contracts); err != nil {
		t.Fatalf("count contracts: %v", err)
	}
	if contracts != 0 {
		t.Fatalf("contracts = %d after barrier, want zero: normal entry stops even with the worker off", contracts)
	}
}

// holdCustodyTransfers counts the distinct journal transfers touching
// one hold custody: reservation moves in once, release moves out
// once, never twice.
func holdCustodyTransfers(t *testing.T, ctx context.Context, db *dbtest.TestDB, holdID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx,
		`SELECT count(DISTINCT e.transfer_id) FROM app.economy_entries e
		 JOIN app.economy_holds h ON h.hold_custody_id = e.custody_id
		 WHERE h.id = $1::uuid`, holdID).Scan(&count); err != nil {
		t.Fatalf("count hold transfers: %v", err)
	}
	return count
}

// TestCloseDrainCrashResumeReleasesOnce proves crash recovery and
// exactly-once holds: a cancelled batch commits nothing, resume
// continues past the cursor, two closers release one hold once, and
// the admitted transfer lands exactly once with S conserved.
func TestCloseDrainCrashResumeReleasesOnce(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := closeCtx()
	defer cancel()

	_, ends := endedCloseSeason(t, ctx, db, "temporada-fecho-c", 95)
	endedCloseSeason(t, ctx, db, "temporada-fecho-outra", 96)
	otherHolder := closeAccount(t, ctx, db)
	fundCloseHolder(t, ctx, db, otherHolder, "temporada-fecho-outra", 50000)
	otherHold := reserveCloseHold(t, ctx, db, otherHolder, "temporada-fecho-outra", 7000)
	otherBuyer := closeAccount(t, ctx, db)
	otherProvider := closeAccount(t, ctx, db)
	seedFundedEscrow(t, ctx, db, escrowFundOrder{
		key: "outra-1", buyer: otherBuyer, provider: otherProvider,
		amount: 9000, posted: ends.Add(-3 * time.Hour), season: "temporada-fecho-outra", ends: ends,
	})
	otherGenesis, _, otherLegs, _ := closeSums(t, ctx, db, "temporada-fecho-outra")

	holder := closeAccount(t, ctx, db)
	provider := closeAccount(t, ctx, db)
	fundCloseHolder(t, ctx, db, holder, "temporada-fecho-c", 100000)
	activateCloseSeason(t, ctx, db, "temporada-fecho-c")
	holdA := reserveCloseHold(t, ctx, db, holder, "temporada-fecho-c", 8000)
	holdB := reserveCloseHold(t, ctx, db, holder, "temporada-fecho-c", 6000)
	seedFundedEscrow(t, ctx, db, escrowFundOrder{
		key: "fecho-1", buyer: holder, provider: provider,
		amount: 20000, posted: ends.Add(-2 * time.Hour), season: "temporada-fecho-c", ends: ends,
	})
	genesisBefore, signedBefore, legsBefore, _ := closeSums(t, ctx, db, "temporada-fecho-c")
	if signedBefore != genesisBefore {
		t.Fatalf("signed total = %d, want Genesis S %d by mint plus double entry", signedBefore, genesisBefore)
	}

	worker, economyRepo := closeWorker(db, 1)
	opened, err := worker.Begin(ctx, "temporada-fecho-c", "fechador-1", time.Minute)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	drain := seasondomain.CloseDrain{Season: "temporada-fecho-c", Generation: opened.Generation, Owner: "fechador-1"}

	// Crash before commit: a cancelled batch moves nothing, the
	// cursor stays empty.
	dead, stop := context.WithCancel(context.Background())
	stop()
	if _, err := worker.Store.DrainBatch(dead, closeapp.DrainRequest{
		Season: "temporada-fecho-c", Generation: opened.Generation, Owner: "fechador-1", Limit: 10,
	}, nil); err == nil {
		t.Fatal("cancelled batch passed: crash before commit leaves zero effect")
	}
	loaded, err := worker.Store.Load(ctx, "temporada-fecho-c")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.LastHoldKey != "" || loaded.Drained != 0 || loaded.Blocked != 0 {
		t.Fatalf("cursor = %+v after crash, want empty", loaded)
	}

	// Two closers on one hold release exactly once: the loser replays
	// settled.
	type raceResult struct {
		released bool
		err      error
	}
	race := func(out chan<- raceResult) {
		ok, err := economyRepo.ReleaseForClose(ctx, holdA, drain)
		out <- raceResult{released: ok, err: err}
	}
	first, second := make(chan raceResult, 1), make(chan raceResult, 1)
	go race(first)
	go race(second)
	resA, resB := <-first, <-second
	if resA.err != nil || resB.err != nil {
		t.Fatalf("hold race errors: %v / %v", resA.err, resB.err)
	}
	if resA.released == resB.released {
		t.Fatal("hold race released zero or two effects, want exactly one")
	}
	if n := holdCustodyTransfers(t, ctx, db, holdA); n != 2 {
		t.Fatalf("hold transfers = %d, want reservation plus exactly one release", n)
	}

	// Crash after commit resumes past the cursor: batches of one
	// drain hold A (replayed settled), hold B, then the escrow.
	for i := 0; i < 5; i++ {
		batch, err := worker.DrainOnce(ctx, opened)
		if err != nil {
			t.Fatalf("DrainOnce: %v", err)
		}
		if batch.HoldsDone && batch.EscrowsDone {
			break
		}
	}
	for _, hold := range []string{holdA, holdB} {
		var status string
		if err := db.QueryRow(ctx, `SELECT status FROM app.economy_holds WHERE id = $1::uuid`, hold).Scan(&status); err != nil {
			t.Fatalf("read hold: %v", err)
		}
		if status != "released" {
			t.Fatalf("hold %s = %q, want released", hold, status)
		}
		if n := holdCustodyTransfers(t, ctx, db, hold); n != 2 {
			t.Fatalf("hold %s transfers = %d, want exactly one release", hold, n)
		}
	}
	// The funded escrow stays funded with its fund legs exactly once:
	// admitted before the barrier, included once, never duplicated.
	var fundLegs int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.economy_entries e
		 JOIN app.commerce_contracts c ON c.escrow_transfer_id = e.transfer_id
		 WHERE c.contract_key = $1`, "fecho-1").Scan(&fundLegs); err != nil {
		t.Fatalf("count fund legs: %v", err)
	}
	if fundLegs != 2 {
		t.Fatalf("fund legs = %d, want buyer debit plus escrow credit once", fundLegs)
	}
	genesisAfter, signedAfter, legsAfter, _ := closeSums(t, ctx, db, "temporada-fecho-c")
	if genesisAfter != genesisBefore || signedAfter != genesisAfter || legsAfter <= legsBefore {
		t.Fatalf("sums moved unexpectedly: genesis %d->%d signed %d legs %d->%d",
			genesisBefore, genesisAfter, signedAfter, legsBefore, legsAfter)
	}
	// Zero cross-book effect: the other book is byte-identical.
	afterGenesis, _, afterLegs, _ := closeSums(t, ctx, db, "temporada-fecho-outra")
	if afterGenesis != otherGenesis || afterLegs != otherLegs {
		t.Fatalf("other book moved: genesis %d->%d legs %d->%d", otherGenesis, afterGenesis, otherLegs, afterLegs)
	}
	var otherStatus string
	if err := db.QueryRow(ctx, `SELECT status FROM app.economy_holds WHERE id = $1::uuid`, otherHold).Scan(&otherStatus); err != nil {
		t.Fatalf("read other hold: %v", err)
	}
	if otherStatus != "active" {
		t.Fatalf("other hold = %q, want untouched active", otherStatus)
	}
}

// closeBalances snapshots every custody balance of one book for
// preservation proofs.
func closeBalances(t *testing.T, ctx context.Context, db *dbtest.TestDB, season string) map[string]int64 {
	t.Helper()
	rows, err := db.Query(ctx,
		`SELECT c.kind || '/' || c.label,
		        COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_custodies c LEFT JOIN app.economy_entries e ON e.custody_id = c.id
		 WHERE c.season_key = $1 GROUP BY c.kind, c.label`, season)
	if err != nil {
		t.Fatalf("read balances: %v", err)
	}
	defer rows.Close()
	balances := map[string]int64{}
	for rows.Next() {
		var name string
		var total int64
		if err := rows.Scan(&name, &total); err != nil {
			t.Fatalf("scan balance: %v", err)
		}
		balances[name] = total
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read balances: %v", err)
	}
	return balances
}

func balancesEqual(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for key, total := range a {
		if b[key] != total {
			return false
		}
	}
	return true
}

// TestCloseSealBlockedPreservesCustody proves indecision blocks the
// seal: funded and accepted escrows keep custody, the barrier writes
// zero legs, no seal row appears and no successor opens.
func TestCloseSealBlockedPreservesCustody(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := closeCtx()
	defer cancel()

	_, ends := endedCloseSeason(t, ctx, db, "temporada-fecho-d", 97)
	buyer := closeAccount(t, ctx, db)
	provider := closeAccount(t, ctx, db)
	fundCloseHolder(t, ctx, db, buyer, "temporada-fecho-d", 100000)
	activateCloseSeason(t, ctx, db, "temporada-fecho-d")
	seedFundedEscrow(t, ctx, db, escrowFundOrder{
		key: "bloqueio-1", buyer: buyer, provider: provider,
		amount: 20000, posted: ends.Add(-2 * time.Hour), season: "temporada-fecho-d", ends: ends,
	})
	seedFundedEscrow(t, ctx, db, escrowFundOrder{
		key: "bloqueio-2", buyer: buyer, provider: provider,
		amount: 15000, posted: ends.Add(-time.Hour), season: "temporada-fecho-d", ends: ends,
	})
	var second string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM app.commerce_contracts WHERE contract_key = $1`, "bloqueio-2").Scan(&second); err != nil {
		t.Fatalf("read contract: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO app.commerce_settlements (contract_id, action, transfer_id, posted_at)
		 VALUES ($1::uuid, 'accept', NULL, $2)`, second, ends.Add(-30*time.Minute)); err != nil {
		t.Fatalf("seed acceptance: %v", err)
	}
	before := closeBalances(t, ctx, db, "temporada-fecho-d")
	var legsBefore int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries WHERE season_key = $1`, "temporada-fecho-d").Scan(&legsBefore); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	var seasonsBefore int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.seasons`).Scan(&seasonsBefore); err != nil {
		t.Fatalf("count seasons: %v", err)
	}

	worker, _ := closeWorker(db, 10)
	opened, err := worker.Begin(ctx, "temporada-fecho-d", "fechador-1", time.Minute)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := 0; i < 3; i++ {
		batch, err := worker.DrainOnce(ctx, opened)
		if err != nil {
			t.Fatalf("DrainOnce: %v", err)
		}
		if batch.HoldsDone && batch.EscrowsDone {
			break
		}
	}
	if _, err := worker.Seal(ctx, opened); !errors.Is(err, seasondomain.ErrCloseBlocked) {
		t.Fatalf("blocked seal = %v, want ErrCloseBlocked", err)
	}
	if after := closeBalances(t, ctx, db, "temporada-fecho-d"); !balancesEqual(before, after) {
		t.Fatalf("balances moved under BLOCKED: %+v vs %+v", before, after)
	}
	var legsAfter, sealed, seasonsAfter int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries WHERE season_key = $1`, "temporada-fecho-d").Scan(&legsAfter); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.season_lifecycle WHERE season_key = $1 AND to_state = 'sealed'`, "temporada-fecho-d").Scan(&sealed); err != nil {
		t.Fatalf("count seals: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.seasons`).Scan(&seasonsAfter); err != nil {
		t.Fatalf("count seasons: %v", err)
	}
	if legsAfter != legsBefore || sealed != 0 || seasonsAfter != seasonsBefore {
		t.Fatalf("legs %d->%d sealed %d seasons %d->%d: BLOCKED moves nothing and opens nothing",
			legsBefore, legsAfter, sealed, seasonsBefore, seasonsAfter)
	}
}

// TestCloseSealCleanBook proves the seal lands on a conserved book:
// terminal receipts stay, S reads equal, exactly one sealed row
// appears and a second seal replays it.
func TestCloseSealCleanBook(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := closeCtx()
	defer cancel()

	_, ends := endedCloseSeason(t, ctx, db, "temporada-fecho-e", 98)
	buyer := closeAccount(t, ctx, db)
	provider := closeAccount(t, ctx, db)
	fundCloseHolder(t, ctx, db, buyer, "temporada-fecho-e", 100000)
	activateCloseSeason(t, ctx, db, "temporada-fecho-e")
	hold := reserveCloseHold(t, ctx, db, buyer, "temporada-fecho-e", 5000)
	economyRepo := economypg.NewRepository(db.Pool.Pool())
	if _, err := economyRepo.Release(ctx, hold); err != nil {
		t.Fatalf("release hold pre-close: %v", err)
	}
	seedReleasedEscrow(t, ctx, db, escrowFundOrder{
		key: "limpo-1", buyer: buyer, provider: provider,
		amount: 20000, posted: ends.Add(-2 * time.Hour), season: "temporada-fecho-e", ends: ends,
	})
	genesisBefore, _, _, _ := closeSums(t, ctx, db, "temporada-fecho-e")

	worker, _ := closeWorker(db, 10)
	sealed, err := worker.Run(ctx, "temporada-fecho-e", "fechador-1", time.Minute)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sealed.Generation != 1 {
		t.Fatalf("seal = %+v, want generation 1", sealed)
	}
	genesisAfter, signedAfter, _, _ := closeSums(t, ctx, db, "temporada-fecho-e")
	if genesisAfter != genesisBefore || signedAfter != genesisAfter {
		t.Fatalf("S moved at seal: genesis %d->%d signed %d", genesisBefore, genesisAfter, signedAfter)
	}
	var seals int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.season_lifecycle WHERE season_key = $1 AND to_state = 'sealed'`, "temporada-fecho-e").Scan(&seals); err != nil {
		t.Fatalf("count seals: %v", err)
	}
	if seals != 1 {
		t.Fatalf("seals = %d, want exactly one", seals)
	}
	again, err := worker.Seal(ctx, closeapp.CloseRunView{Season: "temporada-fecho-e", Generation: sealed.Generation, LeaseOwner: "fechador-1"})
	if err != nil || again.Generation != sealed.Generation {
		t.Fatalf("second seal = %+v/%v, want idempotent replay", again, err)
	}
}

// TestCloseLeaseTakeoverFencesStaleWorkers proves lease timeout
// recovery: a held lease refuses takeover, an expired one hands the
// book to generation 2, and the old generation moves nothing after.
func TestCloseLeaseTakeoverFencesStaleWorkers(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := closeCtx()
	defer cancel()

	endedCloseSeason(t, ctx, db, "temporada-fecho-f", 99)
	activateCloseSeason(t, ctx, db, "temporada-fecho-f")
	worker, _ := closeWorker(db, 10)
	opened, err := worker.Begin(ctx, "temporada-fecho-f", "fechador-1", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	closer, err := closepg.NewCloser(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewCloser: %v", err)
	}
	if _, err := closer.Takeover(ctx, "temporada-fecho-f", "fechador-2", time.Minute); !errors.Is(err, seasondomain.ErrLeaseHeld) {
		t.Fatalf("held takeover = %v, want ErrLeaseHeld", err)
	}
	var taken closeapp.CloseRunView
	deadline := time.Now().Add(20 * time.Second)
	for {
		taken, err = closer.Takeover(ctx, "temporada-fecho-f", "fechador-2", time.Minute)
		if err == nil {
			break
		}
		if !errors.Is(err, seasondomain.ErrLeaseHeld) || time.Now().After(deadline) {
			t.Fatalf("takeover wait: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if taken.Generation != opened.Generation+1 || taken.LeaseOwner != "fechador-2" {
		t.Fatalf("takeover = %+v, want generation 2 under fechador-2", taken)
	}
	if _, err := worker.DrainOnce(ctx, opened); !errors.Is(err, seasondomain.ErrStaleGeneration) {
		t.Fatalf("old worker = %v, want ErrStaleGeneration: after a takeover only the new generation moves", err)
	}
	batch, err := worker.DrainOnce(ctx, taken)
	if err != nil || !batch.HoldsDone || !batch.EscrowsDone {
		t.Fatalf("new generation drain = %+v/%v, want empty done batch", batch, err)
	}
}

// TestCloseAcceptCancelRaceSettlesOnce proves concurrent release and
// refund paths never duplicate nor pick another destination: funded
// escrows end accepted or refunded, each with at most one terminal
// settlement and conserved books.
func TestCloseAcceptCancelRaceSettlesOnce(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := closeCtx()
	defer cancel()

	liveStarts := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	_, liveEnds := seedCloseSeason(t, ctx, db, "temporada-fecho-g", 100, liveStarts)
	genesisHolder := closeAccount(t, ctx, db)
	fundCloseHolder(t, ctx, db, genesisHolder, "temporada-fecho-g", 100000)
	activateCloseSeason(t, ctx, db, "temporada-fecho-g")
	economyLive := economypg.NewRepository(db.Pool.Pool())
	escrows, err := commercepg.NewEscrowRepository(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewEscrowRepository: %v", err)
	}
	fundUC, _ := commerceapp.NewFundContractUseCase(escrows)
	acceptUC, _ := commerceapp.NewAcceptDeliveryUseCase(escrows)

	for i := 0; i < 10; i++ {
		buyer := closeAccount(t, ctx, db)
		provider := closeAccount(t, ctx, db)
		closeCustodyID(t, ctx, db, "user", buyer, "temporada-fecho-g")
		buyerAmount, err := economydomain.NewMilliInk(100000)
		if err != nil {
			t.Fatalf("NewMilliInk: %v", err)
		}
		userKind, _ := economydomain.ParseCustodyKind("user")
		treasuryKind, _ := economydomain.ParseCustodyKind("treasury")
		if _, err := economyLive.Transfer(ctx, economyapp.TransferRequest{
			FromSeason: economydomain.SeasonKey("temporada-fecho-g"), FromKind: treasuryKind, FromLabel: "main",
			ToSeason: economydomain.SeasonKey("temporada-fecho-g"), ToKind: userKind, ToLabel: buyer, Amount: buyerAmount,
		}); err != nil {
			t.Fatalf("fund buyer %d: %v", i, err)
		}
		key := "corrida-1"
		now := time.Now().UTC().Truncate(time.Second)
		if _, err := fundUC.Execute(ctx, commerceapp.FundCommand{
			Key: key, Object: "revisao de fecho", Buyer: buyer, Provider: provider,
			AmountMill: 20000, ExpiresAt: liveEnds.Add(-time.Hour), Now: now,
			Season: "temporada-fecho-g", PolicyRef: "terminal-v1", PolicyHash: closePolicyHash,
			BuyerAccept: "aceite-comprador-1", ProviderAccept: "aceite-prestador-1",
			SeasonEndsAt: liveEnds, ResetAcknowledged: true,
		}); err != nil {
			t.Fatalf("fund %d: %v", i, err)
		}
		var acceptErr, cancelErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, acceptErr = acceptUC.Execute(ctx, key, buyer) }()
		go func() { defer wg.Done(); _, cancelErr = escrows.CancelContract(ctx, key, buyer) }()
		wg.Wait()
		if acceptErr != nil && cancelErr != nil {
			t.Fatalf("iteration %d: both paths failed: %v / %v", i, acceptErr, cancelErr)
		}
		var status string
		var terminal, payouts int
		if err := db.QueryRow(ctx,
			`SELECT COALESCE((SELECT action FROM app.commerce_settlements s
			 JOIN app.commerce_contracts c ON c.id = s.contract_id
			 WHERE c.contract_key = $1 AND c.buyer_id = $2::uuid
			 ORDER BY s.posted_at DESC, s.id DESC LIMIT 1), 'funded')`, key, buyer).Scan(&status); err != nil {
			t.Fatalf("read status %d: %v", i, err)
		}
		if status != "accept" && status != "refund" {
			t.Fatalf("iteration %d status = %q, want accept or refund: one destination, never both", i, status)
		}
		if err := db.QueryRow(ctx,
			`SELECT count(*) FROM app.commerce_settlements s
			 JOIN app.commerce_contracts c ON c.id = s.contract_id
			 WHERE c.contract_key = $1 AND c.buyer_id = $2::uuid
			   AND s.action IN ('release', 'refund', 'resolve-release', 'resolve-refund')`, key, buyer).Scan(&terminal); err != nil {
			t.Fatalf("count terminal %d: %v", i, err)
		}
		if err := db.QueryRow(ctx,
			`SELECT COALESCE(SUM(CASE WHEN s.transfer_id IS NULL THEN 0 ELSE 1 END), 0) FROM app.commerce_settlements s
			 JOIN app.commerce_contracts c ON c.id = s.contract_id
			 WHERE c.contract_key = $1 AND c.buyer_id = $2::uuid
			   AND s.action IN ('release', 'refund')`, key, buyer).Scan(&payouts); err != nil {
			t.Fatalf("count payouts %d: %v", i, err)
		}
		if terminal > 1 || payouts > 1 {
			t.Fatalf("iteration %d: terminal %d payouts %d, want at most one money-moving step", i, terminal, payouts)
		}
		if status == "refund" && payouts != 1 {
			t.Fatalf("iteration %d: refunded without exactly one payout", i)
		}
		if status == "accept" && payouts != 0 {
			t.Fatalf("iteration %d: accepted with payout legs", i)
		}
	}
	genesis, signed, _, _ := closeSums(t, ctx, db, "temporada-fecho-g")
	if genesis != 2100000000000 || signed != genesis {
		t.Fatalf("book moved: genesis %d signed %d", genesis, signed)
	}
}
