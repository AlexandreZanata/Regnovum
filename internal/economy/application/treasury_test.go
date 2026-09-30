package application_test

import (
	"context"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubTreasuryRepository answers vault positions from canned values: the
// summation logic under test never touches a database through it.
type stubTreasuryRepository struct {
	vaults []application.TreasuryVaultsView
	total  int64
	err    error
}

func (s *stubTreasuryRepository) ReadTreasuryVaults(_ context.Context) ([]application.TreasuryVaultsView, int64, error) {
	return s.vaults, s.total, s.err
}

func balancedVaults() []application.TreasuryVaultsView {
	return []application.TreasuryVaultsView{
		{Vault: domain.TreasuryVaultGenesisHome, Millis: 100, Legs: 1},
		{Vault: domain.TreasuryVaultSovereignReserve, Millis: 20, Legs: 1},
		{Vault: domain.TreasuryVaultCommercialStock, Millis: 30, Legs: 1},
		{Vault: domain.TreasuryVaultOperatingCash, Millis: 40, Legs: 1},
		{Vault: domain.TreasuryVaultFree, Millis: 10, Legs: 1},
	}
}

func TestTreasuryVaultsUseCaseSumsToTreasury(t *testing.T) {
	t.Parallel()

	stub := &stubTreasuryRepository{vaults: balancedVaults(), total: 200}
	useCase := application.NewTreasuryVaultsUseCase(stub)
	report, err := useCase.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(report.Mismatch) != 0 {
		t.Fatalf("Mismatch = %v, want clean", report.Mismatch)
	}
	if report.TotalMillis != 200 || len(report.Vaults) != 5 {
		t.Fatalf("report = %+v, want total 200 over 5 vaults", report)
	}
}

func TestTreasuryVaultsUseCaseRecordsEveryBreak(t *testing.T) {
	t.Parallel()

	t.Run("drifted total", func(t *testing.T) {
		t.Parallel()

		stub := &stubTreasuryRepository{vaults: balancedVaults(), total: 199}
		report, err := application.NewTreasuryVaultsUseCase(stub).Execute(context.Background())
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if len(report.Mismatch) == 0 {
			t.Fatal("drifted total reported clean")
		}
	})
	t.Run("missing vault", func(t *testing.T) {
		t.Parallel()

		stub := &stubTreasuryRepository{vaults: balancedVaults()[:4], total: 190}
		report, err := application.NewTreasuryVaultsUseCase(stub).Execute(context.Background())
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if len(report.Mismatch) == 0 {
			t.Fatal("missing vault reported clean")
		}
	})
	t.Run("duplicated vault", func(t *testing.T) {
		t.Parallel()

		doubled := append(balancedVaults(), application.TreasuryVaultsView{Vault: domain.TreasuryVaultFree, Millis: 10})
		stub := &stubTreasuryRepository{vaults: doubled, total: 210}
		report, err := application.NewTreasuryVaultsUseCase(stub).Execute(context.Background())
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if len(report.Mismatch) == 0 {
			t.Fatal("duplicated vault reported clean")
		}
	})
	t.Run("unknown vault", func(t *testing.T) {
		t.Parallel()

		views := balancedVaults()
		views[0].Vault = "slush"
		stub := &stubTreasuryRepository{vaults: views, total: 200}
		report, err := application.NewTreasuryVaultsUseCase(stub).Execute(context.Background())
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if len(report.Mismatch) == 0 {
			t.Fatal("unknown vault reported clean")
		}
	})
}
