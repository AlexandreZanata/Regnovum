package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/positions/application"
	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

func seedProjection(t *testing.T, repo *fakePositionRepo, initial string) *domain.DebatePosition {
	t.Helper()
	projection, err := domain.ConfirmInitialPosition(mustArenaID(t), mustAccountID(t), mustPosition(t, initial), testInstant)
	if err != nil {
		t.Fatalf("seed projection: %v", err)
	}
	repo.seed(t, projection)
	return projection
}

func newChangeUseCase(repo *fakePositionRepo, arenas *fakeArenaEligibility, uow *fakeUnitOfWork) *application.ChangePositionUseCase {
	if uow.repo == nil {
		uow.repo = repo
	}
	return application.NewChangePositionUseCase(repo, arenas, uow, fixedClock{now: testInstant.Add(time.Hour)})
}

func changeCommand(position string) application.ChangePositionCommand {
	return application.ChangePositionCommand{
		AccountID: testAccountRaw,
		ArenaID:   testArenaRaw,
		Position:  position,
	}
}

func TestChangePositionRecordsTheTransitionAtomically(t *testing.T) {
	repo := newFakePositionRepo()
	seedProjection(t, repo, domain.PositionAgree)
	arenas := &fakeArenaEligibility{}
	uow := newFakeUnitOfWork(repo)
	useCase := newChangeUseCase(repo, arenas, uow)

	result, err := useCase.Execute(context.Background(), changeCommand(domain.PositionDisagree))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ChangeID != "change-1" {
		t.Fatalf("ChangeID = %q, want the stored identifier", result.ChangeID)
	}
	if result.Position.Version() != 2 || !result.Position.CurrentPosition().Equals(mustPosition(t, domain.PositionDisagree)) {
		t.Fatal("projection must advance to the change target")
	}
	if !result.Position.InitialPosition().Equals(mustPosition(t, domain.PositionAgree)) {
		t.Fatal("the change must never touch the initial position")
	}
	if repo.changeCount() != 1 || repo.updateCount() != 1 {
		t.Fatalf("changes = %d, updates = %d, want one of each", repo.changeCount(), repo.updateCount())
	}
	if uow.callCount() != 1 {
		t.Fatalf("transactions = %d, want exactly one", uow.callCount())
	}
	if arenas.callCount() != 1 {
		t.Fatalf("arena checks = %d, want exactly one", arenas.callCount())
	}

	// The appended history row is the recorded transition.
	repo.mu.Lock()
	change := repo.changes[0]
	repo.mu.Unlock()
	if !change.From().Equals(mustPosition(t, domain.PositionAgree)) || !change.To().Equals(mustPosition(t, domain.PositionDisagree)) {
		t.Fatal("history row must record from and to")
	}
	if change.Version() != 2 || !change.ChangedAt().Equal(testInstant.Add(time.Hour)) {
		t.Fatal("history row must record the resulting version and the clock instant")
	}

	// The projection update used the version read before the transaction.
	repo.mu.Lock()
	update := repo.updates[0]
	repo.mu.Unlock()
	if update.expectedVersion != 1 || update.change.Version() != 2 {
		t.Fatalf("update expected version = %d, want the pre-change version 1", update.expectedVersion)
	}
}

func TestChangePositionRequiresAnExistingProjection(t *testing.T) {
	repo := newFakePositionRepo()
	arenas := &fakeArenaEligibility{}
	uow := newFakeUnitOfWork(repo)
	useCase := newChangeUseCase(repo, arenas, uow)

	if _, err := useCase.Execute(context.Background(), changeCommand(domain.PositionDisagree)); !errors.Is(err, application.ErrPositionNotFound) {
		t.Fatalf("error = %v, want ErrPositionNotFound", err)
	}
	if uow.callCount() != 0 || arenas.callCount() != 0 || repo.changeCount() != 0 {
		t.Fatal("a missing projection must not open a transaction or check the arena")
	}
}

func TestChangePositionRefusesClosedArenaWithoutWriting(t *testing.T) {
	repo := newFakePositionRepo()
	seedProjection(t, repo, domain.PositionAgree)
	arenas := &fakeArenaEligibility{err: application.ErrArenaNotOpen}
	uow := newFakeUnitOfWork(repo)
	useCase := newChangeUseCase(repo, arenas, uow)

	if _, err := useCase.Execute(context.Background(), changeCommand(domain.PositionDisagree)); !errors.Is(err, application.ErrArenaNotOpen) {
		t.Fatalf("error = %v, want ErrArenaNotOpen", err)
	}
	if uow.callCount() != 0 || repo.changeCount() != 0 || repo.updateCount() != 0 {
		t.Fatal("a closed arena must not write")
	}

	stored := repo.storedPosition(t, mustArenaID(t), mustAccountID(t))
	if stored.Version() != 1 || !stored.CurrentPosition().Equals(mustPosition(t, domain.PositionAgree)) {
		t.Fatal("a closed arena must not move the projection")
	}
}

func TestChangePositionRefusesTheSamePositionWithoutTransaction(t *testing.T) {
	repo := newFakePositionRepo()
	seedProjection(t, repo, domain.PositionAgree)
	arenas := &fakeArenaEligibility{}
	uow := newFakeUnitOfWork(repo)
	useCase := newChangeUseCase(repo, arenas, uow)

	if _, err := useCase.Execute(context.Background(), changeCommand(domain.PositionAgree)); !errors.Is(err, domain.ErrSamePosition) {
		t.Fatalf("error = %v, want ErrSamePosition", err)
	}
	if uow.callCount() != 0 || repo.changeCount() != 0 {
		t.Fatal("a same-position target must not open a transaction")
	}
}

func TestChangePositionIsNaturallyIdempotentAfterSuccess(t *testing.T) {
	repo := newFakePositionRepo()
	seedProjection(t, repo, domain.PositionAgree)
	arenas := &fakeArenaEligibility{}
	uow := newFakeUnitOfWork(repo)
	useCase := newChangeUseCase(repo, arenas, uow)

	if _, err := useCase.Execute(context.Background(), changeCommand(domain.PositionDisagree)); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	// A retried change targets the position already reached: no duplicate
	// history row is appended.
	if _, err := useCase.Execute(context.Background(), changeCommand(domain.PositionDisagree)); !errors.Is(err, domain.ErrSamePosition) {
		t.Fatalf("retry error = %v, want ErrSamePosition", err)
	}
	if repo.changeCount() != 1 {
		t.Fatalf("changes = %d, want exactly one", repo.changeCount())
	}
}

func TestChangePositionValidatesInputsWithoutTouchingPorts(t *testing.T) {
	probes := []struct {
		name    string
		command application.ChangePositionCommand
		want    error
	}{
		{name: "empty account", command: application.ChangePositionCommand{ArenaID: testArenaRaw, Position: domain.PositionDisagree}, want: domain.ErrEmptyAccountID},
		{name: "empty arena", command: application.ChangePositionCommand{AccountID: testAccountRaw, Position: domain.PositionDisagree}, want: domain.ErrEmptyArenaID},
		{name: "empty position", command: application.ChangePositionCommand{AccountID: testAccountRaw, ArenaID: testArenaRaw}, want: domain.ErrEmptyPosition},
		{name: "unknown position", command: application.ChangePositionCommand{AccountID: testAccountRaw, ArenaID: testArenaRaw, Position: "maybe"}, want: domain.ErrInvalidPosition},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			repo := newFakePositionRepo()
			arenas := &fakeArenaEligibility{}
			uow := newFakeUnitOfWork(repo)
			useCase := newChangeUseCase(repo, arenas, uow)

			if _, err := useCase.Execute(context.Background(), probe.command); !errors.Is(err, probe.want) {
				t.Fatalf("error = %v, want %v", err, probe.want)
			}
			if repo.changeCount() != 0 || uow.callCount() != 0 || arenas.callCount() != 0 {
				t.Fatal("invalid input must not touch any port")
			}
		})
	}
}

func TestChangePositionPropagatesStorageAndTransactionFailures(t *testing.T) {
	storageErr := errors.New("storage down")

	t.Run("read failure", func(t *testing.T) {
		repo := newFakePositionRepo()
		repo.getErr = storageErr
		uow := newFakeUnitOfWork(repo)
		useCase := newChangeUseCase(repo, &fakeArenaEligibility{}, uow)
		if _, err := useCase.Execute(context.Background(), changeCommand(domain.PositionDisagree)); !errors.Is(err, storageErr) {
			t.Fatalf("error = %v, want the storage failure", err)
		}
	})

	t.Run("transaction failure", func(t *testing.T) {
		repo := newFakePositionRepo()
		seedProjection(t, repo, domain.PositionAgree)
		uow := newFakeUnitOfWork(repo)
		uow.err = storageErr
		useCase := newChangeUseCase(repo, &fakeArenaEligibility{}, uow)
		if _, err := useCase.Execute(context.Background(), changeCommand(domain.PositionDisagree)); !errors.Is(err, storageErr) {
			t.Fatalf("error = %v, want the transaction failure", err)
		}
		if repo.changeCount() != 0 {
			t.Fatal("a failed transaction must not write")
		}
	})

	t.Run("append failure", func(t *testing.T) {
		repo := newFakePositionRepo()
		seedProjection(t, repo, domain.PositionAgree)
		repo.createErr = storageErr
		uow := newFakeUnitOfWork(repo)
		useCase := newChangeUseCase(repo, &fakeArenaEligibility{}, uow)
		if _, err := useCase.Execute(context.Background(), changeCommand(domain.PositionDisagree)); !errors.Is(err, storageErr) {
			t.Fatalf("error = %v, want the append failure", err)
		}
		if repo.updateCount() != 0 {
			t.Fatal("the projection must not move after a failed append")
		}
	})

	t.Run("projection failure rolls the append back", func(t *testing.T) {
		repo := newFakePositionRepo()
		seedProjection(t, repo, domain.PositionAgree)
		repo.updateErr = storageErr
		uow := newFakeUnitOfWork(repo)
		useCase := newChangeUseCase(repo, &fakeArenaEligibility{}, uow)
		if _, err := useCase.Execute(context.Background(), changeCommand(domain.PositionDisagree)); !errors.Is(err, storageErr) {
			t.Fatalf("error = %v, want the projection failure", err)
		}
		if repo.changeCount() != 0 {
			t.Fatalf("changes = %d, want the failed transaction rolled back", repo.changeCount())
		}
		stored := repo.storedPosition(t, mustArenaID(t), mustAccountID(t))
		if stored.Version() != 1 || !stored.CurrentPosition().Equals(mustPosition(t, domain.PositionAgree)) {
			t.Fatal("rollback must leave the projection untouched")
		}
	})
}

// TestChangePositionConcurrentChangesUseTheVersionCheck is the P09-T04
// concurrency proof at the application level: two changes read the same
// version, only one advances the chain and the loser reports a version
// conflict with its append rolled back.
func TestChangePositionConcurrentChangesUseTheVersionCheck(t *testing.T) {
	repo := newFakePositionRepo()
	seedProjection(t, repo, domain.PositionAgree)
	arenas := &fakeArenaEligibility{}
	uow := newFakeUnitOfWork(repo)
	useCase := newChangeUseCase(repo, arenas, uow)

	// Both changes read the same version before either transaction starts.
	barrier := &sync.WaitGroup{}
	barrier.Add(2)
	repo.getBarrier = barrier

	values := []string{domain.PositionDisagree, domain.PositionUndecided}
	start := make(chan struct{})
	errs := make(chan error, len(values))
	var waitGroup sync.WaitGroup
	for _, value := range values {
		waitGroup.Add(1)
		go func(value string) {
			defer waitGroup.Done()
			<-start
			_, err := useCase.Execute(context.Background(), changeCommand(value))
			errs <- err
		}(value)
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
	if repo.changeCount() != 1 || repo.updateCount() != 1 {
		t.Fatalf("changes = %d, updates = %d, want the loser rolled back", repo.changeCount(), repo.updateCount())
	}

	stored := repo.storedPosition(t, mustArenaID(t), mustAccountID(t))
	if stored.Version() != 2 {
		t.Fatalf("projection version = %d, want 2", stored.Version())
	}
	if stored.CurrentPosition().Equals(mustPosition(t, domain.PositionAgree)) {
		t.Fatal("the projection must have moved")
	}
}
