package domain

import (
	"errors"
	"testing"
	"time"
)

func mustService(t *testing.T, raw string) ServiceID {
	t.Helper()
	service, err := ParseServiceID(raw)
	if err != nil {
		t.Fatalf("ParseServiceID(%q) = %v, want nil", raw, err)
	}
	return service
}

func priceRequest(service ServiceID, version int, from, until time.Time, price int64, unit BillingUnit, authority string) PriceRequest {
	return PriceRequest{
		Service:    service,
		Version:    version,
		ValidFrom:  from,
		ValidUntil: until,
		PriceMilli: price,
		Unit:       unit,
		Authority:  authority,
	}
}

func fixedWindow() (time.Time, time.Time, time.Time) {
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	middle := start.Add(time.Hour)
	end := middle.Add(time.Hour)
	return start, middle, end
}

func TestParseServiceIDRefusesBadSlugs(t *testing.T) {
	for _, raw := range []string{"", " Argument", "argument ", "Argument", "arg_ment", "arg.ment", "ARG", "a b", "a/b"} {
		if _, err := ParseServiceID(raw); !errors.Is(err, ErrInvalidService) {
			t.Errorf("ParseServiceID(%q) = %v, want ErrInvalidService", raw, err)
		}
	}
	if _, err := ParseServiceID(mustService(t, "argument-publish").String()); err != nil {
		t.Errorf("round-trip service = %v, want nil", err)
	}
}

func TestNewPriceEntryRefusesEmptyWindow(t *testing.T) {
	service := mustService(t, "argument-publish")
	start, _, _ := fixedWindow()
	cases := []struct {
		name  string
		from  time.Time
		until time.Time
	}{
		{name: "zero start", from: time.Time{}, until: start.Add(time.Hour)},
		{name: "zero end", from: start, until: time.Time{}},
		{name: "equal", from: start, until: start},
		{name: "inverted", from: start.Add(time.Hour), until: start},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPriceEntry(priceRequest(service, 1, tc.from, tc.until, 1000, UnitGraphemeCluster, "test-authority")); !errors.Is(err, ErrInvalidPriceWindow) {
				t.Fatalf("NewPriceEntry = %v, want ErrInvalidPriceWindow", err)
			}
		})
	}
}

func TestNewPriceEntryRefusesBadFields(t *testing.T) {
	service := mustService(t, "argument-publish")
	start, _, end := fixedWindow()
	if _, err := NewPriceEntry(priceRequest(service, 0, start, end, 1000, UnitGraphemeCluster, "test-authority")); !errors.Is(err, ErrInvalidVersion) {
		t.Errorf("version zero = %v, want ErrInvalidVersion", err)
	}
	if _, err := NewPriceEntry(priceRequest(service, 1, start, end, 0, UnitGraphemeCluster, "test-authority")); !errors.Is(err, ErrInvalidPrice) {
		t.Errorf("price zero = %v, want ErrInvalidPrice", err)
	}
	if _, err := NewPriceEntry(priceRequest(service, 1, start, end, -5, UnitGraphemeCluster, "test-authority")); !errors.Is(err, ErrInvalidPrice) {
		t.Errorf("price negative = %v, want ErrInvalidPrice", err)
	}
	if _, err := NewPriceEntry(priceRequest(service, 1, start, end, 1000, BillingUnit("byte"), "test-authority")); !errors.Is(err, ErrUnknownUnit) {
		t.Errorf("unit byte = %v, want ErrUnknownUnit", err)
	}
	if _, err := NewPriceEntry(priceRequest(service, 1, start, end, 1000, UnitGraphemeCluster, "")); !errors.Is(err, ErrInvalidAuthority) {
		t.Errorf("empty authority = %v, want ErrInvalidAuthority", err)
	}
	if _, err := ParseBillingUnit("byte"); !errors.Is(err, ErrUnknownUnit) {
		t.Errorf("ParseBillingUnit(byte) = %v, want ErrUnknownUnit", err)
	}
	if _, err := ParseAuthority(" bad"); !errors.Is(err, ErrInvalidAuthority) {
		t.Errorf("authority with space = %v, want ErrInvalidAuthority", err)
	}
}

func TestCatalogRefusesOverlappingWindows(t *testing.T) {
	service := mustService(t, "argument-publish")
	start, middle, end := fixedWindow()
	var catalog Catalog
	first, err := NewPriceEntry(priceRequest(service, 1, start, middle, 1000, UnitGraphemeCluster, "test-authority"))
	if err != nil {
		t.Fatalf("NewPriceEntry v1 = %v", err)
	}
	if err := catalog.Add(first); err != nil {
		t.Fatalf("Add v1 = %v", err)
	}
	overlaps := []struct {
		name    string
		version int
		from    time.Time
		until   time.Time
	}{
		{name: "identical", version: 2, from: start, until: middle},
		{name: "contained", version: 3, from: start.Add(10 * time.Minute), until: middle.Add(-10 * time.Minute)},
		{name: "partial left", version: 4, from: start.Add(-10 * time.Minute), until: start.Add(10 * time.Minute)},
		{name: "partial right", version: 5, from: middle.Add(-10 * time.Minute), until: end},
		{name: "enclosing", version: 6, from: start.Add(-time.Hour), until: end},
	}
	for _, tc := range overlaps {
		t.Run(tc.name, func(t *testing.T) {
			entry, err := NewPriceEntry(priceRequest(service, tc.version, tc.from, tc.until, 2000, UnitGraphemeCluster, "test-authority"))
			if err != nil {
				t.Fatalf("NewPriceEntry = %v", err)
			}
			if err := catalog.Add(entry); !errors.Is(err, ErrOverlappingPrice) {
				t.Fatalf("Add overlapping = %v, want ErrOverlappingPrice", err)
			}
		})
	}
	if got := catalog.Len(); got != 1 {
		t.Fatalf("catalog len = %d, want 1 after refused overlaps", got)
	}
}

func TestCatalogAllowsAbuttingChangeAtBoundary(t *testing.T) {
	service := mustService(t, "argument-publish")
	start, middle, end := fixedWindow()
	var catalog Catalog
	first, _ := NewPriceEntry(priceRequest(service, 1, start, middle, 1000, UnitGraphemeCluster, "test-authority"))
	second, _ := NewPriceEntry(priceRequest(service, 2, middle, end, 2000, UnitGraphemeCluster, "test-authority"))
	if err := catalog.Add(first); err != nil {
		t.Fatalf("Add v1 = %v", err)
	}
	if err := catalog.Add(second); err != nil {
		t.Fatalf("Add abutting v2 = %v, want nil", err)
	}
	before, err := catalog.PriceAt(service, middle.Add(-time.Nanosecond))
	if err != nil {
		t.Fatalf("PriceAt tick before = %v", err)
	}
	if before.Version != 1 || before.PriceMilli != 1000 {
		t.Fatalf("tick before = v%d/%d, want v1/1000", before.Version, before.PriceMilli)
	}
	at, err := catalog.PriceAt(service, middle)
	if err != nil {
		t.Fatalf("PriceAt at boundary = %v", err)
	}
	if at.Version != 2 || at.PriceMilli != 2000 {
		t.Fatalf("at boundary = v%d/%d, want v2/2000", at.Version, at.PriceMilli)
	}
	after, err := catalog.PriceAt(service, end.Add(time.Nanosecond))
	if !errors.Is(err, ErrPriceNotFound) {
		t.Fatalf("PriceAt after end = %+v (%v), want ErrPriceNotFound", after, err)
	}
	atEnd, err := catalog.PriceAt(service, end)
	if !errors.Is(err, ErrPriceNotFound) {
		t.Fatalf("PriceAt at end = %+v (%v), want ErrPriceNotFound (half-open)", atEnd, err)
	}
}

func TestCatalogMissingPriceNeverDefaults(t *testing.T) {
	known := mustService(t, "argument-publish")
	unknown := mustService(t, "arena-publish")
	start, middle, _ := fixedWindow()
	var catalog Catalog
	entry, _ := NewPriceEntry(priceRequest(known, 1, start, middle, 1000, UnitGraphemeCluster, "test-authority"))
	if err := catalog.Add(entry); err != nil {
		t.Fatalf("Add = %v", err)
	}
	if _, err := catalog.PriceAt(unknown, start.Add(time.Minute)); !errors.Is(err, ErrPriceNotFound) {
		t.Fatalf("unknown service = %v, want ErrPriceNotFound", err)
	}
	if _, err := catalog.PriceAt(known, start.Add(-time.Minute)); !errors.Is(err, ErrPriceNotFound) {
		t.Fatalf("before window = %v, want ErrPriceNotFound", err)
	}
	if _, err := catalog.PriceAt(known, middle); !errors.Is(err, ErrPriceNotFound) {
		t.Fatalf("at expiry = %v, want ErrPriceNotFound", err)
	}
	var empty Catalog
	if _, err := empty.PriceAt(known, start); !errors.Is(err, ErrPriceNotFound) {
		t.Fatalf("empty catalog = %v, want ErrPriceNotFound", err)
	}
	if _, err := catalog.PriceAt(known, time.Time{}); !errors.Is(err, ErrInvalidPriceWindow) {
		t.Fatalf("zero instant = %v, want ErrInvalidPriceWindow", err)
	}
}

func TestCatalogKeepsPreviousVersionAuditable(t *testing.T) {
	service := mustService(t, "argument-publish")
	other := mustService(t, "arena-publish")
	start, middle, end := fixedWindow()
	var catalog Catalog
	first, _ := NewPriceEntry(priceRequest(service, 1, start, middle, 1000, UnitGraphemeCluster, "test-authority"))
	second, _ := NewPriceEntry(priceRequest(service, 2, middle, end, 2000, UnitGraphemeCluster, "second-authority"))
	foreign, _ := NewPriceEntry(priceRequest(other, 1, start, middle, 500, UnitGraphemeCluster, "test-authority"))
	for _, entry := range []PriceEntry{first, second, foreign} {
		if err := catalog.Add(entry); err != nil {
			t.Fatalf("Add %+v = %v", entry, err)
		}
	}
	kept, err := catalog.ByVersion(service, 1)
	if err != nil {
		t.Fatalf("ByVersion v1 = %v", err)
	}
	if kept.PriceMilli != 1000 || !kept.ValidFrom.Equal(start) || kept.Authority != "test-authority" {
		t.Fatalf("v1 changed after v2: %+v", kept)
	}
	history := catalog.History(service)
	if len(history) != 2 || history[0].Version != 1 || history[1].Version != 2 {
		t.Fatalf("history = %+v, want [v1 v2]", history)
	}
	duplicate, _ := NewPriceEntry(priceRequest(service, 1, end, end.Add(time.Hour), 3000, UnitGraphemeCluster, "test-authority"))
	if err := catalog.Add(duplicate); !errors.Is(err, ErrDuplicatePriceVersion) {
		t.Fatalf("reuse version = %v, want ErrDuplicatePriceVersion", err)
	}
}

func TestCatalogAllowsSameWindowForDifferentServices(t *testing.T) {
	one := mustService(t, "argument-publish")
	two := mustService(t, "arena-publish")
	start, middle, _ := fixedWindow()
	var catalog Catalog
	first, _ := NewPriceEntry(priceRequest(one, 1, start, middle, 1000, UnitGraphemeCluster, "test-authority"))
	second, _ := NewPriceEntry(priceRequest(two, 1, start, middle, 1000, UnitGraphemeCluster, "test-authority"))
	if err := catalog.Add(first); err != nil {
		t.Fatalf("Add one = %v", err)
	}
	if err := catalog.Add(second); err != nil {
		t.Fatalf("Add same window other service = %v, want nil", err)
	}
}

func TestPriceLocaleRefusesUnknown(t *testing.T) {
	for _, raw := range []string{"", "PT", "EN", "pt-BR", "en-US", "es", "fr", " pt", "pt "} {
		if _, err := ParsePriceLocale(raw); !errors.Is(err, ErrUnknownPriceLocale) {
			t.Errorf("ParsePriceLocale(%q) = %v, want ErrUnknownPriceLocale", raw, err)
		}
	}
	for _, raw := range []string{"pt", "en"} {
		locale, err := ParsePriceLocale(raw)
		if err != nil {
			t.Errorf("ParsePriceLocale(%q) = %v, want nil", raw, err)
		} else if locale.String() != raw {
			t.Errorf("locale round-trip = %q, want %q", locale.String(), raw)
		}
	}
	pt := PriceTitlesFor(PriceLocalePortuguese)
	en := PriceTitlesFor(PriceLocaleEnglish)
	if pt.Title == en.Title || pt.Title == "" || en.Title == "" {
		t.Fatalf("titles do not differ by locale: pt=%q en=%q", pt.Title, en.Title)
	}
}

func TestPriceEntryNormalizesToUTC(t *testing.T) {
	service := mustService(t, "argument-publish")
	zone := time.FixedZone("BRT", -3*60*60)
	from := time.Date(2026, 9, 29, 9, 0, 0, 0, zone)
	until := time.Date(2026, 9, 29, 10, 0, 0, 0, zone)
	entry, err := NewPriceEntry(priceRequest(service, 1, from, until, 1000, UnitGraphemeCluster, "test-authority"))
	if err != nil {
		t.Fatalf("NewPriceEntry = %v", err)
	}
	if entry.ValidFrom.Location() != time.UTC || entry.ValidUntil.Location() != time.UTC {
		t.Fatalf("window not UTC: %v %v", entry.ValidFrom.Location(), entry.ValidUntil.Location())
	}
	var catalog Catalog
	if err := catalog.Add(entry); err != nil {
		t.Fatalf("Add = %v", err)
	}
	got, err := catalog.PriceAt(service, time.Date(2026, 9, 29, 12, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("PriceAt UTC equivalent = %v", err)
	}
	if got.Version != 1 {
		t.Fatalf("PriceAt version = %d, want 1", got.Version)
	}
}
