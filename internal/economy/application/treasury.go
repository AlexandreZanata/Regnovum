package application

import (
	"context"
	"fmt"
	"sort"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// TreasuryVaultBalance is one vault position in raw millis: broken books
// can hold negative balances, and the report must show them instead of
// refusing to render.
type TreasuryVaultBalance struct {
	Vault  domain.TreasuryVault
	Millis int64
	Legs   int64
}

// TreasuryReport is the outcome of one exclusive reading of the
// Treasury: the Genesis home plus the four vaults, and the Treasury
// total recomputed independently of the per-vault sums. An empty
// Mismatch means the vaults add up to the Treasury exactly; anything
// else names the break, never silently.
type TreasuryReport struct {
	Vaults      []TreasuryVaultBalance
	TotalMillis int64
	Mismatch    []string
}

// TreasuryVaultsView is one vault position as the repository read it.
type TreasuryVaultsView struct {
	Vault  domain.TreasuryVault
	Millis int64
	Legs   int64
}

// TreasuryCommand names the season book one Treasury reading
// judges. Vaults exist per book: the same label in another book is
// another vault.
type TreasuryCommand struct {
	Season string
}

// TreasuryRepository reads Treasury vault positions without moving
// value. Both reads are side-effect free.
type TreasuryRepository interface {
	// ReadTreasuryVaults resolves one position per Treasury vault of
	// one book and the Treasury total recomputed independently of
	// the per-vault sums. Callers receive the closed vault list,
	// never another holder's custody.
	ReadTreasuryVaults(ctx context.Context, season domain.SeasonKey) (vaults []TreasuryVaultsView, total int64, err error)
}

// TreasuryVaultsUseCase reads the exclusive Treasury vaults and proves
// they add up to the Treasury. It is an internal operation: no public
// surface calls it.
type TreasuryVaultsUseCase struct {
	treasury TreasuryRepository
}

// NewTreasuryVaultsUseCase creates an instance of TreasuryVaultsUseCase.
func NewTreasuryVaultsUseCase(treasury TreasuryRepository) *TreasuryVaultsUseCase {
	return &TreasuryVaultsUseCase{treasury: treasury}
}

// Execute reads one exclusive Treasury report of one season book,
// recording every break instead of failing silent: unknown or
// duplicated vaults, missing vaults and a total the vaults do not
// add up to.
func (uc *TreasuryVaultsUseCase) Execute(ctx context.Context, cmd TreasuryCommand) (*TreasuryReport, error) {
	season, err := domain.ParseSeasonKey(cmd.Season)
	if err != nil {
		return nil, err
	}
	views, total, err := uc.treasury.ReadTreasuryVaults(ctx, season)
	if err != nil {
		return nil, err
	}
	report := &TreasuryReport{TotalMillis: total}
	seen := map[domain.TreasuryVault]bool{}
	var sum int64
	for _, view := range views {
		if !view.Vault.IsValid() {
			report.Mismatch = append(report.Mismatch, fmt.Sprintf("unknown vault %q", view.Vault))
			continue
		}
		if seen[view.Vault] {
			report.Mismatch = append(report.Mismatch, fmt.Sprintf("vault %q counted twice", view.Vault))
			continue
		}
		seen[view.Vault] = true
		sum += view.Millis
		report.Vaults = append(report.Vaults, TreasuryVaultBalance(view))
	}
	for _, want := range domain.AllTreasuryVaults() {
		if !seen[want] {
			report.Mismatch = append(report.Mismatch, fmt.Sprintf("vault %q missing", want))
		}
	}
	if sum != total {
		report.Mismatch = append(report.Mismatch, fmt.Sprintf("vaults sum %d != treasury %d", sum, total))
	}
	sort.Slice(report.Vaults, func(i, j int) bool { return report.Vaults[i].Vault < report.Vaults[j].Vault })
	sort.Strings(report.Mismatch)
	return report, nil
}
