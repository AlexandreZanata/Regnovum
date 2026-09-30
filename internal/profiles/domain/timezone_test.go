package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

func TestParseTimezoneValidCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "brazil zone", input: "America/Sao_Paulo", want: "America/Sao_Paulo"},
		{name: "europe zone", input: "Europe/Lisbon", want: "Europe/Lisbon"},
		{name: "asia zone", input: "Asia/Tokyo", want: "Asia/Tokyo"},
		{name: "utc", input: "UTC", want: "UTC"},
		{name: "etc utc", input: "Etc/UTC", want: "Etc/UTC"},
		{name: "surrounding whitespace trimmed", input: "  America/New_York  ", want: "America/New_York"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			timezone, err := domain.ParseTimezone(tc.input)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if timezone.String() != tc.want {
				t.Errorf("String() = %q, want %q", timezone.String(), tc.want)
			}
			if timezone.IsZero() {
				t.Error("IsZero() = true, want false")
			}
		})
	}
}

func TestParseTimezoneUnset(t *testing.T) {
	for _, input := range []string{"", "   ", "\t\n"} {
		timezone, err := domain.ParseTimezone(input)
		if err != nil {
			t.Fatalf("ParseTimezone(%q) error = %v, want nil (timezone is optional)", input, err)
		}
		if !timezone.IsZero() {
			t.Errorf("ParseTimezone(%q) is not zero", input)
		}
		if timezone.String() != "" {
			t.Errorf("ParseTimezone(%q).String() = %q, want empty", input, timezone.String())
		}
	}
}

func TestParseTimezoneInvalidCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "unknown zone", input: "Mars/Phobos"},
		{name: "empty region", input: "America/"},
		{name: "path traversal", input: "../../etc/passwd"},
		{name: "absolute path", input: "/etc/passwd"},
		{name: "process alias", input: "Local"},
		{name: "inner space", input: "America/Sao Paulo"},
		{name: "control character", input: "America/\x00Sao_Paulo"},
		{name: "non-ascii", input: "América/São_Paulo"},
		{name: "too long", input: strings.Repeat("A", 65)},
		{name: "plain word", input: "saopaulo"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			timezone, err := domain.ParseTimezone(tc.input)
			if !errors.Is(err, domain.ErrInvalidTimezone) {
				t.Fatalf("ParseTimezone(%q) error = %v, want ErrInvalidTimezone", tc.input, err)
			}
			if !timezone.IsZero() {
				t.Fatalf("ParseTimezone(%q) returned a non-zero timezone on error", tc.input)
			}
		})
	}
}

func TestTimezoneValueSemantics(t *testing.T) {
	var zero domain.Timezone
	if !zero.IsZero() {
		t.Error("zero Timezone IsZero() = false, want true")
	}
	if zero.String() != "" {
		t.Errorf("zero Timezone renders %q, want empty", zero.String())
	}
	if !zero.Equals(domain.Timezone{}) {
		t.Error("two unset timezones must be equal")
	}

	saoPaulo, err := domain.ParseTimezone("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("parse America/Sao_Paulo: %v", err)
	}
	lisbon, err := domain.ParseTimezone("Europe/Lisbon")
	if err != nil {
		t.Fatalf("parse Europe/Lisbon: %v", err)
	}
	if saoPaulo.Equals(lisbon) {
		t.Error("distinct timezones must not be equal")
	}
	if saoPaulo.Equals(zero) || zero.Equals(saoPaulo) {
		t.Error("a set timezone must not equal the unset value")
	}
}

// TestTimezoneIsIndependentFromLocaleAndContentLanguage locks the product
// rule that preferences never rewrite each other: changing the interface
// locale keeps the timezone and username intact, and changing or clearing
// the timezone keeps the locale intact. content_language is not even part of
// this aggregate — it belongs to Arena content.
func TestTimezoneIsIndependentFromLocaleAndContentLanguage(t *testing.T) {
	const accountID domain.AccountID = "018f6b2a-0000-7000-8000-000000000050"
	username, err := domain.ParseUsername("TimezoneUser")
	if err != nil {
		t.Fatalf("parse username: %v", err)
	}
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	profile, err := domain.NewProfile(accountID, username, domain.DefaultLocale(), now)
	if err != nil {
		t.Fatalf("NewProfile: %v", err)
	}
	if !profile.Timezone().IsZero() {
		t.Fatal("new profiles must start with an unset timezone")
	}

	saoPaulo, err := domain.ParseTimezone("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("parse timezone: %v", err)
	}
	profile.ChangeTimezone(saoPaulo, now.Add(time.Hour))

	english, err := domain.ParseLocale("en-US")
	if err != nil {
		t.Fatalf("parse locale: %v", err)
	}
	if err := profile.ChangeLocale(english, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("ChangeLocale: %v", err)
	}
	if !profile.Timezone().Equals(saoPaulo) {
		t.Errorf("timezone changed with locale: %q", profile.Timezone())
	}
	if profile.Username().Normalized() != "timezoneuser" {
		t.Errorf("username changed with locale: %q", profile.Username())
	}
	if !profile.UpdatedAt().Equal(now.Add(2 * time.Hour)) {
		t.Errorf("UpdatedAt = %v, want %v", profile.UpdatedAt(), now.Add(2*time.Hour))
	}

	profile.ChangeTimezone(domain.Timezone{}, now.Add(3*time.Hour))
	if !profile.Timezone().IsZero() {
		t.Error("clearing the timezone must yield the unset value")
	}
	if profile.Locale().String() != domain.LocaleAmericanEnglish {
		t.Errorf("locale changed when clearing timezone: %q", profile.Locale())
	}
}

func FuzzParseTimezone(f *testing.F) {
	f.Add("America/Sao_Paulo")
	f.Add("Europe/Lisbon")
	f.Add("UTC")
	f.Add("America/New_York")
	f.Add("")
	f.Add("   ")
	f.Add("Mars/Phobos")
	f.Add("../../etc/passwd")
	f.Add("Local")
	f.Add("America/Sao Paulo")
	f.Add("América/São_Paulo")

	f.Fuzz(func(t *testing.T, input string) {
		timezone, err := domain.ParseTimezone(input)
		if err != nil {
			if !timezone.IsZero() {
				t.Fatalf("expected zero timezone on error for input %q", input)
			}
			return
		}
		if timezone.IsZero() {
			return
		}

		name := timezone.String()
		if len(name) == 0 || len(name) > 64 {
			t.Fatalf("stored timezone %q has invalid length for input %q", name, input)
		}
		for i := 0; i < len(name); i++ {
			if name[i] < 0x21 || name[i] > 0x7e {
				t.Fatalf("stored timezone %q contains non-printable byte for input %q", name, input)
			}
		}

		// Idempotency: re-parsing the stored name yields the same value.
		reparsed, err := domain.ParseTimezone(name)
		if err != nil {
			t.Fatalf("re-parse stored timezone %q failed: %v", name, err)
		}
		if !timezone.Equals(reparsed) {
			t.Fatalf("parse is not idempotent for input %q", input)
		}
	})
}
