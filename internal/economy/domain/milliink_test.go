package domain_test

import (
	"errors"
	"math"
	"regexp"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func mustMilliInk(t *testing.T, millis int64) domain.MilliInk {
	t.Helper()
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		t.Fatalf("NewMilliInk(%d): %v", millis, err)
	}
	return amount
}

func TestNewMilliInkBoundaries(t *testing.T) {
	t.Parallel()

	for _, millis := range []int64{0, 1, 1000, 2100000000000, math.MaxInt64} {
		amount, err := domain.NewMilliInk(millis)
		if err != nil {
			t.Errorf("NewMilliInk(%d): %v", millis, err)
			continue
		}
		if amount.Millis() != millis {
			t.Errorf("NewMilliInk(%d).Millis() = %d", millis, amount.Millis())
		}
	}
	if _, err := domain.NewMilliInk(-1); !errors.Is(err, domain.ErrNegativeMilliInk) {
		t.Errorf("NewMilliInk(-1) = %v, want ErrNegativeMilliInk", err)
	}
	if _, err := domain.NewMilliInk(math.MinInt64); !errors.Is(err, domain.ErrNegativeMilliInk) {
		t.Errorf("NewMilliInk(MinInt64) = %v, want ErrNegativeMilliInk", err)
	}
}

func TestFromInkAndGenesisSupply(t *testing.T) {
	t.Parallel()

	one, err := domain.FromInk(1)
	if err != nil {
		t.Fatalf("FromInk(1): %v", err)
	}
	if one.Millis() != 1000 {
		t.Fatalf("FromInk(1).Millis() = %d, want 1000", one.Millis())
	}
	supply := domain.GenesisSupply()
	if supply.Millis() != 2100000000000 {
		t.Fatalf("GenesisSupply().Millis() = %d, want 2100000000000", supply.Millis())
	}
	fromSupply, err := domain.FromInk(2100000000)
	if err != nil {
		t.Fatalf("FromInk(2100000000): %v", err)
	}
	if !fromSupply.Equals(supply) {
		t.Fatalf("FromInk(2100000000) != GenesisSupply()")
	}
	if _, err := domain.FromInk(-1); !errors.Is(err, domain.ErrNegativeMilliInk) {
		t.Errorf("FromInk(-1) = %v, want ErrNegativeMilliInk", err)
	}
	if _, err := domain.FromInk(math.MaxInt64/1000 + 1); !errors.Is(err, domain.ErrMilliInkOverflow) {
		t.Errorf("FromInk exceeding int64 millis = %v, want ErrMilliInkOverflow", err)
	}
}

func TestAddSubChecked(t *testing.T) {
	t.Parallel()

	sum, err := mustMilliInk(t, 1500).Add(mustMilliInk(t, 600))
	if err != nil || sum.Millis() != 2100 {
		t.Fatalf("1500 + 600 = %v, %v; want 2100, nil", sum.Millis(), err)
	}
	if _, err := mustMilliInk(t, math.MaxInt64).Add(mustMilliInk(t, 1)); !errors.Is(err, domain.ErrMilliInkOverflow) {
		t.Errorf("MaxInt64 + 1 = %v, want ErrMilliInkOverflow", err)
	}
	left, err := mustMilliInk(t, 2100).Sub(mustMilliInk(t, 600))
	if err != nil || left.Millis() != 1500 {
		t.Fatalf("2100 - 600 = %v, %v; want 1500, nil", left.Millis(), err)
	}
	if _, err := mustMilliInk(t, 599).Sub(mustMilliInk(t, 600)); !errors.Is(err, domain.ErrInsufficientMilliInk) {
		t.Errorf("599 - 600 = %v, want ErrInsufficientMilliInk", err)
	}
	if !mustMilliInk(t, 0).IsZero() || mustMilliInk(t, 1).IsZero() {
		t.Errorf("IsZero disagrees with the millis count")
	}
	if !mustMilliInk(t, 1000).Equals(mustMilliInk(t, 1000)) || mustMilliInk(t, 1000).Equals(mustMilliInk(t, 1001)) {
		t.Errorf("Equals disagrees with the millis count")
	}
}

func TestParseCanonicalMillis(t *testing.T) {
	t.Parallel()

	valid := map[string]int64{
		"0":                   0,
		"1":                   1,
		"1000":                1000,
		"2100000000000":       2100000000000,
		"9223372036854775807": math.MaxInt64,
	}
	for raw, want := range valid {
		amount, err := domain.Parse(raw)
		if err != nil {
			t.Errorf("Parse(%q): %v", raw, err)
			continue
		}
		if amount.Millis() != want {
			t.Errorf("Parse(%q).Millis() = %d, want %d", raw, amount.Millis(), want)
		}
		if amount.String() != raw {
			t.Errorf("Parse(%q).String() = %q, want canonical %q", raw, amount.String(), raw)
		}
	}

	invalid := []string{"", " ", "1 ", " 1", "+1", "-1", "1.5", "1,5", "1e3", "0x10", "NaN", "１２３", "1_000", "00", "01", "007"}
	for _, raw := range invalid {
		if _, err := domain.Parse(raw); !errors.Is(err, domain.ErrInvalidMilliInk) {
			t.Errorf("Parse(%q) = %v, want ErrInvalidMilliInk", raw, err)
		}
	}
	if _, err := domain.Parse("9223372036854775808"); !errors.Is(err, domain.ErrMilliInkOverflow) {
		t.Errorf("Parse past MaxInt64 = %v, want ErrMilliInkOverflow", err)
	}
}

func TestParseDecimalLocales(t *testing.T) {
	t.Parallel()

	valid := []struct {
		raw    string
		locale string
		want   int64
	}{
		{"0,000", "pt", 0},
		{"1,5", "pt", 1500},
		{"1,500", "pt-BR", 1500},
		{"2100000000,000", "pt", 2100000000000},
		{"0.000", "en", 0},
		{"1.5", "en", 1500},
		{"1.500", "en-US", 1500},
		{"1.000", "en", 1000},
		{"2100000000.000", "en", 2100000000000},
	}
	for _, test := range valid {
		amount, err := domain.ParseDecimal(test.raw, test.locale)
		if err != nil {
			t.Errorf("ParseDecimal(%q, %q): %v", test.raw, test.locale, err)
			continue
		}
		if amount.Millis() != test.want {
			t.Errorf("ParseDecimal(%q, %q).Millis() = %d, want %d", test.raw, test.locale, amount.Millis(), test.want)
		}
	}

	rejected := []struct {
		raw    string
		locale string
		err    error
	}{
		{"1.000", "pt", domain.ErrInvalidMilliInk},
		{"1,000", "en", domain.ErrInvalidMilliInk},
		{"1,0000", "pt", domain.ErrMilliInkPrecision},
		{"1.0000", "en", domain.ErrMilliInkPrecision},
		{"-1,5", "pt", domain.ErrInvalidMilliInk},
		{"+1.5", "en", domain.ErrInvalidMilliInk},
		{"5,", "pt", domain.ErrInvalidMilliInk},
		{"", "pt", domain.ErrInvalidMilliInk},
		{"abc", "en", domain.ErrInvalidMilliInk},
		{"1,5", "fr", domain.ErrUnknownLocale},
		{"1.5", "", domain.ErrUnknownLocale},
		{"1e3", "en", domain.ErrInvalidMilliInk},
	}
	for _, test := range rejected {
		if _, err := domain.ParseDecimal(test.raw, test.locale); !errors.Is(err, test.err) {
			t.Errorf("ParseDecimal(%q, %q) = %v, want %v", test.raw, test.locale, err, test.err)
		}
	}
}

func TestFormatDecimalRoundTripPtEn(t *testing.T) {
	t.Parallel()

	for _, millis := range []int64{0, 1, 999, 1000, 1500, 2100000000000, math.MaxInt64} {
		amount := mustMilliInk(t, millis)
		rendered, err := amount.FormatDecimal("pt")
		if err != nil {
			t.Fatalf("FormatDecimal(%d, pt): %v", millis, err)
		}
		back, err := domain.ParseDecimal(rendered, "pt")
		if err != nil || !back.Equals(amount) {
			t.Errorf("pt round-trip of %d via %q: %v, %v", millis, rendered, back.Millis(), err)
		}

		rendered, err = amount.FormatDecimal("en")
		if err != nil {
			t.Fatalf("FormatDecimal(%d, en): %v", millis, err)
		}
		back, err = domain.ParseDecimal(rendered, "en")
		if err != nil || !back.Equals(amount) {
			t.Errorf("en round-trip of %d via %q: %v, %v", millis, rendered, back.Millis(), err)
		}
	}

	thousand, _ := domain.FromInk(1)
	if text, _ := thousand.FormatDecimal("pt"); text != "1,000" {
		t.Errorf("FromInk(1) pt = %q, want %q", text, "1,000")
	}
	if text, _ := thousand.FormatDecimal("en"); text != "1.000" {
		t.Errorf("FromInk(1) en = %q, want %q", text, "1.000")
	}
	if _, err := mustMilliInk(t, 1).FormatDecimal("fr"); !errors.Is(err, domain.ErrUnknownLocale) {
		t.Errorf("FormatDecimal(fr) = %v, want ErrUnknownLocale", err)
	}
}

func TestCanonicalTextIsJSONNumber(t *testing.T) {
	t.Parallel()

	shape := regexp.MustCompile(`^[0-9]+$`)
	for _, millis := range []int64{0, 1, 1000, 2100000000000, math.MaxInt64} {
		text := mustMilliInk(t, millis).String()
		if !shape.MatchString(text) {
			t.Errorf("String() of %d is %q: not a JSON number without float syntax", millis, text)
		}
		back, err := domain.Parse(text)
		if err != nil || back.Millis() != millis {
			t.Errorf("String()/Parse round-trip of %d failed: %v", millis, err)
		}
	}
}
