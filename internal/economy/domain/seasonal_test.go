package domain

import (
	"errors"
	"testing"
)

func TestRequireSeasonalReset(t *testing.T) {
	if err := RequireSeasonalReset(SeasonKey(CompatSeasonKey), false); err != nil {
		t.Fatalf("compat without reset = %v, want nil (legacy path keeps its contract)", err)
	}
	if err := RequireSeasonalReset(SeasonKey("temporada-1"), true); err != nil {
		t.Fatalf("seasonal with reset = %v, want nil", err)
	}
	if err := RequireSeasonalReset(SeasonKey("temporada-1"), false); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("seasonal without reset = %v, want ErrConsentRequired", err)
	}
}
