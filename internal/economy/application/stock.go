package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// StockReport is one conservative reading of a Treasury vault for sale
// purposes: the journal balance, the amount locked in active
// commitments and the spendable remainder. The display is conservative
// by construction — Available never exceeds Balance, committed funds
// already left the vault legs, and third-party custodies never enter
// any of the three numbers. All values are raw millis: broken books
// render instead of refusing.
type StockReport struct {
	Vault     domain.TreasuryVault
	Balance   int64
	Committed int64
	Available int64
}

// StockRepository reads one vault position without moving value. Both
// reads are side-effect free.
type StockRepository interface {
	// ReadStock resolves the journal balance of one Treasury vault and
	// the amount its active commitments still lock. Vaults never
	// opened read zero.
	ReadStock(ctx context.Context, vault domain.TreasuryVault) (balance, committed int64, err error)
}

// SellableStockCommand names the vault whose sellable funds are
// displayed.
type SellableStockCommand struct {
	Vault string
}

// SaleCheckCommand names the vault and the exact sale quantity probed
// against it. The check moves nothing: it only answers whether the
// quantity fits the conservative available.
type SaleCheckCommand struct {
	Vault  string
	Millis int64
}

// SellableStockUseCase displays one vault's sellable funds. It is an
// internal operation: no public surface calls it.
type SellableStockUseCase struct {
	stock StockRepository
}

// NewSellableStockUseCase creates an instance of SellableStockUseCase.
func NewSellableStockUseCase(stock StockRepository) *SellableStockUseCase {
	return &SellableStockUseCase{stock: stock}
}

// Execute reads the conservative report. Vaults outside the closed
// vocabulary never reach the journal.
func (uc *SellableStockUseCase) Execute(ctx context.Context, cmd SellableStockCommand) (*StockReport, error) {
	vault, err := domain.ParseTreasuryVault(cmd.Vault)
	if err != nil {
		return nil, err
	}
	return readStockReport(ctx, uc.stock, vault)
}

// SaleCheckUseCase probes one sale quantity against the conservative
// available. Short stock refuses before any charge exists to write:
// the refusal itself is the proof no partial sale can happen. It is an
// internal operation: no public surface calls it.
type SaleCheckUseCase struct {
	stock StockRepository
}

// NewSaleCheckUseCase creates an instance of SaleCheckUseCase.
func NewSaleCheckUseCase(stock StockRepository) *SaleCheckUseCase {
	return &SaleCheckUseCase{stock: stock}
}

// Execute refuses short stock without writing anything, and answers
// the conservative report when the quantity fits.
func (uc *SaleCheckUseCase) Execute(ctx context.Context, cmd SaleCheckCommand) (*StockReport, error) {
	vault, err := domain.ParseTreasuryVault(cmd.Vault)
	if err != nil {
		return nil, err
	}
	requested, err := domain.NewMilliInk(cmd.Millis)
	if err != nil {
		return nil, err
	}
	if requested.IsZero() {
		return nil, domain.ErrInvalidGrant
	}
	report, err := readStockReport(ctx, uc.stock, vault)
	if err != nil {
		return nil, err
	}
	if requested.Millis() > report.Available {
		return nil, domain.ErrInsufficientMilliInk
	}
	return report, nil
}

// readStockReport renders one vault report with the conservative
// identity available = balance: committed funds left the vault legs at
// commit time, so whatever the journal still holds is spendable and
// the display can never promise locked or third-party units.
func readStockReport(ctx context.Context, stock StockRepository, vault domain.TreasuryVault) (*StockReport, error) {
	balance, committed, err := stock.ReadStock(ctx, vault)
	if err != nil {
		return nil, err
	}
	return &StockReport{Vault: vault, Balance: balance, Committed: committed, Available: balance}, nil
}
