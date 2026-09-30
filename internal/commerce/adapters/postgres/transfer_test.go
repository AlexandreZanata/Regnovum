package postgres_test

// P37-T02 — authorized voluntary peer transfers on real PostgreSQL.
//
// One transaction settles the commerce row with the peer legs moving
// the exact amount: eligibility, sanctions, limits, locks, balance
// and legs share it. The tests prove on a disposable database: exact
// settlement with sealed kind, replay and conflict by key, refusal
// for unknown, inactive and sanctioned accounts on both sides,
// self-payments, amount and rate ceilings, cancelled contexts with
// nothing written, and one hundred simultaneous retries settling
// once. The suite moves no money outside its own ledger and leaves
// third parties untouched.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	commercepg "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/postgres"
	commerceapp "github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	economyapp "github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func transferCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func transferLimits() commercepg.TransferLimits {
	return commercepg.TransferLimits{MaxAmountMilli: 50000, MaxPerWindow: 1000, WindowMinutes: 60}
}

func mustCommerceAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB, status string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('commerce-' || gen_random_uuid()::text || '@invalid.example', $1) RETURNING id::text`,
		status).Scan(&id); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return id
}

func fundCommerceHolder(t *testing.T, ctx context.Context, db *dbtest.TestDB, account string, funds int64) {
	t.Helper()
	pool := db.Pool.Pool()
	repo := economypg.NewRepository(pool)
	key, err := economydomain.ParseGenesisKey("commerce-funding-seed")
	if err != nil {
		t.Fatalf("ParseGenesisKey: %v", err)
	}
	// Genesis runs once per database; later holders reuse the funded
	// Treasury.
	if _, err := repo.RunGenesis(ctx, economyapp.GenesisRequest{Key: key}); err != nil {
		if !errors.Is(err, economydomain.ErrGenesisAlreadyExists) {
			t.Fatalf("seed Genesis: %v", err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1)
		 ON CONFLICT DO NOTHING`, account); err != nil {
		t.Fatalf("create custody: %v", err)
	}
	amount, err := economydomain.NewMilliInk(funds)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	fromKind, _ := economydomain.ParseCustodyKind("treasury")
	toKind, _ := economydomain.ParseCustodyKind("user")
	if _, err := repo.Transfer(ctx, economyapp.TransferRequest{
		FromKind: fromKind, FromLabel: "main", ToKind: toKind, ToLabel: account, Amount: amount,
	}); err != nil {
		t.Fatalf("fund holder: %v", err)
	}
}

func commerceRepo(t *testing.T, db *dbtest.TestDB, limits commercepg.TransferLimits) (*commercepg.Repository, *commerceapp.TransferUseCase) {
	t.Helper()
	repo, err := commercepg.NewRepository(db.Pool.Pool(), limits)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	uc, err := commerceapp.NewTransferUseCase(repo)
	if err != nil {
		t.Fatalf("NewTransferUseCase: %v", err)
	}
	return repo, uc
}

func transferCmd(key, kind, payer, payee string, amount int64) commerceapp.TransferCommand {
	return commerceapp.TransferCommand{
		Key: key, Kind: kind, Payer: payer, Payee: payee,
		AmountMilli: amount, ConsentRef: "consent-" + key,
	}
}

func commerceBalance(t *testing.T, ctx context.Context, db *dbtest.TestDB, account string) int64 {
	t.Helper()
	var balance int64
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'user' AND c.label = $1`, account).Scan(&balance); err != nil {
		t.Fatalf("balance %s: %v", account, err)
	}
	return balance
}

func countCommerceRows(t *testing.T, ctx context.Context, db *dbtest.TestDB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.commerce_transfers`).Scan(&count); err != nil {
		t.Fatalf("count transfers: %v", err)
	}
	return count
}

// TestTransferSettlesExactGift proves one voluntary gift moves the
// exact amount between eligible holders with the sealed kind, one
// paired transfer and both balances exact.
func TestTransferSettlesExactGift(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := transferCtx()
	defer cancel()

	payer := mustCommerceAccount(t, ctx, db, "active")
	payee := mustCommerceAccount(t, ctx, db, "active")
	fundCommerceHolder(t, ctx, db, payer, 100000)
	fundCommerceHolder(t, ctx, db, payee, 1000)
	_, uc := commerceRepo(t, db, transferLimits())

	result, err := uc.Execute(ctx, transferCmd("gift-1", "gift", payer, payee, 20000))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Replayed || result.AmountMilli != 20000 {
		t.Fatalf("result = %+v, want fresh 20000", result)
	}
	if commerceBalance(t, ctx, db, payer) != 80000 || commerceBalance(t, ctx, db, payee) != 21000 {
		t.Fatal("balances did not move the exact amount")
	}
	var kind string
	if err := db.QueryRow(ctx,
		`SELECT kind FROM app.commerce_transfers WHERE id = $1::uuid`, result.TransferRowID).Scan(&kind); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if kind != "gift" {
		t.Fatalf("kind = %q, want sealed gift", kind)
	}
}

// TestTransferReplayAndConflict proves the same key and payload
// replays the original settlement while the same key with divergent
// terms conflicts instead of paying twice.
func TestTransferReplayAndConflict(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := transferCtx()
	defer cancel()

	payer := mustCommerceAccount(t, ctx, db, "active")
	payee := mustCommerceAccount(t, ctx, db, "active")
	fundCommerceHolder(t, ctx, db, payer, 100000)
	fundCommerceHolder(t, ctx, db, payee, 1000)
	_, uc := commerceRepo(t, db, transferLimits())

	first, err := uc.Execute(ctx, transferCmd("idem-1", "trade", payer, payee, 15000))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := uc.Execute(ctx, transferCmd("idem-1", "trade", payer, payee, 15000))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.Replayed || second.TransferID != first.TransferID {
		t.Fatal("same key and payload must replay the original settlement")
	}
	if _, err := uc.Execute(ctx, transferCmd("idem-1", "trade", payer, payee, 15001)); !errors.Is(err, commercedomain.ErrIntentionConflict) {
		t.Fatalf("divergent amount = %v, want ErrIntentionConflict", err)
	}
	if _, err := uc.Execute(ctx, transferCmd("idem-1", "gift", payer, payee, 15000)); !errors.Is(err, commercedomain.ErrIntentionConflict) {
		t.Fatalf("divergent kind = %v, want ErrIntentionConflict", err)
	}
	if countCommerceRows(t, ctx, db) != 1 {
		t.Fatal("exactly one settlement row must exist")
	}
}

// TestTransferRefusesIneligibleParties proves unknown, inactive and
// sanctioned accounts on either side, plus self-payments, refuse
// with journal and registry unchanged.
func TestTransferRefusesIneligibleParties(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := transferCtx()
	defer cancel()

	payer := mustCommerceAccount(t, ctx, db, "active")
	payee := mustCommerceAccount(t, ctx, db, "active")
	suspended := mustCommerceAccount(t, ctx, db, "suspended")
	deleted := mustCommerceAccount(t, ctx, db, "deleted")
	fundCommerceHolder(t, ctx, db, payer, 100000)
	fundCommerceHolder(t, ctx, db, suspended, 100000)
	_, uc := commerceRepo(t, db, transferLimits())

	before := commerceBalance(t, ctx, db, payer)
	cases := []struct {
		name string
		cmd  commerceapp.TransferCommand
		want error
	}{
		{name: "unknown payer", cmd: transferCmd("no-1", "gift", "00000000-0000-4000-8000-000000000000", payee, 100), want: commercedomain.ErrUnknownAccount},
		{name: "unknown payee", cmd: transferCmd("no-2", "gift", payer, "00000000-0000-4000-8000-000000000000", 100), want: commercedomain.ErrUnknownAccount},
		{name: "suspended payer", cmd: transferCmd("no-3", "gift", suspended, payee, 100), want: commercedomain.ErrAccountNotActive},
		{name: "deleted payee", cmd: transferCmd("no-4", "gift", payer, deleted, 100), want: commercedomain.ErrAccountNotActive},
		{name: "self payment", cmd: transferCmd("no-5", "gift", payer, payer, 100), want: commercedomain.ErrSelfTransfer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := uc.Execute(ctx, tc.cmd); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	sanctioned := mustCommerceAccount(t, ctx, db, "active")
	fundCommerceHolder(t, ctx, db, sanctioned, 100000)
	if _, err := db.Exec(ctx,
		`INSERT INTO app.commerce_sanctions (account_id, reason) VALUES ($1::uuid, 'court-order')`, sanctioned); err != nil {
		t.Fatalf("sanction: %v", err)
	}
	if _, err := uc.Execute(ctx, transferCmd("no-6", "gift", sanctioned, payee, 100)); !errors.Is(err, commercedomain.ErrSanctionedAccount) {
		t.Fatalf("sanctioned payer = %v, want ErrSanctionedAccount", err)
	}
	if _, err := uc.Execute(ctx, transferCmd("no-7", "gift", payer, sanctioned, 100)); !errors.Is(err, commercedomain.ErrSanctionedAccount) {
		t.Fatalf("sanctioned payee = %v, want ErrSanctionedAccount", err)
	}

	if countCommerceRows(t, ctx, db) != 0 {
		t.Fatal("refused transfers must leave the registry empty")
	}
	if commerceBalance(t, ctx, db, payer) != before {
		t.Fatal("refused transfers must leave balances untouched")
	}
}

// TestTransferEnforcesApprovedLimits proves the amount ceiling, the
// count window and the consent gate refuse before any lock is taken
// or leg written.
func TestTransferEnforcesApprovedLimits(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := transferCtx()
	defer cancel()

	payer := mustCommerceAccount(t, ctx, db, "active")
	payee := mustCommerceAccount(t, ctx, db, "active")
	fundCommerceHolder(t, ctx, db, payer, 1000000)
	fundCommerceHolder(t, ctx, db, payee, 1000)
	_, uc := commerceRepo(t, db, commercepg.TransferLimits{MaxAmountMilli: 10000, MaxPerWindow: 2, WindowMinutes: 60})

	if _, err := uc.Execute(ctx, transferCmd("cap-1", "gift", payer, payee, 10001)); !errors.Is(err, commercedomain.ErrLimitExceeded) {
		t.Fatalf("over ceiling = %v, want ErrLimitExceeded", err)
	}
	if _, err := uc.Execute(ctx, transferCmd("cap-2", "gift", payer, payee, 9000)); err != nil {
		t.Fatalf("under ceiling: %v", err)
	}
	if _, err := uc.Execute(ctx, transferCmd("cap-3", "gift", payer, payee, 9000)); err != nil {
		t.Fatalf("second: %v", err)
	}
	if _, err := uc.Execute(ctx, transferCmd("cap-4", "gift", payer, payee, 100)); !errors.Is(err, commercedomain.ErrRateLimited) {
		t.Fatalf("over window = %v, want ErrRateLimited", err)
	}
	cmd := transferCmd("cap-5", "gift", payer, payee, 100)
	cmd.ConsentRef = ""
	if _, err := uc.Execute(ctx, cmd); !errors.Is(err, commercedomain.ErrConsentRequired) {
		t.Fatalf("missing consent = %v, want ErrConsentRequired", err)
	}
	if countCommerceRows(t, ctx, db) != 2 {
		t.Fatal("only the two covered transfers must exist")
	}
}

// TestTransferCancelledRollsBack proves a cancelled context settles
// nothing: no row, no legs, balances intact.
func TestTransferCancelledRollsBack(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := transferCtx()
	defer cancel()

	payer := mustCommerceAccount(t, ctx, db, "active")
	payee := mustCommerceAccount(t, ctx, db, "active")
	fundCommerceHolder(t, ctx, db, payer, 100000)
	fundCommerceHolder(t, ctx, db, payee, 1000)
	_, uc := commerceRepo(t, db, transferLimits())

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := uc.Execute(cancelled, transferCmd("cancel-1", "gift", payer, payee, 5000)); err == nil {
		t.Fatal("cancelled context must fail, never settle")
	}
	if countCommerceRows(t, ctx, db) != 0 || commerceBalance(t, ctx, db, payer) != 100000 {
		t.Fatal("cancelled settlement must leave no trace")
	}
}

// TestTransferRacesCollapseToOne proves one hundred simultaneous
// retries of one key settle a single transfer: one executes,
// ninety-nine replay, with one paired debit under -race.
func TestTransferRacesCollapseToOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(110, 1))
	ctx, cancel := transferCtx()
	defer cancel()

	payer := mustCommerceAccount(t, ctx, testDB, "active")
	payee := mustCommerceAccount(t, ctx, testDB, "active")
	fundCommerceHolder(t, ctx, testDB, payer, 100000)
	fundCommerceHolder(t, ctx, testDB, payee, 1000)
	repo, err := commercepg.NewRepository(testDB.Pool.Pool(), transferLimits())
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}

	const runners = 100
	var wg sync.WaitGroup
	results := make([]*commerceapp.TransferResult, runners)
	errs := make([]error, runners)
	start := make(chan struct{})
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			uc, err := commerceapp.NewTransferUseCase(repo)
			if err != nil {
				errs[i] = err
				return
			}
			results[i], errs[i] = uc.Execute(ctx, transferCmd("race-100", "gift", payer, payee, 5000))
		}(i)
	}
	close(start)
	wg.Wait()

	var transferID, rowID string
	founded, replayed := 0, 0
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
			transferID, rowID = results[i].TransferID, results[i].TransferRowID
		} else if results[i].TransferID != transferID || results[i].TransferRowID != rowID {
			t.Fatal("runners resolved different settlements for one key")
		}
	}
	if founded != 1 || replayed != runners-1 {
		t.Fatalf("founded = %d, replayed = %d; want 1 and %d", founded, replayed, runners-1)
	}
	if countCommerceRows(t, ctx, testDB) != 1 {
		t.Fatal("race settled more than one transfer")
	}
	if commerceBalance(t, ctx, testDB, payer) != 95000 || commerceBalance(t, ctx, testDB, payee) != 6000 {
		t.Fatal("race moved anything but one exact transfer")
	}
}
