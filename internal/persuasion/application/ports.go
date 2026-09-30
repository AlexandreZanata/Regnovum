package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// Clock exposes wall-clock time to persuasion use cases, keeping them
// deterministic under test (ADR-012).
type Clock interface {
	Now() time.Time
}

// UnitOfWork runs a function inside one database transaction. Recording
// attributions uses it so the set-level checks and the inserts commit or
// roll back together, and the change row lock serializes concurrent
// requests.
type UnitOfWork interface {
	// WithinTransaction begins a transaction, makes it available to
	// participants through the context and commits only when fn returns nil.
	WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// AttributionRepository persists attributions and provides the eligibility
// inputs.
type AttributionRepository interface {
	// LockChangeForAttributor loads the position change scoped to its
	// account and locks it FOR UPDATE. It must be called inside the shared
	// transaction. A missing or foreign change reports ErrChangeNotFound.
	LockChangeForAttributor(ctx context.Context, changeID domain.ChangeID, attributorID domain.AttributorID) (*domain.Change, error)

	// ListAttributedArgumentIDs returns the arguments already credited by
	// the change.
	ListAttributedArgumentIDs(ctx context.Context, changeID domain.ChangeID) ([]domain.ArgumentID, error)

	// ListCandidates loads the eligibility inputs of the proposed
	// arguments. Any unknown identifier reports ErrArgumentNotFound.
	ListCandidates(ctx context.Context, argumentIDs []domain.ArgumentID) ([]domain.Candidate, error)

	// CreateAttributions records the accepted candidates under the unique
	// (change, argument) pair; existing pairs are left untouched.
	CreateAttributions(ctx context.Context, changeID domain.ChangeID, attributorID domain.AttributorID, candidates []domain.Candidate) error
}
