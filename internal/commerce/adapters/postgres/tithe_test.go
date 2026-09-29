package postgres_test

// P37-T04 — the 10% tithe splits only on liquidated formal
// settlement on real PostgreSQL.
//
// When an accepted service escrow releases, the same transaction
// debits the full escrow and credits floor(10%) to treasury/main
// with the rest to the provider, in milliINK; gifts, unreleased
// escrow and buyer refunds never move tithe. The tests prove on a
// disposable database: exact 10% on 20000 and 1000 units, dust
// 0–20 units staying with the provider, concurrent releases paying
// once, cancelled releases rolling back and total outputs equal to
// the paid value with S conserved.

import (
	"context"
	"sync"
	"testing"
	"time"

	commercepg "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/postgres"
	commerceapp "github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func titheTreasury(t *testing.T, ctx context.Context, db *dbtest.TestDB) int64 {
	t.Helper()
	var balance int64
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'treasury' AND c.label = 'main'`).Scan(&balance); err != nil {
		t.Fatalf("treasury balance: %v", err)
	}
	return balance
}

func fundTitheContract(t *testing.T, ctx context.Context, kit *escrowKit, key string, amount int64) {
	t.Helper()
	view, err := kit.fund.Execute(ctx, commerceapp.FundCommand{
		Key: key, Object: "serviço com dízimo", Buyer: kit.buyer, Provider: kit.provider,
		AmountMill: amount, ExpiresAt: kit.now.Add(time.Hour), Now: kit.now,
	})
	if err != nil {
		t.Fatalf("fund %s (%d): %v", key, amount, err)
	}
	if view.AmountMill != amount {
		t.Fatalf("funded = %d, want %d", view.AmountMill, amount)
	}
}

func releaseTitheContract(t *testing.T, ctx context.Context, kit *escrowKit, key string) {
	t.Helper()
	if _, err := kit.accept.Execute(ctx, key, kit.buyer); err != nil {
		t.Fatalf("accept %s: %v", key, err)
	}
	if _, err := kit.release.Execute(ctx, key, kit.buyer); err != nil {
		t.Fatalf("release %s: %v", key, err)
	}
}

func settlementLegs(t *testing.T, ctx context.Context, db *dbtest.TestDB, key string) (debit, providerCredit, treasuryCredit int64, transfer string) {
	t.Helper()
	if err := db.QueryRow(ctx,
		`SELECT transfer_id::text FROM app.commerce_settlements s
		 JOIN app.commerce_contracts c ON c.id = s.contract_id
		 WHERE c.contract_key = $1 AND s.action = 'release'`, key).Scan(&transfer); err != nil {
		t.Fatalf("settlement transfer %s: %v", key, err)
	}
	rows, err := db.Query(ctx,
		`SELECT c.kind || '/' || c.label, e.direction, e.amount_milli
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE e.transfer_id = $1::uuid`, transfer)
	if err != nil {
		t.Fatalf("read legs %s: %v", key, err)
	}
	defer rows.Close()
	for rows.Next() {
		var custody, direction string
		var amount int64
		if err := rows.Scan(&custody, &direction, &amount); err != nil {
			t.Fatalf("scan leg: %v", err)
		}
		if direction == "debit" {
			debit += amount
		} else if custody == "treasury/main" {
			treasuryCredit += amount
		} else {
			providerCredit += amount
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("legs %s: %v", key, err)
	}
	return debit, providerCredit, treasuryCredit, transfer
}

// TestTitheReleaseSplitsExact proves the 20000 release debits the
// full escrow once and credits 18000 to the provider with 2000 to
// the Treasury under one transfer, with outputs summing to paid.
func TestTitheReleaseSplitsExact(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newEscrowKit(t, db, 100000)
	beforeTreasury := titheTreasury(t, ctx, db)
	beforeProvider := escrowBalance(t, ctx, db, kit.provider)
	fundTitheContract(t, ctx, kit, "tithe-exact-1", 20000)
	releaseTitheContract(t, ctx, kit, "tithe-exact-1")

	if got := escrowBalance(t, ctx, db, kit.provider); got != beforeProvider+18000 {
		t.Fatalf("provider = %d, want %d", got, beforeProvider+18000)
	}
	if got := titheTreasury(t, ctx, db); got != beforeTreasury+2000 {
		t.Fatalf("treasury = %d, want %d", got, beforeTreasury+2000)
	}
	debit, providerCredit, treasuryCredit, _ := settlementLegs(t, ctx, db, "tithe-exact-1")
	if debit != 20000 || providerCredit != 18000 || treasuryCredit != 2000 {
		t.Fatalf("legs = debit %d provider %d treasury %d, want 20000/18000/2000",
			debit, providerCredit, treasuryCredit)
	}
	if providerCredit+treasuryCredit != debit {
		t.Fatalf("outputs %d+%d != paid %d", providerCredit, treasuryCredit, debit)
	}
}

// TestTitheSmallAmountsAndThousand proves floor(10%) on dust 0–20
// and on 1000 units: 1–9 move whole to the provider with no
// Treasury leg, 10+ split exact, and outputs always sum to paid.
func TestTitheSmallAmountsAndThousand(t *testing.T) {
	cases := []struct {
		amount int64
		tithe  int64
		net    int64
	}{
		{1, 0, 1},
		{9, 0, 9},
		{10, 1, 9},
		{11, 1, 10},
		{19, 1, 18},
		{20, 2, 18},
		{1000, 100, 900},
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			db := dbtest.New(t)
			ctx, cancel := escrowCtx()
			defer cancel()

			kit := newEscrowKit(t, db, 100000)
			beforeTreasury := titheTreasury(t, ctx, db)
			beforeProvider := escrowBalance(t, ctx, db, kit.provider)
			key := "tithe-dust-1"
			fundTitheContract(t, ctx, kit, key, tc.amount)
			releaseTitheContract(t, ctx, kit, key)

			if got := escrowBalance(t, ctx, db, kit.provider); got != beforeProvider+tc.net {
				t.Fatalf("amount %d: provider = %d, want net %d", tc.amount, got, beforeProvider+tc.net)
			}
			if got := titheTreasury(t, ctx, db); got != beforeTreasury+tc.tithe {
				t.Fatalf("amount %d: treasury = %d, want +%d", tc.amount, got, tc.tithe)
			}
			debit, providerCredit, treasuryCredit, _ := settlementLegs(t, ctx, db, key)
			if debit != tc.amount || providerCredit != tc.net || treasuryCredit != tc.tithe {
				t.Fatalf("amount %d: legs debit %d provider %d treasury %d, want %d/%d/%d",
					tc.amount, debit, providerCredit, treasuryCredit, tc.amount, tc.net, tc.tithe)
			}
			if providerCredit+treasuryCredit != debit {
				t.Fatalf("amount %d: outputs do not sum to paid", tc.amount)
			}
			if tc.tithe == 0 {
				var legs int
				if err := db.QueryRow(ctx,
					`SELECT count(*) FROM app.economy_entries WHERE transfer_id =
					 (SELECT transfer_id FROM app.commerce_settlements s
					  JOIN app.commerce_contracts c ON c.id = s.contract_id
					  WHERE c.contract_key = $1 AND s.action = 'release')`, key).Scan(&legs); err != nil {
					t.Fatalf("count legs: %v", err)
				}
				if legs != 2 {
					t.Fatalf("dust %d: legs = %d, want exactly the pair with no zero leg", tc.amount, legs)
				}
			}
		})
	}
}

// TestTitheNeverOnGiftUnreleasedOrRefund proves gifts, funded but
// unreleased escrow and buyer refunds move no tithe: the Treasury
// stays untouched and refunds return whole.
func TestTitheNeverOnGiftUnreleasedOrRefund(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newEscrowKit(t, db, 100000)
	beforeTreasury := titheTreasury(t, ctx, db)

	fundTitheContract(t, ctx, kit, "tithe-quiet-1", 20000)
	if got := titheTreasury(t, ctx, db); got != beforeTreasury {
		t.Fatal("funding must not move tithe")
	}
	if _, err := kit.accept.Execute(ctx, "tithe-quiet-1", kit.buyer); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if got := titheTreasury(t, ctx, db); got != beforeTreasury {
		t.Fatal("acceptance must not move tithe")
	}

	limits := commercepg.TransferLimits{MaxAmountMilli: 50000, MaxPerWindow: 1000, WindowMinutes: 60}
	repo, err := commercepg.NewRepository(db.Pool.Pool(), limits)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	uc, err := commerceapp.NewTransferUseCase(repo)
	if err != nil {
		t.Fatalf("NewTransferUseCase: %v", err)
	}
	if _, err := uc.Execute(ctx, commerceapp.TransferCommand{
		Key: "tithe-gift-1", Kind: "gift", Payer: kit.buyer, Payee: kit.provider,
		AmountMilli: 5000, ConsentRef: "consent-tithe-gift-1",
	}); err != nil {
		t.Fatalf("gift: %v", err)
	}
	if got := titheTreasury(t, ctx, db); got != beforeTreasury {
		t.Fatal("gift must never bear tithe")
	}

	fundTitheContract(t, ctx, kit, "tithe-refund-1", 20000)
	if _, err := kit.cancel.Execute(ctx, "tithe-refund-1", kit.buyer); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := titheTreasury(t, ctx, db); got != beforeTreasury {
		t.Fatal("buyer refund must return whole with no tithe")
	}
	if got := escrowBalance(t, ctx, db, kit.buyer); got != 75000 {
		t.Fatalf("buyer = %d, want 75000 after one quiet hold plus gift 5000 plus full refund", got)
	}
}

// TestTitheConcurrentReleasesPayOnce proves two simultaneous
// releases resolve the single split: one executes, one replays,
// with one Treasury credit and outputs summing to paid.
func TestTitheConcurrentReleasesPayOnce(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newEscrowKit(t, testDB, 100000)
	beforeTreasury := titheTreasury(t, ctx, testDB)
	fundTitheContract(t, ctx, kit, "tithe-race-1", 20000)
	if _, err := kit.accept.Execute(ctx, "tithe-race-1", kit.buyer); err != nil {
		t.Fatalf("accept: %v", err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = kit.release.Execute(ctx, "tithe-race-1", kit.buyer)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent release: %v", err)
		}
	}
	if got := escrowBalance(t, ctx, testDB, kit.provider); got != 18000 {
		t.Fatalf("provider = %d, want single net 18000", got)
	}
	if got := titheTreasury(t, ctx, testDB); got != beforeTreasury+2000 {
		t.Fatalf("treasury = %d, want single tithe +2000", got)
	}
	debit, providerCredit, treasuryCredit, _ := settlementLegs(t, ctx, testDB, "tithe-race-1")
	if debit != 20000 || providerCredit != 18000 || treasuryCredit != 2000 {
		t.Fatalf("race legs = %d/%d/%d, want 20000/18000/2000", debit, providerCredit, treasuryCredit)
	}
}

// TestTitheCancelledReleaseRollsBack proves a cancelled release
// settles nothing: no settlement row, no legs, Treasury untouched.
func TestTitheCancelledReleaseRollsBack(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newEscrowKit(t, db, 100000)
	beforeTreasury := titheTreasury(t, ctx, db)
	fundTitheContract(t, ctx, kit, "tithe-cancel-1", 20000)
	if _, err := kit.accept.Execute(ctx, "tithe-cancel-1", kit.buyer); err != nil {
		t.Fatalf("accept: %v", err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := kit.release.Execute(cancelled, "tithe-cancel-1", kit.buyer); err == nil {
		t.Fatal("cancelled release must fail, never settle")
	}
	var settlements int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.commerce_settlements s
		 JOIN app.commerce_contracts c ON c.id = s.contract_id
		 WHERE c.contract_key = 'tithe-cancel-1' AND s.action = 'release'`).Scan(&settlements); err != nil {
		t.Fatalf("count settlements: %v", err)
	}
	if settlements != 0 {
		t.Fatalf("settlements = %d, want none after rollback", settlements)
	}
	if got := titheTreasury(t, ctx, db); got != beforeTreasury {
		t.Fatal("cancelled release must leave the Treasury untouched")
	}
	if got := escrowBalance(t, ctx, db, kit.provider); got != 0 {
		t.Fatalf("provider = %d, want 0 after rollback", got)
	}
}
