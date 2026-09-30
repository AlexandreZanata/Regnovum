package contract_test

// P29-T03 — money, numbers and time per locale: the domain prices in
// integer minor units with an ISO code and reasons in UTC instants,
// while presentation renders per locale and timezone.
//
// The matrix pins four product promises: no rule parses formatted money
// text (formatted prices and non-vocabulary currencies — including 0-
// and 3-decimal codes — are refused without inferring products that do
// not exist); integers cannot round and survive the int64 boundaries
// exactly; DST transitions and numeric offsets never move a benefit
// period; and the pt/en presentation snapshots render the pinned
// sentences for fixed UTC instants.

import (
	"errors"
	"math"
	"testing"
	"time"

	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

// TestNoRuleParsesFormattedMoneyText proves formatted prices never enter
// the domain: the only string entry (ParseCurrency) accepts ISO codes and
// refuses display text, and Money itself is only ever built from an int64.
func TestNoRuleParsesFormattedMoneyText(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"R$", "R$ 9,90", "US$", "$9.90", "9,90", "9.90", "1.000,00",
		"dólar", "dollar", "", "   ", "BRL9", "9BRL",
		"EUR", "JPY", "BHD", "KWD",
	} {
		if _, err := billingdomain.ParseCurrency(raw); !errors.Is(err, billingdomain.ErrUnsupportedCurrency) {
			t.Errorf("ParseCurrency(%q) = %v, want ErrUnsupportedCurrency", raw, err)
		}
	}

	for _, raw := range []string{"brl", "BRL", " brl ", "usd", "USD"} {
		if _, err := billingdomain.ParseCurrency(raw); err != nil {
			t.Errorf("ParseCurrency(%q) error = %v, want the canonical code", raw, err)
		}
	}
}

// TestMoneyStaysIntegerAtTheBoundaries proves integers cannot round and
// survive the int64 edges exactly: a one-unit difference at the top is
// still a different amount, and the diagnostic rendering never inserts a
// locale separator.
func TestMoneyStaysIntegerAtTheBoundaries(t *testing.T) {
	t.Parallel()

	top, err := billingdomain.NewMoney(math.MaxInt64, billingdomain.CurrencyBRL)
	if err != nil {
		t.Fatalf("NewMoney(MaxInt64) error = %v", err)
	}
	if top.MinorUnits() != math.MaxInt64 {
		t.Fatalf("MinorUnits() = %d, want exact MaxInt64", top.MinorUnits())
	}
	below, err := billingdomain.NewMoney(math.MaxInt64-1, billingdomain.CurrencyBRL)
	if err != nil {
		t.Fatalf("NewMoney(MaxInt64-1) error = %v", err)
	}
	if top.Equals(below) {
		t.Fatal("MaxInt64 and MaxInt64-1 must not compare equal")
	}
	if top.String() != "BRL 9223372036854775807" {
		t.Errorf("String() = %q, want code plus exact minor units", top.String())
	}

	ninety, err := billingdomain.NewMoney(990, billingdomain.CurrencyBRL)
	if err != nil {
		t.Fatalf("NewMoney(990) error = %v", err)
	}
	if ninety.String() != "BRL 990" || ninety.MinorUnits() != 990 {
		t.Errorf("990 minor units rendered as %q/%d", ninety.String(), ninety.MinorUnits())
	}
}

// TestBenefitPeriodIgnoresDSTAndOffset proves the billed benefit period
// is an instant interval, not wall-clock text: the same epoch instant in
// São Paulo, UTC and +05:30 yields one identical UTC period, and a period
// spanning the US spring-forward transition keeps its exact elapsed
// duration instead of a 24-hour calendar assumption.
func TestBenefitPeriodIgnoresDSTAndOffset(t *testing.T) {
	t.Parallel()

	instant := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	end := instant.Add(30 * 24 * time.Hour)
	saoPaulo := instant.In(time.FixedZone("BRT", -3*60*60))
	kolkata := instant.In(time.FixedZone("IST", 5*60*60+30*60))

	reference, err := billingdomain.NewBillingPeriod(instant, end)
	if err != nil {
		t.Fatalf("NewBillingPeriod error = %v", err)
	}
	for name, start := range map[string]time.Time{"utc": instant, "sao_paulo": saoPaulo, "kolkata": kolkata} {
		period, err := billingdomain.NewBillingPeriod(start, end)
		if err != nil {
			t.Fatalf("%s start error = %v", name, err)
		}
		if !period.Start().Equal(reference.Start()) || !period.End().Equal(reference.End()) {
			t.Errorf("%s period = %s, want %s", name, period.String(), reference.String())
		}
		if period.Start().Location() != time.UTC || period.End().Location() != time.UTC {
			t.Errorf("%s bounds must normalize to UTC", name)
		}
	}

	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation(America/New_York): %v", err)
	}
	// Spring forward 2026: 2026-03-08 02:00 EST becomes 03:00 EDT, so
	// midnight-to-midnight wall clock is 23 elapsed hours.
	before := time.Date(2026, time.March, 8, 0, 0, 0, 0, newYork)
	after := time.Date(2026, time.March, 9, 0, 0, 0, 0, newYork)
	transition, err := billingdomain.NewBillingPeriod(before, after)
	if err != nil {
		t.Fatalf("transition period error = %v", err)
	}
	if elapsed := transition.End().Sub(transition.Start()); elapsed != 23*time.Hour {
		t.Fatalf("transition elapsed = %v, want exactly 23h", elapsed)
	}
	// The same epoch instants stated in UTC are the same benefit period.
	utcTwin, err := billingdomain.NewBillingPeriod(before.UTC(), after.UTC())
	if err != nil {
		t.Fatalf("UTC twin error = %v", err)
	}
	if !transition.Start().Equal(utcTwin.Start()) || !transition.End().Equal(utcTwin.End()) {
		t.Fatalf("zone-stated %s and UTC-stated %s diverge", transition.String(), utcTwin.String())
	}
}

// TestPresentationSnapshotsPtEn approves the pt/en presentation
// snapshots for fixed UTC instants: dates travel as RFC 3339 UTC with a
// timezone label, and the sentence shape per locale is pinned.
func TestPresentationSnapshotsPtEn(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"start":    "2026-09-01T00:00:00Z",
		"end":      "2026-10-01T00:00:00Z",
		"timezone": "America/Sao_Paulo",
	}
	want := map[string]string{
		"pt-BR": "Período: 2026-09-01T00:00:00Z – 2026-10-01T00:00:00Z (America/Sao_Paulo)",
		"en-US": "Period: 2026-09-01T00:00:00Z – 2026-10-01T00:00:00Z (America/Sao_Paulo)",
	}
	for _, locale := range []string{"pt-BR", "en-US"} {
		got, err := i18n.Format(locale, "transparency.document.period", values)
		if err != nil {
			t.Fatalf("Format(%s) error = %v", locale, err)
		}
		if got != want[locale] {
			t.Errorf("Format(%s) = %q, want snapshot %q", locale, got, want[locale])
		}
	}

	updated, err := i18n.Format("pt-BR", "transparency.document.updated", map[string]string{
		"at":      "2026-09-28T16:00:00Z",
		"version": "3",
	})
	if err != nil {
		t.Fatalf("Format(updated) error = %v", err)
	}
	if updated != "Atualizado em 2026-09-28T16:00:00Z · metodologia v3" {
		t.Errorf("updated snapshot = %q", updated)
	}
}
