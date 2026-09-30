package application_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/positions/application"
	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

const (
	testArenaRaw   = "018f6b2a-0000-7000-8000-000000000001"
	testAccountRaw = "018f6b2a-0000-7000-8000-000000000002"
)

var testInstant = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type fakeAccountEligibility struct {
	mu    sync.Mutex
	err   error
	calls []domain.AccountID
}

func (f *fakeAccountEligibility) EnsureEligible(_ context.Context, accountID domain.AccountID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, accountID)
	return f.err
}

func (f *fakeAccountEligibility) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeArenaEligibility struct {
	mu    sync.Mutex
	err   error
	calls []domain.ArenaID
}

func (f *fakeArenaEligibility) EnsureAcceptsPositions(_ context.Context, arenaID domain.ArenaID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, arenaID)
	return f.err
}

func (f *fakeArenaEligibility) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeTx is the transaction-local overlay of the fake repository. Writes
// land here until the fake unit of work commits them, so overlapping
// transactions isolate like real database transactions.
type fakeTx struct {
	stored  map[string]*domain.DebatePosition
	changes []domain.PositionChange
	updates []updateCall
}

type fakeTxKey struct{}

type fakePositionRepo struct {
	mu sync.Mutex
	// txMu serializes fake transactions, like the row locks of the real
	// store: a version check inside a transaction sees the committed state
	// of every transaction that finished before it.
	txMu sync.Mutex
	// getBarrier, when set, holds reads until every participant loaded, so
	// tests can pin concurrent readers to the same version.
	getBarrier *sync.WaitGroup

	stored  map[string]*domain.DebatePosition
	inserts int
	changes []domain.PositionChange
	updates []updateCall
	nextID  int

	createErr error
	updateErr error

	getErr             error
	firstGetErr        error
	confirmErr         error
	confirmNotInserted bool
}

// updateCall records one projection update attempt of the fake.
type updateCall struct {
	change          domain.PositionChange
	expectedVersion int32
}

func newFakePositionRepo() *fakePositionRepo {
	return &fakePositionRepo{stored: map[string]*domain.DebatePosition{}}
}

func positionKey(arenaID domain.ArenaID, accountID domain.AccountID) string {
	return arenaID.String() + "|" + accountID.String()
}

// copyProjection hands out independent entities, like the database adapter
// does when it reconstitutes a row: use cases mutate the projection they
// loaded, so sharing the stored pointer would leak the mutation.
func copyProjection(projection *domain.DebatePosition) *domain.DebatePosition {
	copied, err := domain.ReconstituteDebatePosition(
		projection.ArenaID(), projection.AccountID(),
		projection.InitialPosition(), projection.CurrentPosition(),
		projection.Version(), projection.CreatedAt(), projection.UpdatedAt(),
	)
	if err != nil {
		panic(err)
	}
	return copied
}

// overlay returns the transaction-local state carried by the context.
func overlay(ctx context.Context) *fakeTx {
	tx, _ := ctx.Value(fakeTxKey{}).(*fakeTx)
	return tx
}

// effectiveStored resolves the visible projection of a pair: the
// transaction overlay wins over the committed store.
func (r *fakePositionRepo) effectiveStored(ctx context.Context, key string) (*domain.DebatePosition, bool) {
	if tx := overlay(ctx); tx != nil {
		if stored, ok := tx.stored[key]; ok {
			return stored, true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.stored[key]
	return stored, ok
}

func (r *fakePositionRepo) GetByAccountAndArena(ctx context.Context, arenaID domain.ArenaID, accountID domain.AccountID) (*domain.DebatePosition, error) {
	r.mu.Lock()
	if r.firstGetErr != nil {
		err := r.firstGetErr
		r.firstGetErr = nil
		r.mu.Unlock()
		return nil, err
	}
	if r.getErr != nil {
		err := r.getErr
		r.mu.Unlock()
		return nil, err
	}
	r.mu.Unlock()

	stored, ok := r.effectiveStored(ctx, positionKey(arenaID, accountID))
	if !ok {
		return nil, application.ErrPositionNotFound
	}
	if r.getBarrier != nil {
		r.getBarrier.Done()
		r.getBarrier.Wait()
	}
	return copyProjection(stored), nil
}

func (r *fakePositionRepo) ConfirmInitialPosition(ctx context.Context, arenaID domain.ArenaID, accountID domain.AccountID, position domain.Position, at time.Time) (*domain.DebatePosition, bool, error) {
	key := positionKey(arenaID, accountID)

	if tx := overlay(ctx); tx != nil {
		r.mu.Lock()
		confirmErr := r.confirmErr
		notInserted := r.confirmNotInserted
		committed, committedOK := r.stored[key]
		r.mu.Unlock()
		if confirmErr != nil {
			return nil, false, confirmErr
		}
		if stored, ok := tx.stored[key]; ok {
			return copyProjection(stored), false, nil
		}
		if committedOK {
			return copyProjection(committed), false, nil
		}
		if notInserted {
			return nil, false, nil
		}
		created, err := domain.ConfirmInitialPosition(arenaID, accountID, position, at)
		if err != nil {
			return nil, false, err
		}
		tx.stored[key] = created
		return copyProjection(created), true, nil
	}

	// Direct (autocommit) calls check and insert atomically, like the
	// unique primary key of the real table.
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.confirmErr != nil {
		return nil, false, r.confirmErr
	}
	if stored, ok := r.stored[key]; ok {
		return copyProjection(stored), false, nil
	}
	if r.confirmNotInserted {
		return nil, false, nil
	}
	created, err := domain.ConfirmInitialPosition(arenaID, accountID, position, at)
	if err != nil {
		return nil, false, err
	}
	r.stored[key] = created
	r.inserts++
	return copyProjection(created), true, nil
}

func (r *fakePositionRepo) CreatePositionChange(ctx context.Context, change domain.PositionChange) (string, error) {
	r.mu.Lock()
	if r.createErr != nil {
		err := r.createErr
		r.mu.Unlock()
		return "", err
	}
	r.nextID++
	id := fmt.Sprintf("change-%d", r.nextID)
	r.mu.Unlock()

	if tx := overlay(ctx); tx != nil {
		tx.changes = append(tx.changes, change)
	} else {
		r.mu.Lock()
		r.changes = append(r.changes, change)
		r.mu.Unlock()
	}
	return id, nil
}

func (r *fakePositionRepo) UpdateCurrentPosition(ctx context.Context, change domain.PositionChange, expectedVersion int32) error {
	r.mu.Lock()
	if r.updateErr != nil {
		err := r.updateErr
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	key := positionKey(change.ArenaID(), change.AccountID())
	stored, ok := r.effectiveStored(ctx, key)
	if !ok {
		return application.ErrPositionNotFound
	}
	if stored.Version() != expectedVersion {
		return application.ErrVersionConflict
	}

	updated, err := domain.ReconstituteDebatePosition(
		change.ArenaID(), change.AccountID(),
		stored.InitialPosition(), change.To(),
		change.Version(), stored.CreatedAt(), change.ChangedAt(),
	)
	if err != nil {
		return err
	}
	r.writeStored(ctx, key, updated)

	if tx := overlay(ctx); tx != nil {
		tx.updates = append(tx.updates, updateCall{change: change, expectedVersion: expectedVersion})
	} else {
		r.mu.Lock()
		r.updates = append(r.updates, updateCall{change: change, expectedVersion: expectedVersion})
		r.mu.Unlock()
	}
	return nil
}

// writeStored records a projection in the transaction overlay when one is
// active, otherwise in the committed store.
func (r *fakePositionRepo) writeStored(ctx context.Context, key string, projection *domain.DebatePosition) {
	if tx := overlay(ctx); tx != nil {
		if tx.stored == nil {
			tx.stored = map[string]*domain.DebatePosition{}
		}
		tx.stored[key] = projection
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stored[key] = projection
	r.inserts++
}

func (r *fakePositionRepo) ListPositionChanges(_ context.Context, arenaID domain.ArenaID, accountID domain.AccountID) ([]application.PositionChangeRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	matching := make([]domain.PositionChange, 0, len(r.changes))
	for _, change := range r.changes {
		if change.ArenaID().Equals(arenaID) && change.AccountID().Equals(accountID) {
			matching = append(matching, change)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].Version() > matching[j].Version() })

	records := make([]application.PositionChangeRecord, 0, len(matching))
	for index, change := range matching {
		records = append(records, application.PositionChangeRecord{
			ID:     fmt.Sprintf("change-%d", len(matching)-index),
			Change: change,
		})
	}
	return records, nil
}

func (r *fakePositionRepo) changeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.changes)
}

func (r *fakePositionRepo) updateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.updates)
}

type fakeUnitOfWork struct {
	mu    sync.Mutex
	calls int
	err   error
	repo  *fakePositionRepo
}

// newFakeUnitOfWork builds a unit of work that commits the transaction
// overlay only when the transactional function succeeds.
func newFakeUnitOfWork(repo *fakePositionRepo) *fakeUnitOfWork {
	return &fakeUnitOfWork{repo: repo}
}

func (u *fakeUnitOfWork) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	if u.err != nil {
		return u.err
	}

	if u.repo != nil {
		u.repo.txMu.Lock()
		defer u.repo.txMu.Unlock()
	}

	tx := &fakeTx{stored: map[string]*domain.DebatePosition{}}
	if err := fn(context.WithValue(ctx, fakeTxKey{}, tx)); err != nil {
		return err
	}
	u.repo.commit(tx)
	return nil
}

// commit merges one successful transaction overlay into the committed store.
func (r *fakePositionRepo) commit(tx *fakeTx) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, projection := range tx.stored {
		r.stored[key] = projection
		r.inserts++
	}
	r.changes = append(r.changes, tx.changes...)
	r.updates = append(r.updates, tx.updates...)
}

func (u *fakeUnitOfWork) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func (r *fakePositionRepo) insertCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inserts
}

func (r *fakePositionRepo) storedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.stored)
}

func (r *fakePositionRepo) storedPosition(t *testing.T, arenaID domain.ArenaID, accountID domain.AccountID) *domain.DebatePosition {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.stored[positionKey(arenaID, accountID)]
	if !ok {
		t.Fatal("no stored position")
	}
	return stored
}

func (r *fakePositionRepo) seed(t *testing.T, projection *domain.DebatePosition) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stored[positionKey(projection.ArenaID(), projection.AccountID())] = projection
}

func newConfirmUseCase(repo *fakePositionRepo, accounts *fakeAccountEligibility, arenas *fakeArenaEligibility) *application.ConfirmInitialPositionUseCase {
	return application.NewConfirmInitialPositionUseCase(repo, accounts, arenas, fixedClock{now: testInstant})
}

func confirmCommand(position string) application.ConfirmInitialPositionCommand {
	return application.ConfirmInitialPositionCommand{
		AccountID: testAccountRaw,
		ArenaID:   testArenaRaw,
		Position:  position,
	}
}

func mustArenaID(t *testing.T) domain.ArenaID {
	t.Helper()
	arenaID, err := domain.ParseArenaID(testArenaRaw)
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	return arenaID
}

func mustAccountID(t *testing.T) domain.AccountID {
	t.Helper()
	accountID, err := domain.ParseAccountID(testAccountRaw)
	if err != nil {
		t.Fatalf("ParseAccountID: %v", err)
	}
	return accountID
}

func mustPosition(t *testing.T, raw string) domain.Position {
	t.Helper()
	position, err := domain.ParsePosition(raw)
	if err != nil {
		t.Fatalf("ParsePosition(%q): %v", raw, err)
	}
	return position
}

func TestConfirmInitialPositionRecordsTheFirstChoice(t *testing.T) {
	repo := newFakePositionRepo()
	accounts := &fakeAccountEligibility{}
	arenas := &fakeArenaEligibility{}
	useCase := newConfirmUseCase(repo, accounts, arenas)

	result, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed {
		t.Fatal("first confirmation must not be a replay")
	}
	if !result.Position.InitialPosition().Equals(mustPosition(t, domain.PositionAgree)) {
		t.Fatal("initial position was not recorded")
	}
	if !result.Position.CurrentPosition().Equals(result.Position.InitialPosition()) || result.Position.Version() != 1 {
		t.Fatal("confirmation must start current equal to initial at version 1")
	}
	if !result.Position.CreatedAt().Equal(testInstant) {
		t.Fatal("confirmation must use the injected clock")
	}
	if repo.insertCount() != 1 || repo.storedCount() != 1 {
		t.Fatalf("inserts = %d, stored = %d, want exactly one", repo.insertCount(), repo.storedCount())
	}
	if accounts.callCount() != 1 || arenas.callCount() != 1 {
		t.Fatal("first confirmation must check account and arena eligibility exactly once")
	}
}

func TestConfirmInitialPositionIsIdempotentOnRetry(t *testing.T) {
	repo := newFakePositionRepo()
	accounts := &fakeAccountEligibility{}
	arenas := &fakeArenaEligibility{}
	useCase := newConfirmUseCase(repo, accounts, arenas)

	first, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionDisagree))
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	retry, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionDisagree))
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed {
		t.Fatal("retry must resolve a replay")
	}
	if retry.Position.Version() != first.Position.Version() || !retry.Position.InitialPosition().Equals(first.Position.InitialPosition()) {
		t.Fatal("replay must return the recorded projection")
	}
	if repo.insertCount() != 1 || repo.storedCount() != 1 {
		t.Fatal("retry must not write again")
	}
	if accounts.callCount() != 1 || arenas.callCount() != 1 {
		t.Fatal("replay must not re-check eligibility")
	}
}

func TestConfirmInitialPositionRefusesDifferentInitialValue(t *testing.T) {
	repo := newFakePositionRepo()
	accounts := &fakeAccountEligibility{}
	arenas := &fakeArenaEligibility{}
	useCase := newConfirmUseCase(repo, accounts, arenas)

	if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree)); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionUndecided)); !errors.Is(err, application.ErrInitialPositionAlreadySet) {
		t.Fatalf("different value error = %v, want ErrInitialPositionAlreadySet", err)
	}

	stored := repo.storedPosition(t, mustArenaID(t), mustAccountID(t))
	if !stored.InitialPosition().Equals(mustPosition(t, domain.PositionAgree)) || stored.Version() != 1 {
		t.Fatal("refused confirmation mutated the stored projection")
	}
	if repo.insertCount() != 1 {
		t.Fatal("refused confirmation must not write")
	}
}

func TestConfirmInitialPositionChecksEligibilityOnlyForTheFirstWrite(t *testing.T) {
	repo := newFakePositionRepo()
	accounts := &fakeAccountEligibility{err: application.ErrAccountSuspended}
	arenas := &fakeArenaEligibility{}
	useCase := newConfirmUseCase(repo, accounts, arenas)

	if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree)); !errors.Is(err, application.ErrAccountSuspended) {
		t.Fatalf("suspended account error = %v, want ErrAccountSuspended", err)
	}
	if repo.storedCount() != 0 || repo.insertCount() != 0 {
		t.Fatal("suspended account must not write")
	}
	if arenas.callCount() != 0 {
		t.Fatal("arena eligibility must not be checked after an account refusal")
	}

	accounts.err = nil
	arenas.err = application.ErrArenaNotOpen
	if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree)); !errors.Is(err, application.ErrArenaNotOpen) {
		t.Fatalf("closed arena error = %v, want ErrArenaNotOpen", err)
	}
	if repo.storedCount() != 0 || repo.insertCount() != 0 {
		t.Fatal("closed arena must not write")
	}

	// Missing accounts and Arenas propagate their own errors.
	accounts.err = application.ErrAccountNotFound
	arenas.err = nil
	if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree)); !errors.Is(err, application.ErrAccountNotFound) {
		t.Fatalf("missing account error = %v, want ErrAccountNotFound", err)
	}
	accounts.err = nil
	arenas.err = application.ErrArenaNotFound
	if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree)); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("missing arena error = %v, want ErrArenaNotFound", err)
	}
	if repo.storedCount() != 0 || repo.insertCount() != 0 {
		t.Fatal("ineligible pairs must not write")
	}

	// A recorded confirmation replays even after the Arena closes.
	arenas.err = nil
	if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree)); err != nil {
		t.Fatalf("confirmation after eligibility returned: %v", err)
	}
	arenas.err = application.ErrArenaNotOpen
	accounts.err = application.ErrAccountSuspended
	replay, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree))
	if err != nil {
		t.Fatalf("replay after closure error = %v", err)
	}
	if !replay.Replayed {
		t.Fatal("replay after closure must return the recorded result")
	}
}

func TestConfirmInitialPositionResolvesLostInsertRaces(t *testing.T) {
	arenaID := mustArenaID(t)
	accountID := mustAccountID(t)

	t.Run("same value replays", func(t *testing.T) {
		repo := newFakePositionRepo()
		winner, err := domain.ConfirmInitialPosition(arenaID, accountID, mustPosition(t, domain.PositionAgree), testInstant)
		if err != nil {
			t.Fatalf("seed projection: %v", err)
		}
		repo.stored[positionKey(arenaID, accountID)] = winner
		repo.firstGetErr = application.ErrPositionNotFound
		repo.confirmNotInserted = true

		useCase := newConfirmUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{})
		result, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree))
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if !result.Replayed || result.Position.Version() != 1 {
			t.Fatal("lost race with the same value must resolve the replay")
		}
	})

	t.Run("different value conflicts", func(t *testing.T) {
		repo := newFakePositionRepo()
		winner, err := domain.ConfirmInitialPosition(arenaID, accountID, mustPosition(t, domain.PositionDisagree), testInstant)
		if err != nil {
			t.Fatalf("seed projection: %v", err)
		}
		repo.stored[positionKey(arenaID, accountID)] = winner
		repo.firstGetErr = application.ErrPositionNotFound
		repo.confirmNotInserted = true

		useCase := newConfirmUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{})
		if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree)); !errors.Is(err, application.ErrInitialPositionAlreadySet) {
			t.Fatalf("lost race with a different value error = %v, want ErrInitialPositionAlreadySet", err)
		}
	})
}

func TestConfirmInitialPositionValidatesInputsWithoutTouchingPorts(t *testing.T) {
	probes := []struct {
		name    string
		command application.ConfirmInitialPositionCommand
		want    error
	}{
		{name: "empty account", command: application.ConfirmInitialPositionCommand{ArenaID: testArenaRaw, Position: domain.PositionAgree}, want: domain.ErrEmptyAccountID},
		{name: "empty arena", command: application.ConfirmInitialPositionCommand{AccountID: testAccountRaw, Position: domain.PositionAgree}, want: domain.ErrEmptyArenaID},
		{name: "empty position", command: application.ConfirmInitialPositionCommand{AccountID: testAccountRaw, ArenaID: testArenaRaw}, want: domain.ErrEmptyPosition},
		{name: "unknown position", command: application.ConfirmInitialPositionCommand{AccountID: testAccountRaw, ArenaID: testArenaRaw, Position: "maybe"}, want: domain.ErrInvalidPosition},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			repo := newFakePositionRepo()
			accounts := &fakeAccountEligibility{}
			arenas := &fakeArenaEligibility{}
			useCase := newConfirmUseCase(repo, accounts, arenas)

			if _, err := useCase.Execute(context.Background(), probe.command); !errors.Is(err, probe.want) {
				t.Fatalf("error = %v, want %v", err, probe.want)
			}
			if repo.storedCount() != 0 || repo.insertCount() != 0 || accounts.callCount() != 0 || arenas.callCount() != 0 {
				t.Fatal("invalid input must not touch any port")
			}
		})
	}
}

func TestConfirmInitialPositionPropagatesStorageFailures(t *testing.T) {
	storageErr := errors.New("storage down")

	t.Run("read failure", func(t *testing.T) {
		repo := newFakePositionRepo()
		repo.getErr = storageErr
		useCase := newConfirmUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{})
		if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree)); !errors.Is(err, storageErr) {
			t.Fatalf("error = %v, want the storage failure", err)
		}
	})

	t.Run("insert failure", func(t *testing.T) {
		repo := newFakePositionRepo()
		repo.confirmErr = storageErr
		useCase := newConfirmUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{})
		if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree)); !errors.Is(err, storageErr) {
			t.Fatalf("error = %v, want the storage failure", err)
		}
	})

	t.Run("re-read failure after a lost race", func(t *testing.T) {
		repo := newFakePositionRepo()
		repo.firstGetErr = application.ErrPositionNotFound
		repo.confirmNotInserted = true
		useCase := newConfirmUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{})
		if _, err := useCase.Execute(context.Background(), confirmCommand(domain.PositionAgree)); !errors.Is(err, application.ErrPositionNotFound) {
			t.Fatalf("error = %v, want ErrPositionNotFound", err)
		}
	})
}

// TestConfirmInitialPositionConcurrentDifferentValues is the P09-T03
// concurrency proof: many simultaneous confirmations with different values
// produce exactly one initial position; calls matching the winner replay and
// calls with the losing value conflict.
func TestConfirmInitialPositionConcurrentDifferentValues(t *testing.T) {
	repo := newFakePositionRepo()
	accounts := &fakeAccountEligibility{}
	arenas := &fakeArenaEligibility{}
	useCase := newConfirmUseCase(repo, accounts, arenas)

	const perValue = 6
	values := []string{domain.PositionAgree, domain.PositionDisagree}

	type outcome struct {
		value    string
		replayed bool
		err      error
	}

	start := make(chan struct{})
	outcomes := make(chan outcome, perValue*len(values))
	var waitGroup sync.WaitGroup
	for _, value := range values {
		for i := 0; i < perValue; i++ {
			waitGroup.Add(1)
			go func(value string) {
				defer waitGroup.Done()
				<-start
				result, err := useCase.Execute(context.Background(), confirmCommand(value))
				if err != nil {
					outcomes <- outcome{value: value, err: err}
					return
				}
				outcomes <- outcome{value: value, replayed: result.Replayed}
			}(value)
		}
	}
	close(start)
	waitGroup.Wait()
	close(outcomes)

	fresh := 0
	replayed := map[string]int{}
	conflicts := map[string]int{}
	winner := ""
	for result := range outcomes {
		switch {
		case result.err == nil && !result.replayed:
			fresh++
			winner = result.value
		case result.err == nil && result.replayed:
			replayed[result.value]++
		case errors.Is(result.err, application.ErrInitialPositionAlreadySet):
			conflicts[result.value]++
		default:
			t.Fatalf("unexpected outcome: value %q, error %v", result.value, result.err)
		}
	}

	if fresh != 1 {
		t.Fatalf("fresh confirmations = %d, want exactly 1", fresh)
	}
	if winner == "" {
		t.Fatal("no winning value recorded")
	}
	if replayed[winner] != perValue-1 {
		t.Fatalf("replays for the winner = %d, want %d", replayed[winner], perValue-1)
	}
	loser := domain.PositionAgree
	if winner == domain.PositionAgree {
		loser = domain.PositionDisagree
	}
	if replayed[loser] != 0 || conflicts[loser] != perValue {
		t.Fatalf("loser outcomes = %d replays / %d conflicts, want 0/%d", replayed[loser], conflicts[loser], perValue)
	}

	if repo.insertCount() != 1 || repo.storedCount() != 1 {
		t.Fatalf("inserts = %d, stored = %d, want exactly one", repo.insertCount(), repo.storedCount())
	}
	stored := repo.storedPosition(t, mustArenaID(t), mustAccountID(t))
	if stored.InitialPosition().String() != winner || stored.Version() != 1 {
		t.Fatalf("stored winner = %q v%d, want %q v1", stored.InitialPosition().String(), stored.Version(), winner)
	}
}
