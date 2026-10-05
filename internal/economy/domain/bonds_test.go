package domain_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func validHypotheticalTerms() domain.HypotheticalBondTerms {
	return domain.HypotheticalBondTerms{
		Custody:          domain.CustodyTitle,
		DurationDays:     90,
		WeightFactor:     1,
		YieldCapped:      true,
		YieldGuaranteed:  false,
		FundedByEmission: false,
		Status:           domain.BondStatusPendingLegalReview,
	}
}

func TestValidateHypotheticalContractInvariants(t *testing.T) {
	t.Parallel()

	// Valid terms must fail-closed with ErrBondsUnavailable (concept only).
	terms := validHypotheticalTerms()
	err := domain.ValidateHypotheticalContract(terms)
	if !errors.Is(err, domain.ErrBondsUnavailable) {
		t.Fatalf("valid terms err = %v, want ErrBondsUnavailable", err)
	}

	// Unsegregated custody must fail with ErrUnauthorizedCustody.
	for _, badCustody := range []domain.CustodyKind{
		domain.CustodyUser,
		domain.CustodyTreasury,
		domain.CustodyEscrow,
		domain.CustodyContract,
		"outra",
	} {
		bad := terms
		bad.Custody = badCustody
		if err := domain.ValidateHypotheticalContract(bad); !errors.Is(err, domain.ErrUnauthorizedCustody) {
			t.Errorf("custody %q err = %v, want ErrUnauthorizedCustody", badCustody, err)
		}
	}

	// Guaranteed yield is strictly forbidden.
	guaranteed := terms
	guaranteed.YieldGuaranteed = true
	if err := domain.ValidateHypotheticalContract(guaranteed); !errors.Is(err, domain.ErrBondYieldForbidden) {
		t.Errorf("guaranteed yield err = %v, want ErrBondYieldForbidden", err)
	}

	// Emission funding is strictly forbidden.
	emission := terms
	emission.FundedByEmission = true
	if err := domain.ValidateHypotheticalContract(emission); !errors.Is(err, domain.ErrInvalidGenesisKey) {
		t.Errorf("emission funding err = %v, want ErrInvalidGenesisKey", err)
	}

	// Uncapped yield is strictly forbidden.
	uncapped := terms
	uncapped.YieldCapped = false
	if err := domain.ValidateHypotheticalContract(uncapped); !errors.Is(err, domain.ErrBondYieldForbidden) {
		t.Errorf("uncapped yield err = %v, want ErrBondYieldForbidden", err)
	}

	// Non-positive duration or weight is invalid.
	badDuration := terms
	badDuration.DurationDays = 0
	if err := domain.ValidateHypotheticalContract(badDuration); !errors.Is(err, domain.ErrInvalidMilliInk) {
		t.Errorf("duration 0 err = %v, want ErrInvalidMilliInk", err)
	}

	badWeight := terms
	badWeight.WeightFactor = -1
	if err := domain.ValidateHypotheticalContract(badWeight); !errors.Is(err, domain.ErrInvalidMilliInk) {
		t.Errorf("weight -1 err = %v, want ErrInvalidMilliInk", err)
	}

	badStatus := terms
	badStatus.Status = "ativo"
	if err := domain.ValidateHypotheticalContract(badStatus); !errors.Is(err, domain.ErrUnknownCustody) {
		t.Errorf("bad status err = %v, want ErrUnknownCustody", err)
	}
}

func TestAreBondsAvailableIsFalse(t *testing.T) {
	t.Parallel()
	if domain.AreBondsAvailable() {
		t.Fatal("AreBondsAvailable returned true; must be false pending legal review")
	}
}

func TestEvaluateBondPurchaseAlwaysRejects(t *testing.T) {
	t.Parallel()
	if err := domain.EvaluateBondPurchase(); !errors.Is(err, domain.ErrBondsUnavailable) {
		t.Fatalf("EvaluateBondPurchase err = %v, want ErrBondsUnavailable", err)
	}
}

func TestEvaluateBondYieldPublicationAlwaysRejects(t *testing.T) {
	t.Parallel()
	for _, rate := range []float64{0.0, 0.05, 0.10, -0.01} {
		if err := domain.EvaluateBondYieldPublication(rate); !errors.Is(err, domain.ErrBondYieldForbidden) {
			t.Errorf("rate %f err = %v, want ErrBondYieldForbidden", rate, err)
		}
	}
}

func TestPrincipalSegregationCannotSpend(t *testing.T) {
	t.Parallel()
	// Invariant: CustodyTitle can never spend by plain transfer.
	if domain.CustodyTitle.CanSpend() {
		t.Fatal("CustodyTitle.CanSpend() = true; principal must never fund ordinary spending")
	}
}

func TestParseHypotheticalBondStatus(t *testing.T) {
	t.Parallel()
	for _, valid := range []domain.HypotheticalBondStatus{
		domain.BondStatusConceptOnly,
		domain.BondStatusPendingLegalReview,
	} {
		parsed, err := domain.ParseHypotheticalBondStatus(string(valid))
		if err != nil || parsed != valid {
			t.Fatalf("ParseHypotheticalBondStatus(%q) = %v, %v", valid, parsed, err)
		}
		if parsed.String() != string(valid) {
			t.Fatalf("String() = %q, want %q", parsed.String(), valid)
		}
	}
	if _, err := domain.ParseHypotheticalBondStatus("unknown"); !errors.Is(err, domain.ErrUnknownCustody) {
		t.Fatalf("unknown status err = %v, want ErrUnknownCustody", err)
	}
}

func TestReflectionWithoutBondProductionFields(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(domain.HypotheticalBondTerms{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		for _, forbidden := range []string{"Production", "Active", "Live", "Checkout", "Stripe"} {
			if name == forbidden {
				t.Errorf("HypotheticalBondTerms contains production field %q", name)
			}
		}
	}
}
