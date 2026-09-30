package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Statement pagination bounds fixed by the API conventions (x-conventions:
// limit defaults to 20, maximum 100).
const (
	DefaultStatementLimit = 20
	MaxStatementLimit     = 100
)

// WalletBalance is the derived balance of an account, reconstructed
// exclusively from the append-only ledger.
type WalletBalance struct {
	Free      domain.Ink
	Purchased domain.Ink
}

// StatementEntry is one ledger line of the owner statement. Amounts stay
// exact signed integers; dates are converted to RFC 3339/UTC only at the
// transport boundary.
type StatementEntry struct {
	TransactionID string
	OperationID   string
	OperationType domain.OperationType
	Reference     domain.Reference
	Bucket        domain.Bucket
	Amount        int64
	CreatedAt     time.Time
}

// StatementPosition is the decoded keyset cursor position: the last entry
// already delivered to the caller.
type StatementPosition struct {
	CreatedAt     time.Time
	TransactionID string
}

// WalletStatement is one page of the owner statement. An empty NextCursor
// means the page is the last one.
type WalletStatement struct {
	Entries    []StatementEntry
	NextCursor string
}

// WalletQueryRepository exposes the derived balance and the paginated owner
// statement. Both are always scoped by the owning account, so a cursor can
// only move within the caller's own history.
type WalletQueryRepository interface {
	// DerivedBalance recomputes the bucket balances from the ledger.
	DerivedBalance(ctx context.Context, accountID domain.AccountID) (*WalletBalance, error)

	// ListStatementPage returns up to limit entries strictly older than the
	// cursor position, newest first.
	ListStatementPage(ctx context.Context, accountID domain.AccountID, after *StatementPosition, limit int) ([]StatementEntry, error)
}
