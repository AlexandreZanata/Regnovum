package testsupport

import (
	"encoding/json"
	"time"

	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// JobOption varies the job a builder makes.
type JobOption func(*jobSpec)

type jobSpec struct {
	payload        map[string]any
	maxAttempts    int
	idempotencyKey string
	availableAt    *time.Time
}

// WithJobPayload declares the parameters of the job. The payload is serialized
// and judged by the same rule the queue applies, so a payload that is not an
// object or nests too deeply is refused here.
func WithJobPayload(payload map[string]any) JobOption {
	return func(spec *jobSpec) { spec.payload = payload }
}

// WithJobMaxAttempts declares the attempt budget of the job.
func WithJobMaxAttempts(attempts int) JobOption {
	return func(spec *jobSpec) { spec.maxAttempts = attempts }
}

// WithJobIdempotencyKey declares a caller-chosen retry key, which is how a
// scenario asserts that the same retry does not run twice.
func WithJobIdempotencyKey(key string) JobOption {
	return func(spec *jobSpec) { spec.idempotencyKey = key }
}

// WithJobAvailableAt declares the instant the job becomes runnable, which is
// how a scenario schedules work in the future without waiting for it.
func WithJobAvailableAt(availableAt time.Time) JobOption {
	return func(spec *jobSpec) {
		instant := availableAt.UTC()
		spec.availableAt = &instant
	}
}

// Job builds one queued unit of work: the workload name from the closed
// vocabulary, a versioned JSON-object payload, an attempt budget, and an
// identifier and key of the scenario's own.
//
// The value is judged by Job.Validate before it is answered, so a scenario never
// hands the queue a row the queue would refuse — including the coherence
// between state, lease and failure, which is the pair of invariants most often
// broken by hand-written fixtures.
func (b *Builder) Job(jobType jobsdomain.JobType, options ...JobOption) (jobsdomain.Job, error) {
	b.t.Helper()
	spec := jobSpec{
		maxAttempts: jobsdomain.DefaultMaxAttempts,
	}
	if !jobType.IsValid() {
		return jobsdomain.Job{}, jobsdomain.ErrUnknownJobType
	}
	for _, option := range options {
		option(&spec)
	}

	if spec.payload == nil {
		spec.payload = map[string]any{"job": string(jobType)}
	}
	payload, err := json.Marshal(spec.payload)
	if err != nil {
		return jobsdomain.Job{}, err
	}

	instant := b.Now()
	availableAt := instant
	if spec.availableAt != nil {
		availableAt = spec.availableAt.UTC()
	}

	job := jobsdomain.Job{
		ID:             b.Identifier(),
		Type:           jobType,
		Version:        1,
		Payload:        payload,
		IdempotencyKey: spec.idempotencyKey,
		State:          jobsdomain.StateQueued,
		AvailableAt:    availableAt,
		Attempts:       0,
		MaxAttempts:    spec.maxAttempts,
		CreatedAt:      instant,
		UpdatedAt:      instant,
	}
	if err := job.Validate(); err != nil {
		return jobsdomain.Job{}, err
	}
	return job, nil
}
