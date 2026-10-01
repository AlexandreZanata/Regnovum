package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestParseSeasonLocaleClosesVocabulary(t *testing.T) {
	for _, raw := range []string{"pt", "en"} {
		if _, err := ParseSeasonLocale(raw); err != nil {
			t.Fatalf("ParseSeasonLocale(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", "PT", " pt", "temporada", "pt-BR"} {
		if _, err := ParseSeasonLocale(raw); !errors.Is(err, ErrInvalidSeason) {
			t.Fatalf("ParseSeasonLocale(%q) = %v, want ErrInvalidSeason", raw, err)
		}
	}
	if got := len(AllSeasonLocales()); got != 2 {
		t.Fatalf("locales = %d, want pt and en", got)
	}
}

func TestSeasonTitlesDifferWithoutIntegers(t *testing.T) {
	pt := SeasonTitlesFor(SeasonLocalePortuguese)
	en := SeasonTitlesFor(SeasonLocaleEnglish)
	if pt.Title == en.Title || pt.History == en.History || pt.State == en.State {
		t.Fatalf("titles do not differ: pt=%+v en=%+v", pt, en)
	}
	for _, title := range []string{pt.Title, pt.Current, pt.History, pt.State, en.Title, en.Current, en.History, en.State} {
		if title == "" {
			t.Fatal("empty title: every locale renders every key")
		}
		for _, digit := range []string{"0", "1", "2", "7776000"} {
			if strings.Contains(title, digit) {
				t.Fatalf("title %q carries %q: translated text never enters the computation", title, digit)
			}
		}
	}
	if !strings.Contains(pt.Title, "Temporada") || !strings.Contains(en.Title, "eason") {
		t.Fatalf("titles lost the season: pt=%q en=%q", pt.Title, en.Title)
	}
}
