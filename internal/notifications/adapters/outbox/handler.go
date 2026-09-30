package outbox

import (
	"context"
	"errors"
	"fmt"

	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// Handler delivers the queued email jobs of the worker (P15-T04).
//
// It runs after the event is committed, in its own transaction, which is the
// property that keeps a delivery failure from touching the domain: by the time
// a worker sees the job, the account, the token or the decision it announces
// is already durable, and the worst a failing provider can do is return the
// job to the queue.
type Handler struct {
	deliverer *application.Deliverer
}

// NewHandler wires the handler. A nil deliverer fails at construction.
func NewHandler(deliverer *application.Deliverer) (*Handler, error) {
	if deliverer == nil {
		return nil, domain.ErrMissingDependency
	}
	return &Handler{deliverer: deliverer}, nil
}

// Type reports the workload the handler consumes.
func (h *Handler) Type() jobsdomain.JobType { return jobsdomain.TypeEmailDelivery }

// Version reports the payload schema the handler understands. The registry
// keys on (type, version), so a v2 payload is never interpreted as v1.
func (h *Handler) Version() int { return PayloadVersion }

// Handle delivers one queued notification.
//
// The message is rendered here, from the payload's frozen locale, and the
// provider idempotency key is derived from the job: every retry of this row
// reaches the provider as the same logical send, so an attempt that timed out
// after the provider accepted it cannot produce a second email.
func (h *Handler) Handle(ctx context.Context, job *jobsdomain.Job) error {
	if h == nil || h.deliverer == nil {
		return domain.ErrMissingDependency
	}
	if job == nil {
		return errors.New("outbox: nil job")
	}
	if job.Type != jobsdomain.TypeEmailDelivery || job.Version != PayloadVersion {
		// The registry should have refused this pairing; reaching it here
		// means the job is not ours to run.
		return fmt.Errorf("outbox: unsupported job %s v%d", job.Type, job.Version)
	}
	notification, err := decodePayload(job.Payload)
	if err != nil {
		// A payload that cannot be understood is permanent: no retry
		// changes it, and the attempt budget bounds how long the row sits
		// in the queue before it is recorded as dead.
		return fmt.Errorf("outbox: decode payload: %w", err)
	}
	deliveryKey, err := domain.DeliveryKey(job.ID)
	if err != nil {
		return fmt.Errorf("outbox: derive delivery key: %w", err)
	}
	if _, err := h.deliverer.Deliver(ctx, application.DeliverRequest{
		Recipient:      notification.Recipient,
		Locale:         notification.Locale,
		Template:       notification.Template,
		Values:         notification.Values,
		IdempotencyKey: deliveryKey,
	}); err != nil {
		// The failure is returned unclassified: the worker records it and
		// applies the attempt budget, while application.IsRetryable is what
		// tells whether the retry can help.
		return fmt.Errorf("outbox: deliver %s: %w", notification.Template, err)
	}
	return nil
}

// Register installs the handler in a worker registry, so the composition root
// does not have to know the type and version pair.
func (h *Handler) Register(registry *jobsapp.HandlerMap) error {
	if registry == nil {
		return domain.ErrMissingDependency
	}
	return registry.Register(h.Type(), h.Version(), h.Handle)
}
