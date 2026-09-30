package application

import (
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/metering/domain"
)

// PreviewCommand asks the price of publishing a candidate text: the
// account, the raw candidate, the service and the judgment instant.
// Nothing is stored and nothing is charged: drafts, keystrokes and
// previews never enter the ledger. Season names the publication
// book (empty binds compat-legacy for the legacy path); seasonal
// books need an explicit reset acknowledgement shown before
// acceptance and an exclusive book end capping the preview.
type PreviewCommand struct {
	Account           string
	Content           string
	Service           string
	Now               time.Time
	MaxUnits          int
	TTL               time.Duration
	Season            string
	SeasonEndsAt      time.Time
	ResetAcknowledged bool
}

// PreviewResult is the priced preview: the measured content, the
// covering price entry and the sealed quote beside the plain
// amounts a confirmation needs. A confirmation reuses these exact
// values instead of trusting caller text: edited bytes price anew.
type PreviewResult struct {
	Content     domain.MeasuredContent
	Price       domain.PriceEntry
	Quote       domain.PublicationQuote
	Units       int
	PriceMilli  int64
	TotalMilli  int64
	Version     int
	ContentHash string
	QuoteHash   string
	AcceptedAt  time.Time
	ExpiresAt   time.Time
}

// PreviewUseCase prices one publication candidate without storing
// or charging anything. It is an internal operation: no public
// surface calls it before activation.
type PreviewUseCase struct {
	prices  domain.Catalog
	counter domain.GraphemeCounter
}

// NewPreviewUseCase creates an instance of PreviewUseCase, refusing
// incomplete composition.
func NewPreviewUseCase(prices domain.Catalog, counter domain.GraphemeCounter) (*PreviewUseCase, error) {
	if counter == nil {
		return nil, ErrInvalidPublishConfig
	}
	return &PreviewUseCase{prices: prices, counter: counter}, nil
}

// Execute measures the candidate, prices it against the approved
// table at the judgment instant and seals the preview window. The
// locale never enters: amounts travel as canonical integers and the
// presentation layer translates around them. A stale preview from
// another book never prices this settlement: the book travels
// beside the seal and is matched before any intention opens.
func (uc *PreviewUseCase) Execute(cmd PreviewCommand) (*PreviewResult, error) {
	if strings.TrimSpace(cmd.Account) == "" || strings.TrimSpace(cmd.Account) != cmd.Account {
		return nil, domain.ErrInvalidQuote
	}
	service, err := domain.ParseServiceID(cmd.Service)
	if err != nil {
		return nil, err
	}
	if cmd.Now.IsZero() {
		return nil, domain.ErrInvalidQuote
	}
	if cmd.MaxUnits <= 0 || cmd.TTL <= 0 {
		return nil, domain.ErrInvalidQuote
	}
	content, err := domain.ParseMeasuredContent(cmd.Content, uc.counter, cmd.MaxUnits)
	if err != nil {
		return nil, err
	}
	price, err := uc.prices.PriceAt(service, cmd.Now.UTC())
	if err != nil {
		return nil, err
	}
	season := domain.SeasonKey(domain.CompatSeasonKey)
	if cmd.Season != "" {
		season, err = domain.ParseSeasonKey(cmd.Season)
		if err != nil {
			return nil, err
		}
	}
	if err := domain.RequireSeasonalReset(season, cmd.ResetAcknowledged); err != nil {
		return nil, err
	}
	quote, err := domain.AcceptSeasonalPublicationQuote(domain.SeasonalQuoteRequest{
		Account: cmd.Account, Content: content, Price: price,
		AcceptedAt: cmd.Now.UTC(), TTL: cmd.TTL,
		Season: season, SeasonEndsAt: cmd.SeasonEndsAt.UTC(),
		ResetAcknowledged: cmd.ResetAcknowledged,
	})
	if err != nil {
		return nil, err
	}
	return &PreviewResult{
		Content: content, Price: price, Quote: quote,
		Units: quote.Units, PriceMilli: quote.PriceMilli, TotalMilli: quote.TotalMilli,
		Version: quote.Version, ContentHash: quote.ContentHash.String(), QuoteHash: quote.Hash,
		AcceptedAt: quote.AcceptedAt, ExpiresAt: quote.ExpiresAt,
	}, nil
}
