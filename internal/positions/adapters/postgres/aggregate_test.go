package postgres_test

import (
	"context"
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

func mustPositionAccountWithStatus(t *testing.T, ctx context.Context, q *platformpg.Queries, email, status string) pgtype.UUID {
	t.Helper()
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: status})
	if err != nil {
		t.Fatalf("create %s account %s: %v", status, email, err)
	}
	return account.ID
}

// confirmPosition seeds one confirmed projection through the real use case.
func confirmPosition(t *testing.T, ctx context.Context, repo *positionspg.Repository, arenaID, accountID pgtype.UUID, position string) {
	t.Helper()
	confirm := application.NewConfirmInitialPositionUseCase(repo, eligibleAccounts{}, openArenas{}, clockseed.NewClock())
	if _, err := confirm.Execute(ctx, confirmCommandFor(arenaID, accountID, position)); err != nil {
		t.Fatalf("confirm %s: %v", position, err)
	}
}

func TestPositionAggregateCountsEligibleAccountsOnly(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)
	repo := positionspg.NewRepository(pool)

	arena := mustPositionArena(t, ctx, pool, mustPositionAccount(t, ctx, q, "positions-aggregate@arena.example.com"))
	active1 := mustPositionAccount(t, ctx, q, "positions-active-1@arena.example.com")
	active2 := mustPositionAccount(t, ctx, q, "positions-active-2@arena.example.com")
	active3 := mustPositionAccount(t, ctx, q, "positions-active-3@arena.example.com")
	active4 := mustPositionAccount(t, ctx, q, "positions-active-4@arena.example.com")
	pending := mustPositionAccountWithStatus(t, ctx, q, "positions-pending@arena.example.com", "pending")
	suspended := mustPositionAccountWithStatus(t, ctx, q, "positions-suspended@arena.example.com", "suspended")
	deleted := mustPositionAccountWithStatus(t, ctx, q, "positions-deleted@arena.example.com", "deleted")

	confirmPosition(t, ctx, repo, arena, active1, domain.PositionAgree)
	confirmPosition(t, ctx, repo, arena, active2, domain.PositionDisagree)
	confirmPosition(t, ctx, repo, arena, active3, domain.PositionUndecided)
	confirmPosition(t, ctx, repo, arena, active4, domain.PositionAgree)
	confirmPosition(t, ctx, repo, arena, pending, domain.PositionUndecided)
	confirmPosition(t, ctx, repo, arena, suspended, domain.PositionAgree)
	confirmPosition(t, ctx, repo, arena, deleted, domain.PositionDisagree)

	// One eligible participant changes: initial and current diverge.
	arenaID, accountID := positionDomainIDs(t, arena, active4)
	change, err := domain.NewPositionChange(arenaID, accountID, mustPositionValue(t, domain.PositionAgree), mustPositionValue(t, domain.PositionDisagree), 2, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewPositionChange: %v", err)
	}
	uow := platformpg.NewTxManager(pool)
	if err := uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if _, err := repo.CreatePositionChange(txCtx, change); err != nil {
			return err
		}
		return repo.UpdateCurrentPosition(txCtx, change, 1)
	}); err != nil {
		t.Fatalf("apply change: %v", err)
	}

	policy := domain.AggregatePolicy{Version: "test", MinParticipants: 1}
	useCase := application.NewGetPositionAggregateUseCase(repo, policy, clockseed.NewClock())
	aggregate, err := useCase.Execute(ctx, application.GetPositionAggregateQuery{ArenaID: uuidText(arena)})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if aggregate.Suppressed || aggregate.Total != 4 {
		t.Fatalf("aggregate = total %d suppressed %v, want 4 eligible participants", aggregate.Total, aggregate.Suppressed)
	}
	if aggregate.Initial.Agree != 2 || aggregate.Initial.Disagree != 1 || aggregate.Initial.Undecided != 1 {
		t.Fatalf("initial distribution = %+v, want agree 2 / disagree 1 / undecided 1", aggregate.Initial)
	}
	if aggregate.Current.Agree != 1 || aggregate.Current.Disagree != 2 || aggregate.Current.Undecided != 1 {
		t.Fatalf("current distribution = %+v, want agree 1 / disagree 2 / undecided 1", aggregate.Current)
	}

	// Reconstruct the aggregate from the raw rows: the derived counts must
	// match an independent recomputation.
	var initialAgree, initialDisagree, initialUndecided int64
	if err := pool.QueryRow(ctx, `
		SELECT
		    count(*) FILTER (WHERE dp.initial_position = 'agree'),
		    count(*) FILTER (WHERE dp.initial_position = 'disagree'),
		    count(*) FILTER (WHERE dp.initial_position = 'undecided')
		FROM app.debate_positions dp
		JOIN app.accounts a ON a.id = dp.account_id AND a.status = 'active'
		WHERE dp.arena_id = $1`, arena).Scan(&initialAgree, &initialDisagree, &initialUndecided); err != nil {
		t.Fatalf("reconstruct aggregate: %v", err)
	}
	if aggregate.Initial.Agree != initialAgree || aggregate.Initial.Disagree != initialDisagree || aggregate.Initial.Undecided != initialUndecided {
		t.Fatalf("aggregate %+v diverges from the reconstructed source (%d/%d/%d)", aggregate.Initial, initialAgree, initialDisagree, initialUndecided)
	}
}

func TestPositionAggregateInvalidationExcludesAccounts(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)
	repo := positionspg.NewRepository(pool)

	arena := mustPositionArena(t, ctx, pool, mustPositionAccount(t, ctx, q, "positions-invalidation@arena.example.com"))
	agree := mustPositionAccount(t, ctx, q, "positions-invalidation-agree@arena.example.com")
	disagree := mustPositionAccount(t, ctx, q, "positions-invalidation-disagree@arena.example.com")
	undecided := mustPositionAccount(t, ctx, q, "positions-invalidation-undecided@arena.example.com")

	confirmPosition(t, ctx, repo, arena, agree, domain.PositionAgree)
	confirmPosition(t, ctx, repo, arena, disagree, domain.PositionDisagree)
	confirmPosition(t, ctx, repo, arena, undecided, domain.PositionUndecided)

	policy := domain.AggregatePolicy{Version: "test", MinParticipants: 1}
	useCase := application.NewGetPositionAggregateUseCase(repo, policy, clockseed.NewClock())

	aggregate, err := useCase.Execute(ctx, application.GetPositionAggregateQuery{ArenaID: uuidText(arena)})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if aggregate.Total != 3 {
		t.Fatalf("total = %d, want 3 before invalidation", aggregate.Total)
	}

	// Suspension removes the account from the official totals.
	if _, err := pool.Exec(ctx, "UPDATE app.accounts SET status = 'suspended' WHERE id = $1", agree); err != nil {
		t.Fatalf("suspend account: %v", err)
	}
	aggregate, err = useCase.Execute(ctx, application.GetPositionAggregateQuery{ArenaID: uuidText(arena)})
	if err != nil {
		t.Fatalf("Execute() after suspension error = %v", err)
	}
	if aggregate.Total != 2 || aggregate.Initial.Agree != 0 {
		t.Fatalf("aggregate after suspension = total %d agree %d, want 2/0", aggregate.Total, aggregate.Initial.Agree)
	}

	// Deletion removes the account too; the history stays stored.
	if _, err := pool.Exec(ctx, "UPDATE app.accounts SET status = 'deleted' WHERE id = $1", disagree); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	aggregate, err = useCase.Execute(ctx, application.GetPositionAggregateQuery{ArenaID: uuidText(arena)})
	if err != nil {
		t.Fatalf("Execute() after deletion error = %v", err)
	}
	if aggregate.Total != 1 || aggregate.Initial.Disagree != 0 || aggregate.Current.Undecided != 1 {
		t.Fatalf("aggregate after deletion = %+v, want the single undecided participant", aggregate)
	}
}

func TestPositionAggregateThresholdSuppressesSmallSamples(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)
	repo := positionspg.NewRepository(pool)

	arena := mustPositionArena(t, ctx, pool, mustPositionAccount(t, ctx, q, "positions-threshold@arena.example.com"))
	first := mustPositionAccount(t, ctx, q, "positions-threshold-1@arena.example.com")
	second := mustPositionAccount(t, ctx, q, "positions-threshold-2@arena.example.com")
	confirmPosition(t, ctx, repo, arena, first, domain.PositionAgree)
	confirmPosition(t, ctx, repo, arena, second, domain.PositionDisagree)

	query := application.GetPositionAggregateQuery{ArenaID: uuidText(arena)}

	suppressing := application.NewGetPositionAggregateUseCase(repo, domain.AggregatePolicy{Version: "test", MinParticipants: 3}, clockseed.NewClock())
	aggregate, err := suppressing.Execute(ctx, query)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !aggregate.Suppressed || aggregate.Total != 0 || aggregate.Initial.Total() != 0 || aggregate.Current.Total() != 0 {
		t.Fatalf("below-threshold aggregate = %+v, want every count withheld", aggregate)
	}

	boundary := application.NewGetPositionAggregateUseCase(repo, domain.AggregatePolicy{Version: "test", MinParticipants: 2}, clockseed.NewClock())
	aggregate, err = boundary.Execute(ctx, query)
	if err != nil {
		t.Fatalf("Execute() at the threshold error = %v", err)
	}
	if aggregate.Suppressed || aggregate.Total != 2 {
		t.Fatalf("at-threshold aggregate = %+v, want the distribution published", aggregate)
	}
}
