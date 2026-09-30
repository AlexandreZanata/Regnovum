package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

var _ application.ReconciliationStore = (*Repository)(nil)

// ListIntents returns the checkout intents created in the half-open window
// for the mode, oldest first.
func (r *Repository) ListIntents(ctx context.Context, start, end time.Time, livemode bool) ([]application.CheckoutIntentRecord, error) {
	rows, err := r.queries.ListCheckoutIntentsForReconciliation(ctx, platformpg.ListCheckoutIntentsForReconciliationParams{
		CreatedAt:   pgtype.Timestamptz{Time: start.UTC(), Valid: true},
		CreatedAt_2: pgtype.Timestamptz{Time: end.UTC(), Valid: true},
		Livemode:    livemode,
	})
	if err != nil {
		return nil, fmt.Errorf("list intents for reconciliation: %w", err)
	}
	records := make([]application.CheckoutIntentRecord, 0, len(rows))
	for _, row := range rows {
		record, err := mapCheckoutIntent(intentFields{
			id:             row.ID,
			accountID:      row.AccountID,
			market:         row.Market,
			productID:      row.ProductID,
			catalogVersion: row.CatalogVersion,
			currency:       row.Currency,
			amountMinor:    row.AmountMinor,
			livemode:       row.Livemode,
			status:         row.Status,
			sessionID:      row.StripeCheckoutSessionID,
			createdAt:      row.CreatedAt,
		})
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, nil
}

// ListSubscriptions returns the subscription mirrors updated in the
// half-open window for the mode, oldest first.
func (r *Repository) ListSubscriptions(ctx context.Context, start, end time.Time, livemode bool) ([]application.SubscriptionRecord, error) {
	rows, err := r.queries.ListSubscriptionsForReconciliation(ctx, platformpg.ListSubscriptionsForReconciliationParams{
		UpdatedAt:   pgtype.Timestamptz{Time: start.UTC(), Valid: true},
		UpdatedAt_2: pgtype.Timestamptz{Time: end.UTC(), Valid: true},
		Livemode:    livemode,
	})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions for reconciliation: %w", err)
	}
	records := make([]application.SubscriptionRecord, 0, len(rows))
	for _, row := range rows {
		record, err := mapSubscription(row)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, nil
}

// ListUnprocessedEvents returns the verified events created in the window
// that never reached a terminal outcome.
func (r *Repository) ListUnprocessedEvents(ctx context.Context, start, end time.Time, livemode bool) ([]application.WebhookEventRecord, error) {
	rows, err := r.queries.ListUnprocessedStripeEvents(ctx, platformpg.ListUnprocessedStripeEventsParams{
		StripeCreatedAt:   pgtype.Timestamptz{Time: start.UTC(), Valid: true},
		StripeCreatedAt_2: pgtype.Timestamptz{Time: end.UTC(), Valid: true},
		Livemode:          livemode,
	})
	if err != nil {
		return nil, fmt.Errorf("list unprocessed events: %w", err)
	}
	records := make([]application.WebhookEventRecord, 0, len(rows))
	for _, row := range rows {
		record, err := mapWebhookEvent(row)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, nil
}

// CreateRun opens one reconciliation window run.
func (r *Repository) CreateRun(ctx context.Context, livemode bool, start, end time.Time) (string, error) {
	row, err := r.queries.CreateReconciliationRun(ctx, platformpg.CreateReconciliationRunParams{
		Livemode:    livemode,
		WindowStart: pgtype.Timestamptz{Time: start.UTC(), Valid: true},
		WindowEnd:   pgtype.Timestamptz{Time: end.UTC(), Valid: true},
	})
	if err != nil {
		return "", fmt.Errorf("create reconciliation run: %w", err)
	}
	return uuidToString(row.ID), nil
}

// RecordFinding appends one immutable divergence to the run.
func (r *Repository) RecordFinding(ctx context.Context, request application.RecordFindingRequest) error {
	runID, err := uuidFromString(request.RunID)
	if err != nil {
		return fmt.Errorf("record finding: %w", err)
	}
	if !request.Kind.IsValid() {
		return fmt.Errorf("record finding: %w", domain.ErrInvalidReconciliationKind)
	}
	if request.Reference == "" || len(request.Reference) > 200 {
		return fmt.Errorf("record finding: invalid reference: %w", domain.ErrInvalidReference)
	}
	var accountID pgtype.UUID
	if request.AccountID != nil {
		accountID, err = pgUUIDFromAccountID(*request.AccountID)
		if err != nil {
			return fmt.Errorf("record finding: %w", err)
		}
	}
	var details pgtype.Text
	if request.Details != "" {
		details = pgtype.Text{String: request.Details, Valid: true}
	}
	_, err = r.queries.InsertReconciliationFinding(ctx, platformpg.InsertReconciliationFindingParams{
		RunID:     runID,
		AccountID: accountID,
		Kind:      request.Kind.String(),
		Reference: request.Reference,
		Details:   details,
	})
	if err != nil {
		return fmt.Errorf("record finding: %w", err)
	}
	return nil
}

// FinishRun closes the run with its counters. A failed run keeps its
// findings: the failure only says the window was not fully inspected.
func (r *Repository) FinishRun(ctx context.Context, runID string, scanned, findings int, failed bool) error {
	id, err := uuidFromString(runID)
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	status := "completed"
	if failed {
		status = "failed"
	}
	_, err = r.queries.FinishReconciliationRun(ctx, platformpg.FinishReconciliationRunParams{
		ID:             id,
		Status:         status,
		ScannedObjects: int32(scanned),
		FindingsCount:  int32(findings),
	})
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	return nil
}
