package domain

import (
	"errors"
	"testing"
)

func TestParseReceiptLocaleClosesVocabulary(t *testing.T) {
	for _, raw := range []string{"pt", "en"} {
		locale, err := ParseReceiptLocale(raw)
		if err != nil {
			t.Fatalf("ParseReceiptLocale(%q): %v", raw, err)
		}
		if locale.String() != raw || !locale.IsValid() {
			t.Fatalf("round-trip %q failed", raw)
		}
	}
	for _, raw := range []string{"", " ", "PT", "EN", "pt-BR", "en-US", " pt", "pt ", "gift", "trade"} {
		if _, err := ParseReceiptLocale(raw); !errors.Is(err, ErrInvalidReceipt) {
			t.Fatalf("ParseReceiptLocale(%q) = nil, want ErrInvalidReceipt", raw)
		}
	}
	if len(AllReceiptLocales()) != 2 {
		t.Fatalf("vocabulary = %d locales, want the closed two", len(AllReceiptLocales()))
	}
}

func TestReceiptTitlesTranslateWithoutTouchingValues(t *testing.T) {
	pt := ReceiptTitlesFor(ReceiptLocalePortuguese)
	en := ReceiptTitlesFor(ReceiptLocaleEnglish)
	if pt == en {
		t.Fatal("titles do not differ between locales")
	}
	for _, title := range []string{pt.Title, en.Title, pt.Gross, en.Gross, pt.Tithe, en.Tithe} {
		if title == "" {
			t.Fatal("titles carry no blank strings")
		}
	}
}
