package domain_test

import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

// FuzzParseContent proves the content value object never panics on
// arbitrary input and that every accepted content is internally consistent:
// bounded cost, non-zero canonical hash and idempotent normalization.
func FuzzParseContent(f *testing.F) {
	f.Add("A AGI existirá até 2040")
	f.Add("Fusion energy will be commercial by 2035")
	f.Add("👩🏽‍🚀 bandeira 🇧🇷 e acento cafe\u0301")
	f.Add("linha um\nlinha dois")
	f.Add("")
	f.Add(" \n\t ")
	f.Add("\u202Econteúdo reordenado")
	f.Add(strings.Repeat("a", 3001))
	f.Add(strings.Repeat("👩🏽‍🚀", 3001))
	f.Add(string([]byte{0xff, 0xfe}))

	f.Fuzz(func(t *testing.T, raw string) {
		content, err := domain.ParseContent(raw, text.GraphemeCount)
		if err != nil {
			return
		}
		if content.IsZero() {
			t.Fatal("accepted content must not be zero")
		}
		if content.GraphemeCost() < 1 || content.GraphemeCost() > domain.MaxGraphemeCost {
			t.Fatalf("accepted cost out of range: %d", content.GraphemeCost())
		}
		if content.Hash().IsZero() {
			t.Fatal("accepted content must carry a canonical hash")
		}

		// Normalization is idempotent and the cost/hash are stable.
		reparsed, err := domain.ParseContent(content.String(), text.GraphemeCount)
		if err != nil {
			t.Fatalf("reparsing accepted content failed: %v", err)
		}
		if !reparsed.Equals(content) {
			t.Fatalf("normalization is not idempotent: %q vs %q", reparsed.String(), content.String())
		}
		if reparsed.GraphemeCost() != content.GraphemeCost() || !reparsed.Hash().Equals(content.Hash()) {
			t.Fatal("cost or hash changed on reparsing")
		}
	})
}

// FuzzParseSource proves the source value object never panics and that
// accepted sources round-trip through their own canonical form.
func FuzzParseSource(f *testing.F) {
	f.Add("https://example.com/estudo", "Estudo revisado")
	f.Add("HTTP://Example.com/x", "")
	f.Add("example.com/x", "sem esquema")
	f.Add("javascript://alert(1)", "")
	f.Add("https://ex ample.com/x", "com espaço")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, rawURL, rawDescription string) {
		source, err := domain.ParseSource(rawURL, rawDescription)
		if err != nil {
			return
		}
		if source.IsZero() {
			t.Fatal("accepted source must not be zero")
		}
		reparsed, err := domain.ParseSource(source.URL(), source.Description())
		if err != nil {
			t.Fatalf("reparsing accepted source failed: %v", err)
		}
		if !reparsed.Equals(source) {
			t.Fatalf("source is not canonical: %q vs %q", reparsed.URL(), source.URL())
		}
	})
}
