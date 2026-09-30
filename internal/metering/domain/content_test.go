package domain

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

func runeCounter(value string) int { return utf8.RuneCountInString(value) }

func TestParseMeasuredNormalizesAndMeasures(t *testing.T) {
	content, err := ParseMeasuredContent("  Olá,\r\nmundo!  ", runeCounter, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent() error = %v", err)
	}
	if content.String() != "Olá,\nmundo!" {
		t.Fatalf("content = %q, want trimmed LF-normalized text", content.String())
	}
	if content.Units() != 11 {
		t.Fatalf("units = %d, want the counter value", content.Units())
	}
	if content.Hash().IsZero() || len(content.Hash().String()) != 3+64 {
		t.Fatalf("hash = %q, want the versioned canonical format", content.Hash().String())
	}
	if content.IsZero() {
		t.Fatal("accepted content must not be zero")
	}
}

func TestParseMeasuredRejections(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		counter GraphemeCounter
		limit   int
		want    error
	}{
		{name: "missing counter", raw: "conteúdo", counter: nil, limit: 3000, want: ErrMissingMeterCounter},
		{name: "bad limit zero", raw: "conteúdo", counter: runeCounter, limit: 0, want: ErrInvalidMeasuredLimit},
		{name: "bad limit negative", raw: "conteúdo", counter: runeCounter, limit: -1, want: ErrInvalidMeasuredLimit},
		{name: "invalid utf8", raw: string([]byte{0xff, 0xfe, 0xfd}), counter: runeCounter, limit: 3000, want: ErrInvalidMeasuredContent},
		{name: "empty", raw: "", counter: runeCounter, limit: 3000, want: ErrEmptyMeasuredContent},
		{name: "whitespace only", raw: " \n\t ", counter: runeCounter, limit: 3000, want: ErrEmptyMeasuredContent},
		{name: "control character", raw: "conteúdo\x00oculto", counter: runeCounter, limit: 3000, want: ErrInvalidMeasuredContent},
		{name: "bidi override", raw: "conteúdo\u202Ereordenado", counter: runeCounter, limit: 3000, want: ErrInvalidMeasuredContent},
		{name: "above the limit", raw: strings.Repeat("a", 11), counter: runeCounter, limit: 10, want: ErrMeasuredContentTooLong},
		{name: "inconsistent counter", raw: "conteúdo", counter: func(string) int { return 0 }, limit: 3000, want: ErrEmptyMeasuredContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseMeasuredContent(test.raw, test.counter, test.limit); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	atLimit, err := ParseMeasuredContent(strings.Repeat("a", 10), runeCounter, 10)
	if err != nil {
		t.Fatalf("boundary content rejected: %v", err)
	}
	if atLimit.Units() != 10 {
		t.Fatalf("boundary units = %d, want 10", atLimit.Units())
	}
}

// TestParseMeasuredUnicodeCorpus proves the billed unit with the
// approved UAX #29 counter against an independent oracle: the
// hardcoded cluster counts below come from Unicode UAX #29, not from
// the implementation. Runes and bytes would answer differently.
func TestParseMeasuredUnicodeCorpus(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  int
		runes int
	}{
		{name: "composed cafe", raw: "café", want: 4, runes: 4},
		{name: "decomposed cafe", raw: "café", want: 4, runes: 5},
		{name: "zwj astronaut", raw: "👩🏽‍🚀", want: 1, runes: 4},
		{name: "flag pair", raw: "🇧🇷", want: 1, runes: 2},
		{name: "hebrew rtl", raw: "שלום", want: 4, runes: 4},
		{name: "arabic rtl", raw: "مرحبا", want: 5, runes: 5},
		{name: "mixed rtl numbers", raw: "שלום 123", want: 8, runes: 8},
		{name: "spaces and newline", raw: "a b\nc", want: 5, runes: 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content, err := ParseMeasuredContent(test.raw, text.GraphemeCount, 3000)
			if err != nil {
				t.Fatalf("ParseMeasuredContent() error = %v", err)
			}
			if content.Units() != test.want {
				t.Fatalf("units = %d, want oracle %d", content.Units(), test.want)
			}
			if got := runeCounter(test.raw); got != test.runes {
				t.Fatalf("oracle runes = %d, want %d (fixture drift)", got, test.runes)
			}
		})
	}
	composed, err := ParseMeasuredContent("café", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("composed error = %v", err)
	}
	decomposed, err := ParseMeasuredContent("café", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("decomposed error = %v", err)
	}
	if composed.Units() != decomposed.Units() {
		t.Fatalf("normalization split the unit: %d vs %d", composed.Units(), decomposed.Units())
	}
	if composed.Hash().Equals(decomposed.Hash()) {
		t.Fatal("distinct byte sequences must not share a quote hash")
	}
	zwj, err := ParseMeasuredContent("👩🏽‍🚀", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("zwj error = %v", err)
	}
	if zwj.Units() != 1 {
		t.Fatalf("zwj units = %d, want 1 (runes would say %d)", zwj.Units(), runeCounter("👩🏽‍🚀"))
	}
	if _, err := ParseMeasuredContent(strings.Repeat("👩🏽‍🚀", 3001), text.GraphemeCount, 3000); !errors.Is(err, ErrMeasuredContentTooLong) {
		t.Fatalf("3001 clusters error = %v, want ErrMeasuredContentTooLong", err)
	}
}

func TestMeasuredHashBindsQuoteToFinalBytes(t *testing.T) {
	quoted, err := ParseMeasuredContent("texto final", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent() error = %v", err)
	}
	if err := quoted.VerifyQuote(quoted.Hash()); err != nil {
		t.Fatalf("VerifyQuote(same) = %v, want nil", err)
	}
	edited, err := ParseMeasuredContent("texto final editado", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("edited error = %v", err)
	}
	if edited.Hash().Equals(quoted.Hash()) {
		t.Fatal("edited bytes reused the quoted hash")
	}
	if err := edited.VerifyQuote(quoted.Hash()); !errors.Is(err, ErrMeasuredContentHashMismatch) {
		t.Fatalf("VerifyQuote(edited) = %v, want ErrMeasuredContentHashMismatch", err)
	}
	if err := quoted.VerifyQuote(edited.Hash()); !errors.Is(err, ErrMeasuredContentHashMismatch) {
		t.Fatalf("VerifyQuote(swapped) = %v, want ErrMeasuredContentHashMismatch", err)
	}
	if err := quoted.VerifyQuote(MeasuredHash{}); !errors.Is(err, ErrMeasuredContentHashMismatch) {
		t.Fatalf("VerifyQuote(zero) = %v, want ErrMeasuredContentHashMismatch", err)
	}
	rebuilt, err := ReconstituteMeasured(quoted.String(), quoted.Units(), quoted.Hash(), 3000)
	if err != nil {
		t.Fatalf("ReconstituteMeasured() error = %v", err)
	}
	if !rebuilt.Equals(quoted) || rebuilt.Units() != quoted.Units() {
		t.Fatal("reconstitution lost content or units")
	}
	if _, err := ReconstituteMeasured("texto adulterado", quoted.Units(), quoted.Hash(), 3000); !errors.Is(err, ErrMeasuredContentHashMismatch) {
		t.Fatalf("reconstitute tampered = %v, want ErrMeasuredContentHashMismatch", err)
	}
	if _, err := ParseMeasuredHash("v1:" + strings.Repeat("z", 64)); !errors.Is(err, ErrInvalidMeasuredHash) {
		t.Fatalf("bad hash = %v, want ErrInvalidMeasuredHash", err)
	}
	parsed, err := ParseMeasuredHash(quoted.Hash().String())
	if err != nil {
		t.Fatalf("ParseMeasuredHash() error = %v", err)
	}
	if !parsed.Equals(quoted.Hash()) {
		t.Fatal("parsed hash diverges from the stored one")
	}
}
