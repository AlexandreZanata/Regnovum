package domain_test

import (
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func TestParseTreasuryVault(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"main", "sovereign_reserve", "commercial_stock", "operating_cash", "free_treasury"} {
		vault, err := domain.ParseTreasuryVault(raw)
		if err != nil {
			t.Errorf("ParseTreasuryVault(%q): %v", raw, err)
		} else if vault.String() != raw {
			t.Errorf("ParseTreasuryVault(%q).String() = %q", raw, vault.String())
		}
	}
	for _, raw := range []string{"", "MAIN", "sovereign-reserve", "reserve", " main", "main ", "treasury", "available", "obligations"} {
		if _, err := domain.ParseTreasuryVault(raw); !errors.Is(err, domain.ErrUnknownCustody) {
			t.Errorf("ParseTreasuryVault(%q) = %v, want ErrUnknownCustody", raw, err)
		}
	}
}

func TestAllTreasuryVaultsAreExclusive(t *testing.T) {
	t.Parallel()

	vaults := domain.AllTreasuryVaults()
	if len(vaults) != 5 {
		t.Fatalf("vaults = %d, want 5 (Genesis home plus four exclusive vaults)", len(vaults))
	}
	seen := map[domain.TreasuryVault]bool{}
	for _, vault := range vaults {
		if !vault.IsValid() {
			t.Errorf("vault %q is not valid: the list must hold the closed vocabulary", vault)
		}
		if seen[vault] {
			t.Errorf("vault %q appears twice: one unit must never read in two vaults", vault)
		}
		seen[vault] = true
	}
	if vaults[0] != domain.TreasuryVaultGenesisHome {
		t.Errorf("first vault = %q, want the Genesis home", vaults[0])
	}
}
