package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// Clock exposes wall-clock time to profiles use cases, keeping the domain
// and the application deterministic under test (ADR-012).
type Clock interface {
	Now() time.Time
}

// Random exposes cryptographically secure random bytes to profiles use
// cases, keeping randomness an injected effect (ADR-012). It mirrors the
// minimal reader the identity module uses.
type Random interface {
	// Read fills buffer with cryptographically secure random bytes and
	// returns the number of bytes written, or an error when the entropy
	// source fails. It must not return fewer bytes with a nil error.
	Read(buffer []byte) (int, error)
}

// UnitOfWork runs a function inside one database transaction. The deletion
// workflow uses it so the state transition and its audit record commit or
// roll back together. The concrete manager is composed at bootstrap; the
// module never imports another module's adapters.
type UnitOfWork interface {
	// WithinTransaction begins a transaction, makes it available to
	// participants through the context and commits only when fn returns nil.
	WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// ProfileRepository persists profiles and their auditable username history.
// Writes that combine a profile mutation with an audit entry must be atomic.
type ProfileRepository interface {
	// CreateProfileWithUsernameHistory atomically creates the profile and its
	// first username_history entry. It returns ErrUsernameTaken when the
	// normalized username is already claimed and ErrProfileAlreadyExists when
	// the account already owns a profile.
	CreateProfileWithUsernameHistory(ctx context.Context, accountID domain.AccountID, username domain.Username, locale domain.Locale, changedAt time.Time) (*domain.Profile, error)

	// GetProfileByAccountID retrieves the profile owned by an account.
	GetProfileByAccountID(ctx context.Context, accountID domain.AccountID) (*domain.Profile, error)

	// LastUsernameChangeAt returns the most recent username audit instant, or
	// the zero time when the account has no history yet.
	LastUsernameChangeAt(ctx context.Context, accountID domain.AccountID) (time.Time, error)

	// ApplyUsernameChange atomically applies a planned username change to the
	// profile and appends the audit entry. It returns ErrUsernameTaken when
	// the proposed handle is already claimed.
	ApplyUsernameChange(ctx context.Context, accountID domain.AccountID, change domain.UsernameChange) (*domain.Profile, error)

	// UpdateProfileLocale replaces the interface locale preference.
	UpdateProfileLocale(ctx context.Context, accountID domain.AccountID, locale domain.Locale, updatedAt time.Time) (*domain.Profile, error)

	// UpdateProfileTimezone replaces the optional IANA timezone preference;
	// the zero timezone clears it.
	UpdateProfileTimezone(ctx context.Context, accountID domain.AccountID, timezone domain.Timezone, updatedAt time.Time) (*domain.Profile, error)
}

// AccountEligibility is the consumer-oriented port that answers whether an
// account may own or mutate a profile. The PostgreSQL adapter implements it
// against the minimal account status/verification projection; tests provide
// fakes. It never exposes account data beyond eligibility.
type AccountEligibility interface {
	// EnsureEligible returns ErrAccountNotEligible when the account is
	// missing, unverified, suspended or deleted.
	EnsureEligible(ctx context.Context, accountID domain.AccountID) error
}
