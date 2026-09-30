package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

func TestParseEmailValidCases(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectedLocal  string
		expectedDomain string
		expectedString string
	}{
		{
			name:           "simple lowercase",
			input:          "user@example.com",
			expectedLocal:  "user",
			expectedDomain: "example.com",
			expectedString: "user@example.com",
		},
		{
			name:           "mixed case normalized to lowercase",
			input:          "User.Name+Tag@Sub.Example.COM",
			expectedLocal:  "user.name+tag",
			expectedDomain: "sub.example.com",
			expectedString: "user.name+tag@sub.example.com",
		},
		{
			name:           "trimmed surrounding spaces",
			input:          "   person@domain.org   ",
			expectedLocal:  "person",
			expectedDomain: "domain.org",
			expectedString: "person@domain.org",
		},
		{
			name:           "allowed special characters in local part",
			input:          "!#$%&'*+-/=?^_`{|}~@special.co.uk",
			expectedLocal:  "!#$%&'*+-/=?^_`{|}~",
			expectedDomain: "special.co.uk",
			expectedString: "!#$%&'*+-/=?^_`{|}~@special.co.uk",
		},
		{
			name:           "numeric local and domain label",
			input:          "12345@domain42.com",
			expectedLocal:  "12345",
			expectedDomain: "domain42.com",
			expectedString: "12345@domain42.com",
		},
		{
			name:           "subdomains hierarchy",
			input:          "a@b.c.d.example.com",
			expectedLocal:  "a",
			expectedDomain: "b.c.d.example.com",
			expectedString: "a@b.c.d.example.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, err := domain.ParseEmail(tc.input)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if e.Local() != tc.expectedLocal {
				t.Errorf("Local() = %q, want %q", e.Local(), tc.expectedLocal)
			}
			if e.Domain() != tc.expectedDomain {
				t.Errorf("Domain() = %q, want %q", e.Domain(), tc.expectedDomain)
			}
			if e.String() != tc.expectedString {
				t.Errorf("String() = %q, want %q", e.String(), tc.expectedString)
			}
			if e.IsZero() {
				t.Error("IsZero() = true, want false")
			}
		})
	}
}

func TestParseEmailBoundaryLimits(t *testing.T) {
	// 1. Exactly 64 chars in local part -> valid
	local64 := strings.Repeat("a", 64)
	emailWithLocal64 := local64 + "@example.com"
	e, err := domain.ParseEmail(emailWithLocal64)
	if err != nil {
		t.Fatalf("64-char local part should be valid, got: %v", err)
	}
	if len(e.Local()) != 64 {
		t.Errorf("len(Local) = %d, want 64", len(e.Local()))
	}

	// 2. 65 chars in local part -> ErrInvalidEmail
	local65 := strings.Repeat("a", 65)
	_, err = domain.ParseEmail(local65 + "@example.com")
	if !errors.Is(err, domain.ErrInvalidEmail) {
		t.Errorf("65-char local part should return ErrInvalidEmail, got: %v", err)
	}

	// 3. Exactly 254 chars total -> valid
	// Domain: example.com is 11 chars. Need "@" (1 char). Local needs 254 - 12 = 242 chars.
	// But local part max is 64! So domain must be long.
	// 64 local chars + 1 '@' = 65 chars.
	// Domain can be up to 254 - 65 = 189 chars.
	// 189 chars = "sub." (4) + 2 labels of 63 + 1 label of 54 + ".com" (4). Total = 4 + 63 + 1 + 63 + 1 + 52 + 5 = 189.
	label63A := strings.Repeat("a", 63)
	label63B := strings.Repeat("b", 63)
	label57 := strings.Repeat("c", 57)
	longDomain := label63A + "." + label63B + "." + label57 + ".com" // 63 + 1 + 63 + 1 + 57 + 4 = 189 chars.
	email254 := local64 + "@" + longDomain                           // 64 + 1 + 189 = 254 chars.
	if len(email254) != 254 {
		t.Fatalf("constructed email length = %d, want 254", len(email254))
	}
	e254, err := domain.ParseEmail(email254)
	if err != nil {
		t.Fatalf("254-char email should be valid, got: %v", err)
	}
	if len(e254.String()) != 254 {
		t.Errorf("len(String) = %d, want 254", len(e254.String()))
	}

	// 4. 255 chars total -> ErrEmailTooLong
	email255 := email254 + "x"
	_, err = domain.ParseEmail(email255)
	if !errors.Is(err, domain.ErrEmailTooLong) {
		t.Errorf("255-char email should return ErrEmailTooLong, got: %v", err)
	}
}

func TestParseEmailInvalidCases(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedError error
	}{
		{name: "empty string", input: "", expectedError: domain.ErrEmptyEmail},
		{name: "whitespace only", input: "   \t\n   ", expectedError: domain.ErrEmptyEmail},
		{name: "missing at sign", input: "userexample.com", expectedError: domain.ErrInvalidEmail},
		{name: "multiple at signs", input: "user@@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "at in local", input: "user@name@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "empty local part", input: "@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "empty domain part", input: "user@", expectedError: domain.ErrInvalidEmail},
		{name: "leading dot in local", input: ".user@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "trailing dot in local", input: "user.@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "consecutive dots in local", input: "user..name@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "internal whitespace in local", input: "user name@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "tab in local", input: "user\tname@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "newline in local", input: "user\nname@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "null byte in local", input: "user\x00@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "invalid local character", input: "user()@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "no dot in domain", input: "user@localhost", expectedError: domain.ErrInvalidEmail},
		{name: "leading dot in domain", input: "user@.example.com", expectedError: domain.ErrInvalidEmail},
		{name: "trailing dot in domain", input: "user@example.com.", expectedError: domain.ErrInvalidEmail},
		{name: "consecutive dots in domain", input: "user@example..com", expectedError: domain.ErrInvalidEmail},
		{name: "leading hyphen in domain label", input: "user@-example.com", expectedError: domain.ErrInvalidEmail},
		{name: "trailing hyphen in domain label", input: "user@example-.com", expectedError: domain.ErrInvalidEmail},
		{name: "single char TLD", input: "user@example.c", expectedError: domain.ErrInvalidEmail},
		{name: "all numeric TLD", input: "user@192.168.1.1", expectedError: domain.ErrInvalidEmail},
		{name: "invalid UTF-8 bytes", input: "user\xff\xfe@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "RTL override spoofing (RLO)", input: "\u202Euser@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "LRO override spoofing", input: "user\u202D@example.com", expectedError: domain.ErrInvalidEmail},
		{name: "LRE override spoofing", input: "user@\u202Aexample.com", expectedError: domain.ErrInvalidEmail},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ParseEmail(tc.input)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.input)
			}
			if !errors.Is(err, tc.expectedError) {
				t.Errorf("expected error %v, got %v", tc.expectedError, err)
			}
		})
	}
}

func TestEmailValueObjectSemantics(t *testing.T) {
	e1, _ := domain.ParseEmail("User@Example.Com")
	e2, _ := domain.ParseEmail("user@example.com")
	e3, _ := domain.ParseEmail("other@example.com")

	if !e1.Equals(e2) {
		t.Errorf("%v should equal %v", e1, e2)
	}
	if e1.Equals(e3) {
		t.Errorf("%v should not equal %v", e1, e3)
	}

	var zero domain.Email
	if !zero.IsZero() {
		t.Error("zero email should report IsZero() = true")
	}
	if zero.String() != "" || zero.Local() != "" || zero.Domain() != "" {
		t.Error("zero email getters should return empty strings")
	}
}

func TestEmailMasked(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"a@example.com", "a***@example.com"},
		{"ab@example.com", "a***b@example.com"},
		{"abc@example.com", "a***c@example.com"},
		{"alexandre@example.com", "a***e@example.com"},
		{"support+arena@sub.domain.org", "s***a@sub.domain.org"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			e, err := domain.ParseEmail(tc.input)
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if masked := e.Masked(); masked != tc.expected {
				t.Errorf("Masked() = %q, want %q", masked, tc.expected)
			}
		})
	}

	var zero domain.Email
	if zero.Masked() != "" {
		t.Errorf("zero email Masked() = %q, want empty", zero.Masked())
	}
}

func FuzzParseEmail(f *testing.F) {
	// Seed corpus with valid and edge-case inputs
	f.Add("user@example.com")
	f.Add("User.Name+Tag@Sub.Example.COM")
	f.Add("a@b.co")
	f.Add("")
	f.Add("   ")
	f.Add("invalid")
	f.Add("invalid@")
	f.Add("@invalid.com")
	f.Add("user@.com")
	f.Add("user@com")
	f.Add(strings.Repeat("a", 300))
	f.Add("\u202Euser@example.com")
	f.Add("user\x00@example.com")

	f.Fuzz(func(t *testing.T, input string) {
		e, err := domain.ParseEmail(input)
		if err != nil {
			// Parsing returned an expected error: ensure zero value
			if !e.IsZero() {
				t.Errorf("expected zero email on error for input %q", input)
			}
			return
		}

		// Invariant: parsed email must not be zero
		if e.IsZero() {
			t.Errorf("successful parse returned zero email for input %q", input)
		}

		// Invariant: length must be <= 254
		if len(e.String()) > 254 {
			t.Errorf("normalized email length %d > 254 for input %q", len(e.String()), input)
		}

		// Invariant: re-parsing normalized email must succeed and be identical (idempotency)
		reparsed, err := domain.ParseEmail(e.String())
		if err != nil {
			t.Fatalf("failed to re-parse normalized email %q: %v", e.String(), err)
		}
		if !reparsed.Equals(e) {
			t.Fatalf("re-parsed email %v did not equal original %v", reparsed, e)
		}
	})
}

func TestParseEmailRangeEndpointsAreAccepted(t *testing.T) {
	// Every endpoint of the local-part and label ranges must validate:
	// each boundary mutant moves exactly one fence, and only the fence
	// character itself tells the two versions apart (mutation gate:
	// email.go:96,133,135).
	valid := []string{
		"aZzA09@example.com",
		"user@aZzA09-x.example.com",
		"9lives@9lives-ninth.example.com",
	}
	for _, input := range valid {
		if _, err := domain.ParseEmail(input); err != nil {
			t.Errorf("ParseEmail(%q) = %v, want valid", input, err)
		}
	}
}

func TestParseEmailRangeNeighborsAreRejected(t *testing.T) {
	// Bytes no class admits stay refused around every fence. The fence
	// characters themselves (covered above as valid) are what tell a
	// boundary mutant apart; these neighbors pin the refusal side so a
	// loosened range cannot pass in silence. Note the local part also
	// allows RFC specials (among them the backtick and braces), so its
	// neighbors must avoid that list.
	invalid := []string{
		"user,x@example.com",
		"user:x@example.com",
		"user@ex`mple.com",
		"user@ex{mple.com",
		"user@ex[mple.com",
		"user@ex:mple.com",
		"user@ex_ample.com",
		"user:x@example.com",
	}
	for _, input := range invalid {
		if _, err := domain.ParseEmail(input); !errors.Is(err, domain.ErrInvalidEmail) {
			t.Errorf("ParseEmail(%q) = nil, want ErrInvalidEmail", input)
		}
	}
}
