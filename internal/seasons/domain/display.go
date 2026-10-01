package domain

// SeasonLocale is the closed vocabulary of interface locales the
// season lifecycle display renders in: Portuguese and English. Dates
// and ordinals travel as canonical values either way; the locale
// only selects the titles, never the instants.
type SeasonLocale string

const (
	// SeasonLocalePortuguese renders the season titles in Portuguese.
	SeasonLocalePortuguese SeasonLocale = "pt"
	// SeasonLocaleEnglish renders the season titles in English.
	SeasonLocaleEnglish SeasonLocale = "en"
)

// AllSeasonLocales returns the closed vocabulary in canonical order.
func AllSeasonLocales() []SeasonLocale {
	return []SeasonLocale{SeasonLocalePortuguese, SeasonLocaleEnglish}
}

// ParseSeasonLocale validates a locale against the closed vocabulary.
// Matching is exact: surrounding whitespace is not trimmed and case
// is not folded. Unknown locales refuse before any season is read
// or displayed.
func ParseSeasonLocale(raw string) (SeasonLocale, error) {
	locale := SeasonLocale(raw)
	if !locale.IsValid() {
		return "", ErrInvalidSeason
	}
	return locale, nil
}

// IsValid reports whether the locale belongs to the closed vocabulary.
func (l SeasonLocale) IsValid() bool {
	switch l {
	case SeasonLocalePortuguese, SeasonLocaleEnglish:
		return true
	default:
		return false
	}
}

// String returns the stored locale value.
func (l SeasonLocale) String() string { return string(l) }

// SeasonTitles carries every human string the season lifecycle display
// may hold in one locale. No instant, ordinal, identifier, balance or
// secret ever enters this shape — only these dictionary values reach
// the reader.
type SeasonTitles struct {
	Title   string
	Current string
	History string
	State   string
}

// SeasonTitlesFor renders the closed title dictionary in one locale.
// The values translate; the canonical dates and ordinals never do.
func SeasonTitlesFor(locale SeasonLocale) SeasonTitles {
	if locale == SeasonLocaleEnglish {
		return SeasonTitles{
			Title:   "Current season",
			Current: "Current season",
			History: "Season history",
			State:   "Lifecycle state",
		}
	}
	return SeasonTitles{
		Title:   "Temporada atual",
		Current: "Temporada atual",
		History: "Histórico de temporadas",
		State:   "Estado do ciclo",
	}
}
