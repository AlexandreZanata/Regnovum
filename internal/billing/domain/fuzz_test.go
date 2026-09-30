package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// Seed rationale (P24-T02): the currency vocabulary is the pt/en market pair
// (BRL for the BR market, USD for INTERNATIONAL), plus hostile Unicode
// (bidi override, NUL, flag emoji, raw invalid bytes), boundary lengths and
// the unknown-code limit cases. A panic, a non-canonical acceptance or a
// non-deterministic parse persists corpus by the native Go fuzz contract.

// FuzzParseCurrency proves the money vocabulary never panics on arbitrary
// input and that every accepted code is canonical: upper-cased, trimmed and
// idempotent under re-parsing.
func FuzzParseCurrency(f *testing.F) {
	f.Add("BRL")
	f.Add("USD")
	f.Add("brl")
	f.Add(" usd ")
	f.Add("BRL ")
	f.Add("EUR")
	f.Add("")
	f.Add("   ")
	f.Add("JPY")
	f.Add("br")
	f.Add("BRLBRL")
	f.Add(strings.Repeat("BRL", 100))
	f.Add("BR\u202eL")
	f.Add("BRL\x00")
	f.Add("BR🇧🇷L")
	f.Add("b r l")
	f.Add("US$")
	f.Add(string([]byte{0xff, 0xfe}))

	f.Fuzz(func(t *testing.T, raw string) {
		first, err := domain.ParseCurrency(raw)
		if err != nil {
			if first.IsValid() {
				t.Fatalf("invalid currency %q parsed as valid %q", raw, first.String())
			}
			return
		}
		if !first.IsValid() {
			t.Fatalf("accepted currency %q is not valid", raw)
		}
		if first.String() != "BRL" && first.String() != "USD" {
			t.Fatalf("accepted currency %q is outside the closed vocabulary", first.String())
		}

		// Normalization is canonical and deterministic: re-parsing the
		// canonical form succeeds and parsing twice agrees.
		reparsed, err := domain.ParseCurrency(first.String())
		if err != nil {
			t.Fatalf("re-parsing canonical %q failed: %v", first.String(), err)
		}
		if reparsed != first {
			t.Fatalf("currency is not canonical: %q vs %q", reparsed.String(), first.String())
		}
		again, err := domain.ParseCurrency(raw)
		if err != nil || again != first {
			t.Fatalf("currency parsing is not deterministic for %q", raw)
		}
	})
}

// FuzzMoneyInvariants proves exact money never panics, never carries a
// negative amount or an unsupported currency, and preserves the exact minor
// units: integers only, never float.
func FuzzMoneyInvariants(f *testing.F) {
	f.Add(int64(990), "BRL")
	f.Add(int64(0), "USD")
	f.Add(int64(1), "BRL")
	f.Add(int64(-1), "BRL")
	f.Add(int64(9223372036854775807), "USD")
	f.Add(int64(100), "EUR")
	f.Add(int64(50), "")
	f.Add(int64(10), "BR\u202eL")

	f.Fuzz(func(t *testing.T, minorUnits int64, currencyRaw string) {
		currency, err := domain.ParseCurrency(currencyRaw)
		if err != nil {
			return
		}
		money, err := domain.NewMoney(minorUnits, currency)
		if minorUnits < 0 {
			if err == nil {
				t.Fatalf("negative amount %d accepted in %q", minorUnits, currency.String())
			}
			return
		}
		if err != nil {
			t.Fatalf("NewMoney(%d, %q) failed: %v", minorUnits, currency.String(), err)
		}
		if money.MinorUnits() != minorUnits {
			t.Fatalf("minor units = %d, want %d", money.MinorUnits(), minorUnits)
		}
		if money.Currency() != currency {
			t.Fatalf("currency = %q, want %q", money.Currency().String(), currency.String())
		}
		if money.IsZero() != (minorUnits == 0) {
			t.Fatalf("IsZero = %v for amount %d", money.IsZero(), minorUnits)
		}
		if !money.Equals(money) {
			t.Fatalf("money is not equal to itself: %q", money.String())
		}

		// Exactness is deterministic: building twice agrees.
		again, err := domain.NewMoney(minorUnits, currency)
		if err != nil || !again.Equals(money) {
			t.Fatalf("money construction is not deterministic for (%d, %q)", minorUnits, currency.String())
		}
	})
}

// FuzzBillingPeriod proves period construction never panics on arbitrary
// unix instants and that every accepted period advances in time, is pinned
// to UTC and rebuilds deterministically from its own bounds.
func FuzzBillingPeriod(f *testing.F) {
	f.Add(int64(1758000000), int64(1758003600))
	f.Add(int64(1758000000), int64(1758000000))
	f.Add(int64(1758003600), int64(1758000000))
	f.Add(int64(0), int64(1))
	f.Add(int64(-62135596800), int64(-62135596799))
	f.Add(int64(4102444800), int64(4102448400))
	f.Add(int64(9223372036854775807), int64(9223372036854775807))

	f.Fuzz(func(t *testing.T, startUnix, endUnix int64) {
		start := time.Unix(startUnix, 0)
		end := time.Unix(endUnix, 0)
		period, err := domain.NewBillingPeriod(start, end)
		if start.IsZero() || end.IsZero() || !end.After(start) {
			if err == nil {
				t.Fatalf("incomplete period %v..%v accepted", start, end)
			}
			return
		}
		if err != nil {
			t.Fatalf("NewBillingPeriod(%v, %v) failed: %v", start, end, err)
		}
		if period.IsZero() {
			t.Fatal("accepted period reports zero")
		}
		if period.Start().Location() != time.UTC || period.End().Location() != time.UTC {
			t.Fatal("accepted period is not pinned to UTC")
		}
		if !period.End().After(period.Start()) {
			t.Fatal("accepted period does not advance")
		}

		// Bounds are canonical and deterministic: rebuilding from the
		// reported bounds succeeds and agrees.
		rebuilt, err := domain.NewBillingPeriod(period.Start(), period.End())
		if err != nil {
			t.Fatalf("rebuilding from reported bounds failed: %v", err)
		}
		if !rebuilt.Start().Equal(period.Start()) || !rebuilt.End().Equal(period.End()) {
			t.Fatal("period bounds are not stable under rebuilding")
		}
	})
}
