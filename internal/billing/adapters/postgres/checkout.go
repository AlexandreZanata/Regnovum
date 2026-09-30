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

// Checkout surface of the billing adapter (P12-T04): the purchase eligibility
// bit, the account→provider customer correlation and the checkout intents.
//
// Every write is idempotent by construction. The customer mapping is inserted
// once per account and the intent once per provider session, so a retried or
// concurrent request resolves the stored row instead of writing a second one;
// both unique constraints live in the schema (migration 00020) and no query
// here ever updates or deletes them.
var (
	_ application.PurchaserDirectory       = (*Repository)(nil)
	_ application.StripeCustomerRepository = (*Repository)(nil)
	_ application.CheckoutIntentRepository = (*Repository)(nil)
)

// PurchaserForCheckout returns the single eligibility bit the checkout needs:
// an active account with a verified email. It never reads the email, the
// credentials or any payment identifier, and a missing account is reported as
// such instead of being treated as ineligible.
func (r *Repository) PurchaserForCheckout(ctx context.Context, accountID domain.AccountID) (application.Purchaser, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return application.Purchaser{}, fmt.Errorf("load purchaser: %w", err)
	}

	eligible, err := r.queries.IsAccountEligibleForPurchase(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.Purchaser{}, application.ErrPurchaserNotFound
		}
		return application.Purchaser{}, fmt.Errorf("load purchaser: %w", err)
	}
	if !eligible.Valid {
		return application.Purchaser{}, errors.New("stored account eligibility is unreadable")
	}

	return application.Purchaser{AccountID: accountID, Eligible: eligible.Bool}, nil
}

// StripeCustomer returns the stored provider customer of the account, nil when
// the account has none yet.
func (r *Repository) StripeCustomer(ctx context.Context, accountID domain.AccountID) (*application.StripeCustomerRecord, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("load stripe customer: %w", err)
	}

	row, err := r.queries.GetStripeCustomer(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load stripe customer: %w", err)
	}

	record, err := mapStripeCustomer(row.AccountID, row.StripeCustomerID, row.Livemode, row.CreatedAt.Time)
	if err != nil {
		return nil, err
	}
	return record, nil
}

// RecordStripeCustomer stores the account→provider customer correlation once.
// A concurrent or retried insertion writes nothing and the stored mapping is
// the one returned, so a retry can never leave the account correlated with two
// different provider customers.
func (r *Repository) RecordStripeCustomer(ctx context.Context, request application.RecordStripeCustomerRequest) (*application.StripeCustomerRecord, error) {
	pgUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, fmt.Errorf("record stripe customer: %w", err)
	}

	row, err := r.queries.RecordStripeCustomerIfAbsent(ctx, platformpg.RecordStripeCustomerIfAbsentParams{
		AccountID:        pgUUID,
		StripeCustomerID: request.CustomerID.String(),
		Livemode:         request.Livemode,
	})
	if err == nil {
		record, mapErr := mapStripeCustomer(row.AccountID, row.StripeCustomerID, row.Livemode, row.CreatedAt.Time)
		if mapErr != nil {
			return nil, mapErr
		}
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("record stripe customer: %w", err)
	}

	stored, err := r.queries.GetStripeCustomer(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("stripe customer conflict without a stored mapping")
		}
		return nil, fmt.Errorf("load replayed stripe customer: %w", err)
	}
	record, err := mapStripeCustomer(stored.AccountID, stored.StripeCustomerID, stored.Livemode, stored.CreatedAt.Time)
	if err != nil {
		return nil, err
	}
	return record, nil
}

// AccountIDByStripeCustomer resolves the account ID from the stored
// provider customer mapping. It returns ErrPurchaserNotFound when no
// mapping carries the customer identifier.
func (r *Repository) AccountIDByStripeCustomer(ctx context.Context, customerID domain.StripeCustomerID) (domain.AccountID, error) {
	row, err := r.queries.GetAccountByStripeCustomerID(ctx, customerID.String())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", application.ErrPurchaserNotFound
		}
		return "", fmt.Errorf("load account by stripe customer: %w", err)
	}

	return domain.AccountID(uuidToString(row.AccountID)), nil
}

// RecordCheckoutIntent stores the server-resolved commercial decision exactly
// once per provider session. A replay resolves the stored intent, and a session
// already recorded for a different account is refused instead of disclosed.
func (r *Repository) RecordCheckoutIntent(ctx context.Context, request application.RecordCheckoutIntentRequest) (*application.RecordCheckoutIntentResult, error) {
	if request.Status != domain.CheckoutIntentOpen && request.Status != domain.CheckoutIntentExpired {
		return nil, fmt.Errorf("record checkout intent: %w", domain.ErrInvalidCheckoutIntentStatus)
	}
	if (request.Status == domain.CheckoutIntentExpired) != (request.ClosedAt != nil) {
		return nil, errors.New("record checkout intent: an expired intent is closed and an open one is not")
	}

	pgUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, fmt.Errorf("record checkout intent: %w", err)
	}

	row, err := r.queries.RecordCheckoutIntentIfAbsent(ctx, platformpg.RecordCheckoutIntentIfAbsentParams{
		AccountID:               pgUUID,
		Market:                  request.Market.String(),
		ProductID:               request.ProductID.String(),
		CatalogVersion:          int32(request.CatalogVersion),
		Currency:                request.Amount.Currency().String(),
		AmountMinor:             request.Amount.MinorUnits(),
		Livemode:                request.Livemode,
		Status:                  request.Status.String(),
		StripeCheckoutSessionID: pgText(request.SessionID.String()),
		ClosedAt:                timestamptz(request.ClosedAt),
	})
	if err == nil {
		record, mapErr := mapCheckoutIntent(intentFields{
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
		if mapErr != nil {
			return nil, mapErr
		}
		return &application.RecordCheckoutIntentResult{Intent: *record}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("record checkout intent: %w", err)
	}

	existing, err := r.queries.GetCheckoutIntentBySession(ctx, pgText(request.SessionID.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("checkout intent conflict without a stored intent")
		}
		return nil, fmt.Errorf("load replayed checkout intent: %w", err)
	}
	record, err := mapCheckoutIntent(intentFields{
		id:             existing.ID,
		accountID:      existing.AccountID,
		market:         existing.Market,
		productID:      existing.ProductID,
		catalogVersion: existing.CatalogVersion,
		currency:       existing.Currency,
		amountMinor:    existing.AmountMinor,
		livemode:       existing.Livemode,
		status:         existing.Status,
		sessionID:      existing.StripeCheckoutSessionID,
		createdAt:      existing.CreatedAt,
	})
	if err != nil {
		return nil, err
	}
	if record.AccountID != request.AccountID {
		return nil, errors.New("checkout session is already recorded for another account")
	}
	return &application.RecordCheckoutIntentResult{Intent: *record, Replayed: true}, nil
}

// intentFields is the column set both intent queries return, so the mapping
// rules live in exactly one place.
type intentFields struct {
	id             pgtype.UUID
	accountID      pgtype.UUID
	market         string
	productID      string
	catalogVersion int32
	currency       string
	amountMinor    int64
	livemode       bool
	status         string
	sessionID      pgtype.Text
	createdAt      pgtype.Timestamptz
}

// mapCheckoutIntent reconstitutes a stored intent, validating every value
// against the domain vocabulary: a row the domain cannot express is an
// integrity problem and is refused instead of being handed to a caller.
func mapCheckoutIntent(fields intentFields) (*application.CheckoutIntentRecord, error) {
	market, err := domain.ParseMarket(fields.market)
	if err != nil {
		return nil, fmt.Errorf("stored checkout intent market is invalid: %w", err)
	}
	productID, err := domain.ParseProductID(fields.productID)
	if err != nil {
		return nil, fmt.Errorf("stored checkout intent product is invalid: %w", err)
	}
	currency, err := domain.ParseCurrency(fields.currency)
	if err != nil {
		return nil, fmt.Errorf("stored checkout intent currency is invalid: %w", err)
	}
	amount, err := domain.NewMoney(fields.amountMinor, currency)
	if err != nil {
		return nil, fmt.Errorf("stored checkout intent amount is invalid: %w", err)
	}
	status, err := domain.ParseCheckoutIntentStatus(fields.status)
	if err != nil {
		return nil, fmt.Errorf("stored checkout intent status is invalid: %w", err)
	}
	sessionID, err := domain.ParseStripeCheckoutSessionID(fields.sessionID.String, fields.livemode)
	if err != nil {
		return nil, fmt.Errorf("stored checkout intent session is invalid: %w", err)
	}

	return &application.CheckoutIntentRecord{
		ID:             uuidToString(fields.id),
		AccountID:      domain.AccountID(uuidToString(fields.accountID)),
		Market:         market,
		ProductID:      productID,
		CatalogVersion: int(fields.catalogVersion),
		Amount:         amount,
		Livemode:       fields.livemode,
		Status:         status,
		SessionID:      sessionID,
		CreatedAt:      fields.createdAt.Time.UTC(),
	}, nil
}

// mapStripeCustomer reconstitutes a stored customer mapping, validating the
// private provider identifier shape before it is used again.
func mapStripeCustomer(accountID pgtype.UUID, customerID string, livemode bool, createdAt time.Time) (*application.StripeCustomerRecord, error) {
	parsed, err := domain.ParseStripeCustomerID(customerID)
	if err != nil {
		return nil, fmt.Errorf("stored stripe customer identifier is invalid: %w", err)
	}
	return &application.StripeCustomerRecord{
		AccountID:  domain.AccountID(uuidToString(accountID)),
		CustomerID: parsed,
		Livemode:   livemode,
		CreatedAt:  createdAt.UTC(),
	}, nil
}

// pgText maps a Go string onto a nullable text parameter.
func pgText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

// GetCheckoutIntentBySession resolves the intent a provider session stands
// for. It returns ErrCheckoutIntentNotFound when no intent carries the
// session identifier.
func (r *Repository) GetCheckoutIntentBySession(ctx context.Context, sessionID domain.StripeCheckoutSessionID) (*application.CheckoutIntentRecord, error) {
	row, err := r.queries.GetCheckoutIntentBySession(ctx, pgText(sessionID.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: session %s", application.ErrCheckoutIntentNotFound, sessionID)
		}
		return nil, fmt.Errorf("get checkout intent by session: %w", err)
	}
	return mapCheckoutIntent(intentFields{
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
}

// MarkCheckoutIntentPaid transitions the intent to the paid terminal state
// and records the settlement instant. It is a no-op when the intent is
// already paid (a replay of the same webhook event).
func (r *Repository) MarkCheckoutIntentPaid(ctx context.Context, sessionID domain.StripeCheckoutSessionID) error {
	err := r.queries.MarkCheckoutIntentPaid(ctx, pgText(sessionID.String()))
	if err != nil {
		return fmt.Errorf("mark checkout intent paid: %w", err)
	}
	return nil
}
