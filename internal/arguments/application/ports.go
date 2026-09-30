package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

// Clock reports the current instant. The application never reads the system
// clock directly.
type Clock interface {
	Now() time.Time
}

// UnitOfWork runs a function inside one database transaction. Publication
// uses it so the INK debit, the argument and its sources commit or roll
// back together; the concrete manager is composed at bootstrap.
type UnitOfWork interface {
	// WithinTransaction begins a transaction, makes it available to
	// participants through the context and commits only when fn returns nil.
	WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// AccountEligibility answers whether an account may publish arguments. The
// arguments module never decides account policy: an identity adapter
// answers this consumer-oriented port.
type AccountEligibility interface {
	// EnsureEligible returns ErrAccountNotFound for unknown accounts,
	// ErrAccountNotEligible for accounts that may not publish yet and
	// ErrAccountSuspended for suspended accounts.
	EnsureEligible(ctx context.Context, accountID domain.AccountID) error
}

// ArenaEligibility answers whether an Arena accepts new arguments. The
// arenas module owns the lifecycle: an adapter maps unknown identifiers
// into ErrArenaNotFound and every non-published state into ErrArenaNotOpen.
type ArenaEligibility interface {
	// EnsureAcceptsArguments returns ErrArenaNotFound or ErrArenaNotOpen.
	EnsureAcceptsArguments(ctx context.Context, arenaID domain.ArenaID) error
}

// InkDebitRequest is one publication charge: the author pays the grapheme
// cost of the content in INK, under the attempt idempotency key.
type InkDebitRequest struct {
	AccountID      string
	Amount         int64
	Reference      string
	IdempotencyKey string
}

// InkDebitResult reports whether the charge resolved an earlier attempt.
type InkDebitResult struct {
	Replayed bool
}

// InkDebit charges the publication cost to the author's wallet. The wallet
// adapter answers this consumer-oriented port, joins the caller transaction
// and translates its error vocabulary (insufficient balance, at least).
type InkDebit interface {
	Debit(ctx context.Context, request InkDebitRequest) (InkDebitResult, error)
}

// PublishedArgument is the stored argument projection returned to callers.
type PublishedArgument struct {
	ID        domain.ArgumentID
	ArenaID   domain.ArenaID
	AuthorID  domain.AccountID
	ParentID  domain.ArgumentID
	Relation  domain.Relation
	Content   domain.Content
	Status    string
	CreatedAt time.Time
	// WithdrawnAt is the author withdrawal instant, or nil while the
	// argument was never withdrawn.
	WithdrawnAt *time.Time
}

// CreateArgumentRequest is a validated argument ready to persist.
type CreateArgumentRequest struct {
	ArenaID        domain.ArenaID
	AuthorID       domain.AccountID
	ParentID       domain.ArgumentID
	Relation       domain.Relation
	Content        domain.Content
	IdempotencyKey domain.IdempotencyKey
	CreatedAt      time.Time
}

// ArgumentRepository persists arguments and their sources.
type ArgumentRepository interface {
	// CreateArgument inserts the argument under the author idempotency key.
	// inserted is false when that key was already used: the stored argument
	// is returned so the caller can resolve a replay.
	CreateArgument(ctx context.Context, request CreateArgumentRequest) (*PublishedArgument, bool, error)

	// CreateArgumentSource attaches one structured source to an argument.
	CreateArgumentSource(ctx context.Context, argumentID domain.ArgumentID, source domain.Source, at time.Time) error

	// GetByAuthorAndIdempotencyKey returns the argument recorded under the
	// key, or ErrArgumentNotFound.
	GetByAuthorAndIdempotencyKey(ctx context.Context, authorID domain.AccountID, key domain.IdempotencyKey) (*PublishedArgument, error)

	// GetParent returns the parent argument together with its derived depth
	// (0 for a top-level argument), or ErrArgumentNotFound. Depth is walked
	// from the chain, never denormalized.
	GetParent(ctx context.Context, argumentID domain.ArgumentID) (*PublishedArgument, int, error)

	// GetForAuthor returns one argument scoped to its author. A foreign
	// argument is deliberately indistinguishable from a missing one.
	GetForAuthor(ctx context.Context, argumentID domain.ArgumentID, authorID domain.AccountID) (*PublishedArgument, error)

	// WithdrawArgument moves a published argument to withdrawn under the
	// author scope, recording the withdrawal instant once. transitioned is
	// false when the status moved concurrently: the caller re-reads and
	// resolves.
	WithdrawArgument(ctx context.Context, argumentID domain.ArgumentID, authorID domain.AccountID, at time.Time) (*PublishedArgument, bool, error)
}
