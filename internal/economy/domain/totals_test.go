package domain_test

import (
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func TestParseTotalsLocale(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"pt", "en"} {
		locale, err := domain.ParseTotalsLocale(raw)
		if err != nil {
			t.Errorf("ParseTotalsLocale(%q): %v", raw, err)
		} else if locale.String() != raw {
			t.Errorf("ParseTotalsLocale(%q).String() = %q", raw, locale.String())
		}
	}
	for _, raw := range []string{"", "PT", "EN", "pt-BR", "en-US", "pt ", " pt", "fr"} {
		if _, err := domain.ParseTotalsLocale(raw); !errors.Is(err, domain.ErrInvalidTotals) {
			t.Errorf("ParseTotalsLocale(%q) = %v, want ErrInvalidTotals", raw, err)
		}
	}
}

func TestTotalsLabelsTranslateTitlesOnly(t *testing.T) {
	t.Parallel()

	pt := domain.TotalsLabelsFor(domain.TotalsLocalePortuguese)
	en := domain.TotalsLabelsFor(domain.TotalsLocaleEnglish)
	for _, pair := range [][2]string{
		{pt.Title, en.Title},
		{pt.Supply, en.Supply},
		{pt.Treasury, en.Treasury},
		{pt.Reserve, en.Reserve},
		{pt.Circulation, en.Circulation},
		{pt.Locked, en.Locked},
		{pt.Vaults, en.Vaults},
	} {
		if pair[0] == "" || pair[1] == "" {
			t.Errorf("empty title in %+v", pair)
		}
		if pair[0] == pair[1] {
			t.Errorf("title %q did not translate", pair[0])
		}
	}
}

func TestSuppressTotal(t *testing.T) {
	t.Parallel()

	if amount, suppressed := domain.SuppressTotal(600, 6); amount != 600 || suppressed {
		t.Errorf("SuppressTotal(600, 6) = (%d, %v), want (600, false)", amount, suppressed)
	}
	if amount, suppressed := domain.SuppressTotal(600, 5); amount != 600 || suppressed {
		t.Errorf("SuppressTotal(600, 5) = (%d, %v), want (600, false)", amount, suppressed)
	}
	if amount, suppressed := domain.SuppressTotal(600, 4); amount != 0 || !suppressed {
		t.Errorf("SuppressTotal(600, 4) = (%d, %v), want (0, true)", amount, suppressed)
	}
	if amount, suppressed := domain.SuppressTotal(600, 0); amount != 0 || !suppressed {
		t.Errorf("SuppressTotal(600, 0) = (%d, %v), want (0, true)", amount, suppressed)
	}
}
