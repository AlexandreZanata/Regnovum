package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

func TestArenaIDValueObject(t *testing.T) {
	valid := []struct {
		input string
		want  string
	}{
		{input: "018f6b2a-0000-7000-8000-000000000001", want: "018f6b2a-0000-7000-8000-000000000001"},
		{input: "  arena-uuid  ", want: "arena-uuid"},
		{input: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
		{input: "edge!~id", want: "edge!~id"},
	}
	for _, tc := range valid {
		arenaID, err := domain.ParseArenaID(tc.input)
		if err != nil {
			t.Fatalf("ParseArenaID(%q) error = %v", tc.input, err)
		}
		if arenaID.String() != tc.want || arenaID.IsZero() {
			t.Errorf("ParseArenaID(%q) = %q", tc.input, arenaID.String())
		}
	}

	invalid := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyArenaID},
		{name: "blank", input: "   ", want: domain.ErrEmptyArenaID},
		{name: "too long", input: strings.Repeat("a", 65), want: domain.ErrInvalidArenaID},
		{name: "inner space", input: "arena id", want: domain.ErrInvalidArenaID},
		{name: "inner DEL", input: "arena\x7fid", want: domain.ErrInvalidArenaID},
		{name: "newline", input: "arena\nid", want: domain.ErrInvalidArenaID},
		{name: "non-ascii", input: "arena-ção", want: domain.ErrInvalidArenaID},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			arenaID, err := domain.ParseArenaID(tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseArenaID(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if !arenaID.IsZero() {
				t.Fatal("failed parse must yield the zero ArenaID")
			}
		})
	}

	left, _ := domain.ParseArenaID("arena-1")
	same, _ := domain.ParseArenaID("arena-1")
	other, _ := domain.ParseArenaID("arena-2")
	if !left.Equals(same) || left.Equals(other) || left.Equals(domain.ArenaID{}) {
		t.Error("arena id equality semantics are inconsistent")
	}
}
