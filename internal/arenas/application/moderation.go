package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// ModerationAction is the stable vocabulary of Arena moderation actions.
type ModerationAction string

const (
	// ModerationRestrict limits interaction while keeping the Arena
	// accessible under a recorded decision.
	ModerationRestrict ModerationAction = "RESTRICT"

	// ModerationRemove takes the Arena out of the public surface; removed is
	// terminal in the MVP.
	ModerationRemove ModerationAction = "REMOVE"
)

// ModerationEvent is the audit record of one Arena moderation action. The
// Arena row already carries the resulting status; this event feeds the
// administrative audit trail.
type ModerationEvent struct {
	ActorAccountID domain.ModeratorID
	ArenaID        domain.ArenaID
	Action         ModerationAction
	Reason         domain.Reason
	Replayed       bool
	OccurredAt     time.Time
}

// ModerationAuthorizer verifies that the acting account may moderate Arenas.
// The arenas module never decides roles: the identity/moderation adapter
// answers this consumer-oriented port.
type ModerationAuthorizer interface {
	// EnsureModerator returns ErrNotAuthorized when the actor may not
	// moderate Arenas.
	EnsureModerator(ctx context.Context, actor domain.ModeratorID) error
}

// ModerationAuditRecorder persists Arena moderation audit events.
// Implementations must be idempotent by (Arena, Action): the use cases
// report both fresh and replayed outcomes so a retry can repair a lost
// record without duplicating it.
type ModerationAuditRecorder interface {
	RecordArenaModeration(ctx context.Context, event ModerationEvent) error
}

// ModerationResult is the outcome of a moderation action.
type ModerationResult struct {
	Arena    domain.Arena
	Replayed bool
}
