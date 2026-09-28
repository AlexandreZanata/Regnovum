package postgres_test

// P32-T07 — generic value holds on real PostgreSQL.
//
// A hold locks funds out of the spendable balance into a dedicated
// escrow custody until an explicit release or capture; expiry alone
// moves nothing. The tests prove on a disposable database: locked funds
// are not spendable, release returns them, capture pays the beneficiary
// with a single winner under race, expiry at and past the deadline
// moves no leg, retries and cancellations conserve S, and every
// conservation check still balances.

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

func holdsCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func fundHolder(t *testing.T, ctx context.Context, repo *postgres.Repository, pool *pgxpool.Pool, label string, millis int64) {
	t.Helper()
	if _, err := repo.RunGenesis(ctx, application.GenesisRequest{Key: mustTransferKey(t, "genesis-holds")}); err != nil {
		t.Fatalf("seed Genesis: %v", err)
	}
	makeCustody(t, ctx, pool, "user", label)
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		t.Fatalf("NewMilliInk(%d): %v", millis, err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromKind: domain.CustodyTreasury, FromLabel: "main",
		ToKind: domain.CustodyUser, ToLabel: label,
		Amount: amount,
	}); err != nil {
		t.Fatalf("fund %s: %v", label, err)
	}
}

func reserveFor(t *testing.T, ctx context.Context, repo *postgres.Repository, owner string, millis int64, expiresAt time.Time) *application.HoldView {
	t.Helper()
	useCase := application.NewReserveUseCase(repo, fixedHoldClock{now: expiresAt.Add(-time.Hour)})
	view, err := useCase.Execute(ctx, application.ReserveCommand{
		OwnerKind: "user", OwnerLabel: owner, Purpose: "hold for test",
		Millis: millis, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("reserve %s %d: %v", owner, millis, err)
	}
	return view
}

type fixedHoldClock struct {
	now time.Time
}

func (c fixedHoldClock) Now() time.Time { return c.now }

func holdBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, holdID string) int64 {
	t.Helper()
	var balance int64
	err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE custody_id = (SELECT hold_custody_id FROM app.economy_holds WHERE id = $1::uuid)`,
		holdID).Scan(&balance)
	if err != nil {
		t.Fatalf("hold balance: %v", err)
	}
	return balance
}

func supplyConserved(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
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

// TestReserveLocksFundsOutOfSpendable proves a reservation moves value
// into the hold custody and out of what plain transfers may spend.
func TestReserveLocksFundsOutOfSpendable(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := holdsCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundHolder(t, ctx, repo, pool, "ana", 5000)

	hold := reserveFor(t, ctx, repo, "ana", 2000, time.Now().UTC().Add(time.Hour))
	if hold.Status != "active" {
		t.Fatalf("hold status = %q, want active", hold.Status)
	}
	if got := holdBalance(t, ctx, pool, hold.HoldID); got != 2000 {
		t.Fatalf("hold custody = %d, want 2000", got)
	}
	if got := custodyBalance(t, ctx, pool, "user", "ana"); got != 3000 {
		t.Fatalf("ana journal = %d, want 3000: the reserved legs left the owner", got)
	}
	amount, _ := domain.NewMilliInk(4000)
	makeCustody(t, ctx, pool, "user", "bia")
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromKind: domain.CustodyUser, FromLabel: "ana",
		ToKind: domain.CustodyUser, ToLabel: "bia",
		Amount: amount,
	}); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("overspending reserved funds = %v, want ErrInsufficientMilliInk", err)
	}
	// A transfer within the spendable remainder still lands.
	settleForHold(t, ctx, repo, "ana", "bia", 3000)
	supplyConserved(t, ctx, pool)
}

func settleForHold(t *testing.T, ctx context.Context, repo *postgres.Repository, from, to string, millis int64) {
	t.Helper()
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		t.Fatalf("NewMilliInk(%d): %v", millis, err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromKind: domain.CustodyUser, FromLabel: from,
		ToKind: domain.CustodyUser, ToLabel: to,
		Amount: amount,
	}); err != nil {
		t.Fatalf("transfer %s -> %s %d: %v", from, to, millis, err)
	}
}

// TestReleaseReturnsFunds proves release pays the hold back to its owner
// in full and never reopens: the second release is refused with S intact.
func TestReleaseReturnsFunds(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := holdsCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundHolder(t, ctx, repo, pool, "ana", 5000)
	hold := reserveFor(t, ctx, repo, "ana", 2000, time.Now().UTC().Add(time.Hour))

	released, err := repo.Release(ctx, hold.HoldID)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if released.Status != "released" {
		t.Fatalf("hold status = %q, want released", released.Status)
	}
	if got := custodyBalance(t, ctx, pool, "user", "ana"); got != 5000 {
		t.Fatalf("ana = %d after release, want 5000", got)
	}
	if _, err := repo.Release(ctx, hold.HoldID); !errors.Is(err, domain.ErrHoldState) {
		t.Fatalf("second release = %v, want ErrHoldState", err)
	}
	supplyConserved(t, ctx, pool)
}

// TestCapturePaysBeneficiaryOnce proves capture pays the beneficiary and
// that two simultaneous capturers never both win.
func TestCapturePaysBeneficiaryOnce(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := holdsCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundHolder(t, ctx, repo, pool, "ana", 5000)
	makeCustody(t, ctx, pool, "user", "bia")
	hold := reserveFor(t, ctx, repo, "ana", 2000, time.Now().UTC().Add(time.Hour))

	const racers = 2
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = repo.Capture(ctx, hold.HoldID, "user", "bia")
		}(i)
	}
	wg.Wait()
	won, refused := 0, 0
	for i := range racers {
		switch {
		case errs[i] == nil:
			won++
		case errors.Is(errs[i], domain.ErrHoldState):
			refused++
		default:
			t.Fatalf("racer %d: unexpected error %v", i, errs[i])
		}
	}
	if won != 1 || refused != racers-1 {
		t.Fatalf("won = %d, refused = %d; want 1 and %d", won, refused, racers-1)
	}
	if got := custodyBalance(t, ctx, pool, "user", "bia"); got != 2000 {
		t.Fatalf("bia = %d, want 2000", got)
	}
	if got := custodyBalance(t, ctx, pool, "user", "ana"); got != 3000 {
		t.Fatalf("ana = %d, want 3000", got)
	}
	supplyConserved(t, ctx, pool)
}

// TestExpiryMarksWithoutMoving proves expiry at and past the deadline
// writes no leg, keeps funds locked, still allows late release, and
// refuses premature expiry.
func TestExpiryMarksWithoutMoving(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := holdsCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundHolder(t, ctx, repo, pool, "ana", 5000)

	past := reserveFor(t, ctx, repo, "ana", 1000, time.Now().UTC().Add(-time.Minute))
	expired, err := repo.Expire(ctx, past.HoldID)
	if err != nil {
		t.Fatalf("Expire lapsed hold: %v", err)
	}
	if expired.Status != "expired" {
		t.Fatalf("hold status = %q, want expired", expired.Status)
	}
	if got := holdBalance(t, ctx, pool, past.HoldID); got != 1000 {
		t.Fatalf("expired hold custody = %d, want 1000 locked", got)
	}
	if got := custodyBalance(t, ctx, pool, "user", "ana"); got != 4000 {
		t.Fatalf("ana journal = %d, want 4000: the reserved legs left the owner", got)
	}
	// Expired funds stay out of reach but still settle explicitly.
	if _, err := repo.Release(ctx, past.HoldID); err != nil {
		t.Fatalf("late release: %v", err)
	}
	future := reserveFor(t, ctx, repo, "ana", 500, time.Now().UTC().Add(time.Hour))
	if _, err := repo.Expire(ctx, future.HoldID); !errors.Is(err, domain.ErrHoldNotExpired) {
		t.Fatalf("premature expiry = %v, want ErrHoldNotExpired", err)
	}
	if _, err := repo.Expire(ctx, past.HoldID); !errors.Is(err, domain.ErrHoldState) {
		t.Fatalf("re-expiry = %v, want ErrHoldState", err)
	}
	supplyConserved(t, ctx, pool)
}

// TestHoldRetryAndCancelConserveSupply proves retries settle without
// duplicating and cancellations write nothing: cardinality per
// intention, S intact.
func TestHoldRetryAndCancelConserveSupply(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := holdsCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundHolder(t, ctx, repo, pool, "ana", 3000)
	clock := fixedHoldClock{now: time.Now().UTC()}
	reserve := application.NewReserveUseCase(repo, clock)

	first, err := reserve.Execute(ctx, application.ReserveCommand{
		OwnerKind: "user", OwnerLabel: "ana", Purpose: "retry",
		Millis: 1000, ExpiresAt: clock.now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	second, err := reserve.Execute(ctx, application.ReserveCommand{
		OwnerKind: "user", OwnerLabel: "ana", Purpose: "retry",
		Millis: 1000, ExpiresAt: clock.now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("retry reserve: %v", err)
	}
	if first.HoldID == second.HoldID {
		t.Fatalf("retry reused one hold: distinct reservations must not merge")
	}
	// Two holds lock 2000; a third over the remainder is refused.
	if _, err := reserve.Execute(ctx, application.ReserveCommand{
		OwnerKind: "user", OwnerLabel: "ana", Purpose: "retry",
		Millis: 1001, ExpiresAt: clock.now.Add(time.Hour),
	}); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("third reserve = %v, want ErrInsufficientMilliInk", err)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := reserve.Execute(cancelled, application.ReserveCommand{
		OwnerKind: "user", OwnerLabel: "ana", Purpose: "cancelled",
		Millis: 100, ExpiresAt: clock.now.Add(time.Hour),
	}); err == nil {
		t.Fatalf("cancelled reserve succeeded")
	}
	var holds int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_holds`).Scan(&holds); err != nil {
		t.Fatalf("count holds: %v", err)
	}
	if holds != 2 {
		t.Fatalf("holds = %d, want exactly the 2 settled ones", holds)
	}
	supplyConserved(t, ctx, pool)
}

// TestHoldRaceOnLimitedFunds proves concurrent reservations over one
// balance settle deterministically: three win, the fourth is refused,
// and S is conserved.
func TestHoldRaceOnLimitedFunds(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := holdsCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundHolder(t, ctx, repo, pool, "ana", 3000)
	clock := fixedHoldClock{now: time.Now().UTC()}

	const racers = 4
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = application.NewReserveUseCase(repo, clock).Execute(ctx, application.ReserveCommand{
				OwnerKind: "user", OwnerLabel: "ana", Purpose: fmt.Sprintf("race-%d", i),
				Millis: 1000, ExpiresAt: clock.now.Add(time.Hour),
			})
		}(i)
	}
	wg.Wait()
	won, refused := 0, 0
	for i := range racers {
		switch {
		case errs[i] == nil:
			won++
		case errors.Is(errs[i], domain.ErrInsufficientMilliInk):
			refused++
		default:
			t.Fatalf("racer %d: unexpected error %v", i, errs[i])
		}
	}
	if won != 3 || refused != 1 {
		t.Fatalf("won = %d, refused = %d; want 3 and 1", won, refused)
	}
	supplyConserved(t, ctx, pool)
}
