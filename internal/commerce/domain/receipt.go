package domain

// ReceiptLocale is the closed vocabulary of interface locales the
// trade receipt display renders in: Portuguese and English. Amounts
// travel as canonical integers either way; the locale only selects
// the titles, never the values.
type ReceiptLocale string

const (
	// ReceiptLocalePortuguese renders the receipt titles in Portuguese.
	ReceiptLocalePortuguese ReceiptLocale = "pt"
	// ReceiptLocaleEnglish renders the receipt titles in English.
	ReceiptLocaleEnglish ReceiptLocale = "en"
)

// AllReceiptLocales returns the closed vocabulary in canonical order.
func AllReceiptLocales() []ReceiptLocale {
	return []ReceiptLocale{ReceiptLocalePortuguese, ReceiptLocaleEnglish}
}

// ParseReceiptLocale validates a locale against the closed vocabulary.
// Matching is exact: surrounding whitespace is not trimmed and case
// is not folded. Unknown locales refuse before any receipt is read
// or displayed.
func ParseReceiptLocale(raw string) (ReceiptLocale, error) {
	locale := ReceiptLocale(raw)
	if !locale.IsValid() {
		return "", ErrInvalidReceipt
	}
	return locale, nil
}

// IsValid reports whether the locale belongs to the closed vocabulary.
func (l ReceiptLocale) IsValid() bool {
	switch l {
	case ReceiptLocalePortuguese, ReceiptLocaleEnglish:
		return true
	default:
		return false
	}
}

// String returns the stored locale value.
func (l ReceiptLocale) String() string { return string(l) }

// ReceiptTitles carries every human string the trade receipt display
// may hold in one locale. No amount, identifier or secret ever enters
// this shape — only these dictionary values reach the reader.
type ReceiptTitles struct {
	Title   string
	Gross   string
	Tithe   string
	Net     string
	Status  string
	Posted  string
	Balance string
}

// ReceiptTitlesFor renders the closed title dictionary in one locale.
// The values translate; the canonical amounts never do.
func ReceiptTitlesFor(locale ReceiptLocale) ReceiptTitles {
	if locale == ReceiptLocaleEnglish {
		return ReceiptTitles{
			Title:   "Trade receipt",
			Gross:   "Gross (milliINK)",
			Tithe:   "Tithe (milliINK)",
			Net:     "Net to provider (milliINK)",
			Status:  "Escrow status",
			Posted:  "Posted (UTC)",
			Balance: "Refunded (milliINK)",
		}
	}
	return ReceiptTitles{
		Title:   "Recibo de comércio",
		Gross:   "Bruto (milliINK)",
		Tithe:   "Dízimo (milliINK)",
		Net:     "Líquido ao prestador (milliINK)",
		Status:  "Estado do escrow",
		Posted:  "Publicado (UTC)",
		Balance: "Reembolsado (milliINK)",
	}
}
