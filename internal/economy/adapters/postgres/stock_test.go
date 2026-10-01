package postgres_test

// P34-T04 — sellable commercial stock on real PostgreSQL.
//
// The sale quantity derives from the vault journal after reserve and
// obligations: committed funds left the vault legs at commit time, so
// the display nets them by construction and third-party custodies
// never enter it. The tests prove on a disposable database: committed
// funds net out of the display, zero stock refuses without charge,
// third parties keep every unit, concurrent sale × crumb × decree
// classes serialize on the available edge and S is conserved
// throughout.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func stockCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func stockReport(t *testing.T, ctx context.Context, repo *postgres.Repository, vault string) *application.StockReport {
	t.Helper()
	report, err := application.NewSellableStockUseCase(repo).Execute(ctx, application.SellableStockCommand{Vault: vault, Season: domain.CompatSeasonKey})
	if err != nil {
		t.Fatalf("stock report: %v", err)
	}
	return report
}

func checkSale(t *testing.T, ctx context.Context, repo *postgres.Repository, vault string, millis int64) (*application.StockReport, error) {
	t.Helper()
	return application.NewSaleCheckUseCase(repo).Execute(ctx, application.SaleCheckCommand{Vault: vault, Season: domain.CompatSeasonKey, Millis: millis})
}

func commitForStock(t *testing.T, ctx context.Context, repo *postgres.Repository, vault, purpose string, millis int64) {
	t.Helper()
	useCase := application.NewCommitFundsUseCase(repo, fixedHoldClock{now: time.Now().UTC()})
	if _, err := useCase.Execute(ctx, application.CommitFundsCommand{
		Season: domain.CompatSeasonKey,
		Vault:  vault, Purpose: purpose, Millis: millis, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("commit %s %d: %v", purpose, millis, err)
	}
}

// TestSellableStockNetsCommitments proves the conservative display:
// 1000 funded with 600 committed for an accepted sale reads balance
// 400, committed 600, available 400 — never the funded 1000.
func TestSellableStockNetsCommitments(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := stockCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "commercial_stock", 1000)
	commitForStock(t, ctx, repo, "commercial_stock", "accepted sale #1", 600)

	report := stockReport(t, ctx, repo, "commercial_stock")
	if report.Balance != 400 || report.Committed != 600 || report.Available != 400 {
		t.Fatalf("report = %+v, want 400/600/400", report)
	}
	if report.Available > report.Balance {
		t.Fatalf("displayed %d above balance %d", report.Available, report.Balance)
	}
	economySupply(t, ctx, pool)
}

// TestSaleCheckRefusesZeroStockWithoutCharge proves an empty vault
// refuses before any charge exists to write: journals identical,
// zero holds recorded.
func TestSaleCheckRefusesZeroStockWithoutCharge(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := stockCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	beforeEconomy, beforeLegacy := journalFingerprint(t, ctx, pool)
	if _, err := checkSale(t, ctx, repo, "commercial_stock", 100); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("zero-stock sale = %v, want ErrInsufficientMilliInk", err)
	}
	if afterEconomy, afterLegacy := journalFingerprint(t, ctx, pool); beforeEconomy != afterEconomy || beforeLegacy != afterLegacy {
		t.Fatalf("refused check moved the journals: refusal writes nothing")
	}
	var holds int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_holds`).Scan(&holds); err != nil {
		t.Fatalf("count holds: %v", err)
	}
	if holds != 0 {
		t.Fatalf("refused check recorded %d holds", holds)
	}
	economySupply(t, ctx, pool)
}

// TestSellableStockIgnoresThirdParties proves sales read one vault
// only: operating cash and identified escrows keep every unit while
// commercial stock commits and checks.
func TestSellableStockIgnoresThirdParties(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := stockCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "commercial_stock", 500)
	moveToVault(t, ctx, pool, "operating_cash", 700)
	seedCutEscrow(t, ctx, pool, 300)
	commitForStock(t, ctx, repo, "commercial_stock", "due payment", 200)

	report := stockReport(t, ctx, repo, "commercial_stock")
	if report.Available != 300 || report.Committed != 200 {
		t.Fatalf("report = %+v, want available 300 with 200 committed", report)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "operating_cash"); got != 700 {
		t.Fatalf("operating cash = %d, want 700 untouched", got)
	}
	assertCutEscrow(t, ctx, pool, 300)
	if _, err := checkSale(t, ctx, repo, "commercial_stock", 301); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("oversale = %v, want ErrInsufficientMilliInk", err)
	}
	economySupply(t, ctx, pool)
}

// TestSaleRaceAcrossObligationClasses proves concurrent sale, crumb
// and decree commitments serialize on the available edge: two of three
// 400 commitments on 1000 win, the third finds the edge.
func TestSaleRaceAcrossObligationClasses(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := stockCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	moveToVault(t, ctx, pool, "commercial_stock", 1000)

	purposes := []string{"concurrent sale", "concurrent crumb", "concurrent decree"}
	const runners = 3
	var wg sync.WaitGroup
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			useCase := application.NewCommitFundsUseCase(repo, fixedHoldClock{now: time.Now().UTC()})
			_, errs[i] = useCase.Execute(ctx, application.CommitFundsCommand{
				Season: domain.CompatSeasonKey,
				Vault:  "commercial_stock", Purpose: purposes[i], Millis: 400, ExpiresAt: time.Now().UTC().Add(time.Hour),
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
			t.Fatalf("runner %d (%s): %v", i, purposes[i], errs[i])
		}
	}
	if won != 2 || refused != 1 {
		t.Fatalf("won = %d, refused = %d; want 2 and 1", won, refused)
	}
	report := stockReport(t, ctx, repo, "commercial_stock")
	if report.Committed != 800 || report.Available != 200 {
		t.Fatalf("report = %+v, want committed 800 available 200", report)
	}
	economySupply(t, ctx, pool)
}
