package postgres_test

// P34-T02 — sovereign reserve allocation on real PostgreSQL.
//
// One ratified act funds the reserve vault from existing Treasury
// stock in a single transaction: the Genesis home debits while the
// reserve credits, keyed idempotently by the act itself. Amounts are
// always explicit — Q23 stays PENDENTE, so no percentage lives here.
// The tests prove on a disposable database: exact settlement with S
// conserved, replay without duplication, concurrent acts collapsing to
// one, empty homes refusing instead of minting, divergent terms
// conflicting while later acts stay prospective, obligations untouched
// and the frozen book refusing.

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

func reserveCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func allocateOnce(t *testing.T, ctx context.Context, repo *postgres.Repository, act string, millis int64) (*application.AllocateReserveResult, error) {
	t.Helper()
	useCase := application.NewAllocateReserveUseCase(repo)
	return useCase.Execute(ctx, application.AllocateReserveCommand{ActID: act, Millis: millis})
}

func mustAllocate(t *testing.T, ctx context.Context, repo *postgres.Repository, act string, millis int64) *application.AllocateReserveResult {
	t.Helper()
	result, err := allocateOnce(t, ctx, repo, act, millis)
	if err != nil {
		t.Fatalf("AllocateReserve(%s, %d): %v", act, millis, err)
	}
	return result
}

func reserveVaultBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	return custodyBalance(t, ctx, pool, "treasury", "sovereign_reserve")
}

// TestReserveAllocationSettlesFromExistingStock proves the happy path:
// 1000 leaves the Genesis home for the reserve vault, S is conserved
// and the T01 vault report agrees.
func TestReserveAllocationSettlesFromExistingStock(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reserveCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	result := mustAllocate(t, ctx, repo, "act-2026-09", 1000)
	if result.Allocated.Millis() != 1000 || result.Replayed {
		t.Fatalf("result = %+v, want 1000 fresh", result)
	}
	if got := reserveVaultBalance(t, ctx, pool); got != 1000 {
		t.Fatalf("reserve = %d, want 1000", got)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "main"); got != domain.GenesisSupplyMillis-1000 {
		t.Fatalf("home = %d, want S-1000", got)
	}
	report, err := application.NewTreasuryVaultsUseCase(repo).Execute(ctx, application.TreasuryCommand{Season: domain.CompatSeasonKey})
	if err != nil {
		t.Fatalf("vaults report: %v", err)
	}
	if len(report.Mismatch) != 0 {
		t.Fatalf("Mismatch = %v, want clean", report.Mismatch)
	}
	economySupply(t, ctx, pool)
}

// TestReserveAllocationReplaysWithoutDuplicating proves the second
// settlement of one act resolves the original transfer with zero new
// legs.
func TestReserveAllocationReplaysWithoutDuplicating(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reserveCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	first := mustAllocate(t, ctx, repo, "act-replay", 2500)
	second := mustAllocate(t, ctx, repo, "act-replay", 2500)
	if !second.Replayed || second.TransferID != first.TransferID {
		t.Fatalf("replay resolved another settlement: %+v vs %+v", second, first)
	}
	if got := reserveVaultBalance(t, ctx, pool); got != 2500 {
		t.Fatalf("reserve = %d, want 2500 once", got)
	}
	var intentions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_intentions WHERE operation = 'reserve'`).Scan(&intentions); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	if intentions != 1 {
		t.Fatalf("intentions = %d, want exactly 1", intentions)
	}
	economySupply(t, ctx, pool)
}

// TestReserveAllocationRacesCollapseToOne proves eight simultaneous runs
// of one act settle a single allocation: one executes, seven replay.
func TestReserveAllocationRacesCollapseToOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := reserveCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)

	const runners = 8
	var wg sync.WaitGroup
	results := make([]*application.AllocateReserveResult, runners)
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			useCase := application.NewAllocateReserveUseCase(repo)
			results[i], errs[i] = useCase.Execute(ctx, application.AllocateReserveCommand{ActID: "act-race", Millis: 4000})
		}(i)
	}
	wg.Wait()
	founded, replayed := 0, 0
	var transferID string
	for i := range runners {
		if errs[i] != nil {
			t.Fatalf("runner %d: %v", i, errs[i])
		}
		if results[i].Replayed {
			replayed++
		} else {
			founded++
		}
		if transferID == "" {
			transferID = results[i].TransferID
		} else if results[i].TransferID != transferID {
			t.Fatalf("runner %d settled another transfer", i)
		}
	}
	if founded != 1 || replayed != runners-1 {
		t.Fatalf("founded = %d, replayed = %d; want 1 and %d", founded, replayed, runners-1)
	}
	if got := reserveVaultBalance(t, ctx, pool); got != 4000 {
		t.Fatalf("reserve = %d, want 4000 once", got)
	}
	economySupply(t, ctx, pool)
}

// TestReserveAllocationRefusesEmptyHome proves stockout fails closed:
// allocating beyond the home stock writes nothing anywhere.
func TestReserveAllocationRefusesEmptyHome(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reserveCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	if _, err := allocateOnce(t, ctx, repo, "act-stockout", domain.GenesisSupplyMillis+1); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("stockout allocation = %v, want ErrInsufficientMilliInk", err)
	}
	if got := reserveVaultBalance(t, ctx, pool); got != 0 {
		t.Fatalf("reserve moved on refused allocation: %d", got)
	}
	var intentions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_intentions WHERE operation = 'reserve'`).Scan(&intentions); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	if intentions != 0 {
		t.Fatalf("refused allocation recorded %d intentions", intentions)
	}
	economySupply(t, ctx, pool)
}

// TestReserveAllocationKeepsLaterActsProspective proves divergent terms
// under one act conflict instead of rewriting it, while a later act
// with its own key settles prospectively beside the first.
func TestReserveAllocationKeepsLaterActsProspective(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reserveCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	first := mustAllocate(t, ctx, repo, "act-first", 1000)
	if _, err := allocateOnce(t, ctx, repo, "act-first", 2000); !errors.Is(err, domain.ErrIntentionConflict) {
		t.Fatalf("divergent terms = %v, want ErrIntentionConflict", err)
	}
	second := mustAllocate(t, ctx, repo, "act-second", 2000)
	if second.TransferID == first.TransferID {
		t.Fatal("later act reused the first transfer: acts stay prospective")
	}
	if got := reserveVaultBalance(t, ctx, pool); got != 3000 {
		t.Fatalf("reserve = %d, want 1000+2000", got)
	}
	replayed, err := allocateOnce(t, ctx, repo, "act-first", 1000)
	if err != nil || !replayed.Replayed || replayed.TransferID != first.TransferID {
		t.Fatalf("original act did not replay untouched: %+v, %v", replayed, err)
	}
	economySupply(t, ctx, pool)
}

// TestReserveAllocationNeverInvadesObligations proves the reserve draws
// from the Genesis home only: a funded escrow keeps its exact balance.
func TestReserveAllocationNeverInvadesObligations(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reserveCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	var treasury, escrow string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&treasury); err != nil {
		t.Fatalf("resolve treasury home: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('escrow', 'case-9')`); err != nil {
		t.Fatalf("open escrow: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'escrow' AND label = 'case-9'`).Scan(&escrow); err != nil {
		t.Fatalf("resolve escrow: %v", err)
	}
	var transfer string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', 300), ($1::uuid, $3::uuid, 'credit', 300)`,
		transfer, treasury, escrow); err != nil {
		t.Fatalf("carve escrow: %v", err)
	}
	mustAllocate(t, ctx, repo, "act-beside-escrow", 5000)
	var escrowBalance int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE custody_id = $1::uuid`, escrow).Scan(&escrowBalance); err != nil {
		t.Fatalf("escrow balance: %v", err)
	}
	if escrowBalance != 300 {
		t.Fatalf("escrow = %d, want 300 untouched", escrowBalance)
	}
	economySupply(t, ctx, pool)
}

// TestReserveAllocationFrozenRefuses proves the frozen book refuses the
// allocation while reads continue.
func TestReserveAllocationFrozenRefuses(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := reserveCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	mustAllocate(t, ctx, repo, "act-before-freeze", 100)
	makeCustody(t, ctx, pool, "user", "reserve-frozen-holder")
	freezeWithOrphan(t, ctx, pool, "reserve-frozen-holder")
	if _, err := repo.Reconcile(ctx, domain.SeasonKey(domain.CompatSeasonKey)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := allocateOnce(t, ctx, repo, "act-frozen", 100); !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("frozen allocation = %v, want ErrEconomyFrozen", err)
	}
}
