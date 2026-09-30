package domain_test

import (
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func TestParseCharterVersion(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"v1", "v2", "v36"} {
		version, err := domain.ParseCharterVersion(raw)
		if err != nil {
			t.Errorf("ParseCharterVersion(%q): %v", raw, err)
		} else if version.String() != raw {
			t.Errorf("ParseCharterVersion(%q).String() = %q", raw, version.String())
		}
	}
	for _, raw := range []string{"", "1", "v0", "V1", "v01", "latest", "ｖ１", "v1 ", " v1", "v1.0"} {
		if _, err := domain.ParseCharterVersion(raw); !errors.Is(err, domain.ErrInvalidCharter) {
			t.Errorf("ParseCharterVersion(%q) = %v, want ErrInvalidCharter", raw, err)
		}
	}
}

func TestConsentDecisionAndPreservedRights(t *testing.T) {
	t.Parallel()

	accepted, err := domain.ParseConsentDecision("accepted")
	if err != nil {
		t.Fatalf("ParseConsentDecision(accepted): %v", err)
	}
	refused, err := domain.ParseConsentDecision("refused")
	if err != nil {
		t.Fatalf("ParseConsentDecision(refused): %v", err)
	}
	if _, err := domain.ParseConsentDecision("maybe"); !errors.Is(err, domain.ErrInvalidCharter) {
		t.Fatalf("ParseConsentDecision(maybe) = %v, want ErrInvalidCharter", err)
	}
	kept := map[string]bool{}
	for _, right := range domain.PreservedRights(refused) {
		kept[right] = true
	}
	for _, right := range []string{"history", "export", "recourse", "settlement"} {
		if !kept[right] {
			t.Errorf("refusal drops %q: earlier rights stay intact", right)
		}
	}
	if len(domain.PreservedRights(accepted)) != len(kept)+1 {
		t.Errorf("acceptance adds exactly the new-activities right")
	}
}

func TestParseConversionRate(t *testing.T) {
	t.Parallel()

	rate, err := domain.ParseConversionRate(10, 1)
	if err != nil {
		t.Fatalf("ParseConversionRate(10, 1): %v", err)
	}
	if !rate.Equals(rate) {
		t.Fatalf("rate does not equal itself")
	}
	other, err := domain.ParseConversionRate(10, 2)
	if err != nil {
		t.Fatalf("ParseConversionRate(10, 2): %v", err)
	}
	if rate.Equals(other) {
		t.Fatalf("distinct rates compare equal")
	}
	for _, pair := range [][2]int64{{0, 1}, {10, 0}, {-1, 1}, {1, -1}} {
		if _, err := domain.ParseConversionRate(pair[0], pair[1]); !errors.Is(err, domain.ErrInvalidCharter) {
			t.Errorf("ParseConversionRate(%d, %d) = %v, want ErrInvalidCharter", pair[0], pair[1], err)
		}
	}
}
