package postgres_test

// P34-T03 — Treasury fund commitments on real PostgreSQL.
//
// One named obligation locks vault funds through the holds machinery
// with the owner fixed to the Treasury: accepted sales, approved
// Crumbs, compensations and due payments commit before any available
// balance is exposed. The tests prove on a disposable database:
// simultaneous commits never exceed the vault available, cancel and
// expiry return only their own commitment, crashes leave no duplicate
// and third parties keep every unit, with S conserved throughout.

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

func commitCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func commitNow() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }

func commitFunds(t *testing.T, ctx context.Context, repo *postgres.Repository, vault, purpose string, millis int64, expiresAt time.Time) *application.HoldView {
	t.Helper()
	useCase := application.NewCommitFundsUseCase(repo, fixedHoldClock{now: commitNow()})
	view, err := useCase.Execute(ctx, application.CommitFundsCommand{
		Vault: vault, Purpose: purpose, Millis: millis, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("CommitFunds(%s, %d): %v", vault, millis, err)
	}
	return view
}

func fundCommitVault(t *testing.T, ctx context.Context, pool *pgxpool.Pool, vault string, millis int64) {
	t.Helper()
	moveToVault(t, ctx, pool, vault, millis)
}

func committedTotal(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var total int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_holds WHERE status = 'active'`).Scan(&total); err != nil {
		t.Fatalf("sum active holds: %v", err)
	}
	return total
}

// TestCommitFundsLocksVaultAvailable proves the happy path: 600 of 1000
// commercial stock locks for one accepted sale, the vault keeps 400,
// third parties keep every unit and S is conserved.
func TestCommitFundsLocksVaultAvailable(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := commitCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	fundCommitVault(t, ctx, pool, "commercial_stock", 1000)
	seedCutEscrow(t, ctx, pool, 300)

	view := commitFunds(t, ctx, repo, "commercial_stock", "accepted sale #1", 600, commitNow().Add(time.Hour))
	if view.Amount.Millis() != 600 {
		t.Fatalf("committed = %d, want 600", view.Amount.Millis())
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 400 {
		t.Fatalf("vault available = %d, want 400", got)
	}
	if got := committedTotal(t, ctx, pool); got != 600 {
		t.Fatalf("committed total = %d, want 600", got)
	}
	assertCutEscrow(t, ctx, pool, 300)
	if got := custodyBalance(t, ctx, pool, "treasury", "operating_cash"); got != 0 {
		t.Fatalf("innocent vault moved: %d", got)
	}
	economySupply(t, ctx, pool)
}

// TestCommitFundsNeverExceedsAvailable proves sequential commitments
// stop at the vault edge: 600 + 600 on 1000 refuses the second, 400
// settles exactly.
func TestCommitFundsNeverExceedsAvailable(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := commitCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	fundCommitVault(t, ctx, pool, "commercial_stock", 1000)

	commitFunds(t, ctx, repo, "commercial_stock", "sale A", 600, commitNow().Add(time.Hour))
	useCase := application.NewCommitFundsUseCase(repo, fixedHoldClock{now: commitNow()})
	if _, err := useCase.Execute(ctx, application.CommitFundsCommand{
		Vault: "commercial_stock", Purpose: "sale B", Millis: 600, ExpiresAt: commitNow().Add(time.Hour),
	}); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("over-commit = %v, want ErrInsufficientMilliInk", err)
	}
	commitFunds(t, ctx, repo, "commercial_stock", "sale C", 400, commitNow().Add(time.Hour))
	if got := committedTotal(t, ctx, pool); got != 1000 {
		t.Fatalf("committed total = %d, want exactly 1000", got)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 0 {
		t.Fatalf("vault available = %d, want 0", got)
	}
	economySupply(t, ctx, pool)
}

// TestCommitFundsRacesSerializeOnAvailable proves two simultaneous 600
// commitments on 1000 collapse to one winner: the row lock serializes
// them and the loser finds the edge instead of over-committing.
func TestCommitFundsRacesSerializeOnAvailable(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := commitCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	fundCommitVault(t, ctx, pool, "commercial_stock", 1000)

	const runners = 2
	var wg sync.WaitGroup
	views := make([]*application.HoldView, runners)
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			useCase := application.NewCommitFundsUseCase(repo, fixedHoldClock{now: commitNow()})
			views[i], errs[i] = useCase.Execute(ctx, application.CommitFundsCommand{
				Vault: "commercial_stock", Purpose: "race sale", Millis: 600, ExpiresAt: commitNow().Add(time.Hour),
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
			t.Fatalf("runner %d: %v", i, errs[i])
		}
	}
	if won != 1 || refused != 1 {
		t.Fatalf("won = %d, refused = %d; want 1 and 1", won, refused)
	}
	if got := committedTotal(t, ctx, pool); got != 600 {
		t.Fatalf("committed total = %d, want 600 once", got)
	}
	economySupply(t, ctx, pool)
}

// TestCommitFundsCancelReturnsOnlyItsOwn proves cancel and expiry are
// scoped to their own commitment: releasing A returns exactly A while
// B stays locked, and a lapsed C expires then returns exactly C.
func TestCommitFundsCancelReturnsOnlyItsOwn(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := commitCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	fundCommitVault(t, ctx, pool, "commercial_stock", 1000)

	first := commitFunds(t, ctx, repo, "commercial_stock", "due payment A", 300, commitNow().Add(time.Hour))
	second := commitFunds(t, ctx, repo, "commercial_stock", "due payment B", 200, commitNow().Add(time.Hour))

	releases := application.NewReleaseUseCase(repo)
	if _, err := releases.Execute(ctx, application.SettleCommand{HoldID: first.HoldID}); err != nil {
		t.Fatalf("release A: %v", err)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 800 {
		t.Fatalf("vault available = %d, want 800 (only A returned)", got)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.economy_holds WHERE id = $1::uuid`, second.HoldID).Scan(&status); err != nil {
		t.Fatalf("read B: %v", err)
	}
	if status != "active" {
		t.Fatalf("B status = %q, want active (untouched by A's cancel)", status)
	}

	seedLapsedCommit(t, ctx, pool, "commercial_stock", 150)
	lapsed := findCommitByPurpose(t, ctx, pool, "lapsed compensation")
	expires := application.NewExpireUseCase(repo)
	if _, err := expires.Execute(ctx, application.SettleCommand{HoldID: lapsed}); err != nil {
		t.Fatalf("expire lapsed: %v", err)
	}
	if _, err := releases.Execute(ctx, application.SettleCommand{HoldID: lapsed}); err != nil {
		t.Fatalf("release lapsed: %v", err)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 800 {
		t.Fatalf("vault available = %d, want 800 (A 300 and lapsed 150 back, B 200 still locked)", got)
	}
	economySupply(t, ctx, pool)
}

// TestCommitFundsCrashLeavesNoDuplicate proves torn writes vanish: a
// cancelled command settles nothing and the journal keeps every unit.
func TestCommitFundsCrashLeavesNoDuplicate(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := commitCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	fundCommitVault(t, ctx, pool, "commercial_stock", 1000)

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	useCase := application.NewCommitFundsUseCase(repo, fixedHoldClock{now: commitNow()})
	if _, err := useCase.Execute(cancelled, application.CommitFundsCommand{
		Vault: "commercial_stock", Purpose: "torn sale", Millis: 100, ExpiresAt: commitNow().Add(time.Hour),
	}); err == nil {
		t.Fatal("cancelled commitment succeeded")
	}
	if got := committedTotal(t, ctx, pool); got != 0 {
		t.Fatalf("torn commitment recorded %d", got)
	}
	if got := custodyBalance(t, ctx, pool, "treasury", "commercial_stock"); got != 1000 {
		t.Fatalf("vault available = %d, want 1000 untouched", got)
	}
	economySupply(t, ctx, pool)
}

// seedCutEscrow carves 300 out of the Genesis home into an identified
// escrow, so commitment tests prove third parties keep every unit.
func seedCutEscrow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, millis int64) {
	t.Helper()
	var treasury, escrow string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main'`).Scan(&treasury); err != nil {
		t.Fatalf("resolve treasury home: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('escrow', 'commit-case')`); err != nil {
		t.Fatalf("open escrow: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'escrow' AND label = 'commit-case'`).Scan(&escrow); err != nil {
		t.Fatalf("resolve escrow: %v", err)
	}
	var transfer string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $3), ($1::uuid, $4::uuid, 'credit', $3)`,
		transfer, treasury, millis, escrow); err != nil {
		t.Fatalf("carve escrow: %v", err)
	}
}

func assertCutEscrow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int64) {
	t.Helper()
	var balance int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries e
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'escrow' AND c.label = 'commit-case'`).Scan(&balance); err != nil {
		t.Fatalf("escrow balance: %v", err)
	}
	if balance != want {
		t.Fatalf("escrow = %d, want %d untouched", balance, want)
	}
}

// seedLapsedCommit records one commitment whose deadline already
// passed, the way a lapsed obligation reads the day after: active row,
// past expiry, legs already locked.
func seedLapsedCommit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, vault string, millis int64) {
	t.Helper()
	var owner string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = $1`, vault).Scan(&owner); err != nil {
		t.Fatalf("resolve vault %s: %v", vault, err)
	}
	var hold, holdCustody, transfer string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&hold); err != nil {
		t.Fatalf("hold id: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('escrow', $1) RETURNING id::text`,
		"hold-"+hold).Scan(&holdCustody); err != nil {
		t.Fatalf("hold custody: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_holds (id, owner_custody_id, hold_custody_id, amount_milli, purpose, expires_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'lapsed compensation', now() - interval '1 day')`,
		hold, owner, holdCustody, millis); err != nil {
		t.Fatalf("lapsed hold: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', $3), ($1::uuid, $4::uuid, 'credit', $3)`,
		transfer, owner, millis, holdCustody); err != nil {
		t.Fatalf("lapsed legs: %v", err)
	}
}

func findCommitByPurpose(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purpose string) string {
	t.Helper()
	var hold string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app.economy_holds WHERE purpose = $1`, purpose).Scan(&hold); err != nil {
		t.Fatalf("find hold %q: %v", purpose, err)
	}
	return hold
}
