package postgres_test

// P33-T04 — conservative opt-in conversion on real PostgreSQL.
//
// One recorded opt-in settles across both books in a single
// transaction: the legacy debit extinguishes the right while the
// Treasury credit pays it in converted milliINK, keyed idempotently by
// the opt-in itself. The tests prove on a disposable database: exact
// settlement with both books reconciled, replay without duplication,
// concurrent founders collapsing to one, empty Treasury refusing
// instead of minting, crashes settling zero or one, divergent rates and
// missing or lapsed intents refused with nothing written, and the
// frozen book refusing conversion.

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

func convertCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func convertHolder(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1, 'active') RETURNING id::text`,
		email).Scan(&id); err != nil {
		t.Fatalf("create holder: %v", err)
	}
	return id
}

func fundLegacy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID string, free, purchased int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased) VALUES ($1::uuid, $2, $3)`,
		accountID, free, purchased); err != nil {
		t.Fatalf("fund legacy projection: %v", err)
	}
	seedLegacy := func(operation, bucket string, amount int64) {
		t.Helper()
		var operationID string
		if err := pool.QueryRow(ctx,
			`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
			 VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`,
			accountID, operation, "convert-seed-"+accountID+"-"+operation, "convert-seed").Scan(&operationID); err != nil {
			t.Fatalf("seed legacy operation: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) VALUES ($1::uuid, $2, $3)`,
			operationID, bucket, amount); err != nil {
			t.Fatalf("seed legacy leg: %v", err)
		}
	}
	if free > 0 {
		seedLegacy("credit_free", "FREE_INK", free)
	}
	if purchased > 0 {
		seedLegacy("credit_purchase", "PURCHASED_INK", purchased)
	}
}

func recordOptIn(t *testing.T, ctx context.Context, repo *postgres.Repository, holder, charter string, millis int64) {
	t.Helper()
	consents := application.NewConsentUseCase(repo)
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: holder, Charter: charter, Decision: "accepted"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	optins := application.NewOptInUseCase(repo, fixedHoldClock{now: time.Now().UTC()})
	if _, err := optins.Execute(ctx, application.OptInCommand{
		AccountID: holder, Charter: charter, Millis: millis, RateNum: 1000, RateDen: 1, ValidDays: 30,
	}); err != nil {
		t.Fatalf("opt-in: %v", err)
	}
}

func convertOnce(t *testing.T, ctx context.Context, repo *postgres.Repository, holder, charter string) (*application.ConversionResult, error) {
	t.Helper()
	converts := application.NewConvertUseCase(repo, repo, fixedHoldClock{now: time.Now().UTC()})
	return converts.Execute(ctx, application.ConvertCommand{
		AccountID: holder, Charter: charter, RateNum: 1000, RateDen: 1,
	})
}

func legacySums(t *testing.T, ctx context.Context, pool *pgxpool.Pool, holder string) (free, purchased int64) {
	t.Helper()
	if err := pool.QueryRow(ctx,
		`SELECT balance_free, balance_purchased FROM app.wallet_accounts WHERE account_id = $1::uuid`,
		holder).Scan(&free, &purchased); err != nil {
		t.Fatalf("legacy sums: %v", err)
	}
	return free, purchased
}

func economySupply(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
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
	return credits - debits
}

// TestConversionSettlesBothBooks proves the happy path: 5 legacy units
// become 5000 milliINK from Treasury stock, the legacy right is
// extinguished free-first, S is conserved and the legacy obligation
// reconciles to the exact remainder.
func TestConversionSettlesBothBooks(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := convertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	holder := convertHolder(t, ctx, pool, "convert-1@invalid.example")
	fundLegacy(t, ctx, pool, holder, 5000, 0)
	recordOptIn(t, ctx, repo, holder, "v1", 5000)

	result, err := convertOnce(t, ctx, repo, holder, "v1")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if result.Converted.Millis() != 5000 || result.Replayed {
		t.Fatalf("result = %+v, want 5000 fresh", result)
	}
	if free, purchased := legacySums(t, ctx, pool, holder); free != 4995 || purchased != 0 {
		t.Fatalf("legacy remainder = %d/%d, want 4995/0: five units extinguished", free, purchased)
	}
	if got := custodyBalance(t, ctx, pool, "user", holder); got != 5000 {
		t.Fatalf("holder custody = %d, want 5000", got)
	}
	economySupply(t, ctx, pool)
}

// TestConversionReplaysWithoutDuplicating proves the second settlement
// of one opt-in resolves the original transfer with zero new legs.
func TestConversionReplaysWithoutDuplicating(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := convertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	holder := convertHolder(t, ctx, pool, "convert-2@invalid.example")
	fundLegacy(t, ctx, pool, holder, 3000, 2000)
	recordOptIn(t, ctx, repo, holder, "v1", 5000)

	first, err := convertOnce(t, ctx, repo, holder, "v1")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	second, err := convertOnce(t, ctx, repo, holder, "v1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.Replayed || second.TransferID != first.TransferID {
		t.Fatalf("replay resolved another settlement: %+v vs %+v", second, first)
	}
	if free, purchased := legacySums(t, ctx, pool, holder); free != 2995 || purchased != 2000 {
		t.Fatalf("legacy remainder = %d/%d, want 2995/2000", free, purchased)
	}
	var intentions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_intentions`).Scan(&intentions); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	if intentions != 1 {
		t.Fatalf("intentions = %d, want exactly 1", intentions)
	}
	economySupply(t, ctx, pool)
}

// TestConversionRacesCollapseToOne proves eight simultaneous founders
// settle a single conversion: one executes, seven replay the same
// transfer, and both books balance.
func TestConversionRacesCollapseToOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := convertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	holder := convertHolder(t, ctx, pool, "convert-3@invalid.example")
	fundLegacy(t, ctx, pool, holder, 8000, 0)
	recordOptIn(t, ctx, repo, holder, "v1", 8000)

	const runners = 8
	var wg sync.WaitGroup
	results := make([]*application.ConversionResult, runners)
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			converts := application.NewConvertUseCase(repo, repo, fixedHoldClock{now: time.Now().UTC()})
			results[i], errs[i] = converts.Execute(ctx, application.ConvertCommand{
				AccountID: holder, Charter: "v1", RateNum: 1000, RateDen: 1,
			})
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
	if got := custodyBalance(t, ctx, pool, "user", holder); got != 8000 {
		t.Fatalf("holder custody = %d, want 8000", got)
	}
	economySupply(t, ctx, pool)
}

// TestConversionRefusesEmptyTreasury proves stockout fails closed: the
// legacy right stays intact and Genesis never mints the difference.
func TestConversionRefusesEmptyTreasury(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := convertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	makeCustody(t, ctx, pool, "user", "convert-drain")
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(domain.CompatSeasonKey),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(domain.CompatSeasonKey),
		ToKind:   domain.CustodyUser, ToLabel: "convert-drain",
		Amount: mustConvertTreasuryRemainder(t, ctx, pool, 100),
	}); err != nil {
		t.Fatalf("drain treasury: %v", err)
	}
	holder := convertHolder(t, ctx, pool, "convert-4@invalid.example")
	fundLegacy(t, ctx, pool, holder, 200000, 0)
	recordOptIn(t, ctx, repo, holder, "v1", 200000)
	if _, err := convertOnce(t, ctx, repo, holder, "v1"); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Fatalf("stockout conversion = %v, want ErrInsufficientMilliInk", err)
	}
	if free, _ := legacySums(t, ctx, pool, holder); free != 200000 {
		t.Fatalf("legacy moved on refused conversion: %d", free)
	}
	economySupply(t, ctx, pool)
}

func mustConvertTreasuryRemainder(t *testing.T, ctx context.Context, pool *pgxpool.Pool, leave int64) domain.MilliInk {
	t.Helper()
	var balance int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'treasury' AND c.label = 'main'`).Scan(&balance); err != nil {
		t.Fatalf("treasury balance: %v", err)
	}
	amount, err := domain.NewMilliInk(balance - leave)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	return amount
}

// TestConversionCrashSettlesZeroOrOne proves every crash frontier
// settles nothing or the single effect: cancelled commands and torn
// prefixes write zero rows, post-commit retries replay the one.
func TestConversionCrashSettlesZeroOrOne(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := convertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	holder := convertHolder(t, ctx, pool, "convert-5@invalid.example")
	fundLegacy(t, ctx, pool, holder, 4000, 0)
	recordOptIn(t, ctx, repo, holder, "v1", 4000)

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	converts := application.NewConvertUseCase(repo, repo, fixedHoldClock{now: time.Now().UTC()})
	if _, err := converts.Execute(cancelled, application.ConvertCommand{
		AccountID: holder, Charter: "v1", RateNum: 1000, RateDen: 1,
	}); err == nil {
		t.Fatalf("cancelled conversion succeeded")
	}
	var intentions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_intentions`).Scan(&intentions); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	if intentions != 0 {
		t.Fatalf("cancelled conversion recorded %d intentions", intentions)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		 VALUES ($1::uuid, 'debit_conversion', 'convert-torn', 'torn')`, holder); err != nil {
		t.Fatalf("torn operation: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var operations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.wallet_operations WHERE idempotency_key = 'convert-torn'`).Scan(&operations); err != nil {
		t.Fatalf("count torn: %v", err)
	}
	if operations != 0 {
		t.Fatalf("torn prefix survived: %d operations", operations)
	}

	first, err := convertOnce(t, ctx, repo, holder, "v1")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	second, err := convertOnce(t, ctx, repo, holder, "v1")
	if err != nil || !second.Replayed || second.TransferID != first.TransferID {
		t.Fatalf("post-commit retry did not replay: %+v, %v", second, err)
	}
	economySupply(t, ctx, pool)
}

// TestConversionRefusesDivergentTerms proves unaccepted, missing,
// divergent-rate and lapsed intents convert nothing anywhere.
func TestConversionRefusesDivergentTerms(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := convertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	neophyte := convertHolder(t, ctx, pool, "convert-6a@invalid.example")
	fundLegacy(t, ctx, pool, neophyte, 1000, 0)
	converts := application.NewConvertUseCase(repo, repo, fixedHoldClock{now: time.Now().UTC()})
	if _, err := converts.Execute(ctx, application.ConvertCommand{
		AccountID: neophyte, Charter: "v1", RateNum: 1000, RateDen: 1,
	}); !errors.Is(err, domain.ErrConsentRequired) {
		t.Fatalf("conversion without acceptance = %v, want ErrConsentRequired", err)
	}

	lapsed := convertHolder(t, ctx, pool, "convert-6b@invalid.example")
	fundLegacy(t, ctx, pool, lapsed, 1000, 0)
	consents := application.NewConsentUseCase(repo)
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: lapsed, Charter: "v1", Decision: "accepted"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_optins (account_id, charter_version, quantity_milli, rate_num, rate_den, valid_until)
		 VALUES ($1::uuid, 'v1', 1000, 1000, 1, now() - interval '1 day')`, lapsed); err != nil {
		t.Fatalf("seed lapsed intent: %v", err)
	}
	if _, err := converts.Execute(ctx, application.ConvertCommand{
		AccountID: lapsed, Charter: "v1", RateNum: 1000, RateDen: 1,
	}); !errors.Is(err, domain.ErrOptInExpired) {
		t.Fatalf("lapsed intent = %v, want ErrOptInExpired", err)
	}

	divergent := convertHolder(t, ctx, pool, "convert-6c@invalid.example")
	fundLegacy(t, ctx, pool, divergent, 1000, 0)
	recordOptIn(t, ctx, repo, divergent, "v1", 1000)
	if _, err := converts.Execute(ctx, application.ConvertCommand{
		AccountID: divergent, Charter: "v1", RateNum: 999, RateDen: 1,
	}); !errors.Is(err, domain.ErrRateMismatch) {
		t.Fatalf("divergent rate = %v, want ErrRateMismatch", err)
	}
	var intentions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_intentions`).Scan(&intentions); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	if intentions != 0 {
		t.Fatalf("refused conversions recorded %d intentions", intentions)
	}
	economySupply(t, ctx, pool)
}

// TestConversionFrozenRefuses proves the frozen book refuses conversion
// while reads continue: the T08 guard covers the new mutation path.
func TestConversionFrozenRefuses(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := convertCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	holder := convertHolder(t, ctx, pool, "convert-7@invalid.example")
	fundLegacy(t, ctx, pool, holder, 2000, 0)
	recordOptIn(t, ctx, repo, holder, "v1", 2000)
	makeCustody(t, ctx, pool, "user", holder)
	freezeWithOrphan(t, ctx, pool, holder)
	if _, err := repo.Reconcile(ctx, domain.SeasonKey(domain.CompatSeasonKey)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := convertOnce(t, ctx, repo, holder, "v1"); !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("frozen conversion = %v, want ErrEconomyFrozen", err)
	}
}
