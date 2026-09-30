package text_test

import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

// TestGraphemeCountExploratoryCorpus is the P10-T01 exploratory proof that
// the approved UAX #29 library counts user-perceived characters correctly
// for the product cases: ZWJ emoji, combining marks, flags, pt/en scripts,
// whitespace and line breaks. Expectations are the Unicode-correct cluster
// counts, never rune counts.
func TestGraphemeCountExploratoryCorpus(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{name: "empty", value: "", want: 0},
		{name: "ascii letter", value: "a", want: 1},

		// pt-BR and en-US product text.
		{name: "pt sentence", value: "A AGI existirá até 2040", want: 23},
		{name: "en sentence", value: "Fusion energy will be commercial by 2035", want: 40},
		{name: "pt punctuation and newline", value: "Olá, mundo!\nBom dia.", want: 20},
		{name: "en with emoji", value: "Minds changed 🧠", want: 15},

		// Combining marks: NFC and NFD are the same user-perceived text.
		{name: "nfc cafe", value: "café", want: 4},
		{name: "nfd cafe", value: "cafe\u0301", want: 4},
		{name: "single combining pair", value: "e\u0301", want: 1},

		// ZWJ emoji sequences and skin-tone modifiers.
		{name: "woman astronaut with skin tone", value: "👩🏽‍🚀", want: 1},
		{name: "family zwj sequence", value: "👨‍👩‍👧‍👦", want: 1},
		{name: "rainbow flag zwj sequence", value: "🏳️‍🌈", want: 1},

		// Regional indicator pairs are one cluster each.
		{name: "brazil flag", value: "🇧🇷", want: 1},
		{name: "two flags", value: "🇧🇷🇺🇸", want: 2},

		// Spaces, punctuation and breaks count as clusters.
		{name: "spaces and newline", value: "a b\nc", want: 5},
		{name: "blank whitespace", value: " \n\t", want: 3},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := text.GraphemeCount(test.value); got != test.want {
				t.Fatalf("GraphemeCount(%q) = %d, want %d", test.value, got, test.want)
			}
		})
	}

	// Canonically equivalent texts must never diverge: this is exactly what
	// a rune count gets wrong.
	if composed, decomposed := text.GraphemeCount("café"), text.GraphemeCount("cafe\u0301"); composed != decomposed {
		t.Fatalf("composed/decomposed counts diverge: %d vs %d", composed, decomposed)
	}
}

// TestGraphemeCountBoundary covers the 3.000-cluster product limit with
// both plain and multi-codepoint clusters.
func TestGraphemeCountBoundary(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{name: "exactly 3000 ascii", value: strings.Repeat("a", 3000), want: 3000},
		{name: "3001 ascii", value: strings.Repeat("a", 3001), want: 3001},
		{name: "exactly 3000 zwj clusters", value: strings.Repeat("👩🏽‍🚀", 3000), want: 3000},
		{name: "3001 zwj clusters", value: strings.Repeat("👩🏽‍🚀", 3001), want: 3001},
		{name: "exactly 3000 combining pairs", value: strings.Repeat("e\u0301", 3000), want: 3000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := text.GraphemeCount(test.value); got != test.want {
				t.Fatalf("GraphemeCount(%s...) = %d, want %d", test.name, got, test.want)
			}
		})
	}
}
