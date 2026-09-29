package domain_test

import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// Seed rationale (P32-T01): the decimal vocabulary is the pt/en pair with
// the Genesis supply in both forms, plus hostile input (signs, exponents,
// grouping marks, over-precision, NUL, bidi override, emoji, fullwidth
// digits, raw invalid bytes, huge digit runs) and the locale boundary
// (unknown, empty, mixed tags). A panic, an accepted non-canonical value
// or a broken round-trip persists corpus by the native Go fuzz contract.

// FuzzParseDecimal proves decimal text never panics and every accepted
// value is exact: it re-renders in the same locale and parses back to the
// identical millis, so no subunit is gained or rounded away.
func FuzzParseDecimal(f *testing.F) {
	f.Add("0,000", "pt")
	f.Add("1,5", "pt")
	f.Add("2100000000,000", "pt-BR")
	f.Add("0.000", "en")
	f.Add("1.5", "en")
	f.Add("2100000000.000", "en-US")
	f.Add("1.000", "en")
	f.Add("1.000", "pt")
	f.Add("1,0000", "pt")
	f.Add("-1,5", "pt")
	f.Add("", "pt")
	f.Add("abc", "en")
	f.Add("1,5", "fr")
	f.Add("1e3", "en")
	f.Add("1,5 ", "pt")
	f.Add(" 1.5", "en")
	f.Add("NaN", "en")
	f.Add("1\x005", "en")
	f.Add("1\u202e,5", "pt")
	f.Add("1😀,5", "pt")
	f.Add("１,５", "pt")
	f.Add(string([]byte{0xff, 0xfe}), "en")
	f.Add(strings.Repeat("9", 30), "en")
	f.Add(strings.Repeat("9", 30)+",000", "pt")

	f.Fuzz(func(t *testing.T, raw, locale string) {
		first, err := domain.ParseDecimal(raw, locale)
		if err != nil {
			return
		}
		if first.Millis() < 0 {
			t.Fatalf("accepted %q (%s) with negative millis %d", raw, locale, first.Millis())
		}
		rendered, err := first.FormatDecimal(locale)
		if err != nil {
			t.Fatalf("accepted %q (%s) cannot re-render: %v", raw, locale, err)
		}
		second, err := domain.ParseDecimal(rendered, locale)
		if err != nil {
			t.Fatalf("re-parsing %q (%s) failed: %v", rendered, locale, err)
		}
		if !second.Equals(first) {
			t.Fatalf("round-trip of %q (%s) moved %d to %d", raw, locale, first.Millis(), second.Millis())
		}
	})
}

// FuzzParseCanonical proves the millis text never panics and every
// accepted value is canonical: re-rendering the millis reproduces the
// input bytes, so the ledger form has exactly one spelling.
func FuzzParseCanonical(f *testing.F) {
	f.Add("0")
	f.Add("1")
	f.Add("2100000000000")
	f.Add("9223372036854775807")
	f.Add("")
	f.Add("-1")
	f.Add("1.5")
	f.Add("1,5")
	f.Add(" 1")
	f.Add("0x10")
	f.Add("1e3")
	f.Add("９９９")
	f.Add("1\x00")
	f.Add(strings.Repeat("9", 30))

	f.Fuzz(func(t *testing.T, raw string) {
		first, err := domain.Parse(raw)
		if err != nil {
			return
		}
		if first.String() != raw {
			t.Fatalf("accepted %q renders as %q: two spellings for one amount", raw, first.String())
		}
	})
}
