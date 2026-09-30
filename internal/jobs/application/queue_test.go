package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

type stubClock struct{ now time.Time }

func (c stubClock) Now() time.Time { return c.now }

type fakeRepo struct {
	enqueueRecords  []jobsapp.EnqueueRecord
	enqueueJob      *jobsdomain.Job
	enqueueReplayed bool
	enqueueErr      error

	leaseOwner string
	leaseNow   time.Time
	leaseUntil time.Time
	leaseJob   *jobsdomain.Job
	leaseErr   error

	completeID  string
	completeOwn string
	completeNow time.Time
	completeErr error
	completeJob *jobsdomain.Job

	failFailure jobsdomain.Failure
	failID      string
	failOwn     string
	failRetryAt time.Time
	failNow     time.Time
	failErr     error
	failJob     *jobsdomain.Job

	releaseNow time.Time
	releaseN   int
	releaseErr error

	jobByID    *jobsdomain.Job
	jobByIDErr error
}

func (f *fakeRepo) Enqueue(_ context.Context, record jobsapp.EnqueueRecord) (*jobsdomain.Job, bool, error) {
	f.enqueueRecords = append(f.enqueueRecords, record)
	return f.enqueueJob, f.enqueueReplayed, f.enqueueErr
}

func (f *fakeRepo) Lease(_ context.Context, owner string, now, until time.Time) (*jobsdomain.Job, error) {
	f.leaseOwner, f.leaseNow, f.leaseUntil = owner, now, until
	return f.leaseJob, f.leaseErr
}

func (f *fakeRepo) Complete(_ context.Context, id, owner string, now time.Time) (*jobsdomain.Job, error) {
	f.completeID, f.completeOwn, f.completeNow = id, owner, now
	return f.completeJob, f.completeErr
}

func (f *fakeRepo) Fail(_ context.Context, failure jobsdomain.Failure, id, owner string, retryAt, now time.Time) (*jobsdomain.Job, error) {
	f.failFailure, f.failID, f.failOwn = failure, id, owner
	f.failRetryAt, f.failNow = retryAt, now
	return f.failJob, f.failErr
}

func (f *fakeRepo) ReleaseExpiredLeases(_ context.Context, now time.Time) (int, error) {
	f.releaseNow = now
	return f.releaseN, f.releaseErr
}

func (f *fakeRepo) JobByID(_ context.Context, id string) (*jobsdomain.Job, error) {
	return f.jobByID, f.jobByIDErr
}

func TestUseCasesRequireDependencies(t *testing.T) {
	clock := stubClock{now: time.Now()}
	repo := &fakeRepo{}

	cases := map[string]error{
		"enqueue without repo":  errOf(func() error { _, err := jobsapp.NewEnqueueUseCase(nil, clock); return err }),
		"enqueue without clock": errOf(func() error { _, err := jobsapp.NewEnqueueUseCase(repo, nil); return err }),
		"lease without repo":    errOf(func() error { _, err := jobsapp.NewLeaseUseCase(nil, clock); return err }),
		"lease without clock":   errOf(func() error { _, err := jobsapp.NewLeaseUseCase(repo, nil); return err }),
		"complete without repo": errOf(func() error { _, err := jobsapp.NewCompleteUseCase(nil, clock); return err }),
		"fail without clock":    errOf(func() error { _, err := jobsapp.NewFailUseCase(repo, nil); return err }),
		"recover without repo":  errOf(func() error { _, err := jobsapp.NewRecoverExpiredLeasesUseCase(nil, clock); return err }),
	}
	for name, err := range cases {
		if !errors.Is(err, jobsapp.ErrInvalidQueueConfig) {
			t.Errorf("%s: got %v, want ErrInvalidQueueConfig", name, err)
		}
	}
}

func errOf(fn func() error) error { return fn() }

func TestEnqueueValidatesAndDefaults(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{}
	useCase, err := jobsapp.NewEnqueueUseCase(repo, stubClock{now: now})
	if err != nil {
		t.Fatalf("NewEnqueueUseCase: %v", err)
	}

	result, err := useCase.Enqueue(context.Background(), jobsapp.EnqueueCommand{
		Type:    jobsdomain.TypeEmailDelivery,
		Version: 1,
		Payload: []byte(`{"account_id":"a1"}`),
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if result == nil {
		t.Fatal("Enqueue returned no result")
	}
	if len(repo.enqueueRecords) != 1 {
		t.Fatalf("records: got %d, want 1", len(repo.enqueueRecords))
	}
	record := repo.enqueueRecords[0]
	if record.MaxAttempts != jobsdomain.DefaultMaxAttempts {
		t.Errorf("max attempts = %d, want default %d", record.MaxAttempts, jobsdomain.DefaultMaxAttempts)
	}
	if !record.AvailableAt.Equal(now) {
		t.Errorf("available at = %v, want now %v", record.AvailableAt, now)
	}
	if !record.CreatedAt.Equal(now) {
		t.Errorf("created at = %v, want now %v", record.CreatedAt, now)
	}

	deferred := now.Add(2 * time.Hour)
	if _, err := useCase.Enqueue(context.Background(), jobsapp.EnqueueCommand{
		Type:        jobsdomain.TypeRetentionRun,
		Version:     1,
		Payload:     []byte(`{}`),
		AvailableAt: &deferred,
		MaxAttempts: 3,
	}); err != nil {
		t.Fatalf("deferred enqueue: %v", err)
	}
	if got := repo.enqueueRecords[1]; !got.AvailableAt.Equal(deferred) || got.MaxAttempts != 3 {
		t.Errorf("deferred record = %+v", got)
	}
}

func TestEnqueueRejectsInvalidCommands(t *testing.T) {
	useCase, err := jobsapp.NewEnqueueUseCase(&fakeRepo{}, stubClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewEnqueueUseCase: %v", err)
	}

	cases := map[string]struct {
		cmd  jobsapp.EnqueueCommand
		want error
	}{
		"unknown type": {
			cmd:  jobsapp.EnqueueCommand{Type: "send_email", Version: 1, Payload: []byte(`{}`)},
			want: jobsdomain.ErrUnknownJobType,
		},
		"zero version": {
			cmd:  jobsapp.EnqueueCommand{Type: jobsdomain.TypeEmailDelivery, Version: 0, Payload: []byte(`{}`)},
			want: jobsdomain.ErrInvalidVersion,
		},
		"invalid payload": {
			cmd:  jobsapp.EnqueueCommand{Type: jobsdomain.TypeEmailDelivery, Version: 1, Payload: []byte(`[]`)},
			want: jobsdomain.ErrPayloadNotObject,
		},
		"negative budget": {
			cmd:  jobsapp.EnqueueCommand{Type: jobsdomain.TypeEmailDelivery, Version: 1, Payload: []byte(`{}`), MaxAttempts: -1},
			want: jobsapp.ErrInvalidMaxAttempts,
		},
		"blank key": {
			cmd:  jobsapp.EnqueueCommand{Type: jobsdomain.TypeEmailDelivery, Version: 1, Payload: []byte(`{}`), IdempotencyKey: "   "},
			want: jobsdomain.ErrInvalidIdempotencyKey,
		},
		"oversized key": {
			cmd: jobsapp.EnqueueCommand{
				Type: jobsdomain.TypeEmailDelivery, Version: 1, Payload: []byte(`{}`),
				IdempotencyKey: strings.Repeat("k", jobsdomain.MaxIdempotencyKeyLength+1),
			},
			want: jobsdomain.ErrInvalidIdempotencyKey,
		},
	}
	for name, tc := range cases {
		if _, err := useCase.Enqueue(context.Background(), tc.cmd); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestEnqueuePropagatesReplay(t *testing.T) {
	repo := &fakeRepo{enqueueReplayed: true, enqueueJob: &jobsdomain.Job{ID: "job-1"}}
	useCase, err := jobsapp.NewEnqueueUseCase(repo, stubClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewEnqueueUseCase: %v", err)
	}
	result, err := useCase.Enqueue(context.Background(), jobsapp.EnqueueCommand{
		Type: jobsdomain.TypeEmailDelivery, Version: 1, Payload: []byte(`{}`), IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !result.Replayed || result.Job.ID != "job-1" {
		t.Errorf("result = %+v", result)
	}
}

func TestLeaseWindowAndOwner(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{}
	useCase, err := jobsapp.NewLeaseUseCase(repo, stubClock{now: now})
	if err != nil {
		t.Fatalf("NewLeaseUseCase: %v", err)
	}

	if _, err := useCase.Lease(context.Background(), "worker-1", 30*time.Second); err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if repo.leaseOwner != "worker-1" {
		t.Errorf("owner = %q", repo.leaseOwner)
	}
	if !repo.leaseNow.Equal(now) || !repo.leaseUntil.Equal(now.Add(30*time.Second)) {
		t.Errorf("window = %v..%v", repo.leaseNow, repo.leaseUntil)
	}

	if _, err := useCase.Lease(context.Background(), "  ", time.Second); !errors.Is(err, jobsapp.ErrInvalidLeaseOwner) {
		t.Errorf("blank owner: got %v", err)
	}
	if _, err := useCase.Lease(context.Background(), strings.Repeat("w", jobsdomain.MaxLeaseOwnerLength+1), time.Second); !errors.Is(err, jobsapp.ErrInvalidLeaseOwner) {
		t.Errorf("oversized owner: got %v", err)
	}
	if _, err := useCase.Lease(context.Background(), "worker-1", 0); !errors.Is(err, jobsapp.ErrInvalidLeaseDuration) {
		t.Errorf("zero duration: got %v", err)
	}
	if _, err := useCase.Lease(context.Background(), "worker-1", -time.Second); !errors.Is(err, jobsapp.ErrInvalidLeaseDuration) {
		t.Errorf("negative duration: got %v", err)
	}
	if _, err := useCase.Lease(context.Background(), "worker-1", jobsapp.MaxLeaseDuration+time.Second); !errors.Is(err, jobsapp.ErrInvalidLeaseDuration) {
		t.Errorf("oversized duration: got %v", err)
	}
	if _, err := useCase.Lease(context.Background(), "worker-1", jobsapp.MaxLeaseDuration); err != nil {
		t.Errorf("maximum duration should be accepted: %v", err)
	}
}

func TestLeaseEmptyQueueIsNotAnError(t *testing.T) {
	useCase, err := jobsapp.NewLeaseUseCase(&fakeRepo{}, stubClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewLeaseUseCase: %v", err)
	}
	job, err := useCase.Lease(context.Background(), "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("Lease on empty queue: %v", err)
	}
	if job != nil {
		t.Errorf("empty queue returned a job: %+v", job)
	}
}

func TestCompleteAndFailHoldTheLease(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{}
	clock := stubClock{now: now}

	complete, err := jobsapp.NewCompleteUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewCompleteUseCase: %v", err)
	}
	if _, err := complete.Complete(context.Background(), "job-1", "worker-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if repo.completeID != "job-1" || repo.completeOwn != "worker-1" || !repo.completeNow.Equal(now) {
		t.Errorf("complete call = %q %q %v", repo.completeID, repo.completeOwn, repo.completeNow)
	}

	repo.completeErr = jobsapp.ErrLeaseNotHeld
	if _, err := complete.Complete(context.Background(), "job-1", "worker-1"); !errors.Is(err, jobsapp.ErrLeaseNotHeld) {
		t.Errorf("stale holder: got %v", err)
	}

	fail, err := jobsapp.NewFailUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewFailUseCase: %v", err)
	}
	if _, err := fail.Fail(context.Background(), "job-1", "worker-1", jobsdomain.FailureHandlerError, "dial tcp 10.0.0.1:5432 refused", nil); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if repo.failFailure.Code != jobsdomain.FailureHandlerError {
		t.Errorf("failure code = %q", repo.failFailure.Code)
	}
	if strings.ContainsAny(repo.failFailure.Detail, "@/\\\"") {
		t.Errorf("failure detail was not sanitized: %q", repo.failFailure.Detail)
	}
	if !repo.failRetryAt.Equal(now) || !repo.failNow.Equal(now) {
		t.Errorf("fail instants = %v %v", repo.failRetryAt, repo.failNow)
	}

	if _, err := fail.Fail(context.Background(), "job-1", "worker-1", "JOB_NOPE", "x", nil); !errors.Is(err, jobsdomain.ErrUnknownFailureCode) {
		t.Errorf("unknown code: got %v", err)
	}
	if _, err := fail.Fail(context.Background(), "job-1", " ", jobsdomain.FailureHandlerError, "x", nil); !errors.Is(err, jobsapp.ErrInvalidLeaseOwner) {
		t.Errorf("blank owner: got %v", err)
	}

	retryAt := now.Add(90 * time.Second)
	if _, err := fail.Fail(context.Background(), "job-1", "worker-1", jobsdomain.FailureHandlerError, "boom", &retryAt); err != nil {
		t.Fatalf("Fail with retry: %v", err)
	}
	if !repo.failRetryAt.Equal(retryAt) {
		t.Errorf("retry at = %v, want %v", repo.failRetryAt, retryAt)
	}
}

func TestRecoverExpiredLeasesReportsCount(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{releaseN: 3}
	useCase, err := jobsapp.NewRecoverExpiredLeasesUseCase(repo, stubClock{now: now})
	if err != nil {
		t.Fatalf("NewRecoverExpiredLeasesUseCase: %v", err)
	}
	reclaimed, err := useCase.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if reclaimed != 3 {
		t.Errorf("reclaimed = %d, want 3", reclaimed)
	}
	if !repo.releaseNow.Equal(now) {
		t.Errorf("release instant = %v", repo.releaseNow)
	}

	repo.releaseErr = errors.New("boom")
	if _, err := useCase.Recover(context.Background()); err == nil {
		t.Error("repository error must propagate")
	}
}
