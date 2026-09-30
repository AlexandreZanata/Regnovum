package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

func TestParseSourceCanonicalizesAndValidates(t *testing.T) {
	valid := []struct {
		name        string
		url         string
		description string
		wantURL     string
		wantDesc    string
	}{
		{name: "https with description", url: "https://example.com/estudo", description: "  Estudo revisado  ", wantURL: "https://example.com/estudo", wantDesc: "Estudo revisado"},
		{name: "http without description", url: "http://example.com/dado", wantURL: "http://example.com/dado"},
		{name: "scheme case canonicalized", url: "HTTPS://Example.com/Estudo", wantURL: "https://Example.com/Estudo"},
		{name: "trimmed url", url: "  https://example.com/x  ", wantURL: "https://example.com/x"},
		{name: "minimum length", url: "http://a", wantURL: "http://a"},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			source, err := domain.ParseSource(test.url, test.description)
			if err != nil {
				t.Fatalf("ParseSource() error = %v", err)
			}
			if source.URL() != test.wantURL {
				t.Fatalf("URL = %q, want %q", source.URL(), test.wantURL)
			}
			if source.Description() != test.wantDesc {
				t.Fatalf("description = %q, want %q", source.Description(), test.wantDesc)
			}
			if source.HasDescription() != (test.wantDesc != "") {
				t.Fatalf("HasDescription = %v, want %v", source.HasDescription(), test.wantDesc != "")
			}
			if source.IsZero() {
				t.Fatal("accepted source must not be zero")
			}
		})
	}

	invalid := []struct {
		name string
		url  string
		desc string
		want error
	}{
		{name: "empty url", url: "", want: domain.ErrEmptySourceURL},
		{name: "blank url", url: "   ", want: domain.ErrEmptySourceURL},
		{name: "missing scheme", url: "example.com/x", want: domain.ErrInvalidSourceURL},
		{name: "unknown scheme", url: "ftp://example.com/x", want: domain.ErrInvalidSourceURL},
		{name: "javascript scheme", url: "javascript://alert(1)", want: domain.ErrInvalidSourceURL},
		{name: "url without host", url: "http://", want: domain.ErrInvalidSourceURL},
		{name: "url starting with slash", url: "http:///caminho", want: domain.ErrInvalidSourceURL},
		{name: "whitespace inside", url: "https://ex ample.com/x", want: domain.ErrInvalidSourceURL},
		{name: "control inside", url: "https://example.com/\u0000", want: domain.ErrInvalidSourceURL},
		{name: "credentials in url", url: "https://user:secret@example.com/x", want: domain.ErrInvalidSourceURL},
		{name: "unicode hostname", url: "https://exämple.com/x", want: domain.ErrInvalidSourceURL},
		{name: "data scheme", url: "data:text/plain,hello", want: domain.ErrInvalidSourceURL},
		{name: "file scheme", url: "file:///tmp/report", want: domain.ErrInvalidSourceURL},
		{name: "javascript scheme", url: "javascript:alert(1)", want: domain.ErrInvalidSourceURL},
		{name: "too long", url: "https://example.com/" + strings.Repeat("a", domain.SourceURLMaxLength), want: domain.ErrInvalidSourceURL},
		{name: "description too long", url: "https://example.com/x", desc: strings.Repeat("d", domain.SourceDescriptionMaxLength+1), want: domain.ErrSourceDescriptionTooLong},
		{name: "description with control", url: "https://example.com/x", desc: "texto\u0007controle", want: domain.ErrInvalidContent},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := domain.ParseSource(test.url, test.desc); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestReconstituteSourceValidatesStoredState(t *testing.T) {
	source, err := domain.ReconstituteSource("https://example.com/estudo", "Estudo revisado")
	if err != nil {
		t.Fatalf("ReconstituteSource() error = %v", err)
	}
	if source.URL() != "https://example.com/estudo" || !source.HasDescription() {
		t.Fatal("reconstitution lost stored fields")
	}

	probes := []struct {
		name string
		url  string
		desc string
		want error
	}{
		{name: "empty url", url: "", want: domain.ErrEmptySourceURL},
		{name: "long url", url: "https://example.com/" + strings.Repeat("a", domain.SourceURLMaxLength), want: domain.ErrInvalidSourceURL},
		{name: "long description", url: "https://example.com/x", desc: strings.Repeat("d", domain.SourceDescriptionMaxLength+1), want: domain.ErrSourceDescriptionTooLong},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			if _, err := domain.ReconstituteSource(probe.url, probe.desc); !errors.Is(err, probe.want) {
				t.Fatalf("error = %v, want %v", err, probe.want)
			}
		})
	}

	if (domain.Source{}).IsZero() != true {
		t.Fatal("zero Source must be zero")
	}
	other, err := domain.ReconstituteSource("https://example.com/estudo", "Estudo revisado")
	if err != nil {
		t.Fatalf("ReconstituteSource(other): %v", err)
	}
	if !source.Equals(other) {
		t.Fatal("equal sources must compare equal")
	}
	withoutDescription, err := domain.ReconstituteSource("https://example.com/estudo", "")
	if err != nil {
		t.Fatalf("ReconstituteSource(without description): %v", err)
	}
	if source.Equals(withoutDescription) {
		t.Fatal("sources with different descriptions must not compare equal")
	}
}
