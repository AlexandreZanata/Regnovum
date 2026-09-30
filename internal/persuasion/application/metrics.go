package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// ArgumentMetrics is the public count projection of one argument: the valid
// attribution events it received and the eligible people who credited it. The
// attributor identity is private by default (BR §5.1), so counts only — never
// an account identifier, a handle or an email — cross this projection.
type ArgumentMetrics struct {
	ArgumentID domain.ArgumentID
	// DistinctPeople counts eligible attributors once per argument (BR §5.1,
	// §7), so repeated attributions by the same person never inflate the
	// count. Eligibility is a historical fact: only attributions made while
	// the account was active with a verified email are visible through the
	// port.
	DistinctPeople int64
	// ValidAttributions counts valid attribution events, so the additional
	// events of a person already counted can be shown separately (BR §6).
	// Invalidated attributions never integrate it (BR §6, REQ-PERS-08).
	ValidAttributions int64
	// CheckedAt is the derivation instant reported to clients so public
	// numbers carry when they were computed (BR §7).
	CheckedAt time.Time
}

// Validate checks the coherence of a count projection before it is published:
// a malformed projection silently inflating a public number is refused
// instead of served. The rules are the same ones the reputation projection
// enforces, so every adapter is bound by them.
func (m ArgumentMetrics) Validate() error {
	if m.ArgumentID.IsZero() {
		return refusal("argument identifier is missing")
	}
	if m.CheckedAt.IsZero() {
		return refusal("derivation instant is missing")
	}
	if m.DistinctPeople < 0 || m.ValidAttributions < 0 {
		return refusal("argument " + m.ArgumentID.String() + " carries a negative count")
	}
	if m.DistinctPeople > m.ValidAttributions {
		return refusal("argument " + m.ArgumentID.String() + " counts more people than events")
	}
	return nil
}

// ArgumentMetricsRepository derives the public count facts of one argument.
// Counts only cross the port: attributor identifiers never leave the
// database.
type ArgumentMetricsRepository interface {
	// GetArgumentMetrics returns the counts of the argument. An argument with
	// no eligible attribution returns zeroed counts; an identifier that
	// addresses no argument reports ErrArgumentNotFound.
	GetArgumentMetrics(ctx context.Context, argumentID domain.ArgumentID) (*ArgumentMetrics, error)
}

// AuthorHandle is the resolved public identity of an author: the internal
// identity the metrics are keyed by plus the canonical username clients
// address. The identifier stays inside the module — public documents are
// addressed by the username alone.
type AuthorHandle struct {
	AuthorID domain.AuthorID
	Username string
}

// AuthorDirectory resolves a public username to the author who owns it. It is
// a read-only directory: no profile field crosses the port, only the resolved
// identity does.
type AuthorDirectory interface {
	// ResolveAuthor resolves the username (compared under the canonical
	// normalized form, as every profile lookup does) to its author. A
	// username that owns no profile reports ErrProfileNotFound.
	ResolveAuthor(ctx context.Context, username string) (AuthorHandle, error)
}
