package outbox

import (
	"context"
	"fmt"
	"strings"

	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// Sender satisfies the identity module's email port by queueing the message
// instead of sending it (P15-T04).
//
// This is the bridge pattern the codebase already uses where one module needs
// another's capability: identity declares what it wants ("deliver or enqueue
// this verification email"), and this adapter decides how, without identity
// learning anything about queues, templates or a provider.
//
// The port's methods return an error and the flows propagate it, so a queue
// that cannot accept the job fails the operation that was creating the event.
// That is the deliberate choice: an account created in a state where its
// verification message was silently dropped would be a user with no way in.
type Sender struct {
	notifier *application.Notifier
}

var _ identityapp.EmailSender = (*Sender)(nil)

// NewSender wires the bridge. A nil notifier fails at construction.
func NewSender(notifier *application.Notifier) (*Sender, error) {
	if notifier == nil {
		return nil, domain.ErrMissingDependency
	}
	return &Sender{notifier: notifier}, nil
}

// SendVerificationEmail queues the confirmation of an email address.
func (s *Sender) SendVerificationEmail(ctx context.Context, email identitydomain.Email, token string) error {
	return s.queue(ctx, domain.TemplateVerification, email, token)
}

// SendPasswordResetEmail queues the password recovery code.
func (s *Sender) SendPasswordResetEmail(ctx context.Context, email identitydomain.Email, token string) error {
	return s.queue(ctx, domain.TemplatePasswordReset, email, token)
}

// SendPasswordChangedEmail queues the notice that the password changed.
//
// It is the same queue as the other two and deliberately a different shape: no
// token travels with it, so nothing in this message can be spent by whoever
// reads it — only the fact that the change happened.
func (s *Sender) SendPasswordChangedEmail(ctx context.Context, email identitydomain.Email, changeID string) error {
	if s == nil || s.notifier == nil {
		return domain.ErrMissingDependency
	}
	address := email.String()
	if address == "" {
		return domain.ErrInvalidRecipient
	}
	// The anchor is required, not optional: the enqueue key is what keeps a
	// retry of one change from sending twice and, symmetrically, what keeps
	// the next change from being swallowed as a replay of the previous one. A
	// notice with no anchor would be deduplicated forever after the first
	// change of the account.
	anchor, err := changeAnchor(changeID)
	if err != nil {
		return err
	}
	values, err := domain.NewNoticeValues("")
	if err != nil {
		return err
	}
	if _, err := s.notifier.Notify(ctx, application.NotificationRequest{
		Template:  domain.TemplatePasswordChanged,
		Recipient: address,
		Values:    values,
		// The anchor plays the role the one-time code plays for the other
		// templates: it is the part of the event that makes it unique.
		EventKey: domain.EventKey(domain.TemplatePasswordChanged, address, anchor),
	}); err != nil {
		return fmt.Errorf("outbox: queue %s: %w", domain.TemplatePasswordChanged, err)
	}
	return nil
}

// changeAnchor validates the identifier the caller states for one change. The
// alphabet is the one the enqueue key accepts, so a caller-crafted value cannot
// smuggle a separator or a prefix into the key it becomes, and it is bounded so
// the key stays within the column the queue validates.
func changeAnchor(changeID string) (string, error) {
	trimmed := strings.TrimSpace(changeID)
	if trimmed == "" || len(trimmed) > maxChangeAnchorBytes {
		return "", domain.ErrInvalidIdempotencyKey
	}
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9':
			continue
		case strings.ContainsRune("._:-@", char):
			continue
		default:
			return "", domain.ErrInvalidIdempotencyKey
		}
	}
	return trimmed, nil
}

// maxChangeAnchorBytes bounds the caller-stated identifier of one change.
const maxChangeAnchorBytes = 100

func (s *Sender) queue(ctx context.Context, templateID domain.TemplateID, email identitydomain.Email, token string) error {
	if s == nil || s.notifier == nil {
		return domain.ErrMissingDependency
	}
	address := email.String()
	if address == "" {
		return domain.ErrInvalidRecipient
	}
	// The code is the runtime value of the template; the display name is not
	// known at this layer, so the greeting degrades to its generic form
	// instead of inventing one.
	values, err := domain.NewTemplateValues("", token)
	if err != nil {
		return err
	}
	if _, err := s.notifier.Notify(ctx, application.NotificationRequest{
		Template:  templateID,
		Recipient: address,
		Values:    values,
	}); err != nil {
		// The template name is safe to name; the address and the code are not.
		return fmt.Errorf("outbox: queue %s: %w", templateID, err)
	}
	return nil
}
