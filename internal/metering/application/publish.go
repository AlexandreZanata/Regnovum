package application

import (
	"context"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/metering/domain"
)

// PublishCommand names one publication with charge: the caller
// operation token, the publishing account, the measured final
// content, the price entry covering acceptance, the sealed quote and
// the ledger endpoints. Cost, version and hash resolve from the
// sealed quote, never from caller text: edited bytes or another
// account need a new acceptance. Season names the publication book
// (empty binds compat-legacy for the legacy path); seasonal books
// need an explicit reset acknowledgement shown before acceptance.
type PublishCommand struct {
	Key               string
	Account           string
	Content           domain.MeasuredContent
	Price             domain.PriceEntry
	Quote             domain.PublicationQuote
	FromKind          string
	FromLabel         string
	ToKind            string
	ToLabel           string
	Now               time.Time
	Season            string
	ResetAcknowledged bool
}

// PublishRequest is the validated publication for the repository
// port: the token, the bound account, the sealed terms, the opaque
// ledger endpoints and the payload seal telling a replay from a
// conflict. Custody kinds travel opaque here; the adapter judges
// them against the ledger vocabulary inside the transaction. Season
// names the publication book the legs settle in.
type PublishRequest struct {
	Key         domain.PublishKey
	Account     string
	Content     domain.MeasuredContent
	Price       domain.PriceEntry
	Quote       domain.PublicationQuote
	FromKind    string
	FromLabel   string
	ToKind      string
	ToLabel     string
	At          time.Time
	PayloadHash string
	Season      domain.SeasonKey
}

// PublishResult is the settled publication: the stored row, the
// journal transfer moving the exact quoted cost, the database
// posted instant and whether the call replayed the original
// settlement. Season names the publication book the receipt stays
// pinned to.
type PublishResult struct {
	PublicationID string
	TransferID    string
	TotalMilli    int64
	PostedAt      time.Time
	Replayed      bool
	Season        domain.SeasonKey
}

// PublishRepository settles publications with their INK charge in
// one transaction: the publication row and the ledger legs commit
// together, or nothing is stored at all.
type PublishRepository interface {
	// Publish settles one intention keyed idempotently by account
	// and token. Replays resolve the original settlement untouched;
	// divergent terms under one key conflict instead of charging
	// twice; uncovered balances refuse without writing.
	Publish(ctx context.Context, request PublishRequest) (*PublishResult, error)
}

// PublishUseCase publishes one content with its exact quoted charge.
// It is an internal operation: no public surface calls it.
type PublishUseCase struct {
	publications PublishRepository
}

// NewPublishUseCase creates an instance of PublishUseCase, refusing
// incomplete composition.
func NewPublishUseCase(publications PublishRepository) (*PublishUseCase, error) {
	if publications == nil {
		return nil, ErrInvalidPublishConfig
	}
	return &PublishUseCase{publications: publications}, nil
}

// Execute validates the publication envelope and settles it. The
// token, the account bound to the quote, the unedited content, the
// live quote, the covering price and the coherent total stop
// malformed calls before any store is touched; every ledger fact
// resolves inside the repository transaction. The book travels
// beside the seal: a stale or cross-book preview conflicts instead
// of charging another book.
func (uc *PublishUseCase) Execute(ctx context.Context, cmd PublishCommand) (*PublishResult, error) {
	key, err := domain.ParsePublishKey(cmd.Key)
	if err != nil {
		return nil, err
	}
	if cmd.Content.IsZero() {
		return nil, domain.ErrInvalidQuote
	}
	season := domain.SeasonKey(domain.CompatSeasonKey)
	if cmd.Season != "" {
		season, err = domain.ParseSeasonKey(cmd.Season)
		if err != nil {
			return nil, err
		}
	}
	if cmd.Quote.Season.String() == "" {
		cmd.Quote.Season = domain.SeasonKey(domain.CompatSeasonKey)
	}
	if err := domain.RequireSeasonalReset(season, cmd.ResetAcknowledged); err != nil {
		return nil, err
	}
	if err := cmd.Quote.VerifySeasonalAcceptance(cmd.Account, cmd.Content, cmd.Now, season); err != nil {
		return nil, err
	}
	// The accepted version protects a valid intention across later
	// price changes: the price must cover the acceptance instant,
	// not the settlement instant. Liveness at settlement is already
	// proven above, so a quote accepted under v1 settles at v1 even
	// after v2 takes effect.
	if !cmd.Price.Covers(cmd.Quote.AcceptedAt) {
		return nil, domain.ErrPriceNotFound
	}
	if cmd.Price.Service != cmd.Quote.Service || cmd.Price.Version != cmd.Quote.Version {
		return nil, domain.ErrInvalidQuote
	}
	total, err := domain.TotalFor(cmd.Content.Units(), cmd.Price.PriceMilli)
	if err != nil {
		return nil, err
	}
	if total != cmd.Quote.TotalMilli {
		return nil, domain.ErrInvalidQuote
	}
	if strings.TrimSpace(cmd.FromKind) == "" || strings.TrimSpace(cmd.FromLabel) == "" ||
		strings.TrimSpace(cmd.ToKind) == "" || strings.TrimSpace(cmd.ToLabel) == "" {
		return nil, domain.ErrInvalidQuote
	}
	return uc.publications.Publish(ctx, PublishRequest{
		Key:       key,
		Account:   cmd.Account,
		Content:   cmd.Content,
		Price:     cmd.Price,
		Quote:     cmd.Quote,
		FromKind:  strings.TrimSpace(cmd.FromKind),
		FromLabel: strings.TrimSpace(cmd.FromLabel),
		ToKind:    strings.TrimSpace(cmd.ToKind),
		ToLabel:   strings.TrimSpace(cmd.ToLabel),
		At:        cmd.Now.UTC(),
		Season:    season,
		PayloadHash: domain.PublishPayloadHash(domain.PublishPayload{
			Account: cmd.Account, Service: cmd.Quote.Service.String(),
			Version: cmd.Quote.Version, Units: cmd.Quote.Units,
			AmountMilli: cmd.Quote.TotalMilli,
			ContentHash: cmd.Quote.ContentHash.String(), QuoteHash: cmd.Quote.Hash,
			FromKind: strings.TrimSpace(cmd.FromKind), FromLabel: strings.TrimSpace(cmd.FromLabel),
			ToKind: strings.TrimSpace(cmd.ToKind), ToLabel: strings.TrimSpace(cmd.ToLabel),
		}),
	})
}
