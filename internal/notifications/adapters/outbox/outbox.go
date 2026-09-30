// Package outbox turns product notifications into durable jobs and delivers
// them from the worker (P15-T04).
//
// It is the seam where the notifications module meets identity, profiles and
// the queue, and it is deliberately one package: the bridge a use case calls,
// the directory that resolves the recipient facts, and the handler that runs
// in the worker all interpret the same payload, and splitting them would let
// three copies of that format drift apart.
//
// The transaction story is the point of the package. Enqueue goes through the
// jobs use case, whose repository joins the transaction carried by the
// context; so when the calling use case opens one, the job row commits with
// the domain effect it announces and rolls back with it. A message is never
// queued for an event that did not happen, and a queue row never survives an
// event that was rolled back.
package outbox

import (
	"context"
	"encoding/json"
	"errors"

	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// PayloadVersion is the schema version of the email payload. It travels in the
// job row, so a later change adds a version and a handler instead of
// reinterpreting rows already queued.
const PayloadVersion = 1

// payloadV1 is the persisted form of one notification: the frozen facts of the
// message (template, locale, address, runtime values) and never a rendered
// body. Rendering belongs to the worker, so a translator's fix applies to a
// job that is still queued, and a retry renders in the locale the job was
// created with.
type payloadV1 struct {
	Template  string `json:"template"`
	Locale    string `json:"locale"`
	Recipient string `json:"recipient"`
	Name      string `json:"name"`
	Code      string `json:"code"`
}

// Enqueuer persists a resolved notification as an email job.
type Enqueuer struct {
	enqueue *jobsapp.EnqueueUseCase
}

// NewEnqueuer wires the adapter with the jobs enqueue use case.
func NewEnqueuer(enqueue *jobsapp.EnqueueUseCase) (*Enqueuer, error) {
	if enqueue == nil {
		return nil, domain.ErrMissingDependency
	}
	return &Enqueuer{enqueue: enqueue}, nil
}

// Enqueue stores one notification as durable work.
//
// The payload is encoded here and validated by the jobs domain before it is
// stored, so an unsupported template or an oversized value fails the caller's
// transaction instead of producing a job no worker can run.
func (e *Enqueuer) Enqueue(ctx context.Context, notification application.ResolvedNotification) (application.DurableWork, error) {
	if e == nil || e.enqueue == nil {
		return application.DurableWork{}, domain.ErrMissingDependency
	}
	if ctx == nil {
		return application.DurableWork{}, errors.New("outbox: nil context")
	}
	if err := validateResolved(notification); err != nil {
		return application.DurableWork{}, err
	}
	body, err := json.Marshal(payloadV1{
		Template:  notification.Template.String(),
		Locale:    notification.Locale.String(),
		Recipient: notification.Recipient,
		Name:      notification.Values.Name,
		Code:      notification.Values.Code,
	})
	if err != nil {
		return application.DurableWork{}, errors.New("outbox: encode payload")
	}
	result, err := e.enqueue.Enqueue(ctx, jobsapp.EnqueueCommand{
		Type:    jobsdomain.TypeEmailDelivery,
		Version: PayloadVersion,
		Payload: body,
		// The event key is the caller's retry anchor: repeating the same
		// event resolves the original job instead of queueing a second
		// message.
		IdempotencyKey: notification.EventKey,
		MaxAttempts:    jobsdomain.DefaultMaxAttempts,
	})
	if err != nil {
		return application.DurableWork{}, err
	}
	if result == nil || result.Job == nil {
		return application.DurableWork{}, errors.New("outbox: enqueue returned no job")
	}
	return application.DurableWork{JobID: result.Job.ID, Replayed: result.Replayed}, nil
}

// validateResolved re-checks the resolved notification at the persistence
// boundary. The enqueuer is reachable from more than one bridge, so it does
// not assume the caller validated anything.
func validateResolved(notification application.ResolvedNotification) error {
	if !notification.Template.Valid() {
		return domain.ErrUnsupportedTemplate
	}
	if !notification.Locale.Valid() {
		return domain.ErrUnsupportedLocale
	}
	if err := domain.ValidateRecipient(notification.Recipient); err != nil {
		return err
	}
	if _, err := domain.ValidateTemplateValues(notification.Template, notification.Values.Name, notification.Values.Code); err != nil {
		return err
	}
	return domain.ValidateEventKey(notification.EventKey)
}

// decodePayload reads one stored payload back into the frozen notification.
// Every field is validated against the same closed sets the producer used, so
// a hand-edited or truncated row is refused instead of delivered.
func decodePayload(body []byte) (application.ResolvedNotification, error) {
	if len(body) == 0 {
		return application.ResolvedNotification{}, errors.New("outbox: empty payload")
	}
	var decoded payloadV1
	if err := json.Unmarshal(body, &decoded); err != nil {
		return application.ResolvedNotification{}, errors.New("outbox: malformed payload")
	}
	templateID := domain.TemplateID(decoded.Template)
	if !templateID.Valid() {
		return application.ResolvedNotification{}, domain.ErrUnsupportedTemplate
	}
	locale, err := domain.ParseLocale(decoded.Locale)
	if err != nil {
		return application.ResolvedNotification{}, err
	}
	// The values are validated against the template they were frozen with:
	// a code-carrying payload whose code was lost and a notice payload that
	// gained one are both refused here, before a provider sees either.
	values, err := domain.ValidateTemplateValues(templateID, decoded.Name, decoded.Code)
	if err != nil {
		return application.ResolvedNotification{}, err
	}
	if err := domain.ValidateRecipient(decoded.Recipient); err != nil {
		return application.ResolvedNotification{}, err
	}
	return application.ResolvedNotification{
		Template:  templateID,
		Locale:    locale,
		Recipient: decoded.Recipient,
		Values:    values,
	}, nil
}
