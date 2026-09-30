package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// Clock exposes the current instant. It is a local port (the application layer
// does not import platform packages), so the queue is deterministic under a
// stub clock and never reads `time.Now` directly.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// Lease window policy. A lease must be long enough to run a handler and short
// enough that a crashed worker is recovered quickly; the maximum keeps a
// forgotten lease from hiding work for hours.
const (
	DefaultLeaseDuration = 60 * time.Second
	MaxLeaseDuration     = 30 * time.Minute
)

// EnqueueCommand is a request to add one unit of work to the queue.
type EnqueueCommand struct {
	// Type is the workload name; it must belong to the closed vocabulary.
	Type domain.JobType
	// Version is the schema version of the payload.
	Version int
	// Payload is the bounded JSON object the handler interprets.
	Payload []byte
	// IdempotencyKey is an optional caller-chosen retry key: repeating an
	// enqueue with the same key resolves the original job instead of
	// creating a second one.
	IdempotencyKey string
	// AvailableAt postpones the job; nil means "as soon as possible".
	AvailableAt *time.Time
	// MaxAttempts is the attempt budget; zero means the default.
	MaxAttempts int
}

// EnqueueResult is the outcome of an enqueue. Replayed reports that the key
// already existed and the returned job is the original one.
type EnqueueResult struct {
	Job      *domain.Job
	Replayed bool
}

// EnqueueRecord is what the repository persists: the validated shape of one
// new queue row.
type EnqueueRecord struct {
	Type           domain.JobType
	Version        int
	Payload        []byte
	IdempotencyKey string
	AvailableAt    time.Time
	MaxAttempts    int
	CreatedAt      time.Time
}

// QueueRepository is the persistence port of the queue. Enqueue is idempotent;
// Lease must claim one due row atomically (PostgreSQL `FOR UPDATE SKIP
// LOCKED`), so concurrent workers never take the same job.
type QueueRepository interface {
	// Enqueue stores a job, returning the stored row and whether an existing
	// idempotency key was resolved instead of inserting.
	Enqueue(ctx context.Context, record EnqueueRecord) (*domain.Job, bool, error)

	// Lease claims the next due job, or returns nil when none is due.
	Lease(ctx context.Context, owner string, now, until time.Time) (*domain.Job, error)

	// Complete records success for a job the caller still leases.
	Complete(ctx context.Context, jobID, owner string, now time.Time) (*domain.Job, error)

	// Fail records a redacted failure for a job the caller still leases,
	// requeueing at retryAt or moving it to dead when the budget is spent.
	Fail(ctx context.Context, failure domain.Failure, jobID, owner string, retryAt, now time.Time) (*domain.Job, error)

	// ReleaseExpiredLeases returns every job whose lease ended to the queue
	// and reports how many were reclaimed.
	ReleaseExpiredLeases(ctx context.Context, now time.Time) (int, error)

	// JobByID loads one job; a missing row is ErrJobNotFound.
	JobByID(ctx context.Context, id string) (*domain.Job, error)
}

// EnqueueUseCase validates and stores one unit of work.
type EnqueueUseCase struct {
	repo  QueueRepository
	clock Clock
}

// NewEnqueueUseCase builds the use case.
func NewEnqueueUseCase(repo QueueRepository, clock Clock) (*EnqueueUseCase, error) {
	if repo == nil || clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	return &EnqueueUseCase{repo: repo, clock: clock}, nil
}

// Enqueue validates the command and persists it. An invalid payload or an
// unknown type is refused here: a job that cannot run is never queued.
func (uc *EnqueueUseCase) Enqueue(ctx context.Context, cmd EnqueueCommand) (*EnqueueResult, error) {
	if !cmd.Type.IsValid() {
		return nil, domain.ErrUnknownJobType
	}
	if cmd.Version < 1 {
		return nil, domain.ErrInvalidVersion
	}
	if err := domain.ValidatePayload(cmd.Payload); err != nil {
		return nil, err
	}
	maxAttempts := cmd.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = domain.DefaultMaxAttempts
	}
	if maxAttempts < 1 {
		return nil, ErrInvalidMaxAttempts
	}
	if key := strings.TrimSpace(cmd.IdempotencyKey); cmd.IdempotencyKey != "" && (key == "" || len(cmd.IdempotencyKey) > domain.MaxIdempotencyKeyLength) {
		return nil, domain.ErrInvalidIdempotencyKey
	}

	now := uc.clock.Now().UTC()
	availableAt := now
	if cmd.AvailableAt != nil {
		availableAt = cmd.AvailableAt.UTC()
	}

	job, replayed, err := uc.repo.Enqueue(ctx, EnqueueRecord{
		Type:           cmd.Type,
		Version:        cmd.Version,
		Payload:        cmd.Payload,
		IdempotencyKey: cmd.IdempotencyKey,
		AvailableAt:    availableAt,
		MaxAttempts:    maxAttempts,
		CreatedAt:      now,
	})
	if err != nil {
		return nil, fmt.Errorf("enqueue job: %w", err)
	}
	return &EnqueueResult{Job: job, Replayed: replayed}, nil
}

// LeaseUseCase claims one due unit of work for a worker.
type LeaseUseCase struct {
	repo  QueueRepository
	clock Clock
}

// NewLeaseUseCase builds the use case.
func NewLeaseUseCase(repo QueueRepository, clock Clock) (*LeaseUseCase, error) {
	if repo == nil || clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	return &LeaseUseCase{repo: repo, clock: clock}, nil
}

// Lease claims the next due job and returns nil when the queue has none. A
// non-positive or oversized window is refused rather than silently clamped.
func (uc *LeaseUseCase) Lease(ctx context.Context, owner string, duration time.Duration) (*domain.Job, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	if duration <= 0 || duration > MaxLeaseDuration {
		return nil, ErrInvalidLeaseDuration
	}
	now := uc.clock.Now().UTC()
	job, err := uc.repo.Lease(ctx, owner, now, now.Add(duration))
	if err != nil {
		return nil, fmt.Errorf("lease job: %w", err)
	}
	return job, nil
}

// CompleteUseCase records a successful outcome.
type CompleteUseCase struct {
	repo  QueueRepository
	clock Clock
}

// NewCompleteUseCase builds the use case.
func NewCompleteUseCase(repo QueueRepository, clock Clock) (*CompleteUseCase, error) {
	if repo == nil || clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	return &CompleteUseCase{repo: repo, clock: clock}, nil
}

// Complete marks the job succeeded. The repository only matches a row the
// caller still leases, so a stale holder cannot overwrite a newer outcome.
func (uc *CompleteUseCase) Complete(ctx context.Context, jobID, owner string) (*domain.Job, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	job, err := uc.repo.Complete(ctx, jobID, owner, uc.clock.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("complete job: %w", err)
	}
	return job, nil
}

// FailUseCase records a redacted failure and applies the attempt budget.
type FailUseCase struct {
	repo  QueueRepository
	clock Clock
}

// NewFailUseCase builds the use case.
func NewFailUseCase(repo QueueRepository, clock Clock) (*FailUseCase, error) {
	if repo == nil || clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	return &FailUseCase{repo: repo, clock: clock}, nil
}

// Fail records the failure. The detail is sanitized by the domain before it
// can reach the column; retryAt postpones a retry (nil means immediately).
// The repository promotes the job to dead once the attempt budget is spent, so
// a poison job leaves the queue instead of blocking it.
func (uc *FailUseCase) Fail(ctx context.Context, jobID, owner string, code domain.FailureCode, detail string, retryAt *time.Time) (*domain.Job, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	failure, err := domain.NewFailure(code, detail)
	if err != nil {
		return nil, err
	}
	now := uc.clock.Now().UTC()
	retry := now
	if retryAt != nil {
		retry = retryAt.UTC()
	}
	job, err := uc.repo.Fail(ctx, failure, jobID, owner, retry, now)
	if err != nil {
		return nil, fmt.Errorf("fail job: %w", err)
	}
	return job, nil
}

// RecoverExpiredLeasesUseCase returns abandoned work to the queue.
type RecoverExpiredLeasesUseCase struct {
	repo  QueueRepository
	clock Clock
}

// NewRecoverExpiredLeasesUseCase builds the use case.
func NewRecoverExpiredLeasesUseCase(repo QueueRepository, clock Clock) (*RecoverExpiredLeasesUseCase, error) {
	if repo == nil || clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	return &RecoverExpiredLeasesUseCase{repo: repo, clock: clock}, nil
}

// Recover reclaims every job whose lease ended and reports how many.
func (uc *RecoverExpiredLeasesUseCase) Recover(ctx context.Context) (int, error) {
	reclaimed, err := uc.repo.ReleaseExpiredLeases(ctx, uc.clock.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("recover expired leases: %w", err)
	}
	return reclaimed, nil
}

func validateOwner(owner string) error {
	if strings.TrimSpace(owner) == "" || len(owner) > domain.MaxLeaseOwnerLength {
		return ErrInvalidLeaseOwner
	}
	return nil
}
