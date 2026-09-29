package domain

// TreasuryVault is the closed vocabulary of Treasury vaults: the Genesis
// home and the four exclusive partitions every Treasury unit lives in.
// Vaults follow docs/reino/RESPOSTAS.md item 23 (Q23 proposta): the names
// are structural mechanics only. Q23 stays PENDENTE, so no amount, share
// or percentage is vigente here — later phases fund the vaults from
// existing Treasury stock once the titular ratifies them, and never by
// inference from this vocabulary.
type TreasuryVault string

const (
	// TreasuryVaultGenesisHome is where Genesis is born: the single
	// origin of supply before any allocation moves it.
	TreasuryVaultGenesisHome TreasuryVault = "main"
	// TreasuryVaultSovereignReserve is the locked sovereign stock.
	TreasuryVaultSovereignReserve TreasuryVault = "sovereign_reserve"
	// TreasuryVaultCommercialStock is the saleable inventory.
	TreasuryVaultCommercialStock TreasuryVault = "commercial_stock"
	// TreasuryVaultOperatingCash is the operational till.
	TreasuryVaultOperatingCash TreasuryVault = "operating_cash"
	// TreasuryVaultFree is the unallocated remainder.
	TreasuryVaultFree TreasuryVault = "free_treasury"
)

// AllTreasuryVaults returns the closed vocabulary in canonical order:
// the Genesis home first, then the four exclusive vaults.
func AllTreasuryVaults() []TreasuryVault {
	return []TreasuryVault{
		TreasuryVaultGenesisHome,
		TreasuryVaultSovereignReserve,
		TreasuryVaultCommercialStock,
		TreasuryVaultOperatingCash,
		TreasuryVaultFree,
	}
}

// ParseTreasuryVault validates a vault name against the schema CHECK
// vocabulary. Matching is exact: surrounding whitespace is not trimmed,
// so two spellings can never name one vault. Unknown vaults are refused.
func ParseTreasuryVault(raw string) (TreasuryVault, error) {
	vault := TreasuryVault(raw)
	if !vault.IsValid() {
		return "", ErrUnknownCustody
	}
	return vault, nil
}

// IsValid reports whether the vault belongs to the closed vocabulary.
func (v TreasuryVault) IsValid() bool {
	switch v {
	case TreasuryVaultGenesisHome,
		TreasuryVaultSovereignReserve,
		TreasuryVaultCommercialStock,
		TreasuryVaultOperatingCash,
		TreasuryVaultFree:
		return true
	default:
		return false
	}
}

// String returns the stored vault value.
func (v TreasuryVault) String() string { return string(v) }
