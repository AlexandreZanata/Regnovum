package domain_test

import (
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func TestParseDisbursementPurpose(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"sale_settlement", "crumb_distribution", "compensation", "due_payment"} {
		purpose, err := domain.ParseDisbursementPurpose(raw)
		if err != nil {
			t.Errorf("ParseDisbursementPurpose(%q): %v", raw, err)
		} else if purpose.String() != raw {
			t.Errorf("ParseDisbursementPurpose(%q).String() = %q", raw, purpose.String())
		}
	}
	for _, raw := range []string{"", "SALE_SETTLEMENT", "sale-settlement", "sale", "decree", " sale_settlement", "sale_settlement "} {
		if _, err := domain.ParseDisbursementPurpose(raw); !errors.Is(err, domain.ErrInvalidDisbursement) {
			t.Errorf("ParseDisbursementPurpose(%q) = %v, want ErrInvalidDisbursement", raw, err)
		}
	}
}

func TestAllDisbursementPurposesAreExclusive(t *testing.T) {
	t.Parallel()

	purposes := domain.AllDisbursementPurposes()
	if len(purposes) != 4 {
		t.Fatalf("purposes = %d, want 4 obligation classes", len(purposes))
	}
	seen := map[domain.DisbursementPurpose]bool{}
	for _, purpose := range purposes {
		if !purpose.IsValid() {
			t.Errorf("purpose %q is not valid: the list must hold the closed vocabulary", purpose)
		}
		if seen[purpose] {
			t.Errorf("purpose %q appears twice", purpose)
		}
		seen[purpose] = true
	}
}
