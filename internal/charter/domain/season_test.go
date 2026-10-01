package domain

import (
	"errors"
	"testing"
)

func TestSeasonalTermsRequireFullDisclosure(t *testing.T) {
	if _, err := ParseSeasonalTerms(7776000, true, true, "disputa do trono por riqueza sazonal", true); err != nil {
		t.Fatalf("valid seasonal terms = %v, want acceptance", err)
	}
	cases := []struct {
		name                string
		carry, history, ack bool
		function            string
		resetSeconds        int
	}{
		{"reset omitido", true, true, true, "funcao real", 0},
		{"reset civil", true, true, true, "funcao real", 90},
		{"carry-over prometido", false, true, true, "funcao real", 7776000},
		{"historia apagada", true, false, true, "funcao real", 7776000},
		{"funcao vazia", true, true, true, "", 7776000},
		{"funcao em branco", true, true, true, "   ", 7776000},
		{"sem aceite explicito", true, true, false, "funcao real", 7776000},
	}
	for _, tc := range cases {
		if _, err := ParseSeasonalTerms(tc.resetSeconds, tc.carry, tc.history, tc.function, tc.ack); !errors.Is(err, ErrInvalidCharter) {
			t.Errorf("%s = %v, want ErrInvalidCharter: omitted disclosure never authorizes", tc.name, err)
		}
	}
}

func TestSeasonalAcceptanceGrantsNoBalanceOrRole(t *testing.T) {
	chain := consentChain(t)
	ledger, accepted := mustRecord(t, chain, nil, consentReq())
	if len(RightsOf(accepted.Decision)) != 5 {
		t.Fatalf("acceptance rights = %v, want exactly the preserved four plus new activities", RightsOf(accepted.Decision))
	}
	for _, right := range RightsOf(accepted.Decision) {
		if string(right) == "balance" || string(right) == "cargo" || string(right) == "saldo" || string(right) == "role" {
			t.Fatalf("acceptance right %q mints balance or role: accepting the Charter grants nothing economic", right)
		}
	}
	terms, err := ParseSeasonalTerms(7776000, true, true, "disputa do trono por riqueza sazonal", true)
	if err != nil {
		t.Fatalf("ParseSeasonalTerms: %v", err)
	}
	if !terms.AuthorizesSeasonalConversion() {
		t.Fatal("valid terms must authorize seasonal conversion")
	}
	legacy := SeasonalTerms{}
	if legacy.AuthorizesSeasonalConversion() {
		t.Fatal("omitted reset authorized: old acceptance alone must never convert seasonally")
	}
	_ = ledger
}
