// Package application orchestrates transactional email: it composes the
// localized body through the Renderer port and hands the frozen message to
// the Sender port (P15-T03).
//
// The layer defines its ports locally, as the rest of the codebase does, and
// stays standard library only: rendering engines and HTTP clients are
// adapter concerns the composition root wires in.
package application

import (
	"context"
	"errors"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// Renderer turns a template, a locale and the runtime values into the
// rendered representations of the message. Implementations live in
// adapters and own both the catalog lookup and the contextual escaping.
type Renderer interface {
	// Render composes the body. It fails when the template or locale is
	// unknown, or when a value may not travel in a message; the failure is
	// permanent, so the caller must not retry it.
	Render(templateID domain.TemplateID, locale domain.Locale, values domain.TemplateValues) (domain.Body, error)
}

// Sender delivers a fully composed message. Implementations are provider
// adapters and must be safe for concurrent use.
type Sender interface {
	// Send delivers the message and returns the provider receipt. A
	// failure is returned wrapped around one of the provider sentinels of
	// this package, so the caller can decide whether retrying helps.
	Send(ctx context.Context, message domain.Message) (Receipt, error)
}

// Receipt is the provider's acknowledgement of one delivery.
type Receipt struct {
	// ProviderID is the identifier the provider assigned to the accepted
	// message. It carries no personal data, so it is safe to log.
	ProviderID string
}

// The provider failure taxonomy. A durable job retries only the failures
// that classifies as retryable; everything else is a permanent rejection
// and must not consume the attempt budget.
var (
	// ErrProviderTimeout reports that the provider did not answer within
	// the configured deadline.
	ErrProviderTimeout = errors.New("notifications: provider timeout")

	// ErrProviderUnavailable reports a transport failure or a 5xx answer:
	// the provider did not process the request.
	ErrProviderUnavailable = errors.New("notifications: provider unavailable")

	// ErrProviderRateLimited reports throttling. Retrying is correct, but
	// the caller must back off rather than spin.
	ErrProviderRateLimited = errors.New("notifications: provider rate limited")

	// ErrProviderRejected reports a 4xx answer: the provider understood
	// the request and refused it. Retrying the same request cannot change
	// the outcome, so the job must not burn attempts on it.
	ErrProviderRejected = errors.New("notifications: provider rejected the request")
)

// IsRetryable reports whether a send failure may succeed on a later
// attempt. A nil error is not a failure.
func IsRetryable(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrProviderTimeout),
		errors.Is(err, ErrProviderUnavailable),
		errors.Is(err, ErrProviderRateLimited):
		return true
	default:
		return false
	}
}
