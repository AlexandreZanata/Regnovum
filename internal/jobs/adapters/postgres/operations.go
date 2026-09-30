package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// The operational surface of the queue (P15-T06): what an operator reads and
// the one write they are allowed to make.
//
// Every projection here is chosen so the answer cannot carry a payload. The
// health query counts rows, the dead list selects the lifecycle columns, and
// the retry returns the transitioned row: none of them selects `parameters`,
// which is the column a job's arguments live in. That is a property of the
// statements rather than of a filter applied afterwards, so no code path can
// forget to strip it.

// QueueHealth measures the queue at the given instant.
func (r *Repository) QueueHealth(ctx context.Context, now time.Time) (*jobsapp.QueueHealth, error) {
	row, err := r.queriesFor(ctx).GetQueueHealth(ctx, pgTimestamptz(now))
	if err != nil {
		return nil, fmt.Errorf("job queue health: %w", err)
	}
	health := &jobsapp.QueueHealth{
		Queued:    row.Queued,
		Leased:    row.Leased,
		Succeeded: row.Succeeded,
		Dead:      row.Dead,
		DueNow:    row.DueNow,
	}
	// The lag is the wait of the oldest due job. A queue with nothing due is
	// not behind, whatever its size.
	if row.OldestDueAt.Valid {
		health.LagSeconds = elapsedSeconds(now, row.OldestDueAt.Time)
	}
	health.OldestDeadSeconds = elapsedSeconds(now, timeFromPg(row.OldestDeadAt))
	return health, nil
}

// elapsedSeconds measures how long ago an instant was. A timestamp in the
// future (a clock that moved backwards between the write and the read) reports
// zero instead of a negative wait.
func elapsedSeconds(now, then time.Time) int64 {
	if then.IsZero() {
		return 0
	}
	elapsed := now.UTC().Sub(then.UTC())
	if elapsed <= 0 {
		return 0
	}
	return int64(elapsed / time.Second)
}

// ListDeadJobs returns up to limit dead jobs, oldest first.
func (r *Repository) ListDeadJobs(ctx context.Context, limit int, now time.Time) ([]jobsapp.DeadJob, error) {
	narrowed, err := narrowInt32(limit, jobsapp.ErrInvalidQueueConfig)
	if err != nil {
		return nil, err
	}
	rows, err := r.queriesFor(ctx).ListDeadJobs(ctx, narrowed)
	if err != nil {
		return nil, fmt.Errorf("list dead jobs: %w", err)
	}
	out := make([]jobsapp.DeadJob, 0, len(rows))
	for _, row := range rows {
		job, err := deadJobFromRow(row, now)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, nil
}

// SessionAgeAt returns how long ago the session authenticated (P15-T06).
//
// The adapter reads the session the platform recorded and never a value the
// caller sent: a client that states its own freshness is asserting exactly what
// the step-up rule exists to verify. An unknown or malformed identifier denies
// distinctly instead of being treated as fresh.
func (r *Repository) SessionAgeAt(ctx context.Context, sessionID string, now time.Time) (time.Duration, error) {
	id, err := pgUUIDFromString(sessionID)
	if err != nil {
		return 0, jobsapp.ErrUnknownSession
	}
	createdAt, err := r.queries.GetSessionCreatedAt(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, jobsapp.ErrUnknownSession
		}
		return 0, fmt.Errorf("load session age: %w", err)
	}
	age := now.UTC().Sub(createdAt.Time.UTC())
	if age < 0 {
		age = 0
	}
	return age, nil
}

// RetryDeadJob returns one dead job to the queue. A row that is not dead
// matches nothing and resolves to nil, so two operators racing the same job
// cannot both report a retry.
func (r *Repository) RetryDeadJob(ctx context.Context, jobID string, now time.Time) (*jobsapp.DeadJob, error) {
	id, err := pgUUIDFromString(jobID)
	if err != nil {
		return nil, err
	}
	row, err := r.queriesFor(ctx).RetryDeadJob(ctx, platformpg.RetryDeadJobParams{
		ID:  id,
		Now: pgTimestamptz(now),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("retry dead job: %w", err)
	}
	jobType := domain.JobType(row.Type)
	if !jobType.IsValid() {
		return nil, domain.ErrUnknownJobType
	}
	return &jobsapp.DeadJob{
		ID:          uuidToString(row.ID),
		Type:        jobType,
		Version:     int(row.Version),
		Attempts:    int(row.Attempts),
		MaxAttempts: int(row.MaxAttempts),
		Retryable:   domain.RetryAllowed(jobType),
	}, nil
}

// deadJobFromRow maps one lifecycle projection, including the age the
// operational view reports instead of a timestamp.
func deadJobFromRow(row platformpg.ListDeadJobsRow, now time.Time) (jobsapp.DeadJob, error) {
	jobType := domain.JobType(row.Type)
	if !jobType.IsValid() {
		return jobsapp.DeadJob{}, domain.ErrUnknownJobType
	}
	job := jobsapp.DeadJob{
		ID:          uuidToString(row.ID),
		Type:        jobType,
		Version:     int(row.Version),
		Attempts:    int(row.Attempts),
		MaxAttempts: int(row.MaxAttempts),
		Retryable:   domain.RetryAllowed(jobType),
	}
	if row.LastErrorCode.Valid {
		job.LastErrorCode = domain.FailureCode(row.LastErrorCode.String)
	}
	job.AgeSeconds = elapsedSeconds(now, timeFromPg(row.UpdatedAt))
	return job, nil
}
