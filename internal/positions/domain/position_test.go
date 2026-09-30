package domain_test

import (
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

func TestParsePositionAcceptsOnlyCanonicalVocabulary(t *testing.T) {
	valid := []struct {
		raw  string
		want string
	}{
		{raw: domain.PositionAgree, want: domain.PositionAgree},
		{raw: domain.PositionDisagree, want: domain.PositionDisagree},
		{raw: domain.PositionUndecided, want: domain.PositionUndecided},
		{raw: "  agree  ", want: domain.PositionAgree},
	}
	for _, test := range valid {
		position, err := domain.ParsePosition(test.raw)
		if err != nil {
			t.Fatalf("ParsePosition(%q) error = %v", test.raw, err)
		}
		if position.String() != test.want {
			t.Fatalf("ParsePosition(%q) = %q, want %q", test.raw, position.String(), test.want)
		}
		if !position.IsSupported() || position.IsZero() {
			t.Fatalf("ParsePosition(%q) is not a supported value", test.raw)
		}
	}

	invalid := []struct {
		name string
		raw  string
		want error
	}{
		{name: "empty", raw: "", want: domain.ErrEmptyPosition},
		{name: "blank", raw: "   ", want: domain.ErrEmptyPosition},
		{name: "uppercase", raw: "Agree", want: domain.ErrInvalidPosition},
		{name: "unknown", raw: "maybe", want: domain.ErrInvalidPosition},
		{name: "localized", raw: "discordo", want: domain.ErrInvalidPosition},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := domain.ParsePosition(test.raw); !errors.Is(err, test.want) {
				t.Fatalf("ParsePosition(%q) error = %v, want %v", test.raw, err, test.want)
			}
		})
	}
}

func TestSupportedPositionsAreTheClosedVocabulary(t *testing.T) {
	positions := domain.SupportedPositions()
	if len(positions) != 3 {
		t.Fatalf("SupportedPositions() = %v, want exactly three values", positions)
	}
	want := []string{domain.PositionAgree, domain.PositionDisagree, domain.PositionUndecided}
	for index, position := range positions {
		if position.String() != want[index] {
			t.Fatalf("SupportedPositions()[%d] = %q, want %q", index, position.String(), want[index])
		}
	}

	// Distinct values never compare equal; zero values are never supported.
	for _, left := range positions {
		for _, right := range positions {
			if left.Equals(right) != (left.String() == right.String()) {
				t.Fatalf("Equals(%q, %q) diverges from value identity", left.String(), right.String())
			}
		}
	}
	if (domain.Position{}).IsSupported() || !(domain.Position{}).IsZero() {
		t.Fatal("zero Position must be unsupported and zero")
	}
}
