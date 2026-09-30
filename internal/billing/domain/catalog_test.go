package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

func mustMoney(t *testing.T, minorUnits int64, currency domain.Currency) domain.Money {
	t.Helper()
	money, err := domain.NewMoney(minorUnits, currency)
	if err != nil {
		t.Fatalf("NewMoney(%d, %s): %v", minorUnits, currency, err)
	}
	return money
}

func mustINKGrant(t *testing.T, quantity int64) domain.Grant {
	t.Helper()
	grant, err := domain.NewINKGrant(quantity)
	if err != nil {
		t.Fatalf("NewINKGrant(%d): %v", quantity, err)
	}
	return grant
}

func mustPassGrant(t *testing.T, quantity int32) domain.Grant {
	t.Helper()
	grant, err := domain.NewArenaPassGrant(quantity)
	if err != nil {
		t.Fatalf("NewArenaPassGrant(%d): %v", quantity, err)
	}
	return grant
}

func mustProduct(t *testing.T, market domain.Market, id string, minorUnits int64, currency domain.Currency, grant domain.Grant, priceID string) domain.Product {
	t.Helper()
	productID, err := domain.ParseProductID(id)
	if err != nil {
		t.Fatalf("ParseProductID(%q): %v", id, err)
	}
	price, err := domain.ParseStripePriceID(priceID)
	if err != nil {
		t.Fatalf("ParseStripePriceID(%q): %v", priceID, err)
	}
	product, err := domain.NewProduct(market, productID, mustMoney(t, minorUnits, currency), grant, price)
	if err != nil {
		t.Fatalf("NewProduct(%s/%s): %v", market, id, err)
	}
	return product
}

func TestMarketVocabularyAndCurrency(t *testing.T) {
	t.Parallel()

	canonicalizing := []struct {
		input string
		want  domain.Market
	}{
		{input: "BR", want: domain.MarketBrazil},
		{input: "br", want: domain.MarketBrazil},
		{input: "  Br  ", want: domain.MarketBrazil},
		{input: "international", want: domain.MarketInternational},
		{input: "INTERNATIONAL", want: domain.MarketInternational},
	}
	for _, tc := range canonicalizing {
		market, err := domain.ParseMarket(tc.input)
		if err != nil {
			t.Fatalf("ParseMarket(%q) error = %v", tc.input, err)
		}
		if market != tc.want || market.String() != tc.want.String() {
			t.Errorf("ParseMarket(%q) = %q, want %q", tc.input, market, tc.want)
		}
	}

	for _, input := range []string{"", "   ", "MARS", "brl", "BR-2", "internacional"} {
		market, err := domain.ParseMarket(input)
		if !errors.Is(err, domain.ErrInvalidMarket) {
			t.Errorf("ParseMarket(%q) error = %v, want ErrInvalidMarket", input, err)
		}
		if !market.IsZero() {
			t.Errorf("failed ParseMarket(%q) must yield the zero Market", input)
		}
	}

	// Every market charges exactly one currency: this binding is what makes a
	// configured currency able to disagree with the catalog.
	wantCurrency := map[domain.Market]domain.Currency{
		domain.MarketBrazil:        domain.CurrencyBRL,
		domain.MarketInternational: domain.CurrencyUSD,
	}
	markets := domain.AllMarkets()
	if len(markets) != len(wantCurrency) {
		t.Fatalf("AllMarkets() = %v, want %d entries", markets, len(wantCurrency))
	}
	for _, market := range markets {
		currency, err := market.Currency()
		if err != nil {
			t.Fatalf("Currency() of %s: %v", market, err)
		}
		if currency != wantCurrency[market] {
			t.Errorf("Currency() of %s = %s, want %s", market, currency, wantCurrency[market])
		}
	}

	if _, err := domain.Market("MARS").Currency(); !errors.Is(err, domain.ErrInvalidMarket) {
		t.Errorf("unknown market currency error = %v, want ErrInvalidMarket", err)
	}
}

func TestCurrencyVocabulary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		input string
		want  domain.Currency
	}{
		{input: "BRL", want: domain.CurrencyBRL},
		{input: "brl", want: domain.CurrencyBRL},
		{input: " usd ", want: domain.CurrencyUSD},
	} {
		currency, err := domain.ParseCurrency(tc.input)
		if err != nil {
			t.Fatalf("ParseCurrency(%q) error = %v", tc.input, err)
		}
		if currency != tc.want {
			t.Errorf("ParseCurrency(%q) = %s, want %s", tc.input, currency, tc.want)
		}
	}

	for _, input := range []string{"", "EUR", "US", "USDD", "BRLS"} {
		if _, err := domain.ParseCurrency(input); !errors.Is(err, domain.ErrUnsupportedCurrency) {
			t.Errorf("ParseCurrency(%q) error = %v, want ErrUnsupportedCurrency", input, err)
		}
	}

	if len(domain.AllCurrencies()) != 2 {
		t.Errorf("AllCurrencies() = %v, want the two documented regions", domain.AllCurrencies())
	}
}

func TestMoneyIsExactAndNeverFormatsAPrice(t *testing.T) {
	t.Parallel()

	money, err := domain.NewMoney(990, domain.CurrencyBRL)
	if err != nil {
		t.Fatalf("NewMoney(990, BRL): %v", err)
	}
	if money.MinorUnits() != 990 || money.Currency() != domain.CurrencyBRL {
		t.Errorf("money = %d %s, want 990 BRL", money.MinorUnits(), money.Currency())
	}
	if money.IsZero() {
		t.Error("990 minor units must not be zero")
	}
	// The diagnostic rendering keeps minor units and never inserts a locale
	// separador: the backend does not pre-format money (I18N_STANDARD §5).
	if money.String() != "BRL 990" {
		t.Errorf("String() = %q, want %q", money.String(), "BRL 990")
	}
	if money.Equals(money) != true || money.Equals(domain.Money{}) {
		t.Error("money equality semantics are inconsistent")
	}

	if _, err := domain.NewMoney(-1, domain.CurrencyBRL); !errors.Is(err, domain.ErrInvalidMoney) {
		t.Errorf("negative money error = %v, want ErrInvalidMoney", err)
	}
	if _, err := domain.NewMoney(990, domain.Currency("EUR")); !errors.Is(err, domain.ErrUnsupportedCurrency) {
		t.Errorf("unsupported currency error = %v, want ErrUnsupportedCurrency", err)
	}
	if zero, err := domain.NewMoney(0, domain.CurrencyUSD); err != nil || !zero.IsZero() {
		t.Errorf("zero amount = %v, %v; want a valid zero amount", zero, err)
	}
}

func TestProductIDVocabulary(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"ink_10000", "pass_1", "member_monthly", "abc", "a_1", strings.Repeat("p", 64)} {
		id, err := domain.ParseProductID(input)
		if err != nil {
			t.Fatalf("ParseProductID(%q) error = %v", input, err)
		}
		if id.String() != input {
			t.Errorf("ParseProductID(%q) = %q", input, id)
		}
	}

	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "too short", input: "ab"},
		{name: "too long", input: strings.Repeat("a", 65)},
		{name: "upper case", input: "INK_10000"},
		{name: "leading digit", input: "1ink"},
		{name: "hyphen", input: "ink-10000"},
		{name: "space", input: "ink 10000"},
		{name: "dot", input: "ink.10000"},
		{name: "trailing underscore", input: "ink_"},
		{name: "accent", input: "tinta_ção"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := domain.ParseProductID(tc.input)
			if !errors.Is(err, domain.ErrInvalidProductID) {
				t.Fatalf("ParseProductID(%q) error = %v, want ErrInvalidProductID", tc.input, err)
			}
			if id != "" {
				t.Fatalf("failed parse must yield the zero ProductID, got %q", id)
			}
		})
	}
}

func TestStripePriceIDIsOptionalButNeverMalformed(t *testing.T) {
	t.Parallel()

	unset, err := domain.ParseStripePriceID("")
	if err != nil {
		t.Fatalf("unset price must be valid: %v", err)
	}
	if unset.IsSet() || !unset.IsZero() {
		t.Error("unset price must report IsZero and not IsSet")
	}

	price, err := domain.ParseStripePriceID("price_1PabcdefghIJKLmnop")
	if err != nil {
		t.Fatalf("ParseStripePriceID: %v", err)
	}
	if !price.IsSet() || price.String() != "price_1PabcdefghIJKLmnop" {
		t.Errorf("price = %q", price)
	}

	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "prefix only", input: "price_"},
		{name: "wrong prefix", input: "prod_123"},
		{name: "no prefix", input: "1PabcdefghIJKLmnop"},
		{name: "underscore in body", input: "price_ab_cd"},
		{name: "inner space", input: "price_ab cd"},
		{name: "newline", input: "price_ab\ncd"},
		{name: "non ascii", input: "price_ação"},
		{name: "too long", input: "price_" + strings.Repeat("a", 195)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := domain.ParseStripePriceID(tc.input); !errors.Is(err, domain.ErrInvalidStripePriceID) {
				t.Fatalf("ParseStripePriceID(%q) error = %v, want ErrInvalidStripePriceID", tc.input, err)
			}
		})
	}
}

func TestGrantDescribesExactlyOneShape(t *testing.T) {
	t.Parallel()

	ink := mustINKGrant(t, 10000)
	if ink.Kind() != domain.GrantKindINK || ink.Quantity() != 10000 || ink.String() != "INK 10000" {
		t.Errorf("ink grant = %s (%s/%d)", ink, ink.Kind(), ink.Quantity())
	}
	if err := ink.Validate(); err != nil {
		t.Fatalf("ink grant must validate: %v", err)
	}

	passes := mustPassGrant(t, 5)
	if passes.Kind() != domain.GrantKindArenaPass || passes.Quantity() != 5 {
		t.Errorf("pass grant = %s (%s/%d)", passes, passes.Kind(), passes.Quantity())
	}

	member := domain.NewMemberGrant()
	if member.Kind() != domain.GrantKindMember || member.Quantity() != 0 || member.String() != "MEMBER" {
		t.Errorf("member grant = %s (%s/%d)", member, member.Kind(), member.Quantity())
	}
	if err := member.Validate(); err != nil {
		t.Fatalf("member grant must validate: %v", err)
	}

	if !ink.Equals(mustINKGrant(t, 10000)) || ink.Equals(passes) || ink.Equals(member) {
		t.Error("grant equality semantics are inconsistent")
	}
	if !(domain.Grant{}).IsZero() {
		t.Error("zero Grant must report IsZero")
	}
	if err := (domain.Grant{}).Validate(); !errors.Is(err, domain.ErrInvalidGrant) {
		t.Errorf("zero grant error = %v, want ErrInvalidGrant", err)
	}

	if _, err := domain.NewINKGrant(0); !errors.Is(err, domain.ErrInvalidGrant) {
		t.Errorf("zero ink grant error = %v, want ErrInvalidGrant", err)
	}
	if _, err := domain.NewINKGrant(-1); !errors.Is(err, domain.ErrInvalidGrant) {
		t.Errorf("negative ink grant error = %v, want ErrInvalidGrant", err)
	}
	if _, err := domain.NewArenaPassGrant(0); !errors.Is(err, domain.ErrInvalidGrant) {
		t.Errorf("zero pass grant error = %v, want ErrInvalidGrant", err)
	}

	for _, kind := range domain.AllGrantKinds() {
		parsed, err := domain.ParseGrantKind(string(kind))
		if err != nil || parsed != kind {
			t.Errorf("ParseGrantKind(%q) = %q, %v", kind, parsed, err)
		}
	}
	for _, raw := range []string{"", "ink", "TOKENS", "PASS"} {
		if _, err := domain.ParseGrantKind(raw); !errors.Is(err, domain.ErrInvalidGrant) {
			t.Errorf("ParseGrantKind(%q) error = %v, want ErrInvalidGrant", raw, err)
		}
	}
}

func TestProductRefusesUnchargeableEntries(t *testing.T) {
	t.Parallel()

	product := mustProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, mustINKGrant(t, 10000), "")
	if product.Market() != domain.MarketBrazil || product.ID() != "ink_10000" {
		t.Errorf("product identity = %s", product)
	}
	if product.Amount().MinorUnits() != 990 || product.Currency() != domain.CurrencyBRL {
		t.Errorf("product price = %s", product.Amount())
	}
	if product.IsPriced() {
		t.Error("entry with no configured price must not report IsPriced")
	}

	priced, err := product.WithPriceID(domain.StripePriceID("price_abc123"))
	if err != nil {
		t.Fatalf("WithPriceID: %v", err)
	}
	if !priced.IsPriced() || priced.PriceID().String() != "price_abc123" {
		t.Errorf("priced product = %s/%s", priced.PriceID(), priced)
	}
	if product.IsPriced() {
		t.Error("WithPriceID must not mutate the original entry")
	}
	if priced.Equals(product) || priced.PriceID() == product.PriceID() {
		t.Error("the priced copy must differ from the unpriced entry")
	}

	type rejection struct {
		name    string
		build   func() (domain.Product, error)
		wantErr error
	}
	rejections := []rejection{
		{
			name: "unknown market",
			build: func() (domain.Product, error) {
				return domain.NewProduct(domain.Market("MARS"), "ink_10000", mustMoney(t, 990, domain.CurrencyBRL), mustINKGrant(t, 10000), "")
			},
			wantErr: domain.ErrInvalidMarket,
		},
		{
			name: "invalid product id",
			build: func() (domain.Product, error) {
				return domain.NewProduct(domain.MarketBrazil, "INK_10000", mustMoney(t, 990, domain.CurrencyBRL), mustINKGrant(t, 10000), "")
			},
			wantErr: domain.ErrInvalidProductID,
		},
		{
			name: "currency of another market",
			build: func() (domain.Product, error) {
				return domain.NewProduct(domain.MarketBrazil, "ink_10000", mustMoney(t, 199, domain.CurrencyUSD), mustINKGrant(t, 10000), "")
			},
			wantErr: domain.ErrMarketCurrencyMismatch,
		},
		{
			name: "free product",
			build: func() (domain.Product, error) {
				return domain.NewProduct(domain.MarketBrazil, "ink_10000", mustMoney(t, 0, domain.CurrencyBRL), mustINKGrant(t, 10000), "")
			},
			wantErr: domain.ErrInvalidMoney,
		},
		{
			name: "grant without quantity",
			build: func() (domain.Product, error) {
				return domain.NewProduct(domain.MarketBrazil, "ink_10000", mustMoney(t, 990, domain.CurrencyBRL), domain.Grant{}, "")
			},
			wantErr: domain.ErrInvalidGrant,
		},
		{
			name: "malformed stripe price",
			build: func() (domain.Product, error) {
				return domain.NewProduct(domain.MarketBrazil, "ink_10000", mustMoney(t, 990, domain.CurrencyBRL), mustINKGrant(t, 10000), "prod_123")
			},
			wantErr: domain.ErrInvalidStripePriceID,
		},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			product, err := tc.build()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if !product.Equals(domain.Product{}) {
				t.Fatalf("failed construction must yield the zero Product, got %s", product)
			}
		})
	}

	if err := (domain.Product{}).Validate(); !errors.Is(err, domain.ErrInvalidMarket) {
		t.Errorf("zero product error = %v, want ErrInvalidMarket", err)
	}
}

func TestCatalogIsVersionedOrderedAndValidated(t *testing.T) {
	t.Parallel()

	brInk := mustProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, mustINKGrant(t, 10000), "")
	brPass := mustProduct(t, domain.MarketBrazil, "pass_1", 990, domain.CurrencyBRL, mustPassGrant(t, 1), "")
	internationalInk := mustProduct(t, domain.MarketInternational, "ink_10000", 199, domain.CurrencyUSD, mustINKGrant(t, 10000), "")
	member := mustProduct(t, domain.MarketInternational, "member_monthly", 399, domain.CurrencyUSD, domain.NewMemberGrant(), "")

	// Deliberately out of order, including identifiers whose lexical order
	// differs from their amount order.
	catalog, err := domain.NewCatalog(1, []domain.Product{member, brPass, internationalInk, brInk})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	if catalog.Version() != 1 {
		t.Errorf("version = %d, want 1", catalog.Version())
	}

	products := catalog.Products()
	if len(products) != 4 {
		t.Fatalf("Products() has %d entries, want 4", len(products))
	}
	for index := 1; index < len(products); index++ {
		previous, current := products[index-1], products[index]
		if previous.Market() > current.Market() ||
			(previous.Market() == current.Market() && previous.ID() > current.ID()) {
			t.Errorf("catalog order is not canonical: %s before %s", previous, current)
		}
	}

	// The slice is a copy: a caller cannot rewrite the catalog.
	products[0] = mustProduct(t, domain.MarketBrazil, "ink_40000", 2490, domain.CurrencyBRL, mustINKGrant(t, 40000), "")
	if catalog.Products()[0].ID() == "ink_40000" {
		t.Error("Products() must return a copy of the catalog")
	}

	markets := catalog.Markets()
	if len(markets) != 2 || markets[0] != domain.MarketBrazil || markets[1] != domain.MarketInternational {
		t.Errorf("Markets() = %v, want BR then INTERNATIONAL", markets)
	}
	if products := catalog.ProductsForMarket(domain.MarketBrazil); len(products) != 2 {
		t.Errorf("ProductsForMarket(BR) has %d entries, want 2", len(products))
	}
	for _, product := range catalog.ProductsForMarket(domain.MarketBrazil) {
		if product.Market() != domain.MarketBrazil {
			t.Errorf("ProductsForMarket(BR) returned %s product %s", product.Market(), product.ID())
		}
	}
	if products := catalog.ProductsForMarket(domain.MarketInternational); len(products) != 2 {
		t.Errorf("ProductsForMarket(INTL) has %d entries, want 2", len(products))
	}
	if !catalog.Has(domain.MarketBrazil, "ink_10000") || catalog.Has(domain.MarketBrazil, "ink_40000") {
		t.Error("Has must report only the catalogued entries")
	}

	// An unpriced entry is known but cannot be charged in this environment.
	if _, err := catalog.Product(domain.MarketBrazil, "ink_10000"); !errors.Is(err, domain.ErrProductNotPriced) {
		t.Errorf("unpriced lookup error = %v, want ErrProductNotPriced", err)
	}
	if _, err := catalog.Product(domain.MarketBrazil, "ink_99999"); !errors.Is(err, domain.ErrUnknownProduct) {
		t.Errorf("unknown lookup error = %v, want ErrUnknownProduct", err)
	}
	if _, err := catalog.Product(domain.MarketInternational, "member_monthly"); !errors.Is(err, domain.ErrProductNotPriced) {
		t.Errorf("unpriced member lookup error = %v, want ErrProductNotPriced", err)
	}

	if _, err := catalog.Product(domain.MarketBrazil, "ink_10000"); err == nil {
		t.Fatal("lookup of an unpriced product must fail")
	}

	// Once the Stripe price is provisioned the same entry becomes sellable.
	pricedInk, err := mustProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, mustINKGrant(t, 10000), "price_abc").WithPriceID("price_abc")
	if err != nil {
		t.Fatalf("WithPriceID: %v", err)
	}
	pricedCatalog, err := domain.NewCatalog(1, []domain.Product{pricedInk})
	if err != nil {
		t.Fatalf("NewCatalog with priced entry: %v", err)
	}
	resolved, err := pricedCatalog.Product(domain.MarketBrazil, "ink_10000")
	if err != nil {
		t.Fatalf("priced lookup: %v", err)
	}
	if resolved.PriceID().String() != "price_abc" || resolved.Amount().MinorUnits() != 990 {
		t.Errorf("resolved product = %s %s", resolved.PriceID(), resolved.Amount())
	}
}

func TestCatalogRefusesIncoherentLists(t *testing.T) {
	t.Parallel()

	entry := mustProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, mustINKGrant(t, 10000), "")

	if _, err := domain.NewCatalog(0, nil); !errors.Is(err, domain.ErrInvalidCatalogVersion) {
		t.Errorf("version zero error = %v, want ErrInvalidCatalogVersion", err)
	}
	if _, err := domain.NewCatalog(-1, nil); !errors.Is(err, domain.ErrInvalidCatalogVersion) {
		t.Errorf("negative version error = %v, want ErrInvalidCatalogVersion", err)
	}

	duplicated, err := domain.NewCatalog(1, []domain.Product{entry, entry})
	if !errors.Is(err, domain.ErrDuplicateProduct) {
		t.Fatalf("duplicate error = %v, want ErrDuplicateProduct", err)
	}
	if duplicated != nil {
		t.Error("a refused catalog must not be returned")
	}

	// The same product identifier in two markets is legitimate: each market
	// sells its own price list.
	international := mustProduct(t, domain.MarketInternational, "ink_10000", 199, domain.CurrencyUSD, mustINKGrant(t, 10000), "")
	if _, err := domain.NewCatalog(1, []domain.Product{entry, international}); err != nil {
		t.Errorf("same identifier in two markets must be accepted: %v", err)
	}

	if _, err := domain.NewCatalog(1, []domain.Product{{}}); !errors.Is(err, domain.ErrInvalidMarket) {
		t.Errorf("invalid entry error = %v, want ErrInvalidMarket", err)
	}

	empty, err := domain.NewCatalog(3, nil)
	if err != nil {
		t.Fatalf("empty catalog: %v", err)
	}
	if empty.Version() != 3 || len(empty.Products()) != 0 || len(empty.Markets()) != 0 {
		t.Errorf("empty catalog = version %d, %d products", empty.Version(), len(empty.Products()))
	}
}

func TestCatalog_ProductByPriceID(t *testing.T) {
	t.Parallel()

	member := mustProduct(t, domain.MarketBrazil, "member_monthly", 1990, domain.CurrencyBRL, domain.NewMemberGrant(), "price_1QbrMember")
	ink := mustProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, mustINKGrant(t, 10000), "price_1QbrInk")

	cat, err := domain.NewCatalog(1, []domain.Product{member, ink})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}

	// 1. Resolve existing member price ID
	prod, err := cat.ProductByPriceID(domain.StripePriceID("price_1QbrMember"))
	if err != nil {
		t.Fatalf("ProductByPriceID(price_1QbrMember): %v", err)
	}
	if prod.ID().String() != "member_monthly" {
		t.Errorf("product ID = %q, want member_monthly", prod.ID())
	}
	if prod.Grant().Kind() != domain.GrantKindMember {
		t.Errorf("grant kind = %v, want MEMBER", prod.Grant().Kind())
	}

	// 2. Empty price ID returns ErrInvalidStripePriceID
	_, err = cat.ProductByPriceID(domain.StripePriceID(""))
	if !errors.Is(err, domain.ErrInvalidStripePriceID) {
		t.Errorf("empty price ID error = %v, want ErrInvalidStripePriceID", err)
	}

	// 3. Unknown price ID returns ErrUnknownProduct
	_, err = cat.ProductByPriceID(domain.StripePriceID("price_unknown999"))
	if !errors.Is(err, domain.ErrUnknownProduct) {
		t.Errorf("unknown price ID error = %v, want ErrUnknownProduct", err)
	}
}

func TestCatalogBoundaryIdentifiersAndMoney(t *testing.T) {
	t.Parallel()

	// A three-character product identifier is the boundary and parses;
	// two characters refuse (mutation gate: catalog.go:24).
	if _, err := domain.ParseProductID("ab1"); err != nil {
		t.Fatalf("3-char product id: %v", err)
	}
	if _, err := domain.ParseProductID("ab"); !errors.Is(err, domain.ErrInvalidProductID) {
		t.Fatalf("2-char product id error = %v, want ErrInvalidProductID", err)
	}
	// One minor unit is a priced product; zero is not (mutation gate:
	// catalog.go:198).
	oneCentID, err := domain.ParseProductID("one_cent")
	if err != nil {
		t.Fatalf("parse one_cent: %v", err)
	}
	oneCentPrice, err := domain.ParseStripePriceID("")
	if err != nil {
		t.Fatalf("parse empty price: %v", err)
	}
	if _, err := domain.NewProduct(domain.MarketBrazil, oneCentID, mustMoney(t, 1, domain.CurrencyBRL), mustINKGrant(t, 1), oneCentPrice); err != nil {
		t.Fatalf("1-minor-unit product: %v", err)
	}
	if _, err := domain.NewProduct(domain.MarketBrazil, oneCentID, mustMoney(t, 0, domain.CurrencyBRL), mustINKGrant(t, 1), oneCentPrice); !errors.Is(err, domain.ErrInvalidMoney) {
		t.Fatalf("0-minor-unit product error = %v, want ErrInvalidMoney", err)
	}
}

func TestCatalogSingleMarketResolution(t *testing.T) {
	t.Parallel()

	// A single-market catalog resolves only its own market: the market
	// filter must compare for equality, and a 200-character price
	// identifier is the boundary that parses (mutation gate:
	// catalog.go:67,278,291).
	solo, err := domain.NewCatalog(1, []domain.Product{
		mustProduct(t, domain.MarketBrazil, "ink_10000", 990, domain.CurrencyBRL, mustINKGrant(t, 10000), ""),
	})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	markets := solo.Markets()
	if len(markets) != 1 || markets[0] != domain.MarketBrazil {
		t.Fatalf("Markets() = %v, want exactly [brazil]", markets)
	}
	if products := solo.ProductsForMarket(domain.MarketBrazil); len(products) != 1 {
		t.Fatalf("ProductsForMarket(BR) has %d entries, want 1", len(products))
	}
	if products := solo.ProductsForMarket(domain.MarketInternational); len(products) != 0 {
		t.Fatalf("ProductsForMarket(INTL) has %d entries, want 0", len(products))
	}

	price200, err := domain.ParseStripePriceID("price_" + strings.Repeat("p", 194))
	if err != nil {
		t.Fatalf("200-char price id: %v", err)
	}
	if price200.String() != "price_"+strings.Repeat("p", 194) {
		t.Fatalf("price id round-trip = %q", price200.String())
	}
	if _, err := domain.ParseStripePriceID("price_" + strings.Repeat("p", 195)); !errors.Is(err, domain.ErrInvalidStripePriceID) {
		t.Fatalf("201-char price id error = %v, want ErrInvalidStripePriceID", err)
	}
}

func TestCatalogRejectsDuplicatesInLargeCatalogs(t *testing.T) {
	t.Parallel()

	// Thirteen entries force the general sorting path: a duplicate pair
	// far apart must still meet and refuse, whatever the comparator
	// admits (mutation gate: catalog.go:247,249).
	ids := []string{"ink_a", "ink_b", "ink_c", "ink_d", "ink_e", "ink_f", "ink_g", "ink_h", "ink_i", "ink_j", "ink_k"}
	products := make([]domain.Product, 0, 13)
	products = append(products, mustProduct(t, domain.MarketBrazil, "ink_dup", 990, domain.CurrencyBRL, mustINKGrant(t, 10000), ""))
	for _, id := range ids {
		products = append(products, mustProduct(t, domain.MarketBrazil, id, 990, domain.CurrencyBRL, mustINKGrant(t, 10000), ""))
	}
	products = append(products, mustProduct(t, domain.MarketBrazil, "ink_dup", 990, domain.CurrencyBRL, mustINKGrant(t, 10000), ""))
	if _, err := domain.NewCatalog(1, products); !errors.Is(err, domain.ErrDuplicateProduct) {
		t.Fatalf("far-apart duplicate error = %v, want ErrDuplicateProduct", err)
	}
}
