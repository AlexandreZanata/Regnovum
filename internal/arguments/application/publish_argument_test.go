package application_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

const (
	testArenaRaw   = "018f6b2a-0000-7000-8000-000000000001"
	testAccountRaw = "018f6b2a-0000-7000-8000-000000000002"
	testParentRaw  = "018f6b2a-0000-7000-8000-000000000003"
	testOtherArena = "018f6b2a-0000-7000-8000-000000000004"
)

var testInstant = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func runeCounter(value string) int {
	return utf8.RuneCountInString(value)
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type fakeAccountEligibility struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (f *fakeAccountEligibility) EnsureEligible(context.Context, domain.AccountID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *fakeAccountEligibility) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeArenaEligibility struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (f *fakeArenaEligibility) EnsureAcceptsArguments(context.Context, domain.ArenaID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *fakeArenaEligibility) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeInkDebit struct {
	mu       sync.Mutex
	requests []application.InkDebitRequest
	err      error
	byKey    map[string]bool
}

func newFakeInkDebit() *fakeInkDebit {
	return &fakeInkDebit{byKey: map[string]bool{}}
}

func (f *fakeInkDebit) Debit(_ context.Context, request application.InkDebitRequest) (application.InkDebitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return application.InkDebitResult{}, f.err
	}
	if f.byKey[request.IdempotencyKey] {
		return application.InkDebitResult{Replayed: true}, nil
	}
	f.byKey[request.IdempotencyKey] = true
	f.requests = append(f.requests, request)
	return application.InkDebitResult{}, nil
}

func (f *fakeInkDebit) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

type argumentSnapshot struct {
	arguments map[string]*application.PublishedArgument
	sources   map[string][]domain.Source
	debits    map[string]bool
	debitReqs []application.InkDebitRequest
}

type fakeArgumentRepo struct {
	mu                sync.Mutex
	arguments         map[string]*application.PublishedArgument
	sources           map[string][]domain.Source
	createErr         error
	sourceErr         map[string]error
	createNotInserted bool
	// withdrawNotTransitioned simulates a lost status race; firstGetStatus
	// overrides the status seen by the first owner-scoped read.
	withdrawNotTransitioned bool
	firstGetStatus          string
}

func newFakeArgumentRepo() *fakeArgumentRepo {
	return &fakeArgumentRepo{
		arguments: map[string]*application.PublishedArgument{},
		sources:   map[string][]domain.Source{},
		sourceErr: map[string]error{},
	}
}

func argumentKey(authorID domain.AccountID, key domain.IdempotencyKey) string {
	return authorID.String() + "|" + key.String()
}

func (r *fakeArgumentRepo) CreateArgument(_ context.Context, request application.CreateArgumentRequest) (*application.PublishedArgument, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return nil, false, r.createErr
	}
	key := argumentKey(request.AuthorID, request.IdempotencyKey)
	if stored, ok := r.arguments[key]; ok {
		return stored, false, nil
	}
	if r.createNotInserted {
		return nil, false, nil
	}
	argumentID, err := domain.ParseArgumentID("018f6b2a-0000-7000-8000-0000000000ff")
	if err != nil {
		return nil, false, err
	}
	argument := &application.PublishedArgument{
		ID:        argumentID,
		ArenaID:   request.ArenaID,
		AuthorID:  request.AuthorID,
		ParentID:  request.ParentID,
		Relation:  request.Relation,
		Content:   request.Content,
		Status:    "published",
		CreatedAt: request.CreatedAt,
	}
	r.arguments[key] = argument
	return argument, true, nil
}

func (r *fakeArgumentRepo) CreateArgumentSource(_ context.Context, argumentID domain.ArgumentID, source domain.Source, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.sourceErr[source.URL()]; err != nil {
		return err
	}
	r.sources[argumentID.String()] = append(r.sources[argumentID.String()], source)
	return nil
}

func (r *fakeArgumentRepo) GetByAuthorAndIdempotencyKey(_ context.Context, authorID domain.AccountID, key domain.IdempotencyKey) (*application.PublishedArgument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.arguments[argumentKey(authorID, key)]
	if !ok {
		return nil, application.ErrArgumentNotFound
	}
	return stored, nil
}

func (r *fakeArgumentRepo) GetParent(_ context.Context, argumentID domain.ArgumentID) (*application.PublishedArgument, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, stored := range r.arguments {
		if !stored.ID.Equals(argumentID) {
			continue
		}
		depth := 0
		current := stored
		for !current.ParentID.IsZero() && depth < 100 {
			parent, ok := r.findByIDLocked(current.ParentID)
			if !ok {
				break
			}
			depth++
			current = parent
		}
		return stored, depth, nil
	}
	return nil, 0, application.ErrArgumentNotFound
}

func (r *fakeArgumentRepo) GetForAuthor(_ context.Context, argumentID domain.ArgumentID, authorID domain.AccountID) (*application.PublishedArgument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.findByIDLocked(argumentID)
	if !ok || !stored.AuthorID.Equals(authorID) {
		return nil, application.ErrArgumentNotFound
	}
	if r.firstGetStatus != "" {
		overridden := *stored
		overridden.Status = r.firstGetStatus
		r.firstGetStatus = ""
		return &overridden, nil
	}
	return stored, nil
}

func (r *fakeArgumentRepo) WithdrawArgument(_ context.Context, argumentID domain.ArgumentID, authorID domain.AccountID, at time.Time) (*application.PublishedArgument, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.findByIDLocked(argumentID)
	if !ok || !stored.AuthorID.Equals(authorID) || stored.Status != "published" || r.withdrawNotTransitioned {
		return nil, false, nil
	}
	updated := *stored
	updated.Status = "withdrawn"
	instant := at
	updated.WithdrawnAt = &instant
	r.replaceLocked(stored, &updated)
	return &updated, true, nil
}

// replaceLocked swaps one stored pointer for its updated copy.
func (r *fakeArgumentRepo) replaceLocked(previous, updated *application.PublishedArgument) {
	for key, stored := range r.arguments {
		if stored == previous {
			r.arguments[key] = updated
			return
		}
	}
}

func (r *fakeArgumentRepo) findByIDLocked(argumentID domain.ArgumentID) (*application.PublishedArgument, bool) {
	for _, stored := range r.arguments {
		if stored.ID.Equals(argumentID) {
			return stored, true
		}
	}
	return nil, false
}

func (r *fakeArgumentRepo) seed(t *testing.T, argument *application.PublishedArgument, key string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.arguments[argumentKey(argument.AuthorID, mustKey(t, key))] = argument
}

func (r *fakeArgumentRepo) argumentCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.arguments)
}

func (r *fakeArgumentRepo) sourceCount(argumentID domain.ArgumentID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sources[argumentID.String()])
}

type fakeUnitOfWork struct {
	mu     sync.Mutex
	calls  int
	err    error
	repo   *fakeArgumentRepo
	wallet *fakeInkDebit
}

func newFakeUnitOfWork(repo *fakeArgumentRepo, wallet *fakeInkDebit) *fakeUnitOfWork {
	return &fakeUnitOfWork{repo: repo, wallet: wallet}
}

func (u *fakeUnitOfWork) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	if u.err != nil {
		return u.err
	}

	snapshot := u.snapshot()
	if err := fn(ctx); err != nil {
		u.restore(snapshot)
		return err
	}
	return nil
}

func (u *fakeUnitOfWork) snapshot() argumentSnapshot {
	snapshot := argumentSnapshot{
		arguments: map[string]*application.PublishedArgument{},
		sources:   map[string][]domain.Source{},
		debits:    map[string]bool{},
	}
	u.repo.mu.Lock()
	for key, argument := range u.repo.arguments {
		snapshot.arguments[key] = argument
	}
	for key, sources := range u.repo.sources {
		snapshot.sources[key] = append([]domain.Source{}, sources...)
	}
	u.repo.mu.Unlock()

	u.wallet.mu.Lock()
	for key, used := range u.wallet.byKey {
		snapshot.debits[key] = used
	}
	snapshot.debitReqs = append([]application.InkDebitRequest{}, u.wallet.requests...)
	u.wallet.mu.Unlock()
	return snapshot
}

func (u *fakeUnitOfWork) restore(snapshot argumentSnapshot) {
	u.repo.mu.Lock()
	u.repo.arguments = snapshot.arguments
	u.repo.sources = snapshot.sources
	u.repo.mu.Unlock()

	u.wallet.mu.Lock()
	u.wallet.byKey = snapshot.debits
	u.wallet.requests = snapshot.debitReqs
	u.wallet.mu.Unlock()
}

func (u *fakeUnitOfWork) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func mustKey(t *testing.T, raw string) domain.IdempotencyKey {
	t.Helper()
	key, err := domain.ParseIdempotencyKey(raw)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey(%q): %v", raw, err)
	}
	return key
}

func mustRelation(t *testing.T, raw string) domain.Relation {
	t.Helper()
	relation, err := domain.ParseRelation(raw)
	if err != nil {
		t.Fatalf("ParseRelation(%q): %v", raw, err)
	}
	return relation
}

func mustAccount(t *testing.T, raw string) domain.AccountID {
	t.Helper()
	accountID, err := domain.ParseAccountID(raw)
	if err != nil {
		t.Fatalf("ParseAccountID(%q): %v", raw, err)
	}
	return accountID
}

func mustArena(t *testing.T, raw string) domain.ArenaID {
	t.Helper()
	arenaID, err := domain.ParseArenaID(raw)
	if err != nil {
		t.Fatalf("ParseArenaID(%q): %v", raw, err)
	}
	return arenaID
}

func mustArgumentID(t *testing.T, raw string) domain.ArgumentID {
	t.Helper()
	argumentID, err := domain.ParseArgumentID(raw)
	if err != nil {
		t.Fatalf("ParseArgumentID(%q): %v", raw, err)
	}
	return argumentID
}

func newPublishUseCase(repo *fakeArgumentRepo, accounts *fakeAccountEligibility, arenas *fakeArenaEligibility, wallet *fakeInkDebit, uow *fakeUnitOfWork) *application.PublishArgumentUseCase {
	return newPublishUseCaseWithPolicy(repo, accounts, arenas, wallet, uow, domain.DefaultReplyPolicy())
}

func newPublishUseCaseWithPolicy(repo *fakeArgumentRepo, accounts *fakeAccountEligibility, arenas *fakeArenaEligibility, wallet *fakeInkDebit, uow *fakeUnitOfWork, policy domain.ReplyPolicy) *application.PublishArgumentUseCase {
	return application.NewPublishArgumentUseCase(repo, accounts, arenas, wallet, uow, runeCounter, policy, fixedClock{now: testInstant})
}

func publishCommand() application.PublishArgumentCommand {
	return application.PublishArgumentCommand{
		AccountID:      testAccountRaw,
		ArenaID:        testArenaRaw,
		Relation:       domain.RelationSupport,
		Content:        "A AGI existirá até 2040",
		IdempotencyKey: "attempt-1",
	}
}

func TestPublishArgumentRecordsAtomically(t *testing.T) {
	repo := newFakeArgumentRepo()
	accounts := &fakeAccountEligibility{}
	arenas := &fakeArenaEligibility{}
	wallet := newFakeInkDebit()
	uow := newFakeUnitOfWork(repo, wallet)
	useCase := newPublishUseCase(repo, accounts, arenas, wallet, uow)

	command := publishCommand()
	command.Sources = []application.SourceCommand{
		{URL: "https://example.com/estudo", Description: "Estudo revisado"},
		{URL: "http://example.com/dados"},
	}

	result, err := useCase.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed {
		t.Fatal("first publication must not be a replay")
	}
	if result.Argument.Relation.String() != domain.RelationSupport || result.Argument.Content.GraphemeCost() != 23 {
		t.Fatalf("argument = %+v, want support with cost 23", result.Argument)
	}
	if !result.Argument.ParentID.IsZero() || result.Argument.Status != "published" {
		t.Fatalf("argument = %+v, want a top-level published argument", result.Argument)
	}
	if !result.Argument.CreatedAt.Equal(testInstant) {
		t.Fatal("publication must use the injected clock")
	}
	if repo.argumentCount() != 1 || repo.sourceCount(result.Argument.ID) != 2 {
		t.Fatalf("arguments = %d, sources = %d, want one argument with two sources", repo.argumentCount(), repo.sourceCount(result.Argument.ID))
	}
	if wallet.requestCount() != 1 {
		t.Fatalf("debits = %d, want exactly one", wallet.requestCount())
	}

	wallet.mu.Lock()
	debit := wallet.requests[0]
	wallet.mu.Unlock()
	if debit.Amount != 23 || debit.AccountID != testAccountRaw {
		t.Fatalf("debit = %+v, want the 23-cluster cost from the author", debit)
	}
	if debit.IdempotencyKey != "attempt-1" || debit.Reference != "argument:attempt-1" {
		t.Fatalf("debit = %+v, want the attempt key and its wallet reference", debit)
	}
	if accounts.callCount() != 1 || arenas.callCount() != 1 || uow.callCount() != 1 {
		t.Fatal("publication must check eligibility once and open exactly one transaction")
	}
}

func TestPublishArgumentRepliesCarryTheirParent(t *testing.T) {
	repo := newFakeArgumentRepo()
	parentID := mustArgumentID(t, testParentRaw)
	repo.seed(t, &application.PublishedArgument{
		ID:       parentID,
		ArenaID:  mustArena(t, testArenaRaw),
		AuthorID: mustAccount(t, "018f6b2a-0000-7000-8000-0000000000aa"),
		Status:   "published",
	}, "parent-key")

	wallet := newFakeInkDebit()
	useCase := newPublishUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{}, wallet, newFakeUnitOfWork(repo, wallet))

	command := publishCommand()
	command.ParentID = testParentRaw
	command.Content = "Resposta direta ao argumento"
	result, err := useCase.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Argument.ParentID.Equals(parentID) {
		t.Fatalf("parent = %q, want %q", result.Argument.ParentID.String(), parentID.String())
	}
}

func TestPublishArgumentValidatesParent(t *testing.T) {
	parentID := mustArgumentID(t, testParentRaw)
	withdrawn := &application.PublishedArgument{
		ID:       parentID,
		ArenaID:  mustArena(t, testArenaRaw),
		AuthorID: mustAccount(t, "018f6b2a-0000-7000-8000-0000000000aa"),
		Status:   "withdrawn",
	}
	crossArena := &application.PublishedArgument{
		ID:       parentID,
		ArenaID:  mustArena(t, testOtherArena),
		AuthorID: mustAccount(t, "018f6b2a-0000-7000-8000-0000000000aa"),
		Status:   "published",
	}
	removed := &application.PublishedArgument{
		ID:       parentID,
		ArenaID:  mustArena(t, testArenaRaw),
		AuthorID: mustAccount(t, "018f6b2a-0000-7000-8000-0000000000aa"),
		Status:   "removed",
	}

	tests := []struct {
		name   string
		parent *application.PublishedArgument
		want   error
	}{
		{name: "missing parent", parent: nil, want: application.ErrParentNotFound},
		{name: "cross arena parent", parent: crossArena, want: application.ErrParentNotAvailable},
		{name: "withdrawn parent", parent: withdrawn, want: application.ErrParentNotAvailable},
		{name: "removed parent", parent: removed, want: application.ErrParentNotAvailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newFakeArgumentRepo()
			if test.parent != nil {
				repo.seed(t, test.parent, "parent-key")
			}
			wallet := newFakeInkDebit()
			uow := newFakeUnitOfWork(repo, wallet)
			useCase := newPublishUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{}, wallet, uow)

			command := publishCommand()
			command.ParentID = testParentRaw
			if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if wallet.requestCount() != 0 || uow.callCount() != 0 || repo.argumentCount() != (boolToInt(test.parent != nil)) {
				t.Fatal("invalid parent must not debit, open a transaction or write")
			}
		})
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestPublishArgumentReplaysRecordedAttempts(t *testing.T) {
	repo := newFakeArgumentRepo()
	content, err := domain.ParseContent("A AGI existirá até 2040", runeCounter)
	if err != nil {
		t.Fatalf("ParseContent: %v", err)
	}
	repo.seed(t, &application.PublishedArgument{
		ID:        mustArgumentID(t, "018f6b2a-0000-7000-8000-0000000000ff"),
		ArenaID:   mustArena(t, testArenaRaw),
		AuthorID:  mustAccount(t, testAccountRaw),
		Relation:  mustRelation(t, domain.RelationSupport),
		Content:   content,
		Status:    "published",
		CreatedAt: testInstant,
	}, "attempt-1")

	wallet := newFakeInkDebit()
	accounts := &fakeAccountEligibility{}
	arenas := &fakeArenaEligibility{}
	uow := newFakeUnitOfWork(repo, wallet)
	useCase := newPublishUseCase(repo, accounts, arenas, wallet, uow)

	result, err := useCase.Execute(context.Background(), publishCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Replayed || result.Argument.Content.GraphemeCost() != 23 {
		t.Fatal("retry must resolve the recorded argument")
	}
	if wallet.requestCount() != 0 || uow.callCount() != 0 || accounts.callCount() != 0 || arenas.callCount() != 0 {
		t.Fatal("replay must not debit, open a transaction or re-check eligibility")
	}

	// Concurrent attempt: the insert reports no row, and the use case
	// resolves the stored argument outside the transaction.
	repo2 := newFakeArgumentRepo()
	repo2.createNotInserted = true
	stored := result.Argument
	repo2.arguments[argumentKey(mustAccount(t, testAccountRaw), mustKey(t, "attempt-1"))] = &stored
	wallet2 := newFakeInkDebit()
	uow2 := newFakeUnitOfWork(repo2, wallet2)
	useCase2 := newPublishUseCase(repo2, &fakeAccountEligibility{}, &fakeArenaEligibility{}, wallet2, uow2)

	raced, err := useCase2.Execute(context.Background(), publishCommand())
	if err != nil {
		t.Fatalf("raced Execute() error = %v", err)
	}
	if !raced.Replayed || raced.Argument.ID.String() != result.Argument.ID.String() {
		t.Fatal("lost insert race must resolve the winner as a replay")
	}
	if repo2.sourceCount(raced.Argument.ID) != 0 {
		t.Fatal("the losing attempt must not attach sources")
	}
}

func TestPublishArgumentRejectsInsufficientInkWithoutPartialState(t *testing.T) {
	repo := newFakeArgumentRepo()
	wallet := newFakeInkDebit()
	wallet.err = application.ErrInsufficientInk
	uow := newFakeUnitOfWork(repo, wallet)
	useCase := newPublishUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{}, wallet, uow)

	command := publishCommand()
	command.Sources = []application.SourceCommand{{URL: "https://example.com/estudo"}}
	if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, application.ErrInsufficientInk) {
		t.Fatalf("error = %v, want ErrInsufficientInk", err)
	}
	if repo.argumentCount() != 0 || wallet.requestCount() != 0 {
		t.Fatal("a refused charge must leave no argument and no persisted debit")
	}
}

func TestPublishArgumentRollsBackAfterDebitOnLaterFailure(t *testing.T) {
	repo := newFakeArgumentRepo()
	repo.sourceErr["https://example.com/falha"] = errors.New("storage down")
	wallet := newFakeInkDebit()
	uow := newFakeUnitOfWork(repo, wallet)
	useCase := newPublishUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{}, wallet, uow)

	command := publishCommand()
	command.Sources = []application.SourceCommand{
		{URL: "https://example.com/ok"},
		{URL: "https://example.com/falha"},
	}
	if _, err := useCase.Execute(context.Background(), command); err == nil {
		t.Fatal("a failure after the debit must fail the publication")
	}
	if repo.argumentCount() != 0 {
		t.Fatal("rollback must discard the argument")
	}
	if wallet.requestCount() != 0 {
		t.Fatal("rollback must discard the debit")
	}
}

func TestPublishArgumentValidationsDoNotTouchPorts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(command *application.PublishArgumentCommand)
		want   error
	}{
		{name: "empty account", mutate: func(c *application.PublishArgumentCommand) { c.AccountID = "" }, want: domain.ErrEmptyAccountID},
		{name: "empty arena", mutate: func(c *application.PublishArgumentCommand) { c.ArenaID = "" }, want: domain.ErrEmptyArenaID},
		{name: "empty relation", mutate: func(c *application.PublishArgumentCommand) { c.Relation = "" }, want: domain.ErrEmptyRelation},
		{name: "unknown relation", mutate: func(c *application.PublishArgumentCommand) { c.Relation = "maybe" }, want: domain.ErrInvalidRelation},
		{name: "empty content", mutate: func(c *application.PublishArgumentCommand) { c.Content = "   " }, want: domain.ErrEmptyContent},
		{name: "empty key", mutate: func(c *application.PublishArgumentCommand) { c.IdempotencyKey = "" }, want: domain.ErrEmptyIdempotencyKey},
		{name: "invalid key", mutate: func(c *application.PublishArgumentCommand) { c.IdempotencyKey = "with space" }, want: domain.ErrInvalidIdempotencyKey},
		{name: "invalid source", mutate: func(c *application.PublishArgumentCommand) {
			c.Sources = []application.SourceCommand{{URL: "example.com/x"}}
		}, want: domain.ErrInvalidSourceURL},
		{name: "invalid parent id", mutate: func(c *application.PublishArgumentCommand) { c.ParentID = "with space" }, want: domain.ErrInvalidArgumentID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newFakeArgumentRepo()
			wallet := newFakeInkDebit()
			accounts := &fakeAccountEligibility{}
			arenas := &fakeArenaEligibility{}
			uow := newFakeUnitOfWork(repo, wallet)
			useCase := newPublishUseCase(repo, accounts, arenas, wallet, uow)

			command := publishCommand()
			test.mutate(&command)
			if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if repo.argumentCount() != 0 || wallet.requestCount() != 0 || uow.callCount() != 0 || accounts.callCount() != 0 || arenas.callCount() != 0 {
				t.Fatal("invalid input must not touch any port")
			}
		})
	}
}

func TestPublishArgumentPropagatesEligibilityAndStorageFailures(t *testing.T) {
	storageErr := errors.New("storage down")

	t.Run("account suspended", func(t *testing.T) {
		repo := newFakeArgumentRepo()
		wallet := newFakeInkDebit()
		accounts := &fakeAccountEligibility{err: application.ErrAccountSuspended}
		arenas := &fakeArenaEligibility{}
		uow := newFakeUnitOfWork(repo, wallet)
		useCase := newPublishUseCase(repo, accounts, arenas, wallet, uow)

		if _, err := useCase.Execute(context.Background(), publishCommand()); !errors.Is(err, application.ErrAccountSuspended) {
			t.Fatalf("error = %v, want ErrAccountSuspended", err)
		}
		if wallet.requestCount() != 0 || uow.callCount() != 0 || arenas.callCount() != 0 {
			t.Fatal("a suspended account must not reach the arena, the wallet or the transaction")
		}
	})

	t.Run("arena closed", func(t *testing.T) {
		repo := newFakeArgumentRepo()
		wallet := newFakeInkDebit()
		arenas := &fakeArenaEligibility{err: application.ErrArenaNotOpen}
		uow := newFakeUnitOfWork(repo, wallet)
		useCase := newPublishUseCase(repo, &fakeAccountEligibility{}, arenas, wallet, uow)

		if _, err := useCase.Execute(context.Background(), publishCommand()); !errors.Is(err, application.ErrArenaNotOpen) {
			t.Fatalf("error = %v, want ErrArenaNotOpen", err)
		}
		if wallet.requestCount() != 0 || uow.callCount() != 0 {
			t.Fatal("a closed arena must not reach the wallet or the transaction")
		}
	})

	t.Run("storage failure", func(t *testing.T) {
		repo := newFakeArgumentRepo()
		repo.createErr = storageErr
		wallet := newFakeInkDebit()
		uow := newFakeUnitOfWork(repo, wallet)
		useCase := newPublishUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{}, wallet, uow)

		if _, err := useCase.Execute(context.Background(), publishCommand()); !errors.Is(err, storageErr) {
			t.Fatalf("error = %v, want the storage failure", err)
		}
		if wallet.requestCount() != 0 {
			t.Fatal("a failed insert must roll the debit back")
		}
	})
}

// TestPublishArgumentAcceptsRepliesToOtherAuthors is the P10-T05
// authorization proof: replying does not require owning the parent, only an
// eligible account and an available parent.
func TestPublishArgumentAcceptsRepliesToOtherAuthors(t *testing.T) {
	repo := newFakeArgumentRepo()
	parentID := mustArgumentID(t, testParentRaw)
	repo.seed(t, &application.PublishedArgument{
		ID:       parentID,
		ArenaID:  mustArena(t, testArenaRaw),
		AuthorID: mustAccount(t, "018f6b2a-0000-7000-8000-0000000000aa"),
		Status:   "published",
	}, "parent-key")

	wallet := newFakeInkDebit()
	useCase := newPublishUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{}, wallet, newFakeUnitOfWork(repo, wallet))

	command := publishCommand()
	command.ParentID = testParentRaw
	command.Content = "Resposta de outra pessoa ao argumento"
	result, err := useCase.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Argument.ParentID.Equals(parentID) || result.Argument.AuthorID.String() != testAccountRaw {
		t.Fatalf("reply = %+v, want the authenticated author replying to the other author", result.Argument)
	}
}

// TestPublishArgumentRejectsDeepReplies is the P10-T05 depth proof: the
// domain policy accepts one recursion level, so a reply to a reply is
// refused without debiting.
func TestPublishArgumentRejectsDeepReplies(t *testing.T) {
	topID := mustArgumentID(t, testParentRaw)
	replyID := mustArgumentID(t, "018f6b2a-0000-7000-8000-000000000005")
	author := mustAccount(t, testAccountRaw)

	tests := []struct {
		name        string
		parentDepth int
	}{
		{name: "reply to a reply", parentDepth: 1},
		{name: "reply to a deep chain", parentDepth: 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newFakeArgumentRepo()
			// Build a chain of test.parentDepth arguments: each one replies
			// to the previous, so the last has exactly that depth.
			current := &application.PublishedArgument{
				ID:       topID,
				ArenaID:  mustArena(t, testArenaRaw),
				AuthorID: author,
				Status:   "published",
			}
			repo.seed(t, current, "chain-0")
			for depth := 1; depth <= test.parentDepth; depth++ {
				parent := current
				current = &application.PublishedArgument{
					ID:       replyID,
					ArenaID:  mustArena(t, testArenaRaw),
					AuthorID: author,
					ParentID: parent.ID,
					Status:   "published",
				}
				repo.seed(t, current, fmt.Sprintf("chain-%d", depth))
			}

			wallet := newFakeInkDebit()
			uow := newFakeUnitOfWork(repo, wallet)
			useCase := newPublishUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{}, wallet, uow)

			command := publishCommand()
			command.ParentID = current.ID.String()
			if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, application.ErrReplyDepthExceeded) {
				t.Fatalf("error = %v, want ErrReplyDepthExceeded", err)
			}
			if wallet.requestCount() != 0 || uow.callCount() != 0 {
				t.Fatal("a too-deep reply must not debit or open a transaction")
			}
		})
	}
}

// TestPublishArgumentRejectsRestrictedArenaReplies proves the Arena gate is
// enforced before the parent rules: a restricted Arena accepts no reply.
func TestPublishArgumentRejectsRestrictedArenaReplies(t *testing.T) {
	repo := newFakeArgumentRepo()
	parentID := mustArgumentID(t, testParentRaw)
	repo.seed(t, &application.PublishedArgument{
		ID:       parentID,
		ArenaID:  mustArena(t, testArenaRaw),
		AuthorID: mustAccount(t, testAccountRaw),
		Status:   "published",
	}, "parent-key")

	wallet := newFakeInkDebit()
	uow := newFakeUnitOfWork(repo, wallet)
	arenas := &fakeArenaEligibility{err: application.ErrArenaNotOpen}
	useCase := newPublishUseCase(repo, &fakeAccountEligibility{}, arenas, wallet, uow)

	command := publishCommand()
	command.ParentID = testParentRaw
	if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, application.ErrArenaNotOpen) {
		t.Fatalf("error = %v, want ErrArenaNotOpen", err)
	}
	if wallet.requestCount() != 0 || uow.callCount() != 0 {
		t.Fatal("a restricted Arena must not debit or open a transaction")
	}
}

func TestPublishArgumentRejectsInvalidReplyPolicy(t *testing.T) {
	repo := newFakeArgumentRepo()
	wallet := newFakeInkDebit()
	uow := newFakeUnitOfWork(repo, wallet)
	useCase := newPublishUseCaseWithPolicy(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{}, wallet, uow,
		domain.ReplyPolicy{Version: "", MaxDepth: 1})

	if _, err := useCase.Execute(context.Background(), publishCommand()); !errors.Is(err, domain.ErrInvalidPolicy) {
		t.Fatalf("error = %v, want ErrInvalidPolicy", err)
	}
	if wallet.requestCount() != 0 || uow.callCount() != 0 || repo.argumentCount() != 0 {
		t.Fatal("an invalid policy must not touch any port")
	}
}
