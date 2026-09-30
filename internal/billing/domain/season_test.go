package domain

import (
	"errors"
	"testing"
)

func TestPurchaseSeasonRequiresResetOutsideCompat(t *testing.T) {
	if err := RequireSeasonalReset(SeasonKey(CompatSeasonKey), false); err != nil {
		t.Fatalf("compat without reset = %v, want nil", err)
	}
	if err := RequireSeasonalReset(SeasonKey("temporada-1"), true); err != nil {
		t.Fatalf("seasonal with reset = %v, want nil", err)
	}
	if err := RequireSeasonalReset(SeasonKey("temporada-1"), false); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("seasonal without reset = %v, want ErrInvalidSeason", err)
	}
	if _, err := ParseSeasonKey("   "); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("blank season = %v, want ErrInvalidSeason", err)
	}
}
