package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Clock exposes wall-clock time to wallet use cases, keeping them
// deterministic under test (ADR-012).
type Clock interface {
	Now() time.Time
}

// CreditRequest is a validated INK credit to persist atomically.
type CreditRequest struct {
	AccountID      domain.AccountID
	Bucket         domain.Bucket
	OperationType  domain.OperationType
	IdempotencyKey domain.IdempotencyKey
	Reference      domain.Reference
	// Reason and ActorAccountID are mandatory for administrative types and
	// zero for regular credits.
	Reason         domain.Reason
	ActorAccountID domain.AccountID
	// Delta is the signed ledger amount produced by the operation direction
	// (positive for credits).
	Delta     int64
	ChangedAt time.Time
}

// CreditResult is the outcome of a credit: the operation that the
// idempotency key resolves to, and whether it was a replay of an earlier
// attempt (no new transaction or balance change happened).
type CreditResult struct {
	Operation domain.Operation
	Replayed  bool
}

// CreditRepository persists credits and their idempotency registry.
type CreditRepository interface {
	// ApplyCredit atomically ensures the wallet, stores the operation under
	// its idempotency key, records the bucket transaction and updates the
	// balance. The same key resolves to the original operation untouched.
	ApplyCredit(ctx context.Context, request CreditRequest) (*CreditResult, error)
}

// DebitRequest is a validated INK debit to persist atomically. The bucket
// split is not part of the request: it is computed inside the repository
// transaction, under the wallet lock, following the mandatory priority.
type DebitRequest struct {
	AccountID      domain.AccountID
	OperationType  domain.OperationType
	IdempotencyKey domain.IdempotencyKey
	Reference      domain.Reference
	// Reason and ActorAccountID are mandatory for administrative types and
	// zero for regular debits.
	Reason         domain.Reason
	ActorAccountID domain.AccountID
	Amount         domain.Ink
	ChangedAt      time.Time
}

// DebitResult is the outcome of a debit: the operation the idempotency key
// resolves to, the consumption plan actually applied (reconstructed from the
// ledger on replays) and whether it was a replay.
type DebitResult struct {
	Operation  domain.Operation
	Allocation domain.Allocation
	Replayed   bool
}

// DebitRepository persists debits and their idempotency registry.
type DebitRepository interface {
	// ApplyDebit locks the wallet, plans the bucket consumption by priority,
	// stores the operation under its idempotency key, records one line per
	// consumed bucket and updates the balances atomically. Insufficient
	// balance leaves no partial state. The same key resolves to the original
	// operation untouched.
	ApplyDebit(ctx context.Context, request DebitRequest) (*DebitResult, error)
}
