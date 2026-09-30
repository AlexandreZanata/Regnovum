package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

func TestParseLocaleCanonicalization(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "already canonical pt-BR", input: "pt-BR", want: "pt-BR"},
		{name: "lowercase region", input: "pt-br", want: "pt-BR"},
		{name: "uppercase language", input: "PT-BR", want: "pt-BR"},
		{name: "lowercase language and region", input: "en-us", want: "en-US"},
		{name: "surrounding whitespace trimmed", input: "  EN-US  ", want: "en-US"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			locale, err := domain.ParseLocale(tc.input)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if locale.String() != tc.want {
				t.Errorf("String() = %q, want %q", locale.String(), tc.want)
			}
			if !locale.IsSupported() {
				t.Errorf("locale %q should be supported", locale.String())
			}
			if locale.IsZero() {
				t.Error("IsZero() = true, want false")
			}
		})
	}
}

func TestParseLocaleInvalidAndUnsupported(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyLocale},
		{name: "whitespace only", input: "   ", want: domain.ErrEmptyLocale},
		{name: "unsupported region fr-FR", input: "fr-FR", want: domain.ErrUnsupportedLocale},
		{name: "unsupported language es-ES", input: "es-ES", want: domain.ErrUnsupportedLocale},
		{name: "unsupported portugal variant", input: "pt-PT", want: domain.ErrUnsupportedLocale},
		{name: "language only pt", input: "pt", want: domain.ErrUnsupportedLocale},
		{name: "language only en", input: "en", want: domain.ErrUnsupportedLocale},
		{name: "language only with unknown", input: "de", want: domain.ErrUnsupportedLocale},
		{name: "underscore separator", input: "pt_BR", want: domain.ErrInvalidLocale},
		{name: "wildcard", input: "*", want: domain.ErrInvalidLocale},
		{name: "script subtag", input: "pt-Latn-BR", want: domain.ErrInvalidLocale},
		{name: "private use extension", input: "pt-BR-x-private", want: domain.ErrInvalidLocale},
		{name: "trailing separator", input: "pt-", want: domain.ErrInvalidLocale},
		{name: "leading separator", input: "-BR", want: domain.ErrInvalidLocale},
		{name: "space inside", input: "pt BR", want: domain.ErrInvalidLocale},
		{name: "one letter region", input: "pt-B", want: domain.ErrInvalidLocale},
		{name: "three letter alphabetic region", input: "pt-BRA", want: domain.ErrInvalidLocale},
		{name: "punctuation", input: "pt-br!", want: domain.ErrInvalidLocale},
		{name: "numeric language", input: "12-BR", want: domain.ErrInvalidLocale},
		{name: "emoji", input: "🏳arena", want: domain.ErrInvalidLocale},
		{name: "too many subtags", input: "pt-BR-extra-more", want: domain.ErrInvalidLocale},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			locale, err := domain.ParseLocale(tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseLocale(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if !locale.IsZero() {
				t.Fatalf("ParseLocale(%q) returned non-zero locale on error", tc.input)
			}
		})
	}
}

func TestLocaleValueSemantics(t *testing.T) {
	var zero domain.Locale
	if !zero.IsZero() {
		t.Error("zero Locale IsZero() = false, want true")
	}
	if zero.String() != "" {
		t.Errorf("zero Locale renders %q, want empty string", zero.String())
	}
	if zero.IsSupported() {
		t.Error("zero Locale should not be supported")
	}

	defaultLocale := domain.DefaultLocale()
	if defaultLocale.String() != domain.LocaleBrazilianPortuguese {
		t.Errorf("DefaultLocale() = %q, want %q", defaultLocale.String(), domain.LocaleBrazilianPortuguese)
	}
	if !defaultLocale.IsSupported() {
		t.Error("DefaultLocale() must be supported")
	}

	supported := domain.SupportedLocales()
	if len(supported) != 2 {
		t.Fatalf("SupportedLocales() has %d entries, want 2", len(supported))
	}
	for _, locale := range supported {
		if !locale.IsSupported() {
			t.Errorf("SupportedLocales() entry %q is not supported", locale.String())
		}
	}

	english, err := domain.ParseLocale("en-US")
	if err != nil {
		t.Fatalf("parse en-US: %v", err)
	}
	if !english.Equals(domain.SupportedLocales()[1]) {
		t.Error("parsed en-US must equal the second supported locale")
	}
	if english.Equals(defaultLocale) {
		t.Error("en-US must differ from the default locale")
	}
}

func FuzzParseLocale(f *testing.F) {
	f.Add("pt-BR")
	f.Add("pt-br")
	f.Add("PT-BR")
	f.Add("en-US")
	f.Add("EN-us")
	f.Add("")
	f.Add("   ")
	f.Add("fr-FR")
	f.Add("pt")
	f.Add("pt_BR")
	f.Add("pt-BR-x")
	f.Add("-BR")
	f.Add("🏳arena")

	f.Fuzz(func(t *testing.T, input string) {
		locale, err := domain.ParseLocale(input)
		if err != nil {
			if !locale.IsZero() {
				t.Fatalf("expected zero locale on error for input %q", input)
			}
			return
		}

		tag := locale.String()
		if !locale.IsSupported() {
			t.Fatalf("successful parse %q produced unsupported locale %q", input, tag)
		}
		if strings.TrimSpace(tag) != tag || tag == "" {
			t.Fatalf("canonical tag %q is not trimmed", tag)
		}
		for i := 0; i < len(tag); i++ {
			if tag[i] >= 0x80 {
				t.Fatalf("canonical tag %q contains non-ASCII byte for input %q", tag, input)
			}
		}

		// Idempotency: re-parsing a canonical tag must yield the same locale.
		reparsed, err := domain.ParseLocale(tag)
		if err != nil {
			t.Fatalf("re-parse canonical %q failed: %v", tag, err)
		}
		if !locale.Equals(reparsed) {
			t.Fatalf("parse is not idempotent for input %q", input)
		}
	})
}
