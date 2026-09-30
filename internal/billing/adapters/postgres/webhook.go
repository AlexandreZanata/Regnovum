package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Webhook surface of the billing adapter (P12-T05): event idempotency and
// processing lifecycle.
//
// The INSERT ON CONFLICT DO NOTHING on the unique provider event ID is the
// idempotency anchor: a replay resolves the existing row and never creates a
// second one. The UPDATE statements that transition status are guarded by the
// CHECK constraint of migration 00020 (stripe_events_status_transition), so
// an illegal transition is a database error, not a silent misbehavior.
var _ application.WebhookEventRepository = (*Repository)(nil)

// NewRepositoryWithClock builds the repository with an injected clock for
// webhook event processing.
func NewRepositoryWithClock(pool *pgxpool.Pool, clock ports.Clock) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
		clock:   clock,
	}
}

// ClaimEvent inserts the event if it is new (status received) and returns it
// with Replayed=false. If the event already exists, it resolves the existing
// row with Replayed=true and does not overwrite it.
func (r *Repository) ClaimEvent(ctx context.Context, request application.ClaimWebhookEventRequest) (*application.ClaimWebhookEventResult, error) {
	// Attempt to insert the event. ON CONFLICT on the unique provider
	// event ID means a replay returns 0 rows inserted.
	inserted, err := r.queries.InsertWebhookEventIfAbsent(ctx, platformpg.InsertWebhookEventIfAbsentParams{
		StripeEventID:   request.EventID.String(),
		EventType:       request.EventType.String(),
		Livemode:        request.Livemode,
		StripeCreatedAt: pgtype.Timestamptz{Time: request.StripeCreatedAt, Valid: true},
		PayloadSha256:   request.PayloadSHA256,
		PayloadBytes:    int32(request.PayloadBytes),
	})
	if err == nil {
		// New event: transition from received → processing.
		updated, err := r.queries.UpdateWebhookEventStatus(ctx, platformpg.UpdateWebhookEventStatusParams{
			StripeEventID: request.EventID.String(),
			Status:        string(application.WebhookEventProcessing),
		})
		if err != nil {
			return nil, fmt.Errorf("claim webhook event (processing): %w", err)
		}
		record, err := mapWebhookEvent(updated)
		if err != nil {
			return nil, err
		}
		_ = inserted
		return &application.ClaimWebhookEventResult{Record: *record, Replayed: false}, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("claim webhook event (insert): %w", err)
	}

	// The event already exists: resolve the existing row.
	existing, err := r.queries.GetWebhookEventByEventID(ctx, request.EventID.String())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("webhook event conflict without a stored event")
		}
		return nil, fmt.Errorf("claim webhook event (resolve): %w", err)
	}

	record, err := mapWebhookEvent(existing)
	if err != nil {
		return nil, err
	}

	// If the event is in received state, transition it to processing.
	if record.Status == application.WebhookEventReceived {
		updated, err := r.queries.UpdateWebhookEventStatus(ctx, platformpg.UpdateWebhookEventStatusParams{
			StripeEventID: request.EventID.String(),
			Status:        string(application.WebhookEventProcessing),
		})
		if err != nil {
			return nil, fmt.Errorf("claim webhook event (processing existing): %w", err)
		}
		record, err = mapWebhookEvent(updated)
		if err != nil {
			return nil, err
		}
	}

	return &application.ClaimWebhookEventResult{Record: *record, Replayed: true}, nil
}

// MarkProcessed transitions the event to the processed terminal state.
func (r *Repository) MarkProcessed(ctx context.Context, eventID domain.WebhookEventID) error {
	_, err := r.queries.UpdateWebhookEventStatusProcessed(ctx, platformpg.UpdateWebhookEventStatusProcessedParams{
		StripeEventID: eventID.String(),
		Status:        string(application.WebhookEventProcessed),
		ProcessedAt:   pgtype.Timestamptz{Time: r.clock.Now().UTC(), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("mark webhook event processed: %w", err)
	}
	return nil
}

// MarkFailed transitions the event to the failed state with a bounded error
// reason.
func (r *Repository) MarkFailed(ctx context.Context, eventID domain.WebhookEventID, reason string) error {
	// Truncate the reason to the schema bound (500 chars).
	if len(reason) > 500 {
		reason = reason[:500]
	}
	_, err := r.queries.UpdateWebhookEventStatusFailed(ctx, platformpg.UpdateWebhookEventStatusFailedParams{
		StripeEventID: eventID.String(),
		Status:        string(application.WebhookEventFailed),
		LastError:     pgtype.Text{String: reason, Valid: reason != ""},
	})
	if err != nil {
		return fmt.Errorf("mark webhook event failed: %w", err)
	}
	return nil
}

// MarkIgnored transitions the event to the ignored terminal state for event
// types that are acknowledged but not handled.
func (r *Repository) MarkIgnored(ctx context.Context, eventID domain.WebhookEventID) error {
	_, err := r.queries.UpdateWebhookEventStatusIgnored(ctx, platformpg.UpdateWebhookEventStatusIgnoredParams{
		StripeEventID: eventID.String(),
		Status:        string(application.WebhookEventIgnored),
		ProcessedAt:   pgtype.Timestamptz{Time: r.clock.Now().UTC(), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("mark webhook event ignored: %w", err)
	}
	return nil
}

// mapWebhookEvent reconstitutes a stored event, validating every value
// against the domain vocabulary.
func mapWebhookEvent(row platformpg.AppStripeEvent) (*application.WebhookEventRecord, error) {
	eventID, err := domain.ParseWebhookEventID(row.StripeEventID)
	if err != nil {
		return nil, fmt.Errorf("stored webhook event id is invalid: %w", err)
	}
	eventType, err := domain.ParseWebhookEventType(row.EventType)
	if err != nil {
		return nil, fmt.Errorf("stored webhook event type is invalid: %w", err)
	}
	status := application.WebhookEventStatus(row.Status)
	if !application.WebhookEventStatusIsValid(status) {
		return nil, fmt.Errorf("stored webhook event status is invalid: %s", row.Status)
	}

	return &application.WebhookEventRecord{
		ID:              uuidToString(row.ID),
		EventID:         eventID,
		EventType:       eventType,
		Livemode:        row.Livemode,
		StripeCreatedAt: row.StripeCreatedAt.Time.UTC(),
		PayloadSHA256:   row.PayloadSha256,
		PayloadBytes:    int(row.PayloadBytes),
		Status:          status,
		Attempts:        int(row.Attempts),
		LastError:       row.LastError.String,
		ReceivedAt:      row.ReceivedAt.Time.UTC(),
		ProcessedAt:     nullableTimeToPtr(row.ProcessedAt),
	}, nil
}

// nullableTimeToPtr converts a nullable timestamp to a *time.Time.
func nullableTimeToPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}
