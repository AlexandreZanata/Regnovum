package domain

// TotalsCacheSeconds is the public cache lifetime of the custody
// totals document in seconds: one hour, mirroring the transparency
// metrics window, so readers share one derivation per period instead
// of recomputing the journal on every look.
const TotalsCacheSeconds = 3600

// TotalsLowCount bounds reidentification from small third-party cells,
// mirroring the transparency low-count rule: a third-party aggregate
// over fewer custodies or holds reports zero instead of the exact
// amount. The rule applies uniformly, and sovereign classes (supply,
// Treasury, vaults) never suppress: they name the Treasury itself,
// never a person.
const TotalsLowCount = 5

// TotalsLocale is the closed vocabulary of interface locales the
// public totals document renders in: Portuguese and English. Amounts
// travel as integer millis either way; the locale only selects the
// section titles, never the numbers.
type TotalsLocale string

const (
	// TotalsLocalePortuguese renders the document titles in Portuguese.
	TotalsLocalePortuguese TotalsLocale = "pt"
	// TotalsLocaleEnglish renders the document titles in English.
	TotalsLocaleEnglish TotalsLocale = "en"
)

// AllTotalsLocales returns the closed vocabulary in canonical order.
func AllTotalsLocales() []TotalsLocale {
	return []TotalsLocale{TotalsLocalePortuguese, TotalsLocaleEnglish}
}

// ParseTotalsLocale validates a locale against the closed vocabulary.
// Matching is exact: surrounding whitespace is not trimmed and case
// is not folded, so two spellings can never name one locale. Unknown
// locales are refused before any reading happens.
func ParseTotalsLocale(raw string) (TotalsLocale, error) {
	locale := TotalsLocale(raw)
	if !locale.IsValid() {
		return "", ErrInvalidTotals
	}
	return locale, nil
}

// IsValid reports whether the locale belongs to the closed vocabulary.
func (l TotalsLocale) IsValid() bool {
	switch l {
	case TotalsLocalePortuguese, TotalsLocaleEnglish:
		return true
	default:
		return false
	}
}

// String returns the stored locale value.
func (l TotalsLocale) String() string { return string(l) }

// TotalsLabels carries every human string the public document may
// hold: the section titles in one locale. No account label, transfer
// identifier or secret ever enters this shape — only these dictionary
// values reach the reader.
type TotalsLabels struct {
	Title       string
	Supply      string
	Treasury    string
	Reserve     string
	Circulation string
	Locked      string
	Vaults      string
}

// TotalsLabelsFor renders the closed title dictionary in one locale.
// The keys are stable across locales; only the titles translate, so a
// pt document and an en document carry identical numbers.
func TotalsLabelsFor(locale TotalsLocale) TotalsLabels {
	if locale == TotalsLocaleEnglish {
		return TotalsLabels{
			Title:       "Treasury custody — public totals",
			Supply:      "Total supply (S)",
			Treasury:    "Total treasury",
			Reserve:     "Sovereign reserve",
			Circulation: "In circulation",
			Locked:      "Locked in holds",
			Vaults:      "By vault",
		}
	}
	return TotalsLabels{
		Title:       "Custódia do Tesouro — totais públicos",
		Supply:      "Oferta total (S)",
		Treasury:    "Tesouro total",
		Reserve:     "Reserva soberana",
		Circulation: "Em circulação",
		Locked:      "Bloqueado em empenhos",
		Vaults:      "Por cofre",
	}
}

// SuppressTotal applies the low-count rule to one third-party amount:
// fewer sources than the threshold report zero with suppressed set,
// anything else reports the exact amount untouched.
func SuppressTotal(millis, sources int64) (amount int64, suppressed bool) {
	if sources < TotalsLowCount {
		return 0, true
	}
	return millis, false
}
