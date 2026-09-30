package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

func TestParseSlugValidCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple", input: "arena", want: "arena"},
		{name: "hyphenated", input: "a-agi-existira-ate-2040", want: "a-agi-existira-ate-2040"},
		{name: "digits", input: "a1b2c3", want: "a1b2c3"},
		{name: "minimum length", input: "abc", want: "abc"},
		{name: "surrounding whitespace trimmed", input: "  arena-slug  ", want: "arena-slug"},
		{name: "maximum length", input: strings.Repeat("a", 80), want: strings.Repeat("a", 80)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slug, err := domain.ParseSlug(tc.input)
			if err != nil {
				t.Fatalf("ParseSlug(%q) error = %v", tc.input, err)
			}
			if slug.String() != tc.want || slug.IsZero() {
				t.Errorf("ParseSlug(%q) = %q", tc.input, slug.String())
			}
		})
	}
}

func TestParseSlugInvalidCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptySlug},
		{name: "blank", input: "   ", want: domain.ErrEmptySlug},
		{name: "too short", input: "ab", want: domain.ErrInvalidSlug},
		{name: "too long", want: domain.ErrInvalidSlug, input: strings.Repeat("a", 81)},
		{name: "uppercase", want: domain.ErrInvalidSlug, input: "Arena"},
		{name: "leading hyphen", want: domain.ErrInvalidSlug, input: "-arena"},
		{name: "trailing hyphen", want: domain.ErrInvalidSlug, input: "arena-"},
		{name: "double hyphen", want: domain.ErrInvalidSlug, input: "a--b"},
		{name: "underscore", want: domain.ErrInvalidSlug, input: "arena_slug"},
		{name: "inner space", want: domain.ErrInvalidSlug, input: "arena slug"},
		{name: "accented", want: domain.ErrInvalidSlug, input: "arená"},
		{name: "emoji", want: domain.ErrInvalidSlug, input: "arena🏟"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slug, err := domain.ParseSlug(tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseSlug(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if !slug.IsZero() {
				t.Fatal("failed parse must yield the zero slug")
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "question reformulated", input: "A AGI existirá até 2040?", want: "a-agi-existira-ate-2040"},
		{name: "accents transliterated", input: "Educação e saúde pública", want: "educacao-e-saude-publica"},
		{name: "punctuation collapses", input: "Olá, Mundo!!! Agora?", want: "ola-mundo-agora"},
		{name: "multiple separators collapse", input: "a   b___c---d", want: "a-b-c-d"},
		{name: "leading punctuation dropped", input: "??? afirmação", want: "afirmacao"},
		{name: "uppercase lowered", input: "MUDANÇA CLIMÁTICA", want: "mudanca-climatica"},
		{name: "emoji dropped", input: "arena 🏟 aberta", want: "arena-aberta"},
		{name: "only punctuation", input: "!!!???", want: ""},
		{name: "empty", input: "   ", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.Slugify(tc.input); got != tc.want {
				t.Errorf("Slugify(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestSlugifyCompositionIsParseable proves the composition contract of the
// publication flow: whenever the derived base already satisfies the format
// bounds, ParseSlug accepts it verbatim.
func TestSlugifyCompositionIsParseable(t *testing.T) {
	sources := []string{
		"Educação e saúde pública",
		"MUDANÇA CLIMÁTICA",
		"a b c d",
		"A AGI existirá até 2040?",
	}
	for _, source := range sources {
		base := domain.Slugify(source)
		if len(base) < domain.SlugMinLength || len(base) > domain.SlugMaxLength {
			continue
		}
		if _, err := domain.ParseSlug(base); err != nil {
			t.Errorf("Slugify(%q) = %q is not a valid slug: %v", source, base, err)
		}
	}
}

func FuzzParseSlug(f *testing.F) {
	f.Add("arena")
	f.Add("a-agi-existira-ate-2040")
	f.Add("abc")
	f.Add("")
	f.Add("ab")
	f.Add("-arena")
	f.Add("arena-")
	f.Add("a--b")
	f.Add("Arena")
	f.Add("arená")
	f.Add(strings.Repeat("a", 81))

	f.Fuzz(func(t *testing.T, input string) {
		slug, err := domain.ParseSlug(input)
		if err != nil {
			if !slug.IsZero() {
				t.Fatalf("non-zero slug on error for input %q", input)
			}
			return
		}

		value := slug.String()
		if len(value) < domain.SlugMinLength || len(value) > domain.SlugMaxLength {
			t.Fatalf("slug length %d out of bounds for input %q", len(value), input)
		}
		if value[0] == '-' || value[len(value)-1] == '-' {
			t.Fatalf("slug %q has an edge hyphen for input %q", value, input)
		}
		for i := 0; i < len(value); i++ {
			b := value[i]
			if (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' {
				continue
			}
			t.Fatalf("slug %q kept byte %q for input %q", value, b, input)
		}

		reparsed, err := domain.ParseSlug(value)
		if err != nil || !reparsed.Equals(slug) {
			t.Fatalf("parse is not idempotent for input %q", input)
		}
	})
}

func TestParseLanguage(t *testing.T) {
	canonical := []struct {
		input string
		want  string
	}{
		{input: "pt-BR", want: "pt-BR"},
		{input: "pt-br", want: "pt-BR"},
		{input: "PT-BR", want: "pt-BR"},
		{input: "  en-us  ", want: "en-US"},
	}
	for _, tc := range canonical {
		language, err := domain.ParseLanguage(tc.input)
		if err != nil {
			t.Fatalf("ParseLanguage(%q) error = %v", tc.input, err)
		}
		if language.String() != tc.want || language.IsZero() {
			t.Errorf("ParseLanguage(%q) = %q, want %q", tc.input, language.String(), tc.want)
		}
	}

	unsupported := []string{"fr-FR", "es-ES", "pt-PT", "pt", "en", "de"}
	for _, input := range unsupported {
		if _, err := domain.ParseLanguage(input); !errors.Is(err, domain.ErrUnsupportedLanguage) {
			t.Errorf("ParseLanguage(%q) error = %v, want ErrUnsupportedLanguage", input, err)
		}
	}

	invalid := []string{"", "   ", "pt_BR", "pt-BR-x", "-BR", "pt-", "pt BR", "*", "🏳"}
	for _, input := range invalid {
		if _, err := domain.ParseLanguage(input); !errors.Is(err, domain.ErrInvalidLanguage) && !errors.Is(err, domain.ErrEmptyLanguage) {
			t.Errorf("ParseLanguage(%q) error = %v, want ErrInvalidLanguage/ErrEmptyLanguage", input, err)
		}
	}

	supported := domain.SupportedLanguages()
	if len(supported) != 2 {
		t.Fatalf("SupportedLanguages() = %d, want 2", len(supported))
	}
	portuguese, _ := domain.ParseLanguage("pt-BR")
	if !portuguese.Equals(supported[0]) || portuguese.Equals(supported[1]) {
		t.Error("supported language semantics are inconsistent")
	}
}

func TestParseCategory(t *testing.T) {
	valid := []string{"technology", "science", "a1", "culture-and-society", strings.Repeat("a", 40)}
	for _, input := range valid {
		category, err := domain.ParseCategory(input)
		if err != nil {
			t.Fatalf("ParseCategory(%q) error = %v", input, err)
		}
		if category.String() != input || category.IsZero() {
			t.Errorf("ParseCategory(%q) = %q", input, category.String())
		}
	}

	invalid := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyCategory},
		{name: "blank", input: "   ", want: domain.ErrEmptyCategory},
		{name: "too short", input: "a", want: domain.ErrInvalidCategory},
		{name: "too long", input: strings.Repeat("a", 41), want: domain.ErrInvalidCategory},
		{name: "uppercase", input: "Technology", want: domain.ErrInvalidCategory},
		{name: "leading hyphen", input: "-tech", want: domain.ErrInvalidCategory},
		{name: "trailing hyphen", input: "tech-", want: domain.ErrInvalidCategory},
		{name: "underscore", input: "tech_nology", want: domain.ErrInvalidCategory},
		{name: "space", input: "tech nology", want: domain.ErrInvalidCategory},
		{name: "accented", input: "tecnologia-ç", want: domain.ErrInvalidCategory},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			category, err := domain.ParseCategory(tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseCategory(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if !category.IsZero() {
				t.Fatal("failed parse must yield the zero category")
			}
		})
	}
}
