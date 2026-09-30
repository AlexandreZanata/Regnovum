package application_test

// The local administration of the installation (P19-T09).
//
// These tests pin the rules of the two use cases: who may become an
// administrator, when the promotion is refused, what the trail records, and
// what a repeated command does. The fakes below model the two properties the
// real adapter enforces — a write happens inside a transaction, and the
// decision "the installation has no administrator" is serialized against the
// write that follows it — because those are what make a bootstrap a bootstrap
// instead of a privilege escalator.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// txMarker is the unexported key the fake unit of work sets, and the fake
// administration requires. It stands in for platformpg.TxFromContext.
type txMarker struct{}

// fakeRepositoryForAdministration is the read side of the assignment schema.
type fakeRepositoryForAdministration struct {
	mu          sync.Mutex
	assignments map[domain.AccountID]*application.RoleAssignment
	err         error
}

func (f *fakeRepositoryForAdministration) AssignmentFor(_ context.Context, accountID domain.AccountID) (*application.RoleAssignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.assignments[accountID], nil
}

// fakeAdministration is the write side. It refuses a call outside a transaction
// and serializes the decision and the write exactly like the table lock does.
type fakeAdministration struct {
	mu   sync.Mutex
	rows map[domain.AccountID]*application.RoleAssignment

	locks    int
	grants   []application.RoleGrantRequest
	revokes  []domain.AccountID
	outOfTx  int32
	lockErr  error
	grantErr error
}

func newFakeAdministration() *fakeAdministration {
	return &fakeAdministration{rows: map[domain.AccountID]*application.RoleAssignment{}}
}

func (f *fakeAdministration) LockAssignments(ctx context.Context) error {
	if ctx.Value(txMarker{}) == nil {
		atomic.AddInt32(&f.outOfTx, 1)
		return application.ErrAdministrationOutsideTransaction
	}
	if f.lockErr != nil {
		return f.lockErr
	}
	f.locks++
	return nil
}

func (f *fakeAdministration) AnyActiveAssignment(_ context.Context) (bool, error) {
	for _, assignment := range f.rows {
		if !assignment.Revoked {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeAdministration) Grant(_ context.Context, grant application.RoleGrantRequest) (*application.RoleGrantRecord, error) {
	if f.grantErr != nil {
		return nil, f.grantErr
	}
	existing := f.rows[grant.AccountID]
	record := &application.RoleGrantRecord{
		AccountID: grant.AccountID,
		Role:      grant.Role,
		GrantedBy: grant.GrantedBy,
		GrantedAt: grant.GrantedAt,
	}
	if existing != nil {
		// The real statement keeps the origin of an existing row.
		record.GrantedBy = existing.AccountID
	}
	f.rows[grant.AccountID] = &application.RoleAssignment{AccountID: grant.AccountID, Role: grant.Role}
	f.grants = append(f.grants, grant)
	return record, nil
}

func (f *fakeAdministration) Revoke(_ context.Context, accountID domain.AccountID, _ time.Time) (bool, error) {
	assignment, ok := f.rows[accountID]
	if !ok || assignment.Revoked {
		return false, nil
	}
	assignment.Revoked = true
	f.rows[accountID] = assignment
	f.revokes = append(f.revokes, accountID)
	return true, nil
}

// administrationSnapshot is what the fakes recorded, read as one value so a
// caller cannot pair the wrong count with the wrong slice.
type administrationSnapshot struct {
	locks              int
	grants             []application.RoleGrantRequest
	revokes            []domain.AccountID
	outsideTransaction int
}

// snapshot reads the store under the lock the writes take.
func (f *fakeAdministration) snapshot() administrationSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return administrationSnapshot{
		locks:              f.locks,
		grants:             append([]application.RoleGrantRequest(nil), f.grants...),
		revokes:            append([]domain.AccountID(nil), f.revokes...),
		outsideTransaction: int(atomic.LoadInt32(&f.outOfTx)),
	}
}

// fakeUnitOfWork serializes whole transactions, which is what the database does
// for these writes: the decision and the write are one atomic step.
type fakeUnitOfWork struct {
	mu        sync.Mutex
	commits   int32
	rollbacks int32
}

func (u *fakeUnitOfWork) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := fn(context.WithValue(ctx, txMarker{}, true)); err != nil {
		atomic.AddInt32(&u.rollbacks, 1)
		return err
	}
	atomic.AddInt32(&u.commits, 1)
	return nil
}

// fakeRoleAudit records the facts the trail would hold, validating them against
// the audit domain's own rules through a validating recorder in the bridge
// tests. Here it stores what it was given.
type fakeRoleAudit struct {
	mu          sync.Mutex
	grants      []application.AdministrativeGrantEvent
	revocations []application.AdministrativeRevocationEvent
	err         error
}

func (f *fakeRoleAudit) RecordAdministrativeGrant(_ context.Context, event application.AdministrativeGrantEvent) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants = append(f.grants, event)
	return nil
}

func (f *fakeRoleAudit) RecordAdministrativeRevocation(_ context.Context, event application.AdministrativeRevocationEvent) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revocations = append(f.revocations, event)
	return nil
}

func (f *fakeRoleAudit) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.grants), len(f.revocations)
}

// fakeAdministrationDirectory is the directory of administrative targets.
type fakeAdministrationDirectory struct {
	target *application.AdministrationTarget
	err    error
}

func (f *fakeAdministrationDirectory) LookupByEmail(_ context.Context, _ string) (*application.AdministrationTarget, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.target, nil
}

// seedActiveAssignment puts an active administrative assignment in both halves
// of the fake schema, which is what the real schema holds after a promotion.
func (f *administrationFixture) seedActiveAssignment() {
	assignment := &application.RoleAssignment{AccountID: f.accountID, Role: domain.RoleAdmin}
	f.reads.assignments[f.accountID] = assignment
	f.writes.rows[f.accountID] = assignment
}

// eligibleTarget is the account every rule accepts.
func eligibleTarget(accountID domain.AccountID) *application.AdministrationTarget {
	return &application.AdministrationTarget{
		AccountID:             accountID,
		EmailVerified:         true,
		SecondFactorConfirmed: true,
		CanAuthenticate:       true,
	}
}

const administratorAccount domain.AccountID = "0192f3a0-0000-7000-8000-00000000adm1"

type administrationFixture struct {
	usecase        *application.GrantFirstAdministratorUseCase
	revocation     *application.RevokeAdministratorUseCase
	targets        *fakeAdministrationDirectory
	reads          *fakeRepositoryForAdministration
	writes         *fakeAdministration
	audit          *fakeRoleAudit
	unitOfWork     *fakeUnitOfWork
	instant        time.Time
	accountID      domain.AccountID
	directoryError error
	readError      error
	lockError      error
	grantError     error
	auditError     error
}

func newAdministrationFixture(t *testing.T, options ...func(*administrationFixture)) *administrationFixture {
	t.Helper()

	fixture := &administrationFixture{
		accountID: administratorAccount,
		instant:   time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
		targets:   &fakeAdministrationDirectory{target: eligibleTarget(administratorAccount)},
		reads: &fakeRepositoryForAdministration{
			assignments: map[domain.AccountID]*application.RoleAssignment{},
		},
		writes:     newFakeAdministration(),
		audit:      &fakeRoleAudit{},
		unitOfWork: &fakeUnitOfWork{},
	}
	for _, option := range options {
		option(fixture)
	}

	fixture.targets.err = fixture.directoryError
	fixture.reads.err = fixture.readError
	fixture.writes.lockErr = fixture.lockError
	fixture.writes.grantErr = fixture.grantError
	fixture.audit.err = fixture.auditError

	grant, err := application.NewGrantFirstAdministratorUseCase(
		fixture.targets, fixture.reads, fixture.writes, fixture.audit, &fakeClock{now: fixture.instant}, fixture.unitOfWork,
	)
	if err != nil {
		t.Fatalf("NewGrantFirstAdministratorUseCase: %v", err)
	}
	revocation, err := application.NewRevokeAdministratorUseCase(
		fixture.targets, fixture.reads, fixture.writes, fixture.audit, &fakeClock{now: fixture.instant}, fixture.unitOfWork,
	)
	if err != nil {
		t.Fatalf("NewRevokeAdministratorUseCase: %v", err)
	}
	fixture.usecase = grant
	fixture.revocation = revocation
	return fixture
}

func TestGrantFirstAdministratorPromotesAndRecordsTheFact(t *testing.T) {
	fixture := newAdministrationFixture(t)

	result, err := fixture.usecase.Execute(context.Background(), application.GrantFirstAdministratorCommand{
		Email: "first@arena.example.com",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.AccountID != fixture.accountID || result.Role != domain.RoleAdmin {
		t.Fatalf("result = %+v, want the account promoted to admin", result)
	}
	if result.Revived {
		t.Fatalf("Revived = true, want false for a first grant")
	}

	written := fixture.writes.snapshot()
	if written.locks != 1 {
		t.Fatalf("locks = %d, want exactly one lock before the decision", written.locks)
	}
	if written.outsideTransaction != 0 {
		t.Fatalf("the use case wrote outside a transaction %d times", written.outsideTransaction)
	}
	if len(written.grants) != 1 || written.grants[0].Role != domain.RoleAdmin {
		t.Fatalf("grants = %+v, want one administrative grant", written.grants)
	}
	if written.grants[0].GrantedBy != fixture.accountID {
		t.Fatalf("GrantedBy = %q, want the promoted account (the local command has no operator account)", written.grants[0].GrantedBy)
	}
	if !written.grants[0].GrantedAt.Equal(fixture.instant) {
		t.Fatalf("GrantedAt = %s, want the clock instant %s", written.grants[0].GrantedAt, fixture.instant)
	}

	facts, _ := fixture.audit.counts()
	if facts != 1 {
		t.Fatalf("audit facts = %d, want exactly one", facts)
	}
	event := fixture.audit.grants[0]
	if event.AccountID != string(fixture.accountID) || event.Role != domain.RoleAdmin {
		t.Fatalf("grant event = %+v, want the account and the role", event)
	}
	if event.PreviousStatus != application.StatusNone {
		t.Fatalf("PreviousStatus = %q, want %q", event.PreviousStatus, application.StatusNone)
	}
	if event.Rule != application.RuleFirstAdministrator {
		t.Fatalf("Rule = %q, want %q", event.Rule, application.RuleFirstAdministrator)
	}
}

func TestGrantFirstAdministratorRefusesEveryUnmetRequirement(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*administrationFixture)
		want    error
	}{
		{
			name: "the address identifies no account",
			prepare: func(f *administrationFixture) {
				f.targets.target = nil
			},
			want: application.ErrAdministrationTargetNotFound,
		},
		{
			name: "the address is not valid",
			prepare: func(f *administrationFixture) {
				f.directoryError = application.ErrInvalidAdministrationTarget
			},
			want: application.ErrInvalidAdministrationTarget,
		},
		{
			name: "the email was never verified",
			prepare: func(f *administrationFixture) {
				f.targets.target.EmailVerified = false
			},
			want: application.ErrAdministrationTargetEmailUnverified,
		},
		{
			name: "the account cannot authenticate",
			prepare: func(f *administrationFixture) {
				f.targets.target.CanAuthenticate = false
			},
			want: application.ErrAdministrationTargetNotActive,
		},
		{
			name: "the account holds no confirmed second factor",
			prepare: func(f *administrationFixture) {
				f.targets.target.SecondFactorConfirmed = false
			},
			want: application.ErrAdministrationTargetWithoutSecondFactor,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAdministrationFixture(t, testCase.prepare)

			result, err := fixture.usecase.Execute(context.Background(), application.GrantFirstAdministratorCommand{
				Email: "first@arena.example.com",
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}
			if result != nil {
				t.Fatalf("result = %+v, want nil on refusal", result)
			}
			facts, _ := fixture.audit.counts()
			if facts != 0 {
				t.Fatalf("audit facts = %d, want none: a refused promotion records nothing", facts)
			}
			if written := fixture.writes.snapshot(); len(written.grants) != 0 {
				t.Fatalf("grants = %+v, want none", written.grants)
			}
		})
	}
}

func TestGrantFirstAdministratorRefusesAnEmptyTarget(t *testing.T) {
	fixture := newAdministrationFixture(t)

	result, err := fixture.usecase.Execute(context.Background(), application.GrantFirstAdministratorCommand{Email: "   "})
	if !errors.Is(err, application.ErrEmptyAdministrationTarget) {
		t.Fatalf("err = %v, want %v", err, application.ErrEmptyAdministrationTarget)
	}
	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	if written := fixture.writes.snapshot(); len(written.grants) != 0 {
		t.Fatalf("grants = %+v, want none: an empty target never reaches the database", written.grants)
	}
}

func TestGrantFirstAdministratorRefusesWhenAnAdministratorExists(t *testing.T) {
	fixture := newAdministrationFixture(t)
	holder := domain.AccountID("0192f3a0-0000-7000-8000-000000000002")
	fixture.writes.rows[holder] = &application.RoleAssignment{AccountID: holder, Role: domain.RoleAdmin}

	result, err := fixture.usecase.Execute(context.Background(), application.GrantFirstAdministratorCommand{
		Email: "second@arena.example.com",
	})
	if !errors.Is(err, application.ErrAdministratorAlreadyExists) {
		t.Fatalf("err = %v, want %v", err, application.ErrAdministratorAlreadyExists)
	}
	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	if written := fixture.writes.snapshot(); len(written.grants) != 0 {
		t.Fatalf("grants = %+v, want none: the bootstrap is not a way to grant the role", written.grants)
	}
	facts, _ := fixture.audit.counts()
	if facts != 0 {
		t.Fatalf("audit facts = %d, want none", facts)
	}
}

func TestGrantFirstAdministratorRevivesARevokedAssignment(t *testing.T) {
	fixture := newAdministrationFixture(t)
	fixture.reads.assignments[fixture.accountID] = &application.RoleAssignment{
		AccountID: fixture.accountID,
		Role:      domain.RoleAdmin,
		Revoked:   true,
	}

	result, err := fixture.usecase.Execute(context.Background(), application.GrantFirstAdministratorCommand{
		Email: "first@arena.example.com",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Revived {
		t.Fatalf("Revived = false, want true: a demoted account returns to the same assignment")
	}
	if fixture.audit.grants[0].PreviousStatus != application.StatusRevoked {
		t.Fatalf("PreviousStatus = %q, want %q", fixture.audit.grants[0].PreviousStatus, application.StatusRevoked)
	}
}

func TestGrantFirstAdministratorReplayIsRefused(t *testing.T) {
	fixture := newAdministrationFixture(t)
	ctx := context.Background()

	if _, err := fixture.usecase.Execute(ctx, application.GrantFirstAdministratorCommand{Email: "first@arena.example.com"}); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	result, err := fixture.usecase.Execute(ctx, application.GrantFirstAdministratorCommand{Email: "first@arena.example.com"})
	if !errors.Is(err, application.ErrAdministratorAlreadyExists) {
		t.Fatalf("replayed err = %v, want %v", err, application.ErrAdministratorAlreadyExists)
	}
	if result != nil {
		t.Fatalf("replayed result = %+v, want nil", result)
	}

	written := fixture.writes.snapshot()
	if len(written.grants) != 1 || written.locks != 2 {
		t.Fatalf("grants = %d, locks = %d, want one write and two decisions", len(written.grants), written.locks)
	}
	if commits := atomic.LoadInt32(&fixture.unitOfWork.commits); commits != 1 {
		t.Fatalf("commits = %d, want one: the refused replay commits nothing", commits)
	}
	if facts, _ := fixture.audit.counts(); facts != 1 {
		t.Fatalf("audit facts = %d, want one: a refused replay records nothing", facts)
	}
}

func TestGrantFirstAdministratorIsRefusedWhenTheAccountAlreadyActs(t *testing.T) {
	fixture := newAdministrationFixture(t)
	fixture.reads.assignments[fixture.accountID] = &application.RoleAssignment{
		AccountID: fixture.accountID,
		Role:      domain.RoleModerator,
	}

	_, err := fixture.usecase.Execute(context.Background(), application.GrantFirstAdministratorCommand{Email: "first@arena.example.com"})
	if !errors.Is(err, application.ErrAssignmentAlreadyActive) {
		t.Fatalf("err = %v, want %v", err, application.ErrAssignmentAlreadyActive)
	}
	if written := fixture.writes.snapshot(); len(written.grants) != 0 {
		t.Fatalf("grants = %+v, want none", written.grants)
	}
}

func TestGrantFirstAdministratorSerializesConcurrentCommands(t *testing.T) {
	fixture := newAdministrationFixture(t)
	const commands = 8

	results := make(chan error, commands)
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	for i := 0; i < commands; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, err := fixture.usecase.Execute(context.Background(), application.GrantFirstAdministratorCommand{
				Email: "first@arena.example.com",
			})
			results <- err
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)

	var promoted, refused int
	for err := range results {
		switch {
		case err == nil:
			promoted++
		case errors.Is(err, application.ErrAdministratorAlreadyExists):
			refused++
		default:
			t.Fatalf("unexpected err: %v", err)
		}
	}
	if promoted != 1 {
		t.Fatalf("promotions = %d, want exactly one: the installation has one first administrator", promoted)
	}
	if refused != commands-1 {
		t.Fatalf("refusals = %d, want %d", refused, commands-1)
	}
	if written := fixture.writes.snapshot(); len(written.grants) != 1 {
		t.Fatalf("grants = %d, want one", len(written.grants))
	}
	if facts, _ := fixture.audit.counts(); facts != 1 {
		t.Fatalf("audit facts = %d, want one", facts)
	}
}

func TestGrantFirstAdministratorRecordsNothingItCouldNotWrite(t *testing.T) {
	fixture := newAdministrationFixture(t, func(f *administrationFixture) {
		f.grantError = errors.New("database is gone")
	})

	if _, err := fixture.usecase.Execute(context.Background(), application.GrantFirstAdministratorCommand{Email: "first@arena.example.com"}); err == nil {
		t.Fatal("expected the write failure to surface")
	}
	if facts, _ := fixture.audit.counts(); facts != 0 {
		t.Fatalf("audit facts = %d, want none: the record follows the write", facts)
	}
	if rollbacks := atomic.LoadInt32(&fixture.unitOfWork.rollbacks); rollbacks != 1 {
		t.Fatalf("rollbacks = %d, want one", rollbacks)
	}
}

func TestGrantFirstAdministratorFailsWhenTheTrailCannotRecord(t *testing.T) {
	fixture := newAdministrationFixture(t, func(f *administrationFixture) {
		f.auditError = errors.New("trail is unavailable")
	})

	if _, err := fixture.usecase.Execute(context.Background(), application.GrantFirstAdministratorCommand{Email: "first@arena.example.com"}); err == nil {
		t.Fatal("expected the audit failure to surface: an unrecorded promotion is not a promotion")
	}
	if rollbacks := atomic.LoadInt32(&fixture.unitOfWork.rollbacks); rollbacks != 1 {
		t.Fatalf("rollbacks = %d, want one: the assignment and its record commit together or not at all", rollbacks)
	}
}

func TestRevokeAdministratorDatesTheRevocationAndRecordsIt(t *testing.T) {
	fixture := newAdministrationFixture(t)
	fixture.seedActiveAssignment()

	result, err := fixture.revocation.Execute(context.Background(), application.RevokeAdministratorCommand{Email: "first@arena.example.com"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.AccountID != fixture.accountID || result.Role != domain.RoleAdmin {
		t.Fatalf("result = %+v, want the demoted account and its role", result)
	}
	if !result.RevokedAt.Equal(fixture.instant) {
		t.Fatalf("RevokedAt = %s, want %s", result.RevokedAt, fixture.instant)
	}
	if written := fixture.writes.snapshot(); len(written.revokes) != 1 {
		t.Fatalf("revokes = %d, want one", len(written.revokes))
	}
	_, facts := fixture.audit.counts()
	if facts != 1 {
		t.Fatalf("audit facts = %d, want one", facts)
	}
	if fixture.audit.revocations[0].Rule != application.RuleLocalDemotion {
		t.Fatalf("Rule = %q, want %q", fixture.audit.revocations[0].Rule, application.RuleLocalDemotion)
	}
}

func TestRevokeAdministratorReplayIsRefused(t *testing.T) {
	fixture := newAdministrationFixture(t)
	fixture.seedActiveAssignment()
	ctx := context.Background()

	if _, err := fixture.revocation.Execute(ctx, application.RevokeAdministratorCommand{Email: "first@arena.example.com"}); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	result, err := fixture.revocation.Execute(ctx, application.RevokeAdministratorCommand{Email: "first@arena.example.com"})
	if !errors.Is(err, application.ErrNoActiveAssignment) {
		t.Fatalf("replayed err = %v, want %v", err, application.ErrNoActiveAssignment)
	}
	if result != nil {
		t.Fatalf("replayed result = %+v, want nil", result)
	}
	if written := fixture.writes.snapshot(); len(written.revokes) != 1 {
		t.Fatalf("revokes = %d, want one", len(written.revokes))
	}
	if _, facts := fixture.audit.counts(); facts != 1 {
		t.Fatalf("audit facts = %d, want one: a refused replay records nothing", facts)
	}
}

func TestRevokeAdministratorDemotesAnAccountThatCouldNotBePromoted(t *testing.T) {
	// Losing access must always be possible: an account whose email is no
	// longer verified, that cannot sign in or that lost its factor still has
	// its assignment removed, or an installation would keep an administrator
	// it cannot delist.
	fixture := newAdministrationFixture(t, func(f *administrationFixture) {
		f.targets.target = &application.AdministrationTarget{AccountID: administratorAccount}
	})
	fixture.seedActiveAssignment()

	if _, err := fixture.revocation.Execute(context.Background(), application.RevokeAdministratorCommand{Email: "first@arena.example.com"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, facts := fixture.audit.counts(); facts != 1 {
		t.Fatalf("audit facts = %d, want one", facts)
	}
}

func TestRevokeAdministratorRefusesAnUnknownTarget(t *testing.T) {
	fixture := newAdministrationFixture(t, func(f *administrationFixture) {
		f.targets.target = nil
	})

	_, err := fixture.revocation.Execute(context.Background(), application.RevokeAdministratorCommand{Email: "nobody@arena.example.com"})
	if !errors.Is(err, application.ErrAdministrationTargetNotFound) {
		t.Fatalf("err = %v, want %v", err, application.ErrAdministrationTargetNotFound)
	}
	if written := fixture.writes.snapshot(); len(written.revokes) != 0 {
		t.Fatalf("revokes = %d, want none", len(written.revokes))
	}
}

func TestAdministrationUseCasesFailClosedOnIncompleteComposition(t *testing.T) {
	valid := newAdministrationFixture(t)
	clock := &fakeClock{now: valid.instant}

	dependencies := []struct {
		name   string
		build  func() (*application.GrantFirstAdministratorUseCase, error)
		revoke func() (*application.RevokeAdministratorUseCase, error)
	}{
		{
			name: "no directory",
			build: func() (*application.GrantFirstAdministratorUseCase, error) {
				return application.NewGrantFirstAdministratorUseCase(nil, valid.reads, valid.writes, valid.audit, clock, valid.unitOfWork)
			},
			revoke: func() (*application.RevokeAdministratorUseCase, error) {
				return application.NewRevokeAdministratorUseCase(nil, valid.reads, valid.writes, valid.audit, clock, valid.unitOfWork)
			},
		},
		{
			name: "no assignment reads",
			build: func() (*application.GrantFirstAdministratorUseCase, error) {
				return application.NewGrantFirstAdministratorUseCase(valid.targets, nil, valid.writes, valid.audit, clock, valid.unitOfWork)
			},
			revoke: func() (*application.RevokeAdministratorUseCase, error) {
				return application.NewRevokeAdministratorUseCase(valid.targets, nil, valid.writes, valid.audit, clock, valid.unitOfWork)
			},
		},
		{
			name: "no assignment writes",
			build: func() (*application.GrantFirstAdministratorUseCase, error) {
				return application.NewGrantFirstAdministratorUseCase(valid.targets, valid.reads, nil, valid.audit, clock, valid.unitOfWork)
			},
			revoke: func() (*application.RevokeAdministratorUseCase, error) {
				return application.NewRevokeAdministratorUseCase(valid.targets, valid.reads, nil, valid.audit, clock, valid.unitOfWork)
			},
		},
		{
			name: "no trail",
			build: func() (*application.GrantFirstAdministratorUseCase, error) {
				return application.NewGrantFirstAdministratorUseCase(valid.targets, valid.reads, valid.writes, nil, clock, valid.unitOfWork)
			},
			revoke: func() (*application.RevokeAdministratorUseCase, error) {
				return application.NewRevokeAdministratorUseCase(valid.targets, valid.reads, valid.writes, nil, clock, valid.unitOfWork)
			},
		},
		{
			name: "no clock",
			build: func() (*application.GrantFirstAdministratorUseCase, error) {
				return application.NewGrantFirstAdministratorUseCase(valid.targets, valid.reads, valid.writes, valid.audit, nil, valid.unitOfWork)
			},
			revoke: func() (*application.RevokeAdministratorUseCase, error) {
				return application.NewRevokeAdministratorUseCase(valid.targets, valid.reads, valid.writes, valid.audit, nil, valid.unitOfWork)
			},
		},
		{
			name: "no transaction manager",
			build: func() (*application.GrantFirstAdministratorUseCase, error) {
				return application.NewGrantFirstAdministratorUseCase(valid.targets, valid.reads, valid.writes, valid.audit, clock, nil)
			},
			revoke: func() (*application.RevokeAdministratorUseCase, error) {
				return application.NewRevokeAdministratorUseCase(valid.targets, valid.reads, valid.writes, valid.audit, clock, nil)
			},
		},
	}

	for _, dependency := range dependencies {
		t.Run(dependency.name, func(t *testing.T) {
			if _, err := dependency.build(); !errors.Is(err, application.ErrInvalidRoleAdministrationConfig) {
				t.Fatalf("grant err = %v, want %v", err, application.ErrInvalidRoleAdministrationConfig)
			}
			if _, err := dependency.revoke(); !errors.Is(err, application.ErrInvalidRoleAdministrationConfig) {
				t.Fatalf("revoke err = %v, want %v", err, application.ErrInvalidRoleAdministrationConfig)
			}
		})
	}
}
