package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// AccountRef identifies the account behind an address and the locale its
// owner prefers.
type AccountRef struct {
	// ID is the account identifier. Empty means the directory knows no
	// account for the address, which is not an error: the notification is
	// still composed, in the product default locale.
	ID string
	// Locale is the canonical preferred locale, or the empty string when
	// the account stated no preference. An unknown preference is never
	// guessed at: the caller falls back to the product default, with the
	// same rule the interface uses.
	Locale domain.Locale
}

// AccountDirectory resolves the account that receives a notification. It is
// the read path the notifications module needs from identity and profiles,
// expressed as one question instead of two module imports.
type AccountDirectory interface {
	// AccountForAddress resolves the account and its preferred locale. An
	// unknown address or an absent preference returns the zero AccountRef
	// without error; an infrastructure failure returns an error, which the
	// caller must not swallow — the enqueue belongs to the same transaction
	// as the event it announces.
	AccountForAddress(ctx context.Context, address string) (AccountRef, error)
}

// NotificationRequest is one notification the product wants to deliver.
type NotificationRequest struct {
	// Template is the message to compose; it must belong to the closed set.
	Template domain.TemplateID
	// Recipient is the address the message goes to.
	Recipient string
	// Values are the runtime values of the template.
	Values domain.TemplateValues
	// Locale freezes the locale of the message. Nil means "resolve the
	// owner's preference now", which is what every event-driven flow wants:
	// the job stores the locale, so a later preference change cannot
	// retarget a message already queued.
	Locale *domain.Locale
	// EventKey is the caller's stable identity of the event. Empty derives
	// one from the template, the address and the code.
	EventKey string
}

// ResolvedNotification is a notification whose locale is already frozen and
// whose enqueue key is already derived, ready to be persisted.
type ResolvedNotification struct {
	Template  domain.TemplateID
	Recipient string
	Values    domain.TemplateValues
	Locale    domain.Locale
	EventKey  string
}

// DurableWork is the persisted unit of work that will deliver the message.
type DurableWork struct {
	// JobID identifies the queued job.
	JobID string
	// Replayed reports that the event key resolved an existing job instead
	// of queueing a second one.
	Replayed bool
}

// NotificationEnqueuer persists a notification as durable work.
//
// The adapter that implements it joins the caller's transaction when the
// context carries one, so the job row commits with the domain effect it
// announces. That is the whole reason the port takes the context and returns
// nothing but an identifier: an email is never enqueued before the event it
// describes is durable, and once it is enqueued the delivery outlives the
// process that scheduled it.
type NotificationEnqueuer interface {
	// Enqueue stores one notification and returns its identifier.
	Enqueue(ctx context.Context, notification ResolvedNotification) (DurableWork, error)
}

// Notifier turns a product intent into durable work: it freezes the locale and
// the enqueue key, so the queue row says exactly what will be delivered and
// exactly which event it belongs to.
type Notifier struct {
	directory AccountDirectory
	enqueuer  NotificationEnqueuer
}

// NewNotifier wires the use case. A nil port fails at construction.
func NewNotifier(directory AccountDirectory, enqueuer NotificationEnqueuer) (*Notifier, error) {
	if directory == nil || enqueuer == nil {
		return nil, domain.ErrMissingDependency
	}
	return &Notifier{directory: directory, enqueuer: enqueuer}, nil
}

// Notify validates the request, resolves the locale and enqueues the work.
//
// A refused request never reaches the queue, and an error is returned rather
// than logged: the caller runs inside the transaction that creates the event,
// so a notification that cannot be queued must fail that transaction instead
// of silently leaving an event with no notification.
func (n *Notifier) Notify(ctx context.Context, request NotificationRequest) (DurableWork, error) {
	if n == nil || n.directory == nil || n.enqueuer == nil {
		return DurableWork{}, domain.ErrMissingDependency
	}
	if ctx == nil {
		return DurableWork{}, errors.New("notifications: nil context")
	}
	if !request.Template.Valid() {
		return DurableWork{}, domain.ErrUnsupportedTemplate
	}
	if _, err := domain.ValidateTemplateValues(request.Template, request.Values.Name, request.Values.Code); err != nil {
		return DurableWork{}, err
	}
	// The address is validated before the directory is consulted: asking
	// about a malformed address would be a pointless round trip, and an
	// address the composed message would refuse must never occupy a queue
	// row.
	if err := domain.ValidateRecipient(request.Recipient); err != nil {
		return DurableWork{}, err
	}

	locale := domain.LocaleDefault
	if request.Locale != nil {
		if !request.Locale.Valid() {
			return DurableWork{}, domain.ErrUnsupportedLocale
		}
		locale = *request.Locale
	} else {
		ref, err := n.directory.AccountForAddress(ctx, request.Recipient)
		if err != nil {
			return DurableWork{}, fmt.Errorf("notifications: resolve account: %w", err)
		}
		if ref.Locale.Valid() {
			locale = ref.Locale
		}
	}

	key := request.EventKey
	if key == "" {
		key = domain.EventKey(request.Template, request.Recipient, request.Values.Code)
	}

	return n.enqueuer.Enqueue(ctx, ResolvedNotification{
		Template:  request.Template,
		Recipient: request.Recipient,
		Values:    request.Values,
		Locale:    locale,
		EventKey:  key,
	})
}
