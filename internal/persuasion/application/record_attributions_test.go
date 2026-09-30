package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

var testInstant = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

const (
	testAccountRaw   = "018f6b2a-0000-7000-8000-000000000003"
	testChangeRaw    = "018f6b2a-0000-7000-8000-000000000001"
	testArenaRaw     = "018f6b2a-0000-7000-8000-000000000002"
	testOtherAccount = "018f6b2a-0000-7000-8000-0000000000aa"
)

type fakeAttributionRepo struct {
	mu sync.Mutex
	// txMu serializes fake transactions, like the FOR UPDATE lock on the
	// change row: the cumulative limit holds under concurrency.
	txMu sync.Mutex

	changes  map[string]domain.Change
	args     map[string]domain.Candidate
	recorded map[string][]domain.ArgumentID

	lockErr       error
	listErr       error
	candidatesErr error
	createErr     error
}

func newFakeAttributionRepo() *fakeAttributionRepo {
	repo := &fakeAttributionRepo{
		changes:  map[string]domain.Change{},
		args:     map[string]domain.Candidate{},
		recorded: map[string][]domain.ArgumentID{},
	}
	changeID, _ := domain.ParseChangeID(testChangeRaw)
	arenaID, _ := domain.ParseArenaID(testArenaRaw)
	attributorID, _ := domain.ParseAttributorID(testAccountRaw)
	repo.changes[testChangeRaw] = domain.Change{
		ID:           changeID,
		ArenaID:      arenaID,
		AttributorID: attributorID,
		ChangedAt:    testInstant,
	}
	return repo
}

func (r *fakeAttributionRepo) seedCandidate(t *testing.T, id string, mutate func(candidate *domain.Candidate)) {
	t.Helper()
	argumentID, err := domain.ParseArgumentID(id)
	if err != nil {
		t.Fatalf("ParseArgumentID: %v", err)
	}
	authorID, err := domain.ParseAuthorID(testOtherAccount)
	if err != nil {
		t.Fatalf("ParseAuthorID: %v", err)
	}
	arenaID, err := domain.ParseArenaID(testArenaRaw)
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	candidate := domain.Candidate{
		ID:        argumentID,
		ArenaID:   arenaID,
		AuthorID:  authorID,
		CreatedAt: testInstant.Add(-time.Hour),
		Status:    domain.ArgumentStatusPublished,
	}
	if mutate != nil {
		mutate(&candidate)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args[id] = candidate
}

func (r *fakeAttributionRepo) LockChangeForAttributor(_ context.Context, changeID domain.ChangeID, attributorID domain.AttributorID) (*domain.Change, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lockErr != nil {
		return nil, r.lockErr
	}
	change, ok := r.changes[changeID.String()]
	if !ok || !change.AttributorID.Equals(attributorID) {
		return nil, application.ErrChangeNotFound
	}
	return &change, nil
}

func (r *fakeAttributionRepo) ListAttributedArgumentIDs(_ context.Context, changeID domain.ChangeID) ([]domain.ArgumentID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]domain.ArgumentID{}, r.recorded[changeID.String()]...), nil
}

func (r *fakeAttributionRepo) ListCandidates(_ context.Context, argumentIDs []domain.ArgumentID) ([]domain.Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.candidatesErr != nil {
		return nil, r.candidatesErr
	}
	candidates := make([]domain.Candidate, 0, len(argumentIDs))
	for _, argumentID := range argumentIDs {
		candidate, ok := r.args[argumentID.String()]
		if !ok {
			return nil, application.ErrArgumentNotFound
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func (r *fakeAttributionRepo) CreateAttributions(_ context.Context, changeID domain.ChangeID, _ domain.AttributorID, candidates []domain.Candidate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	for _, candidate := range candidates {
		r.recorded[changeID.String()] = append(r.recorded[changeID.String()], candidate.ID)
	}
	return nil
}

func (r *fakeAttributionRepo) recordedCount(changeID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.recorded[changeID])
}

type fakeUnitOfWork struct {
	mu    sync.Mutex
	calls int
	err   error
	repo  *fakeAttributionRepo
}

func newFakeUnitOfWork(repo *fakeAttributionRepo) *fakeUnitOfWork {
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
	return fn(ctx)
}

func newRecordUseCase(repo *fakeAttributionRepo, uow *fakeUnitOfWork) *application.RecordAttributionsUseCase {
	return application.NewRecordAttributionsUseCase(repo, domain.DefaultEligibilityPolicy(), uow)
}

func recordCommand(argumentIDs ...string) application.RecordAttributionsCommand {
	return application.RecordAttributionsCommand{
		AccountID:   testAccountRaw,
		ChangeID:    testChangeRaw,
		ArgumentIDs: argumentIDs,
	}
}

func TestRecordAttributionsAcceptsEmptyAndSmallSelections(t *testing.T) {
	repo := newFakeAttributionRepo()
	repo.seedCandidate(t, "argument-a", nil)
	repo.seedCandidate(t, "argument-b", nil)
	repo.seedCandidate(t, "argument-c", nil)
	uow := newFakeUnitOfWork(repo)
	useCase := newRecordUseCase(repo, uow)

	// Skipping is a valid call that creates nothing.
	skipped, err := useCase.Execute(context.Background(), recordCommand())
	if err != nil {
		t.Fatalf("skip Execute() error = %v", err)
	}
	if len(skipped.ArgumentIDs) != 0 || skipped.Replayed {
		t.Fatalf("skip result = %+v, want an empty fresh selection", skipped)
	}
	if repo.recordedCount(testChangeRaw) != 0 {
		t.Fatal("skipping must not create any attribution")
	}

	// Up to three arguments are recorded in one transaction.
	result, err := useCase.Execute(context.Background(), recordCommand("argument-c", "argument-a", "argument-b"))
	if err != nil {
		t.Fatalf("record Execute() error = %v", err)
	}
	if result.Replayed || len(result.ArgumentIDs) != 3 {
		t.Fatalf("result = %+v, want three fresh attributions", result)
	}
	// The recorded set resolves in deterministic ascending order,
	// however the request orders them (mutation gate:
	// record_attributions.go:155).
	wantOrder := []string{"argument-a", "argument-b", "argument-c"}
	for i, id := range result.ArgumentIDs {
		if id.String() != wantOrder[i] {
			t.Fatalf("order = %v, want %v", result.ArgumentIDs, wantOrder)
		}
	}
	if repo.recordedCount(testChangeRaw) != 3 || uow.calls != 2 {
		t.Fatalf("recorded = %d, transactions = %d, want 3/2", repo.recordedCount(testChangeRaw), uow.calls)
	}
}

func TestRecordAttributionsIsIdempotentAndGrowsUpToTheLimit(t *testing.T) {
	repo := newFakeAttributionRepo()
	repo.seedCandidate(t, "argument-a", nil)
	repo.seedCandidate(t, "argument-b", nil)
	repo.seedCandidate(t, "argument-c", nil)
	repo.seedCandidate(t, "argument-d", nil)
	useCase := newRecordUseCase(repo, newFakeUnitOfWork(repo))

	if _, err := useCase.Execute(context.Background(), recordCommand("argument-a")); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}

	// A retry of the same selection resolves the recorded set.
	retry, err := useCase.Execute(context.Background(), recordCommand("argument-a"))
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed || len(retry.ArgumentIDs) != 1 {
		t.Fatalf("retry = %+v, want the recorded set replayed", retry)
	}
	if repo.recordedCount(testChangeRaw) != 1 {
		t.Fatal("retry must not duplicate rows")
	}

	// A skip after a recorded selection resolves the recorded set too.
	skipAfter, err := useCase.Execute(context.Background(), recordCommand())
	if err != nil {
		t.Fatalf("skip after Execute() error = %v", err)
	}
	if !skipAfter.Replayed || len(skipAfter.ArgumentIDs) != 1 {
		t.Fatalf("skip after = %+v, want the recorded set replayed", skipAfter)
	}

	// The selection grows while the cumulative total stays within three.
	grown, err := useCase.Execute(context.Background(), recordCommand("argument-a", "argument-b", "argument-c"))
	if err != nil {
		t.Fatalf("grown Execute() error = %v", err)
	}
	if grown.Replayed || len(grown.ArgumentIDs) != 3 {
		t.Fatalf("grown = %+v, want three recorded arguments", grown)
	}

	// The fourth distinct argument exceeds the cumulative limit.
	if _, err := useCase.Execute(context.Background(), recordCommand("argument-d")); !errors.Is(err, domain.ErrTooManyAttributions) {
		t.Fatalf("fourth argument error = %v, want ErrTooManyAttributions", err)
	}
	if repo.recordedCount(testChangeRaw) != 3 {
		t.Fatal("a refused selection must not write")
	}
}

func TestRecordAttributionsRejectsInvalidSelections(t *testing.T) {
	otherArena, err := domain.ParseArenaID("arena-other")
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	attributor, err := domain.ParseAuthorID(testAccountRaw)
	if err != nil {
		t.Fatalf("ParseAuthorID: %v", err)
	}

	tests := []struct {
		name    string
		seed    func(repo *fakeAttributionRepo, t *testing.T)
		command application.RecordAttributionsCommand
		want    error
	}{
		{
			name:    "four arguments at once",
			seed:    func(repo *fakeAttributionRepo, t *testing.T) { repo.seedCandidate(t, "argument-a", nil) },
			command: recordCommand("argument-a", "argument-b", "argument-c", "argument-d"),
			want:    domain.ErrTooManyAttributions,
		},
		{
			name: "duplicate argument in the request",
			seed: func(repo *fakeAttributionRepo, t *testing.T) {
				repo.seedCandidate(t, "argument-a", nil)
			},
			command: recordCommand("argument-a", "argument-a"),
			want:    domain.ErrDuplicateAttribution,
		},
		{
			name: "self attribution",
			seed: func(repo *fakeAttributionRepo, t *testing.T) {
				repo.seedCandidate(t, "argument-a", func(c *domain.Candidate) { c.AuthorID = attributor })
			},
			command: recordCommand("argument-a"),
			want:    domain.ErrSelfAttribution,
		},
		{
			name: "cross arena",
			seed: func(repo *fakeAttributionRepo, t *testing.T) {
				repo.seedCandidate(t, "argument-a", func(c *domain.Candidate) { c.ArenaID = otherArena })
			},
			command: recordCommand("argument-a"),
			want:    domain.ErrCrossArenaArgument,
		},
		{
			name: "argument after the change",
			seed: func(repo *fakeAttributionRepo, t *testing.T) {
				repo.seedCandidate(t, "argument-a", func(c *domain.Candidate) { c.CreatedAt = testInstant.Add(time.Minute) })
			},
			command: recordCommand("argument-a"),
			want:    domain.ErrArgumentNotBeforeChange,
		},
		{
			name: "withdrawn argument",
			seed: func(repo *fakeAttributionRepo, t *testing.T) {
				repo.seedCandidate(t, "argument-a", func(c *domain.Candidate) { c.Status = domain.ArgumentStatusWithdrawn })
			},
			command: recordCommand("argument-a"),
			want:    domain.ErrArgumentNotEligible,
		},
		{
			name:    "unknown argument",
			seed:    func(repo *fakeAttributionRepo, t *testing.T) {},
			command: recordCommand("argument-ghost"),
			want:    application.ErrArgumentNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newFakeAttributionRepo()
			test.seed(repo, t)
			useCase := newRecordUseCase(repo, newFakeUnitOfWork(repo))

			if _, err := useCase.Execute(context.Background(), test.command); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if repo.recordedCount(testChangeRaw) != 0 {
				t.Fatal("a refused selection must not write")
			}
		})
	}
}

func TestRecordAttributionsIsOwnerScoped(t *testing.T) {
	repo := newFakeAttributionRepo()
	repo.seedCandidate(t, "argument-a", nil)
	useCase := newRecordUseCase(repo, newFakeUnitOfWork(repo))

	command := recordCommand("argument-a")
	command.AccountID = testOtherAccount
	if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, application.ErrChangeNotFound) {
		t.Fatalf("foreign change error = %v, want ErrChangeNotFound", err)
	}
	if repo.recordedCount(testChangeRaw) != 0 {
		t.Fatal("a foreign account must not write")
	}
}

func TestRecordAttributionsValidatesInputsAndPropagatesFailures(t *testing.T) {
	storageErr := errors.New("storage down")

	tests := []struct {
		name    string
		command application.RecordAttributionsCommand
		repo    *fakeAttributionRepo
		uowErr  error
		want    error
	}{
		{name: "empty account", command: application.RecordAttributionsCommand{ChangeID: testChangeRaw}, repo: newFakeAttributionRepo(), want: domain.ErrEmptyAttributorID},
		{name: "empty change", command: application.RecordAttributionsCommand{AccountID: testAccountRaw}, repo: newFakeAttributionRepo(), want: domain.ErrEmptyChangeID},
		{name: "invalid argument id", command: recordCommand("with space"), repo: newFakeAttributionRepo(), want: domain.ErrInvalidIdentifier},
		{name: "transaction failure", command: recordCommand(), repo: newFakeAttributionRepo(), uowErr: storageErr, want: storageErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			uow := newFakeUnitOfWork(test.repo)
			uow.err = test.uowErr
			useCase := newRecordUseCase(test.repo, uow)

			if _, err := useCase.Execute(context.Background(), test.command); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.repo.recordedCount(testChangeRaw) != 0 {
				t.Fatal("invalid input must not write")
			}
		})
	}

	t.Run("candidate load failure", func(t *testing.T) {
		repo := newFakeAttributionRepo()
		repo.seedCandidate(t, "argument-a", nil)
		repo.candidatesErr = storageErr
		useCase := newRecordUseCase(repo, newFakeUnitOfWork(repo))
		if _, err := useCase.Execute(context.Background(), recordCommand("argument-a")); !errors.Is(err, storageErr) {
			t.Fatalf("error = %v, want the storage failure", err)
		}
		if repo.recordedCount(testChangeRaw) != 0 {
			t.Fatal("a failed load must not write")
		}
	})

	t.Run("insert failure", func(t *testing.T) {
		repo := newFakeAttributionRepo()
		repo.seedCandidate(t, "argument-a", nil)
		repo.createErr = storageErr
		useCase := newRecordUseCase(repo, newFakeUnitOfWork(repo))
		if _, err := useCase.Execute(context.Background(), recordCommand("argument-a")); !errors.Is(err, storageErr) {
			t.Fatalf("error = %v, want the storage failure", err)
		}
	})
}

// TestRecordAttributionsConcurrentDisjointSelections proves the cumulative
// limit holds when two requests race: the repository serializes on the
// change lock (emulated here), so 2 + 2 disjoint arguments cannot both fit.
func TestRecordAttributionsConcurrentDisjointSelections(t *testing.T) {
	repo := newFakeAttributionRepo()
	repo.seedCandidate(t, "argument-a", nil)
	repo.seedCandidate(t, "argument-b", nil)
	repo.seedCandidate(t, "argument-c", nil)
	repo.seedCandidate(t, "argument-d", nil)
	uow := newFakeUnitOfWork(repo)
	useCase := newRecordUseCase(repo, uow)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var waitGroup sync.WaitGroup
	selections := [][]string{
		{"argument-a", "argument-b"},
		{"argument-c", "argument-d"},
	}
	for _, selection := range selections {
		waitGroup.Add(1)
		go func(selection []string) {
			defer waitGroup.Done()
			<-start
			_, err := useCase.Execute(context.Background(), recordCommand(selection...))
			errs <- err
		}(selection)
	}
	close(start)
	waitGroup.Wait()
	close(errs)

	successes := 0
	refusals := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrTooManyAttributions):
			refusals++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || refusals != 1 {
		t.Fatalf("successes = %d, refusals = %d, want one of each", successes, refusals)
	}
	if repo.recordedCount(testChangeRaw) != 2 {
		t.Fatalf("recorded = %d, want the winner's two arguments", repo.recordedCount(testChangeRaw))
	}
}
