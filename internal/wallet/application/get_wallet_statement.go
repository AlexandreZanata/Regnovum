package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// GetWalletStatementUseCase answers the paginated owner statement. Pages are
// keyset-based: the cursor only moves backward from the last delivered
// entry, so entries can never be duplicated or skipped, and the account
// filter is applied on every page independently of the cursor.
type GetWalletStatementUseCase struct {
	queries WalletQueryRepository
	cursors *StatementCursorCodec
}

// NewGetWalletStatementUseCase creates an instance of
// GetWalletStatementUseCase with signed cursors.
func NewGetWalletStatementUseCase(queries WalletQueryRepository, cursors *StatementCursorCodec) *GetWalletStatementUseCase {
	return &GetWalletStatementUseCase{queries: queries, cursors: cursors}
}

// Execute returns one page of the statement plus the cursor of the next
// page (empty when the page is the last one). Limits are clamped to the
// contract bounds: default 20, maximum 100.
func (uc *GetWalletStatementUseCase) Execute(ctx context.Context, accountID domain.AccountID, cursor string, limit int) (*WalletStatement, error) {
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	after, err := uc.cursors.Decode(cursor)
	if err != nil {
		return nil, err
	}

	switch {
	case limit <= 0:
		limit = DefaultStatementLimit
	case limit > MaxStatementLimit:
		limit = MaxStatementLimit
	}

	entries, err := uc.queries.ListStatementPage(ctx, accountID, after, limit+1)
	if err != nil {
		return nil, err
	}

	statement := &WalletStatement{Entries: entries}
	if len(entries) > limit {
		statement.Entries = entries[:limit]
		statement.NextCursor = uc.cursors.Encode(statement.Entries[limit-1])
	}
	return statement, nil
}
