package domain

// PriceLocale is the closed vocabulary of interface locales the
// metering price display renders in: Portuguese and English.
// Amounts travel as canonical integers either way; the locale only
// selects the titles, never the values.
type PriceLocale string

const (
	// PriceLocalePortuguese renders the price titles in Portuguese.
	PriceLocalePortuguese PriceLocale = "pt"
	// PriceLocaleEnglish renders the price titles in English.
	PriceLocaleEnglish PriceLocale = "en"
)

// AllPriceLocales returns the closed vocabulary in canonical order.
func AllPriceLocales() []PriceLocale {
	return []PriceLocale{PriceLocalePortuguese, PriceLocaleEnglish}
}

// ParsePriceLocale validates a locale against the closed vocabulary.
// Matching is exact: surrounding whitespace is not trimmed and case
// is not folded. Unknown locales refuse before any price is read or
// displayed.
func ParsePriceLocale(raw string) (PriceLocale, error) {
	locale := PriceLocale(raw)
	if !locale.IsValid() {
		return "", ErrUnknownPriceLocale
	}
	return locale, nil
}

// IsValid reports whether the locale belongs to the closed vocabulary.
func (l PriceLocale) IsValid() bool {
	switch l {
	case PriceLocalePortuguese, PriceLocaleEnglish:
		return true
	default:
		return false
	}
}

// String returns the stored locale value.
func (l PriceLocale) String() string { return string(l) }

// PriceTitles carries every human string the price display may hold
// in one locale. No amount, identifier or secret ever enters this
// shape — only these dictionary values reach the reader.
type PriceTitles struct {
	Title    string
	Service  string
	Price    string
	Validity string
}

// PriceTitlesFor renders the closed title dictionary in one locale.
// The values translate; the canonical amounts never do.
func PriceTitlesFor(locale PriceLocale) PriceTitles {
	if locale == PriceLocaleEnglish {
		return PriceTitles{
			Title:    "INK metering price",
			Service:  "Service",
			Price:    "Price (milliINK per unit)",
			Validity: "Validity (UTC)",
		}
	}
	return PriceTitles{
		Title:    "Preço de medição de INK",
		Service:  "Serviço",
		Price:    "Preço (milliINK por unidade)",
		Validity: "Vigência (UTC)",
	}
}
