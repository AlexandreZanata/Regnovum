package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

var _ application.SubscriptionRepository = (*Repository)(nil)

// GetSubscriptionByStripeID resolves a subscription mirror by its provider
// identifier. It returns nil, nil when no subscription exists yet.
func (r *Repository) GetSubscriptionByStripeID(ctx context.Context, subID domain.StripeSubscriptionID) (*application.SubscriptionRecord, error) {
	row, err := r.queries.GetSubscriptionByStripeID(ctx, subID.String())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load subscription: %w", err)
	}

	return mapSubscription(row)
}

// UpsertSubscription inserts or updates the subscription mirror in the database.
func (r *Repository) UpsertSubscription(ctx context.Context, request application.UpsertSubscriptionRequest) (*application.SubscriptionRecord, error) {
	pgUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, fmt.Errorf("upsert subscription: %w", err)
	}

	params := platformpg.UpsertSubscriptionParams{
		AccountID:            pgUUID,
		StripeSubscriptionID: request.StripeSubscriptionID.String(),
		Status:               request.Status.String(),
		Livemode:             request.Livemode,
		Market:               request.Market.String(),
		ProductID:            request.ProductID.String(),
		CatalogVersion:       int32(request.CatalogVersion),
		StripePriceID:        request.StripePriceID.String(),
		CurrentPeriodStart:   timestamptzFromTimePtr(request.CurrentPeriodStart),
		CurrentPeriodEnd:     timestamptzFromTimePtr(request.CurrentPeriodEnd),
		CancelAtPeriodEnd:    request.CancelAtPeriodEnd,
		CanceledAt:           timestamptzFromTimePtr(request.CanceledAt),
	}

	row, err := r.queries.UpsertSubscription(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("upsert subscription: %w", err)
	}

	return mapSubscription(row)
}

// GetActiveSubscriptionByAccount returns the most recent active or trialing
// subscription for an account, or nil if none exists.
func (r *Repository) GetActiveSubscriptionByAccount(ctx context.Context, accountID domain.AccountID) (*application.SubscriptionRecord, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("load active subscription: %w", err)
	}

	row, err := r.queries.GetActiveSubscriptionByAccount(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load active subscription: %w", err)
	}

	return mapSubscription(row)
}

func mapSubscription(row platformpg.AppSubscription) (*application.SubscriptionRecord, error) {
	accountID := domain.AccountID(uuidToString(row.AccountID))

	subID, err := domain.ParseStripeSubscriptionID(row.StripeSubscriptionID)
	if err != nil {
		return nil, fmt.Errorf("stored subscription id is invalid: %w", err)
	}

	status, err := domain.ParseSubscriptionStatus(row.Status)
	if err != nil {
		return nil, fmt.Errorf("stored subscription status is invalid: %w", err)
	}

	market, err := domain.ParseMarket(row.Market)
	if err != nil {
		return nil, fmt.Errorf("stored subscription market is invalid: %w", err)
	}

	productID, err := domain.ParseProductID(row.ProductID)
	if err != nil {
		return nil, fmt.Errorf("stored subscription product is invalid: %w", err)
	}

	priceID, err := domain.ParseStripePriceID(row.StripePriceID)
	if err != nil {
		return nil, fmt.Errorf("stored subscription price is invalid: %w", err)
	}

	var periodStart, periodEnd, canceledAt *time.Time
	if row.CurrentPeriodStart.Valid {
		t := row.CurrentPeriodStart.Time.UTC()
		periodStart = &t
	}
	if row.CurrentPeriodEnd.Valid {
		t := row.CurrentPeriodEnd.Time.UTC()
		periodEnd = &t
	}
	if row.CanceledAt.Valid {
		t := row.CanceledAt.Time.UTC()
		canceledAt = &t
	}

	return &application.SubscriptionRecord{
		ID:                   uuidToString(row.ID),
		AccountID:            accountID,
		StripeSubscriptionID: subID,
		Status:               status,
		Livemode:             row.Livemode,
		Market:               market,
		ProductID:            productID,
		CatalogVersion:       int(row.CatalogVersion),
		StripePriceID:        priceID,
		CurrentPeriodStart:   periodStart,
		CurrentPeriodEnd:     periodEnd,
		CancelAtPeriodEnd:    row.CancelAtPeriodEnd,
		CanceledAt:           canceledAt,
		CreatedAt:            row.CreatedAt.Time.UTC(),
		UpdatedAt:            row.UpdatedAt.Time.UTC(),
	}, nil
}

func timestamptzFromTimePtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}
