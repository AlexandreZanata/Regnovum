package domain_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

// runeCounter is the deterministic fake used for structural tests; the
// Unicode corpus tests below inject the approved UAX #29 counter
// (internal/platform/text, ADR-013).
func runeCounter(value string) int {
	return utf8.RuneCountInString(value)
}

func TestParseContentNormalizesAndMeasures(t *testing.T) {
	content, err := domain.ParseContent("  Olá,\r\nmundo!  ", runeCounter)
	if err != nil {
		t.Fatalf("ParseContent() error = %v", err)
	}
	if content.String() != "Olá,\nmundo!" {
		t.Fatalf("content = %q, want trimmed LF-normalized text", content.String())
	}
	if content.GraphemeCost() != 11 {
		t.Fatalf("cost = %d, want the counter value", content.GraphemeCost())
	}
	if content.Hash().IsZero() || !strings.HasPrefix(content.Hash().String(), "v1:") || len(content.Hash().String()) != 3+64 {
		t.Fatalf("hash = %q, want the versioned canonical format", content.Hash().String())
	}
	if content.IsZero() {
		t.Fatal("accepted content must not be zero")
	}
}

func TestParseContentRejections(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		counter domain.GraphemeCounter
		want    error
	}{
		{name: "missing counter", raw: "conteúdo", counter: nil, want: domain.ErrMissingGraphemeCounter},
		{name: "invalid utf8", raw: string([]byte{0xff, 0xfe, 0xfd}), counter: runeCounter, want: domain.ErrInvalidContent},
		{name: "empty", raw: "", counter: runeCounter, want: domain.ErrEmptyContent},
		{name: "whitespace only", raw: " \n\t ", counter: runeCounter, want: domain.ErrEmptyContent},
		{name: "control character", raw: "conteúdo\u0000oculto", counter: runeCounter, want: domain.ErrInvalidContent},
		{name: "bidi override", raw: "conteúdo\u202Ereordenado", counter: runeCounter, want: domain.ErrInvalidContent},
		{name: "above the limit", raw: strings.Repeat("a", domain.MaxGraphemeCost+1), counter: runeCounter, want: domain.ErrContentTooLong},
		{name: "inconsistent counter", raw: "conteúdo", counter: func(string) int { return 0 }, want: domain.ErrEmptyContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := domain.ParseContent(test.raw, test.counter); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	// Exactly at the limit is accepted.
	content, err := domain.ParseContent(strings.Repeat("a", domain.MaxGraphemeCost), runeCounter)
	if err != nil {
		t.Fatalf("boundary content rejected: %v", err)
	}
	if content.GraphemeCost() != domain.MaxGraphemeCost {
		t.Fatalf("boundary cost = %d, want %d", content.GraphemeCost(), domain.MaxGraphemeCost)
	}
}

// TestParseContentUnicodeCorpus is the P10-T03 corpus proof with the
// approved UAX #29 counter: user-perceived characters, not runes, and
// newlines/spaces counting as clusters.
func TestParseContentUnicodeCorpus(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "pt sentence", raw: "A AGI existirá até 2040", want: 23},
		{name: "en sentence", raw: "Fusion energy will be commercial by 2035", want: 40},
		{name: "nfd combining", raw: "cafe\u0301", want: 4},
		{name: "zwj astronaut", raw: "👩🏽‍🚀", want: 1},
		{name: "flag pair", raw: "🇧🇷", want: 1},
		{name: "spaces and newline", raw: "a b\nc", want: 5},
		{name: "line break counts", raw: "linha um\nlinha dois", want: 19},
		{name: "exactly 3000 clusters", raw: strings.Repeat("a", 3000), want: 3000},
		{name: "exactly 3000 zwj clusters", raw: strings.Repeat("👩🏽‍🚀", 3000), want: 3000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content, err := domain.ParseContent(test.raw, text.GraphemeCount)
			if err != nil {
				t.Fatalf("ParseContent() error = %v", err)
			}
			if content.GraphemeCost() != test.want {
				t.Fatalf("cost = %d, want %d", content.GraphemeCost(), test.want)
			}
		})
	}

	// One cluster above the limit is refused with the real counter too.
	if _, err := domain.ParseContent(strings.Repeat("👩🏽‍🚀", 3001), text.GraphemeCount); !errors.Is(err, domain.ErrContentTooLong) {
		t.Fatalf("3001 clusters error = %v, want ErrContentTooLong", err)
	}
}

// TestContentHashIsStableAcrossExecutions pins a golden digest: the same
// normalized content must hash identically in every run and revision of the
// v1 algorithm.
func TestContentHashIsStableAcrossExecutions(t *testing.T) {
	const raw = "A AGI existirá até 2040"
	const golden = "v1:2a9470bda2d5099b34054b758384746fdcd4905393ca9233f2d3ff0e445799a2"

	content, err := domain.ParseContent(raw, text.GraphemeCount)
	if err != nil {
		t.Fatalf("ParseContent() error = %v", err)
	}
	if content.Hash().String() != golden {
		t.Fatalf("hash = %q, want the golden v1 digest %q", content.Hash().String(), golden)
	}

	// Determinism: repeated construction and a fresh parse agree.
	first := domain.HashContent(content.String())
	second := domain.HashContent(content.String())
	if !first.Equals(second) || !first.Equals(content.Hash()) {
		t.Fatal("hash is not deterministic")
	}

	// Different content, different digest.
	other := domain.HashContent("A AGI não existirá até 2040")
	if other.Equals(content.Hash()) {
		t.Fatal("distinct contents must not share a digest")
	}
}

func TestParseContentHashAndReconstitution(t *testing.T) {
	content, err := domain.ParseContent("Conteúdo reconstituível", runeCounter)
	if err != nil {
		t.Fatalf("ParseContent() error = %v", err)
	}

	parsed, err := domain.ParseContentHash(content.Hash().String())
	if err != nil {
		t.Fatalf("ParseContentHash() error = %v", err)
	}
	if !parsed.Equals(content.Hash()) {
		t.Fatal("parsed hash diverges from the stored one")
	}

	invalid := []string{
		"",
		"v1:",
		"v2:" + strings.Repeat("a", 64),
		"v1:" + strings.Repeat("A", 64),
		"v1:" + strings.Repeat("a", 63),
		"v1:" + strings.Repeat("z", 64),
	}
	for _, raw := range invalid {
		if _, err := domain.ParseContentHash(raw); !errors.Is(err, domain.ErrInvalidContentHash) {
			t.Fatalf("ParseContentHash(%q) error = %v, want ErrInvalidContentHash", raw, err)
		}
	}

	reconstituted, err := domain.ReconstituteContent(content.String(), content.GraphemeCost(), content.Hash())
	if err != nil {
		t.Fatalf("ReconstituteContent() error = %v", err)
	}
	if !reconstituted.Equals(content) || reconstituted.GraphemeCost() != content.GraphemeCost() {
		t.Fatal("reconstitution lost content or cost")
	}

	probes := []struct {
		name  string
		value string
		cost  int
		hash  domain.ContentHash
		want  error
	}{
		{name: "empty value", value: "", cost: 10, hash: content.Hash(), want: domain.ErrEmptyContent},
		{name: "zero cost", value: content.String(), cost: 0, hash: content.Hash(), want: domain.ErrContentTooLong},
		{name: "cost above limit", value: content.String(), cost: 3001, hash: content.Hash(), want: domain.ErrContentTooLong},
		{name: "zero hash", value: content.String(), cost: 10, want: domain.ErrInvalidContentHash},
		{name: "tampered value", value: "Conteúdo alterado", cost: 10, hash: content.Hash(), want: domain.ErrContentHashMismatch},
		{name: "unnormalized value", value: "Conteúdo\r\nreconstituível", cost: 24, hash: content.Hash(), want: domain.ErrContentHashMismatch},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			if _, err := domain.ReconstituteContent(probe.value, probe.cost, probe.hash); !errors.Is(err, probe.want) {
				t.Fatalf("error = %v, want %v", err, probe.want)
			}
		})
	}
}
