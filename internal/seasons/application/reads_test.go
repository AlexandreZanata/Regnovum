package application

import (
	"reflect"
	"strings"
	"testing"
)

func TestSeasonViewCarriesOnlyAllowlist(t *testing.T) {
	fields := []string{}
	for _, field := range reflect.VisibleFields(reflect.TypeOf(SeasonView{})) {
		fields = append(fields, field.Name)
	}
	want := []string{"SeasonKey", "Ordinal", "StartsAt", "EndsAt", "State"}
	if len(fields) != len(want) {
		t.Fatalf("SeasonView fields = %v, want %v: balances and personal data never enter reads", fields, want)
	}
	for i := range want {
		if fields[i] != want[i] {
			t.Fatalf("SeasonView fields = %v, want %v", fields, want)
		}
	}
	encoded := strings.Join(fields, ",")
	for _, banned := range []string{"Balance", "Milli", "Treasury", "Holder", "Monarch", "Regent", "Account", "Email", "Custody", "Genesis"} {
		if strings.Contains(encoded, banned) {
			t.Fatalf("SeasonView carries %q: history is allowlist, never a ledger extract", banned)
		}
	}
}
