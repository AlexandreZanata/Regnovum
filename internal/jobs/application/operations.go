package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// Bounds of the operational surface.
const (
	// DefaultDeadListLimit is how many dead jobs one page returns.
	DefaultDeadListLimit = 50
	// MaxDeadListLimit bounds one page.
	MaxDeadListLimit = 200
)

// QueueHealth is the state of the queue at one instant.
//
// It carries counts and durations, never a payload: an operator needs to know
// that eleven jobs are stuck, not what they carry.
type QueueHealth struct {
	Queued    int64
	Leased    int64
	Succeeded int64
	Dead      int64
	// DueNow counts the queued jobs that are available and unclaimed: a
	// non-zero value with idle workers means the workers are not running.
	DueNow int64
	// LagSeconds is how long the oldest due job has been waiting. It is the
	// number an alert watches: a queue that is behind says nothing about its
	// own lateness, and this does.
	LagSeconds int64
	// OldestDeadSeconds is how long the oldest dead job has been dead, which
	// is how long an operator has been able to act and has not.
	OldestDeadSeconds int64
}

// DeadJob is one dead job as the operational surface exposes it. The payload is
// absent by construction, not by omission: the query that reads it does not
// select the column.
type DeadJob struct {
	ID            string
	Type          domain.JobType
	Version       int
	Attempts      int
	MaxAttempts   int
	LastErrorCode domain.FailureCode
	AgeSeconds    int64
	// Retryable reports the allowlist decision, so a client does not have to
	// duplicate the policy to know which rows it may act on.
	Retryable bool
}

// DeadReport is one page of dead jobs.
type DeadReport struct {
	GeneratedAt time.Time
	Total       int64
	Items       []DeadJob
}

// HealthReport is the operational view of the queue.
type HealthReport struct {
	GeneratedAt time.Time
	Queue       QueueHealth
}

// OperationsRepository is the read and retry surface the operational use cases
// need. Every method returns the operational view of a job; none of them
// returns a payload.
type OperationsRepository interface {
	// QueueHealth measures the queue at the given instant: the counts, the
	// wait of the oldest due job and the age of the oldest dead one.
	QueueHealth(ctx context.Context, now time.Time) (*QueueHealth, error)
	// ListDeadJobs returns up to limit dead jobs, oldest first.
	ListDeadJobs(ctx context.Context, limit int, now time.Time) ([]DeadJob, error)
	// RetryDeadJob returns one dead job to the queue and reports the job it
	// moved. A job that is not dead resolves to nil.
	RetryDeadJob(ctx context.Context, jobID string, now time.Time) (*DeadJob, error)
}

// JobLookup loads one job so the application can apply the retry policy before
// anything is written.
type JobLookup interface {
	// JobByID loads one job; a missing row is ErrJobNotFound.
	JobByID(ctx context.Context, id string) (*domain.Job, error)
}

// AdminAudit records what an operator did.
//
// The port is declared here, in the module that produces the fact, and the
// adapter that satisfies it translates to the audit module's vocabulary. That
// is the same bridge pattern the queue uses for other modules: jobs states what
// it wants recorded, and nothing about the platform learns jobs' internals.
type AdminAudit interface {
	// Record persists one administrative fact.
	Record(ctx context.Context, fact AdminFact) error
}

// AdminFact is one administrative fact, in this module's terms.
//
// ReasonCode and Reason are deliberately separate: the trail records a stable
// code that is greppable years later and never free prose, so the operator's
// stated justification travels as allowlisted metadata while the code stays a
// constant of this module.
type AdminFact struct {
	Actor      string
	Action     string
	TargetID   string
	ReasonCode string
	// Reason is the operator's stated justification, bounded and recorded as
	// allowlisted metadata. It is never the trail's reason code.
	Reason     string
	Metadata   map[string]string
	OccurredAt time.Time
}

// SessionAgeDirectory resolves how old the caller's authentication is.
//
// The age comes from a server-observed fact about the session, never from a
// client claim: a client that states its own freshness is asserting exactly
// what the step-up rule exists to check. Modules that need this question
// declare their own copy of the port, so no module learns another's
// persistence.
type SessionAgeDirectory interface {
	// SessionAgeAt returns how long ago the session authenticated, as of
	// now. An unknown session fails with ErrUnknownSession.
	SessionAgeAt(ctx context.Context, sessionID string, now time.Time) (time.Duration, error)
}

// OperatorDirectory answers whether an account may act on the job queue.
//
// Reading the queue and retrying dead work is an operational capability, not a
// moderation one: the moderator role triages content, and the queue is not
// content. The port keeps that decision in the module that owns the action,
// while the assignment model stays where it lives.
type OperatorDirectory interface {
	// IsOperator reports whether the account holds an active operational
	// assignment. Unknown and revoked assignments answer false identically,
	// so the gate never oracles assignment state.
	IsOperator(ctx context.Context, accountID string) (bool, error)
}

// ErrUnknownSession reports a session identifier that cannot be resolved. It is
// a denial, not an outage: an unknown session is exactly what the step-up rule
// must refuse.
var ErrUnknownSession = errors.New("application: session is unknown")

// UnitOfWork runs a function inside one database transaction, so the operator's
// action and its audit record commit or roll back together. An operator action
// that leaves no trace is not allowed to have happened.
type UnitOfWork interface {
	WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// GetQueueHealthUseCase answers the operational health query.
type GetQueueHealthUseCase struct {
	repository OperationsRepository
	clock      Clock
}

// NewGetQueueHealthUseCase builds the query.
func NewGetQueueHealthUseCase(repository OperationsRepository, clock Clock) (*GetQueueHealthUseCase, error) {
	if repository == nil || clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	return &GetQueueHealthUseCase{repository: repository, clock: clock}, nil
}

// Execute measures the queue at the instant of the injected clock.
func (uc *GetQueueHealthUseCase) Execute(ctx context.Context) (*HealthReport, error) {
	if uc == nil || uc.repository == nil || uc.clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	now := uc.clock.Now().UTC()
	health, err := uc.repository.QueueHealth(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("queue health: %w", err)
	}
	if health == nil {
		return nil, errors.New("queue health: repository returned nothing")
	}
	return &HealthReport{GeneratedAt: now, Queue: *health}, nil
}

// ListDeadJobsUseCase answers the dead-job page.
type ListDeadJobsUseCase struct {
	repository OperationsRepository
	clock      Clock
}

// NewListDeadJobsUseCase builds the query.
func NewListDeadJobsUseCase(repository OperationsRepository, clock Clock) (*ListDeadJobsUseCase, error) {
	if repository == nil || clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	return &ListDeadJobsUseCase{repository: repository, clock: clock}, nil
}

// Execute returns one page. An out-of-range limit is clamped to the surface
// bounds instead of being refused: reading a queue is not a security boundary,
// and the bound is what keeps the answer small.
func (uc *ListDeadJobsUseCase) Execute(ctx context.Context, limit int) (*DeadReport, error) {
	if uc == nil || uc.repository == nil || uc.clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	if limit <= 0 {
		limit = DefaultDeadListLimit
	}
	if limit > MaxDeadListLimit {
		limit = MaxDeadListLimit
	}
	now := uc.clock.Now().UTC()
	items, err := uc.repository.ListDeadJobs(ctx, limit, now)
	if err != nil {
		return nil, fmt.Errorf("list dead jobs: %w", err)
	}
	total := int64(len(items))
	for index := range items {
		items[index].Retryable = domain.RetryAllowed(items[index].Type)
	}
	return &DeadReport{GeneratedAt: now, Total: total, Items: items}, nil
}

// RetryJobCommand is one authorized retry.
//
// The session age travels with the command rather than being checked by the
// adapter: the rule belongs to the module that owns the action, and an inbound
// adapter that forgot to check it would otherwise bypass the policy entirely.
// The adapter's only job is to resolve the age from a server-observed fact.
type RetryJobCommand struct {
	// Actor is the authenticated operator.
	Actor string
	// SessionAge is how old the operator's authentication is.
	SessionAge time.Duration
	// JobID is the dead job to requeue.
	JobID string
	// Reason is the operator's stated justification.
	Reason string
}

// RetryJobResult is the outcome of one retry.
type RetryJobResult struct {
	JobID string
	Type  domain.JobType
	State domain.JobState
}

// RetryJobUseCase returns one dead job to the queue and records who did it.
//
// The order of the steps is the policy: authorize, load, apply the allowlist,
// then write the retry and the audit record in one transaction. Nothing is
// written before the checks pass, and nothing is written without a trace.
type RetryJobUseCase struct {
	lookup     JobLookup
	repository OperationsRepository
	audit      AdminAudit
	uow        UnitOfWork
	clock      Clock
}

// NewRetryJobUseCase builds the use case. Every dependency is required: a
// retry with no audit trail or without a transaction is not this use case.
func NewRetryJobUseCase(
	lookup JobLookup,
	repository OperationsRepository,
	audit AdminAudit,
	uow UnitOfWork,
	clock Clock,
) (*RetryJobUseCase, error) {
	if lookup == nil || repository == nil || audit == nil || uow == nil || clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	return &RetryJobUseCase{lookup: lookup, repository: repository, audit: audit, uow: uow, clock: clock}, nil
}

// Execute authorizes the operator, applies the allowlist and requeues the job.
func (uc *RetryJobUseCase) Execute(ctx context.Context, command RetryJobCommand) (*RetryJobResult, error) {
	if uc == nil || uc.lookup == nil || uc.repository == nil || uc.audit == nil || uc.uow == nil || uc.clock == nil {
		return nil, ErrInvalidQueueConfig
	}
	if err := authorizeRetry(command); err != nil {
		return nil, err
	}
	now := uc.clock.Now().UTC()

	job, err := uc.lookup.JobByID(ctx, command.JobID)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, ErrJobNotFound
	}
	if job.State != domain.StateDead {
		return nil, fmt.Errorf("%w: %s is %s", domain.ErrJobNotDead, job.ID, job.State)
	}
	if !domain.RetryAllowed(job.Type) {
		return nil, fmt.Errorf("%w: %s", domain.ErrRetryNotAllowed, job.Type)
	}

	var result *RetryJobResult
	err = uc.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		retried, err := uc.repository.RetryDeadJob(txCtx, job.ID, now)
		if err != nil {
			return err
		}
		if retried == nil {
			// The row changed between the read and the write, so another
			// operator (or a worker) already moved it. Refusing is right:
			// recording a retry that did not happen would be a false entry
			// in the trail.
			return ErrJobNotFound
		}
		// The audit fact names the transition that actually happened. Its
		// metadata uses only allowlisted keys, and the job id is the target
		// identifier: what the job carries stays out of the trail. The
		// operator's reason travels as metadata, because the trail's reason
		// column is a stable code and never prose.
		if err := uc.audit.Record(txCtx, AdminFact{
			Actor:      command.Actor,
			Action:     domain.ActionRetryDeadJob,
			TargetID:   retried.ID,
			ReasonCode: domain.ReasonCodeDeadJobRetry,
			Reason:     command.Reason,
			Metadata: map[string]string{
				"previous_status": string(domain.StateDead),
				"new_status":      string(domain.StateQueued),
				"reference":       retried.ID,
			},
			OccurredAt: now,
		}); err != nil {
			return fmt.Errorf("record retry audit: %w", err)
		}
		// The statement only matches a dead row and writes it back as
		// queued, so the resulting state is that by construction; the row it
		// returned is what confirms the transition happened.
		result = &RetryJobResult{JobID: retried.ID, Type: retried.Type, State: domain.StateQueued}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// authorizeRetry applies the actor, step-up and reason rules before anything is
// read or written.
func authorizeRetry(command RetryJobCommand) error {
	if err := domain.ValidateReason(command.Reason); err != nil {
		return err
	}
	if !validActor(command.Actor) {
		return domain.ErrEmptyActor
	}
	if _, err := domain.StepUpSatisfied(domain.ActionRetryDeadJob, command.SessionAge); err != nil {
		return err
	}
	if !validJobReference(command.JobID) {
		return ErrJobNotFound
	}
	return nil
}

// validActor rejects an empty or oversized operator identity. The actor is a
// server-resolved account identifier, never a client claim.
func validActor(actor string) bool {
	trimmed := strings.TrimSpace(actor)
	return trimmed != "" && len(trimmed) <= domain.MaxLeaseOwnerLength
}

// validJobReference rejects an identifier that could not be a job.
func validJobReference(id string) bool {
	trimmed := strings.TrimSpace(id)
	return trimmed != "" && len(trimmed) <= 64
}
