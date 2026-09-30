// Package catalog is the inbound configuration adapter of the billing module
// (P12-T01): it turns the versioned product catalog shipped with the binary
// plus the deployment's commercial configuration into a validated
// billing domain Catalog.
//
// Two sources of truth, split by nature:
//
//   - products.json (embedded, versioned, reviewed in Git) carries what a
//     product is and costs: the commercial region, the currency, the exact
//     amount in minor units and the entitlement it confers. Amounts come from
//     docs/MONETIZATION.md §4, which documents them as commercial hypotheses;
//     changing a price means shipping a new catalog version, so rights already
//     acquired are never rewritten retroactively.
//   - the deployment configuration carries what is environment specific: which
//     commercial regions are enabled, the currency each one charges, and the
//     Stripe price ID of every product, which differs per Stripe account and
//     mode and therefore can never be committed.
//
// Load composes both and refuses to produce a catalog that cannot be charged
// correctly: an unknown product or region, a currency that disagrees with the
// region, a price configured for a disabled region, and — in production — any
// enabled product without a Stripe price.
package catalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

//go:embed products.json
var versionedProducts []byte

// EnabledMarket is a commercial region enabled in this deployment, with the
// currency it charges.
type EnabledMarket struct {
	Market   string
	Currency string
}

// ConfiguredPrice is the Stripe price configured for one catalog product of an
// enabled market.
type ConfiguredPrice struct {
	Market  string
	Product string
	PriceID string
}

// Spec is the deployment-specific billing configuration that Load composes
// with the versioned catalog. Values are kept as strings because they come
// from the environment (ARENA_BILLING_MARKETS, ARENA_BILLING_PRICE_IDS) and
// are validated here through the domain constructors.
type Spec struct {
	// Production turns on the production safety rules: at least one enabled
	// commercial region, and every enabled product priced.
	Production bool
	// Markets are the enabled commercial regions.
	Markets []EnabledMarket
	// Prices are the Stripe price IDs of the enabled products.
	Prices []ConfiguredPrice
}

// Load builds the validated catalog of the deployment from the embedded
// versioned products and the given specification. Only products of enabled
// markets are part of the result, so a region that is not served cannot be
// charged.
func Load(spec Spec) (*domain.Catalog, error) {
	return loadFrom(versionedProducts, spec)
}

// loadFrom is the seam that lets tests exercise malformed catalog data without
// touching the embedded file.
func loadFrom(data []byte, spec Spec) (*domain.Catalog, error) {
	file, err := parseFile(data)
	if err != nil {
		return nil, err
	}
	return build(file, spec)
}

// catalogFile mirrors the versioned JSON document.
type catalogFile struct {
	Version  int            `json:"version"`
	Products []productEntry `json:"products"`
}

// productEntry mirrors one entry of the versioned JSON document. Amounts are
// integers in minor units; grants carry exactly one quantity field, matching
// the grant kind.
type productEntry struct {
	Market       string `json:"market"`
	Product      string `json:"product"`
	Currency     string `json:"currency"`
	AmountMinor  int64  `json:"amount_minor"`
	Grant        string `json:"grant"`
	InkQuantity  int64  `json:"ink_quantity,omitempty"`
	PassQuantity int32  `json:"pass_quantity,omitempty"`
}

// parseFile decodes the versioned catalog strictly: unknown fields and trailing
// content are refused, so a typo in the file fails loudly instead of being
// ignored.
func parseFile(data []byte) (catalogFile, error) {
	var file catalogFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return catalogFile{}, fmt.Errorf("catalog: versioned catalog is not valid JSON: %w", err)
	}
	if decoder.More() {
		return catalogFile{}, fmt.Errorf("catalog: versioned catalog carries trailing content")
	}
	return file, nil
}

// product converts one JSON entry into a domain product with no Stripe price;
// prices are environment specific and are applied later in build.
func (entry productEntry) product() (domain.Product, error) {
	label := fmt.Sprintf("%s/%s", entry.Market, entry.Product)

	market, err := domain.ParseMarket(entry.Market)
	if err != nil {
		return domain.Product{}, fmt.Errorf("catalog: %s: %w", label, err)
	}
	id, err := domain.ParseProductID(entry.Product)
	if err != nil {
		return domain.Product{}, fmt.Errorf("catalog: %s: %w", label, err)
	}
	currency, err := domain.ParseCurrency(entry.Currency)
	if err != nil {
		return domain.Product{}, fmt.Errorf("catalog: %s: %w", label, err)
	}
	amount, err := domain.NewMoney(entry.AmountMinor, currency)
	if err != nil {
		return domain.Product{}, fmt.Errorf("catalog: %s: %w", label, err)
	}
	grant, err := entry.grant()
	if err != nil {
		return domain.Product{}, fmt.Errorf("catalog: %s: %w", label, err)
	}

	product, err := domain.NewProduct(market, id, amount, grant, "")
	if err != nil {
		return domain.Product{}, fmt.Errorf("catalog: %s: %w", label, err)
	}
	return product, nil
}

// grant builds the entitlement described by one entry, refusing any quantity
// field that does not belong to the declared kind.
func (entry productEntry) grant() (domain.Grant, error) {
	kind, err := domain.ParseGrantKind(entry.Grant)
	if err != nil {
		return domain.Grant{}, err
	}

	switch kind {
	case domain.GrantKindINK:
		if entry.PassQuantity != 0 {
			return domain.Grant{}, domain.ErrInvalidGrant
		}
		return domain.NewINKGrant(entry.InkQuantity)
	case domain.GrantKindArenaPass:
		if entry.InkQuantity != 0 {
			return domain.Grant{}, domain.ErrInvalidGrant
		}
		return domain.NewArenaPassGrant(entry.PassQuantity)
	default:
		if entry.InkQuantity != 0 || entry.PassQuantity != 0 {
			return domain.Grant{}, domain.ErrInvalidGrant
		}
		return domain.NewMemberGrant(), nil
	}
}

// build validates the versioned catalog, checks the deployment configuration
// against it and returns the catalog of the enabled markets only.
func build(file catalogFile, spec Spec) (*domain.Catalog, error) {
	entries := make([]domain.Product, 0, len(file.Products))
	for index, entry := range file.Products {
		product, err := entry.product()
		if err != nil {
			return nil, fmt.Errorf("catalog: versioned entry %d: %w", index, err)
		}
		entries = append(entries, product)
	}
	versioned, err := domain.NewCatalog(file.Version, entries)
	if err != nil {
		return nil, fmt.Errorf("catalog: versioned catalog: %w", err)
	}

	enabled, err := enabledMarkets(versioned, spec)
	if err != nil {
		return nil, err
	}
	if spec.Production && len(enabled) == 0 {
		return nil, domain.ErrNoMarketEnabled
	}

	prices, err := configuredPrices(versioned, enabled, spec)
	if err != nil {
		return nil, err
	}

	products := make([]domain.Product, 0, len(versioned.Products()))
	// Products() is in canonical order, so catalog errors are deterministic.
	for _, product := range versioned.Products() {
		if !enabled[product.Market()] {
			continue
		}
		priceID := prices[product.Market()][product.ID()]
		if !priceID.IsSet() {
			if spec.Production {
				return nil, fmt.Errorf("catalog: production requires a Stripe price for %s: %w", product, domain.ErrPriceIDRequired)
			}
			products = append(products, product)
			continue
		}
		priced, err := product.WithPriceID(priceID)
		if err != nil {
			return nil, fmt.Errorf("catalog: %s: %w", product, err)
		}
		products = append(products, priced)
	}

	return domain.NewCatalog(file.Version, products)
}

// enabledMarkets validates the configured commercial regions against the
// versioned catalog: every region must exist and charge exactly the currency
// it declares in the catalog.
func enabledMarkets(versioned *domain.Catalog, spec Spec) (map[domain.Market]bool, error) {
	enabled := make(map[domain.Market]bool, len(spec.Markets))
	configured := make(map[domain.Market]domain.Currency, len(spec.Markets))

	for _, entry := range spec.Markets {
		market, err := domain.ParseMarket(entry.Market)
		if err != nil {
			return nil, fmt.Errorf("catalog: enabled market %q: %w", entry.Market, err)
		}
		if enabled[market] {
			return nil, fmt.Errorf("catalog: enabled market %q: %w", entry.Market, domain.ErrDuplicateMarket)
		}
		currency, err := domain.ParseCurrency(entry.Currency)
		if err != nil {
			return nil, fmt.Errorf("catalog: currency of enabled market %s: %w", market, err)
		}
		enabled[market] = true
		configured[market] = currency
	}

	// Canonical order keeps the reported problem deterministic.
	for _, market := range domain.AllMarkets() {
		if !enabled[market] {
			continue
		}
		if len(versioned.ProductsForMarket(market)) == 0 {
			return nil, fmt.Errorf("catalog: enabled market %s: %w", market, domain.ErrUnknownMarket)
		}
		expected, err := market.Currency()
		if err != nil {
			return nil, err
		}
		if configured[market] != expected {
			return nil, fmt.Errorf(
				"catalog: enabled market %s charges %s, not %s: %w",
				market, expected, configured[market], domain.ErrMarketCurrencyMismatch,
			)
		}
	}

	return enabled, nil
}

// configuredPrices validates every configured Stripe price against the versioned
// catalog and the enabled regions, and groups the result by market and product.
func configuredPrices(versioned *domain.Catalog, enabled map[domain.Market]bool, spec Spec) (map[domain.Market]map[domain.ProductID]domain.StripePriceID, error) {
	prices := make(map[domain.Market]map[domain.ProductID]domain.StripePriceID, len(enabled))

	for _, entry := range spec.Prices {
		label := fmt.Sprintf("%s/%s", entry.Market, entry.Product)

		market, err := domain.ParseMarket(entry.Market)
		if err != nil {
			return nil, fmt.Errorf("catalog: configured price %s: %w", label, err)
		}
		id, err := domain.ParseProductID(entry.Product)
		if err != nil {
			return nil, fmt.Errorf("catalog: configured price %s: %w", label, err)
		}
		priceID, err := domain.ParseStripePriceID(entry.PriceID)
		if err != nil {
			return nil, fmt.Errorf("catalog: configured price %s: %w", label, err)
		}
		if !versioned.Has(market, id) {
			return nil, fmt.Errorf("catalog: configured price %s: %w", label, domain.ErrUnknownProduct)
		}
		if !enabled[market] {
			return nil, fmt.Errorf("catalog: configured price %s: %w", label, domain.ErrMarketNotEnabled)
		}
		if prices[market] == nil {
			prices[market] = make(map[domain.ProductID]domain.StripePriceID)
		}
		if _, duplicate := prices[market][id]; duplicate {
			return nil, fmt.Errorf("catalog: configured price %s: %w", label, domain.ErrDuplicatePrice)
		}
		prices[market][id] = priceID
	}

	return prices, nil
}
