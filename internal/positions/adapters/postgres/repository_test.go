package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	positionspg "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/positions/application"
	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// openArenas is a stub of the Arena eligibility port: tests choose the
// answer, since the arenas adapter arrives with the module bridges.
type openArenas struct{ err error }

func (o openArenas) EnsureAcceptsPositions(context.Context, domain.ArenaID) error { return o.err }

// eligibleAccounts is a stub of the account eligibility port.
type eligibleAccounts struct{ err error }

func (e eligibleAccounts) EnsureEligible(context.Context, domain.AccountID) error { return e.err }

func mustPositionAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) pgtype.UUID {
	t.Helper()
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return account.ID
}

// mustPositionArena creates a minimal Arena row through raw SQL; the Arena
// lifecycle belongs to the arenas module and eligibility is stubbed here.
func mustPositionArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Afirmação para o módulo de posições', 'technology', 'pt-BR', 'draft')
		RETURNING id`, creator).Scan(&id); err != nil {
		t.Fatalf("insert arena: %v", err)
	}
	return id
}

func positionDomainIDs(t *testing.T, arenaID, accountID pgtype.UUID) (domain.ArenaID, domain.AccountID) {
	t.Helper()
	arena, err := domain.ParseArenaID(uuidText(arenaID))
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	account, err := domain.ParseAccountID(uuidText(accountID))
	if err != nil {
		t.Fatalf("ParseAccountID: %v", err)
	}
	return arena, account
}

func uuidText(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func changeCommandFor(arenaID, accountID pgtype.UUID, position string) application.ChangePositionCommand {
	return application.ChangePositionCommand{
		AccountID: uuidText(accountID),
		ArenaID:   uuidText(arenaID),
		Position:  position,
	}
}

func confirmCommandFor(arenaID, accountID pgtype.UUID, position string) application.ConfirmInitialPositionCommand {
	return application.ConfirmInitialPositionCommand{
		AccountID: uuidText(accountID),
		ArenaID:   uuidText(arenaID),
		Position:  position,
	}
}

func changeRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, accountID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM app.position_changes
		WHERE arena_id = $1 AND account_id = $2`, arenaID, accountID).Scan(&count); err != nil {
		t.Fatalf("count changes: %v", err)
	}
	return count
}

// storedChanges reads the append-only chain ordered by version and rebuilds
// the domain values, so tests can prove the projection is derivable.
func storedChanges(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, accountID pgtype.UUID) []domain.PositionChange {
	t.Helper()
	arena, account := positionDomainIDs(t, arenaID, accountID)

	rows, err := pool.Query(ctx, `
		SELECT from_position, to_position, version, changed_at
		FROM app.position_changes
		WHERE arena_id = $1 AND account_id = $2
		ORDER BY version`, arenaID, accountID)
	if err != nil {
		t.Fatalf("query changes: %v", err)
	}
	defer rows.Close()

	var changes []domain.PositionChange
	for rows.Next() {
		var from, to string
		var version int32
		var changedAt time.Time
		if err := rows.Scan(&from, &to, &version, &changedAt); err != nil {
			t.Fatalf("scan change: %v", err)
		}
		fromPosition, err := domain.ParsePosition(from)
		if err != nil {
			t.Fatalf("stored from position: %v", err)
		}
		toPosition, err := domain.ParsePosition(to)
		if err != nil {
			t.Fatalf("stored to position: %v", err)
		}
		change, err := domain.NewPositionChange(arena, account, fromPosition, toPosition, version, changedAt)
		if err != nil {
			t.Fatalf("stored change is invalid: %v", err)
		}
		changes = append(changes, change)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate changes: %v", err)
	}
	return changes
}

func TestConfirmInitialPositionEndToEnd(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	account := mustPositionAccount(t, ctx, q, "positions-confirm@arena.example.com")
	arena := mustPositionArena(t, ctx, pool, account)
	repo := positionspg.NewRepository(pool)
	useCase := application.NewConfirmInitialPositionUseCase(repo, eligibleAccounts{}, openArenas{}, clockseed.NewClock())

	first, err := useCase.Execute(ctx, confirmCommandFor(arena, account, domain.PositionAgree))
	if err != nil {
		t.Fatalf("confirm Execute() error = %v", err)
	}
	if first.Replayed || first.Position.Version() != 1 {
		t.Fatalf("first confirmation = replayed %v v%d, want fresh v1", first.Replayed, first.Position.Version())
	}

	retry, err := useCase.Execute(ctx, confirmCommandFor(arena, account, domain.PositionAgree))
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed {
		t.Fatal("retry must resolve a replay")
	}

	if _, err := useCase.Execute(ctx, confirmCommandFor(arena, account, domain.PositionUndecided)); !errors.Is(err, application.ErrInitialPositionAlreadySet) {
		t.Fatalf("different value error = %v, want ErrInitialPositionAlreadySet", err)
	}

	var rows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM app.debate_positions
		WHERE arena_id = $1 AND account_id = $2`, arena, account).Scan(&rows); err != nil {
		t.Fatalf("count positions: %v", err)
	}
	if rows != 1 {
		t.Fatalf("stored projections = %d, want exactly one", rows)
	}
}

func TestChangePositionEndToEndAtomicChain(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	account := mustPositionAccount(t, ctx, q, "positions-chain@arena.example.com")
	arena := mustPositionArena(t, ctx, pool, account)
	repo := positionspg.NewRepository(pool)
	clock := clockseed.NewClock()

	confirm := application.NewConfirmInitialPositionUseCase(repo, eligibleAccounts{}, openArenas{}, clock)
	if _, err := confirm.Execute(ctx, confirmCommandFor(arena, account, domain.PositionAgree)); err != nil {
		t.Fatalf("confirm Execute() error = %v", err)
	}
	change := application.NewChangePositionUseCase(repo, openArenas{}, platformpg.NewTxManager(pool), clock)

	first, err := change.Execute(ctx, changeCommandFor(arena, account, domain.PositionDisagree))
	if err != nil {
		t.Fatalf("first change Execute() error = %v", err)
	}
	if first.ChangeID == "" {
		t.Fatal("change must return its stored identifier for later attribution")
	}
	if first.Position.Version() != 2 || !first.Position.CurrentPosition().Equals(mustPositionValue(t, domain.PositionDisagree)) {
		t.Fatalf("projection = v%d %q, want v2 disagree", first.Position.Version(), first.Position.CurrentPosition().String())
	}

	// The stored row matches the returned identifier and transition.
	var changeID pgtype.UUID
	if err := changeID.Scan(first.ChangeID); err != nil {
		t.Fatalf("change id %q is not a UUID: %v", first.ChangeID, err)
	}
	var from, to string
	var version int32
	if err := pool.QueryRow(ctx, `
		SELECT from_position, to_position, version FROM app.position_changes WHERE id = $1`, changeID,
	).Scan(&from, &to, &version); err != nil {
		t.Fatalf("read change row: %v", err)
	}
	if from != domain.PositionAgree || to != domain.PositionDisagree || version != 2 {
		t.Fatalf("stored change = %s -> %s v%d, want agree -> disagree v2", from, to, version)
	}

	second, err := change.Execute(ctx, changeCommandFor(arena, account, domain.PositionUndecided))
	if err != nil {
		t.Fatalf("second change Execute() error = %v", err)
	}
	if second.Position.Version() != 3 {
		t.Fatalf("projection version = %d, want 3", second.Position.Version())
	}

	// The projection is derivable from the append-only chain.
	changes := storedChanges(t, ctx, pool, arena, account)
	derived, derivedVersion, err := domain.DeriveCurrentPosition(mustPositionValue(t, domain.PositionAgree), changes)
	if err != nil {
		t.Fatalf("DeriveCurrentPosition: %v", err)
	}
	if !derived.Equals(second.Position.CurrentPosition()) || derivedVersion != second.Position.Version() {
		t.Fatalf("derived = %q v%d, want %q v%d", derived.String(), derivedVersion, second.Position.CurrentPosition().String(), second.Position.Version())
	}

	// A retried change to the current position is refused and appends
	// nothing; a closed Arena refuses new changes too.
	if _, err := change.Execute(ctx, changeCommandFor(arena, account, domain.PositionUndecided)); !errors.Is(err, domain.ErrSamePosition) {
		t.Fatalf("retry error = %v, want ErrSamePosition", err)
	}
	closed := application.NewChangePositionUseCase(repo, openArenas{err: application.ErrArenaNotOpen}, platformpg.NewTxManager(pool), clock)
	if _, err := closed.Execute(ctx, changeCommandFor(arena, account, domain.PositionAgree)); !errors.Is(err, application.ErrArenaNotOpen) {
		t.Fatalf("closed arena error = %v, want ErrArenaNotOpen", err)
	}
	if rows := changeRowCount(t, ctx, pool, arena, account); rows != 2 {
		t.Fatalf("stored changes = %d, want exactly two", rows)
	}
}

// TestChangePositionConcurrentRepositoryRace pins two transactions to the
// same version: the unique chain constraint and the optimistic update let
// exactly one advance and the other rolls back.
func TestChangePositionConcurrentRepositoryRace(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	account := mustPositionAccount(t, ctx, q, "positions-race@arena.example.com")
	arena := mustPositionArena(t, ctx, pool, account)
	repo := positionspg.NewRepository(pool)
	clock := clockseed.NewClock()

	confirm := application.NewConfirmInitialPositionUseCase(repo, eligibleAccounts{}, openArenas{}, clock)
	if _, err := confirm.Execute(ctx, confirmCommandFor(arena, account, domain.PositionAgree)); err != nil {
		t.Fatalf("confirm Execute() error = %v", err)
	}

	arenaID, accountID := positionDomainIDs(t, arena, account)
	uow := platformpg.NewTxManager(pool)
	from := mustPositionValue(t, domain.PositionAgree)
	targets := []string{domain.PositionDisagree, domain.PositionUndecided}

	start := make(chan struct{})
	errs := make(chan error, len(targets))
	var waitGroup sync.WaitGroup
	for _, target := range targets {
		change, err := domain.NewPositionChange(arenaID, accountID, from, mustPositionValue(t, target), 2, time.Now().UTC())
		if err != nil {
			t.Fatalf("NewPositionChange: %v", err)
		}
		waitGroup.Add(1)
		go func(change domain.PositionChange) {
			defer waitGroup.Done()
			<-start
			errs <- uow.WithinTransaction(ctx, func(txCtx context.Context) error {
				if _, err := repo.CreatePositionChange(txCtx, change); err != nil {
					return err
				}
				return repo.UpdateCurrentPosition(txCtx, change, 1)
			})
		}(change)
	}
	close(start)
	waitGroup.Wait()
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, application.ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes = %d, conflicts = %d, want exactly one of each", successes, conflicts)
	}
	if rows := changeRowCount(t, ctx, pool, arena, account); rows != 1 {
		t.Fatalf("stored changes = %d, want the loser rolled back", rows)
	}

	projection, err := repo.GetByAccountAndArena(ctx, arenaID, accountID)
	if err != nil {
		t.Fatalf("GetByAccountAndArena: %v", err)
	}
	if projection.Version() != 2 {
		t.Fatalf("projection version = %d, want 2", projection.Version())
	}
	changes := storedChanges(t, ctx, pool, arena, account)
	derived, derivedVersion, err := domain.DeriveCurrentPosition(mustPositionValue(t, domain.PositionAgree), changes)
	if err != nil {
		t.Fatalf("DeriveCurrentPosition: %v", err)
	}
	if !derived.Equals(projection.CurrentPosition()) || derivedVersion != projection.Version() {
		t.Fatal("projection diverged from the chain after the race")
	}
}

// TestChangePositionConcurrentUseCaseChainIntegrity drives many simultaneous
// use-case calls: every outcome is a success or a version conflict, and the
// stored chain always matches the projection.
func TestChangePositionConcurrentUseCaseChainIntegrity(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	account := mustPositionAccount(t, ctx, q, "positions-load@arena.example.com")
	arena := mustPositionArena(t, ctx, pool, account)
	repo := positionspg.NewRepository(pool)
	clock := clockseed.NewClock()

	confirm := application.NewConfirmInitialPositionUseCase(repo, eligibleAccounts{}, openArenas{}, clock)
	if _, err := confirm.Execute(ctx, confirmCommandFor(arena, account, domain.PositionAgree)); err != nil {
		t.Fatalf("confirm Execute() error = %v", err)
	}
	change := application.NewChangePositionUseCase(repo, openArenas{}, platformpg.NewTxManager(pool), clock)

	const workers = 10
	targets := []string{domain.PositionDisagree, domain.PositionUndecided}
	start := make(chan struct{})
	errs := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func(target string) {
			defer waitGroup.Done()
			<-start
			_, err := change.Execute(ctx, changeCommandFor(arena, account, target))
			errs <- err
		}(targets[index%len(targets)])
	}
	close(start)
	waitGroup.Wait()
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, application.ErrVersionConflict):
			conflicts++
		case errors.Is(err, domain.ErrSamePosition):
			// A concurrent change already reached the same target.
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes+conflicts != workers {
		t.Fatalf("outcomes = %d, want %d", successes+conflicts, workers)
	}
	if successes < 1 {
		t.Fatal("at least one change must succeed")
	}

	arenaID, accountID := positionDomainIDs(t, arena, account)
	projection, err := repo.GetByAccountAndArena(ctx, arenaID, accountID)
	if err != nil {
		t.Fatalf("GetByAccountAndArena: %v", err)
	}
	if rows := changeRowCount(t, ctx, pool, arena, account); rows != successes {
		t.Fatalf("stored changes = %d, want one per success (%d)", rows, successes)
	}
	if projection.Version() != int32(1+successes) {
		t.Fatalf("projection version = %d, want 1 + %d successes", projection.Version(), successes)
	}
	if !projection.InitialPosition().Equals(mustPositionValue(t, domain.PositionAgree)) {
		t.Fatal("the initial position changed")
	}

	changes := storedChanges(t, ctx, pool, arena, account)
	derived, derivedVersion, err := domain.DeriveCurrentPosition(mustPositionValue(t, domain.PositionAgree), changes)
	if err != nil {
		t.Fatalf("DeriveCurrentPosition: %v", err)
	}
	if !derived.Equals(projection.CurrentPosition()) || derivedVersion != projection.Version() {
		t.Fatal("projection diverged from the chain under concurrency")
	}
}

// TestChangePositionRollbackLeavesNoDivergence forces the projection update
// to fail after the history append: the whole transaction rolls back and the
// projection keeps matching the chain.
func TestChangePositionRollbackLeavesNoDivergence(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	account := mustPositionAccount(t, ctx, q, "positions-rollback@arena.example.com")
	arena := mustPositionArena(t, ctx, pool, account)
	repo := positionspg.NewRepository(pool)
	clock := clockseed.NewClock()

	confirm := application.NewConfirmInitialPositionUseCase(repo, eligibleAccounts{}, openArenas{}, clock)
	if _, err := confirm.Execute(ctx, confirmCommandFor(arena, account, domain.PositionAgree)); err != nil {
		t.Fatalf("confirm Execute() error = %v", err)
	}

	arenaID, accountID := positionDomainIDs(t, arena, account)
	change, err := domain.NewPositionChange(arenaID, accountID, mustPositionValue(t, domain.PositionAgree), mustPositionValue(t, domain.PositionDisagree), 2, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewPositionChange: %v", err)
	}

	uow := platformpg.NewTxManager(pool)
	err = uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if _, err := repo.CreatePositionChange(txCtx, change); err != nil {
			return err
		}
		// The projection is still at version 1: a stale expectation fails
		// the update and must roll the appended change back.
		return repo.UpdateCurrentPosition(txCtx, change, 7)
	})
	if !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("transaction error = %v, want ErrVersionConflict", err)
	}

	if rows := changeRowCount(t, ctx, pool, arena, account); rows != 0 {
		t.Fatalf("stored changes = %d, want the rollback to discard the append", rows)
	}
	projection, err := repo.GetByAccountAndArena(ctx, arenaID, accountID)
	if err != nil {
		t.Fatalf("GetByAccountAndArena: %v", err)
	}
	if projection.Version() != 1 || !projection.CurrentPosition().Equals(mustPositionValue(t, domain.PositionAgree)) {
		t.Fatalf("projection = v%d %q, want the untouched v1 agree", projection.Version(), projection.CurrentPosition().String())
	}
}

func TestListPositionChangesReturnsTheStoredChain(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	account := mustPositionAccount(t, ctx, q, "positions-history@arena.example.com")
	other := mustPositionAccount(t, ctx, q, "positions-history-other@arena.example.com")
	arena := mustPositionArena(t, ctx, pool, account)
	repo := positionspg.NewRepository(pool)
	clock := clockseed.NewClock()

	confirm := application.NewConfirmInitialPositionUseCase(repo, eligibleAccounts{}, openArenas{}, clock)
	if _, err := confirm.Execute(ctx, confirmCommandFor(arena, account, domain.PositionAgree)); err != nil {
		t.Fatalf("confirm Execute() error = %v", err)
	}
	if _, err := confirm.Execute(ctx, confirmCommandFor(arena, other, domain.PositionDisagree)); err != nil {
		t.Fatalf("other confirm Execute() error = %v", err)
	}

	change := application.NewChangePositionUseCase(repo, openArenas{}, platformpg.NewTxManager(pool), clock)
	first, err := change.Execute(ctx, changeCommandFor(arena, account, domain.PositionDisagree))
	if err != nil {
		t.Fatalf("first change Execute() error = %v", err)
	}
	second, err := change.Execute(ctx, changeCommandFor(arena, account, domain.PositionUndecided))
	if err != nil {
		t.Fatalf("second change Execute() error = %v", err)
	}

	list := application.NewListPositionChangesUseCase(repo)
	records, err := list.Execute(ctx, application.ListPositionChangesQuery{
		AccountID: uuidText(account),
		ArenaID:   uuidText(arena),
	})
	if err != nil {
		t.Fatalf("list Execute() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want the two owner changes", len(records))
	}
	if records[0].ID != second.ChangeID || records[1].ID != first.ChangeID {
		t.Fatalf("history ids = %q, %q, want newest first (%q, %q)", records[0].ID, records[1].ID, second.ChangeID, first.ChangeID)
	}
	if records[0].Change.Version() != 3 || records[1].Change.Version() != 2 {
		t.Fatalf("versions = %d, %d, want 3 then 2", records[0].Change.Version(), records[1].Change.Version())
	}
	if !records[0].Change.From().Equals(mustPositionValue(t, domain.PositionDisagree)) || !records[0].Change.To().Equals(mustPositionValue(t, domain.PositionUndecided)) {
		t.Fatal("history row lost its transition")
	}

	// The other account never sees the owner history.
	otherRecords, err := list.Execute(ctx, application.ListPositionChangesQuery{
		AccountID: uuidText(other),
		ArenaID:   uuidText(arena),
	})
	if err != nil {
		t.Fatalf("other list Execute() error = %v", err)
	}
	if len(otherRecords) != 0 {
		t.Fatalf("other records = %d, want none", len(otherRecords))
	}
}

func TestPositionRepositoryRejectsMalformedIdentifiers(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	repo := positionspg.NewRepository(pool)

	validArena := mustArenaID(t)
	validAccount := mustAccountID(t)
	malformedArena, err := domain.ParseArenaID("not-a-uuid")
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	malformedAccount, err := domain.ParseAccountID("not-a-uuid")
	if err != nil {
		t.Fatalf("ParseAccountID: %v", err)
	}

	if _, err := repo.GetByAccountAndArena(ctx, malformedArena, validAccount); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("malformed arena error = %v, want ErrArenaNotFound", err)
	}
	if _, err := repo.GetByAccountAndArena(ctx, validArena, malformedAccount); !errors.Is(err, application.ErrPositionNotFound) {
		t.Fatalf("malformed account error = %v, want ErrPositionNotFound", err)
	}
	if _, _, err := repo.ConfirmInitialPosition(ctx, malformedArena, validAccount, mustPositionValue(t, domain.PositionAgree), time.Now().UTC()); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("confirm malformed arena error = %v, want ErrArenaNotFound", err)
	}

	change, err := domain.NewPositionChange(malformedArena, validAccount, mustPositionValue(t, domain.PositionAgree), mustPositionValue(t, domain.PositionDisagree), 2, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewPositionChange: %v", err)
	}
	if _, err := repo.CreatePositionChange(ctx, change); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("create change malformed arena error = %v, want ErrArenaNotFound", err)
	}
	if err := repo.UpdateCurrentPosition(ctx, change, 1); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("update malformed arena error = %v, want ErrArenaNotFound", err)
	}
}

func mustArenaID(t *testing.T) domain.ArenaID {
	t.Helper()
	arenaID, err := domain.ParseArenaID(uuidText(pgtype.UUID{Bytes: [16]byte{1}, Valid: true}))
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	return arenaID
}

func mustAccountID(t *testing.T) domain.AccountID {
	t.Helper()
	accountID, err := domain.ParseAccountID(uuidText(pgtype.UUID{Bytes: [16]byte{2}, Valid: true}))
	if err != nil {
		t.Fatalf("ParseAccountID: %v", err)
	}
	return accountID
}

func mustPositionValue(t *testing.T, raw string) domain.Position {
	t.Helper()
	position, err := domain.ParsePosition(raw)
	if err != nil {
		t.Fatalf("ParsePosition(%q): %v", raw, err)
	}
	return position
}
