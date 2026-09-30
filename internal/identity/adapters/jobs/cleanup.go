// Package jobs adapts the identity module's scheduled maintenance to the
// platform worker (P16-T06).
//
// It is the same shape the other workload adapters use: the module states the
// use case, and this package states which workload runs it and at which payload
// version, so the composition root registers a name instead of constructing an
// interpretation of a job row.
package jobs

import (
	"context"
	"errors"
	"fmt"

	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// ErrMissingDependency reports a handler composed without the use case it runs
// or a registration without a registry. Both are wiring faults, and failing
// closed keeps them one.
var ErrMissingDependency = errors.New("identity jobs: missing dependency")

// CleanupPayloadVersion is the payload schema this handler understands.
//
// The version is stated even though the payload is not interpreted: the
// registry keys on the pair, so a future payload that does carry something is a
// v2 handler rather than a v1 handler that quietly changed meaning.
const CleanupPayloadVersion = 1

// CleanupPass is the maintenance pass the handler runs, stated as the consumer
// needs it: one call, one count. The use case satisfies it, and the narrow port
// is what makes the handler testable without a database.
type CleanupPass interface {
	Execute(ctx context.Context) (int64, error)
}

// CleanupHandler runs the session cleanup pass of the session store.
//
// The payload is deliberately not read. What the pass does is derived from the
// session policy and the current instant, not from the period that queued it,
// which is exactly why re-running a period is harmless and why an operator may
// retry a dead row by hand: the sweep is idempotent by construction, and the
// scheduler's period is bookkeeping rather than an input.
type CleanupHandler struct {
	cleanup CleanupPass
}

// NewCleanupHandler wires the handler. A nil pass fails at construction: a
// registered handler that cannot run would turn every period into a dead job.
func NewCleanupHandler(cleanup CleanupPass) (*CleanupHandler, error) {
	if cleanup == nil {
		return nil, ErrMissingDependency
	}
	return &CleanupHandler{cleanup: cleanup}, nil
}

// Type reports the workload this handler consumes.
func (h *CleanupHandler) Type() jobsdomain.JobType { return jobsdomain.TypeSessionCleanup }

// Version reports the payload schema the handler understands.
func (h *CleanupHandler) Version() int { return CleanupPayloadVersion }

// Handle runs one cleanup pass.
func (h *CleanupHandler) Handle(ctx context.Context, job *jobsdomain.Job) error {
	if h == nil || h.cleanup == nil {
		return ErrMissingDependency
	}
	if job == nil {
		return errors.New("identity jobs: nil job")
	}
	if job.Type != jobsdomain.TypeSessionCleanup || job.Version != CleanupPayloadVersion {
		// The registry should have refused this pairing; reaching it here
		// means the row is not ours to run.
		return fmt.Errorf("identity jobs: unsupported job %s v%d", job.Type, job.Version)
	}
	if _, err := h.cleanup.Execute(ctx); err != nil {
		// The failure is returned unclassified: the worker records it and
		// applies the attempt budget, and the next period retries the sweep
		// with the policy it reads then.
		return fmt.Errorf("identity jobs: session cleanup: %w", err)
	}
	return nil
}

// Register installs the handler in a worker registry, so the composition root
// does not have to know the type and version pair.
func (h *CleanupHandler) Register(registry *jobsapp.HandlerMap) error {
	if registry == nil {
		return ErrMissingDependency
	}
	return registry.Register(h.Type(), h.Version(), h.Handle)
}
