package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

func TestParseUsernameValidCases(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		display    string
		normalized string
	}{
		{name: "lowercase", input: "arena", display: "arena", normalized: "arena"},
		{name: "mixed case preserved for display", input: "ArenaUser", display: "ArenaUser", normalized: "arenauser"},
		{name: "surrounding whitespace trimmed", input: "  ArenaUser  ", display: "ArenaUser", normalized: "arenauser"},
		{name: "allowed separators", input: "Arena_User-1", display: "Arena_User-1", normalized: "arena_user-1"},
		{name: "minimum length", input: "abc", display: "abc", normalized: "abc"},
		{name: "maximum length", input: strings.Repeat("A", 30), display: strings.Repeat("A", 30), normalized: strings.Repeat("a", 30)},
		{name: "digits only", input: "123456", display: "123456", normalized: "123456"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			username, err := domain.ParseUsername(tc.input)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if username.String() != tc.display {
				t.Errorf("String() = %q, want %q", username.String(), tc.display)
			}
			if username.Normalized() != tc.normalized {
				t.Errorf("Normalized() = %q, want %q", username.Normalized(), tc.normalized)
			}
			if username.IsZero() {
				t.Error("IsZero() = true, want false")
			}
		})
	}
}

func TestParseUsernameBoundaryLimits(t *testing.T) {
	// 2 characters is below the minimum.
	if _, err := domain.ParseUsername("ab"); !errors.Is(err, domain.ErrUsernameTooShort) {
		t.Errorf("2-char username: got %v, want ErrUsernameTooShort", err)
	}

	// Exactly 3 characters is valid.
	if _, err := domain.ParseUsername("abc"); err != nil {
		t.Errorf("3-char username should be valid, got %v", err)
	}

	// Exactly 30 characters is valid.
	if _, err := domain.ParseUsername(strings.Repeat("a", 30)); err != nil {
		t.Errorf("30-char username should be valid, got %v", err)
	}

	// 31 characters exceeds the maximum.
	if _, err := domain.ParseUsername(strings.Repeat("a", 31)); !errors.Is(err, domain.ErrUsernameTooLong) {
		t.Errorf("31-char username: got %v, want ErrUsernameTooLong", err)
	}
}

func TestParseUsernameInvalidCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyUsername},
		{name: "whitespace only", input: "   ", want: domain.ErrEmptyUsername},
		{name: "tabs and newlines only", input: "\t\n", want: domain.ErrEmptyUsername},
		{name: "leading hyphen", input: "-arena", want: domain.ErrInvalidUsernameFormat},
		{name: "trailing hyphen", input: "arena-", want: domain.ErrInvalidUsernameFormat},
		{name: "leading underscore", input: "_arena", want: domain.ErrInvalidUsernameFormat},
		{name: "trailing underscore", input: "arena_", want: domain.ErrInvalidUsernameFormat},
		{name: "inner space", input: "arena user", want: domain.ErrInvalidUsernameFormat},
		{name: "inner dot", input: "arena.user", want: domain.ErrInvalidUsernameFormat},
		{name: "at sign", input: "arena@user", want: domain.ErrInvalidUsernameFormat},
		{name: "punctuation", input: "arena!", want: domain.ErrInvalidUsernameFormat},
		{name: "inner newline", input: "are\nna", want: domain.ErrInvalidUsernameFormat},
		{name: "nul byte", input: "arena\x00", want: domain.ErrInvalidUsernameFormat},
		{name: "invalid utf-8", input: string([]byte{0xff, 0xfe, 0xfd}), want: domain.ErrInvalidUsernameFormat},
		{name: "accented latin", input: "árena", want: domain.ErrUsernameNonASCII},
		{name: "cyrillic homoglyphs", input: "аrеnа", want: domain.ErrUsernameNonASCII},
		{name: "fullwidth latin", input: "ＡＲＥＮＡ", want: domain.ErrUsernameNonASCII},
		{name: "bidi override", input: "\u202Earena", want: domain.ErrUsernameNonASCII},
		{name: "zero width space", input: "arena\u200B", want: domain.ErrUsernameNonASCII},
		{name: "emoji", input: "🏟arena", want: domain.ErrUsernameNonASCII},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			username, err := domain.ParseUsername(tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseUsername(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if !username.IsZero() {
				t.Fatalf("ParseUsername(%q) returned non-zero username on error", tc.input)
			}
		})
	}
}

func TestUsernameValueSemantics(t *testing.T) {
	var zero domain.Username
	if !zero.IsZero() {
		t.Error("zero Username IsZero() = false, want true")
	}
	if zero.String() != "" || zero.Normalized() != "" {
		t.Errorf("zero Username renders %q/%q, want empty strings", zero.String(), zero.Normalized())
	}

	upper, err := domain.ParseUsername("ArenaUser")
	if err != nil {
		t.Fatalf("parse upper: %v", err)
	}
	lower, err := domain.ParseUsername("arenauser")
	if err != nil {
		t.Fatalf("parse lower: %v", err)
	}
	other, err := domain.ParseUsername("arenauser-2")
	if err != nil {
		t.Fatalf("parse other: %v", err)
	}

	if !upper.Equals(lower) {
		t.Error("usernames differing only in case must be equal")
	}
	if upper.Equals(other) || lower.Equals(other) {
		t.Error("distinct normalized usernames must not be equal")
	}
	if upper.Normalized() != lower.Normalized() {
		t.Errorf("normalized forms differ: %q vs %q", upper.Normalized(), lower.Normalized())
	}
}

// TestUsernameVisualCollisionResidualRisk documents the accepted residual
// risk of ASCII-only handles. Unicode homoglyphs cannot be claimed at all
// (they are rejected before normalization), but collisions inside the
// allowed alphabet are impossible to eliminate at the syntax level, so each
// normalized form remains a distinct handle and impersonation is handled by
// moderation.
func TestUsernameVisualCollisionResidualRisk(t *testing.T) {
	// Non-ASCII homoglyphs of reserved or existing handles are rejected.
	for _, input := range []string{
		"аdmin",     // Cyrillic a
		"ѕupport",   // Cyrillic dze
		"gоyim",     // Cyrillic o
		"ⅼarena",    // Roman numeral fifty
		"ｍoderator", // fullwidth m
	} {
		if _, err := domain.ParseUsername(input); !errors.Is(err, domain.ErrUsernameNonASCII) {
			t.Errorf("homoglyph %q: got %v, want ErrUsernameNonASCII", input, err)
		}
	}

	// ASCII-only confusables stay distinct: accepted residual risk.
	pairs := [][2]string{
		{"arena-1", "arena-l"},
		{"goyim-0", "goyim-o"},
		{"moderator-rn", "moderator-m"},
	}
	for _, pair := range pairs {
		left, err := domain.ParseUsername(pair[0])
		if err != nil {
			t.Fatalf("parse %q: %v", pair[0], err)
		}
		right, err := domain.ParseUsername(pair[1])
		if err != nil {
			t.Fatalf("parse %q: %v", pair[1], err)
		}
		if left.Equals(right) || left.Normalized() == right.Normalized() {
			t.Errorf("confusable pair %q/%q collapsed into one handle", pair[0], pair[1])
		}
	}
}

func FuzzParseUsername(f *testing.F) {
	f.Add("arena")
	f.Add("ArenaUser")
	f.Add("Arena_User-1")
	f.Add("abc")
	f.Add(strings.Repeat("a", 30))
	f.Add("")
	f.Add("   ")
	f.Add("ab")
	f.Add(strings.Repeat("a", 31))
	f.Add("-arena")
	f.Add("arena-")
	f.Add("arena user")
	f.Add("árena")
	f.Add("аrеnа")
	f.Add("\u202Earena")
	f.Add("arena\u200B")
	f.Add("🏟arena")
	f.Add("arena\x00")
	f.Add(string([]byte{0xff, 0xfe, 0xfd}))

	f.Fuzz(func(t *testing.T, input string) {
		username, err := domain.ParseUsername(input)
		if err != nil {
			if !username.IsZero() {
				t.Fatalf("expected zero username on error for input %q", input)
			}
			return
		}

		display := username.String()
		normalized := username.Normalized()

		if username.IsZero() {
			t.Fatalf("successful parse returned zero username for input %q", input)
		}
		if len(display) < domain.UsernameMinLength || len(display) > domain.UsernameMaxLength {
			t.Fatalf("display length %d out of bounds for input %q", len(display), input)
		}
		if normalized != strings.ToLower(display) {
			t.Fatalf("normalized %q is not the lowercase of %q", normalized, display)
		}
		for i := 0; i < len(display); i++ {
			if display[i] >= 0x80 {
				t.Fatalf("display %q contains non-ASCII byte for input %q", display, input)
			}
		}
		if !isUsernameAlphanumeric(display[0]) || !isUsernameAlphanumeric(display[len(display)-1]) {
			t.Fatalf("display %q does not start/end alphanumeric for input %q", display, input)
		}

		// Idempotency: re-parsing either form must yield the same handle.
		reparsedDisplay, err := domain.ParseUsername(display)
		if err != nil {
			t.Fatalf("re-parse display %q failed: %v", display, err)
		}
		reparsedNormalized, err := domain.ParseUsername(normalized)
		if err != nil {
			t.Fatalf("re-parse normalized %q failed: %v", normalized, err)
		}
		if !username.Equals(reparsedDisplay) || !username.Equals(reparsedNormalized) {
			t.Fatalf("parse is not idempotent for input %q", input)
		}
	})
}

func isUsernameAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
