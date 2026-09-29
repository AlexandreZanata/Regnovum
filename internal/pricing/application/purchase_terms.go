package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// PurchaseTermsCommand quotes one purchase before acceptance: the fiat
// ticket, the judged sightings with the median guards, the fee
// schedule and the display locale. Nothing is stored: the sealed
// document is what a later acceptance persists verbatim.
type PurchaseTermsCommand struct {
	FiatMinor  int64
	Sightings  []SightingInput
	MinSources int
	MaxSpread  int64
	Schedule   domain.FeeSchedule
	Locale     string
}

// TermsDocument is the quoted purchase in one locale: the dictionary
// titles with the canonical sealed terms. Locales translate titles
// only: the amounts never move with them.
type TermsDocument struct {
	Locale string
	Titles domain.TermsTitles
	Terms  domain.Terms
}

// QuotePurchaseUseCase quotes one purchase from the guarded median.
// It is an internal operation: no public surface calls it.
type QuotePurchaseUseCase struct{}

// NewQuotePurchaseUseCase creates an instance of QuotePurchaseUseCase.
func NewQuotePurchaseUseCase() *QuotePurchaseUseCase {
	return &QuotePurchaseUseCase{}
}

// Execute guards the median, derives the terms and renders the
// document. Unknown locales refuse before any arithmetic; anomalous
// rounds suspend the quotation instead of pricing through doubt.
func (uc *QuotePurchaseUseCase) Execute(_ context.Context, cmd PurchaseTermsCommand) (TermsDocument, error) {
	locale, err := domain.ParseTermsLocale(cmd.Locale)
	if err != nil {
		return TermsDocument{}, err
	}
	sightings, err := observationsFromInputs(cmd.Sightings)
	if err != nil {
		return TermsDocument{}, err
	}
	median, err := domain.MedianPrice(sightings, domain.MedianPolicy{
		MinSources: cmd.MinSources,
		MaxSpread:  cmd.MaxSpread,
	})
	if err != nil {
		return TermsDocument{}, err
	}
	terms, err := domain.QuoteTerms(cmd.FiatMinor, median, cmd.Schedule)
	if err != nil {
		return TermsDocument{}, err
	}
	return TermsDocument{
		Locale: locale.String(),
		Titles: domain.TermsTitlesFor(locale),
		Terms:  terms,
	}, nil
}
