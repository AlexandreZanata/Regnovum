package domain

// TermsLocale is the closed vocabulary of interface locales the
// purchase terms render in: Portuguese and English. Amounts travel as
// canonical integers either way; the locale only selects the titles,
// never the values.
type TermsLocale string

const (
	// TermsLocalePortuguese renders the document titles in Portuguese.
	TermsLocalePortuguese TermsLocale = "pt"
	// TermsLocaleEnglish renders the document titles in English.
	TermsLocaleEnglish TermsLocale = "en"
)

// AllTermsLocales returns the closed vocabulary in canonical order.
func AllTermsLocales() []TermsLocale {
	return []TermsLocale{TermsLocalePortuguese, TermsLocaleEnglish}
}

// ParseTermsLocale validates a locale against the closed vocabulary.
// Matching is exact: surrounding whitespace is not trimmed and case
// is not folded. Unknown locales refuse before any term is priced.
func ParseTermsLocale(raw string) (TermsLocale, error) {
	locale := TermsLocale(raw)
	if !locale.IsValid() {
		return "", ErrInvalidTerms
	}
	return locale, nil
}

// IsValid reports whether the locale belongs to the closed vocabulary.
func (l TermsLocale) IsValid() bool {
	switch l {
	case TermsLocalePortuguese, TermsLocaleEnglish:
		return true
	default:
		return false
	}
}

// String returns the stored locale value.
func (l TermsLocale) String() string { return string(l) }

// TermsTitles carries every human string the terms document may hold
// in one locale. No amount, identifier or secret ever enters this
// shape — only these dictionary values reach the buyer.
type TermsTitles struct {
	Title     string
	FiatGross string
	Fee       string
	Tax       string
	FiatNet   string
	Price     string
	Ink       string
	Dust      string
}

// TermsTitlesFor renders the closed title dictionary in one locale.
// The values translate; the canonical amounts never do.
func TermsTitlesFor(locale TermsLocale) TermsTitles {
	if locale == TermsLocaleEnglish {
		return TermsTitles{
			Title:     "INK purchase terms",
			FiatGross: "Gross fiat (BRL minor)",
			Fee:       "Service fee (BRL minor)",
			Tax:       "Tax (BRL minor)",
			FiatNet:   "Net fiat (BRL minor)",
			Price:     "Asked price (BRL minor per BTC)",
			Ink:       "INK (milliINK)",
			Dust:      "Dust below step (milliINK)",
		}
	}
	return TermsTitles{
		Title:     "Termos de compra de INK",
		FiatGross: "Bruto em fiat (centavos de BRL)",
		Fee:       "Tarifa (centavos de BRL)",
		Tax:       "Imposto (centavos de BRL)",
		FiatNet:   "Líquido em fiat (centavos de BRL)",
		Price:     "Preço pedido (centavos de BRL por BTC)",
		Ink:       "INK (milliINK)",
		Dust:      "Poeira abaixo do passo (milliINK)",
	}
}
