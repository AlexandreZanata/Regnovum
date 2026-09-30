package application_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// operationsClock is the stub clock of the operational tests.
type operationsClock struct{ now time.Time }

func (c operationsClock) Now() time.Time { return c.now }

// operationsRepository is the in-memory operational surface. It records the
// writes so a test can assert that a refusal wrote nothing.
type operationsRepository struct {
	health   *application.QueueHealth
	dead     []application.DeadJob
	retried  *application.DeadJob
	writeErr error

	mu        sync.Mutex
	retryCall int
}

func (r *operationsRepository) QueueHealth(context.Context, time.Time) (*application.QueueHealth, error) {
	if r.health == nil {
		return &application.QueueHealth{}, nil
	}
	return r.health, nil
}

func (r *operationsRepository) ListDeadJobs(_ context.Context, limit int, _ time.Time) ([]application.DeadJob, error) {
	items := r.dead
	if len(items) > limit {
		items = items[:limit]
	}
	return append([]application.DeadJob(nil), items...), nil
}

func (r *operationsRepository) RetryDeadJob(_ context.Context, _ string, _ time.Time) (*application.DeadJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retryCall++
	if r.writeErr != nil {
		return nil, r.writeErr
	}
	return r.retried, nil
}

func (r *operationsRepository) retries() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.retryCall
}

// jobLookup answers the pre-flight read.
type jobLookup struct {
	job *domain.Job
	err error
}

func (l jobLookup) JobByID(context.Context, string) (*domain.Job, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.job, nil
}

// recordingAudit records the facts it is given, or fails on demand.
type recordingAudit struct {
	mu    sync.Mutex
	facts []application.AdminFact
	err   error
}

func (a *recordingAudit) Record(_ context.Context, fact application.AdminFact) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.facts = append(a.facts, fact)
	return nil
}

func (a *recordingAudit) recorded() []application.AdminFact {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]application.AdminFact(nil), a.facts...)
}

// unitOfWork runs the callback in place and reports whether it was wrapped.
type unitOfWork struct {
	mu     sync.Mutex
	calls  int
	failed bool
}

func (u *unitOfWork) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	err := fn(ctx)
	if err != nil {
		u.mu.Lock()
		u.failed = true
		u.mu.Unlock()
	}
	return err
}

func (u *unitOfWork) wrapped() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

type operationsHarness struct {
	health  *application.GetQueueHealthUseCase
	list    *application.ListDeadJobsUseCase
	retry   *application.RetryJobUseCase
	repo    *operationsRepository
	audit   *recordingAudit
	uow     *unitOfWork
	deadJob *domain.Job
	clock   operationsClock
}

func newOperationsHarness(t *testing.T) *operationsHarness {
	t.Helper()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	clock := operationsClock{now: now}
	repo := &operationsRepository{
		health:  &application.QueueHealth{Queued: 3, Leased: 1, Succeeded: 40, Dead: 2, DueNow: 2, LagSeconds: 90, OldestDeadSeconds: 7200},
		dead:    []application.DeadJob{{ID: "job-1", Type: domain.TypeEmailDelivery, Version: 1, Attempts: 5, MaxAttempts: 5, LastErrorCode: domain.FailureHandlerError, AgeSeconds: 7200}},
		retried: &application.DeadJob{ID: "job-1", Type: domain.TypeEmailDelivery, Version: 1},
	}
	audit := &recordingAudit{}
	uow := &unitOfWork{}
	health, err := application.NewGetQueueHealthUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewGetQueueHealthUseCase() error = %v", err)
	}
	list, err := application.NewListDeadJobsUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewListDeadJobsUseCase() error = %v", err)
	}
	deadJob := &domain.Job{
		ID:          "job-1",
		Type:        domain.TypeEmailDelivery,
		Version:     1,
		State:       domain.StateDead,
		Attempts:    5,
		MaxAttempts: 5,
	}
	retry, err := application.NewRetryJobUseCase(jobLookup{job: deadJob}, repo, audit, uow, clock)
	if err != nil {
		t.Fatalf("NewRetryJobUseCase() error = %v", err)
	}
	return &operationsHarness{
		health: health, list: list, retry: retry, repo: repo,
		audit: audit, uow: uow,
		deadJob: deadJob, clock: clock,
	}
}

func validRetryCommand() application.RetryJobCommand {
	return application.RetryJobCommand{
		Actor:      "account-operator",
		SessionAge: time.Minute,
		JobID:      "job-1",
		Reason:     "provider outage resolved",
	}
}

func TestRetryRequeuesAndRecordsTheFact(t *testing.T) {
	built := newOperationsHarness(t)
	result, err := built.retry.Execute(context.Background(), validRetryCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.JobID != "job-1" || result.State != domain.StateQueued {
		t.Errorf("result = %+v, want the requeued job", result)
	}
	if built.uow.wrapped() != 1 {
		t.Errorf("transactions = %d, want the write and its audit in one", built.uow.wrapped())
	}
	facts := built.audit.recorded()
	if len(facts) != 1 {
		t.Fatalf("audit facts = %d, want one", len(facts))
	}
	fact := facts[0]
	if fact.Actor != "account-operator" || fact.Action != domain.ActionRetryDeadJob || fact.TargetID != "job-1" {
		t.Errorf("fact = %+v, want the operator, the action and the target", fact)
	}
	// The trail's reason column is a stable code, never the operator's prose:
	// the code is greppable and the sentence is attributable metadata.
	if fact.ReasonCode != domain.ReasonCodeDeadJobRetry {
		t.Errorf("reason code = %q, want the module's stable code", fact.ReasonCode)
	}
	if fact.Reason != "provider outage resolved" {
		t.Errorf("reason = %q, want the operator's stated reason", fact.Reason)
	}
	// The trail records the transition and nothing about the payload.
	for key, value := range fact.Metadata {
		switch key {
		case "previous_status", "new_status", "reference":
		default:
			t.Errorf("metadata carries %q, which is outside the fact's transition", key)
		}
		if strings.Contains(value, "K7QP") {
			t.Errorf("metadata %q carries payload content", key)
		}
	}
	if fact.Metadata["previous_status"] != string(domain.StateDead) || fact.Metadata["new_status"] != string(domain.StateQueued) {
		t.Errorf("metadata = %v, want the recorded transition", fact.Metadata)
	}
}

// TestRetryRequiresRecentAuthentication is the step-up rule: an aged session is
// refused, and the refusal happens before anything is read or written.
func TestRetryRequiresRecentAuthentication(t *testing.T) {
	for _, testCase := range []struct {
		name string
		age  time.Duration
	}{
		{"stale", domain.StepUpWindow + time.Second},
		{"exactly at the boundary", domain.StepUpWindow + time.Nanosecond},
		{"unknown age", 0},
		{"negative age", -time.Minute},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			built := newOperationsHarness(t)
			command := validRetryCommand()
			command.SessionAge = testCase.age
			if _, err := built.retry.Execute(context.Background(), command); !errors.Is(err, domain.ErrStepUpRequired) {
				t.Errorf("Execute() error = %v, want ErrStepUpRequired", err)
			}
			if built.repo.retries() != 0 {
				t.Error("a refused retry wrote to the queue")
			}
			if len(built.audit.recorded()) != 0 {
				t.Error("a refused retry left an audit fact")
			}
			if built.uow.wrapped() != 0 {
				t.Error("a refused retry opened a transaction")
			}
		})
	}
	if _, err := domain.StepUpSatisfied(domain.ActionRetryDeadJob, domain.StepUpWindow-time.Second); err != nil {
		t.Errorf("a fresh session was refused: %v", err)
	}
}

func TestRetryRequiresAnActorAndAReason(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		mutate  func(*application.RetryJobCommand)
		wantErr error
	}{
		{"no actor", func(c *application.RetryJobCommand) { c.Actor = "  " }, domain.ErrEmptyActor},
		{"no reason", func(c *application.RetryJobCommand) { c.Reason = "   " }, domain.ErrEmptyReason},
		{"oversized reason", func(c *application.RetryJobCommand) { c.Reason = strings.Repeat("a", 201) }, domain.ErrEmptyReason},
		{"no job", func(c *application.RetryJobCommand) { c.JobID = "" }, application.ErrJobNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			built := newOperationsHarness(t)
			command := validRetryCommand()
			testCase.mutate(&command)
			if _, err := built.retry.Execute(context.Background(), command); !errors.Is(err, testCase.wantErr) {
				t.Errorf("Execute() error = %v, want %v", err, testCase.wantErr)
			}
			if built.repo.retries() != 0 || len(built.audit.recorded()) != 0 {
				t.Error("a refused retry wrote something")
			}
		})
	}
}

// TestRetryOnlyAcceptsTheAllowlist pins the policy: a workload outside the
// closed allowlist is refused even when the job exists and is dead.
func TestRetryOnlyAcceptsTheAllowlist(t *testing.T) {
	built := newOperationsHarness(t)
	built.deadJob.Type = "make_coffee"
	if _, err := built.retry.Execute(context.Background(), validRetryCommand()); !errors.Is(err, domain.ErrRetryNotAllowed) {
		t.Errorf("Execute() error = %v, want ErrRetryNotAllowed", err)
	}
	if built.repo.retries() != 0 {
		t.Error("a workload outside the allowlist reached the queue")
	}
	if len(built.audit.recorded()) != 0 {
		t.Error("a refused retry left an audit fact")
	}
}

// TestEveryWorkloadHasARetryDecision is a forcing test: adding a workload to the
// queue vocabulary without deciding whether an operator may retry it fails here.
func TestEveryWorkloadHasARetryDecision(t *testing.T) {
	retryable := domain.RetryableJobTypes()
	if len(retryable) == 0 {
		t.Fatal("the retry allowlist is empty")
	}
	seen := make(map[domain.JobType]bool, len(retryable))
	for _, workload := range retryable {
		if !workload.IsValid() {
			t.Errorf("the allowlist names %q, which is not a workload", workload)
		}
		if seen[workload] {
			t.Errorf("the allowlist names %q twice", workload)
		}
		seen[workload] = true
	}
	for _, workload := range domain.AllJobTypes {
		if !domain.RetryAllowed(workload) {
			t.Errorf("workload %q has no retry decision: add it to the allowlist or record why it stays out", workload)
		}
	}
	if domain.RetryAllowed("make_coffee") {
		t.Error("an unknown workload is retryable")
	}
	if domain.RetryAllowed("") {
		t.Error("the empty workload is retryable")
	}
}

func TestRetryRefusesAJobThatIsNotDead(t *testing.T) {
	for _, state := range []domain.JobState{domain.StateQueued, domain.StateLeased, domain.StateSucceeded} {
		t.Run(string(state), func(t *testing.T) {
			built := newOperationsHarness(t)
			built.deadJob.State = state
			if _, err := built.retry.Execute(context.Background(), validRetryCommand()); !errors.Is(err, domain.ErrJobNotDead) {
				t.Errorf("Execute() error = %v, want ErrJobNotDead", err)
			}
			if built.repo.retries() != 0 {
				t.Error("a job that is not dead was retried")
			}
		})
	}
}

// TestARetryThatChangedNothingIsNotRecorded is the honesty rule of the trail:
// when the row moved between the read and the write, the action is refused
// instead of being recorded as if it had happened.
func TestARetryThatChangedNothingIsNotRecorded(t *testing.T) {
	built := newOperationsHarness(t)
	built.repo.retried = nil
	if _, err := built.retry.Execute(context.Background(), validRetryCommand()); !errors.Is(err, application.ErrJobNotFound) {
		t.Errorf("Execute() error = %v, want ErrJobNotFound", err)
	}
	if len(built.audit.recorded()) != 0 {
		t.Error("a retry that moved nothing was recorded")
	}
}

// TestAnUnrecordableRetryIsRefused keeps the retry and its trace atomic: the
// failure of the audit is returned, so the transaction that wrapped both rolls
// the requeue back.
func TestAnUnrecordableRetryIsRefused(t *testing.T) {
	built := newOperationsHarness(t)
	failure := errors.New("audit: write unavailable")
	built.audit.err = failure
	if _, err := built.retry.Execute(context.Background(), validRetryCommand()); !errors.Is(err, failure) {
		t.Errorf("Execute() error = %v, want the audit failure", err)
	}
	if !built.uow.failed {
		t.Error("the transaction was not marked failed, so the requeue would have committed")
	}
}

func TestRetrySurfacesAQueueFailure(t *testing.T) {
	built := newOperationsHarness(t)
	failure := errors.New("queue: write unavailable")
	built.repo.writeErr = failure
	if _, err := built.retry.Execute(context.Background(), validRetryCommand()); !errors.Is(err, failure) {
		t.Errorf("Execute() error = %v, want the repository failure", err)
	}
}

func TestRetrySurfacesAMissingJob(t *testing.T) {
	built := newOperationsHarness(t)
	missing := errors.New("not found")
	retry, err := application.NewRetryJobUseCase(jobLookup{err: missing}, built.repo, built.audit, built.uow, built.clock)
	if err != nil {
		t.Fatalf("NewRetryJobUseCase() error = %v", err)
	}
	if _, err := retry.Execute(context.Background(), validRetryCommand()); !errors.Is(err, missing) {
		t.Errorf("Execute() error = %v, want the lookup failure", err)
	}
}

func TestHealthReportsTheQueueAndTheLag(t *testing.T) {
	built := newOperationsHarness(t)
	report, err := built.health.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if report.Queue.Queued != 3 || report.Queue.Dead != 2 || report.Queue.DueNow != 2 {
		t.Errorf("counts = %+v, want the measured queue", report.Queue)
	}
	if report.Queue.LagSeconds != 90 {
		t.Errorf("lag = %d, want the wait of the oldest due job", report.Queue.LagSeconds)
	}
	if report.Queue.OldestDeadSeconds != 7200 {
		t.Errorf("oldest dead = %d, want the age of the oldest dead job", report.Queue.OldestDeadSeconds)
	}
	if !report.GeneratedAt.Equal(built.clock.now) {
		t.Errorf("generated at = %s, want the injected instant", report.GeneratedAt)
	}
}

func TestDeadListingIsBoundedAndFlagged(t *testing.T) {
	built := newOperationsHarness(t)
	built.repo.dead = []application.DeadJob{
		{ID: "job-1", Type: domain.TypeEmailDelivery},
		{ID: "job-2", Type: domain.TypeRetentionRun},
		{ID: "job-3", Type: "make_coffee"},
	}
	report, err := built.list.Execute(context.Background(), 2)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(report.Items) != 2 {
		t.Fatalf("items = %d, want the requested page", len(report.Items))
	}
	for _, item := range report.Items {
		if !item.Retryable {
			t.Errorf("item %s = %q, want it flagged retryable", item.ID, item.Type)
		}
	}

	report, err = built.list.Execute(context.Background(), 0)
	if err != nil {
		t.Fatalf("Execute(0) error = %v", err)
	}
	if len(report.Items) != 3 {
		t.Errorf("items = %d, want the default page", len(report.Items))
	}
	if report.Items[2].Retryable {
		t.Error("a workload outside the allowlist was flagged retryable")
	}

	report, err = built.list.Execute(context.Background(), application.MaxDeadListLimit+1000)
	if err != nil {
		t.Fatalf("Execute(oversized) error = %v", err)
	}
	if len(report.Items) != 3 {
		t.Errorf("items = %d, want the clamped page", len(report.Items))
	}
}

func TestOperationalUseCasesRequireTheirDependencies(t *testing.T) {
	repo := &operationsRepository{}
	clock := operationsClock{now: time.Now()}
	if _, err := application.NewGetQueueHealthUseCase(nil, clock); !errors.Is(err, application.ErrInvalidQueueConfig) {
		t.Error("health without a repository was accepted")
	}
	if _, err := application.NewGetQueueHealthUseCase(repo, nil); !errors.Is(err, application.ErrInvalidQueueConfig) {
		t.Error("health without a clock was accepted")
	}
	if _, err := application.NewListDeadJobsUseCase(nil, clock); !errors.Is(err, application.ErrInvalidQueueConfig) {
		t.Error("listing without a repository was accepted")
	}
	combinations := []struct {
		name  string
		build func() (*application.RetryJobUseCase, error)
	}{
		{"no lookup", func() (*application.RetryJobUseCase, error) {
			return application.NewRetryJobUseCase(nil, repo, &recordingAudit{}, &unitOfWork{}, clock)
		}},
		{"no repository", func() (*application.RetryJobUseCase, error) {
			return application.NewRetryJobUseCase(jobLookup{}, nil, &recordingAudit{}, &unitOfWork{}, clock)
		}},
		{"no audit", func() (*application.RetryJobUseCase, error) {
			return application.NewRetryJobUseCase(jobLookup{}, repo, nil, &unitOfWork{}, clock)
		}},
		{"no unit of work", func() (*application.RetryJobUseCase, error) {
			return application.NewRetryJobUseCase(jobLookup{}, repo, &recordingAudit{}, nil, clock)
		}},
		{"no clock", func() (*application.RetryJobUseCase, error) {
			return application.NewRetryJobUseCase(jobLookup{}, repo, &recordingAudit{}, &unitOfWork{}, nil)
		}},
	}
	for _, testCase := range combinations {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := testCase.build(); !errors.Is(err, application.ErrInvalidQueueConfig) {
				t.Errorf("error = %v, want ErrInvalidQueueConfig", err)
			}
		})
	}
}
