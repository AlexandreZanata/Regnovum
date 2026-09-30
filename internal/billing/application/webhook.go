package application

import (
	"context"
	"errors"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// WebhookPayloadVerifier verifies the authenticity of an inbound webhook
// payload. The port is transport-agnostic: it receives the raw body, the
// provider's signature header and the provider's timestamp header, and
// answers whether the payload is authentic and timely.
//
// The implementation belongs in the provider adapter (Stripe HMAC), not in
// the application or domain layers: the verification algorithm is a provider
// detail that must not leak into use cases.
type WebhookPayloadVerifier interface {
	// Verify checks that the payload was signed by the provider's secret
	// and was created within the tolerance window. It returns nil only when
	// both checks pass. A failure means the payload must be rejected with
	// HTTP 400; it must never be persisted.
	Verify(payload []byte, signatureHeader string, timestampHeader string) error
}

// WebhookEventRecord is the database view of one inbound webhook event.
type WebhookEventRecord struct {
	// ID is the local surrogate key.
	ID string
	// EventID is the provider's unique event identifier (evt_...).
	EventID domain.WebhookEventID
	// EventType is the provider event type (e.g.,
	// checkout.session.completed).
	EventType domain.WebhookEventType
	// Livemode pins the provider mode of the event.
	Livemode bool
	// StripeCreatedAt is the provider's creation instant of the event.
	StripeCreatedAt time.Time
	// PayloadSHA256 is the hex-encoded SHA-256 of the raw body.
	PayloadSHA256 string
	// PayloadBytes is the size in bytes of the raw body.
	PayloadBytes int
	// Status is the processing lifecycle of the event.
	Status WebhookEventStatus
	// Attempts counts how many times this event was claimed for processing.
	Attempts int
	// LastError is the bounded reason of the latest failure; only present
	// when Status is failed.
	LastError string
	// ReceivedAt is the instant the event was first received locally.
	ReceivedAt time.Time
	// ProcessedAt is the instant the event processing completed (terminal).
	ProcessedAt *time.Time
}

// WebhookEventStatus is the processing lifecycle of an inbound webhook event.
type WebhookEventStatus string

const (
	// WebhookEventReceived is the initial state: the event was persisted
	// but not yet claimed for processing.
	WebhookEventReceived WebhookEventStatus = "received"
	// WebhookEventProcessing means the event is being handled by a worker.
	WebhookEventProcessing WebhookEventStatus = "processing"
	// WebhookEventProcessed is a terminal state: the event was handled
	// successfully.
	WebhookEventProcessed WebhookEventStatus = "processed"
	// WebhookEventIgnored is a terminal state: the event type is not
	// handled but was acknowledged.
	WebhookEventIgnored WebhookEventStatus = "ignored"
	// WebhookEventFailed means the processing attempt failed and the event
	// may be retried.
	WebhookEventFailed WebhookEventStatus = "failed"
)

// WebhookEventRepository persists inbound webhook events with idempotency
// anchored on the provider's unique event identifier. The repository is the
// single point where event ID uniqueness is enforced: a replay of the same
// event resolves the existing row instead of creating a second one.
type WebhookEventRepository interface {
	// ClaimEvent inserts the event if it is new (status received) and
	// returns it with Replayed=false. If the event already exists, it
	// resolves the existing row with Replayed=true and does not overwrite
	// it. If the event is in processing by another worker, it returns
	// ErrWebhookEventInProcessing. If the event is terminal (processed,
	// ignored), it returns the existing record with Replayed=true.
	ClaimEvent(ctx context.Context, request ClaimWebhookEventRequest) (*ClaimWebhookEventResult, error)

	// MarkProcessed transitions the event to the processed terminal state.
	MarkProcessed(ctx context.Context, eventID domain.WebhookEventID) error

	// MarkFailed transitions the event to the failed state with a bounded
	// error reason. The event may be retried later.
	MarkFailed(ctx context.Context, eventID domain.WebhookEventID, reason string) error

	// MarkIgnored transitions the event to the ignored terminal state for
	// event types that are acknowledged but not handled.
	MarkIgnored(ctx context.Context, eventID domain.WebhookEventID) error
}

// ClaimWebhookEventRequest is the verified inbound event to persist.
type ClaimWebhookEventRequest struct {
	EventID         domain.WebhookEventID
	EventType       domain.WebhookEventType
	Livemode        bool
	StripeCreatedAt time.Time
	PayloadSHA256   string
	PayloadBytes    int
}

// ClaimWebhookEventResult is the outcome of claiming an event for processing.
type ClaimWebhookEventResult struct {
	Record   WebhookEventRecord
	Replayed bool
}

// WebhookEventStatusIsValid reports whether the status belongs to the closed
// vocabulary.
func WebhookEventStatusIsValid(status WebhookEventStatus) bool {
	switch status {
	case WebhookEventReceived, WebhookEventProcessing, WebhookEventProcessed,
		WebhookEventIgnored, WebhookEventFailed:
		return true
	default:
		return false
	}
}

var (
	// ErrWebhookEventInProcessing indicates the event is currently being
	// processed by another worker. The caller must not retry immediately.
	ErrWebhookEventInProcessing = errors.New("application: webhook event is currently being processed")
)
