package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// ConvertCommand converts one recorded opt-in: account, charter version
// and the rate the caller honors, which must equal the recorded terms
// exactly. The quantity converted is the recorded one, never a new
// amount smuggled in the call.
type ConvertCommand struct {
	AccountID string
	Charter   string
	RateNum   int64
	RateDen   int64
}

// ConversionResult is the settled conversion: the Genesis transfer that
// paid the holder and the converted amount, with replay marking the
// retries that resolved the original settlement.
type ConversionResult struct {
	TransferID string
	Converted  domain.MilliInk
	Replayed   bool
}

// OptInTermsView is the recorded intent the conversion matches against.
type OptInTermsView struct {
	OptInID   string
	Quantity  domain.MilliInk
	Rate      domain.ConversionRate
	ValidTill time.Time
}

// ConversionRepository settles opted-in conversions across both books in
// one transaction: the legacy debit extinguishes the right while the
// Treasury credit pays it, or nothing moves at all.
type ConversionRepository interface {
	// FindTerms resolves the recorded intent for one account and
	// charter, or reports its absence. Callers only ever see their own
	// account.
	FindTerms(ctx context.Context, accountID string, charter domain.CharterVersion) (*OptInTermsView, error)
	// Convert settles the resolved terms: legacy units out, converted
	// milliINK from Treasury stock in, keyed idempotently by the
	// opt-in. Replays resolve the original settlement untouched.
	Convert(ctx context.Context, request ConversionRequest) (*ConversionResult, error)
}

// ConversionRequest carries the caller-honored rate for agreement
// checking: the adapter loads the recorded terms transactionally and
// matches them exactly before moving anything.
type ConversionRequest struct {
	AccountID string
	Charter   domain.CharterVersion
	Rate      domain.ConversionRate
}

// ConvertUseCase settles one recorded opt-in without creating value. It
// is an internal operation: no public surface calls it.
type ConvertUseCase struct {
	consents    ConsentRepository
	conversions ConversionRepository
	clock       Clock
}

// NewConvertUseCase creates an instance of ConvertUseCase.
func NewConvertUseCase(consents ConsentRepository, conversions ConversionRepository, clock Clock) *ConvertUseCase {
	return &ConvertUseCase{consents: consents, conversions: conversions, clock: clock}
}

// Execute validates the call against the recorded intent and settles it.
func (uc *ConvertUseCase) Execute(ctx context.Context, cmd ConvertCommand) (*ConversionResult, error) {
	if cmd.AccountID == "" {
		return nil, domain.ErrOptInMissing
	}
	charter, err := domain.ParseCharterVersion(cmd.Charter)
	if err != nil {
		return nil, err
	}
	rate, err := domain.ParseConversionRate(cmd.RateNum, cmd.RateDen)
	if err != nil {
		return nil, err
	}
	consent, err := uc.consents.FindConsent(ctx, cmd.AccountID, charter)
	if err != nil {
		return nil, err
	}
	if consent == nil || consent.Decision != domain.ConsentAccepted {
		return nil, domain.ErrConsentRequired
	}
	terms, err := uc.conversions.FindTerms(ctx, cmd.AccountID, charter)
	if err != nil {
		return nil, err
	}
	if terms == nil {
		return nil, domain.ErrOptInMissing
	}
	if !terms.Rate.Equals(rate) {
		return nil, domain.ErrRateMismatch
	}
	if !terms.ValidTill.After(uc.clock.Now()) {
		return nil, domain.ErrOptInExpired
	}
	units, err := domain.LegacyUnitsOf(terms.Quantity)
	if err != nil {
		return nil, err
	}
	if _, err := domain.ConvertedMillis(units, terms.Rate); err != nil {
		return nil, err
	}
	return uc.conversions.Convert(ctx, ConversionRequest{
		AccountID: cmd.AccountID,
		Charter:   charter,
		Rate:      rate,
	})
}
