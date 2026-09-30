package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

func testStatementPolicy() domain.StatementPolicy {
	return domain.StatementPolicy{Version: "test", MinLength: 10, MaxLength: 40, ContextMaxLength: 100}
}

func TestParseStatementValidCases(t *testing.T) {
	policy := testStatementPolicy()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain statement", input: "A AGI existirá até 2040", want: "A AGI existirá até 2040"},
		{name: "surrounding whitespace trimmed", input: "   A AGI existirá até 2040   ", want: "A AGI existirá até 2040"},
		{name: "multiple lines preserved", input: "Linha um boa\nLinha dois boa", want: "Linha um boa\nLinha dois boa"},
		{name: "crlf canonicalized to lf", input: "Linha um boa\r\nLinha dois boa", want: "Linha um boa\nLinha dois boa"},
		{name: "bare cr canonicalized to lf", input: "Linha um boa\rLinha dois boa", want: "Linha um boa\nLinha dois boa"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			statement, err := domain.ParseStatement(tc.input, policy)
			if err != nil {
				t.Fatalf("ParseStatement(%q) error = %v", tc.input, err)
			}
			if statement.String() != tc.want {
				t.Errorf("String() = %q, want %q", statement.String(), tc.want)
			}
			if statement.IsZero() {
				t.Error("IsZero() = true, want false")
			}
		})
	}
}

func TestParseStatementBoundaries(t *testing.T) {
	policy := testStatementPolicy()

	minimum := strings.Repeat("a", 10)
	if _, err := domain.ParseStatement(minimum, policy); err != nil {
		t.Fatalf("minimum length statement rejected: %v", err)
	}
	if _, err := domain.ParseStatement(strings.Repeat("a", 9), policy); !errors.Is(err, domain.ErrStatementTooShort) {
		t.Fatalf("below minimum error = %v, want ErrStatementTooShort", err)
	}

	maximum := strings.Repeat("a", 40)
	if _, err := domain.ParseStatement(maximum, policy); err != nil {
		t.Fatalf("maximum length statement rejected: %v", err)
	}
	if _, err := domain.ParseStatement(strings.Repeat("a", 41), policy); !errors.Is(err, domain.ErrStatementTooLong) {
		t.Fatalf("above maximum error = %v, want ErrStatementTooLong", err)
	}

	// Limits are counted in Unicode runes, not bytes.
	accented := strings.Repeat("á", 40)
	statement, err := domain.ParseStatement(accented, policy)
	if err != nil {
		t.Fatalf("40 accented runes rejected: %v", err)
	}
	if statement.RuneCount() != 40 || len(statement.String()) != 80 {
		t.Fatalf("accented statement = %d runes / %d bytes, want 40/80", statement.RuneCount(), len(statement.String()))
	}
}

func TestParseStatementInvalidCases(t *testing.T) {
	policy := testStatementPolicy()
	tests := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyStatement},
		{name: "whitespace only", input: "   \n\t  ", want: domain.ErrEmptyStatement},
		{name: "too short", input: "curta", want: domain.ErrStatementTooShort},
		{name: "nul byte", input: "afirmação\x00 válida", want: domain.ErrInvalidStatement},
		{name: "tab character", input: "afirmação\tcom tab", want: domain.ErrInvalidStatement},
		{name: "bidi override", input: "afirmação\u202E valida", want: domain.ErrInvalidStatement},
		{name: "unicode isolate", input: "afirmação\u2066 valida", want: domain.ErrInvalidStatement},
		{name: "invalid utf-8", input: string([]byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8, 0xf7, 0xf6}), want: domain.ErrInvalidStatement},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			statement, err := domain.ParseStatement(tc.input, policy)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseStatement(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if !statement.IsZero() {
				t.Fatal("failed parse must yield the zero statement")
			}
		})
	}
}

func TestParseStatementRejectsInvalidPolicy(t *testing.T) {
	invalid := domain.StatementPolicy{Version: "broken", MinLength: 0, MaxLength: 10, ContextMaxLength: 10}
	if _, err := domain.ParseStatement("A AGI existirá até 2040", invalid); !errors.Is(err, domain.ErrInvalidPolicy) {
		t.Fatalf("invalid policy error = %v, want ErrInvalidPolicy", err)
	}
	inverted := domain.StatementPolicy{Version: "broken", MinLength: 30, MaxLength: 10, ContextMaxLength: 10}
	if _, err := domain.ParseStatement("A AGI existirá até 2040", inverted); !errors.Is(err, domain.ErrInvalidPolicy) {
		t.Fatalf("inverted policy error = %v, want ErrInvalidPolicy", err)
	}
}

func TestParseContext(t *testing.T) {
	policy := testStatementPolicy()

	for _, input := range []string{"", "   ", "\n\t"} {
		context, err := domain.ParseContext(input, policy)
		if err != nil {
			t.Fatalf("ParseContext(%q) error = %v", input, err)
		}
		if !context.IsZero() {
			t.Fatalf("ParseContext(%q) must yield the unset context", input)
		}
	}

	context, err := domain.ParseContext("  Primeira linha\r\n\r\nSegunda linha  ", policy)
	if err != nil {
		t.Fatalf("valid context error = %v", err)
	}
	if context.String() != "Primeira linha\n\nSegunda linha" {
		t.Fatalf("context = %q, want canonical multi-line text", context.String())
	}

	if _, err := domain.ParseContext(strings.Repeat("a", 101), policy); !errors.Is(err, domain.ErrContextTooLong) {
		t.Fatalf("oversized context error = %v, want ErrContextTooLong", err)
	}
	if _, err := domain.ParseContext("contexto\x00 inválido", policy); !errors.Is(err, domain.ErrInvalidContext) {
		t.Fatalf("invalid context error = %v, want ErrInvalidContext", err)
	}
	if _, err := domain.ParseContext("contexto\x00 inválido", domain.StatementPolicy{MinLength: 0}); !errors.Is(err, domain.ErrInvalidPolicy) {
		t.Fatalf("invalid policy error = %v, want ErrInvalidPolicy", err)
	}
}

func FuzzParseStatement(f *testing.F) {
	f.Add("A AGI existirá até 2040")
	f.Add("Primeira linha\nSegunda linha")
	f.Add("Primeira linha\r\nSegunda linha")
	f.Add("  espaços  ")
	f.Add("")
	f.Add("   ")
	f.Add("curta")
	f.Add(strings.Repeat("a", 41))
	f.Add(strings.Repeat("á", 40))
	f.Add("afirmação\x00")
	f.Add("afirmação\u202E")
	f.Add(string([]byte{0xff, 0xfe}))

	f.Fuzz(func(t *testing.T, input string) {
		policy := testStatementPolicy()
		statement, err := domain.ParseStatement(input, policy)
		if err != nil {
			if !statement.IsZero() {
				t.Fatalf("non-zero statement on error for input %q", input)
			}
			return
		}

		if statement.IsZero() {
			t.Fatalf("successful parse returned the zero statement for input %q", input)
		}
		if count := statement.RuneCount(); count < policy.MinLength || count > policy.MaxLength {
			t.Fatalf("statement runes = %d out of [%d, %d] for input %q", count, policy.MinLength, policy.MaxLength, input)
		}
		for _, r := range statement.String() {
			if r == '\n' {
				continue
			}
			if r < 0x20 || r == 0x7f {
				t.Fatalf("statement %q kept control rune %U", statement.String(), r)
			}
		}

		// Idempotency: re-parsing the normalized statement is a fixed point.
		reparsed, err := domain.ParseStatement(statement.String(), policy)
		if err != nil {
			t.Fatalf("re-parse %q failed: %v", statement.String(), err)
		}
		if !reparsed.Equals(statement) {
			t.Fatalf("parse is not idempotent for input %q", input)
		}
	})
}
