package postgres_test

// P32-T03 — the guarded Genesis creation event on real PostgreSQL.
//
// A fresh migrated database holds zero economy rows: production startup
// never auto-creates supply. The first run credits exactly S to the
// Treasury with its attestation; replays resolve the original event with
// zero extra legs; concurrent runs of any keys create exactly one
// Genesis; a partial Genesis reverts whole; and a second key afterwards
// is refused. Conservation (sum of legs per custody) holds S throughout.

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

func genesisCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func runGenesis(t *testing.T, repo *postgres.Repository, ctx context.Context, key string) *application.GenesisResult {
	t.Helper()
	result, err := repo.RunGenesis(ctx, application.GenesisRequest{Key: mustGenesisKey(t, key)})
	if err != nil {
		t.Fatalf("RunGenesis(%q): %v", key, err)
	}
	return result
}

func mustGenesisKey(t *testing.T, key string) domain.GenesisKey {
	t.Helper()
	parsed, err := domain.ParseGenesisKey(key)
	if err != nil {
		t.Fatalf("ParseGenesisKey(%q): %v", key, err)
	}
	return parsed
}

func sumLegs(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var sum int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries`).Scan(&sum); err != nil {
		t.Fatalf("sum legs: %v", err)
	}
	return sum
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// TestGenesisFirstRunCreatesExactlySupply proves the first event credits
// S to the Treasury with its attestation, on a database that started
// empty: no startup path mints before the explicit command.
func TestGenesisFirstRunCreatesExactlySupply(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := genesisCtx()
	defer cancel()

	for _, table := range []string{"app.economy_custodies", "app.economy_partitions", "app.economy_entries", "app.economy_genesis"} {
		if got := countRows(t, ctx, pool, table); got != 0 {
			t.Fatalf("%s holds %d rows before Genesis: supply pre-exists the command", table, got)
		}
	}

	repo := postgres.NewRepository(pool)
	result := runGenesis(t, repo, ctx, "genesis-first")
	if result.Replayed {
		t.Fatalf("first Genesis reported Replayed")
	}
	if !result.Amount.Equals(domain.GenesisSupply()) {
		t.Fatalf("first Genesis amount = %d, want S", result.Amount.Millis())
	}
	if result.TreasuryCustodyID == "" {
		t.Fatalf("first Genesis names no Treasury custody")
	}
	if got := sumLegs(t, ctx, pool); got != domain.GenesisSupplyMillis {
		t.Fatalf("sum of legs = %d, want S", got)
	}
}

// TestGenesisReplayCreatesZeroExtra proves the same key resolves to the
// original attestation untouched: no new row, no new leg.
func TestGenesisReplayCreatesZeroExtra(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := genesisCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	first := runGenesis(t, repo, ctx, "genesis-replay")
	second := runGenesis(t, repo, ctx, "genesis-replay")
	if !second.Replayed {
		t.Fatalf("second run of the same key did not report Replayed")
	}
	if second.TreasuryCustodyID != first.TreasuryCustodyID || !second.Amount.Equals(first.Amount) {
		t.Fatalf("replay resolved a different attestation")
	}
	if got := sumLegs(t, ctx, pool); got != domain.GenesisSupplyMillis {
		t.Fatalf("sum of legs after replay = %d, want S", got)
	}
	if got := countRows(t, ctx, pool, "app.economy_genesis"); got != 1 {
		t.Fatalf("attestations = %d, want 1", got)
	}
}

// TestGenesisConcurrentSameKeyCreatesOne proves eight simultaneous runs
// of one key create exactly one Genesis: one founder, seven replays,
// still exactly S in a single leg.
func TestGenesisConcurrentSameKeyCreatesOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := genesisCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	const runners = 8
	var wg sync.WaitGroup
	results := make([]*application.GenesisResult, runners)
	errs := make([]error, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = repo.RunGenesis(ctx, application.GenesisRequest{Key: mustGenesisKey(t, "genesis-race")})
		}(i)
	}
	wg.Wait()
	founders, replays := 0, 0
	for i := range runners {
		if errs[i] != nil {
			t.Fatalf("runner %d: %v", i, errs[i])
		}
		if results[i].Replayed {
			replays++
		} else {
			founders++
		}
	}
	if founders != 1 || replays != runners-1 {
		t.Fatalf("founders = %d, replays = %d; want 1 and %d", founders, replays, runners-1)
	}
	if got := sumLegs(t, ctx, pool); got != domain.GenesisSupplyMillis {
		t.Fatalf("sum of legs after race = %d, want S", got)
	}
}

// TestGenesisConcurrentDistinctKeysRefusesSecond proves the singleton
// guard holds under concurrency: eight different keys race, exactly one
// founds Genesis and the other seven are refused without minting.
func TestGenesisConcurrentDistinctKeysRefusesSecond(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := genesisCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	const runners = 8
	var wg sync.WaitGroup
	errs := make([]error, runners)
	founded := make([]bool, runners)
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = repo.RunGenesis(ctx, application.GenesisRequest{
				Key: mustGenesisKey(t, fmt.Sprintf("genesis-key-%d", i)),
			})
			founded[i] = errs[i] == nil
		}(i)
	}
	wg.Wait()
	winners, refused := 0, 0
	for i := range runners {
		switch {
		case errs[i] == nil:
			winners++
		case errors.Is(errs[i], domain.ErrGenesisAlreadyExists):
			refused++
		default:
			t.Fatalf("runner %d: unexpected error %v", i, errs[i])
		}
	}
	if winners != 1 || refused != runners-1 {
		t.Fatalf("winners = %d, refused = %d; want 1 and %d", winners, refused, runners-1)
	}
	if got := sumLegs(t, ctx, pool); got != domain.GenesisSupplyMillis {
		t.Fatalf("sum of legs after key race = %d, want S", got)
	}
}

// TestGenesisPartialRevertsWhole proves a Genesis that fails halfway
// leaves nothing behind: custody, partition, attestation and legs revert
// together, so a retried command starts from an empty journal.
func TestGenesisPartialRevertsWhole(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := genesisCtx()
	defer cancel()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	var custody string
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('treasury', 'partial') RETURNING id::text`,
	).Scan(&custody); err != nil {
		t.Fatalf("partial custody: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_partitions (custody_id, name) VALUES ($1::uuid, 'available')`, custody); err != nil {
		t.Fatalf("partial partition: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES (gen_random_uuid(), $1::uuid, 'sideways', 100)`, custody); err == nil {
		t.Fatalf("invalid direction landed: the CHECK did not bite")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	for _, table := range []string{"app.economy_custodies", "app.economy_partitions", "app.economy_entries", "app.economy_genesis"} {
		if got := countRows(t, ctx, pool, table); got != 0 {
			t.Fatalf("%s holds %d rows after revert: partial Genesis survived", table, got)
		}
	}
}

// TestGenesisSecondKeyRefused proves a second event with another key is
// refused after Genesis, and the supply still sums to exactly S.
func TestGenesisSecondKeyRefused(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := genesisCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	runGenesis(t, repo, ctx, "genesis-original")
	_, err := repo.RunGenesis(ctx, application.GenesisRequest{Key: mustGenesisKey(t, "genesis-second")})
	if !errors.Is(err, domain.ErrGenesisAlreadyExists) {
		t.Fatalf("second Genesis key = %v, want ErrGenesisAlreadyExists", err)
	}
	if got := sumLegs(t, ctx, pool); got != domain.GenesisSupplyMillis {
		t.Fatalf("sum of legs after refused key = %d, want S", got)
	}
}
