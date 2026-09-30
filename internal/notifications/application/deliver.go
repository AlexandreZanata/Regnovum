package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// DeliverRequest is one transactional email to compose and deliver. The
// locale is part of the request, not looked up here: whoever enqueues the
// job freezes it, so a later preference change cannot retarget a message.
type DeliverRequest struct {
	Recipient      string
	Locale         domain.Locale
	Template       domain.TemplateID
	Values         domain.TemplateValues
	IdempotencyKey string
}

// Deliverer is the port the rest of the product uses to send one email: it
// renders, validates and sends, in that order, so nothing malformed ever
// reaches a provider.
type Deliverer struct {
	renderer Renderer
	sender   Sender
}

// NewDeliverer wires the use case. A nil port fails at construction: a
// misconfigured composition root must not discover itself on a user request.
func NewDeliverer(renderer Renderer, sender Sender) (*Deliverer, error) {
	if renderer == nil || sender == nil {
		return nil, domain.ErrMissingDependency
	}
	return &Deliverer{renderer: renderer, sender: sender}, nil
}

// Deliver renders the request into a message and sends it. Failures are
// wrapped around the provider sentinels (see IsRetryable) and never quote
// the recipient, the code or the idempotency key: a delivery error travels
// into logs and job rows.
func (d *Deliverer) Deliver(ctx context.Context, request DeliverRequest) (Receipt, error) {
	if d == nil || d.renderer == nil || d.sender == nil {
		return Receipt{}, domain.ErrMissingDependency
	}
	if ctx == nil {
		return Receipt{}, errors.New("notifications: nil context")
	}
	if !request.Template.Valid() {
		return Receipt{}, domain.ErrUnsupportedTemplate
	}
	if !request.Locale.Valid() {
		return Receipt{}, domain.ErrUnsupportedLocale
	}
	body, err := d.renderer.Render(request.Template, request.Locale, request.Values)
	if err != nil {
		return Receipt{}, fmt.Errorf("notifications: render %s: %w", request.Template, err)
	}
	message, err := domain.NewMessage(request.Recipient, request.Locale, request.Template, body, request.IdempotencyKey)
	if err != nil {
		return Receipt{}, fmt.Errorf("notifications: compose %s: %w", request.Template, err)
	}
	receipt, err := d.sender.Send(ctx, message)
	if err != nil {
		// The template name is safe to name; the recipient is not.
		return Receipt{}, fmt.Errorf("notifications: send %s: %w", request.Template, err)
	}
	return receipt, nil
}
