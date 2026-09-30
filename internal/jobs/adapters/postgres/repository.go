// Package postgres is the PostgreSQL outbound adapter of the jobs module
// (P15-T01). It implements the application queue port against app.jobs
// (migration 00029): enqueue is idempotent by caller-chosen key, claiming one
// due job is a single statement using `FOR UPDATE SKIP LOCKED` — so concurrent
// workers never lease the same row — and completion/failure are lease-guarded
// so a stale holder cannot overwrite a newer outcome.
//
// When the context carries a shared transaction the statements join it, so an
// enqueue can commit together with the domain effect it belongs to.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Repository implements the jobs application queue port using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
}

var _ jobsapp.QueueRepository = (*Repository)(nil)

// NewRepository creates a PostgreSQL repository adapter for the job queue.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

// queriesFor joins the caller transaction when the context carries one.
func (r *Repository) queriesFor(ctx context.Context) *platformpg.Queries {
	if tx, ok := platformpg.TxFromContext(ctx); ok {
		return r.queries.WithTx(tx)
	}
	return r.queries
}

// Enqueue stores one unit of work, resolving an existing idempotency key
// instead of creating a second job.
func (r *Repository) Enqueue(ctx context.Context, record jobsapp.EnqueueRecord) (*jobsdomain.Job, bool, error) {
	version, err := narrowInt32(record.Version, jobsdomain.ErrInvalidVersion)
	if err != nil {
		return nil, false, err
	}
	maxAttempts, err := narrowInt32(record.MaxAttempts, jobsapp.ErrInvalidMaxAttempts)
	if err != nil {
		return nil, false, err
	}

	queries := r.queriesFor(ctx)
	row, err := queries.EnqueueJob(ctx, platformpg.EnqueueJobParams{
		Type:    string(record.Type),
		Version: version,
		// The domain speaks of the job payload; the column is
		// `parameters` (the schema reserves `%payload%` for the provider
		// body digest), and this is the same object.
		Parameters:     record.Payload,
		IdempotencyKey: pgTextOrNull(record.IdempotencyKey),
		AvailableAt:    pgTimestamptz(record.AvailableAt),
		MaxAttempts:    maxAttempts,
		CreatedAt:      pgTimestamptz(record.CreatedAt),
	})
	if err == nil {
		job, convErr := jobFromRow(row)
		return job, false, convErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("enqueue job: %w", err)
	}
	if record.IdempotencyKey == "" {
		return nil, false, fmt.Errorf("enqueue job: %w", err)
	}

	// A conflicting key resolves the original job: retrying an enqueue
	// repairs a lost request without creating duplicate work.
	existing, err := queries.GetJobByIdempotencyKey(ctx, record.IdempotencyKey)
	if err != nil {
		return nil, false, fmt.Errorf("load replayed job: %w", err)
	}
	job, convErr := jobFromRow(existing)
	return job, true, convErr
}

// Lease claims the next due job, or returns nil when none is due.
func (r *Repository) Lease(ctx context.Context, owner string, now, until time.Time) (*jobsdomain.Job, error) {
	row, err := r.queriesFor(ctx).LeaseJob(ctx, platformpg.LeaseJobParams{
		LeaseOwner:  owner,
		LeasedUntil: pgTimestamptz(until),
		Now:         pgTimestamptz(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lease job: %w", err)
	}
	return jobFromRow(row)
}

// Complete records success for a job the caller still leases.
func (r *Repository) Complete(ctx context.Context, jobID, owner string, now time.Time) (*jobsdomain.Job, error) {
	id, err := pgUUIDFromString(jobID)
	if err != nil {
		return nil, jobsapp.ErrLeaseNotHeld
	}
	row, err := r.queriesFor(ctx).CompleteJob(ctx, platformpg.CompleteJobParams{
		Now:        pgTimestamptz(now),
		ID:         id,
		LeaseOwner: owner,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, jobsapp.ErrLeaseNotHeld
	}
	if err != nil {
		return nil, fmt.Errorf("complete job: %w", err)
	}
	return jobFromRow(row)
}

// Fail records a redacted failure, requeueing or promoting the job to dead.
func (r *Repository) Fail(ctx context.Context, failure jobsdomain.Failure, jobID, owner string, retryAt, now time.Time) (*jobsdomain.Job, error) {
	id, err := pgUUIDFromString(jobID)
	if err != nil {
		return nil, jobsapp.ErrLeaseNotHeld
	}
	row, err := r.queriesFor(ctx).FailJob(ctx, platformpg.FailJobParams{
		RetryAt:     pgTimestamptz(retryAt),
		ErrorCode:   string(failure.Code),
		ErrorDetail: failure.Detail,
		Now:         pgTimestamptz(now),
		ID:          id,
		LeaseOwner:  owner,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, jobsapp.ErrLeaseNotHeld
	}
	if err != nil {
		return nil, fmt.Errorf("fail job: %w", err)
	}
	return jobFromRow(row)
}

// ReleaseExpiredLeases returns abandoned work to the queue.
func (r *Repository) ReleaseExpiredLeases(ctx context.Context, now time.Time) (int, error) {
	reclaimed, err := r.queriesFor(ctx).ReleaseExpiredLeases(ctx, pgTimestamptz(now))
	if err != nil {
		return 0, fmt.Errorf("release expired leases: %w", err)
	}
	return int(reclaimed), nil
}

// JobByID loads one job; a missing or malformed identifier is ErrJobNotFound.
func (r *Repository) JobByID(ctx context.Context, id string) (*jobsdomain.Job, error) {
	uuid, err := pgUUIDFromString(id)
	if err != nil {
		return nil, jobsapp.ErrJobNotFound
	}
	row, err := r.queriesFor(ctx).GetJob(ctx, uuid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, jobsapp.ErrJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load job: %w", err)
	}
	return jobFromRow(row)
}

// jobFromRow converts a stored row into a validated domain entity, so a row
// that violates an invariant fails loudly instead of being handed to a worker.
func jobFromRow(row platformpg.AppJob) (*jobsdomain.Job, error) {
	job := &jobsdomain.Job{
		ID:              uuidToString(row.ID),
		Type:            jobsdomain.JobType(row.Type),
		Version:         int(row.Version),
		Payload:         row.Parameters,
		IdempotencyKey:  stringFromPg(row.IdempotencyKey),
		State:           jobsdomain.JobState(row.State),
		AvailableAt:     timeFromPg(row.AvailableAt),
		Attempts:        int(row.Attempts),
		MaxAttempts:     int(row.MaxAttempts),
		LeaseOwner:      stringFromPg(row.LeaseOwner),
		LeasedUntil:     timeFromPg(row.LeasedUntil),
		LastErrorCode:   jobsdomain.FailureCode(stringFromPg(row.LastErrorCode)),
		LastErrorDetail: stringFromPg(row.LastErrorDetail),
		CreatedAt:       timeFromPg(row.CreatedAt),
		UpdatedAt:       timeFromPg(row.UpdatedAt),
	}
	if err := job.Validate(); err != nil {
		return nil, fmt.Errorf("stored job %s is incoherent: %w", job.ID, err)
	}
	return job, nil
}

// narrowInt32 refuses a value that cannot be stored in an integer column
// instead of silently truncating it.
func narrowInt32(value int, invalid error) (int32, error) {
	if value < math.MinInt32 || value > math.MaxInt32 {
		return 0, invalid
	}
	return int32(value), nil
}

func pgTimestamptz(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func timeFromPg(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

func pgTextOrNull(raw string) pgtype.Text {
	if raw == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: raw, Valid: true}
}

func stringFromPg(raw pgtype.Text) string {
	if !raw.Valid {
		return ""
	}
	return raw.String
}

func pgUUIDFromString(raw string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid identifier format")
	}
	return id, nil
}

func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
