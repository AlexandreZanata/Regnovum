package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// ModerationAuthorizer verifies that the acting account may moderate
// attributions. The persuasion module never decides roles: the
// identity/moderation adapter answers this consumer-oriented port.
type ModerationAuthorizer interface {
	// EnsureModerator returns ErrNotAuthorized when the actor may not
	// moderate attributions.
	EnsureModerator(ctx context.Context, actor domain.ModeratorID) error
}

// AttributionModerationRepository reads and rewrites the validity of one
// attribution. Implementations never delete rows: the decision record is
// written on the retained attribution.
type AttributionModerationRepository interface {
	// LockAttributionForModeration loads the attribution with its current
	// validity and latest decision and locks it FOR UPDATE, so concurrent
	// decisions on the same row serialize instead of overwriting each other.
	// It must be called inside the shared transaction. A missing attribution
	// reports ErrAttributionNotFound.
	LockAttributionForModeration(ctx context.Context, attributionID domain.AttributionID) (*domain.Attribution, error)

	// ApplyModerationDecision writes the decision and moves the validity to
	// the action target. The update is guarded by the validity the action
	// requires (valid to invalid for invalidation, invalid to valid for
	// restoration); zero affected rows report ErrModerationConflict, meaning
	// a concurrent decision moved the validity since it was read.
	ApplyModerationDecision(ctx context.Context, attributionID domain.AttributionID, decision domain.ModerationDecision) (*domain.Attribution, error)
}

// AttributionAuditRecorder persists the audit event of an applied
// attribution moderation decision. Implementations must write through the
// transaction carried by the context when there is one, so the decision and
// its audit record commit or roll back together and no invalidation can be
// recorded silently. Only applied decisions are reported: a replay of an
// existing decision produces no second event.
type AttributionAuditRecorder interface {
	RecordAttributionModeration(ctx context.Context, event AttributionModerationEvent) error
}

// AttributionModerationEvent is the audit record of one applied moderation
// decision. The retained attribution row already carries the same facts
// (reason, actor, instant); this event feeds the platform audit trail.
// The attributor identity is deliberately absent: it is private by default
// and the decision never needs it.
type AttributionModerationEvent struct {
	ActorAccountID domain.ModeratorID
	AttributionID  domain.AttributionID
	ArgumentID     domain.ArgumentID
	Action         domain.ModerationAction
	Reason         domain.Reason
	OccurredAt     time.Time
}

// AttributionModerationResult is the outcome of a moderation action: the
// attribution with its current validity, and whether the call resolved an
// already applied decision instead of writing.
type AttributionModerationResult struct {
	Attribution domain.Attribution
	Replayed    bool
}
