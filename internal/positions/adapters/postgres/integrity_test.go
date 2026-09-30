package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	positionspg "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/positions/application"
	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// TestPositionIntegrityUnderConcurrentLoad is the P09-T07 load proof: many
// accounts change positions concurrently while workers contend on the same
// projections. Every outcome is a success or an expected conflict, every
// stored chain stays contiguous with its projection, the aggregate sums
// match an independent reconstruction from the source rows, and the whole
// workload completes inside a deadline (no deadlocks).
func TestPositionIntegrityUnderConcurrentLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)
	repo := positionspg.NewRepository(pool)
	clock := clockseed.NewClock()

	confirm := application.NewConfirmInitialPositionUseCase(repo, eligibleAccounts{}, openArenas{}, clock)
	change := application.NewChangePositionUseCase(repo, openArenas{}, platformpg.NewTxManager(pool), clock)
	aggregateUseCase := application.NewGetPositionAggregateUseCase(repo, domain.AggregatePolicy{Version: "load", MinParticipants: 1}, clock)

	const (
		accounts          = 12
		workersPerAccount = 2
		changesPerWorker  = 4
		rounds            = 3
	)

	arena := mustPositionArena(t, ctx, pool, mustPositionAccount(t, ctx, q, "positions-load-owner@arena.example.com"))
	values := []string{domain.PositionAgree, domain.PositionDisagree, domain.PositionUndecided}

	// Many accounts confirm mixed initial positions.
	accountIDs := make([]pgtype.UUID, 0, accounts)
	initialByAccount := map[string]string{}
	for index := 0; index < accounts; index++ {
		account := mustPositionAccount(t, ctx, q, fmt.Sprintf("positions-load-%d@arena.example.com", index))
		initial := values[index%len(values)]
		if _, err := confirm.Execute(ctx, confirmCommandFor(arena, account, initial)); err != nil {
			t.Fatalf("confirm account %d: %v", index, err)
		}
		accountIDs = append(accountIDs, account)
		initialByAccount[uuidText(account)] = initial
	}

	arenaID, _ := positionDomainIDs(t, arena, accountIDs[0])

	successes := 0
	conflicts := 0

	for round := 0; round < rounds; round++ {
		var waitGroup sync.WaitGroup
		roundSuccesses := 0
		roundConflicts := 0
		var roundMu sync.Mutex

		for index, account := range accountIDs {
			for worker := 0; worker < workersPerAccount; worker++ {
				waitGroup.Add(1)
				go func(account pgtype.UUID, seed int) {
					defer waitGroup.Done()
					for step := 0; step < changesPerWorker; step++ {
						target := values[(seed+step)%len(values)]
						_, err := change.Execute(ctx, changeCommandFor(arena, account, target))
						switch {
						case err == nil:
							roundMu.Lock()
							roundSuccesses++
							roundMu.Unlock()
						case errors.Is(err, application.ErrVersionConflict), errors.Is(err, domain.ErrSamePosition):
							roundMu.Lock()
							roundConflicts++
							roundMu.Unlock()
						default:
							t.Errorf("unexpected change error: %v", err)
							return
						}
					}
				}(account, index+worker)
			}
		}
		waitGroup.Wait()

		successes += roundSuccesses
		conflicts += roundConflicts

		// After every round, every chain must match its projection and the
		// aggregate must equal the reconstruction from the source rows.
		assertPositionIntegrity(t, ctx, repo, arenaID, accountIDs, initialByAccount, arena, aggregateUseCase)
	}

	if successes < accounts {
		t.Fatalf("successes = %d, want at least one change per account", successes)
	}
	if conflicts < 1 {
		t.Fatal("the workload must exercise the version/same-position conflicts")
	}
	t.Logf("load: %d accounts, %d workers, %d rounds — %d accepted changes, %d conflicts",
		accounts, accounts*workersPerAccount, rounds, successes, conflicts)

	// The stored history is exactly the successful changes: the global
	// invariant that no append was lost or duplicated.
	storedChanges := 0
	for _, account := range accountIDs {
		_, accountID := positionDomainIDs(t, arena, account)
		projection, err := repo.GetByAccountAndArena(ctx, arenaID, accountID)
		if err != nil {
			t.Fatalf("read projection: %v", err)
		}
		storedChanges += int(projection.Version() - 1)
	}
	if storedChanges != successes {
		t.Fatalf("stored changes = %d, want one per success (%d)", storedChanges, successes)
	}
}

// assertPositionIntegrity proves the chain/projection consistency of every
// account and that the aggregate sums equal an independent reconstruction
// from the stored projections.
func assertPositionIntegrity(
	t *testing.T,
	ctx context.Context,
	repo *positionspg.Repository,
	arenaID domain.ArenaID,
	accountIDs []pgtype.UUID,
	initialByAccount map[string]string,
	arena pgtype.UUID,
	aggregateUseCase *application.GetPositionAggregateUseCase,
) {
	t.Helper()

	reconstructedInitial := application.PositionDistribution{}
	reconstructedCurrent := application.PositionDistribution{}

	for _, account := range accountIDs {
		_, accountID := positionDomainIDs(t, arena, account)
		projection, err := repo.GetByAccountAndArena(ctx, arenaID, accountID)
		if err != nil {
			t.Fatalf("read projection: %v", err)
		}

		// The initial position is immutable history.
		if projection.InitialPosition().String() != initialByAccount[uuidText(account)] {
			t.Fatalf("account %s initial position changed: %q", uuidText(account), projection.InitialPosition().String())
		}

		// The chain is contiguous and derivable, and the projection is its
		// tip: version = 1 + number of stored changes.
		records, err := repo.ListPositionChanges(ctx, arenaID, accountID)
		if err != nil {
			t.Fatalf("list changes: %v", err)
		}
		changes := make([]domain.PositionChange, 0, len(records))
		for index := len(records) - 1; index >= 0; index-- {
			changes = append(changes, records[index].Change)
		}
		derived, derivedVersion, err := domain.DeriveCurrentPosition(projection.InitialPosition(), changes)
		if err != nil {
			t.Fatalf("account %s chain is broken: %v", uuidText(account), err)
		}
		if !derived.Equals(projection.CurrentPosition()) || derivedVersion != projection.Version() {
			t.Fatalf("account %s projection diverges from its chain: %q v%d vs %q v%d",
				uuidText(account), derived.String(), derivedVersion, projection.CurrentPosition().String(), projection.Version())
		}
		if int(projection.Version()) != 1+len(changes) {
			t.Fatalf("account %s version = %d, want 1 + %d changes", uuidText(account), projection.Version(), len(changes))
		}

		addDistribution(&reconstructedInitial, projection.InitialPosition())
		addDistribution(&reconstructedCurrent, projection.CurrentPosition())
	}

	// The aggregate equals the reconstruction from the source rows.
	aggregate, err := aggregateUseCase.Execute(ctx, application.GetPositionAggregateQuery{ArenaID: uuidText(arena)})
	if err != nil {
		t.Fatalf("aggregate Execute() error = %v", err)
	}
	if aggregate.Suppressed {
		t.Fatal("the load aggregate must not be suppressed")
	}
	if aggregate.Total != int64(len(accountIDs)) {
		t.Fatalf("aggregate total = %d, want %d", aggregate.Total, len(accountIDs))
	}
	if aggregate.Initial != reconstructedInitial || aggregate.Current != reconstructedCurrent {
		t.Fatalf("aggregate diverges from the source: %+v/%+v vs %+v/%+v",
			aggregate.Initial, aggregate.Current, reconstructedInitial, reconstructedCurrent)
	}
}

// addDistribution accumulates one projection into a reconstruction.
func addDistribution(distribution *application.PositionDistribution, position domain.Position) {
	switch position.String() {
	case domain.PositionAgree:
		distribution.Agree++
	case domain.PositionDisagree:
		distribution.Disagree++
	case domain.PositionUndecided:
		distribution.Undecided++
	}
}
