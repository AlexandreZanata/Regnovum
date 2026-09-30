package catalog

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// catalogProducts is the price list documented in docs/MONETIZATION.md §4,
// reproduced here so the versioned data file is checked against the documented
// amounts and currencies instead of against itself.
var catalogProducts = []struct {
	market   string
	product  string
	currency domain.Currency
	amount   int64
	grant    domain.GrantKind
	quantity int64
}{
	{market: "BR", product: "ink_10000", currency: domain.CurrencyBRL, amount: 990, grant: domain.GrantKindINK, quantity: 10000},
	{market: "BR", product: "ink_40000", currency: domain.CurrencyBRL, amount: 2490, grant: domain.GrantKindINK, quantity: 40000},
	{market: "BR", product: "ink_100000", currency: domain.CurrencyBRL, amount: 4990, grant: domain.GrantKindINK, quantity: 100000},
	{market: "BR", product: "pass_1", currency: domain.CurrencyBRL, amount: 990, grant: domain.GrantKindArenaPass, quantity: 1},
	{market: "BR", product: "pass_5", currency: domain.CurrencyBRL, amount: 3990, grant: domain.GrantKindArenaPass, quantity: 5},
	{market: "BR", product: "member_monthly", currency: domain.CurrencyBRL, amount: 1990, grant: domain.GrantKindMember},
	{market: "INTERNATIONAL", product: "ink_10000", currency: domain.CurrencyUSD, amount: 199, grant: domain.GrantKindINK, quantity: 10000},
	{market: "INTERNATIONAL", product: "ink_40000", currency: domain.CurrencyUSD, amount: 499, grant: domain.GrantKindINK, quantity: 40000},
	{market: "INTERNATIONAL", product: "ink_100000", currency: domain.CurrencyUSD, amount: 999, grant: domain.GrantKindINK, quantity: 100000},
	{market: "INTERNATIONAL", product: "pass_1", currency: domain.CurrencyUSD, amount: 199, grant: domain.GrantKindArenaPass, quantity: 1},
	{market: "INTERNATIONAL", product: "pass_5", currency: domain.CurrencyUSD, amount: 799, grant: domain.GrantKindArenaPass, quantity: 5},
	{market: "INTERNATIONAL", product: "member_monthly", currency: domain.CurrencyUSD, amount: 399, grant: domain.GrantKindMember},
}

// priceID returns a deterministic, well-formed Stripe price for the entry.
func priceID(index int) string {
	return fmt.Sprintf("price_test%02d", index)
}

// fullPrices returns a price for every documented product.
func fullPrices() []ConfiguredPrice {
	prices := make([]ConfiguredPrice, 0, len(catalogProducts))
	for index, entry := range catalogProducts {
		prices = append(prices, ConfiguredPrice{Market: entry.market, Product: entry.product, PriceID: priceID(index)})
	}
	return prices
}

// pricesFor returns a price for every product of one market.
func pricesFor(market string) []ConfiguredPrice {
	prices := make([]ConfiguredPrice, 0, len(catalogProducts))
	for index, entry := range catalogProducts {
		if entry.market == market {
			prices = append(prices, ConfiguredPrice{Market: entry.market, Product: entry.product, PriceID: priceID(index)})
		}
	}
	return prices
}

// bothMarkets enables the two documented commercial regions.
func bothMarkets() []EnabledMarket {
	return []EnabledMarket{
		{Market: "BR", Currency: "BRL"},
		{Market: "INTERNATIONAL", Currency: "USD"},
	}
}

func TestVersionedCatalogMatchesTheDocumentedPriceList(t *testing.T) {
	t.Parallel()

	loaded, err := Load(Spec{Markets: bothMarkets(), Prices: fullPrices()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Version() != 1 {
		t.Errorf("version = %d, want 1", loaded.Version())
	}
	if got := len(loaded.Products()); got != len(catalogProducts) {
		t.Fatalf("catalog has %d products, want %d", got, len(catalogProducts))
	}

	expectedPrice := make(map[string]string, len(catalogProducts))
	for index, entry := range catalogProducts {
		expectedPrice[entry.market+"/"+entry.product] = priceID(index)
	}

	for _, expected := range catalogProducts {
		market, err := domain.ParseMarket(expected.market)
		if err != nil {
			t.Fatalf("ParseMarket(%q): %v", expected.market, err)
		}
		id, err := domain.ParseProductID(expected.product)
		if err != nil {
			t.Fatalf("ParseProductID(%q): %v", expected.product, err)
		}

		product, err := loaded.Product(market, id)
		if err != nil {
			t.Fatalf("Product(%s/%s): %v", expected.market, expected.product, err)
		}
		if product.Currency() != expected.currency {
			t.Errorf("%s/%s currency = %s, want %s", expected.market, expected.product, product.Currency(), expected.currency)
		}
		if product.Amount().MinorUnits() != expected.amount {
			t.Errorf("%s/%s amount = %d, want %d", expected.market, expected.product, product.Amount().MinorUnits(), expected.amount)
		}
		if product.Grant().Kind() != expected.grant {
			t.Errorf("%s/%s grant = %s, want %s", expected.market, expected.product, product.Grant().Kind(), expected.grant)
		}
		if product.Grant().Quantity() != expected.quantity {
			t.Errorf("%s/%s grant quantity = %d, want %d", expected.market, expected.product, product.Grant().Quantity(), expected.quantity)
		}
		if want := expectedPrice[expected.market+"/"+expected.product]; product.PriceID().String() != want {
			t.Errorf("%s/%s price = %s, want %s", expected.market, expected.product, product.PriceID(), want)
		}
	}

	if markets := loaded.Markets(); len(markets) != 2 || markets[0] != domain.MarketBrazil || markets[1] != domain.MarketInternational {
		t.Errorf("Markets() = %v, want BR then INTERNATIONAL", markets)
	}

	// Every documented amount is an integer of minor units, never a float.
	for _, product := range loaded.Products() {
		if product.Amount().MinorUnits() < 1 {
			t.Errorf("%s has a non-positive amount", product)
		}
	}
}

func TestLoadServesOnlyEnabledRegions(t *testing.T) {
	t.Parallel()

	loaded, err := Load(Spec{
		Markets: []EnabledMarket{{Market: "br", Currency: " brl "}},
		Prices:  pricesFor("BR"),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Lower-cased configuration is canonicalized, and only the enabled region
	// is part of the deployment's catalog.
	brProducts := loaded.ProductsForMarket(domain.MarketBrazil)
	if len(brProducts) != 6 {
		t.Fatalf("BR has %d products, want 6", len(brProducts))
	}
	if len(loaded.ProductsForMarket(domain.MarketInternational)) != 0 {
		t.Error("a disabled region must not be sellable")
	}
	id, _ := domain.ParseProductID("ink_10000")
	if _, err := loaded.Product(domain.MarketInternational, id); !errors.Is(err, domain.ErrUnknownProduct) {
		t.Errorf("disabled region lookup error = %v, want ErrUnknownProduct", err)
	}
	if _, err := loaded.Product(domain.MarketBrazil, id); err != nil {
		t.Errorf("enabled region lookup error = %v", err)
	}
}

func TestLoadInDevelopmentAllowsUnprovisionedPrices(t *testing.T) {
	t.Parallel()

	loaded, err := Load(Spec{Markets: bothMarkets()})
	if err != nil {
		t.Fatalf("Load without prices must be valid outside production: %v", err)
	}
	if got := len(loaded.Products()); got != len(catalogProducts) {
		t.Fatalf("catalog has %d products, want %d", got, len(catalogProducts))
	}
	for _, product := range loaded.Products() {
		if product.IsPriced() {
			t.Fatalf("%s must not be priced without configuration", product)
		}
	}

	id, _ := domain.ParseProductID("pass_5")
	if _, err := loaded.Product(domain.MarketBrazil, id); !errors.Is(err, domain.ErrProductNotPriced) {
		t.Errorf("unpriced lookup error = %v, want ErrProductNotPriced", err)
	}
	if !loaded.Has(domain.MarketBrazil, id) {
		t.Error("an unpriced product is still a catalogued product")
	}
}

func TestLoadWithoutConfigurationSellsNothing(t *testing.T) {
	t.Parallel()

	loaded, err := Load(Spec{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version() != 1 {
		t.Errorf("version = %d, want 1", loaded.Version())
	}
	if len(loaded.Products()) != 0 || len(loaded.Markets()) != 0 {
		t.Errorf("empty configuration must sell nothing, got %v", loaded.Markets())
	}
	id, _ := domain.ParseProductID("ink_10000")
	if _, err := loaded.Product(domain.MarketBrazil, id); !errors.Is(err, domain.ErrUnknownProduct) {
		t.Errorf("lookup error = %v, want ErrUnknownProduct", err)
	}
}

func TestLoadRefusesUnchargeableConfiguration(t *testing.T) {
	t.Parallel()

	type rejection struct {
		name    string
		spec    Spec
		wantErr error
		mention string
	}
	rejections := []rejection{
		{
			// The catalog binds BR to BRL: charging dollars there would sell a
			// price in the wrong unit.
			name:    "currency of the region",
			spec:    Spec{Markets: []EnabledMarket{{Market: "BR", Currency: "USD"}}, Prices: pricesFor("BR")},
			wantErr: domain.ErrMarketCurrencyMismatch,
			mention: "charges BRL, not USD",
		},
		{
			name:    "unsupported currency",
			spec:    Spec{Markets: []EnabledMarket{{Market: "BR", Currency: "EUR"}}},
			wantErr: domain.ErrUnsupportedCurrency,
		},
		{
			name:    "unknown market",
			spec:    Spec{Markets: []EnabledMarket{{Market: "MARS", Currency: "BRL"}}},
			wantErr: domain.ErrInvalidMarket,
		},
		{
			name: "duplicate market",
			spec: Spec{Markets: []EnabledMarket{
				{Market: "BR", Currency: "BRL"},
				{Market: "br", Currency: "BRL"},
			}},
			wantErr: domain.ErrDuplicateMarket,
		},
		{
			name:    "unknown product price",
			spec:    Spec{Markets: bothMarkets(), Prices: []ConfiguredPrice{{Market: "BR", Product: "ink_99999", PriceID: "price_abc"}}},
			wantErr: domain.ErrUnknownProduct,
			mention: "BR/ink_99999",
		},
		{
			name:    "malformed product identifier",
			spec:    Spec{Markets: bothMarkets(), Prices: []ConfiguredPrice{{Market: "BR", Product: "INK_10000", PriceID: "price_abc"}}},
			wantErr: domain.ErrInvalidProductID,
		},
		{
			name:    "malformed stripe price",
			spec:    Spec{Markets: bothMarkets(), Prices: []ConfiguredPrice{{Market: "BR", Product: "ink_10000", PriceID: "prod_abc"}}},
			wantErr: domain.ErrInvalidStripePriceID,
		},
		{
			// A price configured for a region that is not served is a
			// configuration mistake, not a product to sell.
			name: "price of a disabled region",
			spec: Spec{
				Markets: []EnabledMarket{{Market: "BR", Currency: "BRL"}},
				Prices:  append(pricesFor("BR"), pricesFor("INTERNATIONAL")...),
			},
			wantErr: domain.ErrMarketNotEnabled,
			mention: "INTERNATIONAL/ink_10000",
		},
		{
			name: "duplicate price",
			spec: Spec{Markets: bothMarkets(), Prices: []ConfiguredPrice{
				{Market: "BR", Product: "ink_10000", PriceID: "price_abc"},
				{Market: "br", Product: "ink_10000", PriceID: "price_def"},
			}},
			wantErr: domain.ErrDuplicatePrice,
		},
		{
			name:    "production without an enabled region",
			spec:    Spec{Production: true},
			wantErr: domain.ErrNoMarketEnabled,
		},
		{
			// Production must be able to charge every product it sells.
			name: "production without a price",
			spec: Spec{
				Production: true,
				Markets:    bothMarkets(),
				Prices:     pricesFor("BR"),
			},
			wantErr: domain.ErrPriceIDRequired,
			mention: "INTERNATIONAL/ink_10000",
		},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			loaded, err := Load(tc.spec)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Load error = %v, want %v", err, tc.wantErr)
			}
			if loaded != nil {
				t.Fatal("a refused configuration must not return a catalog")
			}
			if tc.mention != "" && !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("error %q must mention %q", err, tc.mention)
			}
		})
	}
}

func TestLoadProductionAcceptsAFullyPricedCatalog(t *testing.T) {
	t.Parallel()

	loaded, err := Load(Spec{Production: true, Markets: bothMarkets(), Prices: fullPrices()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(loaded.Products()); got != len(catalogProducts) {
		t.Fatalf("catalog has %d products, want %d", got, len(catalogProducts))
	}
	for _, product := range loaded.Products() {
		if !product.IsPriced() {
			t.Errorf("%s must be priced in production", product)
		}
	}
}

func TestVersionedDataFileIsValidatedStrictly(t *testing.T) {
	t.Parallel()

	type rejection struct {
		name    string
		data    string
		wantErr error
	}
	rejections := []rejection{
		{name: "invalid json", data: "{not json"},
		{name: "unknown field", data: `{"version":1,"products":[{"market":"BR","product":"ink_10000","currency":"BRL","amount_minor":990,"grant":"INK","ink_quantity":10000,"discount":true}]}`},
		{name: "trailing content", data: `{"version":1,"products":[]} {}`},
		{name: "missing version", data: `{"products":[]}`, wantErr: domain.ErrInvalidCatalogVersion},
		{name: "unknown market", data: `{"version":1,"products":[{"market":"MARS","product":"ink_10000","currency":"BRL","amount_minor":990,"grant":"INK","ink_quantity":10000}]}`, wantErr: domain.ErrInvalidMarket},
		{name: "unsupported currency", data: `{"version":1,"products":[{"market":"BR","product":"ink_10000","currency":"EUR","amount_minor":990,"grant":"INK","ink_quantity":10000}]}`, wantErr: domain.ErrUnsupportedCurrency},
		{
			name:    "currency of another market",
			data:    `{"version":1,"products":[{"market":"BR","product":"ink_10000","currency":"USD","amount_minor":990,"grant":"INK","ink_quantity":10000}]}`,
			wantErr: domain.ErrMarketCurrencyMismatch,
		},
		{name: "free product", data: `{"version":1,"products":[{"market":"BR","product":"ink_10000","currency":"BRL","amount_minor":0,"grant":"INK","ink_quantity":10000}]}`, wantErr: domain.ErrInvalidMoney},
		{name: "negative amount", data: `{"version":1,"products":[{"market":"BR","product":"ink_10000","currency":"BRL","amount_minor":-990,"grant":"INK","ink_quantity":10000}]}`, wantErr: domain.ErrInvalidMoney},
		{name: "malformed product id", data: `{"version":1,"products":[{"market":"BR","product":"INK","currency":"BRL","amount_minor":990,"grant":"INK","ink_quantity":10000}]}`, wantErr: domain.ErrInvalidProductID},
		{name: "unknown grant", data: `{"version":1,"products":[{"market":"BR","product":"ink_10000","currency":"BRL","amount_minor":990,"grant":"TOKENS","ink_quantity":10000}]}`, wantErr: domain.ErrInvalidGrant},
		{name: "ink grant without quantity", data: `{"version":1,"products":[{"market":"BR","product":"ink_10000","currency":"BRL","amount_minor":990,"grant":"INK"}]}`, wantErr: domain.ErrInvalidGrant},
		{name: "ink grant with a pass quantity", data: `{"version":1,"products":[{"market":"BR","product":"ink_10000","currency":"BRL","amount_minor":990,"grant":"INK","ink_quantity":10000,"pass_quantity":2}]}`, wantErr: domain.ErrInvalidGrant},
		{name: "member grant with a quantity", data: `{"version":1,"products":[{"market":"BR","product":"member_monthly","currency":"BRL","amount_minor":1990,"grant":"MEMBER","ink_quantity":10}]}`, wantErr: domain.ErrInvalidGrant},
		{
			name: "duplicate entry",
			data: `{"version":1,"products":[
				{"market":"BR","product":"ink_10000","currency":"BRL","amount_minor":990,"grant":"INK","ink_quantity":10000},
				{"market":"br","product":"ink_10000","currency":"BRL","amount_minor":1990,"grant":"INK","ink_quantity":20000}
			]}`,
			wantErr: domain.ErrDuplicateProduct,
		},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			loaded, err := loadFrom([]byte(tc.data), Spec{Markets: bothMarkets()})
			if err == nil {
				t.Fatalf("malformed catalog accepted: %v", loaded)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestLoadReportsTheEmbeddedCatalogVersion(t *testing.T) {
	t.Parallel()

	loaded, err := Load(Spec{Markets: bothMarkets(), Prices: fullPrices()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The version travels with the catalog so a grant can always name the
	// price list that was in force when it happened.
	if loaded.Version() != 1 {
		t.Errorf("version = %d, want the versioned data file version 1", loaded.Version())
	}
}
