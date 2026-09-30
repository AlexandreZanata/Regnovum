package locale

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
)

func TestParseBCP47Canonicalizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    Tag
		wantErr bool
	}{
		{name: "already canonical", value: "pt-BR", want: "pt-BR"},
		{name: "case canonicalization", value: "pt-br", want: "pt-BR"},
		{name: "upper language and region", value: "EN-US", want: "en-US"},
		{name: "language only", value: "pt", want: "pt"},
		{name: "script", value: "zh-Hans", want: "zh-Hans"},
		{name: "script and region", value: "zh-hans-CN", want: "zh-Hans-CN"},
		{name: "numeric region", value: "pt-075", want: "pt-075"},
		{name: "empty", value: "", wantErr: true},
		{name: "whitespace only", value: " ", wantErr: true},
		{name: "wildcard", value: "*", wantErr: true},
		{name: "private use", value: "x-arena", wantErr: true},
		{name: "grandfathered style", value: "i-klingon", wantErr: true},
		{name: "too long", value: strings.Repeat("a", 36), wantErr: true},
		{name: "non ascii", value: "pt-Brésil", wantErr: true},
		{name: "newline injection", value: "pt-BR\nX: y", wantErr: true},
		{name: "malformed subtag", value: "pt-BR-", wantErr: true},
		{name: "malformed region", value: "pt-1", wantErr: true},
		{name: "malformed region letters", value: "pt-1B", wantErr: true},
		{name: "variant not allowed", value: "pt-BR-x-arena", wantErr: true},
		{name: "double region", value: "pt-BR-US", wantErr: true},
		{name: "script and numeric region", value: "zh-Hans-419", want: "zh-Hans-419"},
	}

	for _, test := range tests {
		tag, err := ParseBCP47(test.value)
		if test.wantErr {
			if err == nil {
				t.Errorf("%s: ParseBCP47(%q) = %q, want error", test.name, test.value, tag)
			}
			if tag != "" {
				t.Errorf("%s: error path returned tag %q", test.name, tag)
			}
			if !errors.Is(err, ErrInvalidTag) {
				t.Errorf("%s: error should wrap ErrInvalidTag, got %v", test.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: ParseBCP47(%q) error = %v", test.name, test.value, err)
			continue
		}
		if tag != test.want {
			t.Errorf("%s: ParseBCP47(%q) = %q, want %q", test.name, test.value, tag, test.want)
		}
	}
}

func TestAllowlistFollowsCatalog(t *testing.T) {
	t.Parallel()

	if !IsSupported(BrazilianPortuguese) || !IsSupported(AmericanEnglish) {
		t.Fatalf("catalog locales must be supported, got %v", i18n.SupportedLocales())
	}
	for _, name := range i18n.SupportedLocales() {
		t.Logf("supported: %s (cache key %s)", name, CacheKey(Tag(name)))
	}
	for _, unsupported := range []Tag{"es-ES", "xx-XX", "pt-PT", "en-GB"} {
		if IsSupported(unsupported) {
			t.Errorf("%s must not be in the catalog allowlist", unsupported)
		}
	}
}

func TestCacheKeyIsControlled(t *testing.T) {
	t.Parallel()

	if CacheKey(BrazilianPortuguese) != "v1:pt-BR" || CacheKey(AmericanEnglish) != "v1:en-US" {
		t.Fatalf("cache keys must be stable and derived only from the canonical tag")
	}
	// The key never embeds raw header material: equal canonical tags always
	// produce equal keys regardless of how they arrived.
	for _, raw := range []string{"pt-br", "PT-BR", "pt-BR"} {
		tag, err := ParseBCP47(raw)
		if err != nil {
			t.Fatalf("ParseBCP47(%q) error = %v", raw, err)
		}
		if CacheKey(tag) != CacheKey(BrazilianPortuguese) {
			t.Errorf("canonicalization must not change the cache key: %q -> %q", raw, CacheKey(tag))
		}
	}
}

func TestContextRoundTripAndDefault(t *testing.T) {
	t.Parallel()

	if got := FromContext(context.Background()); got != Default() {
		t.Errorf("empty context locale = %q, want default %q", got, Default())
	}
	ctx := WithLocale(context.Background(), AmericanEnglish)
	if got := FromContext(ctx); got != AmericanEnglish {
		t.Errorf("context locale = %q, want %q", got, AmericanEnglish)
	}
}
