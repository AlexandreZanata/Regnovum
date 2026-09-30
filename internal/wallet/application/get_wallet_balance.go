package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// GetWalletBalanceUseCase answers the derived balance query of the owner.
// The value is recomputed from the ledger, so it never depends on the
// cached projection.
type GetWalletBalanceUseCase struct {
	queries WalletQueryRepository
}

// NewGetWalletBalanceUseCase creates an instance of GetWalletBalanceUseCase.
func NewGetWalletBalanceUseCase(queries WalletQueryRepository) *GetWalletBalanceUseCase {
	return &GetWalletBalanceUseCase{queries: queries}
}

// Execute returns the ledger-derived balance of the account.
func (uc *GetWalletBalanceUseCase) Execute(ctx context.Context, accountID domain.AccountID) (*WalletBalance, error) {
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	return uc.queries.DerivedBalance(ctx, accountID)
}
