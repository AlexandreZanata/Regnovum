package postgres_test

// P32-T04 — atomic transfers between Genesis custodies on real PostgreSQL.
//
// Locks are taken in canonical id order before any balance is read, the
// source balance is rechecked inside the lock, and the debit/credit pair
// commits in one transaction. The tests prove on a disposable database:
// exact double entry, insufficient and locked sources refused with zero
// legs, cancelled and partial writes reverted whole, and 100 concurrent
// transfers plus a 10-way contention conserving S to the milliINK.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func transferCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func mustTransferKey(t testing.TB, key string) domain.GenesisKey {
	t.Helper()
	parsed, err := domain.ParseGenesisKey(key)
	if err != nil {
		t.Fatalf("ParseGenesisKey(%q): %v", key, err)
	}
	return parsed
}

func fundTreasury(t *testing.T, ctx context.Context, repo *postgres.Repository) {
	t.Helper()
	if _, err := repo.RunGenesis(ctx, application.GenesisRequest{Key: mustTransferKey(t, "genesis-seed")}); err != nil {
		t.Fatalf("seed Genesis: %v", err)
	}
}

func makeCustody(t testing.TB, ctx context.Context, pool *pgxpool.Pool, kind, label string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ($1, $2)`, kind, label); err != nil {
		t.Fatalf("create custody %s/%s: %v", kind, label, err)
	}
}

func custodyBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind, label string) int64 {
	t.Helper()
	var balance int64
	err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = $1 AND c.label = $2`,
		kind, label).Scan(&balance)
	if err != nil {
		t.Fatalf("balance %s/%s: %v", kind, label, err)
	}
	return balance
}

func globalSupply(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var credits, debits int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'credit'`).Scan(&credits); err != nil {
		t.Fatalf("sum credits: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'debit'`).Scan(&debits); err != nil {
		t.Fatalf("sum debits: %v", err)
	}
	// Conservation around the lawful exception: Genesis is the single
	// unpaired credit, so credits exceed debits by exactly S. Every
	// transfer adds one debit and one credit of the same amount.
	if credits-debits != domain.GenesisSupplyMillis {
		t.Fatalf("credits %d - debits %d != S: journal out of balance", credits, debits)
	}
	return credits - debits
}

func doTransfer(t *testing.T, ctx context.Context, repo *postgres.Repository, fromKind, fromLabel, toKind, toLabel string, millis int64) *application.TransferResult {
	t.Helper()
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		t.Fatalf("NewMilliInk(%d): %v", millis, err)
	}
	result, err := repo.Transfer(ctx, application.TransferRequest{
		FromKind:  mustCustodyKind(t, fromKind),
		FromLabel: fromLabel,
		ToKind:    mustCustodyKind(t, toKind),
		ToLabel:   toLabel,
		Amount:    amount,
	})
	if err != nil {
		t.Fatalf("Transfer %s/%s -> %s/%s %d: %v", fromKind, fromLabel, toKind, toLabel, millis, err)
	}
	return result
}

func mustCustodyKind(t testing.TB, kind string) domain.CustodyKind {
	t.Helper()
	parsed, err := domain.ParseCustodyKind(kind)
	if err != nil {
		t.Fatalf("ParseCustodyKind(%q): %v", kind, err)
	}
	return parsed
}

// TestTransferMovesValueAtomically proves one transfer writes exactly two
// paired legs with the exact amount, moving value instead of creating it.
func TestTransferMovesValueAtomically(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := transferCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	makeCustody(t, ctx, pool, "user", "ana")

	result := doTransfer(t, ctx, repo, "treasury", "main", "user", "ana", 1000)
	if result.Debited.Millis() != 1000 || result.Credited.Millis() != 1000 {
		t.Fatalf("receipt = %d/%d, want 1000/1000", result.Debited.Millis(), result.Credited.Millis())
	}
	if result.TransferID == "" {
		t.Fatalf("transfer names no intention")
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "main"); got != domain.GenesisSupplyMillis-1000 {
		t.Fatalf("treasury = %d, want S-1000", got)
	}
	if got := custodyBalance(t, ctx, pool, "user", "ana"); got != 1000 {
		t.Fatalf("ana = %d, want 1000", got)
	}
	var legs, paired int
	if err := pool.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT transfer_id) FROM app.economy_entries WHERE transfer_id = $1::uuid`,
		result.TransferID).Scan(&legs, &paired); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	if legs != 2 || paired != 1 {
		t.Fatalf("legs = %d in %d transfers, want 2 in 1", legs, paired)
	}
	if got := globalSupply(t, ctx, pool); got != domain.GenesisSupplyMillis {
		t.Fatalf("global supply = %d, want S", got)
	}
}

// TestTransferRefusesInsufficientSource proves an empty source fails with
// zero legs: no partial debit exists anywhere.
func TestTransferRefusesInsufficientSource(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := transferCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	makeCustody(t, ctx, pool, "user", "poor")
	makeCustody(t, ctx, pool, "user", "rich")

	amount, _ := domain.NewMilliInk(1)
	_, err := repo.Transfer(ctx, application.TransferRequest{
		FromKind: domain.CustodyUser, FromLabel: "poor",
		ToKind: domain.CustodyUser, ToLabel: "rich",
		Amount: amount,
	})
	if !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("empty source = %v, want ErrInsufficientMilliInk", err)
	}
	if got := custodyBalance(t, ctx, pool, "user", "rich"); got != 0 {
		t.Fatalf("rich = %d after refused transfer, want 0", got)
	}
	if got := globalSupply(t, ctx, pool); got != domain.GenesisSupplyMillis {
		t.Fatalf("global supply = %d, want S", got)
	}
}

// TestTransferRefusesLockedAndSameCustody proves custody authorization:
// locked holds cannot spend, and a custody cannot face itself.
func TestTransferRefusesLockedAndSameCustody(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := transferCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	makeCustody(t, ctx, pool, "escrow", "deal")
	makeCustody(t, ctx, pool, "user", "ana")
	doTransfer(t, ctx, repo, "treasury", "main", "escrow", "deal", 500)

	amount, _ := domain.NewMilliInk(100)
	_, err := repo.Transfer(ctx, application.TransferRequest{
		FromKind: domain.CustodyEscrow, FromLabel: "deal",
		ToKind: domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	})
	if !errors.Is(err, domain.ErrUnauthorizedCustody) {
		t.Fatalf("escrow source = %v, want ErrUnauthorizedCustody", err)
	}
	_, err = repo.Transfer(ctx, application.TransferRequest{
		FromKind: domain.CustodyTreasury, FromLabel: "main",
		ToKind: domain.CustodyTreasury, ToLabel: "main",
		Amount: amount,
	})
	if !errors.Is(err, domain.ErrSameCustody) {
		t.Fatalf("same custody = %v, want ErrSameCustody", err)
	}
	_, err = repo.Transfer(ctx, application.TransferRequest{
		FromKind: domain.CustodyUser, FromLabel: "ghost",
		ToKind: domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	})
	if !errors.Is(err, domain.ErrUnknownCustody) {
		t.Fatalf("unknown custody = %v, want ErrUnknownCustody", err)
	}
	if got := globalSupply(t, ctx, pool); got != domain.GenesisSupplyMillis {
		t.Fatalf("global supply = %d, want S", got)
	}
}

// TestTransferCancelledContextWritesNothing proves a cancelled command
// leaves the journal untouched.
func TestTransferCancelledContextWritesNothing(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := transferCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	makeCustody(t, ctx, pool, "user", "ana")

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	amount, _ := domain.NewMilliInk(100)
	if _, err := repo.Transfer(cancelled, application.TransferRequest{
		FromKind: domain.CustodyTreasury, FromLabel: "main",
		ToKind: domain.CustodyUser, ToLabel: "ana",
		Amount: amount,
	}); err == nil {
		t.Fatalf("cancelled transfer succeeded")
	}
	var legs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries WHERE custody_id IN (
		SELECT id FROM app.economy_custodies WHERE kind = 'user' AND label = 'ana')`).Scan(&legs); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	if legs != 0 {
		t.Fatalf("cancelled transfer wrote %d legs", legs)
	}
}

// TestTransferWritePrefixRollsBack proves every write prefix of a transfer
// aborts clean: a debit leg without its credit reverts whole, so a crash
// between the two writes can never strand value.
func TestTransferWritePrefixRollsBack(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := transferCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	makeCustody(t, ctx, pool, "user", "ana")

	var treasury string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&treasury); err != nil {
		t.Fatalf("resolve treasury: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES (gen_random_uuid(), $1::uuid, 'debit', 100)`, treasury); err != nil {
		t.Fatalf("prefix debit: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := custodyBalance(t, ctx, pool, "user", "ana"); got != 0 {
		t.Fatalf("ana = %d after reverted prefix, want 0", got)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "main"); got != domain.GenesisSupplyMillis {
		t.Fatalf("treasury = %d after revert, want S", got)
	}
}

// TestTransferConcurrentPreservesSupply fires 100 transfers at once: all
// land, every recipient holds exactly its share, and the global supply is
// still S to the milliINK.
func TestTransferConcurrentPreservesSupply(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(30, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := transferCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	const runners = 100
	for i := range runners {
		makeCustody(t, ctx, pool, "user", fmt.Sprintf("holder-%d", i))
	}
	var wg sync.WaitGroup
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			amount, _ := domain.NewMilliInk(1000)
			_, errs[i] = repo.Transfer(ctx, application.TransferRequest{
				FromKind: domain.CustodyTreasury, FromLabel: "main",
				ToKind: domain.CustodyUser, ToLabel: fmt.Sprintf("holder-%d", i),
				Amount: amount,
			})
		}(i)
	}
	wg.Wait()
	for i := range runners {
		if errs[i] != nil {
			t.Fatalf("runner %d: %v", i, errs[i])
		}
		if got := custodyBalance(t, ctx, pool, "user", fmt.Sprintf("holder-%d", i)); got != 1000 {
			t.Fatalf("holder-%d = %d, want 1000", i, got)
		}
	}
	if got := globalSupply(t, ctx, pool); got != domain.GenesisSupplyMillis {
		t.Fatalf("global supply after 100 races = %d, want S", got)
	}
}

// TestTransferContentionSerializes proves ten racers over one balance of
// 1000 withdrawing 500 each settle deterministically: exactly two win,
// eight are refused, and no milliINK is lost or created.
func TestTransferContentionSerializes(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(30, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := transferCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	makeCustody(t, ctx, pool, "user", "carol")
	doTransfer(t, ctx, repo, "treasury", "main", "user", "carol", 1000)
	const runners = 10
	for i := range runners {
		makeCustody(t, ctx, pool, "user", fmt.Sprintf("dave-%d", i))
	}
	var wg sync.WaitGroup
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			amount, _ := domain.NewMilliInk(500)
			_, errs[i] = repo.Transfer(ctx, application.TransferRequest{
				FromKind: domain.CustodyUser, FromLabel: "carol",
				ToKind: domain.CustodyUser, ToLabel: fmt.Sprintf("dave-%d", i),
				Amount: amount,
			})
		}(i)
	}
	wg.Wait()
	won, refused := 0, 0
	for i := range runners {
		switch {
		case errs[i] == nil:
			won++
		case errors.Is(errs[i], domain.ErrInsufficientMilliInk):
			refused++
		default:
			t.Fatalf("runner %d: unexpected error %v", i, errs[i])
		}
	}
	if won != 2 || refused != runners-2 {
		t.Fatalf("won = %d, refused = %d; want 2 and %d", won, refused, runners-2)
	}
	if got := custodyBalance(t, ctx, pool, "user", "carol"); got != 0 {
		t.Fatalf("carol = %d, want 0", got)
	}
	if got := globalSupply(t, ctx, pool); got != domain.GenesisSupplyMillis {
		t.Fatalf("global supply after contention = %d, want S", got)
	}
}
