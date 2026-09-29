package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.ConsentRepository = (*Repository)(nil)

// RecordConsent stores one verdict per account and version. An identical
// verdict replays untouched; a divergent one is refused, so acceptance
// can never be rewritten into refusal or the reverse.
func (r *Repository) RecordConsent(ctx context.Context, accountID string, charter domain.CharterVersion, decision domain.ConsentDecision) (*application.ConsentView, error) {
	var stored struct {
		decision  string
		decidedAt time.Time
	}
	err := r.pool.QueryRow(ctx,
		`SELECT decision, decided_at FROM app.economy_charter_consents WHERE account_id = $1::uuid AND charter_version = $2`,
		accountID, charter.String()).Scan(&stored.decision, &stored.decidedAt)
	if err == nil {
		if stored.decision != decision.String() {
			return nil, domain.ErrConsentConflict
		}
		return &application.ConsentView{AccountID: accountID, Charter: charter, Decision: decision, DecidedAt: stored.decidedAt}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("lookup consent: %w", err)
	}
	var decidedAt time.Time
	if err := r.pool.QueryRow(ctx,
		`INSERT INTO app.economy_charter_consents (account_id, charter_version, decision) VALUES ($1::uuid, $2, $3) RETURNING decided_at`,
		accountID, charter.String(), decision.String()).Scan(&decidedAt); err != nil {
		return nil, fmt.Errorf("record consent: %w", err)
	}
	return &application.ConsentView{AccountID: accountID, Charter: charter, Decision: decision, DecidedAt: decidedAt}, nil
}

// FindConsent resolves one account's verdict for one version. Unknown
// pairs resolve to absence, never to another holder's row: callers only
// ever address their own account.
func (r *Repository) FindConsent(ctx context.Context, accountID string, charter domain.CharterVersion) (*application.ConsentView, error) {
	var decision string
	var decidedAt time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT decision, decided_at FROM app.economy_charter_consents WHERE account_id = $1::uuid AND charter_version = $2`,
		accountID, charter.String()).Scan(&decision, &decidedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup consent: %w", err)
	}
	parsed, err := domain.ParseConsentDecision(decision)
	if err != nil {
		return nil, fmt.Errorf("stored verdict %q: %w", decision, err)
	}
	return &application.ConsentView{AccountID: accountID, Charter: charter, Decision: parsed, DecidedAt: decidedAt}, nil
}

// RecordOptIn stores one conversion intent after the caller proved an
// acceptance stands: the use case reads it back in the same call path,
// so intents without acceptance never reach storage.
func (r *Repository) RecordOptIn(ctx context.Context, accountID string, charter domain.CharterVersion, quantity domain.MilliInk, rate domain.ConversionRate, validTill time.Time) (*application.OptInView, error) {
	var stored struct {
		id        string
		quantity  int64
		rateNum   int64
		rateDen   int64
		validTill time.Time
	}
	err := r.pool.QueryRow(ctx,
		`SELECT id::text, quantity_milli, rate_num, rate_den, valid_until
		 FROM app.economy_optins WHERE account_id = $1::uuid AND charter_version = $2`,
		accountID, charter.String()).Scan(&stored.id, &stored.quantity, &stored.rateNum, &stored.rateDen, &stored.validTill)
	if err == nil {
		if stored.quantity != quantity.Millis() || stored.rateNum != rate.Num || stored.rateDen != rate.Den {
			return nil, domain.ErrConsentConflict
		}
		storedAmount, err := domain.NewMilliInk(stored.quantity)
		if err != nil {
			return nil, fmt.Errorf("stored intent holds %d milliINK: %w", stored.quantity, err)
		}
		return &application.OptInView{
			OptInID: stored.id, AccountID: accountID, Charter: charter,
			Quantity: storedAmount, Rate: rate, ValidTill: stored.validTill,
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("lookup opt-in: %w", err)
	}
	var view application.OptInView
	err = r.pool.QueryRow(ctx,
		`INSERT INTO app.economy_optins (account_id, charter_version, quantity_milli, rate_num, rate_den, valid_until)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6)
		 RETURNING id::text, valid_until`,
		accountID, charter.String(), quantity.Millis(), rate.Num, rate.Den, validTill).Scan(&view.OptInID, &view.ValidTill)
	if err != nil {
		return nil, fmt.Errorf("record opt-in: %w", err)
	}
	view.AccountID = accountID
	view.Charter = charter
	view.Quantity = quantity
	view.Rate = rate
	return &view, nil
}
