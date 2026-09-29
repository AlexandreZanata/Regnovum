package domain

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

const (
	// MillisPerInk fixes the Genesis subunit: 1 INK is exactly 1000
	// milliINK, indivisible (docs/reino/RESPOSTAS.md item 21, proposta).
	MillisPerInk int64 = 1000

	// GenesisSupplyInk is the single creation event: 2.100.000.000 INK
	// born whole in the Treasury (item 20, proposta).
	GenesisSupplyInk int64 = 2100000000

	// GenesisSupplyMillis is the same supply counted in subunits.
	GenesisSupplyMillis int64 = 2100000000000

	// fractionDigits is the exact decimal width of one INK.
	fractionDigits = 3
)

// MilliInk is an immutable, non-negative quantity in milliINK. The ledger
// counts integers only (AGENTS.md: minor units, never float): every balance,
// price and fee is an exact signed 64-bit integer, matching the bigint
// columns the P32 schema will provide. The canonical text form is the plain
// millis integer, which is valid JSON number syntax by construction, so
// adapters transport it without ever touching float64.
type MilliInk struct {
	millis int64
}

// NewMilliInk builds a quantity from exact subunits. Negative inputs are
// invalid: debits are expressed by the transfer direction, never by a
// negative balance.
func NewMilliInk(millis int64) (MilliInk, error) {
	if millis < 0 {
		return MilliInk{}, ErrNegativeMilliInk
	}
	return MilliInk{millis: millis}, nil
}

// FromInk converts whole INK to subunits, refusing the conversion that
// would cross the 64-bit ceiling instead of wrapping it.
func FromInk(ink int64) (MilliInk, error) {
	if ink < 0 {
		return MilliInk{}, ErrNegativeMilliInk
	}
	if ink > math.MaxInt64/MillisPerInk {
		return MilliInk{}, ErrMilliInkOverflow
	}
	return MilliInk{millis: ink * MillisPerInk}, nil
}

// GenesisSupply returns the fixed supply S. It cannot fail: the product is
// a verified constant (GenesisSupplyInk * MillisPerInk fits int64).
func GenesisSupply() MilliInk {
	return MilliInk{millis: GenesisSupplyMillis}
}

// Parse reads the canonical millis text: ASCII digits only, no sign, no
// whitespace, no separators, no exponent, and no leading zeros (the
// canonical form has exactly one spelling per amount). Anything a float
// could hide behind is refused; values beyond int64 fail with
// ErrMilliInkOverflow.
func Parse(raw string) (MilliInk, error) {
	if raw == "" {
		return MilliInk{}, ErrInvalidMilliInk
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return MilliInk{}, ErrInvalidMilliInk
		}
	}
	if len(raw) > 1 && raw[0] == '0' {
		return MilliInk{}, ErrInvalidMilliInk
	}
	millis, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return MilliInk{}, ErrMilliInkOverflow
		}
		return MilliInk{}, ErrInvalidMilliInk
	}
	return MilliInk{millis: millis}, nil
}

// decimalSeparator resolves the only accepted decimal mark per locale:
// comma for pt, point for en. Grouping marks are never accepted: "1.000"
// reads as one INK in en and as a grouped thousand in pt, so accepting it
// would let the same text mean two amounts. Locales outside pt/en are
// refused instead of guessed.
func decimalSeparator(locale string) (byte, error) {
	folded := strings.ToLower(strings.TrimSpace(locale))
	switch {
	case folded == "pt" || strings.HasPrefix(folded, "pt-") || strings.HasPrefix(folded, "pt_"):
		return ',', nil
	case folded == "en" || strings.HasPrefix(folded, "en-") || strings.HasPrefix(folded, "en_"):
		return '.', nil
	default:
		return 0, ErrUnknownLocale
	}
}

// allDigits reports whether text holds at least one ASCII digit and nothing
// else. Byte-wise iteration keeps multibyte runes out by construction.
func allDigits(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

// ParseDecimal reads human INK text ("1,5" in pt, "1.5" in en) into exact
// subunits. At most three fraction digits are accepted; a fourth digit is
// refused with ErrMilliInkPrecision instead of being rounded, so no price
// can gain or lose a subunit silently.
func ParseDecimal(raw, locale string) (MilliInk, error) {
	separator, err := decimalSeparator(locale)
	if err != nil {
		return MilliInk{}, err
	}
	if raw == "" {
		return MilliInk{}, ErrInvalidMilliInk
	}
	integer, fraction, found := strings.Cut(raw, string(separator))
	if !allDigits(integer) {
		return MilliInk{}, ErrInvalidMilliInk
	}
	scaled := int64(0)
	if found {
		if fraction == "" || len(fraction) > fractionDigits {
			if fraction != "" && allDigits(fraction) {
				return MilliInk{}, ErrMilliInkPrecision
			}
			return MilliInk{}, ErrInvalidMilliInk
		}
		if !allDigits(fraction) {
			return MilliInk{}, ErrInvalidMilliInk
		}
		for len(fraction) < fractionDigits {
			fraction += "0"
		}
		scaled, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return MilliInk{}, ErrInvalidMilliInk
		}
	}
	whole, err := strconv.ParseInt(integer, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return MilliInk{}, ErrMilliInkOverflow
		}
		return MilliInk{}, ErrInvalidMilliInk
	}
	if whole > (math.MaxInt64-scaled)/MillisPerInk {
		return MilliInk{}, ErrMilliInkOverflow
	}
	return MilliInk{millis: whole*MillisPerInk + scaled}, nil
}

// Add returns the exact sum; crossing the ceiling fails instead of wrapping.
func (m MilliInk) Add(other MilliInk) (MilliInk, error) {
	if other.millis > math.MaxInt64-m.millis {
		return MilliInk{}, ErrMilliInkOverflow
	}
	return MilliInk{millis: m.millis + other.millis}, nil
}

// Sub returns the exact difference; it fails instead of producing a
// negative quantity (custodies never go below zero).
func (m MilliInk) Sub(other MilliInk) (MilliInk, error) {
	if other.millis > m.millis {
		return MilliInk{}, ErrInsufficientMilliInk
	}
	return MilliInk{millis: m.millis - other.millis}, nil
}

// Millis returns the exact subunit count. MilliInk never passes through
// float64.
func (m MilliInk) Millis() int64 {
	return m.millis
}

// String renders the canonical millis form (digits only).
func (m MilliInk) String() string {
	return strconv.FormatInt(m.millis, 10)
}

// FormatDecimal renders exact INK text for the locale with the full three
// fraction digits and no grouping: "1500" millis become "1,500" in pt and
// "1.500" in en. Grouping stays presentation-only so parsing the output
// back can never misread it.
func (m MilliInk) FormatDecimal(locale string) (string, error) {
	separator, err := decimalSeparator(locale)
	if err != nil {
		return "", err
	}
	whole := m.millis / MillisPerInk
	rest := m.millis % MillisPerInk
	return strconv.FormatInt(whole, 10) + string(separator) + threeDigits(rest), nil
}

// threeDigits renders a 0-999 remainder as exactly three ASCII digits.
func threeDigits(rest int64) string {
	out := strconv.FormatInt(rest, 10)
	for len(out) < fractionDigits {
		out = "0" + out
	}
	return out
}

// IsZero reports whether the quantity is exactly zero.
func (m MilliInk) IsZero() bool {
	return m.millis == 0
}

// Equals reports whether two quantities are exactly equal.
func (m MilliInk) Equals(other MilliInk) bool {
	return m.millis == other.millis
}
