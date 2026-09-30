package domain_test

import (
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

func TestParseRelationAcceptsOnlyCanonicalVocabulary(t *testing.T) {
	valid := []struct {
		raw  string
		want string
	}{
		{raw: domain.RelationSupport, want: domain.RelationSupport},
		{raw: domain.RelationOppose, want: domain.RelationOppose},
		{raw: domain.RelationContext, want: domain.RelationContext},
		{raw: "  support  ", want: domain.RelationSupport},
	}
	for _, test := range valid {
		relation, err := domain.ParseRelation(test.raw)
		if err != nil {
			t.Fatalf("ParseRelation(%q) error = %v", test.raw, err)
		}
		if relation.String() != test.want || !relation.IsSupported() || relation.IsZero() {
			t.Fatalf("ParseRelation(%q) = %q, want the supported %q", test.raw, relation.String(), test.want)
		}
	}

	invalid := []struct {
		name string
		raw  string
		want error
	}{
		{name: "empty", raw: "", want: domain.ErrEmptyRelation},
		{name: "blank", raw: "   ", want: domain.ErrEmptyRelation},
		{name: "uppercase", raw: "Support", want: domain.ErrInvalidRelation},
		{name: "unknown", raw: "neutral", want: domain.ErrInvalidRelation},
		{name: "portuguese", raw: "a-favor", want: domain.ErrInvalidRelation},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := domain.ParseRelation(test.raw); !errors.Is(err, test.want) {
				t.Fatalf("ParseRelation(%q) error = %v, want %v", test.raw, err, test.want)
			}
		})
	}

	relations := domain.SupportedRelations()
	if len(relations) != 3 {
		t.Fatalf("SupportedRelations() = %v, want exactly three values", relations)
	}
	want := []string{domain.RelationSupport, domain.RelationOppose, domain.RelationContext}
	for index, relation := range relations {
		if relation.String() != want[index] {
			t.Fatalf("SupportedRelations()[%d] = %q, want %q", index, relation.String(), want[index])
		}
	}
	if (domain.Relation{}).IsSupported() || !(domain.Relation{}).IsZero() {
		t.Fatal("zero Relation must be unsupported and zero")
	}
}
