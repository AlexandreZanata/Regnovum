package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubStockRepository answers one vault position from canned values:
// the conservative arithmetic under test never touches a database
// through it.
type stubStockRepository struct {
	called    int
	balance   int64
	committed int64
	err       error
}

func (s *stubStockRepository) ReadStock(_ context.Context, _ domain.TreasuryVault, _ domain.SeasonKey) (int64, int64, error) {
	s.called++
	return s.balance, s.committed, s.err
}

func TestSellableStockUseCaseDisplaysConservatively(t *testing.T) {
	t.Parallel()

	stub := &stubStockRepository{balance: 400, committed: 600}
	useCase := application.NewSellableStockUseCase(stub)
	report, err := useCase.Execute(context.Background(), application.SellableStockCommand{Vault: "commercial_stock", Season: domain.CompatSeasonKey})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if report.Available != 400 || report.Balance != 400 || report.Committed != 600 {
		t.Fatalf("report = %+v, want balance/available 400 with 600 committed aside", report)
	}
	if report.Available > report.Balance {
		t.Fatalf("displayed %d above balance %d: the display must stay conservative", report.Available, report.Balance)
	}
}

func TestSellableStockUseCaseRefusesUnknownVault(t *testing.T) {
	t.Parallel()

	stub := &stubStockRepository{}
	useCase := application.NewSellableStockUseCase(stub)
	if _, err := useCase.Execute(context.Background(), application.SellableStockCommand{Vault: "slush", Season: domain.CompatSeasonKey}); !errors.Is(err, domain.ErrUnknownCustody) {
		t.Errorf("Execute = %v, want ErrUnknownCustody", err)
	}
	if stub.called != 0 {
		t.Errorf("unknown vault reached the repository")
	}
}

func TestSaleCheckUseCaseRefusesShortStockWithoutCharge(t *testing.T) {
	t.Parallel()

	t.Run("zero stock", func(t *testing.T) {
		t.Parallel()

		stub := &stubStockRepository{}
		useCase := application.NewSaleCheckUseCase(stub)
		if _, err := useCase.Execute(context.Background(), application.SaleCheckCommand{Vault: "commercial_stock", Season: domain.CompatSeasonKey, Millis: 100}); !errors.Is(err, domain.ErrInsufficientMilliInk) {
			t.Errorf("Execute = %v, want ErrInsufficientMilliInk", err)
		}
	})
	t.Run("short stock", func(t *testing.T) {
		t.Parallel()

		stub := &stubStockRepository{balance: 400, committed: 600}
		useCase := application.NewSaleCheckUseCase(stub)
		if _, err := useCase.Execute(context.Background(), application.SaleCheckCommand{Vault: "commercial_stock", Season: domain.CompatSeasonKey, Millis: 401}); !errors.Is(err, domain.ErrInsufficientMilliInk) {
			t.Errorf("Execute = %v, want ErrInsufficientMilliInk", err)
		}
	})
	t.Run("fitting sale", func(t *testing.T) {
		t.Parallel()

		stub := &stubStockRepository{balance: 400, committed: 600}
		useCase := application.NewSaleCheckUseCase(stub)
		report, err := useCase.Execute(context.Background(), application.SaleCheckCommand{Vault: "commercial_stock", Season: domain.CompatSeasonKey, Millis: 400})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if report.Available != 400 {
			t.Fatalf("available = %d, want 400", report.Available)
		}
	})
	t.Run("zero quantity", func(t *testing.T) {
		t.Parallel()

		stub := &stubStockRepository{balance: 400}
		useCase := application.NewSaleCheckUseCase(stub)
		if _, err := useCase.Execute(context.Background(), application.SaleCheckCommand{Vault: "commercial_stock", Season: domain.CompatSeasonKey, Millis: 0}); !errors.Is(err, domain.ErrInvalidGrant) {
			t.Errorf("Execute = %v, want ErrInvalidGrant", err)
		}
	})
}
