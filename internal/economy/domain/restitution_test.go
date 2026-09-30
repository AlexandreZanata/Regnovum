package domain_test

import (
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func TestParseRestitutionCause(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"broken_contract", "paying_suspension", "chargeback"} {
		cause, err := domain.ParseRestitutionCause(raw)
		if err != nil {
			t.Errorf("ParseRestitutionCause(%q): %v", raw, err)
		} else if cause.String() != raw {
			t.Errorf("ParseRestitutionCause(%q).String() = %q", raw, cause.String())
		}
	}
	for _, raw := range []string{"", "BROKEN_CONTRACT", "broken-contract", "broken", "decree", "compensation", " broken_contract", "broken_contract "} {
		if _, err := domain.ParseRestitutionCause(raw); !errors.Is(err, domain.ErrInvalidDisbursement) {
			t.Errorf("ParseRestitutionCause(%q) = %v, want ErrInvalidDisbursement", raw, err)
		}
	}
}

func TestAllRestitutionCausesAreExclusive(t *testing.T) {
	t.Parallel()

	causes := domain.AllRestitutionCauses()
	if len(causes) != 3 {
		t.Fatalf("causes = %d, want 3 platform-error scenarios", len(causes))
	}
	seen := map[domain.RestitutionCause]bool{}
	for _, cause := range causes {
		if !cause.IsValid() {
			t.Errorf("cause %q is not valid: the list must hold the closed vocabulary", cause)
		}
		if seen[cause] {
			t.Errorf("cause %q appears twice", cause)
		}
		seen[cause] = true
	}
}
