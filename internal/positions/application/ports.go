package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// Clock reports the current instant. The application never reads the system
// clock directly, so tests and future workers control time.
type Clock interface {
	Now() time.Time
}

// AccountEligibility answers whether an account may confirm positions. The
// positions module never decides account policy: an identity adapter answers
// this consumer-oriented port, mapping its own state into the errors below.
type AccountEligibility interface {
	// EnsureEligible returns ErrAccountNotFound for unknown accounts,
	// ErrAccountNotEligible for accounts that may not participate yet
	// (pending email verification, for example) and ErrAccountSuspended
	// for suspended accounts.
	EnsureEligible(ctx context.Context, accountID domain.AccountID) error
}

// ArenaEligibility answers whether an Arena accepts new positions. The
// arenas module owns the lifecycle: an adapter answers this
// consumer-oriented port, mapping unknown identifiers into ErrArenaNotFound
// and every non-published state (draft, closed, restricted, removed) into
// ErrArenaNotOpen.
type ArenaEligibility interface {
	// EnsureAcceptsPositions returns ErrArenaNotFound or ErrArenaNotOpen.
	EnsureAcceptsPositions(ctx context.Context, arenaID domain.ArenaID) error
}

// UnitOfWork runs a function inside one database transaction. The change
// use case uses it so the appended history row and the projection update
// commit or roll back together; the concrete manager is composed at
// bootstrap and the module never imports another module's adapters.
type UnitOfWork interface {
	// WithinTransaction begins a transaction, makes it available to
	// participants through the context and commits only when fn returns nil.
	WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// PositionChangeRecord is one stored history row: the append-only change
// plus its database identifier, kept for later attribution.
type PositionChangeRecord struct {
	ID     string
	Change domain.PositionChange
}

// PositionRepository persists the private position projection and its
// append-only history.
type PositionRepository interface {
	// GetByAccountAndArena returns the stored projection of one account in
	// one Arena, or ErrPositionNotFound.
	GetByAccountAndArena(ctx context.Context, arenaID domain.ArenaID, accountID domain.AccountID) (*domain.DebatePosition, error)

	// ConfirmInitialPosition atomically inserts the initial projection.
	// inserted is false when the pair already holds a position: the stored
	// projection is returned so the caller can resolve a replay.
	ConfirmInitialPosition(ctx context.Context, arenaID domain.ArenaID, accountID domain.AccountID, position domain.Position, at time.Time) (*domain.DebatePosition, bool, error)

	// CreatePositionChange appends one change to the history chain and
	// returns the stored row identifier, kept for later attribution.
	// Losing a concurrent append race reports ErrVersionConflict.
	CreatePositionChange(ctx context.Context, change domain.PositionChange) (string, error)

	// UpdateCurrentPosition moves the projection to the change target under
	// the optimistic version check. A stale expected version reports
	// ErrVersionConflict and a missing projection reports
	// ErrPositionNotFound.
	UpdateCurrentPosition(ctx context.Context, change domain.PositionChange, expectedVersion int32) error

	// ListPositionChanges returns the account's change history in one
	// Arena, newest first.
	ListPositionChanges(ctx context.Context, arenaID domain.ArenaID, accountID domain.AccountID) ([]PositionChangeRecord, error)
}

// PositionDistribution is the count of one dimension of an aggregate: how
// many participants confirmed each position. It carries no account
// identifier by construction.
type PositionDistribution struct {
	Agree     int64
	Disagree  int64
	Undecided int64
}

// Total returns the population of the distribution.
func (d PositionDistribution) Total() int64 {
	return d.Agree + d.Disagree + d.Undecided
}

// PositionAggregateRepository is the consumer-oriented port of the public
// aggregate projection. It returns counts only: account identifiers never
// leave the database.
type PositionAggregateRepository interface {
	// CountEligiblePositions counts the confirmed positions of one Arena
	// over eligible accounts, split by initial and current choice. The
	// adapter excludes accounts that are not active, per
	// docs/BUSINESS_RULES.md §7.
	CountEligiblePositions(ctx context.Context, arenaID domain.ArenaID) (PositionDistribution, PositionDistribution, error)
}
