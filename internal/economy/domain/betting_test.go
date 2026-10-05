package domain_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func validHypotheticalBettingPreconditions() domain.BettingPreconditions {
	return domain.BettingPreconditions{
		CountryCode:               "BR",
		LegalReviewPassed:         true,
		RegulatoryLicenseGranted:  true,
		AgeAndLocationVerified:    true,
		AntiMoneyLaunderingPassed: true,
		ObjectiveEvent:            true,
		IndependentOracle:         true,
		ConflictsRestricted:       true,
		InvolvesRealHarm:          false,
		Status:                    domain.BettingMarketStatusPendingCountryReview,
	}
}

func TestValidateBettingPreconditionsInvariants(t *testing.T) {
	t.Parallel()

	// 1. Valid preconditions still return ErrBettingMarketsUnavailable (disabled by default).
	p := validHypotheticalBettingPreconditions()
	err := domain.ValidateBettingPreconditions(p)
	if !errors.Is(err, domain.ErrBettingMarketsUnavailable) {
		t.Fatalf("valid preconditions err = %v, want ErrBettingMarketsUnavailable", err)
	}

	// 2. Real harm is unconditionally barred with ErrRealHarmProhibited.
	harmful := p
	harmful.InvolvesRealHarm = true
	if err := domain.ValidateBettingPreconditions(harmful); !errors.Is(err, domain.ErrRealHarmProhibited) {
		t.Fatalf("real harm err = %v, want ErrRealHarmProhibited", err)
	}

	// 3. Each individual omitted precondition must fail with ErrBettingPreconditionMissing.
	cases := []struct {
		name   string
		mutate func(*domain.BettingPreconditions)
	}{
		{"missing legal review", func(cp *domain.BettingPreconditions) { cp.LegalReviewPassed = false }},
		{"missing regulatory license", func(cp *domain.BettingPreconditions) { cp.RegulatoryLicenseGranted = false }},
		{"missing age/location verification", func(cp *domain.BettingPreconditions) { cp.AgeAndLocationVerified = false }},
		{"missing AML clearance", func(cp *domain.BettingPreconditions) { cp.AntiMoneyLaunderingPassed = false }},
		{"subjective event", func(cp *domain.BettingPreconditions) { cp.ObjectiveEvent = false }},
		{"missing oracle", func(cp *domain.BettingPreconditions) { cp.IndependentOracle = false }},
		{"unrestricted conflicts", func(cp *domain.BettingPreconditions) { cp.ConflictsRestricted = false }},
		{"empty country code", func(cp *domain.BettingPreconditions) { cp.CountryCode = "" }},
		{"short country code", func(cp *domain.BettingPreconditions) { cp.CountryCode = "X" }},
		{"bad status", func(cp *domain.BettingPreconditions) { cp.Status = "ativo" }},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bad := validHypotheticalBettingPreconditions()
			tc.mutate(&bad)
			if err := domain.ValidateBettingPreconditions(bad); !errors.Is(err, domain.ErrBettingPreconditionMissing) {
				t.Fatalf("%s err = %v, want ErrBettingPreconditionMissing", tc.name, err)
			}
		})
	}
}

func TestAreBettingMarketsAvailableIsFalse(t *testing.T) {
	t.Parallel()
	if domain.AreBettingMarketsAvailable() {
		t.Fatal("AreBettingMarketsAvailable returned true; must be false by default")
	}
}

func TestAttemptPlaceBetAlwaysRejects(t *testing.T) {
	t.Parallel()
	if err := domain.AttemptPlaceBet(); !errors.Is(err, domain.ErrBettingMarketsUnavailable) {
		t.Fatalf("AttemptPlaceBet err = %v, want ErrBettingMarketsUnavailable", err)
	}
}

func TestAttemptCreateMarketAlwaysRejects(t *testing.T) {
	t.Parallel()
	if err := domain.AttemptCreateMarket(); !errors.Is(err, domain.ErrBettingMarketsUnavailable) {
		t.Fatalf("AttemptCreateMarket err = %v, want ErrBettingMarketsUnavailable", err)
	}
}

func TestAttemptSettleBetAlwaysRejects(t *testing.T) {
	t.Parallel()
	if err := domain.AttemptSettleBet(); !errors.Is(err, domain.ErrBettingMarketsUnavailable) {
		t.Fatalf("AttemptSettleBet err = %v, want ErrBettingMarketsUnavailable", err)
	}
}

func TestParseBettingMarketStatus(t *testing.T) {
	t.Parallel()
	for _, valid := range []domain.BettingMarketStatus{
		domain.BettingMarketStatusDisabledByDefault,
		domain.BettingMarketStatusPendingCountryReview,
	} {
		parsed, err := domain.ParseBettingMarketStatus(string(valid))
		if err != nil || parsed != valid {
			t.Fatalf("ParseBettingMarketStatus(%q) = %v, %v", valid, parsed, err)
		}
		if parsed.String() != string(valid) {
			t.Fatalf("String() = %q, want %q", parsed.String(), valid)
		}
	}
	if _, err := domain.ParseBettingMarketStatus("habilitado"); !errors.Is(err, domain.ErrBettingPreconditionMissing) {
		t.Fatalf("unknown status err = %v, want ErrBettingPreconditionMissing", err)
	}
}

func TestReflectionWithoutBettingProductionFields(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(domain.BettingPreconditions{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		for _, forbidden := range []string{"Production", "Active", "Live", "Checkout", "Bypass", "Wallet"} {
			if name == forbidden {
				t.Errorf("BettingPreconditions contains production bypass field %q", name)
			}
		}
	}
}
